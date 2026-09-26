package vlsup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
)

// fakeProc 测试用进程句柄，不依赖真实 VL 二进制。
type fakeProc struct {
	pid     int
	bin     string
	args    []string
	mu      sync.Mutex
	stopped bool
	exited  bool
	waitCh  chan struct{}
}

// exitImmediately 让该 fake 表现为「启动后立刻退出」，用于锁定启动存活校验。
func (p *fakeProc) exitImmediately() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.exited = true
}

func (p *fakeProc) PID() int { return p.pid }

func (p *fakeProc) Stop(grace time.Duration) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return nil
	}
	p.stopped = true
	close(p.waitCh)
	return nil
}

func (p *fakeProc) Stopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopped
}

// Exited 报告测试进程是否已退出：默认长期存活，便于既有用例继续验证「启动即 RUNNING」；
// 需要模拟「启动后立即退出」的用例可用 exitImmediately 打开。
func (p *fakeProc) Exited() bool {
	if p == nil {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exited
}

// fakeFactory 记录 Start 调用，可注入失败；不 exec 真实二进制。
type fakeFactory struct {
	mu       sync.Mutex
	failNext error
	// exitNext 让下一个创建的进程表现为「启动后立即退出」。
	exitNext bool
	bins     []string
	starts   [][]string
	procs    []*fakeProc
	nextPID  int
}

func (f *fakeFactory) Start(ctx context.Context, bin string, args []string) (Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return nil, err
	}
	f.nextPID++
	p := &fakeProc{
		pid:    f.nextPID,
		bin:    bin,
		args:   append([]string(nil), args...),
		exited: f.exitNext,
		waitCh: make(chan struct{}),
	}
	f.exitNext = false
	f.bins = append(f.bins, bin)
	f.starts = append(f.starts, p.args)
	f.procs = append(f.procs, p)
	return p, nil
}

func (f *fakeFactory) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.starts)
}

func writeFakeBinary(t *testing.T) (path, sha string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "victoria-logs-fake")
	content := []byte("not-a-real-victoria-logs")
	if err := os.WriteFile(p, content, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	return p, hex.EncodeToString(sum[:])
}

