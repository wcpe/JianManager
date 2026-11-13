package grpc

import (
	"context"
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

// TestMetricsHelperProcess 为指标契约测试提供可识别为 java 的长运行进程与 wrapper→shell→java 进程树。
func TestMetricsHelperProcess(t *testing.T) {
	switch os.Getenv(metricsHelperModeEnv) {
	case "java":
		deadline := time.Now().Add(20 * time.Second)
		var counter uint64
		for time.Now().Before(deadline) {
			counter++
			if counter%1_000_000 == 0 {
				runtime.Gosched()
			}
		}
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

func TestGetInstanceMetrics_RunningWithoutProbeUsesSystemMetrics(t *testing.T) {
	srv, mgr, uuid := startMetricsRunningInstance(t)

	var resp *workerpb.GetInstanceMetricsResponse
	require.Eventually(t, func() bool {
		var err error
		resp, err = srv.GetInstanceMetrics(context.Background(), &workerpb.GetInstanceMetricsRequest{InstanceUuid: uuid})
		return err == nil && resp.MemoryMb > 0 && resp.CpuPercent > 0 && resp.UptimeSeconds > 0
	}, 5*time.Second, 100*time.Millisecond)

	assert.False(t, resp.ProbeAvailable)
	// 无探针且未下发直探端口 → 在线人数显式「不可用」（可用位 false，零值），
	// 不再以 -1 伪值占位。
	assert.False(t, resp.PlayersAvailable)
	assert.Zero(t, resp.Tps)
	assert.Zero(t, resp.OnlinePlayers)
	assert.Greater(t, resp.MemoryMb, int64(0), "无探针时应返回游戏进程 RSS")
	assert.Greater(t, resp.CpuPercent, float64(0), "无探针时应返回游戏进程 CPU")
	assert.Greater(t, resp.UptimeSeconds, float64(0), "无探针时应返回游戏进程运行时长")

	cleanupMetricsInstance(mgr, uuid)
}

func TestGetInstanceResourceSnapshot_AggregatesRootAndDescendants(t *testing.T) {
	srv, _, uuid := startMetricsRunningInstance(t)

	var response *workerpb.GetInstanceResourceSnapshotResponse
	require.Eventually(t, func() bool {
		var err error
		response, err = srv.GetInstanceResourceSnapshot(context.Background(), &workerpb.GetInstanceResourceSnapshotRequest{InstanceUuid: uuid})
		return err == nil && response.RssAvailable && response.ProcessRssBytes > 0 && response.UptimeAvailable && response.UptimeSeconds > 0
	}, 5*time.Second, 100*time.Millisecond)

	require.Greater(t, response.RootPid, int64(0))
	require.GreaterOrEqual(t, response.ProcessCount, int32(1))
	require.Empty(t, response.UnavailableReason)
}

func TestCompleteProcessTree_IncludesDescendants(t *testing.T) {
	javaPath := copyTestExecutableAsJava(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestMetricsHelperProcess$")
	cmd.Env = append(os.Environ(), metricsHelperModeEnv+"=wrapper", metricsHelperJavaEnv+"="+javaPath)
	require.NoError(t, cmd.Start())
	var processes []*psproc.Process
	defer func() {
		for _, process := range processes {
			if process.Pid != int32(cmd.Process.Pid) {
				_ = process.Kill()
			}
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	require.Eventually(t, func() bool {
		root, err := psproc.NewProcess(int32(cmd.Process.Pid))
		if err != nil {
			return false
		}
		processes, err = completeProcessTree(context.Background(), root)
		return err == nil && len(processes) >= 2
	}, 5*time.Second, 50*time.Millisecond)
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

	srv, mgr, uuid := startMetricsRunningInstance(t)
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

	cleanupMetricsInstance(mgr, uuid)
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
	javaPath := copyTestExecutableAsJava(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestMetricsHelperProcess$")
	cmd.Env = append(os.Environ(), metricsHelperModeEnv+"=wrapper", metricsHelperJavaEnv+"="+javaPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	var javaProc *psproc.Process
	require.Eventually(t, func() bool {
		javaProc = resolveGameProc(context.Background(), int32(cmd.Process.Pid))
		if javaProc == nil || javaProc.Pid == int32(cmd.Process.Pid) {
			return false
		}
		name, err := javaProc.Name()
		return err == nil && (strings.EqualFold(name, "java") || strings.EqualFold(name, "java.exe"))
	}, 5*time.Second, 50*time.Millisecond)
	defer javaProc.Kill()

	assert.NotEqual(t, int32(cmd.Process.Pid), javaProc.Pid, "应越过 wrapper 与 shell 选中 java 后代")
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
			metricsHelperModeEnv: "wrapper",
			metricsHelperJavaEnv: javaPath,
		},
		false,
		process.ProcessTypeDirect,
		"",
		"",
		0,
		0,
	))
	require.NoError(t, mgr.Start(uuid))
	t.Cleanup(func() { cleanupMetricsInstance(mgr, uuid) })
	return NewServer(mgr, "node-metrics", nil, nil, nil), mgr, uuid
}

func cleanupMetricsInstance(mgr *process.Manager, uuid string) {
	pid := mgr.GetInstancePID(uuid)
	if pid > 0 {
		if game := resolveGameProc(context.Background(), int32(pid)); game != nil {
			_ = game.Kill()
		}
	}
	_ = mgr.Kill(uuid)
}

func copyTestExecutableAsJava(t *testing.T) string {
	t.Helper()
	source, err := os.Executable()
	require.NoError(t, err)
	name := "java"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(t.TempDir(), name)
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
		return fmt.Sprintf(`%s -test.run=^TestMetricsHelperProcess$`, javaPath)
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
