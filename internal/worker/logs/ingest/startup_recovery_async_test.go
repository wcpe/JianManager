package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// 本文件是 2026-10-02 生产事故（启动路径"单线程、与规模成正比的持久化" ⇒ worker 20–30 分钟
// 不可达）的 ② 回归：**启动恢复不得阻塞 New**（生产语义 = StartupRecoveryBackground）、
// **落库分步可续且不丢行**、以及 ①水位化的**接线**（回收水位必须真的下发给索引层）。

// waitContext 返回带超时的 ctx（用例等待后台恢复用）。
func waitContext(t *testing.T, timeout time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(cancel)
	return ctx
}

// TestStartupRecoveryDoesNotBlockNew：对账挂起时，New 必须立刻返回（恢复在后台），
// 就绪面在恢复未完成期间报 not-ready，恢复结束后自动转就绪。
//
// 现场形态（SIGQUIT 141 goroutine 取证）：`ingest.New` → applyStartupRecovery → releaseRecovery
// → persist → ApplyScoped → applyTableDeletes 单线程长跑，New 不返回 ⇒ main 起不到反向隧道 ⇒
// worker 全程不监听。本用例把「恢复慢」构造成确定性的（对账查询挂起），断言启动不再等它。
//
// 转红方式（实测）：让 startStartupRecovery 忽略 background 标志（退回改动前的同步全量）——
// New 会等满挂起的对账超时，本用例在「New 必须立刻返回」处变红。
func TestStartupRecoveryDoesNotBlockNew(t *testing.T) {
	client, fixture, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	seedProjection(t, client, root, journal, catalog.New(journal), source, reconcileTestEvents())
	// 对账查询挂起到客户端超时：给「恢复进行中」一个确定的时间窗（QueryTimeout = 3s）。
	fixture.setStallStats(true)

	started := time.Now()
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: []SourceConfig{source},
		Reconcile:                 &ReconcileConfig{Enabled: true, Concurrency: 1, Timeout: 30 * time.Second, QueryTimeout: 3 * time.Second},
		StartupRecoveryBackground: true,
	})
	elapsed := time.Since(started)
	require.NoError(t, err)
	require.Less(t, elapsed, 2*time.Second,
		"启动恢复在后台 ⇒ New 必须立刻返回（旧形态会等满整段恢复：对账挂起 3s + 重发）")

	status := m.StartupRecoveryStatus()
	require.True(t, status.InProgress, "此刻恢复仍在进行（New 没等它）")
	require.Positive(t, status.Total)
	readiness := m.CutoverReadiness()
	require.False(t, readiness.LedgerReady, "恢复未完成时必须报 not-ready")
	require.Contains(t, readiness.Reasons, "startup_recovery_in_progress",
		"就绪原因必须点名「启动恢复未完成」，不能只给一个笼统的 not-ready")

	// 恢复自行完成（对账超时 → 回退整窗重发 → 落库），就绪面随之转就绪。
	require.NoError(t, m.WaitStartupRecovery(waitContext(t, 30*time.Second)))
	require.False(t, m.StartupRecoveryStatus().InProgress)
	require.True(t, m.CutoverReadiness().LedgerReady, "恢复完成后应回到可用状态")
}

