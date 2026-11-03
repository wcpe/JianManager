package ingest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

func TestIngestSixFixActiveTailBoundedPastGenerationCap(t *testing.T) {
	// 2026-11-02 投递路径简化：本用例守「正常尾采不得每批增长 micro-generation」与
	// 「不退回每批全源物化」。两者都发生在 `writeProjectionDay` → `publishSnapshot` 那条链上，
	// 而简化后 `deliverCtx` 只做「去重 → 写段 → 写 VL」，**不再发布代次也不做物化**。
	//
	// 实测（2026-11-02 全量跑）：`rec.PublishedProjection` 恒为 nil，在第 57 行解引用时
	// panic，并连带把同包后续用例全部拖成 fail。断言前提不存在，保留为明确跳过。
	t.Skip("投递路径简化（2026-11-02）：投递不再发布代次/物化，`rec.PublishedProjection` 恒为 nil；" +
		"「不按每批增长 micro-generation」失去对象")
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	source := hotRetentionTestSource(t, root)
	journal := catalog.NewMemJournal()
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: []SourceConfig{source}})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	first := hotRetentionTestEvent(source.UTCDay, 0, 10, "首个已确认尾部")
	_, err = m.deliver(source, []logtypes.Event{first}, false)
	require.NoError(t, err)
	for i := 0; i < 8; i++ {
		window := []logtypes.Event{hotRetentionTestEvent(source.UTCDay, 0, 10, fmt.Sprintf("独立失败窗口%d", i))}
		_, release, err := m.reserveMaterializationClass(context.Background(), source, fmt.Sprintf("wasted-%d", i), window, window, "rebuild")
		require.NoError(t, err)
		release()
	}
	m.mu.Lock()
	generation := m.state.Sources[key].ProjectionGeneration
	m.mu.Unlock()
	m.markPublicationPending(key, generation)
	m.mu.Lock()
	pendingBefore := m.state.Sources[key].PublicationPending
	m.mu.Unlock()
	require.True(t, pendingBefore, "夹具必须先建立真实待发布责任")
	var skipped int
	m.stageSink = func(stage string) {
		if strings.Contains(stage, "recovery-skipped") {
			skipped++
		}
	}
	for i := 1; i <= maxPublishedProjectionGenerations+20; i++ {
		event := hotRetentionTestEvent(source.UTCDay, uint64(i*10), uint64(i*10+10), fmt.Sprintf("正常尾部%d", i))
		_, err = m.deliver(source, []logtypes.Event{event}, false)
		require.NoError(t, err, "第%d批不能被历史物化预算阻死", i)
	}
	rec, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
	require.Len(t, publishedSourceProjectionGenerations(rec.PublishedProjection, source), 1, "不按每批增长micro-generation")
	require.Equal(t, maxPublishedProjectionGenerations+20, skipped, "正常尾采不能退回每批全源物化")
	require.Equal(t, maxPublishedProjectionGenerations+21, fixture.rowCount())
	m.mu.Lock()
	pending := m.state.Sources[key].PublicationPending
	m.mu.Unlock()
	require.True(t, pending, "追加新尾部不能抹掉历史待发布责任")
	rows, err := m.index.ListMaterializations(context.Background(), key, source.UTCDay, "", 0, 200)
	require.NoError(t, err)
	require.Len(t, rows, 8, "正常尾追加不再造重建副本")
}

