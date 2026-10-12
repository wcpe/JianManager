package ingest

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"

	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"google.golang.org/grpc/metadata"
)

// 真实 HTTP 插入屏障固定旧 H 重放、新 H+B 发布、最新在途尝试三者的完成逆序。
func TestPR61OldReplayCompletionCannotClearNewPending(t *testing.T) {
	fixture := &projectionVLFixture{}
	oldEntered, oldRelease := make(chan struct{}), make(chan struct{})
	latestEntered, latestRelease := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/insert/jsonline" {
			payload, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(payload))
			if bytes.Contains(payload, []byte(`"projection_generation":"projection-2"`)) {
				close(oldEntered)
				<-oldRelease
			}
			if bytes.Contains(payload, []byte(`"projection_generation":"projection-4"`)) {
				close(latestEntered)
				<-latestRelease
			}
		}
		fixture.ServeHTTP(w, r)
	}))
	defer server.Close()
	defer func() {
		select {
		case <-oldRelease:
		default:
			close(oldRelease)
		}
		select {
		case <-latestRelease:
		default:
			close(latestRelease)
		}
	}()
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	src := SourceConfig{LogSourceID: "same", SourceGeneration: "g1", StorageNamespace: "same", UTCDay: "2026-09-28"}
	key := "same/g1"
	m := &Manager{root: t.TempDir(), vl: client, cat: catalog.New(nil), verificationTimeout: time.Second, state: persistedState{Sources: map[string]persistedSource{key: {}}}}
	defer func() {
		if m.index != nil {
			_ = m.index.Close()
		}
	}()
	h := ingestTestEvents("same", "g1", 0, 1, "h")
	_, err = m.writeProjectionPlan(src, h, "projection-1", projectionWritePlan{Write: h, Replace: true})
	require.NoError(t, err)
	oldSnapshot := m.capturePublicationSnapshot(src)
	oldDone := make(chan error, 1)
	go func() {
		_, err := m.writeProjectionPlan(src, h, "projection-2", projectionWritePlan{Write: h, Replace: true, Snapshot: oldSnapshot})
		oldDone <- err
	}()
	select {
	case <-oldEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("旧重放未进入实际插入屏障")
	}
	hb := append(append([]logtypes.Event(nil), h...), ingestTestEvents("same", "g1", 1, 1, "b")...)
	_, err = m.writeProjectionPlan(src, hb, "projection-3", projectionWritePlan{Write: hb, Replace: true})
	require.NoError(t, err)
	// 已过期建窗不得先改共享代次；实际接纳仍须由 writeProjectionPlan 完成。
	item := startupSource{source: src, key: key, events: h, snapshot: oldSnapshot}
	_, err = m.prepareReplayWindow(item, ReconcileReport{Fallback: true})
	require.NoError(t, err)
	m.mu.Lock()
	require.Equal(t, "projection-3", m.state.Sources[key].ProjectionGeneration)
	m.mu.Unlock()
	hbc := append(append([]logtypes.Event(nil), hb...), ingestTestEvents("same", "g1", 2, 1, "c")...)
	latestDone := make(chan error, 1)
	go func() {
		_, err := m.writeProjectionPlan(src, hbc, "projection-4", projectionWritePlan{Write: hbc, Replace: true})
		latestDone <- err
	}()
	select {
	case <-latestEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("最新尝试未进入插入屏障")
	}
	close(oldRelease)
	require.ErrorIs(t, <-oldDone, errPublicationStale)
	m.mu.Lock()
	saved := m.state.Sources[key]
	m.mu.Unlock()
	require.True(t, saved.PublicationPending)
	require.Equal(t, "projection-4", saved.ProjectionGeneration)
	rec, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: src.StorageNamespace, UTCDay: src.UTCDay})
	require.Equal(t, []string{"projection-3"}, publishedSourceProjectionGenerations(rec.PublishedProjection, src))
	require.Equal(t, uint64(199), rec.PublishedProjection.SourceProjections[0].ClosedVisibleSeq)
	close(latestRelease)
	require.NoError(t, <-latestDone)
}