// TestPersistStepwiseSurvivesTinyCycleBudget：每周期总预算压到极小（16 行）时，一次 persist
// 仍必须把**全部**变更落库（分步续跑），既不多写也不少写。
//
// 转红方式（实测）：在 persistSnapshotStep 里让「未完成」也推进 persistedRev/persistedCover
// —— 下一步会把这些源当作未变更而跳过，索引静默少行，本用例在「索引行数 == 注入行数」处变红。
func TestPersistStepwiseSurvivesTinyCycleBudget(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, []byte(
		"[12:00:01] [Server thread/INFO]: seed one\n[12:00:02] [Server thread/INFO]: seed two\n"), 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{{
			LogSourceID: "inst:step", SourceGeneration: "g1", Path: logPath,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:step", UTCDay: runtimeTestUTCDay(),
		}},
		// 周期预算在**打开索引时**生效（键 log_index.persist.* 的同一条接线），故必须经 Options 下发。
		IndexCommit: &stateindex.CommitBudget{CycleMaxRows: 16},
	})
	require.NoError(t, err)
	m.pollOnce()
	const key = "inst:step/g1"
	pipe := m.pipes[key]
	require.NotNil(t, pipe)

	// 注入 200 条待落库的 WAL 行（末端远高于当前读/回收水位 ⇒ 不会被剪枝带走，必须全部落库）。
	identity := logtypes.SourceIdentity{LogSourceID: "inst:step", SourceGeneration: "g1", ParserVersion: "v1"}
	const injected = 200
	seed := make([]acquire.WALEntry, 0, injected)
	for i := 0; i < injected; i++ {
		end := uint64(1_000_000 + i*10)
		seed = append(seed, acquire.WALEntry{
			Seq: uint64(i + 1),
			Event: logtypes.BuildEvent(identity, logtypes.RecordRange{Start: end - 9, End: end},
				"2026-09-22T10:00:00Z", "2026-09-22T10:00:00Z", "INFO", "stdout", fmt.Sprintf("step %d", i)),
			Appended: true, Durable: true,
		})
	}
	require.NoError(t, pipe.WAL().Restore(seed))
	// 制造一次账本修订（真实路径下 WAL 变化总伴随账本变化，落库的「变更源」判据是修订号）。
	require.NoError(t, pipe.Ledger().PauseAcquire(pipe.Key(), "seed"))
	require.NoError(t, pipe.Ledger().ResumeAcquire(pipe.Key()))

	// 周期预算已是 16 行（见上文 Options）：200 行差异不可能一步做完。
	samplesBefore := len(m.PersistSamples())
	require.NoError(t, m.persist())

	// ① 分步确实发生了（采样按提交单元记录 ⇒ 单元数随行数/预算增长）。
	require.Greater(t, len(m.PersistSamples())-samplesBefore, 2,
		"16 行/周期的预算下，200 行变更必须分成多个提交单元")
	require.False(t, m.LastPersistStats().Incomplete, "最后一轮必须把差异做完（否则 persist 不得返回）")

	// ② 全部变更都落库（逐行比对：索引行数 == 管道 WAL 条目数，且一条不少）。
	loaded, err := m.index.Load()
	require.NoError(t, err)
	indexed := 0
	for _, row := range loaded.WAL {
		if row.Key == key {
			indexed++
		}
	}
	require.Equal(t, injected, indexed, "分步续跑后索引必须与管道 WAL 一致（不得静默丢行）")
	require.Len(t, pipe.WAL().Snapshot(), injected)
}

