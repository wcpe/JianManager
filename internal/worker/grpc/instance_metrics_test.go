package grpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	psproc "github.com/shirou/gopsutil/v4/process"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/metrics"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

const metricsHelperModeEnv = "JM_METRICS_HELPER_MODE"
const metricsHelperJavaEnv = "JM_METRICS_HELPER_JAVA"
const metricsHelperQuoteEnv = "JM_METRICS_HELPER_QUOTE"

// TestMetricsHelperProcess 为指标契约测试提供可识别为 java 的长运行进程与 wrapper→shell→java 进程树。
func TestMetricsHelperProcess(t *testing.T) {
	switch os.Getenv(metricsHelperModeEnv) {
	case "java":
		runMetricsJava(t, os.Getenv(metricsHelperJavaEnv))
		os.Exit(0)
	case "wrapper":
		cmd := helperShellCommand(os.Getenv(metricsHelperJavaEnv))
		cmd.Env = append(os.Environ(), metricsHelperModeEnv+"=java")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
}

func runMetricsJava(t *testing.T, javaPath string) {
	t.Helper()
	require.NotEmpty(t, javaPath)
	deadline := time.Now().Add(60 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		if _, err := os.Stat(javaPath + ".stop"); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		// 指标用例需要真实 CPU 样本，不能用纯休眠进程代替。
		workUntil := time.Now().Add(25 * time.Millisecond)
		for time.Now().Before(workUntil) {
		}
		if !ready {
			proc, err := psproc.NewProcess(int32(os.Getpid()))
			require.NoError(t, err)
			created, err := proc.CreateTime()
			require.NoError(t, err)
			data, err := json.Marshal(metricsJavaReady{PID: proc.Pid, CreateTime: created})
			require.NoError(t, err)
			// 先写完身份再发布，避免父进程读到半条记录。
			staged := javaPath + ".ready.tmp"
			require.NoError(t, os.WriteFile(staged, data, 0o600))
			require.NoError(t, os.Rename(staged, javaPath+".ready"))
			ready = true
		}
		runtime.Gosched()
	}
}

func TestMetricsProcessRefRejectsIdentityMismatch(t *testing.T) {
	proc, err := psproc.NewProcess(int32(os.Getpid()))
	require.NoError(t, err)
	ref, err := captureMetricsProcessRef(proc)
	require.NoError(t, err)
	watch, err := watchMetricsProcess(ref)
	require.NoError(t, err)
	t.Cleanup(watch.close)
	exited, err := watch.exited()
	require.NoError(t, err)
	require.False(t, exited)

	stale := ref
	stale.createTime++
	_, err = watchMetricsProcess(stale)
	require.Error(t, err, "创建时间不一致时不得接纳 PID")

	differentName := ref
	differentName.name += "-other"
	_, err = watchMetricsProcess(differentName)
	require.Error(t, err, "进程名不一致时不得接纳 PID")

	differentPath := ref
	differentPath.exe = filepath.Join(t.TempDir(), filepath.Base(ref.exe))
	_, err = watchMetricsProcess(differentPath)
	require.Error(t, err, "绝对路径不一致时不得接纳 PID")
}

func TestMetricsProcessRefCleanupWaitsWithoutTreeSnapshot(t *testing.T) {
	var fixture *metricsProcessFixture
	if !t.Run("Java 已就绪但未采集整树", func(t *testing.T) {
		fixture = startMetricsWrapper(t, copyTestExecutableAsJava(t))
		require.True(t, pollMetricsCondition(5*time.Second, 50*time.Millisecond, func() bool {
			_, err := os.Stat(fixture.javaPath + ".ready")
			return err == nil
		}))
		// 故意不调用 waitReady，模拟在树采集前终止测试。
		require.Nil(t, fixture.javaWatch)
		require.Empty(t, fixture.watches)
	}) {
		t.FailNow()
	}
	require.NotNil(t, fixture.javaWatch, "清理必须独立接收 Java 身份并确认退出")
	require.Empty(t, fixture.watches, "该路径不能依赖已采集的树快照")
}

func TestGetInstanceMetrics_RunningWithoutProbeUsesSystemMetrics(t *testing.T) {
	srv, _, uuid := startMetricsRunningInstance(t)

	var resp *workerpb.GetInstanceMetricsResponse
	require.True(t, pollMetricsCondition(5*time.Second, 100*time.Millisecond, func() bool {
		var err error
		resp, err = srv.GetInstanceMetrics(context.Background(), &workerpb.GetInstanceMetricsRequest{InstanceUuid: uuid})
		return err == nil && resp.MemoryMb > 0 && resp.CpuPercent > 0 && resp.UptimeSeconds > 0
	}))

	assert.False(t, resp.ProbeAvailable)
	// 无探针且未下发直探端口 → 在线人数显式「不可用」（可用位 false，零值），
	// 不再以 -1 伪值占位。
	assert.False(t, resp.PlayersAvailable)
	assert.Zero(t, resp.Tps)
	assert.Zero(t, resp.OnlinePlayers)
	assert.Greater(t, resp.MemoryMb, int64(0), "无探针时应返回游戏进程 RSS")
	assert.Greater(t, resp.CpuPercent, float64(0), "无探针时应返回游戏进程 CPU")
	assert.Greater(t, resp.UptimeSeconds, float64(0), "无探针时应返回游戏进程运行时长")
}

func TestGetInstanceResourceSnapshot_AggregatesRootAndDescendants(t *testing.T) {
	srv, _, uuid := startMetricsRunningInstance(t)

	var response *workerpb.GetInstanceResourceSnapshotResponse
	require.True(t, pollMetricsCondition(5*time.Second, 100*time.Millisecond, func() bool {
		var err error
		response, err = srv.GetInstanceResourceSnapshot(context.Background(), &workerpb.GetInstanceResourceSnapshotRequest{InstanceUuid: uuid})
		return err == nil && response.RssAvailable && response.ProcessRssBytes > 0 && response.UptimeAvailable && response.UptimeSeconds > 0
	}))

	require.Greater(t, response.RootPid, int64(0))
	require.GreaterOrEqual(t, response.ProcessCount, int32(1))
	require.Empty(t, response.UnavailableReason)
}

func TestCompleteProcessTree_IncludesDescendants(t *testing.T) {
	fixture := startMetricsWrapper(t, copyTestExecutableAsJava(t))
	javaPID := fixture.waitReady(t)
	root, err := psproc.NewProcess(fixture.rootPID)
	require.NoError(t, err)
	processes, err := completeProcessTree(context.Background(), root)
	require.NoError(t, err)
	pids := make([]int32, 0, len(processes))
	for _, proc := range processes {
		pids = append(pids, proc.Pid)
	}
	require.Contains(t, pids, fixture.rootPID)
	require.Contains(t, pids, javaPID, "进程树必须包含实际 Java 后代，而不仅是中间 shell")
}

func TestGetInstanceResourceSnapshot_StoppedDoesNotFabricateMetrics(t *testing.T) {
	mgr := process.NewManager(t.TempDir())
	const uuid = "metrics-stopped"
	require.NoError(t, mgr.Create(uuid, "停止实例", "unused", "", t.TempDir(), nil, false, process.ProcessTypeDirect, "", "", 0, 0))
	srv := NewServer(mgr, "node-metrics", nil, nil, nil)

	response, err := srv.GetInstanceResourceSnapshot(context.Background(), &workerpb.GetInstanceResourceSnapshotRequest{InstanceUuid: uuid})
	require.NoError(t, err)
	assert.False(t, response.RssAvailable)
	assert.False(t, response.CpuAvailable)
	assert.False(t, response.UptimeAvailable)
	assert.NotEmpty(t, response.UnavailableReason)
	assert.Zero(t, response.ProcessRssBytes)
}

func TestGetInstanceMetrics_ProbeOverridesSystemMetrics(t *testing.T) {
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `serverprobe_tps{window="1m"} 19.5
serverprobe_players_online 7
serverprobe_heap_used_bytes 268435456
serverprobe_heap_max_bytes 1073741824
serverprobe_mspt_seconds{quantile="avg"} 0.0125
serverprobe_threads 42
serverprobe_system_cpu_load 0.125
serverprobe_uptime_seconds 777
`)
	}))
	defer probe.Close()
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(probe.URL, "http://"))
	require.NoError(t, err)
	var port int
	_, err = fmt.Sscanf(portText, "%d", &port)
	require.NoError(t, err)

	srv, _, uuid := startMetricsRunningInstance(t)
	resp, err := srv.GetInstanceMetrics(context.Background(), &workerpb.GetInstanceMetricsRequest{InstanceUuid: uuid, ProbePort: int32(port)})
	require.NoError(t, err)

	assert.True(t, resp.ProbeAvailable)
	assert.InDelta(t, 19.5, resp.Tps, 0.01)
	assert.Equal(t, int32(7), resp.OnlinePlayers)
	assert.Equal(t, int64(256), resp.MemoryMb)
	assert.Equal(t, int64(1024), resp.HeapMaxMb)
	assert.InDelta(t, 12.5, resp.MsptMillis, 0.01)
	assert.Equal(t, int32(42), resp.Threads)
	assert.InDelta(t, 12.5, resp.CpuPercent, 0.01)
	assert.InDelta(t, 777, resp.UptimeSeconds, 0.01)
}

