//go:build linux

package process

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// 移动 socket inode 与 PID 文件，但保留记录中的旧路径，复现整体部署目录迁移后的现场。
// wrapper/Java、归属复核、控制帧都走真实现；只关闭测试环境里的 cgroup 迁移。
func TestMigratedDaemonSocketRecoversAndStops(t *testing.T) {
	for _, recoverAtStartup := range []bool{true, false} {
		t.Run(map[bool]string{true: "启动恢复", false: "运行期收养"}[recoverAtStartup], func(t *testing.T) {
			h := newRealAdoptHarness(t, 0)
			rec := waitPIDRecord(t, h.pidDir, h.uuid)
			oldDir := h.pidDir
			newDir := shortDaemonPIDDir(t)
			require.NoError(t, os.Rename(rec.SocketAddr, daemon.SocketAddr(newDir, h.uuid)))
			require.NoError(t, os.Rename(daemon.PIDFileName(oldDir, h.uuid), daemon.PIDFileName(newDir, h.uuid)))
			require.NoError(t, os.Rename(h.workDir, h.workDir+"-moved"))
			t.Cleanup(func() { _ = os.RemoveAll(h.workDir + "-moved") })
			h.m = NewManager(newDir)
			h.m.recoverMigrate = func(int, string) (int, error) { return 1, nil }
			h.m.SetRecoverRetryBackoff([]time.Duration{time.Millisecond})
			t.Cleanup(func() {
				h.m.WaitStartupTakeover(time.Second)
				if inst, ok := h.m.GetInstance(h.uuid); ok && inst.strategy != nil {
					_ = inst.strategy.Close()
				}
			})
			if recoverAtStartup {
				n, err := h.m.RecoverDaemonInstances()
				require.NoError(t, err)
				require.Equal(t, 1, n, "旧 socket 路径失效时，应当场经当前 pidDir 恢复连接")
			} else {
				require.True(t, h.m.adoptUnmanagedRecord(h.uuid, rec), "运行期收养同样应支持移动后的 socket")
			}
			state, err := h.m.GetState(h.uuid)
			require.NoError(t, err)
			require.Equal(t, StateRunning, state)
			inst, ok := h.m.GetInstance(h.uuid)
			require.True(t, ok)
			d := inst.strategy.(*daemonStrategy)
			// 强制断开旧控制连接，证明后续 Stop 的即时拨号也能走当前目录。
			d.mu.Lock()
			conn := d.conn
			d.mu.Unlock()
			require.NoError(t, conn.Close())
			require.Eventually(t, func() bool { return !d.controlConnected() }, time.Second, time.Millisecond)
			require.NoError(t, h.m.Stop(h.uuid))
			require.True(t, h.wrapper.waitExit(10*time.Second), "Stop 必须通过移动后的 socket 停止原 wrapper")
			require.NoError(t, daemon.WaitForPriorExitContext(context.Background(), newDir, h.uuid, time.Second), "受控停止后后续启动的上一代退出闸应放行")
			require.Equal(t, rec.WrapperPID, h.wrapper.PID, "恢复不能产生第二个 wrapper")

			// 受控停止后，用迁移后的工作目录启动下一代；仍走真实 cmd.Start、PID 记录与 socket。
			// 仅替换测试二进制入口，使其执行现有 wrapper helper，避免启动真实游戏服。
			t.Setenv(adoptWrapperHelperEnv, "1")
			t.Setenv(daemon.EnvCgroupIsolation, "0")
			next := newDaemonStrategy(NewManager(newDir), CommandSpec{
				UUID: h.uuid, WorkDir: h.workDir + "-moved", StartCommand: keepAliveCmd(),
			})
			bin := adoptWorkerBin(t)
			next.wrapperCommand = func(string) *exec.Cmd { return exec.Command(bin, "daemon") }
			t.Cleanup(func() { _ = next.Kill(); _ = next.Close() })
			require.NoError(t, next.Start(context.Background()), "停止旧代后，下一次 Start 应能成功")
			nextPID := next.GetPID()
			require.NotEqual(t, rec.WrapperPID, nextPID)
			require.Eventually(t, func() bool {
				current, err := daemon.NewPIDFile(daemon.PIDFileName(newDir, h.uuid)).ReadRecord()
				return err == nil && current.WrapperPID == nextPID && current.JavaPID > 0
			}, 5*time.Second, 10*time.Millisecond, "下一代应在当前目录写入自己的 PID 记录")
		})
	}
}