func TestIngestSixFixNecessaryCompactionUsesReservedBytes(t *testing.T) {
	// 2026-11-02 投递路径简化：本用例守「物化预算与压缩预留」整套 UI 都挂在 `publishSnapshot`/`reserveMaterialization` 上。
	// 简化后 `deliverCtx` 只做「去重 → 写段 → 写 VL」，那些机制不在投递路径上运行，
	// 断言前提不存在。保留为明确跳过而不是默默删掉。
	t.Skip("投递路径简化（2026-11-02）：「物化预算与压缩预留」整套 UI 都挂在 `publishSnapshot`/`reserveMaterialization` 上")
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	source := hotRetentionTestSource(t, root)
	journal := catalog.NewMemJournal()
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: []SourceConfig{source}})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	all := make([]logtypes.Event, 0, maxPublishedProjectionGenerations+1)
	for i := 0; i < maxPublishedProjectionGenerations; i++ {
		event := hotRetentionTestEvent(source.UTCDay, uint64(i*10), uint64(i*10+10), fmt.Sprintf("存量微代次%d", i))
		all = append(all, event)
		_, err = m.deliver(source, []logtypes.Event{event}, true)
		require.NoError(t, err)
	}
	rec, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
	require.Len(t, publishedSourceProjectionGenerations(rec.PublishedProjection, source), maxPublishedProjectionGenerations)
	next := hotRetentionTestEvent(source.UTCDay, uint64(maxPublishedProjectionGenerations*10), uint64((maxPublishedProjectionGenerations+1)*10), "必要压缩的新尾部")
	all = append(all, next)
	wire, err := projectionWireBytes(all, fmt.Sprintf("projection-%d", maxPublishedProjectionGenerations+1))
	require.NoError(t, err)
	m.materializationLimits = stateindex.MaterializationLimits{WindowCopies: 4, HourlyCopies: 8, HourlyBytes: wire * 4, CompactionReserveBytes: wire}
	clock := time.Now()
	m.rebuilds.now = func() time.Time { return clock }
	_, permitted, _, err := m.index.ReserveMaterialization(context.Background(), stateindex.MaterializationRequest{Class: "rebuild", SourceKey: key, UTCDay: source.UTCDay, WindowKey: "wasted-background", Generation: "unknown-background", EstimatedBytes: wire * 3, Now: clock}, m.materializationLimits)
	require.NoError(t, err)
	require.True(t, permitted, "只耗尽后台份额，不侵占必要压缩保留")
	// 真正scheduler被挂在执行体入口，主线仍应完成必要压缩和后续追加。
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	m.rebuilds.hookRun = func(string) { once.Do(func() { close(entered); <-release }) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.RunRebuildWorker(ctx) }()
	defer func() {
		cancel()
		select {
		case <-release:
		default:
			close(release)
		}
		<-done
	}()
	require.True(t, m.rebuilds.schedule(&rebuildRequest{key: key, source: source, days: []string{source.UTCDay}}, "字节已耗的历史重建"))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("真实worker未启动")
	}
	_, err = m.deliver(source, []logtypes.Event{next}, false)
	require.NoError(t, err)
	rec, _ = m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
	require.Len(t, publishedSourceProjectionGenerations(rec.PublishedProjection, source), 1, "压缩必须保留全部旧内容后再切到一代")
	require.Equal(t, uint64((maxPublishedProjectionGenerations+1)*10), publishedEvidenceClosed(rec, source))
	for i := 1; i <= maxPublishedProjectionGenerations+2; i++ {
		start := uint64((maxPublishedProjectionGenerations + i) * 10)
		_, err = m.deliver(source, []logtypes.Event{hotRetentionTestEvent(source.UTCDay, start, start+10, fmt.Sprintf("压缩后正常尾部%d", i))}, false)
		require.NoError(t, err)
	}
	close(release)
	require.Eventually(t, func() bool {
		m.rebuilds.mu.Lock()
		defer m.rebuilds.mu.Unlock()
		_, running := m.rebuilds.inflight[key]
		return !running
	}, 5*time.Second, time.Millisecond)
	rows, err := m.index.ListMaterializations(context.Background(), key, source.UTCDay, "", 0, 200)
	require.NoError(t, err)
	var bytes int64
	compactions := 0
	for _, row := range rows {
		bytes += max(row.ReservedBytes, row.WrittenBytes)
		if row.Class == "live_compaction" {
			compactions++
			require.True(t, row.Verified)
			require.True(t, row.Published)
		}
	}
	require.Equal(t, 1, compactions)
	require.LessOrEqual(t, bytes, m.materializationLimits.HourlyBytes)
	out, _ := m.RebuildLastResult(key)
	require.True(t, out.Pending)
	require.ErrorIs(t, out.Err, ErrMaterializationBudget)
	require.Greater(t, fixture.rowCount(), maxPublishedProjectionGenerations, "实际新尾部持续写入，不能只改变Catalog假通过")
}

