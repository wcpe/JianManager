package process

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/daemon"
)

func shortDaemonPIDDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "jm-daemon-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func waitDaemonWrapperReady(t *testing.T, ready <-chan struct{}, done <-chan error) {
	t.Helper()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("wrapper 在监听就绪前退出: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("wrapper 监听就绪超时")
	}
}

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
func TestDaemonStrategy_StopAfterControlConnectionReplaced_KillsChild(t *testing.T) {
	t.Setenv("JIANMANAGER_GRACEFUL_STOP_TIMEOUT", "1s")
	t.Setenv("JIANMANAGER_START_WAIT_PRIOR_EXIT_TIMEOUT", "1s")

	pidDir := shortDaemonPIDDir(t)
	uuid := "daemon-stop-replaced-" + filepath.Base(pidDir)
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
	waitDaemonWrapperReady(t, ready, done)

	pidPath := filepath.Join(pidDir, uuid+".pid")
	pf := daemon.NewPIDFile(pidPath)
	require.Eventually(t, func() bool {
		rec, err := pf.ReadRecord()
		return err == nil && rec.JavaPID > 0 && daemon.IsPIDAlive(rec.JavaPID)
	}, 15*time.Second, 50*time.Millisecond, "应能读到存活的被托管子进程 pid")
	rec, _ := pf.ReadRecord()

	mgr := NewManager(pidDir)
	d := newDaemonStrategy(mgr, CommandSpec{UUID: uuid, WorkDir: pidDir, ProcessType: ProcessTypeDaemon})
	require.NoError(t, d.Reconnect(rec.SocketAddr), "连接 wrapper 失败")

	// jmctl 等本机应急客户端会建立第二条连接。wrapper 接受新连接后会关闭原 Worker 连接，
	// 此时策略仍持有旧 conn；停止必须能从写入 broken pipe 恢复，而不是留下 Java 孤儿。
	emergencyConn, err := daemon.Dial(rec.SocketAddr)
	require.NoError(t, err, "第二客户端连接 wrapper 失败")
	defer emergencyConn.Close()
	require.Eventually(t, func() bool {
		return d.sendControl(daemon.ControlPing) != nil
	}, 3*time.Second, 20*time.Millisecond, "原 Worker 连接应被第二客户端替换为失效连接")

	t.Cleanup(func() {
		if _, err := os.Stat(pidPath); err == nil {
			if conn, dialErr := daemon.Dial(rec.SocketAddr); dialErr == nil {
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

	require.NoError(t, d.Stop(), "控制连接失效后 Stop 应重新拨号停止 wrapper")
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(8 * time.Second):
		t.Fatal("停止后 wrapper 与被托管子进程仍未退出")
	}
	require.Eventually(t, func() bool {
		_, err := os.Stat(pidPath)
		return os.IsNotExist(err)
	}, 3*time.Second, 50*time.Millisecond, "停止后应清理 PID 文件")
	_ = d.Close()
}

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
			pidDir := shortDaemonPIDDir(t)
			// UUID 同时包含子用例序号与随机目录名：Unix socket 保持短路径，Windows Named Pipe
			// 在跨子用例及 -count 重复运行时也不复用名称，避免上一 wrapper 尚未释放监听端。
			uuid := fmt.Sprintf("daemon-stop-race-%d-%s", i, filepath.Base(pidDir))

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
			waitDaemonWrapperReady(t, ready, done)

			// 等 wrapper 写好 PID 文件并拿到被托管子进程 pid + socket 地址。
			pf := daemon.NewPIDFile(filepath.Join(pidDir, uuid+".pid"))
			require.Eventually(t, func() bool {
				rec, err := pf.ReadRecord()
				return err == nil && rec.JavaPID != 0 && daemon.IsPIDAlive(rec.JavaPID)
			}, 15*time.Second, 50*time.Millisecond, "应能读到存活的被托管子进程 pid")
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
