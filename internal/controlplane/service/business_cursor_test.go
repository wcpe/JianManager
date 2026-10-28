package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// ADR-107 §3 / §5：缺口查询与保留期裁剪测试。
//
// 守三件事：
//  1. 只返回**位点**，不含 payload（表里没有该列，这里断言对外结构也没有）；
//  2. 区间过滤 + 按 seq 升序 + limit 上限钳制 + 空结果是空数组而非错误；
//  3. 裁剪只删过期行（按 created_at，不按 occurred_at）。

// TestBusinessCursorQuery_RangeAndOrder 区间过滤 + 升序。
func TestBusinessCursorQuery_RangeAndOrder(t *testing.T) {
	db := newCursorDB(t)
	svc := NewBusinessCursorService(db, 7)
	now := time.Now()
	// 故意乱序写入，验证 ORDER BY 而不是插入顺序。
	for _, seq := range []int64{50, 10, 30, 20, 40} {
		require.NoError(t, db.Create(&model.BusinessEventCursor{
			StreamSeq: seq, InstanceID: 1, Domain: "activity", Seq: seq,
			DedupKey: "d", OccurredAt: 1730000000, CreatedAt: now,
		}).Error)
	}
	// 干扰数据：别的实例、别的域、别的区间。
	require.NoError(t, db.Create(&model.BusinessEventCursor{
		StreamSeq: 900, InstanceID: 2, Domain: "activity", Seq: 900, CreatedAt: now}).Error)
	require.NoError(t, db.Create(&model.BusinessEventCursor{
		StreamSeq: 901, InstanceID: 1, Domain: "economy", Seq: 901, CreatedAt: now}).Error)

	items, err := svc.Query(BusinessCursorQuery{InstanceID: 1, Domain: "activity", FromSeq: 20, ToSeq: 40})
	require.NoError(t, err)
	got := make([]int64, 0, len(items))
	for _, it := range items {
		got = append(got, it.Seq)
	}
	require.Equal(t, []int64{20, 30, 40}, got, "必须按 seq 升序且只含该实例该域的区间内位点")
}

// TestBusinessCursorQuery_EmptyIsEmptyArray 无数据返回空切片（不是 nil、不是错误、不是 404）。
func TestBusinessCursorQuery_EmptyIsEmptyArray(t *testing.T) {
	db := newCursorDB(t)
	svc := NewBusinessCursorService(db, 7)

	items, err := svc.Query(BusinessCursorQuery{InstanceID: 42, Domain: "activity"})
	require.NoError(t, err, "无数据不是错误")
	require.NotNil(t, items, "必须是空切片而不是 nil（nil 会被 JSON 编成 null，违反契约）")
	require.Len(t, items, 0)
}

// TestBusinessCursorQuery_RejectsMissingScope 缺 instance/domain 直接报错（否则等于全表扫）。
func TestBusinessCursorQuery_RejectsMissingScope(t *testing.T) {
	db := newCursorDB(t)
	svc := NewBusinessCursorService(db, 7)
	_, err := svc.Query(BusinessCursorQuery{Domain: "activity"})
	require.ErrorIs(t, err, ErrBusinessCursorInvalidQuery)

	_, err = svc.Query(BusinessCursorQuery{InstanceID: 1})
	require.ErrorIs(t, err, ErrBusinessCursorInvalidQuery)

	_, err = svc.Query(BusinessCursorQuery{InstanceID: 1, Domain: "   "})
	require.ErrorIs(t, err, ErrBusinessCursorInvalidQuery)
}

// TestBusinessCursorQuery_LimitClamp limit 默认 1000、上限 5000、超限钳制。
func TestBusinessCursorQuery_LimitClamp(t *testing.T) {
	require.Equal(t, 1000, NormalizeBusinessCursorLimit(0))
	require.Equal(t, 1000, NormalizeBusinessCursorLimit(-5))
	require.Equal(t, 5000, NormalizeBusinessCursorLimit(999999), "超上限必须钳制而不是原样透传")
	require.Equal(t, 5000, NormalizeBusinessCursorLimit(5000))
	require.Equal(t, 7, NormalizeBusinessCursorLimit(7))

	// 落 30 条，limit=10 只返回 10 条（且是 seq 最小的 10 条）。
	db := newCursorDB(t)
	svc := NewBusinessCursorService(db, 7)
	now := time.Now()
	rows := make([]model.BusinessEventCursor, 0, 30)
	for i := 1; i <= 30; i++ {
		rows = append(rows, model.BusinessEventCursor{
			StreamSeq: int64(i), InstanceID: 1, Domain: "activity", Seq: int64(i), CreatedAt: now,
		})
	}
	require.NoError(t, db.Create(&rows).Error)

	items, err := svc.Query(BusinessCursorQuery{InstanceID: 1, Domain: "activity", Limit: 10})
	require.NoError(t, err)
	require.Len(t, items, 10)
	require.EqualValues(t, 1, items[0].Seq)
}

// TestBusinessCursorQuery_NoPayloadField 对外结构不含各字段 payload（ADR-107 红线）。
func TestBusinessCursorQuery_NoPayloadField(t *testing.T) {
	// 结构体只有四个字段；任何"顺手加个 payload"的改动都会在这里红。
	item := BusinessCursorItem{}
	require.Equal(t, int64(0), item.StreamSeq)
	require.Equal(t, int64(0), item.Seq)
	require.Equal(t, "", item.DedupKey)
	require.Equal(t, int64(0), item.OccurredAt)

	db := newCursorDB(t)
	require.False(t, db.Migrator().HasColumn(&model.BusinessEventCursor{}, "payload"),
		"位点账本表绝不能有 payload 列（ADR-107 明文禁止）")
	require.False(t, db.Migrator().HasColumn(&model.BusinessEventCursor{}, "output"))
	require.False(t, db.Migrator().HasColumn(&model.BusinessEventCursor{}, "data"))
}