func TestGetInstanceMetrics_UsesFreshPushedSnapshot(t *testing.T) {
	srv, _, uuid := startMetricsRunningInstance(t)
	store := metrics.NewPushedSnapshotStore()
	store.Store(uuid, metrics.ProbeSnapshot{
		TPS:           19.5,
		MSPTAvgMillis: 12.5,
		PlayersOnline: 7,
		HeapUsedBytes: 256 * 1024 * 1024,
		HeapMaxBytes:  1024 * 1024 * 1024,
		Threads:       42,
		SystemCPULoad: 0.125,
		UptimeSeconds: 777,
		Worlds:        map[string]metrics.WorldStat{"world": {LoadedChunks: 1200}},
	})
	srv.SetProbeSnapshotReader(store)

	resp, err := srv.GetInstanceMetrics(context.Background(), &workerpb.GetInstanceMetricsRequest{InstanceUuid: uuid})
	require.NoError(t, err)
	assert.True(t, resp.ProbeAvailable)
	assert.InDelta(t, 19.5, resp.Tps, 0.01)
	assert.Equal(t, int32(7), resp.OnlinePlayers)
	assert.Equal(t, int64(256), resp.MemoryMb)
	assert.Equal(t, int64(1024), resp.HeapMaxMb)
	assert.InDelta(t, 12.5, resp.MsptMillis, 0.01)
	assert.Equal(t, int32(42), resp.Threads)
	assert.InDelta(t, 12.5, resp.CpuPercent, 0.01)
	assert.InDelta(t, 777, resp.UptimeSeconds, 0.01)
}

