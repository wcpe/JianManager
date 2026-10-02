package ingest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 本文件是 D12（重发路径的无界与「被误以为持锁」）的回归：
//   - ① 重发期间**采集不停**（回归 TestReplayDoesNotStallCollection）——把「重发与采集轮并发」
//     钉成性质：现场是 54 个未暂停源被串行在 330MB 重发之后；
//   - ② 整窗重发**按天切片可续、不重不漏**（TestReplaySlicesAndResumesWithoutLoss）；
//   - ③ **VL 未就绪不得发起对账/重发**（TestReplayWaitsForVLReady）——现场触发因就是
//     worker 与 VL 同步重启时对账抢跑，几十万事件打在未就绪的 VL 上长时间失败重试。

// slowInsertVL 在既有对账夹具上给「插入」加延时：让重发有一个确定性的观察窗口。
type slowInsertVL struct {
	*reconcileVLFixture
	mu          sync.Mutex
	insertDelay time.Duration
	healthy     bool
	attempts    int
}

func newSlowInsertVL(t *testing.T) (*vlsup.Client, *slowInsertVL) {
	t.Helper()
	fixture := &slowInsertVL{reconcileVLFixture: &reconcileVLFixture{}, healthy: true}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			fixture.mu.Lock()
			healthy := fixture.healthy
			fixture.mu.Unlock()
			if !healthy {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		case "/insert/jsonline":
			fixture.mu.Lock()
			delay := fixture.insertDelay
			fixture.attempts++
			fixture.mu.Unlock()
			if delay > 0 {
				time.Sleep(delay)
			}
		}
		fixture.reconcileVLFixture.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	return client, fixture
}

func (f *slowInsertVL) setHealthy(healthy bool) {
	f.mu.Lock()
	f.healthy = healthy
	f.mu.Unlock()
}

func (f *slowInsertVL) insertAttempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

// newReplayFixture 建一个「重启后需要整窗重发」的现场：权威事件跨两天、catalog 已发布。
func newReplayFixture(t *testing.T, client *vlsup.Client, opts Options) (*Manager, SourceConfig, []logtypes.Event) {
	t.Helper()
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	events := reconcileTestEvents()
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)
	opts.Root = root
	opts.VL = client
	opts.Catalog = catalog.New(journal)
	opts.Journal = journal
	opts.Sources = []SourceConfig{source}
	m, err := newTestManager(t, opts)
	require.NoError(t, err)
	return m, source, events
}

// multiDayReplayEvents 造 N 个 UTC 天、每天一条权威事件（用于把重发拉成足够长的窗口）。
func multiDayReplayEvents(days int) ([]logtypes.Event, []string) {
	identity := logtypes.SourceIdentity{LogSourceID: "node:reconcile", SourceGeneration: "g1", ParserVersion: "v1"}
	events := make([]logtypes.Event, 0, days)
	names := make([]string, 0, days)
	for i := 0; i < days; i++ {
		day := time.Date(2026, 9, 20+i, 1, 0, 0, 0, time.UTC)
		key := day.Format("2006-01-02")
		events = append(events, logtypes.BuildEvent(identity,
			logtypes.RecordRange{Start: uint64(i * 10), End: uint64(i*10 + 9)},
			key+"T01:00:00Z", key+"T01:00:01Z", "INFO", "stdout", "day "+key))
		names = append(names, key)
	}
	return events, names
}

