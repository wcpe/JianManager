package process

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// TestProbeInstanceEvidence_KeepsControlConnectionAlive 是生产事故的回归用例：
// 健康巡检 / CP 证据拉取（每 30s 一轮 liveness → probeOneEvidence）会经 probeSocketReachable
// 向 daemon wrapper 的 socket 做一次性拨号探活；而 wrapper 的 Accept 循环「接受新连接即关闭旧
// workerConn」（见 wrapper.go「关闭旧连接（若存在），避免并发写」）。于是每轮巡检都把 Worker 的
// 控制长连接踢掉，readLoop 退出后 d.conn 仍指向已关闭的 socket → 之后 instance_send_command
// 恒返回 "write ...: broken pipe"，直到下次 Worker 重启（而重启后下一轮巡检又将其踢掉）。
//
// 复现方式：进程内跑真实 wrapper（真实 OS 子进程托管 keepAlive 命令）→ RecoverDaemonInstances
// 接管长连接 → 调用一次证据采集（巡检路径）→ 断言控制长连接仍可下发控制帧。修复前该断言红。
//
// 不变量：证据采集是**只读**操作，不得改变 Worker 与 wrapper 的控制连接状态。
func TestProbeInstanceEvidence_KeepsControlConnectionAlive(t *testing.T) {
	t.Setenv("JIANMANAGER_GRACEFUL_STOP_TIMEOUT", "1s")
	t.Setenv("JIANMANAGER_START_WAIT_PRIOR_EXIT_TIMEOUT", "1s")

	pidDir := shortDaemonPIDDir(t)
	uuid := "daemon-probe-conn-" + filepath.Base(pidDir)
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

	t.Cleanup(func() {
		if conn, err := daemon.Dial(daemon.SocketAddr(pidDir, uuid)); err == nil {
			f := &daemon.Frame{Header: daemon.Header{Channel: daemon.ChannelControl, Type: daemon.TypeCommand}, Payload: []byte(daemon.ControlKill)}
			_ = f.Encode(conn)
			_ = conn.Close()
		}
		select {
		case <-done:
		case <-time.After(8 * time.Second):
		}
	})

	// 接管长连接（等价于 Worker 重启后的恢复路径）。
	mgr := NewManager(pidDir)
	recovered, err := mgr.RecoverDaemonInstances()
	require.NoError(t, err)
	require.Equal(t, 1, recovered, "应接管 1 个 daemon 实例")

	inst, ok := mgr.GetInstance(uuid)
	require.True(t, ok)
	d, ok := inst.strategy.(*daemonStrategy)
	require.True(t, ok, "daemon 实例应持有 daemonStrategy")
	require.NoError(t, d.sendControl(daemon.ControlPing), "接管后控制长连接应可用（基线）")

	// 证据采集：健康巡检与 CP 心跳缺失复核都走这条路径。
	evidences := mgr.ProbeInstanceEvidence([]string{uuid})
	require.Len(t, evidences, 1)
	require.True(t, evidences[0].Running, "wrapper 与子进程都存活时应判为仍在运行")

	// 进程存活证据已充足（wrapper PID 存活即为判据），无需再耗一次拨号。
	require.True(t, evidences[0].ProcessAlive, "应带进程存活证据")
	require.False(t, evidences[0].SocketReachable,
		"内存表策略已确认进程存活时不应再做 socket 探活（探活会替换 wrapper 控制长连接）")

	// 关键断言：只读的证据采集不得踢掉控制长连接。
	// 被替换掉的 socket 上对端 FIN 是异步到达的（生产上表现为采集后约一瞬间 readLoop 才退出），
	// 故在断言前给读循环一个收敛窗口，避免只在「刚拨完探活、FIN 未及送达」的毫秒窗口内取到假绿。
	time.Sleep(300 * time.Millisecond)
	require.NoError(t, d.sendControl(daemon.ControlPing),
		"证据采集后控制长连接必须仍可用；被一次性探活替换即导致后续 console 命令 broken pipe")
}