func TestGetInstanceMetrics_DroppedPushedSnapshotFallsBackToHTTP(t *testing.T) {
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `serverprobe_tps{window="1m"} 17.5
serverprobe_players_online 3
serverprobe_heap_used_bytes 134217728
`)
	}))
	defer probe.Close()
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(probe.URL, "http://"))
	require.NoError(t, err)
	var port int
	_, err = fmt.Sscanf(portText, "%d", &port)
	require.NoError(t, err)

	srv, _, uuid := startMetricsRunningInstance(t)
	store := metrics.NewPushedSnapshotStore()
	store.Store(uuid, metrics.ProbeSnapshot{TPS: 99, PlayersOnline: 99})
	store.Forget(uuid)
	srv.SetProbeSnapshotReader(store)

	resp, err := srv.GetInstanceMetrics(context.Background(), &workerpb.GetInstanceMetricsRequest{InstanceUuid: uuid, ProbePort: int32(port)})
	require.NoError(t, err)
	assert.True(t, resp.ProbeAvailable)
	assert.InDelta(t, 17.5, resp.Tps, 0.01)
	assert.Equal(t, int32(3), resp.OnlinePlayers)
	assert.Equal(t, int64(128), resp.MemoryMb)
}

func TestGetInstanceMetrics_PushedSnapshotsAreIsolatedByInstance(t *testing.T) {
	srvA, _, uuidA := startMetricsRunningInstanceWithSuffix(t, "a")
	srvB, _, uuidB := startMetricsRunningInstanceWithSuffix(t, "b")
	store := metrics.NewPushedSnapshotStore()
	store.Store(uuidA, metrics.ProbeSnapshot{TPS: 11, PlayersOnline: 1})
	store.Store(uuidB, metrics.ProbeSnapshot{TPS: 22, PlayersOnline: 2})
	srvA.SetProbeSnapshotReader(store)
	srvB.SetProbeSnapshotReader(store)

	respA, err := srvA.GetInstanceMetrics(context.Background(), &workerpb.GetInstanceMetricsRequest{InstanceUuid: uuidA})
	require.NoError(t, err)
	respB, err := srvB.GetInstanceMetrics(context.Background(), &workerpb.GetInstanceMetricsRequest{InstanceUuid: uuidB})
	require.NoError(t, err)
	assert.InDelta(t, 11, respA.Tps, 0.01)
	assert.Equal(t, int32(1), respA.OnlinePlayers)
	assert.InDelta(t, 22, respB.Tps, 0.01)
	assert.Equal(t, int32(2), respB.OnlinePlayers)
}