// TestPersistRoutesReclaimWatermarkToRangePrune：①水位化的**接线**回归——采集侧回收水位必须真的
// 作为剪枝提示下发到索引层，且索引层用它把整段差异**一条语句**删掉。
//
// 夹具复刻现场形态：先让索引里存在「已耐久、且落在回收水位之内」的 WAL 行（生产上由积压期产生），
// 再让回收推进把它们判为可剪（生产上由启动恢复的整窗重发触发）。此后落库必须走谓词范围删除。
//
// 转红方式（实测）：把 persistSnapshotStep 里的 prunes 提示改成 nil —— 本用例在
// 「必须走谓词范围删除 / 12 行必须由范围删除清掉」处变红。
func TestPersistRoutesReclaimWatermarkToRangePrune(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	// 三行：末行永远处于「未闭合」状态（多行缓冲在等下一个边界），前两行才会成为事件并完成投递。
	require.NoError(t, os.WriteFile(logPath, []byte(
		"[12:00:01] [Server thread/INFO]: seed one\n"+
			"[12:00:02] [Server thread/INFO]: seed two\n"+
			"[12:00:03] [Server thread/INFO]: seed boundary\n"), 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{{
			LogSourceID: "inst:prune", SourceGeneration: "g1", Path: logPath,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:prune", UTCDay: runtimeTestUTCDay(),
		}},
	})
	require.NoError(t, err)
	const key = "inst:prune/g1"
	pipe := m.pipes[key]
	require.NotNil(t, pipe)
	// 推起回收水位：投递 + 逐字段校验 + 责任转移（可能跨若干轮，故用 Eventually）。
	require.Eventually(t, func() bool {
		m.pollOnce()
		entry := pipe.Ledger().Get(pipe.Key())
		return entry != nil && entry.Positions.Reclaim > 0
	}, 20*time.Second, 20*time.Millisecond, "夹具前提：先把回收水位推起来")

	// 注入 12 条「已耐久且落在回收水位之内」的 WAL 行 —— 它们既应被内存剪枝丢掉，
	// 也应被索引层用**一条谓词删除**清掉。
	identity := logtypes.SourceIdentity{LogSourceID: "inst:prune", SourceGeneration: "g1", ParserVersion: "v1"}
	seed := make([]acquire.WALEntry, 0, 12)
	for i := 0; i < 12; i++ {
		end := uint64(i*10 + 10)
		seed = append(seed, acquire.WALEntry{
			Seq: uint64(i + 1),
			Event: logtypes.BuildEvent(identity, logtypes.RecordRange{Start: end - 9, End: end},
				"2026-09-22T10:00:00Z", "2026-09-22T10:00:00Z", "INFO", "stdout", fmt.Sprintf("seed %d", i)),
			Appended: true, Durable: true,
		})
	}
	require.NoError(t, pipe.WAL().Restore(seed))
	// 真实路径下 WAL 变化总伴随账本变化（读/耐久/回收位置推进），而落库的「变更源」判据是
	// 账本修订号；这里注入的是纯 WAL 行，故显式制造一次账本修订（暂停→恢复，语义上是空操作）。
	require.NoError(t, pipe.Ledger().PauseAcquire(pipe.Key(), "seed"))
	require.NoError(t, pipe.Ledger().ResumeAcquire(pipe.Key()))
	require.NoError(t, m.persist(), "先把这批 WAL 行落进索引（复刻积压期形态）")
	loaded, err := m.index.Load()
	require.NoError(t, err)
	seeded := 0
	for _, row := range loaded.WAL {
		if row.Key == key {
			seeded++
		}
	}
	require.Equal(t, 12, seeded, "索引里应存在 12 条待剪的 WAL 行")

	// 推进回收即「内存剪枝」发生的那一刻：生产上它的调用点是 releaseRecovery 的尾步
	// （启动恢复整窗重发之后），稳态则是每轮采集的投递之后——两处都是 TryReclaim。
	// 推进回收：生产上这一步由 releaseRecovery 走完责任转移链后调 TryReclaim 完成（起点是启动
	// 恢复的整窗重发）。这里按**同一条链**绑定并释放一个覆盖 [reclaim, 注入末端] 的恢复分段，
	// 使账本放行（默认拒绝正是保证「回收必须有凭据」的守卫）。
	from := pipe.Ledger().Get(pipe.Key()).Positions.Reclaim
	const segID = "projection-recovery-test-120"
	require.NoError(t, pipe.BindRecoverySegment(segID, "projection://test", from, 120))
	require.NoError(t, pipe.TransitionRecovery(segID, logtypes.RecoveryDurableVerified, "", ""))
	require.NoError(t, pipe.TransitionRecovery(segID, logtypes.RecoveryWALResponsibilityXfer, "", "test"))
	require.NoError(t, pipe.TransitionRecovery(segID, logtypes.RecoveryReleased, logtypes.ReleaseProjectionBacked, "test"))
	_, err = pipe.TryReclaim()
	require.NoError(t, err, "凭据齐备后回收必须放行（否则夹具没造出「剪枝」这一步）")
	surviving := len(pipe.WAL().Snapshot())
	require.Less(t, surviving, 12, "回收水位之内的已耐久条目必须被内存剪枝丢掉")

	// 紧接着的落库必须把这段差异表达成**一条谓词范围删除**，而不是逐行 DELETE。
	require.NoError(t, m.persist())
	stats := m.LastPersistStats()
	require.Positive(t, stats.RangePrunes, "必须走谓词范围删除（①水位化接线生效）")
	require.Equal(t, 12-surviving, stats.RangePruned, "被剪掉的行必须由范围删除清掉")
	require.LessOrEqual(t, stats.Statements, 4, "删除必须批量（不得退化成逐行 DELETE）")

	// 等价性：索引里该源的 WAL 行数必须与内存 WAL 完全一致（范围删除不得多删或少删）。
	loaded, err = m.index.Load()
	require.NoError(t, err)
	indexed := 0
	for _, row := range loaded.WAL {
		if row.Key == key {
			indexed++
		}
	}
	require.Equal(t, surviving, indexed, "范围删除后索引必须与内存 WAL 逐行一致")
}