// 真实处理轮只完成一个源就取消预算，8/16 源都应按检查点跨轮覆盖；视图和回退路径分别验证。
func TestPR61ReconcileActualRoundsVisitEverySource(t *testing.T) {
	for _, count := range []int{8, 16} {
		for _, useView := range []bool{false, true} {
			t.Run(fmt.Sprintf("sources-%d/view-%t", count, useView), func(t *testing.T) {
				client, _ := newProjectionVL(t)
				m := &Manager{vl: client, vlProbeTimeout: time.Second, vlReadyWait: time.Second, vlNotReady: map[string]string{}, cat: catalog.New(nil), pipes: map[string]*pipeline.Pipeline{}, sources: map[string]SourceConfig{}, state: persistedState{Sources: map[string]persistedSource{}}, reconcile: ReconcileConfig{Enabled: true, Timeout: time.Second, QueryTimeout: time.Second}}
				for i := 0; i < count; i++ {
					key := fmt.Sprintf("source-%02d/g1", i)
					m.pipes[key] = nil
					m.sources[key] = SourceConfig{LogSourceID: fmt.Sprintf("source-%02d", i), SourceGeneration: "g1", StorageNamespace: "shared", UTCDay: "2026-09-28"}
				}
				m.SetReconcileLoopConfig(ReconcileLoopConfig{Enabled: true, RoundBudget: time.Minute, MaxSourcesPerRound: 8})
				m.mu.Lock()
				m.publishReconcileViewLocked(true)
				m.mu.Unlock()
				visited := map[string]bool{}
				for round := 0; round < count; round++ {
					if !useView {
						m.reconcileViewSnapshot.Store((*reconcileView)(nil))
					}
					ctx, cancel := context.WithCancel(context.Background())
					m.reconcileCheckedHook = func(key string) { visited[key] = true; cancel() }
					result := m.ReconcileRoundNow(ctx)
					cancel()
					require.Equal(t, 1, result.CheckedSources, "每轮只完成一个实际检查")
				}
				require.Len(t, visited, count, "实际预算截断不得让尾部源饥饿")
			})
		}
	}
}

// 条数完全相同但正文变坏时，内容确认不能把 count 当成完整发布凭据。
func TestPR61PendingExactCountRejectsWrongContents(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	events := reconcileTestEvents()
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: []SourceConfig{source}})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	fixture.mu.Lock()
	fixture.rows[0].Message = "tampered but same count"
	fixture.mu.Unlock()
	m.mu.Lock()
	saved := m.state.Sources[key]
	saved.PublicationPending = true
	m.state.Sources[key] = saved
	m.mu.Unlock()
	confirmed, err := m.tryConfirmPendingPublication(source, key, saved)
	require.NoError(t, err)
	require.False(t, confirmed)
	m.mu.Lock()
	require.True(t, m.state.Sources[key].PublicationPending)
	m.mu.Unlock()
}

// 单轮仅一天预算时，遗留 pending 必须保留到全部缺失天内容级恢复，且下一轮不重写已修天。
func TestPR61SilentPendingContinuesAcrossDayBudget(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	events := reconcileTestEvents()
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: []SourceConfig{source}})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	fixture.deleteDay("2026-09-22")
	fixture.deleteDay("2026-09-23")
	m.replayBudget = ReplayBudget{MaxDays: 1, MaxDuration: time.Minute}
	m.mu.Lock()
	saved := m.state.Sources[key]
	saved.PublicationPending = true
	m.state.Sources[key] = saved
	m.mu.Unlock()
	first := ReconcileRoundResult{}
	m.reconcileOneCandidate(context.Background(), reconcileCandidate{key: key, source: source}, m.reconcile, &first)
	require.Empty(t, first.Errors)
	// 对账轮只登记（2026-10-09 异步化）；重建由调度器承担 —— 这里同步排空同一套执行体。
	require.Equal(t, 1, m.drainRebuilds(context.Background()), "pending 未确认必须登记一次重建")
	m.mu.Lock()
	require.True(t, m.state.Sources[key].PublicationPending, "还有缺失天时不能清全源pending")
	m.mu.Unlock()
	before := fixture.insertCount()
	second := ReconcileRoundResult{}
	m.reconcileOneCandidate(context.Background(), reconcileCandidate{key: key, source: source}, m.reconcile, &second)
	require.Empty(t, second.Errors)
	require.Equal(t, 1, m.drainRebuilds(context.Background()))
	payloads := fixture.payloadsSince(before)
	require.Len(t, payloads, 1)
	require.Contains(t, payloads[0], "day two alpha")
	require.NotContains(t, payloads[0], "day one alpha")
	m.mu.Lock()
	require.False(t, m.state.Sources[key].PublicationPending)
	m.mu.Unlock()
}

