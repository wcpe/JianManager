//go:build linux

package process

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// 验收补足 · 任务 2 的公共夹具：真实 wrapper 子进程 + 真实 java 游戏服夹具 + 真 Manager。
//
// 这一层刻意**不注入任何桩**：归属复核走 DefaultVerifyAdoptOwnership（读真实 /proc）、存活探测走
// 真内核、拨号走真 Unix socket、Stop 走真控制帧与真进程树。测试里唯一被替换的东西是
// 「pidDir 放在短路径下」（Unix socket 有 107 字节上限）与「测试二进制冒充 worker 二进制」（见
// startRealWrapper 的长注释）。这两处替换都不触及被测逻辑。
type realAdoptHarness struct {
	pidDir  string
	workDir string
	uuid    string
	wrapper *realWrapperProc
	m       *Manager
	s       *OrphanScanner
	// audits 收养/巡检路径落下的审计（经 onOrphanAudit）。
	audits *[]scanAudit
	// outputs 实例 stdout/stderr（经 SetOutputHandler，与生产装配同口径：wrapper 帧 → onOutput）。
	outputs *lockedBuffer
	// javaLog 实例**自己的日志文件**（夹具 --log 落盘，模拟 MC 的 logs/latest.log）。
	// 这是「停止是否走了优雅关服」唯一可靠观测面：wrapper 收到 stop 后立即关闭控制连接，
	// java 关服序列的尾巴往往来不及经 stdio 帧转发回 Worker。
	javaLog string
}

// newRealAdoptHarness 起一套「已经有一个 wrapper+java 在跑、但 Worker 内存表里没有它」的真实现场。
//
// 这正是生产里 Worker 硬崩重启后的形态：磁盘上服务器在跑，Worker 一无所知。收养要做的就是把
// 这条控制通道接回来。
func newRealAdoptHarness(t *testing.T, recordProbePort int) *realAdoptHarness {
	t.Helper()
	return newRealAdoptHarnessPorts(t, recordProbePort, recordProbePort)
}

// newRealAdoptHarnessPorts 同 newRealAdoptHarness，但把「PID 记录里的 ProbePort」与「java 夹具
// 实际监听的端口」拆开——两个量在真实部署里本来就是独立的：记录里的端口是 CP 当年分配的约定值，
// 而是否真有人监听取决于 ServerProbe 插件有没有部署。健康巡检读的是前者、探的是后者，故要分别控制。
//
//   - recordProbePort>0 / javaListenPort<=0：记录声明有探针但无人监听 —— 未部署 ServerProbe 的真实形态，
//     是「探针不可达」用例的必要构造；
//   - 两者相等>0：正常形态；
//   - 两者都<=0：无任何响应维度（巡检走「仅存活即健康」兜底）。
func newRealAdoptHarnessPorts(t *testing.T, recordProbePort, javaListenPort int) *realAdoptHarness {
	t.Helper()
	classes := compileMCStub(t)

	// pidDir 必须短：Unix socket 路径上限 ≈107 字节。
	pidDir, err := os.MkdirTemp("", "adopt-p-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(pidDir) })
	workDir, err := os.MkdirTemp("", "adopt-d-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(workDir) })

	uuid := "adopt-" + filepath.Base(pidDir)
	javaLog := filepath.Join(workDir, "latest.log")
	h := &realAdoptHarness{pidDir: pidDir, workDir: workDir, uuid: uuid, audits: &[]scanAudit{}, javaLog: javaLog}

	// wrapper 的 StartCommand 是真实 java 夹具启动串：它会写日志、读 stdin 的 stop、可选监听端口。
	h.wrapper = startRealWrapper(t, daemon.WrapperConfig{
		InstanceUUID: uuid,
		StartCommand: mcStubStartCommand(t, classes, javaListenPort, uuid, javaLog),
		StopCommand:  "stop",
		WorkDir:      workDir,
		AutoRestart:  false,
		PIDDir:       pidDir,
		ProbePort:    recordProbePort,
	})
	// 等 Java 真的被拉起（PID 记录里出现 JavaPID），否则收养的前置条件「两者均存活」不成立。
	rec := waitPIDRecord(t, pidDir, uuid)
	require.True(t, isPIDAliveLocal(rec.WrapperPID), "wrapper 应存活")
	require.True(t, isPIDAliveLocal(rec.JavaPID), "Java 应存活")

	m := NewManager(pidDir)
	m.onOrphanAudit = func(action, targetID, detail string, success bool, errMsg string) {
		*h.audits = append(*h.audits, scanAudit{action: action, targetID: targetID, detail: detail, success: success})
	}
	s := NewOrphanScanner(m, time.Minute, OrphanPolicyWarn)
	s.listProcesses = func() ([]ScannedProcess, error) { return nil, nil }
	s.listContainers = func(context.Context) ([]ManagedContainer, error) { return nil, nil }
	s.removeContainer = func(context.Context, string) error { return nil }
	h.m, h.s = m, s
	return h
}

// adopt 跑一轮孤儿扫描并断言收养成功，返回收养后的实例。
func (h *realAdoptHarness) adopt(t *testing.T) *Instance {
	t.Helper()
	findings := h.s.ScanOnce()
	require.Len(t, findings, 1, "PID 目录中应恰有一条未纳管活进程记录")
	require.True(t, findings[0].Adopted, "真实 wrapper 形态必须收养成功，审计：%+v", *h.audits)
	inst, ok := h.m.GetInstance(h.uuid)
	require.True(t, ok)
	require.Equal(t, StateRunning, inst.State)
	return inst
}

// countAudit 统计某 action 的审计条数。
func (h *realAdoptHarness) countAudit(action string) int {
	n := 0
	for _, a := range *h.audits {
		if a.action == action {
			n++
		}
	}
	return n
}

// startDecoy 起一个与实例无关的存活进程（自成进程组），用于断言「Stop 没有误杀无关进程」。
func startDecoy(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "120")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	})
	return pid
}

// lockedBuffer 是并发安全的输出收集器：wrapper 的读环 goroutine 往里写，测试主 goroutine 读。
// 用 bytes.Buffer + mutex 而不是 channel：断言关心的是「最终有没有出现这段文本」，不关心时序。
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write 实现 io.Writer（wrapper 的 onOutput 回调按块投递）。
func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String 返回当前累积文本的快照。
func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// collectInstanceOutput 把 Manager 的实例输出接到 h.outputs（生产装配里这条回调接的是终端 WS
// 与日志采集落盘；此处接内存缓冲，用来断言「被托管 java 的输出确实经 wrapper 帧流回了 Worker」）。
func (h *realAdoptHarness) collectInstanceOutput() *lockedBuffer {
	out := &lockedBuffer{}
	h.m.SetOutputHandler(func(instanceID, stream string, data []byte) {
		if instanceID != h.uuid {
			return
		}
		_, _ = out.Write(data)
	})
	h.outputs = out
	return out
}
