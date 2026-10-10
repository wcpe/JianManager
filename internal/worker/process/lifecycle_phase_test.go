package process

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 停止已经提交后再取消重启，旧实例正常收尾但绝不提交新启动。
func TestRV016RestartCanceledAfterStop(t *testing.T) {
	m := NewManager(t.TempDir())
	const uuid = "rv016-stop-phase"
	require.NoError(t, createDirect(m, uuid, "实例", keepAliveCmd(), t.TempDir()))
	s := installRunningStrategy(t, m, uuid)
	ctx, cancel := context.WithCancel(context.Background())
	done := runLifecycleOperation(func() error { return m.RestartContext(ctx, uuid) })
	waitForSignal(t, s.stopStarted, "重启未进入优雅停止")
	cancel()
	s.releaseStop()
	require.ErrorIs(t, waitForResult(t, done, "停止后取消重启未收尾"), context.Canceled)
	require.Equal(t, StateStopped, mustGetState(t, m, uuid))
	require.Zero(t, s.startedCount(), "已停止未新启阶段不得迟到启动")
	require.NoError(t, m.Start(uuid), "取消收尾必须释放 gate")
	require.Equal(t, 1, s.startedCount())
}

// 所有受控排队操作应及时取消，保持原有策略不受影响。
func TestRV016LifecycleQueueCanceled(t *testing.T) {
	for _, action := range []string{"stop", "restart", "kill"} {
		t.Run(action, func(t *testing.T) {
			m := NewManager(t.TempDir())
			const uuid = "rv016-queued-op"
			require.NoError(t, createDirect(m, uuid, "实例", keepAliveCmd(), t.TempDir()))
			s := installRunningStrategy(t, m, uuid)
			inst, _ := m.GetInstance(uuid)
			inst.operationMu.Lock()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			done := runLifecycleOperation(func() error {
				switch action {
				case "stop":
					return m.StopContext(ctx, uuid)
				case "restart":
					return m.RestartContext(ctx, uuid)
				default:
					return m.KillContext(ctx, uuid)
				}
			})
			select {
			case err := <-done:
				require.ErrorIs(t, err, context.DeadlineExceeded)
				inst.operationMu.Unlock()
			case <-time.After(300 * time.Millisecond):
				inst.operationMu.Unlock()
				s.releaseStop()
				<-done
				t.Fatal("排队取消未及时退出")
			}
			require.Equal(t, StateRunning, mustGetState(t, m, uuid))
			require.Zero(t, s.startedCount())
			select {
			case <-s.stopStarted:
				t.Fatal("排队取消不能下发停止")
			default:
			}
			// 同一个 gate 仍可获得，且不存在迟到操作。
			require.NoError(t, inst.operationMu.LockContext(context.Background()))
			inst.operationMu.Unlock()
		})
	}
}

type gateObservedContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (ctx *gateObservedContext) Err() error {
	ctx.once.Do(func() { close(ctx.entered) })
	return ctx.Context.Err()
}

// 等待期间实例被替换时，获得令牌也不能对旧身份发起任何动作。
func TestRV016GateRechecksInstanceIdentity(t *testing.T) {
	m := NewManager(t.TempDir())
	const uuid = "rv016-identity"
	require.NoError(t, createDirect(m, uuid, "实例", keepAliveCmd(), t.TempDir()))
	old, _ := m.GetInstance(uuid)
	old.operationMu.Lock()
	replacement := &Instance{UUID: uuid, State: StateStopped}
	entered := make(chan struct{})
	done := make(chan error, 1)
	ctx := &gateObservedContext{Context: context.Background(), entered: entered}
	go func() { done <- m.StartContext(ctx, uuid) }()
	<-entered
	// gate 首次检查 ctx 时已取得旧实例引用；用屏障确认再替换，不依赖调度时延。
	m.mu.Lock()
	m.instances[uuid] = replacement
	m.mu.Unlock()
	old.operationMu.Unlock()
	require.Error(t, waitForResult(t, done, "实例身份复核未完成"))
	require.Equal(t, StateStopped, mustGetState(t, m, uuid))
	require.NoError(t, old.operationMu.LockContext(context.Background()))
	old.operationMu.Unlock()
}