func TestGetInstanceMetrics_StoppedDoesNotFabricateMetrics(t *testing.T) {
	var probeCalls int
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probeCalls++
		_, _ = io.WriteString(w, "serverprobe_tps 20\n")
	}))
	defer probe.Close()
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(probe.URL, "http://"))
	require.NoError(t, err)
	var port int
	_, err = fmt.Sscanf(portText, "%d", &port)
	require.NoError(t, err)

	mgr := process.NewManager(t.TempDir())
	const uuid = "metrics-stopped"
	require.NoError(t, mgr.Create(uuid, "停止实例", "unused", "", t.TempDir(), nil, false, process.ProcessTypeDirect, "", "", port, 0))
	srv := NewServer(mgr, "node-metrics", nil, nil, nil)

	resp, err := srv.GetInstanceMetrics(context.Background(), &workerpb.GetInstanceMetricsRequest{InstanceUuid: uuid, ProbePort: int32(port)})
	require.NoError(t, err)
	assert.Equal(t, 0, probeCalls, "STOPPED 不应抓取探针或伪造运行指标")
	assert.False(t, resp.ProbeAvailable)
	assert.Zero(t, resp.Tps)
	assert.Zero(t, resp.OnlinePlayers)
	assert.Zero(t, resp.MemoryMb)
	assert.Zero(t, resp.CpuPercent)
	assert.Zero(t, resp.UptimeSeconds)
}

func TestResolveGameProc_WrapperShellJava(t *testing.T) {
	fixture := startMetricsWrapper(t, copyTestExecutableAsJava(t))
	javaPID := fixture.waitReady(t)
	javaProc := resolveGameProc(context.Background(), fixture.rootPID)
	require.NotNil(t, javaProc)
	assert.Equal(t, javaPID, javaProc.Pid, "应越过 wrapper 与 shell 选中本用例启动的 Java 后代")
}

func startMetricsWrapper(t *testing.T, javaPath string) *metricsProcessFixture {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestMetricsHelperProcess$")
	cmd.Env = append(os.Environ(), metricsHelperModeEnv+"=wrapper", metricsHelperJavaEnv+"="+javaPath, metricsHelperQuoteEnv+"=\"")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	return newMetricsProcessFixture(t, javaPath, cmd.Process.Pid, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	})
}

func startMetricsRunningInstance(t *testing.T) (*Server, *process.Manager, string) {
	return startMetricsRunningInstanceWithSuffix(t, "")
}

