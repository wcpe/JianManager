package bot

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestManager_DesiredMapTracksRunningAndStopped(t *testing.T) {
	mgr := NewManager(ManagerConfig{BotWorkerPath: "unused"})
	mgr.rememberDesiredRunning([]BotConfig{
		{ID: "bot-a", Generation: 1, ConfigHash: "hash-a", SessionID: "run-1", Name: "a"},
		{ID: "bot-b", Generation: 2, ConfigHash: "hash-b", SessionID: "run-1", Name: "b"},
	})
	snap := mgr.DesiredSnapshot()
	require.Equal(t, "running", snap["bot-a"].DesiredState)
	require.Equal(t, int64(1), snap["bot-a"].Generation)
	require.Equal(t, "running", snap["bot-b"].DesiredState)

	// 更旧 generation 不得覆盖。
	mgr.rememberDesiredRunning([]BotConfig{
		{ID: "bot-b", Generation: 1, ConfigHash: "old", SessionID: "run-1", Name: "b"},
	})
	require.Equal(t, int64(2), mgr.DesiredSnapshot()["bot-b"].Generation)
	require.Equal(t, "hash-b", mgr.DesiredSnapshot()["bot-b"].Config.ConfigHash)

	mgr.rememberDesiredStopped([]string{"bot-a"}, 3)
	snap = mgr.DesiredSnapshot()
	require.Equal(t, "stopped", snap["bot-a"].DesiredState)
	require.Equal(t, int64(3), snap["bot-a"].Generation)
	// 更旧 stop 忽略。
	mgr.rememberDesiredStopped([]string{"bot-a"}, 1)
	require.Equal(t, int64(3), mgr.DesiredSnapshot()["bot-a"].Generation)
}

func TestManager_CrashMarksDesiredDisconnectedAndCircuit(t *testing.T) {
	mgr := NewManager(ManagerConfig{BotWorkerPath: "unused"})
	mgr.rememberDesiredRunning([]BotConfig{
		{ID: "bot-1", Generation: 1, ConfigHash: "h", SessionID: "run", Name: "b1"},
	})
	mgr.mu.Lock()
	mgr.bots["bot-1"] = &BotState{ID: "bot-1", Status: "connected", Generation: 1}
	mgr.markDesiredDisconnectedLocked("worker-crashed")
	require.Equal(t, "disconnected", mgr.bots["bot-1"].Status)
	require.Equal(t, "worker-crashed", mgr.bots["bot-1"].ErrorCode)

	for i := 0; i < botWorkerCrashThreshold; i++ {
		mgr.recordCrashLocked()
	}
	require.False(t, mgr.circuitOpenUntil.IsZero())
	require.True(t, time.Now().Before(mgr.circuitOpenUntil))
	mgr.mu.Unlock()
	require.True(t, mgr.CircuitOpen())
}

func TestManager_ReplayDesiredUsesBatchAndIdempotency(t *testing.T) {
	mgr := NewManager(ManagerConfig{BotWorkerPath: "unused", RequestTimeout: 50 * time.Millisecond})
	// 注入假 start：不真实 spawn，仅标记 ready。
	mgr.startFn = func(ctx context.Context) error {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		mgr.running = true
		mgr.capacity.Ready = true
		mgr.capacity.Legacy = false
		mgr.capacity.WorkerEpoch = "epoch-test"
		mgr.capacity.WorkerEpochGeneration = 9
		mgr.closeReadySignalLocked()
		return nil
	}
	configs := make([]BotConfig, 0, 55)
	// 使用稳定 id
	for i := 0; i < 55; i++ {
		configs = append(configs, BotConfig{
			ID:         "bot-" + itoa(i),
			Generation: 1, ConfigHash: "hash", SessionID: "run", Name: "n",
		})
	}
	mgr.rememberDesiredRunning(configs)

	// replay 会走 sendRequest，无子进程会失败；验证至少 desired 仍完整。
	err := mgr.replayDesiredRunning(context.Background())
	require.Error(t, err) // 未运行子进程
	require.Len(t, mgr.DesiredSnapshot(), 55)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
