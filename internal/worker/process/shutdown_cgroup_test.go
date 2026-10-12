package process

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// 本文件锁定「Worker 关闭/重启路径不得杀实例」契约的真实进程证据（2026-10-01 生产事故）：
//
// 现场：`systemctl restart jm-prod-worker` 后 11 个 wrapper 全部 `err="signal: terminated"`，且 11 个
// 实例的 Java 自身日志在同一秒打出 `Stopping server`——根因是 systemd 对 Worker **单元 cgroup** 内
// 全部进程（KillMode=control-group、KillSignal=15）发 SIGTERM，`setsid` 逃得出进程组、逃不出 cgroup。
// Worker 代码侧的关闭路径当时（且现在）只对 daemon 实例断连（`Manager.StopAll` 的 daemon 分支），
// 从不杀 wrapper——杀它的是 unit cgroup 连坐。
//
// 因此本用例同时钉住两侧：
//
//	① 关闭路径只断连（StopAll 后 wrapper/java 存活）；
//	② daemon 子树不在 Worker 单元 cgroup 内（wrapper 自迁，见 internal/worker/daemon/cgroup.go），
//	   故「单元级 cgroup SIGTERM」打不到它 —— 这条在旧行为下必红；
//	③ 关闭后可被下一次启动接管（RecoverDaemonInstances），且**显式 Stop 依旧能停**（语义不变）。
const (
	envHelperWrapperCfg = "JM_TEST_HELPER_WRAPPER_CFG"
	envLabCgroupRoot    = "JM_TEST_CGROUP_ROOT"
)

// TestDaemonWrapperHelperProcess 是本文件用例以子进程方式调用的 wrapper 入口：
// 直接跑生产 `daemon.Run`，使「wrapper 自迁 cgroup」这段生产代码在**真实 cgroup** 上被执行。
func TestDaemonWrapperHelperProcess(t *testing.T) {
	raw := os.Getenv(envHelperWrapperCfg)
	if raw == "" {
		t.Skip("非子进程调用（由 TestDaemonSubtreeSurvivesWorkerUnitCgroupKill 注入配置后 exec）")
	}
	var cfg daemon.WrapperConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "解析 wrapper 配置失败: %v\n", err)
		os.Exit(2)
	}
	if err := daemon.Run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "wrapper 退出: %v\n", err)
		os.Exit(3)
	}
	os.Exit(0)
}