// 小周期迁移必须在首启处理全部 Incomplete，并从有凭据半库续迁后正常二启三启。
func TestPR61MigrationSmallCyclesAndRestarts(t *testing.T) {
	root := t.TempDir()
	fixture := chunkLegacyState(1, 30)
	writeLegacyState(t, root, fixture)
	path := filepath.Join(root, "var", "log", "ingest.state.json")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	m := &Manager{root: root, indexCommit: stateindex.CommitBudget{MaxRows: 3, MinRows: 1, CycleMaxRows: 8}}
	store, err := stateindex.OpenWithBudget(m.indexPath(), m.indexCommit)
	require.NoError(t, err)
	desired, err := m.stateToIndexState(fixture)
	require.NoError(t, err)
	migration, err := store.BeginMigration(context.Background(), fmt.Sprintf("%x", sha256.Sum256(data)), 1, desired)
	require.NoError(t, err)
	stats, err := store.ResumeMigration(context.Background(), migration)
	require.NoError(t, err)
	require.True(t, stats.Incomplete)
	require.NoError(t, store.Close())
	for round := 1; round <= 3; round++ {
		manager, err := New(Options{Root: root, Catalog: catalog.New(nil), IndexCommit: &m.indexCommit})
		require.NoError(t, err, "第%d次启动必须成功", round)
		rows, err := manager.index.Load()
		require.NoError(t, err)
		require.Len(t, rows.WAL, 30)
		if round == 1 {
			require.NoFileExists(t, path)
			require.FileExists(t, path+".migrated")
		}
		require.NoError(t, manager.Stop())
	}
}

// 未变化源的历史大小不应扩大 scoped 行映射的分配量；根行内容仍须与全量一致。
func TestPR61ScopedMappingSkipsUnchangedHistory(t *testing.T) {
	m := &Manager{}
	base := &persistedState{Sources: map[string]persistedSource{"quiet/g1": {Ledger: []ledger.Entry{{}}}, "changed/g1": {Ledger: []ledger.Entry{{Gaps: []ledger.Gap{{Reason: "test"}}}}}}}
	measure := func(st *persistedState, changed []string) uint64 {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for i := 0; i < 10; i++ {
			_, err := m.stateToIndexStateScoped(st, changed)
			require.NoError(t, err)
		}
		runtime.ReadMemStats(&after)
		return (after.TotalAlloc - before.TotalAlloc) / 10
	}
	small := measure(base, []string{"changed/g1"})
	quiet := base.Sources["quiet/g1"]
	quiet.Ledger[0].Gaps = make([]ledger.Gap, 50000)
	quiet.Ledger[0].DeliveryBatches = make([]ledger.DeliveryBatch, 50000)
	base.Sources["quiet/g1"] = quiet
	for _, changed := range [][]string{nil, {"changed/g1"}} {
		large := measure(base, changed)
		require.Less(t, large, small+32768, "未变化源不得生成再丢弃历史派生行")
		scoped, err := m.stateToIndexStateScoped(base, changed)
		require.NoError(t, err)
		full, err := m.stateToIndexState(base)
		require.NoError(t, err)
		require.Equal(t, full.Sources, scoped.Sources)
		require.Equal(t, full.Positions, scoped.Positions)
		require.Equal(t, full.Projections, scoped.Projections)
		require.Equal(t, full.Aux, scoped.Aux)
		require.Len(t, scoped.Gaps, len(changed))
	}
}