// 不可达的旧部署记录应立即提示处理残留，而不是每次等完关服预算再给出同样超时。
func TestMigratedDaemonStartFailsActionablyWithoutWaiting(t *testing.T) {
	pidDir := t.TempDir()
	m := NewManager(pidDir)
	uuid := "migrated-unreachable"
	path := daemon.PIDFileName(pidDir, uuid)
	require.NoError(t, daemon.NewPIDFile(path).WriteRecord(daemon.PIDRecord{
		InstanceUUID: uuid, WrapperPID: os.Getpid(), JavaPID: os.Getpid(),
		SocketAddr: filepath.Join(t.TempDir(), uuid+".sock"), WorkDir: "/old-deployment/server",
	}))
	d := newDaemonStrategy(m, CommandSpec{UUID: uuid, WorkDir: "/new-deployment/server"})
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	err := d.Start(ctx)
	require.ErrorContains(t, err, "旧部署残留需接管/清理")
	require.NotErrorIs(t, err, daemon.ErrStartWaitCanceled, "应在等待前识别迁移残留")
	require.Nil(t, d.wrapperCmd, "不得拉起新 wrapper")
	require.FileExists(t, path, "不得清理或改写旧记录")
	require.True(t, daemon.IsPIDAlive(os.Getpid()), "仅观察，绝不按路径杀进程")
}

// Stop 已发出时旧代仍在优雅收尾，当前 socket inode 仍存在；路径迁移不能跳过正常退出等待。
func TestMigratedDaemonGracefulStopThenStartWaitsForPriorExit(t *testing.T) {
	// 覆盖包级为加速测试设置的一秒预算，容纳 race wrapper 的退出收尾延迟。
	t.Setenv("JIANMANAGER_START_WAIT_PRIOR_EXIT_TIMEOUT", "5s")
	oldDir := shortDaemonPIDDir(t)
	newDir := shortDaemonPIDDir(t)
	workDir := t.TempDir()
	stopping := filepath.Join(workDir, "stopping")
	release := filepath.Join(workDir, "release")
	uuid := "migrated-graceful"
	wrapper := startRealWrapper(t, daemon.WrapperConfig{
		InstanceUUID: uuid, PIDDir: oldDir, WorkDir: workDir, StopCommand: "stop",
		StartCommand: "read line; printf stopping > " + shellQuote(stopping) + "; while [ ! -f " + shellQuote(release) + " ]; do sleep 0.01; done",
	})
	rec := waitPIDRecord(t, oldDir, uuid)
	require.NoError(t, os.Rename(rec.SocketAddr, daemon.SocketAddr(newDir, uuid)))
	require.NoError(t, os.Rename(daemon.PIDFileName(oldDir, uuid), daemon.PIDFileName(newDir, uuid)))
	m := NewManager(newDir)
	m.recoverMigrate = func(int, string) (int, error) { return 1, nil }
	n, err := m.RecoverDaemonInstances()
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.NoError(t, m.Stop(uuid))
	require.Eventually(t, func() bool { _, err := os.Stat(stopping); return err == nil }, time.Second, time.Millisecond)
	require.True(t, daemon.IsPIDAlive(rec.WrapperPID), "前提：Stop 已送达，旧 wrapper 仍在优雅收尾")
	require.FileExists(t, daemon.SocketAddr(newDir, uuid), "前提：当前 socket inode 仍存在")

	t.Setenv(adoptWrapperHelperEnv, "1")
	t.Setenv(daemon.EnvCgroupIsolation, "0")
	next := newDaemonStrategy(m, CommandSpec{UUID: uuid, WorkDir: workDir, StartCommand: keepAliveCmd()})
	bin := adoptWorkerBin(t)
	next.wrapperCommand = func(string) *exec.Cmd { return exec.Command(bin, "daemon") }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	done := make(chan error, 1)
	go func() { done <- next.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = os.WriteFile(release, nil, 0o600)
		_ = next.Kill()
		_ = next.Close()
	})
	select {
	case err := <-done:
		t.Fatalf("旧代尚未退出，Start 应继续等待，不能因旧记录地址不同立即结束: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	require.Zero(t, next.GetPID(), "旧代未退出时不能双开")
	require.NoError(t, os.WriteFile(release, nil, 0o600))
	// race 构建的 wrapper 退出会额外等待运行时收尾，预算不能贴着该一秒边界。
	require.True(t, wrapper.waitExit(5*time.Second), "允许旧代完成优雅退出")
	select {
	case err := <-done:
		require.NoError(t, err, "旧代退出后，等待中的 Start 必须继续启动下一代")
	case <-ctx.Done():
		t.Fatal("旧代退出后 Start 未及时继续")
	}
	nextPID := next.GetPID()
	require.NotZero(t, nextPID)
	require.NotEqual(t, rec.WrapperPID, nextPID)
	require.Eventually(t, func() bool {
		current, err := daemon.NewPIDFile(daemon.PIDFileName(newDir, uuid)).ReadRecord()
		return err == nil && current.WrapperPID == nextPID && current.JavaPID > 0
	}, time.Second, 10*time.Millisecond)
}
