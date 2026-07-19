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

	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

const fr343HelperModeEnv = "JM_FR343_HELPER_MODE"
const fr343HelperJavaEnv = "JM_FR343_HELPER_JAVA"

// TestFR343MetricsHelperProcess 为指标契约测试提供可识别为 java 的长运行进程与 wrapper→shell→java 进程树。
func TestFR343MetricsHelperProcess(t *testing.T) {
	switch os.Getenv(fr343HelperModeEnv) {
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
		cmd := helperShellCommand(os.Getenv(fr343HelperJavaEnv))
		cmd.Env = append(os.Environ(), fr343HelperModeEnv+"=java")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
}

func TestGetInstanceMetrics_RunningWithoutProbeUsesSystemMetrics(t *testing.T) {
	srv, mgr, uuid := startFR343RunningInstance(t)

	var resp *workerpb.GetInstanceMetricsResponse
	require.Eventually(t, func() bool {
		var err error
		resp, err = srv.GetInstanceMetrics(context.Background(), &workerpb.GetInstanceMetricsRequest{InstanceUuid: uuid})
		return err == nil && resp.MemoryMb > 0 && resp.CpuPercent > 0 && resp.UptimeSeconds > 0
	}, 5*time.Second, 100*time.Millisecond)

	assert.False(t, resp.ProbeAvailable)
	assert.Equal(t, float32(-1), resp.Tps)
	assert.Equal(t, int32(-1), resp.OnlinePlayers)
	assert.Greater(t, resp.MemoryMb, int64(0), "无探针时应返回游戏进程 RSS")
	assert.Greater(t, resp.CpuPercent, float64(0), "无探针时应返回游戏进程 CPU")
	assert.Greater(t, resp.UptimeSeconds, float64(0), "无探针时应返回游戏进程运行时长")

	cleanupFR343Instance(mgr, uuid)
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

	srv, mgr, uuid := startFR343RunningInstance(t)
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

	cleanupFR343Instance(mgr, uuid)
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
	const uuid = "fr343-stopped"
	require.NoError(t, mgr.Create(uuid, "停止实例", "unused", "", t.TempDir(), nil, false, process.ProcessTypeDirect, "", "", port, 0))
	srv := NewServer(mgr, "node-fr343", nil, nil, nil)

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
	cmd := exec.Command(os.Args[0], "-test.run=^TestFR343MetricsHelperProcess$")
	cmd.Env = append(os.Environ(), fr343HelperModeEnv+"=wrapper", fr343HelperJavaEnv+"="+javaPath)
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

func startFR343RunningInstance(t *testing.T) (*Server, *process.Manager, string) {
	t.Helper()
	javaPath := copyTestExecutableAsJava(t)
	mgr := process.NewManager(t.TempDir())
	uuid := "fr343-running-" + strings.ReplaceAll(t.Name(), "/", "-")
	require.NoError(t, mgr.Create(
		uuid,
		"运行实例",
		helperInvocation(os.Args[0]),
		"",
		t.TempDir(),
		map[string]string{
			fr343HelperModeEnv: "wrapper",
			fr343HelperJavaEnv: javaPath,
		},
		false,
		process.ProcessTypeDirect,
		"",
		"",
		0,
		0,
	))
	require.NoError(t, mgr.Start(uuid))
	t.Cleanup(func() { cleanupFR343Instance(mgr, uuid) })
	return NewServer(mgr, "node-fr343", nil, nil, nil), mgr, uuid
}

func cleanupFR343Instance(mgr *process.Manager, uuid string) {
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
		return fmt.Sprintf(`%s -test.run=^TestFR343MetricsHelperProcess$`, javaPath)
	}
	return fmt.Sprintf("'%s' -test.run=^TestFR343MetricsHelperProcess$", strings.ReplaceAll(javaPath, "'", `'\"'\"'`))
}

func helperShellCommand(javaPath string) *exec.Cmd {
	command := helperInvocation(javaPath)
	if runtime.GOOS == "windows" {
		return exec.Command("cmd.exe", "/d", "/s", "/c", command)
	}
	return exec.Command("sh", "-c", command)
}