// 固定 D0 登记、D1 实际事件，65 个尾部小批次必须压缩且旧历史仍在发布白名单中可见。
func TestPR61CrossDayProjectionCompactionPreservesHistory(t *testing.T) {
	client, fixture := newProjectionVL(t)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, nil, 0600))
	src := SourceConfig{LogSourceID: "cross-day", SourceGeneration: "g1", StorageNamespace: "cross-day", UTCDay: "2026-09-27", Path: path, Mode: pipeline.ModeFilePrimary}
	m, err := newTestManager(t, Options{Root: root, Catalog: catalog.New(nil), VL: client, Sources: []SourceConfig{src}})
	require.NoError(t, err)
	key := src.LogSourceID + "/" + src.SourceGeneration
	p := m.pipes[key]
	all := ingestTestEvents(src.LogSourceID, src.SourceGeneration, 0, 1, "seed")
	_, err = m.writeProjectionPlan(src, all, "seed", projectionWritePlan{Write: all, Replace: true})
	require.NoError(t, err)
	for i := 1; i <= 65; i++ {
		batch := ingestTestEvents(src.LogSourceID, src.SourceGeneration, i, 1, fmt.Sprintf("batch %d", i))
		all = append(all, batch...)
		wal := make([]acquire.WALEntry, len(all))
		for j, event := range all {
			wal[j] = acquire.WALEntry{Seq: uint64(j + 1), Event: event, Appended: true, Durable: true}
		}
		require.NoError(t, p.WAL().Restore(wal))
		require.NoError(t, p.Ledger().AdvanceRead(p.Key(), batch[0].Record.End))
		require.NoError(t, p.Ledger().AdvanceDurable(p.Key(), batch[0].Record.End))
		// 真实管线在投递前先执行 DurablePersist，canonical 快照读取的是该已落库 WAL 视图。
		require.NoError(t, m.persistDurableBeforeDelivery())
		_, err = m.deliver(src, batch, false)
		require.NoError(t, err, "批次%d", i)
	}
	day := src
	day.UTCDay = "2026-09-28"
	rec, ok := m.cat.Get(catalog.PartitionKey{StorageNamespace: src.StorageNamespace, UTCDay: day.UTCDay})
	require.True(t, ok)
	generations := publishedSourceProjectionGenerations(rec.PublishedProjection, day)
	require.LessOrEqual(t, len(generations), 64)
	allowed := map[string]bool{}
	for _, g := range generations {
		allowed[g] = true
	}
	visible := map[string]bool{}
	fixture.mu.Lock()
	for _, payload := range fixture.writes {
		for _, line := range strings.Split(strings.TrimSpace(string(payload)), "\n") {
			var row struct {
				ID         string `json:"event_id"`
				Generation string `json:"projection_generation"`
			}
			if json.Unmarshal([]byte(line), &row) == nil && allowed[row.Generation] {
				visible[row.ID] = true
			}
		}
	}
	fixture.mu.Unlock()
	require.Len(t, visible, len(all), "压缩代必须承载完整canonical，不能只保最后差集")
}

// 实时 revision 在锁外重建期间改变，也必须提交实际源快照；共享镜像的守卫仍不得被绕过。
func TestPR61PersistLiveRevisionDoesNotStarveSnapshots(t *testing.T) {
	m, _, key, p := newRebuildProbeManager(t)
	require.NoError(t, p.Ledger().AdvanceRead(p.Key(), 10))
	m.rebuildSourceBuildHook.Store(func() { require.NoError(t, p.Ledger().AdvanceRead(p.Key(), 20)) })
	plan, err := m.planPersist(context.Background())
	require.NoError(t, err)
	_, err = m.commitPersist(context.Background(), plan)
	require.NoError(t, err)
	rows, err := m.index.Load()
	require.NoError(t, err)
	var read uint64
	for _, row := range rows.Positions {
		if row.Key == key {
			read = row.ReadPos
		}
	}
	require.Equal(t, uint64(20), read, "不能因 live rev 守卫变化而反复提交陈旧共享镜像")
	m.mu.Lock()
	saved := m.state.Sources[key]
	watermark := m.persistedRev[key]
	m.mu.Unlock()
	require.NotEqual(t, p.Ledger().Revision(p.Key()), watermark, "未来修订不能被本快照标 clean")
	if len(saved.Ledger) > 0 {
		require.NotEqual(t, uint64(20), saved.Ledger[0].Positions.Read, "共享镜像仍应遵守变更即弃守卫")
	}
	m.rebuildSourceBuildHook.Store((func())(nil))
}

