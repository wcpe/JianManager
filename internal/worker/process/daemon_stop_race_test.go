package process

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// TestDaemonStrategy_StopDuringConnectWindow_KillsChild 是 FIX-C（bug #4）的回归：
// Start 拉起后「立即 Stop」杀不掉进程 → 孤儿继续输出日志。
//
// 根因：daemonStrategy 与 wrapper 的控制连接（d.conn）由 connectLoop 异步建立（Start 仅 spawn
// wrapper 即返回 RUNNING）。若 Stop 在连接窗口内到达（d.conn==nil），旧实现直接 return nil、
// 从不向 wrapper 下发 ControlStop，wrapper 的子进程永不退出 → 孤儿。
//
// 复现手段：在进程内运行真实 wrapper（real OS 子进程托管 keepAlive 命令），构造指向同一 socket 的
// daemonStrategy。把策略置于「已 spawn、未连接」的窗口态（d.conn==nil）后调用 Stop()，断言被托管
// 子进程在限定时间内退出。旧实现下 Stop 空转、子进程仍存活（用例红）；修复后 Stop 在连接窗口内
// 仍能可靠终止（用例绿）。
//
// 注：daemonStrategy.Start 经 os.Executable() spawn worker 二进制的 daemon 子命令，单测二进制无该
// 子命令分支，故此处不走真实 spawn，而是直接驱动 strategy 的连接/停止路径（与 daemon_test.go 同思路）。
func TestDaemonStrategy_StopDuringConnectWindow_KillsChild(t *testing.T) {
	// 测试替身进程（ping/sleep）不响应 stdin "stop"，缩短优雅停止超时让其快速回退强杀。
	t.Setenv("JIANMANAGER_GRACEFUL_STOP_TIMEOUT", "1s")
	t.Setenv("JIANMANAGER_START_WAIT_PRIOR_EXIT_TIMEOUT", "1s")

	tests := []struct {
		name string
		// connectFirst=true：先连上 wrapper（d.conn 就绪）再 Stop（基线，控制帧路径本就应停掉）。
		// connectFirst=false：处于 d.conn==nil 的连接窗口直接 Stop（复现 bug 的关键路径）。
		connectFirst bool
	}{
		{name: "连接窗口内即停-复现孤儿竞态", connectFirst: false},
		{name: "已连接后停止-基线对照", connectFirst: true},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pidDir := t.TempDir()
			// uuid 含子用例序号：Windows Named Pipe 名由 uuid 派生，跨子用例同名会因上一 wrapper
			// 尚未释放管道而「监听就绪超时」，故每个子用例用独立实例标识隔离。
			uuid := fmt.Sprintf("daemon-stop-race-%d", i)

			// 1) 进程内启动真实 wrapper（real OS 子进程托管 keepAlive 命令），模拟「已拉起的实例」。
			cfg := daemon.WrapperConfig{
				InstanceUUID: uuid,
				StartCommand: keepAliveCmd(),
				WorkDir:      pidDir,
				AutoRestart:  false,
				PIDDir:       pidDir,
			}
			ready := make(chan struct{}, 1)
			done := make(chan error, 1)
			go func() { done <- daemon.RunWithReady(cfg, ready) }()
			select {
			case <-ready:
			case <-time.After(5 * time.Second):
				t.Fatal("wrapper 监听就绪超时")
			}

			// 等 wrapper 写好 PID 文件并拿到被托管子进程 pid + socket 地址。
			pf := daemon.NewPIDFile(filepath.Join(pidDir, uuid+".pid"))
			require.Eventually(t, func() bool {
				rec, err := pf.ReadRecord()
				return err == nil && rec.JavaPID != 0 && daemon.IsPIDAlive(rec.JavaPID)
			}, 5*time.Second, 50*time.Millisecond, "应能读到存活的被托管子进程 pid")
			rec, _ := pf.ReadRecord()
			childPID := rec.JavaPID
			addr := rec.SocketAddr

			// 收尾：无论用例成败，确保 wrapper 退出、子进程不残留，避免 TempDir 清理失败。
			t.Cleanup(func() {
				if daemon.IsPIDAlive(childPID) {
					if conn, err := daemon.Dial(addr); err == nil {
						f := &daemon.Frame{Header: daemon.Header{Channel: daemon.ChannelControl, Type: daemon.TypeCommand}, Payload: []byte(daemon.ControlKill)}
						_ = f.Encode(conn)
						_ = conn.Close()
					}
				}
				select {
				case <-done:
				case <-time.After(8 * time.Second):
				}
			})

			// 2) 构造指向同一 wrapper 的 daemonStrategy（pidDir 一致，使 Stop 兜底能定位实例）。
			mgr := NewManager(pidDir)
			d := newDaemonStrategy(mgr, CommandSpec{UUID: uuid, WorkDir: pidDir, ProcessType: ProcessTypeDaemon})

			if tt.connectFirst {
				// 基线：先连上 wrapper（d.conn 就绪）再 Stop —— 控制帧路径，本就应停掉。
				require.NoError(t, d.Reconnect(addr), "连接 wrapper 失败")
			}
			// connectFirst=false 时刻意保持 d.conn==nil：精确复现 Start 后 connectLoop 尚未连上、
			// Stop 抢先到达的连接窗口竞态（确定性命中 d.conn==nil 分支，不依赖调度时序）。

			// 3) 立即 Stop（连接窗口内）。Stop 必须可靠终止被托管子进程，不留孤儿。
			require.NoError(t, d.Stop())

			// 4) 断言：被托管子进程在限定时间内退出（无孤儿继续输出日志）。
			require.Eventually(t, func() bool {
				return !daemon.IsPIDAlive(childPID)
			}, 8*time.Second, 100*time.Millisecond,
				"Start 后连接窗口内 Stop 应可靠终止被托管子进程，但其仍存活（孤儿竞态未修复）")

			_ = d.Close()
		})
	}
}
