package process

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// TestDaemonStrategy_Start_RefusesWhilePriorProcessAlive 是「restart 静默双开」的回归测试。
//
// 缺陷链路（实例 153 / beacon-main 实证）：Stop 只下发停止帧、不等待进程退出，restartLocked
// 随即调用 Start。若此时上一代 wrapper/Java 仍在优雅关服（wrapper 的强杀兜底默认 30s，
// 平台设置可放大），而等待预算耗尽后旧实现选择"仍继续启动"，就会拉起第二个 wrapper 与第二个
// Java——新进程抢不到端口/锁而秒崩（日志 4 次 exitCode=1 durationMs≈195），旧进程继续用旧配置
// 跑满兜底时长，而 Manager.Restart 早已返回成功：用户看到 {"ok":true}，进程却从未重启。
//
// 修复后：预算耗尽必须返回 daemon.ErrPriorExitTimeout 并**不 spawn 任何新进程**，
// 让本次启动失败在实例状态与日志中可见；仍在服务的旧进程不被干扰。
func TestDaemonStrategy_Start_RefusesWhilePriorProcessAlive(t *testing.T) {
	// 缩短等待预算与强杀兜底：测试替身（sleep）不响应 stdin "stop"，无需等真实默认值。
	t.Setenv("JIANMANAGER_START_WAIT_PRIOR_EXIT_TIMEOUT", "300ms")
	t.Setenv("JIANMANAGER_GRACEFUL_STOP_TIMEOUT", "1s")

	pidDir := shortDaemonPIDDir(t)
	uuid := "daemon-prior-alive-" + filepath.Base(pidDir)
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

	// 等在跑的「上一代」把 PID 记录写全（wrapper + 被托管的子进程）。
	pidPath := filepath.Join(pidDir, uuid+".pid")
	pf := daemon.NewPIDFile(pidPath)
	var rec *daemon.PIDRecord
	require.Eventually(t, func() bool {
		r, err := pf.ReadRecord()
		if err != nil {
			return false
		}
		rec = r
		return r.WrapperPID > 0 && daemon.IsPIDAlive(r.WrapperPID) &&
			r.JavaPID > 0 && daemon.IsPIDAlive(r.JavaPID)
	}, 15*time.Second, 50*time.Millisecond, "应能读到存活的上一代 wrapper 与被托管子进程")

	t.Cleanup(func() {
		if conn, err := daemon.Dial(rec.SocketAddr); err == nil {
			f := &daemon.Frame{
				Header:  daemon.Header{Channel: daemon.ChannelControl, Type: daemon.TypeCommand},
				Payload: []byte(daemon.ControlKill),
			}
			_ = f.Encode(conn)
			_ = conn.Close()
		}
		select {
		case <-done:
		case <-time.After(8 * time.Second):
		}
	})

	mgr := NewManager(pidDir)
	d := newDaemonStrategy(mgr, CommandSpec{UUID: uuid, WorkDir: pidDir, ProcessType: ProcessTypeDaemon})

	err := d.Start(context.Background())
	require.Error(t, err, "上一代进程仍存活时必须拒绝启动，不得双开")
	require.ErrorIs(t, err, daemon.ErrPriorExitTimeout, "应透出「等待上一代退出超时」这一根因")

	// 拒绝启动不得破坏仍在服务的旧进程与其 PID 记录。
	after, readErr := pf.ReadRecord()
	require.NoError(t, readErr, "拒绝启动不得清理既有 PID 记录")
	require.Equal(t, rec.WrapperPID, after.WrapperPID, "拒绝启动不得替换既有 wrapper")
	require.True(t, daemon.IsPIDAlive(after.JavaPID), "拒绝启动不得终止仍在服务的旧进程")

	_ = d.Close()
}

// TestDaemonStrategy_Start_RefusesWhenSocketStillServed 守护启动前的 socket 纵深防御。
//
// 上面的等待只以 PID 文件为依据，而记录可能缺失/损坏（旧 wrapper 被强杀未及清理、记录被误删）：
// 此时等待会立即返回并以为「上一代已自清理」，仍然双开。socket 是实例级唯一地址，能拨通就说明
// 确有 wrapper 在托管该实例。本用例显式删掉 PID 文件来构造这个盲区，断言启动被拒绝且不会凭空
// 造出第二个 wrapper（修复前这里会直接 spawn 新 wrapper 与旧进程并存）。
func TestDaemonStrategy_Start_RefusesWhenSocketStillServed(t *testing.T) {
	t.Setenv("JIANMANAGER_START_WAIT_PRIOR_EXIT_TIMEOUT", "300ms")

	pidDir := shortDaemonPIDDir(t)
	uuid := "daemon-sock-served-" + filepath.Base(pidDir)
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
	var rec *daemon.PIDRecord
	// 必须等到 wrapper **与** 被托管子进程都登记完成：wrapper 在 startJava 成功后还会回写一次
	// 整份 PID 记录，若在回写前删文件，记录会"自己回来"而掩盖本用例要构造的盲区。
	require.Eventually(t, func() bool {
		r, err := pf.ReadRecord()
		if err != nil {
			return false
		}
		rec = r
		return r.WrapperPID > 0 && daemon.IsPIDAlive(r.WrapperPID) &&
			r.JavaPID > 0 && daemon.IsPIDAlive(r.JavaPID)
	}, 15*time.Second, 50*time.Millisecond, "应能读到存活的上一代 wrapper 与被托管子进程")

	t.Cleanup(func() {
		if conn, err := daemon.Dial(rec.SocketAddr); err == nil {
			f := &daemon.Frame{
				Header:  daemon.Header{Channel: daemon.ChannelControl, Type: daemon.TypeCommand},
				Payload: []byte(daemon.ControlKill),
			}
			_ = f.Encode(conn)
			_ = conn.Close()
		}
		select {
		case <-done:
		case <-time.After(8 * time.Second):
		}
	})

	// 构造盲区：删掉 PID 文件，此后 WaitForPriorExit 会立即返回（误以为上一代已自清理）。
	require.NoError(t, os.Remove(pidPath))

	mgr := NewManager(pidDir)
	d := newDaemonStrategy(mgr, CommandSpec{UUID: uuid, WorkDir: pidDir, ProcessType: ProcessTypeDaemon})

	err := d.Start(context.Background())
	require.Error(t, err, "socket 仍可拨通时必须拒绝启动，不得双开")
	require.Contains(t, err.Error(), "socket", "错误须点明是 socket 探测拦下的")

	// 拒绝启动不得凭空造出 PID 记录——即没有 spawn 出第二个 wrapper。
	_, statErr := os.Stat(pidPath)
	assert.True(t, os.IsNotExist(statErr), "拒绝启动不得写入新的 PID 记录")

	_ = d.Close()
}