// 逆序提交直接验证全部根行及派生行，不能只看内存修订号。
func TestPR61PersistOldPlanCannotOverwriteNew(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "partial"}[partial], func(t *testing.T) {
			m := &Manager{root: t.TempDir(), state: *migrationFixture()}
			if partial {
				m.indexCommit = stateindex.CommitBudget{CycleMaxRows: 1}
			}
			old, err := m.planPersist(context.Background())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, m.index.Close()) })
			newer := m.state.Sources["node:1/g1"]
			newer.PublicationPending = false
			newer.ProjectionGeneration = "new-generation"
			newer.Ledger[0].Gaps = append(newer.Ledger[0].Gaps, ledger.Gap{StartPos: 900, EndPos: 910, Reason: "NEW_GAP"})
			m.state.Sources["node:1/g1"] = newer
			next, err := m.planPersist(context.Background())
			require.NoError(t, err)
			incomplete, err := m.commitPersist(context.Background(), next)
			require.NoError(t, err)
			if partial {
				require.True(t, incomplete)
			}
			before, err := m.index.Load()
			require.NoError(t, err)
			_, _ = m.commitPersist(context.Background(), old)
			after, err := m.index.Load()
			require.NoError(t, err)
			require.Equal(t, before, after, "旧整计划不得改写较新已接纳计划的根行或派生行，即便较新计划仅部分提交")
		})
	}
}

// 发布校验期间同源投影变化必须拒绝旧 Replace，但其他源变化不应构成冲突。
func TestPR61PublicationScopeCannotRegress(t *testing.T) {
	m := &Manager{cat: catalog.New(nil), state: persistedState{Sources: map[string]persistedSource{}}}
	src := SourceConfig{LogSourceID: "one", SourceGeneration: "g1", StorageNamespace: "shared", UTCDay: "2026-09-28"}
	first := ingestTestEvents("one", "g1", 0, 1, "first")
	require.NoError(t, m.publish(src, "h", first, true))
	observed, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: src.StorageNamespace, UTCDay: src.UTCDay})
	newer := ingestTestEvents("one", "g1", 1, 1, "newer")
	require.NoError(t, m.publish(src, "b", newer, false))
	require.Error(t, m.publish(src, "old", first, true, observed), "旧同源白名单不得替换已发布 H+B")
	rec, _ := m.cat.Get(observed.Key)
	require.Equal(t, []string{"h", "b"}, publishedSourceProjectionGenerations(rec.PublishedProjection, src))
	other := src
	other.LogSourceID = "two"
	observed, _ = m.cat.Get(observed.Key)
	require.NoError(t, m.publish(other, "other", ingestTestEvents("two", "g1", 0, 1, "other"), false))
	require.NoError(t, m.publish(src, "next", newer, false, observed), "其他源发布必须允许合并")
}

func TestPR61PublishedWatermarkDoesNotUseLiveDurable(t *testing.T) {
	src := SourceConfig{LogSourceID: "one", SourceGeneration: "g1", StorageNamespace: "one", UTCDay: "2026-09-28"}
	m := &Manager{cat: catalog.New(nil), state: persistedState{Sources: map[string]persistedSource{"one/g1": {EventsStoredThrough: 999}}}}
	require.NoError(t, m.publish(src, "old-snapshot", ingestTestEvents("one", "g1", 0, 1, "snapshot"), true))
	rec, _ := m.cat.Get(catalog.PartitionKey{StorageNamespace: "one", UTCDay: src.UTCDay})
	require.Equal(t, uint64(99), rec.PublishedProjection.SourceProjections[0].ClosedVisibleSeq, "已发布封闭水位只能来自本次已校验快照")
}

