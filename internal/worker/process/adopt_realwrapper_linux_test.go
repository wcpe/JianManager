//go:build linux

package process

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// FR-497③ 验收补足：**真实 wrapper 进程**的端到端夹具（Linux）。
//
// 为什么不能再用进程内的 `daemon.RunWithReady`（daemon_test.go 里那套）：进程内运行 wrapper 时
// wrapper 的 PID 就是 `go test` 自己的 PID，于是三件事全部失真——
//
//	① `/proc/<pid>/cmdline` 是测试二进制的 argv（`process.test -test.run …`），不是
//	   `<worker 二进制> daemon`，**wrapper 分支的归属复核根本不会通过**；
//	② `/proc/<pid>/environ` 是测试进程的环境，无法按实例分别造（同一进程只能有一套环境）；
//	③ `Stop` 的强杀兜底 `killProcessTree` 会杀掉测试自身，故所有 Stop 用例只能换成假策略。
//
// 换句话说，进程内 wrapper 让「收养护 wrapper 分支」与「Stop 真进程」永远无法被端到端断言。
//
// 本夹具把 wrapper 跑成**独立子进程**，并让它的 argv 与真实包装完全一致：
// spawn 的是**测试二进制的一份拷贝**，文件名叫 `worker`，argv 是 `<tmp>/worker daemon`；
// 这份拷贝在 init() 里检查 JM_FR497_WRAPPER_HELPER，命中就直接 `daemon.Run(cfg)` 并退出——
// 完全绕过 Go 测试框架（不会被 -test.run 过滤掉，也不会去跑任何用例）。于是：
//   - `/proc/<pid>/cmdline` == `<tmp>/worker daemon` → looksLikeJianManagerDaemon 通过；
//   - `/proc/<pid>/environ` 由本夹具按实例注入 JM_DAEMON_WRAPPER_CONFIG（含实例 UUID）；
//   - socket 是真实的 Unix domain socket，`daemon.Dial` / 控制帧 / PID 文件全是真的。
//
// 为什么用「拷贝测试二进制」而不是 `go build` 一个 worker：后者要拉起 go 工具链（数秒级、需要
// 网络模块缓存、CI 可能没有），而本夹具只需要一个 argv 形状正确、能在 init 里跑 wrapper 的进程。
// 真实 worker 二进制的 daemon 分支（apps/worker/main.go 的 `os.Args[1] == "daemon"`）走的正是
// 同一个 `daemon.Run(cfg)`，故这里替换掉的只有「入口怎么被调到」，不是被测逻辑。
const (
	// fr497WrapperHelperEnv 标记「本进程是被夹具 spawn 出来的 wrapper 子进程」。
	fr497WrapperHelperEnv = "JM_FR497_WRAPPER_HELPER"
	// fr497WrapperReadyEnv 子进程在 wrapper 监听就绪时写入的文件路径（替代 ready channel）。
	fr497WrapperReadyEnv = "JM_FR497_WRAPPER_READY_FILE"
)

// init 是 wrapper 子进程的入口。**必须放在 init**：Go 测试框架的 main 只在包内无 TestMain 时由
// 测试驱动生成，init 一定先于它执行，于是 os.Exit 能干净地拦住后续的测试框架初始化与用例调度。
func init() {
	if os.Getenv(fr497WrapperHelperEnv) != "1" {
		return
	}
	cfg, err := daemon.ParseWrapperConfigFromEnv()
	if err != nil {
		os.Stderr.WriteString("wrapper 夹具配置解析失败: " + err.Error() + "\n")
		os.Exit(2)
	}
	ready := make(chan struct{}, 1)
	if path := os.Getenv(fr497WrapperReadyEnv); path != "" {
		go func() {
			<-ready
			_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644)
		}()
	}
	if err := daemon.RunWithReady(cfg, ready); err != nil {
		os.Stderr.WriteString("wrapper 夹具退出: " + err.Error() + "\n")
		os.Exit(3)
	}
	os.Exit(0)
}

// realWrapperProc 一个真实运行的 wrapper 子进程句柄。
type realWrapperProc struct {
	// Cmd 子进程句柄（用于收尾与判活）。
	Cmd *exec.Cmd
	// PID wrapper 进程本身。
	PID int
	// LogPath 子进程 stdout/stderr 的落盘路径（含 wrapper 自己与 Java 的输出）。
	LogPath string
	// done 在 wrapper 子进程被回收（Wait 返回）后关闭。
	//
	// 为什么必须有自己的回收 goroutine：wrapper 是**本测试进程的直接子进程**，退出后在被 Wait 前
	// 一直是僵尸进程，而 `IsPIDAlive`（signal 0）对僵尸仍返回 true。不主动回收，「wrapper 是否已
	// 退出」这个断言永远为假——不是产品没退，是观测面错了。
	done chan struct{}
}

