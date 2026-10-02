package process

// 复审 P1-4/P1-5 回归（Worker 侧）：启动前的等待被取消时，必须返回**可重试**错误且
// **不得**把实例记为崩溃——取消只说明「还没来得及判定」，不是「实例崩了」。
//
// 与 CP 侧的口径修复配套（见 controlplane/service 的委托超时用例）：CP 的委托超时现在覆盖
// Worker 的等待预算，故正常情况下 Worker 总能给出自己的结论；一旦 CP 提前取消，
// Worker 也必须立刻收手、保持状态、并让再次点击「启动」可以直接重试。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// TestDaemonStrategyStartCanceledIsRetryableAndNotCrashed 守住取消语义：
//   - 取消立刻收手（不等满上一代退出预算）；
//   - 返回 daemon.ErrStartWaitCanceled（可重试），而不是 ErrPriorExitTimeout（确定性拒绝）；
//   - 实例状态保持为取消前的状态（不得被污染成 CRASHED）。
//
// 转红：①让等待退回固定 time.Sleep（不看 ctx）→ 会等满预算并返回 ErrPriorExitTimeout；
// ②让 startLocked 对取消错误也置 CRASHED → 状态断言失败。
func TestDaemonStrategyStartCanceledIsRetryableAndNotCrashed(t *testing.T) {
	// 预算取长值：一旦实现不再看 ctx，本用例会明显慢下来（并被「立即收手」断言抓住）。
	t.Setenv("JIANMANAGER_START_WAIT_PRIOR_EXIT_TIMEOUT", "5s")

	pidDir := shortDaemonPIDDir(t)
	uuid := "daemon-start-cancel-" + filepath.Base(pidDir)
	// 构造「上一代仍在」：PID 记录指向当前测试进程（必然存活）。
	require.NoError(t, daemon.NewPIDFile(daemon.PIDFileName(pidDir, uuid)).WriteRecord(daemon.PIDRecord{
		WrapperPID: os.Getpid(), JavaPID: os.Getpid(), InstanceUUID: uuid,
	}))
	require.False(t, daemon.SocketServed(pidDir, uuid), "socket 未监听（本用例只针对等待阶段）")

	mgr := NewManager(pidDir)
	require.NoError(t, mgr.Create(uuid, "Daemon", keepAliveCmd(), "", pidDir, nil, false, ProcessTypeDaemon, "", "", 0, 300))
	inst, ok := mgr.GetInstance(uuid)
	require.True(t, ok)
	require.Equal(t, StateStopped, inst.State)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := mgr.StartContext(ctx, uuid)
	elapsed := time.Since(start)

	require.Error(t, err, "取消后不得报告启动成功")
	require.ErrorIs(t, err, daemon.ErrStartWaitCanceled, "必须是可重试的取消错误（实测 %v）", err)
	require.False(t, errors.Is(err, daemon.ErrPriorExitTimeout),
		"取消不得被当作「上一代未退出」这一确定性拒绝")
	assert.Less(t, elapsed, time.Second, "取消必须立即收手，不得等满上一代退出预算（实测 %s）", elapsed)

	inst, ok = mgr.GetInstance(uuid)
	require.True(t, ok)
	require.Equal(t, StateStopped, inst.State,
		"调用方取消不是实例崩溃：状态必须保持原样，否则一次取消会把实例污染成 CRASHED")
}