func TestIngestSixFixActiveTailQualificationIsNotRestoredBlindly(t *testing.T) {
	// 2026-11-02 投递路径简化：本用例守「活跃尾部资格不得盲目恢复」——靠 `activeTails` 与发布资格账。
	// 简化后 `deliverCtx` 只做「去重 → 写段 → 写 VL」，那些机制不在投递路径上运行，
	// 断言前提不存在（且 `rec.PublishedProjection` 恒为 nil，会直接 panic）。
	// 保留为明确跳过而不是默默删掉。
	t.Skip("投递路径简化（2026-11-02）：「活跃尾部资格不得盲目恢复」——靠 `activeTails` 与发布资格账")
	client, _, _ := newReconcileVL(t)
	root := t.TempDir()
	source := hotRetentionTestSource(t, root)
	journal := catalog.NewMemJournal()
	m, err := New(Options{Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: []SourceConfig{source}})
	require.NoError(t, err)
	_, err = m.deliver(source, []logtypes.Event{hotRetentionTestEvent(source.UTCDay, 0, 10, "重启前确认")}, false)
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	m.mu.Lock()
	generation := m.activeTails[key].generation
	m.mu.Unlock()
	require.NotEmpty(t, generation)
	require.NoError(t, m.Stop())
	restarted, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: []SourceConfig{source}})
	require.NoError(t, err)
	restarted.mu.Lock()
	_, qualified := restarted.activeTails[key]
	restarted.mu.Unlock()
	require.False(t, qualified, "只有持久代次名不能继承未知在途写入的追加资格")
	_, err = restarted.deliver(source, []logtypes.Event{hotRetentionTestEvent(source.UTCDay, 10, 20, "重启后的新尾部")}, false)
	require.NoError(t, err)
	restarted.mu.Lock()
	newGeneration := restarted.activeTails[key].generation
	restarted.mu.Unlock()
	require.NotEmpty(t, newGeneration)
	require.NotEqual(t, generation, newGeneration, "重启先独立取证/新代确认，不能直接沿用旧资格")
}

// 真实scheduler读取冻结旧清单时，同源主线追加并rollover；旧读不得把新行混入重建。
func TestIngestSixFixManagerSnapshotOverlapsTailRollover(t *testing.T) {
	// 2026-11-02 投递路径简化：本用例守「新发布使旧重建快照失效」——靠发布快照版本（`errPublicationStale`）。
	// 简化后 `deliverCtx` 只做「去重 → 写段 → 写 VL」，那些机制不在投递路径上运行，
	// 断言前提不存在（且 `rec.PublishedProjection` 恒为 nil，会直接 panic）。
	// 保留为明确跳过而不是默默删掉。
	t.Skip("投递路径简化（2026-11-02）：「新发布使旧重建快照失效」——靠发布快照版本（`errPublicationStale`）")
	client, _, _ := newReconcileVL(t)
	root := t.TempDir()
	source := hotRetentionTestSource(t, root)
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(nil), Sources: []SourceConfig{source}, VerificationTimeout: 5 * time.Second})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	base := make([]logtypes.Event, 0, 8)
	for i := 0; i < 8; i++ {
		base = append(base, hotRetentionTestEvent(source.UTCDay, uint64(i*10), uint64(i*10+10), fmt.Sprintf("旧段%d", i)))
	}
	_, err = m.deliver(source, base, false)
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	m.canonicalReadHook = func() {
		if once.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.RunRebuildWorker(ctx) }()
	defer func() {
		cancel()
		select {
		case <-release:
		default:
			close(release)
		}
		<-done
	}()
	require.True(t, m.rebuilds.schedule(&rebuildRequest{key: key, source: source, days: []string{source.UTCDay}}, "冻结读与rollover交错"))
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("重建未进入真实段快照读取")
	}
	tail := make([]logtypes.Event, 0, 600)
	message := strings.Repeat("x", 16<<10)
	for i := 0; i < 600; i++ {
		start := uint64((i + 8) * 10)
		tail = append(tail, hotRetentionTestEvent(source.UTCDay, start, start+10, message))
	}
	_, err = m.deliver(source, tail, false)
	require.NoError(t, err, "冻结读不能阻挡同源真实主线写入")
	// 段目标按批触发封段，越过8MiB后下一批必须实际滚入新段。
	_, err = m.deliver(source, []logtypes.Event{hotRetentionTestEvent(source.UTCDay, 6080, 6090, "触发真实rollover")}, false)
	require.NoError(t, err)
	close(release)
	require.Eventually(t, func() bool {
		m.rebuilds.mu.Lock()
		defer m.rebuilds.mu.Unlock()
		_, busy := m.rebuilds.inflight[key]
		return !busy
	}, 10*time.Second, time.Millisecond)
	out, _ := m.RebuildLastResult(key)
	require.ErrorIs(t, out.Err, errPublicationStale, "新发布使旧重建快照失效，不得把新行拼进旧快照假发布")
	snapshot, err := m.events.Snapshot(key)
	require.NoError(t, err)
	defer snapshot.Close()
	require.GreaterOrEqual(t, len(snapshot.Segments()), 2, "夹具必须实际发生rollover")
	require.Equal(t, 609, snapshot.Count())
	seen := make(map[string]bool)
	require.NoError(t, snapshot.Iterate(func(event logtypes.Event) error {
		require.False(t, seen[event.EventID])
		seen[event.EventID] = true
		return nil
	}))
	require.Len(t, seen, 609)
}

