package process

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDirect_CrashEmitsCrashInfo 非正常退出判定 + 快照现场捕获：
// direct 进程以非零码退出（未请求停止）→ 崩溃回调收到退出码 1、非负时长与发生时刻。
func TestDirect_CrashEmitsCrashInfo(t *testing.T) {
	m := NewManager(t.TempDir())
	m.SetMemGuard(MemGuardConfig{Disabled: true})
	uuid := "crash-info"
	require.NoError(t, createDirect(m, uuid, "Crash", quickCrashCmd(), t.TempDir()))
	got := make(chan CrashInfo, 2)
	m.SetCrashHandler(func(id string, info CrashInfo) {
		if id == uuid {
			got <- info
		}
	})

	require.NoError(t, m.Start(uuid))
	inst, ok := m.GetInstance(uuid)
	require.True(t, ok)
	direct, ok := inst.strategy.(*directStrategy)
	require.True(t, ok)
	t.Cleanup(func() {
		_ = m.Remove(uuid)
		waitDirectStrategy(t, direct)
	})

	select {
	case info := <-got:
		assert.Equal(t, 1, info.ExitCode, "exit 1 的退出码应被捕获")
		assert.Empty(t, info.Signal, "非信号退出信号应为空")
		assert.GreaterOrEqual(t, info.DurationMs, int64(0))
		assert.False(t, info.OccurredAt.IsZero(), "应记录崩溃发生时刻")
	case <-time.After(10 * time.Second):
		t.Fatal("等待崩溃回调超时")
	}
	waitDirectStrategy(t, direct)
	require.NoError(t, m.Remove(uuid))
}

// TestDirect_NormalStopDoesNotEmitCrash 非正常退出判定的反例：主动停止（STOPPING/STOPPED 态
// 退出，Windows Kill 返回非零码也算正常停止）不得生成崩溃快照。
func TestDirect_NormalStopDoesNotEmitCrash(t *testing.T) {
	m := NewManager(t.TempDir())
	m.SetMemGuard(MemGuardConfig{Disabled: true})
	uuid := "normal-stop"
	// 长驻替身进程（复用 daemon_test 的跨平台命令）：不响应 stdin，Stop 走信号/强杀路径。
	require.NoError(t, createDirect(m, uuid, "LongRun", keepAliveCmd(), t.TempDir()))
	crashed := make(chan CrashInfo, 1)
	m.SetCrashHandler(func(id string, info CrashInfo) { crashed <- info })

	// 停止完成信号：direct waitLoop 在正常停止路径把状态记为 STOPPED。
	stopped := make(chan struct{}, 2)
	m.SetStateChangeHandler(func(_ string, _, newState InstanceState) {
		if newState == StateStopped {
			stopped <- struct{}{}
		}
	})

	require.NoError(t, m.Start(uuid))
	inst, ok := m.GetInstance(uuid)
	require.True(t, ok)
	direct, ok := inst.strategy.(*directStrategy)
	require.True(t, ok)
	t.Cleanup(func() {
		_ = m.Remove(uuid)
		waitDirectStrategy(t, direct)
	})
	require.NoError(t, m.Stop(uuid))

	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("等待实例停止超时")
	}
	waitDirectStrategy(t, direct)

	select {
	case info := <-crashed:
		t.Fatalf("主动停止不应生成崩溃快照，却收到 %+v", info)
	case <-time.After(300 * time.Millisecond):
		// 静默期内无崩溃回调 = 通过。
	}
	require.NoError(t, m.Remove(uuid))
}

// waitDirectStrategy 等待 direct 策略的 waitLoop 完成，确保子进程与管道句柄均已回收。
//
// # 为何超时必须是活的等待，而不能只看「子进程该退了」
//
// Stop() 走的是 `Process.Signal(os.Interrupt)`（SIGINT）。但 direct 策略启动的命令是
// `exec.Command("sh", "-c", StartCommand)`（Windows 为 cmd.exe /c），拿到信号的
// **是外层 shell，不是 StartCommand 真正起的那个子进程**：shell 在等待子进程时
// **不会**把 SIGINT 转发下去（实测：在 `sh -c 'sleep 30'` 上发 SIGINT，子 sleep 仍存活）。
// 于是 cmd.Wait() 要等到子进程自然结束才返回，而测试夹具用的是 `keepAliveCmd()`
// （Linux 下 `sleep 30`）—— 最坏情况要等满 30s。
//
// 所以这里不能把超时设得比夹具寿命短，否则在慢机/并发抢占（实测 CI 上 10s 超时）下
// 必然红。也不能靠先 Kill 绕过：那会改变用例要验的「主动停止走正常路径」语义。
// 取 35s = 测试夹具 `sleep 30` / `ping 30` 的寿命再加余量，保证「等不到」只在
// 真的回收失败（产品缺陷）时发生，而不是夹具未到期。
//
// 注：若将来把夹具换成响应信号的进程，这里的长超时不会变慢用例——
// wg 在进程退出瞬间就返回，超时值只是上限。
func waitDirectStrategy(t *testing.T, direct *directStrategy) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		direct.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(35 * time.Second):
		t.Fatal("direct 子进程未在超时内完成 Wait，不能安全清理临时目录")
	}
}
