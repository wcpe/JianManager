package process

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDirect_CrashEmitsCrashInfo 非正常退出判定 + 快照现场捕获（FR-313）：
// direct 进程以非零码退出（未请求停止）→ 崩溃回调收到退出码 1、非负时长与发生时刻。
func TestDirect_CrashEmitsCrashInfo(t *testing.T) {
	m := NewManager(t.TempDir())
	uuid := "crash-info"
	require.NoError(t, createDirect(m, uuid, "Crash", quickCrashCmd(), t.TempDir()))

	got := make(chan CrashInfo, 2)
	m.SetCrashHandler(func(id string, info CrashInfo) {
		if id == uuid {
			got <- info
		}
	})

	require.NoError(t, m.Start(uuid))

	select {
	case info := <-got:
		assert.Equal(t, 1, info.ExitCode, "exit 1 的退出码应被捕获")
		assert.Empty(t, info.Signal, "非信号退出信号应为空")
		assert.GreaterOrEqual(t, info.DurationMs, int64(0))
		assert.False(t, info.OccurredAt.IsZero(), "应记录崩溃发生时刻")
	case <-time.After(10 * time.Second):
		t.Fatal("等待崩溃回调超时")
	}
	_ = m.Remove(uuid)
}

// TestDirect_NormalStopDoesNotEmitCrash 非正常退出判定的反例：主动停止（STOPPING/STOPPED 态
// 退出，Windows Kill 返回非零码也算正常停止）不得生成崩溃快照。
func TestDirect_NormalStopDoesNotEmitCrash(t *testing.T) {
	m := NewManager(t.TempDir())
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
	require.NoError(t, m.Stop(uuid))

	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("等待实例停止超时")
	}

	select {
	case info := <-crashed:
		t.Fatalf("主动停止不应生成崩溃快照，却收到 %+v", info)
	case <-time.After(300 * time.Millisecond):
		// 静默期内无崩溃回调 = 通过。
	}
	_ = m.Remove(uuid)
}