func startMetricsRunningInstanceWithSuffix(t *testing.T, suffix string) (*Server, *process.Manager, string) {
	t.Helper()
	javaPath := copyTestExecutableAsJava(t)
	mgr := process.NewManager(t.TempDir())
	mgr.SetMemGuard(process.MemGuardConfig{Disabled: true})
	uuid := "metrics-running-" + strings.ReplaceAll(t.Name(), "/", "-")
	if suffix != "" {
		uuid += "-" + suffix
	}
	require.NoError(t, mgr.Create(
		uuid,
		"运行实例",
		helperInvocation(os.Args[0]),
		"",
		t.TempDir(),
		map[string]string{
			metricsHelperModeEnv:  "wrapper",
			metricsHelperJavaEnv:  javaPath,
			metricsHelperQuoteEnv: `"`,
		},
		false,
		process.ProcessTypeDirect,
		"",
		"",
		0,
		0,
	))
	require.NoError(t, mgr.Start(uuid))
	fixture := newMetricsProcessFixture(t, javaPath, mgr.GetInstancePID(uuid), func() bool {
		return mgr.GetInstancePID(uuid) == 0
	})
	fixture.waitReady(t)
	return NewServer(mgr, "node-metrics", nil, nil, nil), mgr, uuid
}

type metricsProcessRef struct {
	pid        int32
	name       string
	exe        string
	createTime int64
}

func captureMetricsProcessRef(proc *psproc.Process) (metricsProcessRef, error) {
	name, err := proc.Name()
	if err != nil {
		return metricsProcessRef{}, err
	}
	createTime, err := proc.CreateTime()
	if err != nil {
		return metricsProcessRef{}, err
	}
	exe, err := proc.Exe()
	if err != nil {
		return metricsProcessRef{}, err
	}
	if proc.Pid <= 0 || name == "" || createTime <= 0 || !filepath.IsAbs(exe) {
		return metricsProcessRef{}, fmt.Errorf("PID %d 的进程身份不完整", proc.Pid)
	}
	return metricsProcessRef{pid: proc.Pid, name: name, exe: exe, createTime: createTime}, nil
}

func sameMetricsExecutable(a, b string) bool {
	if !filepath.IsAbs(a) || !filepath.IsAbs(b) {
		return false
	}
	equal := func(a, b string) bool {
		if runtime.GOOS == "windows" {
			return strings.EqualFold(a, b)
		}
		return a == b
	}
	if equal(filepath.Clean(a), filepath.Clean(b)) {
		return true
	}
	// 进程 API 可能返回物理路径，而 TMPDIR 允许经过符号链接。
	realA, errA := filepath.EvalSymlinks(a)
	realB, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && equal(realA, realB)
}

type metricsJavaReady struct {
	PID        int32
	CreateTime int64
}

var errMetricsProcessGone = errors.New("进程身份已不在该 PID 上")

type metricsProcessFixture struct {
	javaPath  string
	rootPID   int32
	rootDone  func() bool
	javaWatch *metricsProcessWatch
	watches   []metricsProcessWatch
}

func newMetricsProcessFixture(t *testing.T, javaPath string, rootPID int, rootDone func() bool) *metricsProcessFixture {
	t.Helper()
	fixture := &metricsProcessFixture{javaPath: javaPath, rootPID: int32(rootPID), rootDone: rootDone}
	t.Cleanup(func() { fixture.cleanup(t) })
	return fixture
}

// metricsReadyBudget 是等夹具就绪的上限，不是业务断言。
// Windows CI 上四层进程首次拉起可能到秒级，给足余量避免把冷启动误判成缺陷；
// 超时仍失败，并在失败时输出现场。
const metricsReadyBudget = 20 * time.Second

