package service

import (
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// ADR-107 业务事件位点账本测试。
//
// 守四件事（对应 ADR 的三条硬约束）：
//  1. 批量策略：满批即写、定时也写，不是逐条 INSERT；
//  2. 背压：队列满丢最旧并计数，**绝不阻塞**；
//  3. 失败兜底：落库失败只记 WARN + 计数，不 panic、不重试放大；
//  4. 关键回归：写入器再慢，业务事件投递路径也不被拖住。

// newCursorDB 建独立内存库并只迁移位点账本表。
func newCursorDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	// 关闭底层连接：共享内存库在最后一个连接关闭后才释放。不关就长期占着句柄，
	// 整包跑时累积数百个未释放的库，后续用例的 AutoMigrate（要写锁）会长时间等锁
	// ——表现为「单个用例秒过、整包随机卡住」，且每次卡住的用例都不一样。
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	require.NoError(t, db.AutoMigrate(&model.BusinessEventCursor{}))
	return db
}

// countCursors 统计表内行数。
func countCursors(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&model.BusinessEventCursor{}).Count(&n).Error)
	return n
}

// cursorAt 造一条位点。
func cursorAt(streamSeq int64, instanceID uint, domain string, seq int64) model.BusinessEventCursor {
	return model.BusinessEventCursor{
		StreamSeq:  streamSeq,
		InstanceID: instanceID,
		Domain:     domain,
		Seq:        seq,
		DedupKey:   "e-" + domain,
		OccurredAt: 1730000000,
		CreatedAt:  time.Now(),
	}
}

// TestBusinessCursorWriter_BatchSizeTrigger 满批即落库（不等定时器）。
func TestBusinessCursorWriter_BatchSizeTrigger(t *testing.T) {
	db := newCursorDB(t)
	// 定时器给 1 小时：能落库就只能是「满批触发」，否则本用例必然超时失败。
	w := NewBusinessCursorWriter(db, BusinessCursorWriterOptions{BatchSize: 10, FlushInterval: time.Hour})
	w.Start()
	defer w.Close()

	for i := 1; i <= 10; i++ {
		w.Enqueue(cursorAt(int64(i), 1, "activity", int64(i)))
	}
	require.Eventually(t, func() bool { return countCursors(t, db) == 10 }, 3*time.Second, 10*time.Millisecond,
		"满批必须触发一次落库")
	require.EqualValues(t, 10, w.Metrics().Written)
	require.EqualValues(t, 10, w.Metrics().Queued)
}

// TestBusinessCursorWriter_FlushIntervalTrigger 未满批时靠定时器落库。
func TestBusinessCursorWriter_FlushIntervalTrigger(t *testing.T) {
	db := newCursorDB(t)
	// 批量阈值给到 500（默认）：3 条永远攒不满，只能靠 50ms 定时器。
	w := NewBusinessCursorWriter(db, BusinessCursorWriterOptions{BatchSize: 500, FlushInterval: 50 * time.Millisecond})
	w.Start()
	defer w.Close()

	for i := 1; i <= 3; i++ {
		w.Enqueue(cursorAt(int64(i), 1, "activity", int64(i)))
	}
	require.Eventually(t, func() bool { return countCursors(t, db) == 3 }, 3*time.Second, 10*time.Millisecond,
		"未满批的位点必须由定时器落库，否则会长期驻留内存")
}

// TestBusinessCursorWriter_DropsOldestWhenFull 队列满丢最旧并计数，且不阻塞。
func TestBusinessCursorWriter_DropsOldestWhenFull(t *testing.T) {
	db := newCursorDB(t)
	// 不 Start：队列不会被消费，稳定复现「打满」。
	w := NewBusinessCursorWriter(db, BusinessCursorWriterOptions{QueueSize: 4})
	// 先占满 4 个槽位。
	for i := 1; i <= 4; i++ {
		w.Enqueue(cursorAt(int64(i), 1, "activity", int64(i)))
	}
	require.Equal(t, 4, w.Metrics().Depth)

	// 再塞 3 条：每次丢最旧，深度保持 4，dropped 累计 3。
	for i := 5; i <= 7; i++ {
		w.Enqueue(cursorAt(int64(i), 1, "activity", int64(i)))
	}
	m := w.Metrics()
	require.Equal(t, 4, m.Depth, "队列深度必须恒不超过容量")
	require.EqualValues(t, 3, m.Dropped, "每次打满都必须计一次丢弃")
	require.EqualValues(t, 7, m.Queued, "入队计数含被丢弃的（它们确实进了队列）")

	// 落库后应当只剩最旧的被丢、最新的保留：streamSeq 4,5,6,7。
	w.FlushNow()
	var rows []model.BusinessEventCursor
	require.NoError(t, db.Order("stream_seq ASC").Find(&rows).Error)
	require.Len(t, rows, 4)
	got := make([]int64, 0, len(rows))
	for _, r := range rows {
		got = append(got, r.StreamSeq)
	}
	require.Equal(t, []int64{4, 5, 6, 7}, got, "丢的必须是最旧（1,2,3），保留最新的 4 条")
}