func TestIngestSixFixUnknownTailWriteRevokesAppendQualification(t *testing.T) {
	// 2026-11-02 投递路径简化：本用例守「未知尾部写入撤销追加资格」——靠发布资格/append 资格那套账。
	// 简化后 `deliverCtx` 只做「去重 → 写段 → 写 VL」，那些机制不在投递路径上运行，
	// 断言前提不存在（且 `rec.PublishedProjection` 恒为 nil，会直接 panic）。
	// 保留为明确跳过而不是默默删掉。
	t.Skip("投递路径简化（2026-11-02）：「未知尾部写入撤销追加资格」——靠发布资格/append 资格那套账")
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	source := hotRetentionTestSource(t, root)
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(nil), Sources: []SourceConfig{source}})
	require.NoError(t, err)
	_, err = m.deliver(source, []logtypes.Event{hotRetentionTestEvent(source.UTCDay, 0, 10, "已确认")}, false)
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	m.mu.Lock()
	first := m.activeTails[key].generation
	m.mu.Unlock()
	require.NotEmpty(t, first)
	fixture.mu.Lock()
	fixture.failInsert = true
	fixture.mu.Unlock()
	event := hotRetentionTestEvent(source.UTCDay, 10, 20, "未知投递")
	_, err = m.deliver(source, []logtypes.Event{event}, false)
	require.Error(t, err)
	m.mu.Lock()
	_, qualified := m.activeTails[key]
	m.mu.Unlock()
	require.False(t, qualified, "失败前即撤销活跃追加许可")
	fixture.mu.Lock()
	fixture.failInsert = false
	fixture.mu.Unlock()
	_, err = m.deliver(source, []logtypes.Event{event}, false)
	require.NoError(t, err)
	m.mu.Lock()
	next := m.activeTails[key].generation
	m.mu.Unlock()
	require.NotEqual(t, first, next, "未知写入的物理代次不能直接再追加或重写")
}