func (f *metricsProcessFixture) waitReady(t *testing.T) int32 {
	t.Helper()
	require.Positive(t, f.rootPID)
	require.NotEqual(t, int32(os.Getpid()), f.rootPID)
	var lastErr error
	started := time.Now()
	ok := pollMetricsCondition(metricsReadyBudget, 50*time.Millisecond, func() bool {
		found, err := f.watchJava()
		lastErr = err
		if !found || err != nil {
			return false
		}
		f.watches, lastErr = f.watchTree()
		return lastErr == nil
	})
	require.True(t, ok, "未能确认自有 Java 已就绪并记录完整进程树（耗时 %s）: %v\n%s",
		time.Since(started).Round(time.Millisecond), lastErr, f.diagnostics())
	return f.javaWatch.pid
}

// diagnostics 在就绪超时时给出可判定的现场：根进程是否还在、握手文件走到哪一步、
// 以及为什么没走到。缺了这些信息，超时只能靠猜。
func (f *metricsProcessFixture) diagnostics() string {
	var b strings.Builder
	fmt.Fprintf(&b, "根 PID %d 已回收=%v；Java 监测句柄=%v\n", f.rootPID, f.rootDone(), f.javaWatch != nil)
	for _, suffix := range []string{".ready", ".ready.tmp", ".stop"} {
		info, err := os.Stat(f.javaPath + suffix)
		if err != nil {
			fmt.Fprintf(&b, "  %s: %v\n", suffix, err)
			continue
		}
		fmt.Fprintf(&b, "  %s: 存在，大小 %d，修改时间 %s\n", suffix, info.Size(), info.ModTime().Format(time.RFC3339Nano))
	}
	if data, err := os.ReadFile(f.javaPath + ".ready"); err == nil {
		fmt.Fprintf(&b, "  .ready 内容: %s\n", strings.TrimSpace(string(data)))
	}
	if proc, err := psproc.NewProcess(f.rootPID); err == nil {
		if children, cerr := proc.Children(); cerr == nil {
			for _, child := range children {
				name, _ := child.Name()
				fmt.Fprintf(&b, "  子进程 PID %d 名称 %q\n", child.Pid, name)
			}
		}
	}
	return b.String()
}

// watchJava 独立于根 PID 和树遍历；即使启动检查失败，清理也可接收 Java 自己发布的身份。
func (f *metricsProcessFixture) watchJava() (bool, error) {
	if f.javaWatch != nil {
		return true, nil
	}
	data, err := os.ReadFile(f.javaPath + ".ready")
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var ready metricsJavaReady
	if err := json.Unmarshal(data, &ready); err != nil {
		return true, err
	}
	if ready.PID <= 0 || ready.PID == int32(os.Getpid()) || ready.PID == f.rootPID || ready.CreateTime <= 0 {
		return true, fmt.Errorf("Java 就绪记录的进程身份无效: %+v", ready)
	}
	ref := metricsProcessRef{pid: ready.PID, createTime: ready.CreateTime, exe: f.javaPath, name: filepath.Base(f.javaPath)}
	watch, err := watchMetricsProcess(ref)
	if err != nil {
		// 无法打开可读句柄不等同于已退出；仅在系统确认 PID 不存在时承认已退出。
		if exists, existsErr := psproc.PidExists(ref.pid); existsErr == nil && !exists {
			return true, errMetricsProcessGone
		}
		return true, err
	}
	f.javaWatch = &watch
	return true, nil
}

func (f *metricsProcessFixture) watchTree() ([]metricsProcessWatch, error) {
	root, err := psproc.NewProcess(f.rootPID)
	if err != nil {
		return nil, err
	}
	procs, err := completeProcessTree(context.Background(), root)
	if err != nil {
		return nil, err
	}
	watches := make([]metricsProcessWatch, 0, len(procs))
	javaInTree := false
	adopted := false
	defer func() {
		if !adopted {
			for _, watch := range watches {
				watch.close()
			}
		}
	}()
	for _, proc := range procs {
		if proc.Pid == f.javaWatch.pid {
			javaInTree = true // Java 已有独立监测句柄，无需重复打开。
			continue
		}
		ref, err := captureMetricsProcessRef(proc)
		if err != nil {
			return nil, err
		}
		watch, err := watchMetricsProcess(ref)
		if err != nil {
			return nil, err
		}
		watches = append(watches, watch)
	}
	if !javaInTree || f.rootDone() {
		return nil, fmt.Errorf("根 PID %d 下未找到仍由该根管理的 Java %q", f.rootPID, f.javaPath)
	}
	exited, err := f.javaWatch.exited()
	if err != nil {
		return nil, err
	}
	if exited {
		return nil, fmt.Errorf("Java 在进程树记录完成前已退出")
	}
	adopted = true
	return watches, nil
}

