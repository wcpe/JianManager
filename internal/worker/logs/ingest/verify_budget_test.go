package ingest

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 本文件是 2026-10-03 现场（近 5 分钟 `投影校验查询 ×410` 占满算力、未暂停源 readΔ=0）的回归：
// **校验/对账查询必须有全局天花板，且不得因此拖慢健康路径**。
//
// 三条性质：
//  1. 健康路径零等待（**天花板不是节拍器** ✗）；
//  2. 风暴被削峰，且采集轮在风暴期间仍能推进 ✓；
//  3. 单簇尝试次数有界（现场 ×410 的来源正是「退避到窗口耗尽」✗）。

// failingQueryVL 让**校验查询**失败（插入正常），并记录每次查询的时刻。
type failingQueryVL struct {
	mu      sync.Mutex
	queries []time.Time
}

func newFailingQueryVL(t *testing.T) (*vlsup.Client, *failingQueryVL) {
	t.Helper()
	fixture := &failingQueryVL{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/select/logsql/query":
			fixture.mu.Lock()
			fixture.queries = append(fixture.queries, time.Now())
			fixture.mu.Unlock()
			w.WriteHeader(http.StatusInternalServerError) // 校验失败 ⇒ 调用方退避重试
		case "/insert/jsonline":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	return client, fixture
}

func (f *failingQueryVL) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queries)
}

// maxPerSecond 返回任一秒钟窗口内的最大查询数（削峰判据）。
func (f *failingQueryVL) maxPerSecond() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	best := 0
	for i := range f.queries {
		window := 0
		for j := i; j < len(f.queries) && f.queries[j].Sub(f.queries[i]) <= time.Second; j++ {
			window++
		}
		if window > best {
			best = window
		}
	}
	return best
}

// newVerifyFixture 造一个「校验必失败」的源（插入正常，便于同时观察采集轮）。
func newVerifyFixture(t *testing.T, client *vlsup.Client, opts Options) (*Manager, SourceConfig) {
	t.Helper()
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, []byte(
		"[12:00:01] [Server thread/INFO]: v one\n[12:00:02] [Server thread/INFO]: v two\n"), 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:verify", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:verify", UTCDay: runtimeTestUTCDay(),
	}
	opts.Root = root
	opts.VL = client
	opts.Catalog = cat
	opts.Journal = cat.Journal()
	opts.Sources = []SourceConfig{source}
	m, err := newTestManager(t, opts)
	require.NoError(t, err)
	return m, source
}

// verifyEvents 造 4 条事件（1 簇，便于精确断言查询数）。
func verifyEvents() []logtypes.Event {
	identity := logtypes.SourceIdentity{LogSourceID: "inst:verify", SourceGeneration: "g1", ParserVersion: "v1"}
	events := make([]logtypes.Event, 0, 4)
	for i := 0; i < 4; i++ {
		end := uint64(10 + i*10)
		events = append(events, logtypes.BuildEvent(identity, logtypes.RecordRange{Start: end - 9, End: end},
			"2026-09-22T10:00:00Z", "2026-09-22T10:00:00Z", "INFO", "stdout", "verify event"))
	}
	return events
}

// TestVerifyLimiterKeepsHealthyPathFast（性质 1）：**天花板不是节拍器**——正常路径（少量查询）
// 必须零等待通过；只有超过上限的风暴才被削峰。
//
// 转红方式（实测）：把上限调成 1/秒（节拍器形态）——4 次查询会被逐个节流，本用例在
// 「健康路径耗时有界」处变红。
func TestVerifyLimiterKeepsHealthyPathFast(t *testing.T) {
	limiter := newVerifyLimiter(VerifyBudget{MaxPerSecond: 30, MaxInFlight: 6})
	started := time.Now()
	for i := 0; i < 4; i++ {
		require.NoError(t, limiter.Acquire(nil))
		limiter.Release()
	}
	elapsed := time.Since(started)
	require.Less(t, elapsed, 20*time.Millisecond,
		"健康路径（4 次查询）必须零等待通过（实测 %s）：上限是天花板，不是节拍器", elapsed)
}