// TestDaemonSubtreeSurvivesWorkerUnitCgroupKill 是「重启不再拖倒实例」的可转红回归：
// 把关闭路径与单元级 cgroup SIGTERM 依次施加到真实 wrapper 子树上，断言 wrapper 与 java 存活、
// 可被下次启动接管，且显式 Stop 仍能停（改回旧行为——wrapper 留在单元 cgroup 且不自迁——即红）。
func TestDaemonSubtreeSurvivesWorkerUnitCgroupKill(t *testing.T) {
	mountRoot, labRel, ok := pickLabCgroupRoot(t)
	if !ok {
		t.Skip("无可用 cgroup 写权限：本用例需在用户可写的 cgroup 内运行（如 systemd-run --user --scope go test …）")
	}
	uuid := fmt.Sprintf("shutdown-contract-%d", os.Getpid())
	unitRel := path.Join(labRel, "lab-worker.service") // 伪造的「Worker systemd 单元 cgroup」
	escapeRel := path.Join(labRel, "jianmanager-daemons", uuid)
	unitDir := filepath.Join(mountRoot, unitRel)
	escapeDir := filepath.Join(mountRoot, escapeRel)
	require.NoError(t, os.MkdirAll(unitDir, 0o755))

	pidDir := t.TempDir()
	cfg := daemon.WrapperConfig{
		InstanceUUID: uuid,
		StartCommand: "sleep 300", // 替身 java：常驻且不消费 stdin
		WorkDir:      t.TempDir(),
		PIDDir:       pidDir,
		AutoRestart:  false,
		// 替身 java 不消费 stdin，优雅停止只能靠超时强杀兜底；给 3s 让「显式 Stop 仍能停」快速收敛
		// （真实部署由 CP 下发平台设置 graceful_stop.timeout，见 FR-063）。
		GracefulStopTimeoutSeconds: 3,
	}
	rawCfg, err := json.Marshal(cfg)
	require.NoError(t, err)

	logPath := filepath.Join(t.TempDir(), "wrapper.log")
	logFile, err := os.Create(logPath)
	require.NoError(t, err)
	defer logFile.Close()

	// 起 wrapper：先用 sh 把自己落进「假 Worker 单元 cgroup」，再 exec 测试二进制内的 helper 入口
	// （exec 保持 PID，故 wrapper 一出生就在单元 cgroup 内，等价于 systemd 下 Worker fork 出的 daemon
	// 子进程；避免「先起后迁」与 wrapper 开头自迁的时序竞争）。
	script := fmt.Sprintf("echo $$ > %s; exec %s -test.run=^TestDaemonWrapperHelperProcess$",
		shellQuote(filepath.Join(unitDir, "cgroup.procs")), shellQuote(os.Args[0]))
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(), envHelperWrapperCfg+"="+string(rawCfg))
	cmd.Stdout, cmd.Stderr = logFile, logFile
	require.NoError(t, cmd.Start())

	// wrapper 是本用例的子进程：退出后若无人回收会停留在僵尸态，而 IsPIDAlive（signal 0）对僵尸恒为真
	// ——生产里这一步由 Worker 的 reapWrapper（cmd.Wait）承担，测试须自备回收，否则 ⑤「显式 Stop
	// 让 wrapper 退出」会假失败。
	exited := make(chan struct{})
	var reapOnce sync.Once
	reap := func() {
		reapOnce.Do(func() {
			_, _ = cmd.Process.Wait()
			close(exited)
		})
	}
	go reap()

	var rec daemon.PIDRecord
	t.Cleanup(func() {
		// 兜底清理：先杀 java 再杀 wrapper，最后回收实验室 cgroup 目录（仅空目录可删）。
		if rec.JavaPID > 0 {
			_ = syscall.Kill(rec.JavaPID, syscall.SIGKILL)
		}
		_ = cmd.Process.Kill()
		reap()
		<-exited
		time.Sleep(200 * time.Millisecond)
		_ = os.Remove(escapeDir)
		_ = os.Remove(filepath.Join(mountRoot, path.Join(labRel, "jianmanager-daemons")))
		_ = os.Remove(unitDir)
		_ = os.Remove(filepath.Join(mountRoot, labRel))
		if t.Failed() {
			if b, err := os.ReadFile(logPath); err == nil {
				t.Logf("wrapper 日志:\n%s", b)
			}
		}
	})

	// 等 wrapper 写出「wrapper pid + java pid」记录。
	require.Eventually(t, func() bool {
		r, err := daemon.NewPIDFile(filepath.Join(pidDir, uuid+".pid")).ReadRecord()
		if err != nil || r.WrapperPID <= 0 || r.JavaPID <= 0 {
			return false
		}
		rec = *r
		return true
	}, 20*time.Second, 100*time.Millisecond, "wrapper 应写出含 java PID 的 PID 记录")

	// ① daemon 子树必须已脱离 Worker 单元 cgroup（否则单元重启必连坐）。
	assert.Equal(t, escapeRel, cgroupOf(t, rec.WrapperPID), "wrapper 应自迁至隔离 cgroup")
	assert.Equal(t, escapeRel, cgroupOf(t, rec.JavaPID), "java（fork 在自迁之后）应随 wrapper 落在隔离 cgroup")
	assert.Empty(t, cgroupPIDs(t, unitDir), "Worker 单元 cgroup 内不应残留任何平台进程")

	// ② Worker 关闭路径只断连、不杀实例。
	m := NewManager(pidDir)
	recovered, err := m.RecoverDaemonInstances()
	require.NoError(t, err)
	require.Equal(t, 1, recovered, "启动接管应重连存活 wrapper")
	st, _ := m.GetState(uuid)
	require.Equal(t, StateRunning, st)
	m.StopAll()
	time.Sleep(300 * time.Millisecond)
	assert.True(t, daemon.IsPIDAlive(rec.WrapperPID), "关闭路径只应断连：wrapper 必须存活")
	assert.True(t, daemon.IsPIDAlive(rec.JavaPID), "关闭路径只应断连：java 必须存活")

	// ③ 模拟 systemd 按 KillMode=control-group 对该单元 cgroup 全员发 KillSignal=15（现场机理）。
	killed := sigtermCgroup(t, unitDir)
	t.Logf("已对假单元 cgroup 全员发 SIGTERM：%v；wrapper 在 %s、java 在 %s",
		killed, cgroupOf(t, rec.WrapperPID), cgroupOf(t, rec.JavaPID))
	time.Sleep(500 * time.Millisecond)
	assert.True(t, daemon.IsPIDAlive(rec.WrapperPID),
		"wrapper 必须存活于单元级 SIGTERM（旧行为下这里即为「每次重启拖倒全部实例」的现场）")
	assert.True(t, daemon.IsPIDAlive(rec.JavaPID), "java 必须存活于单元级 SIGTERM")

	// ④ 关闭后可被下一次启动接管（FR-455① 接管 / FR-497 收养路径不受影响）。
	m2 := NewManager(pidDir)
	recovered2, err := m2.RecoverDaemonInstances()
	require.NoError(t, err)
	require.Equal(t, 1, recovered2, "重启后的 Worker 应能再次接管该 wrapper")
	st2, _ := m2.GetState(uuid)
	require.Equal(t, StateRunning, st2)

	// ⑤ 显式 Stop 语义不变：迁出 cgroup 不影响按 PID/进程组终止。
	require.NoError(t, m2.Stop(uuid))
	require.Eventually(t, func() bool {
		select {
		case <-exited:
			return true
		default:
		}
		return !daemon.IsPIDAlive(rec.WrapperPID)
	}, 20*time.Second, 100*time.Millisecond, "显式 Stop 必须仍能停掉实例（wrapper 应退出）")
	// wrapper 退出时自清隔离目录（非空则留给下次启动清扫，故此处只做软断言）。
	_ = os.Remove(escapeDir)
}