func TestPR61SilentPendingRetriesWithoutNewInput(t *testing.T) {
	m, key, source, _, _, deleteDay := newPendingConfirmFixture(t)
	deleteDay()
	m.mu.Lock()
	saved := m.state.Sources[key]
	saved.PublicationPending = true
	m.state.Sources[key] = saved
	m.mu.Unlock()
	for round := 0; round < 2; round++ {
		result := ReconcileRoundResult{}
		m.reconcileOneCandidate(context.Background(), reconcileCandidate{key: key, source: source}, m.reconcile, &result)
		// 对账轮只登记（2026-10-09 异步化）：重建/发布确认由调度器承担，这里同步排空。
		m.drainRebuilds(context.Background())
	}
	m.mu.Lock()
	pending := m.state.Sources[key].PublicationPending
	m.mu.Unlock()
	require.False(t, pending, "VL健康后无新输入的遗留pending必须受控恢复")
}

// 空源键自动出口即使收到人工 metadata，也不得隐式裁定永久丢失。
func TestPR61AutomaticGapResolveNeverAbandons(t *testing.T) {
	m, _, key, p := newRebuildProbeManager(t)
	require.NoError(t, p.Ledger().AdvanceRead(p.Key(), 100))
	require.NoError(t, p.Ledger().AdvanceDurable(p.Key(), 100))
	require.NoError(t, p.Ledger().RecordGap(p.Key(), 10, 20, "ARCHIVE_GZIP_CORRUPT", "protected"))
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-jm-operator", "operator", "x-jm-reason", "attempt"))
	_ = m.resolveOneSourceGaps(ctx, key, p, m.sources[key])
	after := p.Ledger().Get(p.Key())
	require.Empty(t, after.Abandonments)
	require.False(t, after.Gaps[len(after.Gaps)-1].Resolved)
}

// 正文缺失属于真实恢复修订，静默源的持久化必须能看到新增缺口。
func TestPR61RecoveredMissingWALBodyRemainsDirty(t *testing.T) {
	root := t.TempDir()
	key := "missing/g1"
	source := SourceConfig{LogSourceID: "missing", SourceGeneration: "g1", Mode: pipeline.ModeFilePrimary, Path: filepath.Join(root, "latest.log"), StorageNamespace: "missing", UTCDay: "2026-10-08"}
	entry := ledger.Entry{Key: ledger.SourceKey{LogSourceID: "missing", SourceGeneration: "g1"}}
	saved := persistedSource{Ledger: []ledger.Entry{entry}, WALRefs: []acquire.WALRef{{Seq: 1, EventID: "absent", RecordStart: 10, RecordEnd: 20, Appended: true, Durable: true}}}
	st := persistedState{Sources: map[string]persistedSource{key: saved}, SourceConfigs: map[string]SourceConfig{key: source}}
	m := &Manager{root: root, state: st, sources: map[string]SourceConfig{}, pipes: map[string]*pipeline.Pipeline{}, cat: catalog.New(catalog.NewMemJournal())}
	store, err := stateindex.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	m.index = store
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	desired, err := m.stateToIndexState(&st)
	require.NoError(t, err)
	_, err = store.Apply(desired)
	require.NoError(t, err)
	require.NoError(t, m.Register(source))
	_, err = m.persistSnapshotStep(context.Background())
	require.NoError(t, err)
	rows, err := store.Load()
	require.NoError(t, err)
	require.NotEmpty(t, rows.Gaps, "恢复新增 WAL_BODY_MISSING 不得被登记末尾的 clean 基线吞掉")
	require.Equal(t, "WAL_BODY_MISSING", rows.Gaps[0].Reason)
}