func newTestSupervisor(t *testing.T, factory CmdFactory, sink Sink, allowVL bool) *Supervisor {
	t.Helper()
	bin, sum := writeFakeBinary(t)
	s, err := New(Options{
		BinaryPath:           bin,
		AssetSHA256:          sum,
		DataRoot:             t.TempDir(),
		AuthUsername:         "jm",
		AuthPassword:         "local-only",
		Factory:              factory,
		Sink:                 sink,
		AllowVLRecursiveSink: allowVL,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSupervisorReplaceBinaryRestartsOnlyRunningNamespaces(t *testing.T) {
	factory := &fakeFactory{}
	s := newTestSupervisor(t, factory, &RecordingSink{}, false)
	require.NoError(t, s.Start(context.Background(), NamespaceHot))
	require.NoError(t, s.Start(context.Background(), NamespaceCold))
	newBinary := filepath.Join(t.TempDir(), "victoria-logs-new")
	content := []byte("new approved executable")
	require.NoError(t, os.WriteFile(newBinary, content, 0o755))
	sum := sha256.Sum256(content)
	newSHA := hex.EncodeToString(sum[:])
	require.Error(t, s.ReplaceBinary(context.Background(), newBinary, strings.Repeat("0", 64)))
	require.Equal(t, 2, factory.startCount())
	require.NoError(t, s.ReplaceBinary(context.Background(), newBinary, newSHA))
	require.Equal(t, 4, factory.startCount())
	require.Equal(t, []string{newBinary, newBinary}, factory.bins[2:])
	for _, ns := range []Namespace{NamespaceHot, NamespaceCold} {
		status, err := s.Status(ns)
		require.NoError(t, err)
		require.Equal(t, StateRunning, status.State)
		require.Equal(t, newSHA, status.AssetSHA256)
	}
	status, err := s.Status(NamespaceRehydrate)
	require.NoError(t, err)
	require.Equal(t, StateStopped, status.State)
	require.NoError(t, s.ReplaceBinary(context.Background(), newBinary, newSHA))
	require.Equal(t, 4, factory.startCount())
}

func TestSupervisorReplaceBinaryRestoresPreviousAssetWhenStartupFails(t *testing.T) {
	factory := &fakeFactory{}
	s := newTestSupervisor(t, factory, &RecordingSink{}, false)
	ctx := context.Background()
	if err := s.Start(ctx, NamespaceHot); err != nil {
		t.Fatal(err)
	}
	oldBinary := factory.bins[0]
	newBinary := filepath.Join(t.TempDir(), "victoria-logs-new")
	data := []byte("replacement executable")
	if err := os.WriteFile(newBinary, data, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	factory.failNext = errors.New("new process refused startup")
	if err := s.ReplaceBinary(ctx, newBinary, hex.EncodeToString(sum[:])); err == nil {
		t.Fatal("new asset start failure must be reported")
	}
	if got := factory.bins[len(factory.bins)-1]; got != oldBinary {
		t.Fatalf("rollback must run previous binary: got %s want %s", got, oldBinary)
	}
	status, err := s.Status(NamespaceHot)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateRunning {
		t.Fatalf("previous namespace must be restored: %+v", status)
	}
}

func TestSupervisorStartStopStatus(t *testing.T) {
	f := &fakeFactory{}
	s := newTestSupervisor(t, f, &RecordingSink{}, false)
	ctx := context.Background()

	if err := s.Start(ctx, NamespaceHot); err != nil {
		t.Fatalf("start hot: %v", err)
	}
	st, err := s.Status(NamespaceHot)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateRunning {
		t.Fatalf("state=%s", st.State)
	}
	if st.AssetTag != AssetTag || st.AssetBuildID != AssetBuildID {
		t.Fatalf("status must record asset tag/build: %+v", st)
	}
	if st.PID == 0 {
		t.Fatal("pid expected after start")
	}
	if !strings.HasPrefix(st.ListenAddr, "127.0.0.1:") {
		t.Fatalf("listen must be localhost: %s", st.ListenAddr)
	}

	// 重复 start
	if err := s.Start(ctx, NamespaceHot); err == nil {
		t.Fatal("second start should fail")
	}

	if err := s.Stop(ctx, NamespaceHot); err != nil {
		t.Fatalf("stop: %v", err)
	}
	st, _ = s.Status(NamespaceHot)
	if st.State != StateStopped || st.PID != 0 {
		t.Fatalf("after stop: %+v", st)
	}
}

func TestSupervisorManagesThreeNamespaces(t *testing.T) {
	f := &fakeFactory{}
	s := newTestSupervisor(t, f, &RecordingSink{}, false)
	ctx := context.Background()
	for _, ns := range []Namespace{NamespaceHot, NamespaceCold, NamespaceRehydrate} {
		if err := s.Start(ctx, ns); err != nil {
			t.Fatalf("start %s: %v", ns, err)
		}
	}
	if f.startCount() != 3 {
		t.Fatalf("expected 3 starts, got %d", f.startCount())
	}
	all := s.StatusAll()
	if len(all) != 3 {
		t.Fatalf("status all: %d", len(all))
	}
	// 数据根按 namespace 隔离
	seen := map[string]bool{}
	for _, st := range all {
		if seen[st.StorageDataPath] {
			t.Fatalf("storage path collision: %s", st.StorageDataPath)
		}
		seen[st.StorageDataPath] = true
	}
}

func TestSupervisorStartVerifiesAssetBeforeExec(t *testing.T) {
	f := &fakeFactory{}
	sink := &RecordingSink{}
	s := newTestSupervisor(t, f, sink, false)
	// 破坏资产哈希
	s.assetSHA = strings.Repeat("0", 64)
	err := s.Start(context.Background(), NamespaceCold)
	if err == nil {
		t.Fatal("bad asset hash must fail before exec")
	}
	if f.startCount() != 0 {
		t.Fatal("must not exec when asset verification fails")
	}
	if sink.Count() == 0 {
		t.Fatal("failure must go to independent sink")
	}
	st, _ := s.Status(NamespaceCold)
	if st.State != StateFailed {
		t.Fatalf("state after verify fail: %s", st.State)
	}
}

func TestSupervisorFactoryErrorUsesSinkAndGuard(t *testing.T) {
	f := &fakeFactory{failNext: errors.New("exec boom")}
	// 模拟错误地把 sink 接到 VL 管道
	rec := &RecordingSink{IntoVL: true}
	s := newTestSupervisor(t, f, rec, false)
	err := s.Start(context.Background(), NamespaceHot)
	if err == nil {
		t.Fatal("factory error must surface")
	}
	if rec.Count() != 0 {
		t.Fatal("recursive VL sink must be blocked by guard flag")
	}
	if s.GuardBlocks() == 0 {
		t.Fatal("guard block should be counted")
	}
}

func TestSupervisorIndependentSinkReceivesFailures(t *testing.T) {
	f := &fakeFactory{failNext: errors.New("spawn fail")}
	rec := &RecordingSink{IntoVL: false}
	s := newTestSupervisor(t, f, rec, false)
	_ = s.Start(context.Background(), NamespaceRehydrate)
	if rec.Count() != 1 {
		t.Fatalf("independent sink should receive failure, got %d", rec.Count())
	}
	reports := rec.Snapshot()
	if reports[0].Instance != string(NamespaceRehydrate) {
		t.Fatalf("instance=%s", reports[0].Instance)
	}
}

func TestSupervisorHealthIndependentOfPartitionState(t *testing.T) {	var requireAuthUser, requireAuthPass string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != requireAuthUser || pass != requireAuthPass {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// 将 Health 客户端注入为 httptest base（仍 loopback）
	u := srv.URL
	f := &fakeFactory{}
	bin, sum := writeFakeBinary(t)
	s, err := New(Options{
		BinaryPath:   bin,
		AssetSHA256:  sum,
		DataRoot:     t.TempDir(),
		AuthUsername: "jm",
		AuthPassword: "local-only",
		Factory:      f,
		Sink:         &RecordingSink{},
		HealthClient: func(baseURL, user, pass string) (*Client, error) {
			requireAuthUser, requireAuthPass = user, pass
			return NewClient(ClientOptions{BaseURL: u, Username: user, Password: pass})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Start(ctx, NamespaceHot); err != nil {
		t.Fatal(err)
	}
	if err := s.Health(ctx, NamespaceHot); err != nil {
		t.Fatalf("health: %v", err)
	}
	st, _ := s.Status(NamespaceHot)
	if !st.HealthOK {
		t.Fatal("HealthOK should be true")
	}
	// 契约：进程健康 ≠ 分区恢复完成 ≠ 查询完整可用
	if st.PartitionRecoveryComplete || st.QueryReady {
		t.Fatal("health must not imply partition recovery or query ready")
	}
}

func TestSupervisorHealthUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	f := &fakeFactory{}
	sink := &RecordingSink{}
	bin, sum := writeFakeBinary(t)
	s, err := New(Options{
		BinaryPath:   bin,
		AssetSHA256:  sum,
		DataRoot:     t.TempDir(),
		AuthUsername: "jm",
		AuthPassword: "local-only",
		Factory:      f,
		Sink:         sink,
		HealthClient: func(baseURL, user, pass string) (*Client, error) {
			return NewClient(ClientOptions{BaseURL: srv.URL, Username: user, Password: pass})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), NamespaceCold); err != nil {
		t.Fatal(err)
	}
	err = s.Health(context.Background(), NamespaceCold)
	if err == nil || !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("want unauthorized, got %v", err)
	}
	st, _ := s.Status(NamespaceCold)
	if st.HealthOK {
		t.Fatal("HealthOK must be false on unauthorized")
	}
}

func TestSupervisorStartUsesBuiltArgs(t *testing.T) {
	f := &fakeFactory{}
	s := newTestSupervisor(t, f, &RecordingSink{}, false)
	if err := s.Start(context.Background(), NamespaceHot); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	args := f.starts[0]
	f.mu.Unlock()
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-memory.allowedBytes=536870912") {
		t.Fatalf("hot default cache missing: %v", args)
	}
	if !strings.Contains(joined, "-httpListenAddr=127.0.0.1:") {
		t.Fatalf("localhost bind missing: %v", args)
	}
	if strings.Contains(joined, "0.0.0.0") {
		t.Fatal("non-local bind forbidden")
	}
}

func TestSupervisorEnforceCacheBudgetMethod(t *testing.T) {
	s := newTestSupervisor(t, &fakeFactory{}, &RecordingSink{}, false)
	if err := s.EnforceCacheBudget(DefaultHotCacheBytes); err != nil {
		t.Fatal(err)
	}
	if s.Budget().HotCacheBytes != DefaultHotCacheBytes {
		t.Fatalf("budget not recorded: %+v", s.Budget())
	}
	if err := s.EnforceCacheBudget(-1); err == nil {
		t.Fatal("negative must fail")
	}
}

func TestSupervisorNewValidation(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("empty options must fail")
	}
	bin, sum := writeFakeBinary(t)
	if _, err := New(Options{BinaryPath: bin, AssetSHA256: sum, DataRoot: t.TempDir()}); err == nil {
		t.Fatal("auth required")
	}
}

func TestUnknownNamespace(t *testing.T) {
	s := newTestSupervisor(t, &fakeFactory{}, &RecordingSink{}, false)
	if err := s.Start(context.Background(), Namespace("nope")); err == nil {
		t.Fatal("unknown ns must fail")
	}
	if _, err := s.Status(Namespace("nope")); err == nil {
		t.Fatal("unknown status must fail")
	}
}

func TestAllowVLRecursiveSinkFlagVisible(t *testing.T) {
	s := newTestSupervisor(t, &fakeFactory{}, &RecordingSink{}, false)
	if s.AllowVLRecursiveSink() {
		t.Fatal("production guard flag must default false")
	}
	s2 := newTestSupervisor(t, &fakeFactory{}, &RecordingSink{}, true)
	if !s2.AllowVLRecursiveSink() {
		t.Fatal("flag should be readable when set")
	}
}

// TestWaitHealthyTimesOutWhenNotRunning 锁定启动时序修复：未运行的实例 WaitHealthy 超时返回错误
// （调用方据此保持降级，而不是误判就绪）。
func TestWaitHealthyTimesOutWhenNotRunning(t *testing.T) {
	s := newTestSupervisor(t, &fakeFactory{}, &RecordingSink{}, false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.WaitHealthy(ctx, NamespaceHot, 300*time.Millisecond); err == nil {
		t.Fatal("WaitHealthy must fail while the instance is not running")
	}
}

// TestWaitHealthySucceedsWhenReady 就绪后 WaitHealthy 立即返回 nil。
func TestWaitHealthySucceedsWhenReady(t *testing.T) {	bin, sum := writeFakeBinary(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	u := srv.URL
	s, err := New(Options{
		BinaryPath: bin, AssetSHA256: sum, DataRoot: t.TempDir(),
		AuthUsername: "jm", AuthPassword: "local-only",
		Factory: &fakeFactory{}, Sink: &RecordingSink{},
		HealthClient: func(baseURL, user, pass string) (*Client, error) {
			return NewClient(ClientOptions{BaseURL: u, Username: user, Password: pass})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Start(ctx, NamespaceHot); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitHealthy(ctx, NamespaceHot, 2*time.Second); err != nil {
		t.Fatalf("WaitHealthy after ready: %v", err)
	}
}

// TestVLProcessEnvMemoryLimit 锁定 VL 进程内存上限注入（契约 §6.6 RSS 预算）：
// 0 → 默认 512MiB；显式正值；负值 → 不注入。
func TestVLProcessEnvMemoryLimit(t *testing.T) {
	def := vlProcessEnv(0)
	if len(def) != 1 || def[0] != fmt.Sprintf("GOMEMLIMIT=%dB", DefaultVLMemoryLimitBytes) {
		t.Fatalf("0 must inject the default limit, got %v", def)
	}
	if DefaultVLMemoryLimitBytes != 512<<20 {
		t.Fatalf("default VL memory limit should be 512MiB, got %d", DefaultVLMemoryLimitBytes)
	}
	custom := vlProcessEnv(256 << 20)
	if len(custom) != 1 || custom[0] != fmt.Sprintf("GOMEMLIMIT=%dB", 256<<20) {
		t.Fatalf("custom limit not injected: %v", custom)
	}
	if got := vlProcessEnv(-1); got != nil {
		t.Fatalf("negative must disable injection, got %v", got)
	}
}

// TestSetSubBudgetEnforcesReserveRatio 锁定 supervisor 登记子预算时执行 §6.6 的 25% 预留门禁。
func TestSetSubBudgetEnforcesReserveRatio(t *testing.T) {
	s := newTestSupervisor(t, &fakeFactory{}, &RecordingSink{}, false)
	// 未登记总预算时不可判定 → 允许登记子预算。
	if err := s.SetSubBudget("wal", 900<<20); err != nil {
		t.Fatalf("undecidable must pass: %v", err)
	}
	// 登记 1GiB 总预算 → 25% = 256MiB；已登记的 900MiB WAL 超限 → 必须报错。
	if err := s.SetTotalLogBudget(1 << 30); err == nil {
		t.Fatal("total budget admitting nothing must reject over-reserved wal")
	}
	// 重新按合规比例登记则通过。
	s2 := newTestSupervisor(t, &fakeFactory{}, &RecordingSink{}, false)
	if err := s2.SetTotalLogBudget(4 << 30); err != nil {
		t.Fatalf("set total: %v", err)
	}
	if err := s2.SetSubBudget("wal", 800<<20); err != nil {
		t.Fatalf("800MiB of 4GiB (25%%=1GiB) must pass: %v", err)
	}
	if err := s2.SetSubBudget("staging", 300<<20); err == nil {
		t.Fatal("800+300MiB over 1GiB allowed must fail")
	}
	if err := s2.SetSubBudget("wal", -1); err == nil {
		t.Fatal("negative sub-budget must fail")
	}
}

// TestStartReportsFailureWhenProcessExitsImmediately 锁定真机缺陷修复：
// 进程在 Start 成功后立即退出时（如数据目录缺失致 VL 秒退），不得上报 RUNNING，
// 否则控制面会给出「启动成功」的误导结论（实测 rehydrate 即为此现象）。
func TestStartReportsFailureWhenProcessExitsImmediately(t *testing.T) {
	f := &fakeFactory{exitNext: true}
	s := newTestSupervisor(t, f, &RecordingSink{}, false)

	err := s.Start(context.Background(), NamespaceRehydrate)
	if err == nil {
		t.Fatal("立即退出的进程必须让 Start 返回错误，不得静默置为 RUNNING")
	}
	st, statusErr := s.Status(NamespaceRehydrate)
	if statusErr != nil {
		t.Fatal(statusErr)
	}
	if st.State == StateRunning {
		t.Fatalf("进程已退出，状态不得为 RUNNING: %+v", st)
	}
	if st.State != StateFailed {
		t.Fatalf("进程已退出，应标记 FAILED: %+v", st)
	}
	if st.LastError == "" {
		t.Fatal("失败必须带可诊断原因")
	}
}

// TestStartCreatesNamespaceDataDir 锁定缺陷 2：VL 不自建数据根，缺失时秒退，
// 故 Start 必须先行创建 namespace 数据目录。
func TestStartCreatesNamespaceDataDir(t *testing.T) {
	f := &fakeFactory{}
	s := newTestSupervisor(t, f, &RecordingSink{}, false)
	cfg, err := s.Config(NamespaceRehydrate)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(cfg.StorageDataPath); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), NamespaceRehydrate); err != nil {
		t.Fatalf("start: %v", err)
	}
	if info, statErr := os.Stat(cfg.StorageDataPath); statErr != nil || !info.IsDir() {
		t.Fatalf("namespace 数据目录须在启动前创建: err=%v", statErr)
	}
}

// TestStatusReconcilesProcessThatExitsLater 锁定真机缺口：
// 进程可能在启动观察窗口内尚存活、稍后才退出（实测 rehydrate 缺目录即如此）。
// 此时账面若停留 RUNNING，控制面会长期误导运维，故读取状态时须以进程实况校正为 FAILED。
func TestStatusReconcilesProcessThatExitsLater(t *testing.T) {
	f := &fakeFactory{}
	s := newTestSupervisor(t, f, &RecordingSink{}, false)
	if err := s.Start(context.Background(), NamespaceHot); err != nil {
		t.Fatalf("start: %v", err)
	}
	st, err := s.Status(NamespaceHot)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateRunning {
		t.Fatalf("存活进程应报 RUNNING: %+v", st)
	}

	// 模拟「稍后退出」：绕过启动窗口，直接令进程句柄报告已退出。
	f.mu.Lock()
	proc := f.procs[len(f.procs)-1]
	f.mu.Unlock()
	proc.exitImmediately()

	st, err = s.Status(NamespaceHot)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateFailed {
		t.Fatalf("进程已退出，状态须校正为 FAILED: %+v", st)
	}
	if st.LastError == "" {
		t.Fatal("校正为 FAILED 时须带可诊断原因")
	}

	all := s.StatusAll()
	found := false
	for _, item := range all {
		if item.Namespace == NamespaceHot {
			found = true
			if item.State != StateFailed {
				t.Fatalf("StatusAll 同样须校正: %+v", item)
			}
		}
	}
	if !found {
		t.Fatal("StatusAll 应包含 hot")
	}
}