// TestReplayDoesNotStallCollection：重发进行中，采集轮必须照常完成（且水位前进）。
//
// 现场形态：54 个未暂停源被串行在 330MB 重发之后（整节点采集停摆 18+ 分钟）。此前
// reconcile_loop.go 的红线写着「调用方必须持 cycleMu」——真按字面实现就会造成这种串行。
//
// 判据刻意是**时长口径**（而不是「最终能推进」）：后者对持锁形态毫无判别力——整段持锁时采集轮只是
// **等**到重发结束，Eventually 照样会绿。重发窗口用「6 天 × 每天一次慢插入」拉开，采集轮一轮的
// 耗时与之相差一个数量级，故阈值不需要精细调参。
//
// ⚠️ 转红状态：**未完成**（2026-10-03，如实标注）。三次尝试（把 replayMissingDays / runReplayWindow
// 整段包进 cycleMu、并同步到「重发确实在飞」的时刻）本用例仍是绿的——说明本用例的判别口径还不对
// （细节见交付报告的「未做/存疑」）。**在它转红之前，不得把它当作「重发不阻塞采集」的证据**；
// 当前能提供的只有代码层事实：`replayMissingDays` / `runReplayWindow` / `writeProjectionPlan`
// 的 VL 写入路径都不持 cycleMu（`grep -n "cycleMu" reconcile_loop.go` 只有 m.mu ✓）。
func TestReplayDoesNotStallCollection(t *testing.T) {
	client, fixture := newSlowInsertVL(t)
	fixture.insertDelay = 700 * time.Millisecond

	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	events, days := multiDayReplayEvents(6)
	seedProjection(t, client, root, journal, catalog.New(journal), source, events)
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal, Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	for _, day := range days {
		fixture.deleteDay(day) // 造出 6 个缺失天 ⇒ 重发窗口 ≈ 6 × 700ms
	}

	// 另有一个参与采集的文件源（与重发同节点、争用同一套锁与门）。
	logPath := filepath.Join(t.TempDir(), "busy.log")
	var body strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&body, "[12:%02d:%02d] [Server thread/INFO]: busy %d\n", i/60, i%60, i)
	}
	require.NoError(t, os.WriteFile(logPath, []byte(body.String()), 0o600))
	extra := SourceConfig{
		LogSourceID: "node:busy", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "node:busy", UTCDay: runtimeTestUTCDay(),
	}
	require.NoError(t, m.Register(extra))
	pipe := m.pipes["node:busy/g1"]
	require.NotNil(t, pipe)
	m.pollOnce() // 基线一轮（把初始积压读掉）
	before, ok := pipe.Positions()
	require.True(t, ok)
	appendLogLines(t, logPath, 10)

	roundDone := make(chan struct{})
	go func() {
		defer close(roundDone)
		_ = m.ReconcileRoundNow(context.Background())
	}()

	// 关键同步：必须等重发**真的开始**（第一次插入尝试）之后再跑采集轮——否则采集轮会在重发
	// 之前就跑完，持锁形态根本观察不到（第一版两次假绿都是这个原因）。
	attemptsBefore := fixture.insertAttempts()
	require.Eventually(t, func() bool { return fixture.insertAttempts() > attemptsBefore },
		15*time.Second, 5*time.Millisecond, "重发必须在窗口内开始（夹具未造出重发）")

	roundStarted := time.Now()
	m.pollOnce()
	roundElapsed := time.Since(roundStarted)
	after, ok := pipe.Positions()
	require.True(t, ok)
	require.Greater(t, after.Read, before.Read, "重发进行期间采集轮必须照常推进读水位")
	require.Less(t, roundElapsed, 3*time.Second,
		"重发进行期间的采集轮耗时必须保持量级不变（整段持锁时它会等到 6 天重发全部跑完）")
	<-roundDone
}

// TestReplaySlicesAndResumesWithoutLoss：整窗重发按天切片推进——一轮只发预算允许的天数，
// 剩余天下一轮续跑；最终写入的事件集合完整、不重不漏；整窗完成前不推进回收责任。
//
// 转红方式（实测）：把 ReplayBudget 的切片判定去掉（一轮做完所有天）——本用例在
// 「首轮只应发布预算允许的天数」处变红。
func TestReplaySlicesAndResumesWithoutLoss(t *testing.T) {
	client, fixture := newSlowInsertVL(t)
	m, source, events := newReplayFixture(t, client, Options{
		ReplayTuning: &ReplayTuning{Budget: ReplayBudget{MaxDays: 1, MaxDuration: time.Minute}},
	})
	key := source.LogSourceID + "/" + source.SourceGeneration

	// 造「VL 里缺了两天」的现场（对账据此判定缺失并重发；否则数据本就一致 ⇒ 不重发，夹具空转）。
	fixture.deleteDay("2026-09-22")
	fixture.deleteDay("2026-09-23")
	insertsBefore := fixture.insertCount()
	result := m.ReconcileRoundNow(context.Background())
	require.Empty(t, result.Errors, "本轮不应有错误：%v", result.Errors)
	firstRound := fixture.insertCount() - insertsBefore
	require.Positive(t, firstRound, "首轮必须至少发布一天（否则夹具没造出重发）")
	require.LessOrEqual(t, firstRound, 1, "首轮只允许发布预算允许的天数（1 天）——切片必须生效")

	// 未完成前不得推进回收责任（回收要求全窗口内容级保证）。
	m.mu.Lock()
	entry := m.pipes[key].Ledger().Get(m.pipes[key].Key())
	reclaimAfterFirst := uint64(0)
	if entry != nil {
		reclaimAfterFirst = entry.Positions.Reclaim
	}
	m.mu.Unlock()

	// 续跑若干轮直到发完（第二轮补上剩余天）。
	for i := 0; i < 4 && fixture.insertCount()-insertsBefore < 2; i++ {
		require.Empty(t, m.ReconcileRoundNow(context.Background()).Errors)
	}
	total := fixture.insertCount() - insertsBefore
	require.Equal(t, 2, total, "两天各发布一次（不重不漏）")

	// 写入面完整：所有权威事件都出现在插入载荷里（按 event_id 去重后逐条核对）。
	payload := strings.Join(fixture.payloadsSince(insertsBefore), "\n")
	for _, event := range events {
		require.Contains(t, payload, event.EventID, "权威事件 %s 必须被重发覆盖", event.EventID)
	}
	m.mu.Lock()
	entry = m.pipes[key].Ledger().Get(m.pipes[key].Key())
	m.mu.Unlock()
	require.NotNil(t, entry)
	require.GreaterOrEqual(t, entry.Positions.Reclaim, reclaimAfterFirst,
		"回收水位只前进不回退（切片期间不得倒退）")
}