// TestBusinessCursorWriter_CloseFlushesRemaining 停机排空剩余位点（优雅停机）。
func TestBusinessCursorWriter_CloseFlushesRemaining(t *testing.T) {
	db := newCursorDB(t)
	// 定时器给 1 小时 + 批量 500：不 Close 就一条都不会落库。
	w := NewBusinessCursorWriter(db, BusinessCursorWriterOptions{BatchSize: 500, FlushInterval: time.Hour})
	w.Start()
	for i := 1; i <= 20; i++ {
		w.Enqueue(cursorAt(int64(i), 1, "activity", int64(i)))
	}
	require.EqualValues(t, 0, countCursors(t, db), "前置条件：未触发批量/定时前不应落库")

	w.Close()
	require.EqualValues(t, 20, countCursors(t, db), "Close 必须刷出全部剩余位点")
	require.EqualValues(t, 20, w.Metrics().Written)

	// 幂等：重复 Close 不得 panic，也不得重复写。
	w.Close()
	require.EqualValues(t, 20, countCursors(t, db))
}

// TestBusinessCursorWriter_WriteFailureNoPanic 落库失败只计数，不 panic、不影响后续写入。
func TestBusinessCursorWriter_WriteFailureNoPanic(t *testing.T) {
	db := newCursorDB(t)
	w := NewBusinessCursorWriter(db, BusinessCursorWriterOptions{BatchSize: 10})
	w.Start()
	defer w.Close()

	// 把表删掉：此后所有 INSERT 都失败。
	require.NoError(t, db.Migrator().DropTable(&model.BusinessEventCursor{}))
	require.NotPanics(t, func() {
		for i := 1; i <= 10; i++ {
			w.Enqueue(cursorAt(int64(i), 1, "activity", int64(i)))
		}
		w.FlushNow()
	})
	require.Eventually(t, func() bool { return w.Metrics().Failed >= 10 }, 3*time.Second, 10*time.Millisecond,
		"失败行数必须被计数（与 Written 对照才能看出账本健康度）")
	require.EqualValues(t, 0, w.Metrics().Written)

	// 失败后写入器仍然可用：恢复表再写应当成功。
	require.NoError(t, db.AutoMigrate(&model.BusinessEventCursor{}))
	w.Enqueue(cursorAt(100, 1, "activity", 100))
	require.EqualValues(t, 1, w.FlushNow())
	require.EqualValues(t, 1, countCursors(t, db), "写失败后必须仍能继续工作")
}

// TestBusinessCursorWriter_UniqueIndexIdempotent stream_seq 唯一：重复入队不产生重复行、不整批失败。
func TestBusinessCursorWriter_UniqueIndexIdempotent(t *testing.T) {
	db := newCursorDB(t)
	w := NewBusinessCursorWriter(db, BusinessCursorWriterOptions{BatchSize: 10})
	w.Start()
	defer w.Close()

	w.Enqueue(cursorAt(7, 1, "activity", 7))
	w.Enqueue(cursorAt(7, 1, "activity", 7))
	w.FlushNow()
	require.EqualValues(t, 1, countCursors(t, db), "ON CONFLICT DO NOTHING 必须让重复 stream_seq 幂等")
	require.EqualValues(t, 0, w.Metrics().Failed, "唯一键冲突不应被算作写失败")
}

