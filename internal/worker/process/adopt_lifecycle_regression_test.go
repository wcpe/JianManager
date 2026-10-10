package process

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// 周期收养只有登记胜出后才迁移，锁外执行；迁移失败不能撤销控制通道。
func TestRV014PeriodicAdoptionIsolation(t *testing.T) {
	for _, mode := range []string{"成功", "迁移失败", "归属失败", "拨号失败", "登记竞争失败"} {
		t.Run(mode, func(t *testing.T) {
			f := newAdoptFixture(t)
			moves := 0
			f.m.recoverMigrate = func(pid int, uuid string) (int, error) {
				moves++
				require.Equal(t, adoptWrapperPID, pid)
				require.Equal(t, adoptUUID, uuid)
				// 可读状态证明没有持有 Manager 写锁，且只有登记胜出者进入迁移。
				require.Equal(t, StateRunning, mustGetState(t, f.m, uuid))
				if mode == "迁移失败" {
					return 0, errors.New("无权限")
				}
				return 2, nil
			}
			if mode == "归属失败" {
				f.m.recoverVerifyOwner = func(int, string, string, bool) bool { return false }
			}
			if mode == "拨号失败" {
				f.m.recoverDial = func(*daemonStrategy, string) error { return errors.New("不可达") }
			}
			if mode == "登记竞争失败" {
				f.m.recoverDial = func(*daemonStrategy, string) error {
					f.m.mu.Lock()
					f.m.instances[adoptUUID] = &Instance{UUID: adoptUUID, State: StateRunning}
					f.m.mu.Unlock()
					return nil
				}
			}
			n, _ := f.m.AdoptUnmanagedDaemonInstances()
			if mode == "成功" || mode == "迁移失败" {
				require.Equal(t, 1, n)
				require.Equal(t, 1, moves, "周期收养必须迁移自己的 wrapper 子树")
				n, _ = f.m.AdoptUnmanagedDaemonInstances()
				require.Zero(t, n)
				require.Equal(t, 1, moves, "重复扫描不能重复迁移")
				require.Equal(t, StateRunning, mustGetState(t, f.m, adoptUUID))
			} else {
				require.Zero(t, n)
				require.Zero(t, moves)
			}
			require.Empty(t, f.kills)
		})
	}
}

// 完整登记被收养后，下一次启动应重建策略并携带完整启动规格。
func TestRV015AdoptedCompleteSpecRebuilt(t *testing.T) {
	f := newAdoptFixture(t)
	workDir, err := filepath.EvalSymlinks(f.dir)
	require.NoError(t, err)
	require.NoError(t, f.m.Create(adoptUUID, "实例", "sleep 60", "save-and-stop", workDir, map[string]string{"KEY": "VALUE"}, true, ProcessTypeDaemon, "jdk-home", "jdk-bin", 25566, 120))
	n, _ := f.m.AdoptUnmanagedDaemonInstances()
	require.Equal(t, 1, n)
	inst, _ := f.m.GetInstance(adoptUUID)
	require.True(t, inst.strategyStale, "收养连接的最小策略不能作为下一代启动策略")
	// 用无外部副作用的停止桩替代控制帧；不改变 stale 判据与真实策略重建路径。
	fake := newBlockingLifecycleStrategy()
	fake.releaseStop()
	inst.strategy = fake
	require.NoError(t, f.m.Stop(adoptUUID))
	require.Nil(t, inst.strategy)
	require.NoError(t, daemon.NewPIDFile(daemon.PIDFileName(f.m.pidDir, adoptUUID)).WriteRecord(daemon.PIDRecord{WrapperPID: os.Getpid(), JavaPID: os.Getpid(), InstanceUUID: adoptUUID}))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, f.m.StartContext(ctx, adoptUUID), daemon.ErrStartWaitCanceled)
	d := inst.strategy.(*daemonStrategy)
	require.Equal(t, "sleep 60", d.spec.StartCommand)
	require.Equal(t, "save-and-stop", d.spec.StopCommand)
	require.Equal(t, map[string]string{"KEY": "VALUE"}, d.spec.EnvVars)
	require.Equal(t, "jdk-home", d.spec.JavaHome)
	require.Equal(t, "jdk-bin", d.spec.JDKBinPath)
	require.True(t, d.spec.AutoRestart)
	require.Equal(t, 120, d.spec.GracefulStopTimeoutSeconds)
	require.Equal(t, 25566, d.spec.ProbePort)

	// 走真实 cmd.Start 与环境配置编码，唯一替换的是受控 helper 入口，不运行 Java/真实 daemon。
	require.NoError(t, os.Remove(daemon.PIDFileName(f.m.pidDir, adoptUUID)))
	t.Setenv("JM_RV015_HELPER", "1")
	t.Setenv(daemon.EnvCgroupIsolation, "0")
	d.wrapperCommand = func(executable string) *exec.Cmd {
		return exec.Command(executable, "-test.run=^TestRV015WrapperConfigHelper$")
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	require.NoError(t, f.m.StartContext(ctx2, adoptUUID))
	cancel2()
	pid := d.GetPID()
	t.Cleanup(func() {
		_ = f.m.Kill(adoptUUID)
		require.Eventually(t, func() bool { return !daemon.IsPIDAlive(pid) }, time.Second, 5*time.Millisecond)
		_ = d.Close()
	})
	configPath := filepath.Join(workDir, "wrapper-config.json")
	var cfg daemon.WrapperConfig
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(configPath)
		return err == nil && json.Unmarshal(data, &cfg) == nil
	}, time.Second, 5*time.Millisecond)
	require.Equal(t, d.spec.StartCommand, cfg.StartCommand)
	require.Equal(t, d.spec.StopCommand, cfg.StopCommand)
	require.Equal(t, d.spec.EnvVars, cfg.EnvVars)
	require.Equal(t, d.spec.JavaHome, cfg.JavaHome)
	require.Equal(t, d.spec.JDKBinPath, cfg.JDKBinPath)
	require.Equal(t, d.spec.AutoRestart, cfg.AutoRestart)
	require.Equal(t, 120, cfg.GracefulStopTimeoutSeconds)
	require.Equal(t, 25566, cfg.ProbePort)
	require.True(t, daemon.IsPIDAlive(pid), "成功提交后取消 RPC 不能杀掉 wrapper helper")
}