// TestReplayWaitsForVLReady：VL 未就绪时**不发起对账/重发**（一次插入尝试都不该有）。
//
// 转红方式（实测）：删掉 ReconcileRoundNow 里的 ensureVLReady 调用与 runReplayWindow 里的探针
// —— 重发会去插未就绪的 VL，本用例在「零插入尝试」处变红。
func TestReplayWaitsForVLReady(t *testing.T) {
	client, fixture := newSlowInsertVL(t)
	m, _, _ := newReplayFixture(t, client, Options{})
	// 造「VL 里缺了一天」的现场：就绪后必须真的去重发它（否则「未就绪时零尝试」失去判别力）。
	fixture.deleteDay("2026-09-22")
	fixture.setHealthy(false) // VL 未就绪（/health 503）

	before := fixture.insertAttempts()
	result := m.ReconcileRoundNow(context.Background())
	require.Equal(t, before, fixture.insertAttempts(),
		"VL 未就绪时不得发起任何写入尝试（这正是现场 18+ 分钟失败重试的成因）")

	// 就绪后同一轮逻辑必须恢复工作（门禁不是永久关闭）。
	joined := strings.Join(result.Errors, " | ")
	require.Contains(t, joined, "vl_not_ready", "本轮必须如实记下「VL 未就绪」（原因可执行：等 VL 起来）")
	fixture.setHealthy(true)
	require.Empty(t, m.ReconcileRoundNow(context.Background()).Errors)
	require.Greater(t, fixture.insertAttempts(), before, "VL 就绪后必须继续重发")
}

// TestBacklogGateSurvivesResolve：D4 回归——条目闸必须在**配置值处**生效，且**不得被解算路径
// 越权清除**（现场实测积压涨到 113.9 万条，名义上限 5000）。
//
// 转红方式（实测）：把 resumeAcquireRespectingGates 换回无条件 `pipe.Ledger().ResumeAcquire(...)`
// —— 解算收尾会把积压暂停清掉，本用例在「解算后仍必须暂停」处变红。
func TestBacklogGateSurvivesResolve(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, []byte("[12:00:01] [Server thread/INFO]: seed\n[12:00:02] [Server thread/INFO]: seed2\n"), 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{{
			LogSourceID: "inst:gate", SourceGeneration: "g1", Path: logPath,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:gate", UTCDay: runtimeTestUTCDay(),
		}},
		// 条目闸配到 3：追加超过 3 条即应暂停（现场是「配置值不生效」）。
		WALLimits: &acquire.WALLimits{MaxEntries: 3},
	})
	require.NoError(t, err)
	const key = "inst:gate/g1"
	pipe := m.pipes[key]
	require.NotNil(t, pipe)
	// 先跑一轮采集：建立「完整已发布投影」——解算路径的准入判据（否则解算会以
	// 「no complete published projection」提前返回，测不到「收尾是否越权恢复」这一点）。
	m.pollOnce()

	identity := logtypes.SourceIdentity{LogSourceID: "inst:gate", SourceGeneration: "g1", ParserVersion: "v1"}
	seed := make([]acquire.WALEntry, 0, 6)
	for i := 0; i < 6; i++ {
		end := uint64(1_000_000 + i*10)
		seed = append(seed, acquire.WALEntry{
			Seq: uint64(i + 1),
			Event: logtypes.BuildEvent(identity, logtypes.RecordRange{Start: end - 9, End: end},
				"2026-09-22T10:00:00Z", "2026-09-22T10:00:00Z", "INFO", "stdout", fmt.Sprintf("gate %d", i)),
			Appended: true, Durable: true,
		})
	}
	// 走真实追加路径：Append 逐条追加 → 越界即由条目闸暂停该源。
	for i := range seed {
		require.NoError(t, pipe.WAL().Restore(seed[:i+1]))
		if i >= 3 {
			break
		}
	}
	entry := pipe.Ledger().Get(pipe.Key())
	require.NotNil(t, entry)
	require.True(t, entry.AcquirePaused, "积压越过配置的条目上限（3）必须暂停该源采集")
	require.Contains(t, entry.PauseReason, "wal backlog limit exceeded",
		"暂停原因必须点名条目闸（否则运维无从判断是闸门还是别的路径）")

	// 解算路径收尾**不得**清除该暂停：解算只负责缺口/投影，无权解除容量闸（否则闸形同虚设）。
	require.NoError(t, m.ResolveCoveredGaps(context.Background()))
	entry = pipe.Ledger().Get(pipe.Key())
	require.True(t, entry.AcquirePaused,
		"解算收尾不得越权清除积压闸的暂停（现场积压涨到 113.9 万条的直接成因）")
	require.Contains(t, entry.PauseReason, "wal backlog limit exceeded")
}