// pickLabCgroupRoot 找一个「用户可写、且能把子进程迁进去」的 cgroup 目录作为实验室根。
// 返回 (挂载点, 实验室根 cgroup 相对路径)；不可用时返回 ok=false（调用方 skip）。
//
// 内核要求迁移者对**源与目标的共同祖先** cgroup 目录有写权限，故只有在同一个用户可写子树内
// （典型：systemd 用户会话的 app.slice、或测试自身所属的可写 cgroup 的兄弟位置）才可能成立。
func pickLabCgroupRoot(t *testing.T) (string, string, bool) {
	t.Helper()
	if runtime.GOOS != "linux" {
		return "", "", false
	}
	const mountRoot = "/sys/fs/cgroup"
	own, err := ownCgroupRelPath()
	if err != nil {
		return "", "", false
	}
	var candidates []string
	if v := strings.TrimSpace(os.Getenv(envLabCgroupRoot)); v != "" {
		candidates = append(candidates, strings.TrimPrefix(v, mountRoot))
	}
	candidates = append(candidates, path.Join(path.Dir(own), fmt.Sprintf("jm-lab-shutdown-%d", os.Getpid())))

	for _, cand := range candidates {
		labDir := filepath.Join(mountRoot, cand)
		if err := os.MkdirAll(labDir, 0o755); err != nil {
			continue
		}
		if ok := probeCgroupMove(t, labDir); ok {
			return mountRoot, cand, true
		}
		_ = os.Remove(labDir)
	}
	return "", "", false
}

// probeCgroupMove 用一个短命替身进程验证「能否把子进程迁移到该 cgroup」（只探权限，不改任何平台状态）。
func probeCgroupMove(t *testing.T, labDir string) bool {
	t.Helper()
	probe := exec.Command("/bin/sh", "-c", "sleep 5")
	if err := probe.Start(); err != nil {
		return false
	}
	defer func() {
		_ = probe.Process.Kill()
		_, _ = probe.Process.Wait()
	}()
	f, err := os.OpenFile(filepath.Join(labDir, "cgroup.procs"), os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	_, err = f.WriteString(strconv.Itoa(probe.Process.Pid) + "\n")
	return err == nil
}

// ownCgroupRelPath 返回本进程的 cgroup2 相对路径（`/proc/self/cgroup` 的 `0::` 行）。
func ownCgroupRelPath() (string, error) {
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "0::"); ok {
			return strings.TrimSpace(rest), nil
		}
	}
	return "", fmt.Errorf("无 cgroup2 层级行")
}

// cgroupOf 返回 pid 当前所在的 cgroup2 相对路径。
func cgroupOf(t *testing.T, pid int) string {
	t.Helper()
	rel, err := cgroupOfPID(pid)
	require.NoError(t, err, "读取 pid %d 的 cgroup 失败（进程可能已退出）", pid)
	return rel
}

func cgroupOfPID(pid int) (string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "0::"); ok {
			return strings.TrimSpace(rest), nil
		}
	}
	return "", fmt.Errorf("pid %d 无 cgroup2 层级行", pid)
}

// cgroupPIDs 读 cgroup 目录内的成员 PID（cgroup.procs）。
func cgroupPIDs(t *testing.T, dir string) []int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		return nil
	}
	var pids []int
	for _, f := range strings.Fields(string(raw)) {
		if pid, err := strconv.Atoi(f); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	sort.Ints(pids)
	return pids
}

// sigtermCgroup 模拟 systemd 的 KillMode=control-group：对该 cgroup 内全部 PID 发 SIGTERM。
// 返回实际发到信号的 PID（旧行为下这里会包含 wrapper 与 java）。
func sigtermCgroup(t *testing.T, dir string) []int {
	t.Helper()
	var sent []int
	for _, pid := range cgroupPIDs(t, dir) {
		if err := syscall.Kill(pid, syscall.SIGTERM); err == nil {
			sent = append(sent, pid)
		}
	}
	return sent
}

// shellQuote 单引号包裹并转义，供 /bin/sh -c 安全拼接路径。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