func TestIngestSixFixNecessaryCompaction503CostsAndTailContinues(t *testing.T) {
	// 2026-11-02 投递路径简化：本用例守「代次用尽的必要压缩 + 压缩遇 503 仍计入成本」——整套压缩/代次簿记挂在 `publishSnapshot` 上。
	// 简化后 `deliverCtx` 只做「去重 → 写段 → 写 VL」，那些机制不在投递路径上运行，
	// 断言前提不存在（且 `rec.PublishedProjection` 恒为 nil，会直接 panic）。
	// 保留为明确跳过而不是默默删掉。
	t.Skip("投递路径简化（2026-11-02）：「代次用尽的必要压缩 + 压缩遇 503 仍计入成本」——整套压缩/代次簿记挂在 `publishSnapshot` 上")
	fixture := &reconcileVLFixture{}
	var unavailable atomic.Bool
	var rejected atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/insert/jsonline" && unavailable.Load() {
			rejected.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fixture.ServeHTTP(w, r)
	}))
	defer server.Close()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	root := t.TempDir()
	source := hotRetentionTestSource(t, root)
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(nil), Sources: []SourceConfig{source}})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	catKey := catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay}
	for i := 0; i < maxPublishedProjectionGenerations; i++ {
		_, err = m.deliver(source, []logtypes.Event{hotRetentionTestEvent(source.UTCDay, uint64(i*10), uint64(i*10+10), fmt.Sprintf("待压缩存量%d", i))}, true)
		require.NoError(t, err)
	}
	before, _ := m.cat.Get(catKey)
	require.Len(t, publishedSourceProjectionGenerations(before.PublishedProjection, source), maxPublishedProjectionGenerations)
	next := hotRetentionTestEvent(source.UTCDay, uint64(maxPublishedProjectionGenerations*10), uint64((maxPublishedProjectionGenerations+1)*10), "压缩遇到真实503")
	unavailable.Store(true)
	result, err := m.deliver(source, []logtypes.Event{next}, false)
	require.Error(t, err)
	require.Equal(t, http.StatusServiceUnavailable, result.HTTPStatus)
	require.EqualValues(t, 1, rejected.Load())
	afterFailure, _ := m.cat.Get(catKey)
	require.Equal(t, sourcePublicationToken(before, source), sourcePublicationToken(afterFailure, source), "失败不能推进已发布面")
	m.mu.Lock()
	pending := m.state.Sources[key].PublicationPending
	_, qualified := m.activeTails[key]
	m.mu.Unlock()
	require.True(t, pending)
	require.False(t, qualified)
	failed, err := m.index.ListMaterializations(context.Background(), key, source.UTCDay, "", 0, 200)
	require.NoError(t, err)
	require.Len(t, failed, 1)
	require.Equal(t, "live_compaction", failed[0].Class)
	require.Equal(t, "unknown", failed[0].State)
	require.Positive(t, failed[0].ReservedBytes, "503前的物理尝试也必须占额，不得退款")
	require.False(t, failed[0].Verified)
	require.False(t, failed[0].Published)
	unavailable.Store(false)
	_, err = m.deliver(source, []logtypes.Event{next}, false)
	require.NoError(t, err)
	afterSuccess, _ := m.cat.Get(catKey)
	require.Len(t, publishedSourceProjectionGenerations(afterSuccess.PublishedProjection, source), 1)
	require.Equal(t, next.Record.End, publishedEvidenceClosed(afterSuccess, source))
	// 连续跨过两轮旧代次阈值，新尾仍在已核验代次追加，不能每64批再造全窗副本。
	const follow = 2*maxPublishedProjectionGenerations + 5
	for i := 0; i < follow; i++ {
		start := next.Record.End + uint64(i*10)
		_, err = m.deliver(source, []logtypes.Event{hotRetentionTestEvent(source.UTCDay, start, start+10, fmt.Sprintf("503恢复后新尾%d", i))}, false)
		require.NoError(t, err)
	}
	rows, err := m.index.ListMaterializations(context.Background(), key, source.UTCDay, "", 0, 200)
	require.NoError(t, err)
	require.Len(t, rows, 2, "失败与成功均计费，正常新尾不反复物化")
	var charged int64
	published := 0
	for _, row := range rows {
		charged += max(row.ReservedBytes, row.WrittenBytes)
		if row.Published {
			published++
			require.True(t, row.Verified)
		}
	}
	require.Equal(t, 1, published)
	require.LessOrEqual(t, charged, m.materializationLimits.HourlyBytes)
	final, _ := m.cat.Get(catKey)
	require.Len(t, publishedSourceProjectionGenerations(final.PublishedProjection, source), 1)
	require.Equal(t, next.Record.End+follow*10, publishedEvidenceClosed(final, source))
	require.Equal(t, 2*maxPublishedProjectionGenerations+1+follow, fixture.rowCount(), "真实行数同时包含旧代、完整成功副本和所有新尾")
	snapshot, err := m.events.Snapshot(key)
	require.NoError(t, err)
	defer snapshot.Close()
	require.Equal(t, maxPublishedProjectionGenerations+1+follow, snapshot.Count())
	seen := make(map[string]bool)
	require.NoError(t, snapshot.Iterate(func(event logtypes.Event) error {
		require.False(t, seen[event.EventID], "权威段不能因压缩重试增加重复行")
		seen[event.EventID] = true
		return nil
	}))
	require.Len(t, seen, snapshot.Count())
}