// TestVerifyQueryBudgetIsBounded（性质 3）：单簇一次校验的查询数必须有界。
//
// 现场 ×410 的来源：校验失败后按 200ms–2s 退避重试**直到校验窗口耗尽**（生产 5 分钟 ⇒ 单簇
// 150+ 次查询）。本用例把窗口设为 3s、退避下限设为 10ms：没有预算时会打出上百次查询 ✗。
//
// 转红方式（实测）：去掉单簇尝试预算（`maxQueries` 判定）——查询数远超预算，本用例变红。
func TestVerifyQueryBudgetIsBounded(t *testing.T) {
	client, fixture := newFailingQueryVL(t)
	m, source := newVerifyFixture(t, client, Options{})
	m.verificationTimeout = 3 * time.Second
	m.verifyBackoffMin = 10 * time.Millisecond
	m.verifyBackoffMax = 20 * time.Millisecond
	m.verifyMaxQueriesPerChunk = 12

	err := m.verifyProjection(client, source, "gen-budget", verifyEvents())
	require.Error(t, err, "校验失败必须以错误收口（预算用尽 ≠ 通过 ✗）")
	// 结论措辞：预算用尽时与「窗口到期」路径同构（有传输类 lastErr 时以它为准，否则用
	// "not fully visible" 措辞）——两条都含 "verification"/"visible"，断言不锁定具体分支 ✓。
	joined := err.Error()
	require.True(t, strings.Contains(joined, "verification") || strings.Contains(joined, "visible"),
		"预算用尽的结论必须落在既有措辞族内（实测 %q）", joined)
	require.LessOrEqual(t, fixture.count(), 12,
		"单簇查询数必须被尝试预算封顶（实测 %d 次）", fixture.count())
}

// TestVerifyStormDoesNotStarveCollection（性质 2）：校验风暴期间，采集轮必须照常推进；
// 且查询速率被削峰到全局上限量级（默认 30/s，留一倍余量判阈值）。
//
// 转红方式（实测）：去掉 limiter（`verifyProjectionQuery` 不再取许可）——查询速率飙升，
// 本用例在「观察到的每秒查询数 ≤ 上限」处变红。
func TestVerifyStormDoesNotStarveCollection(t *testing.T) {
	client, fixture := newFailingQueryVL(t)
	// 采集源必须走**健康**的 VL（否则它自己的校验也会失败，测不到"采集不受影响"✗）：
	// 用 VLRoute 把风暴源指向失败夹具、其余源指向健康夹具。
	healthy, _ := newProjectionVL(t)
	m, source := newVerifyFixture(t, client, Options{
		VLRoute: func(src SourceConfig) (*vlsup.Client, bool, error) {
			if src.LogSourceID == "inst:verify" {
				return client, false, nil
			}
			return healthy, false, nil
		},
	})
	m.verificationTimeout = 3 * time.Second
	m.verifyBackoffMin = 5 * time.Millisecond
	m.verifyBackoffMax = 10 * time.Millisecond
	m.verifyMaxQueriesPerChunk = 1000 // 本用例要看「速率闸」，故先放开单簇预算
	m.verifyLimiter = newVerifyLimiter(VerifyBudget{MaxPerSecond: 20, MaxInFlight: 2})
	m.verifyChunkEvents = 1 // 4 条事件 ⇒ 4 簇并发，制造风暴

	// 采集轮：另起一个文件源并不断写入（观察 read 水位是否推进）。
	busyPath := filepath.Join(t.TempDir(), "busy.log")
	require.NoError(t, os.WriteFile(busyPath, []byte("[12:00:01] [Server thread/INFO]: busy\n[12:00:02] [Server thread/INFO]: busy2\n"), 0o600))
	extra := SourceConfig{
		LogSourceID: "inst:busy", SourceGeneration: "g1", Path: busyPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:busy", UTCDay: runtimeTestUTCDay(),
	}
	require.NoError(t, m.Register(extra))
	pipe := m.pipes["inst:busy/g1"]
	require.NotNil(t, pipe)

	stormDone := make(chan struct{})
	go func() {
		defer close(stormDone)
		_ = m.verifyProjection(client, source, "gen-storm", verifyEvents())
	}()
	time.Sleep(50 * time.Millisecond) // 让风暴先起来

	before, ok := pipe.Positions()
	require.True(t, ok)
	appendLogLines(t, busyPath, 30)
	require.Eventually(t, func() bool {
		m.pollOnce()
		after, ok := pipe.Positions()
		return ok && after.Read > before.Read
	}, 20*time.Second, 20*time.Millisecond,
		"校验风暴期间采集轮必须照常推进 read 水位")
	<-stormDone

	require.LessOrEqual(t, fixture.maxPerSecond(), 40,
		"查询速率必须被削峰到全局上限量级（实测峰值 %d 次/秒，上限 20）", fixture.maxPerSecond())
	_ = strings.TrimSpace
}