// fr497WorkerBin 把测试二进制复制成一个名为 `worker` 的文件并返回其路径。
//
// 目录刻意放在 t.TempDir() 之外一层（系统的 TestXxx 目录已经很深）：Unix socket 路径受
// sockaddr_un.sun_path（≈107 字节）限制，pidDir 太长会让 wrapper 监听直接失败。
func fr497WorkerBin(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	body, err := os.ReadFile(exe)
	require.NoError(t, err)

	dir, err := os.MkdirTemp("", "fr497w-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, "worker")
	require.NoError(t, os.WriteFile(path, body, 0o755))
	return path
}

// startRealWrapper 以真实 wrapper 形态启动一个 wrapper 子进程。
//
// cfg 里必填 InstanceUUID / WorkDir / PIDDir / StartCommand；AutoRestart 建议 false（收尾更可控）。
// 返回时 wrapper 已经在监听 socket 上（由 ready 文件同步，不用 sleep 猜）。
func startRealWrapper(t *testing.T, cfg daemon.WrapperConfig) *realWrapperProc {
	t.Helper()
	bin := fr497WorkerBin(t)
	readyPath := filepath.Join(t.TempDir(), "wrapper-ready")

	cfgBytes, err := json.Marshal(cfg)
	require.NoError(t, err)

	cmd := exec.Command(bin, "daemon")
	cmd.Env = append(filterJavaToolOptions(os.Environ()),
		daemon.EnvWrapperConfig+"="+string(cfgBytes),
		fr497WrapperHelperEnv+"=1",
		fr497WrapperReadyEnv+"="+readyPath,
		// cgroup 隔离在测试环境里常常迁不动（无 systemd 单元 / 无权限），关掉以免噪声日志与
		// 与真实 cgroup 层级的意外交互；该开关只影响「迁出 Worker 单元 cgroup」，不影响
		// 归属复核、登记、巡检与停止。
		daemon.EnvCgroupIsolation+"=0",
	)
	logPath := filepath.Join(os.TempDir(), "fr497-wrapper-"+strconv.Itoa(os.Getpid())+"-"+cfg.InstanceUUID+".log")
	logFile, err := os.Create(logPath)
	require.NoError(t, err)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// 自成进程组：收尾时按组精确终止，绝不波及 go test 自身（与 startIsolated 同口径）。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	require.NoError(t, cmd.Start())
	p := &realWrapperProc{Cmd: cmd, PID: cmd.Process.Pid, LogPath: logPath, done: make(chan struct{})}
	// 主动回收：见 done 字段注释（僵尸进程会让「已退出」永远判不出来）。
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		// 组内全部进程（wrapper + sh -c + java）：pgid == wrapper 的 pid。
		_ = syscall.Kill(-p.PID, syscall.SIGKILL)
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
		}
		_ = logFile.Close()
	})

	require.Eventuallyf(t, func() bool {
		_, statErr := os.Stat(readyPath)
		return statErr == nil
	}, 10*time.Second, 20*time.Millisecond, "wrapper 未在 10s 内监听就绪，日志：\n%s", readFileOrEmpty(logPath))
	return p
}

// waitPIDRecord 等 PID 文件写出**含 Java PID** 的记录（wrapper 启动 Java 后才补写）。
//
// 这一步不能省：收养的两个前置条件之一是「wrapper 与 Java 均存活」，而 PID 记录里的 JavaPID
// 只有 wrapper 真正把 Java 拉起来后才写入。不等就会造出 `JavaPID == 0` 的记录，被 adoptableRecord
// 直接判为无效形态，用例会退化成「什么都没测」。
func waitPIDRecord(t *testing.T, pidDir, uuid string) *daemon.PIDRecord {
	t.Helper()
	pf := daemon.NewPIDFile(daemon.PIDFileName(pidDir, uuid))
	var rec daemon.PIDRecord
	require.Eventuallyf(t, func() bool {
		r, err := pf.ReadRecord()
		if err != nil || r == nil || r.JavaPID <= 0 || r.WrapperPID <= 0 {
			return false
		}
		rec = *r
		return true
	}, 20*time.Second, 50*time.Millisecond, "PID 记录未写出含 java pid 的形态")
	return &rec
}

// waitExit 等 wrapper 子进程退出（被回收），返回是否在预算内退出。
//
// 不用 daemon.IsPIDAlive：wrapper 是本进程的直接子进程，退出后在被回收前是僵尸，signal 0 仍会成功。
func (p *realWrapperProc) waitExit(timeout time.Duration) bool {
	select {
	case <-p.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// readFileOrEmpty 读文件，失败返回空串（用在断言失败信息里，不该再引入新的失败）。
func readFileOrEmpty(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// isPIDAliveLocal 报告 pid 是否存活（不经 Manager 的可注入桩，直接查内核）。
func isPIDAliveLocal(pid int) bool {
	return daemon.IsPIDAlive(pid)
}

// javaPIDOf 从 PID 记录里取 Java PID（记录缺失返回 0）。
func javaPIDOf(t *testing.T, pidDir, uuid string) int {
	t.Helper()
	rec, err := daemon.NewPIDFile(daemon.PIDFileName(pidDir, uuid)).ReadRecord()
	if err != nil || rec == nil {
		return 0
	}
	return rec.JavaPID
}