func (f *metricsProcessFixture) cleanup(t *testing.T) {
	t.Helper()
	defer func() {
		if f.javaWatch != nil {
			f.javaWatch.close()
		}
		for _, watch := range f.watches {
			watch.close()
		}
	}()
	// 如已发布身份，尽可能先取得监测句柄，再让 Java 退出，避免这两步互相抢跑。
	_, _ = f.watchJava()
	assert.NoError(t, os.WriteFile(f.javaPath+".stop", nil, 0o600))
	var pending string
	ok := pollMetricsCondition(15*time.Second, 50*time.Millisecond, func() bool {
		rootDone := f.rootDone()
		// 等待根退出期间仍检查就绪记录，覆盖 stop 写入前已启动但稍后才发布身份的 Java。
		if _, err := f.watchJava(); err != nil && !errors.Is(err, errMetricsProcessGone) {
			pending = fmt.Sprintf("无法确认 Java 进程身份: %v", err)
			return false
		}
		if f.javaWatch != nil {
			if exited, err := f.javaWatch.exited(); err != nil || !exited {
				pending = fmt.Sprintf("Java PID %d 尚未确认退出: %v", f.javaWatch.pid, err)
				return false
			}
		}
		if !rootDone {
			pending = fmt.Sprintf("根 PID %d 尚未回收", f.rootPID)
			return false
		}
		for _, watch := range f.watches {
			if exited, err := watch.exited(); err != nil || !exited {
				pending = fmt.Sprintf("PID %d 尚未确认退出: %v", watch.pid, err)
				return false
			}
		}
		return true
	})
	assert.True(t, ok, "删除夹具临时目录前必须确认进程树退出: %s", pending)
}

func pollMetricsCondition(timeout, interval time.Duration, condition func() bool) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticks := time.NewTicker(interval)
	defer ticks.Stop()
	for {
		if condition() {
			return true
		}
		select {
		case <-deadline.C:
			return false
		case <-ticks.C:
		}
	}
}

func copyTestExecutableAsJava(t *testing.T) string {
	t.Helper()
	source, err := os.Executable()
	require.NoError(t, err)
	name := "java"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dir := filepath.Join(t.TempDir(), "metrics java")
	require.NoError(t, os.Mkdir(dir, 0o755))
	target := filepath.Join(dir, name)
	in, err := os.Open(source)
	require.NoError(t, err)
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	require.NoError(t, err)
	_, err = io.Copy(out, in)
	require.NoError(t, err)
	require.NoError(t, out.Close())
	return target
}

func helperInvocation(javaPath string) string {
	if runtime.GOOS == "windows" {
		// 通过 cmd 环境变量展开双引号，避免 exec.Command 将 /c 单参数中的引号转义为字面反斜杠。
		return fmt.Sprintf("call %%%s%%%s%%%s%% -test.run=^TestMetricsHelperProcess$", metricsHelperQuoteEnv, javaPath, metricsHelperQuoteEnv)
	}
	return fmt.Sprintf("'%s' -test.run=^TestMetricsHelperProcess$", strings.ReplaceAll(javaPath, "'", `'\"'\"'`))
}

func helperShellCommand(javaPath string) *exec.Cmd {
	command := helperInvocation(javaPath)
	if runtime.GOOS == "windows" {
		return exec.Command("cmd.exe", "/d", "/s", "/c", command)
	}
	return exec.Command("sh", "-c", command)
}