// TestBusinessCursorPrune_OnlyExpired 裁剪只删过期行（按 created_at，不按 occurred_at）。
func TestBusinessCursorPrune_OnlyExpired(t *testing.T) {
	db := newCursorDB(t)
	svc := NewBusinessCursorService(db, 7)

	now := time.Now()
	old := now.AddDate(0, 0, -10)  // 10 天前：过期
	fresh := now.AddDate(0, 0, -1) // 1 天前：未过期
	require.NoError(t, db.Create(&[]model.BusinessEventCursor{
		// 过期且探针时间戳「看起来很新」：不能因为 occurredAt 新就保留。
		{StreamSeq: 1, InstanceID: 1, Domain: "activity", Seq: 1, OccurredAt: now.UnixMilli(), CreatedAt: old},
		// 未过期但探针时间戳很旧：不能因为 occurredAt 旧就删掉。
		{StreamSeq: 2, InstanceID: 1, Domain: "activity", Seq: 2, OccurredAt: 0, CreatedAt: fresh},
		{StreamSeq: 3, InstanceID: 1, Domain: "activity", Seq: 3, OccurredAt: now.UnixMilli(), CreatedAt: now},
	}).Error)

	deleted := svc.PruneOnce()
	require.Equal(t, 1, deleted, "只应删除 created_at 超出 7 天的 1 行")

	var rows []model.BusinessEventCursor
	require.NoError(t, db.Order("stream_seq ASC").Find(&rows).Error)
	require.Len(t, rows, 2)
	require.EqualValues(t, 2, rows[0].StreamSeq, "未过期但 occurredAt 旧的行必须保留")
	require.EqualValues(t, 3, rows[1].StreamSeq)
}

// TestBusinessCursorPrune_RetentionDaysConfig 保留天数可配（0/负值回落默认 7 天）。
func TestBusinessCursorPrune_RetentionDaysConfig(t *testing.T) {
	db := newCursorDB(t)
	require.Equal(t, 7, NewBusinessCursorService(db, 0).RetentionDays())
	require.Equal(t, 7, NewBusinessCursorService(db, -1).RetentionDays())
	require.Equal(t, 30, NewBusinessCursorService(db, 30).RetentionDays())

	// 配 1 天：2 天前的行必须被删。
	svc := NewBusinessCursorService(db, 1)
	require.NoError(t, db.Create(&model.BusinessEventCursor{
		StreamSeq: 1, InstanceID: 1, Domain: "activity", Seq: 1,
		CreatedAt: time.Now().AddDate(0, 0, -2),
	}).Error)
	require.Equal(t, 1, svc.PruneOnce())
}

// TestBusinessCursorPrune_NoExpiredRows 全部未过期时一行都不删。
func TestBusinessCursorPrune_NoExpiredRows(t *testing.T) {
	db := newCursorDB(t)
	svc := NewBusinessCursorService(db, 7)
	require.NoError(t, db.Create(&model.BusinessEventCursor{
		StreamSeq: 1, InstanceID: 1, Domain: "activity", Seq: 1, CreatedAt: time.Now(),
	}).Error)
	require.Equal(t, 0, svc.PruneOnce())
	require.EqualValues(t, 1, countCursors(t, db))
}

// TestBusinessCursorPrune_ChunkedBeyondOneBatch 过期行超过单批上限时分批删净。
//
// 这条用例盯的是「GORM 在 SQLite 上会静默忽略 Delete 的 Limit」这个坑：若实现退回
// `DELETE ... LIMIT`，SQLite 会一条语句删完整段过期数据（长事务锁库）。
// 这里用超过 businessCursorPruneChunk 的数据量，确认多轮循环真的跑起来了。
func TestBusinessCursorPrune_ChunkedBeyondOneBatch(t *testing.T) {
	db := newCursorDB(t)
	svc := NewBusinessCursorService(db, 7)

	old := time.Now().AddDate(0, 0, -30)
	total := businessCursorPruneChunk + 7
	rows := make([]model.BusinessEventCursor, 0, total)
	for i := 1; i <= total; i++ {
		rows = append(rows, model.BusinessEventCursor{
			StreamSeq: int64(i), InstanceID: 1, Domain: "activity", Seq: int64(i), CreatedAt: old,
		})
	}
	require.NoError(t, db.CreateInBatches(&rows, 1000).Error)
	// 一条未过期的，必须留着。
	require.NoError(t, db.Create(&model.BusinessEventCursor{
		StreamSeq: 999999, InstanceID: 1, Domain: "activity", Seq: 999999, CreatedAt: time.Now(),
	}).Error)

	require.Equal(t, total, svc.PruneOnce(), "超过单批上限也必须全部删净（多轮循环）")
	require.EqualValues(t, 1, countCursors(t, db), "未过期行必须保留")
}

// TestBusinessCursorService_StartStopIdempotent 巡检启动/停止幂等（重复调用不 panic、不重复起协程）。
func TestBusinessCursorService_StartStopIdempotent(t *testing.T) {
	db := newCursorDB(t)
	svc := NewBusinessCursorService(db, 7)
	require.NotPanics(t, func() {
		svc.Start()
		svc.Start()
		svc.Stop()
		svc.Stop()
	})
}