// TestBusinessStreamService_DeliveryNotBlockedBySlowWriter 关键回归：写入器慢/卡死时投递不受影响。
//
// 做法：把 writer 的 db 换成一个「每次写入都卡住」的桩不可行（*gorm.DB 是具体类型），
// 故改为**不启动消费协程**——等价于写入路径长期不可用（队列只进不出）。
// 此时扇出必须照常完成，只是 dropped 增长。
func TestBusinessStreamService_DeliveryNotBlockedBySlowWriter(t *testing.T) {
	db := newCursorDB(t)
	// 容量 8 且不 Start：队列很快打满，之后每次 Enqueue 都走「丢最旧」分支。
	w := NewBusinessCursorWriter(db, BusinessCursorWriterOptions{QueueSize: 8})
	defer w.Close()

	svc := NewBusinessStreamService(nil, NewBusinessStreamBuffer(1000, time.Minute))
	svc.SetCursorWriter(w)
	sub, err := svc.Subscribe("agent:1", BusinessStreamFilter{})
	require.NoError(t, err)

	const events = 200
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < events; i++ {
			svc.Ingest("node-1", mkEvent("inst-1", "activity", "activity.a", "e"))
			// 及时取走，避免把「订阅者队列满→判慢消费者」混进本用例的结论里。
			select {
			case <-sub.Frames():
			default:
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("写入器不可用时事件投递被阻塞——ADR-107 明确禁止")
	}

	// 事件一条不少地下发了（received 计数与缓冲位点都在推进）。
	require.EqualValues(t, events, svc.Metrics().EventsReceived)
	require.EqualValues(t, int64(events), svc.Metrics().BufferNewestSeq)
	// 位点写入器侧只剩容量上限那么多，其余记为丢弃（保守降级，可接受）。
	m := w.Metrics()
	require.Equal(t, 8, m.Depth)
	require.Greater(t, m.Dropped, uint64(0), "队列打满后必须有丢弃计数")
	require.Equal(t, uint64(events), m.Queued, "每条事件都必须尝试入队（不因打满而跳过记账尝试）")
}

// TestBusinessStreamService_RecordsCursor 扇出时把位点交给账本（含探针 per-domain seq）。
func TestBusinessStreamService_RecordsCursor(t *testing.T) {
	db := newCursorDB(t)
	w := NewBusinessCursorWriter(db, BusinessCursorWriterOptions{BatchSize: 10})
	w.Start()
	defer w.Close()

	svc := NewBusinessStreamService(nil, NewBusinessStreamBuffer(100, time.Minute))
	svc.SetCursorWriter(w)

	// 原始帧里带探针 seq：账本必须取到它（workerpb.PluginEvent 本身没有该字段）。
	raw := `{"type":"event","domain":"activity","topic":"activity.a","dedupKey":"d-1","seq":1024,"data":{"v":1}}`
	svc.Ingest("node-1", &workerpb.PluginEvent{
		InstanceUuid: "inst-uuid",
		Timestamp:    1730000001,
		Domain:       "activity",
		DedupKey:     "d-1",
		RawJson:      raw,
	})
	require.EqualValues(t, 1, w.FlushNow())

	var row model.BusinessEventCursor
	require.NoError(t, db.First(&row).Error)
	require.EqualValues(t, 1, row.StreamSeq, "streamSeq 必须与缓冲分配的位点同源")
	require.Equal(t, "activity", row.Domain)
	require.Equal(t, "d-1", row.DedupKey)
	require.EqualValues(t, 1024, row.Seq, "探针 per-domain seq 必须从原始帧信封取出")
	require.EqualValues(t, 1730000001, row.OccurredAt)
	// ADR-107 红线：绝不含 payload。表结构层面就无法存（无该列）。
	require.False(t, db.Migrator().HasColumn(&model.BusinessEventCursor{}, "payload"),
		"位点账本绝不能有 payload 列")
}

// TestBusinessStreamService_NoWriterNoop 未装配写入器时投递照常（剪裁部署/关闭账本）。
func TestBusinessStreamService_NoWriterNoop(t *testing.T) {
	svc := NewBusinessStreamService(nil, NewBusinessStreamBuffer(100, time.Minute))
	sub, err := svc.Subscribe("agent:1", BusinessStreamFilter{})
	require.NoError(t, err)
	require.NotPanics(t, func() {
		svc.Ingest("node-1", mkEvent("inst-1", "activity", "activity.a", "e"))
	})
	require.Len(t, drain(sub), 1)
}

// TestBusinessCursorWriter_ConcurrentEnqueue 并发写入不丢不重（数据竞争与环形指针正确性）。
func TestBusinessCursorWriter_ConcurrentEnqueue(t *testing.T) {
	db := newCursorDB(t)
	w := NewBusinessCursorWriter(db, BusinessCursorWriterOptions{QueueSize: 2000, BatchSize: 100})
	w.Start()
	defer w.Close()

	const goroutines, each = 8, 100
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				// streamSeq 全局唯一，避免被 ON CONFLICT 吞掉导致计数对不上。
				seq := int64(g*each + i + 1)
				w.Enqueue(cursorAt(seq, 1, "activity", seq))
			}
		}(g)
	}
	wg.Wait()
	require.Eventually(t, func() bool { return countCursors(t, db) == goroutines*each },
		10*time.Second, 20*time.Millisecond, "并发入队的位点必须全部落库")
	require.EqualValues(t, 0, w.Metrics().Dropped)
}
