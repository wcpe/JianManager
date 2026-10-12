package grpc

import (
	"context"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/daemon"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
	gogrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func localLifecycleManager(t *testing.T) (*process.Manager, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("本回归 helper 使用本地 Unix shell，不涉及真实游戏服")
	}
	t.Setenv(daemon.EnvCgroupIsolation, "0")
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	m := process.NewManager(dir)
	const uuid = "rv016-local-helper"
	require.NoError(t, m.Create(uuid, "本地 helper", "exec sleep 30", "", dir, nil, false, process.ProcessTypeDirect, "", "", 0, 0))
	t.Cleanup(func() { _ = m.Kill(uuid) })
	return m, uuid
}

// 真 RPC 排队取消：服务端 handler 及时退出，并且不对随后启动的同一实例迟到停止/重启/强杀。
func TestRV016RPCQueuedLifecycleCancellation(t *testing.T) {
	for _, action := range []string{"stop", "restart", "kill"} {
		t.Run(action, func(t *testing.T) {
			m, uuid := localLifecycleManager(t)
			entered, release, handled := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			m.SetInstanceStartHandler(func(string) { close(entered); <-release })
			srv := NewServer(m, "node", nil, nil, nil)
			client := lifecycleRPCClient(t, srv, gogrpc.UnaryInterceptor(func(ctx context.Context, req any, info *gogrpc.UnaryServerInfo, next gogrpc.UnaryHandler) (any, error) {
				defer close(handled)
				return next(ctx, req)
			}))
			startDone := make(chan error, 1)
			go func() { startDone <- m.StartContext(context.Background(), uuid) }()
			<-entered
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
			defer cancel()
			req := &workerpb.InstanceActionRequest{InstanceUuid: uuid}
			var err error
			switch action {
			case "stop":
				_, err = client.StopInstance(ctx, req)
			case "restart":
				_, err = client.RestartInstance(ctx, req)
			default:
				_, err = client.KillInstance(ctx, req)
			}
			require.Equal(t, codes.DeadlineExceeded, status.Code(err))
			handlerExited := false
			select {
			case <-handled:
				handlerExited = true
			case <-time.After(300 * time.Millisecond):
			}
			unblock()
			require.NoError(t, <-startDone)
			// 等迟到 handler 收尾再断言，红例也不能泄漏生命周期协程。
			if !handlerExited {
				select {
				case <-handled:
				case <-time.After(time.Second):
					t.Fatal("RPC handler 未收尾")
				}
			}
			require.True(t, handlerExited, "排队取消应在前一个操作结束前归还请求")
			state, err := m.GetState(uuid)
			require.NoError(t, err)
			require.Equal(t, process.StateRunning, state)
			require.True(t, daemon.IsPIDAlive(m.GetInstancePID(uuid)), "取消的排队操作不能终止后来启动的 helper")
		})
	}
}

// 已启动阶段：故意丢弃成功响应再取消 RPC，真实受管 helper 必须继续运行。
func TestRV016RPCStartedButResponseLost(t *testing.T) {
	m, uuid := localLifecycleManager(t)
	srv := NewServer(m, "node", nil, nil, nil)
	committed := make(chan struct{})
	client := lifecycleRPCClient(t, srv, gogrpc.UnaryInterceptor(func(ctx context.Context, req any, info *gogrpc.UnaryServerInfo, next gogrpc.UnaryHandler) (any, error) {
		result, err := next(ctx, req)
		if err != nil {
			return result, err
		}
		close(committed)
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := client.StartInstance(ctx, &workerpb.InstanceActionRequest{InstanceUuid: uuid})
	require.Equal(t, codes.DeadlineExceeded, status.Code(err))
	<-committed
	state, err := m.GetState(uuid)
	require.NoError(t, err)
	require.Equal(t, process.StateRunning, state)
	require.True(t, daemon.IsPIDAlive(m.GetInstancePID(uuid)), "响应丢失与客户端取消不是停止意图")
}

// 真 RPC 停止后取消：用状态回调屏障确保旧代已停止、取消已抵达服务端，再释放重启后半段。
func TestRV016RPCRestartCanceledAfterStopped(t *testing.T) {
	m, uuid := localLifecycleManager(t)
	require.NoError(t, m.Start(uuid))
	srv := NewServer(m, "node", nil, nil, nil)
	stopped, resume, canceled, handlerDone := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(resume) }) }
	defer unblock()
	m.SetStateChangeHandler(func(_ string, _ process.InstanceState, next process.InstanceState) {
		if next == process.StateStopped {
			close(stopped)
			<-resume
		}
	})
	defer m.SetStateChangeHandler(nil)
	client := lifecycleRPCClient(t, srv, gogrpc.UnaryInterceptor(func(ctx context.Context, req any, info *gogrpc.UnaryServerInfo, next gogrpc.UnaryHandler) (any, error) {
		go func() { <-ctx.Done(); close(canceled) }()
		defer close(handlerDone)
		return next(ctx, req)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := client.RestartInstance(ctx, &workerpb.InstanceActionRequest{InstanceUuid: uuid})
		result <- err
	}()
	<-stopped
	cancel()
	<-canceled
	unblock()
	require.Equal(t, codes.Canceled, status.Code(<-result))
	<-handlerDone
	m.SetStateChangeHandler(nil)
	state, err := m.GetState(uuid)
	require.NoError(t, err)
	require.Equal(t, process.StateStopped, state)
	require.Eventually(t, func() bool { return m.GetInstancePID(uuid) == 0 }, time.Second, 5*time.Millisecond, "停止后取消不能又生成新的 helper")
}