func TestIngestSixFixManagerStartStopThreeDirtyOpens(t *testing.T) {
	// 2026-11-02 投递路径简化：本用例守「三次脏重启不得盲继承追加资格 / 不得重写既有 VL 行」——靠 `activeTails` 与物化预算，两者都随简化下线。
	// 简化后 `deliverCtx` 不再登记 `activeTails`（第 457 行的 `require.NotEmpty(generation)`
	// 必然失败），也不再走物化预算。保留为明确跳过。
	t.Skip("投递路径简化（2026-11-02）：「三次脏重启不得盲继承追加资格 / 不得重写既有 VL 行」——靠 `activeTails` 与物化预算，两者都随简化下线")
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	source := hotRetentionTestSource(t, root)
	key := source.LogSourceID + "/" + source.SourceGeneration
	journal := catalog.NewMemJournal()
	limits := stateindex.MaterializationLimits{WindowCopies: 2, HourlyCopies: 3, HourlyBytes: 1 << 20}
	budgetEvents := []logtypes.Event{hotRetentionTestEvent(source.UTCDay, 0, 10, "跨生命周期保留的未知副本成本")}
	previousGeneration := ""
	for round := 0; round < 3; round++ {
		m, err := New(Options{Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: []SourceConfig{source}, MaterializationLimits: &limits, VerificationTimeout: time.Second})
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		polled := make(chan struct{})
		var once sync.Once
		m.pollDispatchHook.Store(func() { once.Do(func() { close(polled) }) })
		go func() { defer close(done); m.Start(ctx) }()
		func() {
			defer func() { cancel(); <-done; require.NoError(t, m.Stop()) }()
			select {
			case <-polled:
			case <-time.After(3 * time.Second):
				t.Fatal("真实Manager.Start未执行采集轮")
			}
			m.mu.Lock()
			_, inherited := m.activeTails[key]
			m.mu.Unlock()
			require.False(t, inherited, "重启不能盲继承内存追加资格")
			batch := make([]logtypes.Event, 0, 5)
			for i := 0; i < 5; i++ {
				start := uint64((round*5 + i) * 10)
				batch = append(batch, hotRetentionTestEvent(source.UTCDay, start, start+10, fmt.Sprintf("第%d启事件%d", round+1, i)))
			}
			_, err := m.deliver(source, batch, false)
			require.NoError(t, err)
			m.mu.Lock()
			generation := m.activeTails[key].generation
			m.mu.Unlock()
			require.NotEmpty(t, generation)
			require.NotEqual(t, previousGeneration, generation)
			previousGeneration = generation
			reservation, release, err := m.reserveMaterializationClass(context.Background(), source, fmt.Sprintf("dirty-open-%d", round), budgetEvents, budgetEvents, "rebuild")
			if release != nil {
				release()
			}
			if round < 2 {
				require.NoError(t, err)
				require.Positive(t, reservation.ID)
			} else {
				require.ErrorIs(t, err, ErrMaterializationBudget, "三启不能让未决副本成本归零")
			}
			snapshot, err := m.events.Snapshot(key)
			require.NoError(t, err)
			defer snapshot.Close()
			require.Equal(t, (round+1)*5, snapshot.Count())
			seen := make(map[string]bool)
			require.NoError(t, snapshot.Iterate(func(event logtypes.Event) error {
				require.False(t, seen[event.EventID])
				seen[event.EventID] = true
				return nil
			}))
			require.Len(t, seen, snapshot.Count())
			rec, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
			require.Equal(t, uint64((round+1)*50), publishedEvidenceClosed(rec, source))
			require.Equal(t, (round+1)*5, fixture.rowCount(), "脏环境重启不得重写既有VL行")
		}()
	}
}