// 配置捕获 helper 只落自己的临时文件并等待清理；不加载游戏服或操作 cgroup。
func TestRV015WrapperConfigHelper(t *testing.T) {
	if os.Getenv("JM_RV015_HELPER") != "1" {
		return
	}
	data := []byte(os.Getenv(daemon.EnvWrapperConfig))
	var cfg daemon.WrapperConfig
	if json.Unmarshal(data, &cfg) != nil {
		os.Exit(2)
	}
	if os.WriteFile(filepath.Join(cfg.WorkDir, "wrapper-config.json"), data, 0600) != nil {
		os.Exit(3)
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

// 仅有 PID 记录的未知规格必须先拒绝自愈，不得先停现有服务器。
func TestRV015UnknownSpecRejectsBeforeStop(t *testing.T) {
	f := newAdoptFixture(t)
	n, _ := f.m.AdoptUnmanagedDaemonInstances()
	require.Equal(t, 1, n)
	inst, _ := f.m.GetInstance(adoptUUID)
	fake := newBlockingLifecycleStrategy()
	fake.releaseStop()
	inst.strategy = fake
	err := f.m.RestartIfRunning(adoptUUID)
	require.Error(t, err, "尚未收到 CP 完整规格时必须拒绝重启")
	require.Equal(t, StateRunning, mustGetState(t, f.m, adoptUUID))
	select {
	case <-fake.stopStarted:
		t.Fatal("未知规格不能先停再空命令启动")
	default:
	}
	require.Zero(t, fake.startedCount())
}

// 生命周期排队取消不能继续等 Mutex，也不能在释放后迟到启动。
func TestRV016StartQueueCancellation(t *testing.T) {
	m := NewManager(t.TempDir())
	const uuid = "rv016-queue"
	require.NoError(t, createDirect(m, uuid, "实例", keepAliveCmd(), t.TempDir()))
	inst, _ := m.GetInstance(uuid)
	inst.operationMu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.StartContext(ctx, uuid) }()
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
		inst.operationMu.Unlock()
	case <-time.After(100 * time.Millisecond):
		inst.operationMu.Unlock()
		<-done
		_ = m.Kill(uuid)
		t.Fatal("取消必须立即退出生命周期队列，而非等待前一个操作")
	}
	require.Equal(t, StateStopped, mustGetState(t, m, uuid))
	require.NoError(t, m.Remove(uuid), "取消后 gate 必须可复用")
}

// 取消在停止后发生时，不得再启动新一代，也不能记为崩溃。
func TestRV016CanceledContextDoesNotStart(t *testing.T) {
	m := NewManager(t.TempDir())
	uuid := "rv016-canceled-" + filepath.Base(t.TempDir())
	require.NoError(t, createDirect(m, uuid, "实例", keepAliveCmd(), t.TempDir()))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := m.StartContext(ctx, uuid)
	if err == nil {
		_ = m.Kill(uuid)
	}
	require.Error(t, err)
	require.Equal(t, StateStopped, mustGetState(t, m, uuid))
}
