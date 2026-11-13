package main

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

func TestAssembleBotLoadServices_CreatesSharedProcessServices(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	bundle, err := assembleBotLoadServices(db, cpgrpc.NewClientPool(), "stable-server-secret")
	require.NoError(t, err)
	t.Cleanup(bundle.subscriptions.Close)
	require.NotNil(t, bundle.capacity)
	require.NotNil(t, bundle.reservations)
	require.NotNil(t, bundle.signer)
	require.NotNil(t, bundle.preflight)
	require.NotNil(t, bundle.execution)
	require.NotNil(t, bundle.actionResults)
	require.NotNil(t, bundle.barriers)
	require.NotNil(t, bundle.signalRouter)
	require.NotNil(t, bundle.scenarioEvents)
	require.NotNil(t, bundle.coordinator)
	require.NotNil(t, bundle.subscriptions)
	require.Same(t, bundle.execution, bundle.coordinator.SnapshotReconciler())
	require.Same(t, bundle.execution, bundle.coordinator.RuntimeObserver())
	require.Same(t, bundle.scenarioEvents, bundle.coordinator.ActionEventHandler())
	require.Same(t, bundle.coordinator, bundle.subscriptions.RuntimeCoordinator())
	require.Same(t, bundle.subscriptions, bundle.execution.FleetSubscriptionManager())
}

func TestAssembleBotLoadServices_RejectsMissingStableSecret(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	_, err = assembleBotLoadServices(db, cpgrpc.NewClientPool(), "")
	require.Error(t, err)
}

type mainTestFleetSubscriptions struct {
	targets []service.BotFleetSubscriptionTarget
}

func (s *mainTestFleetSubscriptions) Ensure(target service.BotFleetSubscriptionTarget) {
	s.targets = append(s.targets, target)
}

func (s *mainTestFleetSubscriptions) Restore(targets []service.BotFleetSubscriptionTarget) {
	for _, target := range targets {
		s.Ensure(target)
	}
}

func (*mainTestFleetSubscriptions) StopSession(string) {}

func TestRecoverConnectedBotFleetSubscriptions_FiltersStartupAndReconnectNodes(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}, &model.BotStressSession{}, &model.BotLoadBatch{}))
	nodeA := &model.Node{Name: "node-a", Host: "127.0.0.1", Secret: "secret"}
	nodeB := &model.Node{Name: "node-b", Host: "127.0.0.2", Secret: "secret"}
	require.NoError(t, db.Create(nodeA).Error)
	require.NoError(t, db.Create(nodeB).Error)
	instance := &model.Instance{NodeID: nodeA.ID, Name: "target", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, WorkDir: "var/servers/target", StartCommand: "java"}
	require.NoError(t, db.Create(instance).Error)
	session := &model.BotStressSession{InstanceID: instance.ID, Name: "fleet", NamePrefix: "fleet", BotCount: 2, Status: model.BotStressSessionRunning}
	require.NoError(t, db.Create(session).Error)
	batches := []model.BotLoadBatch{
		{StressSessionID: session.ID, ExecutorNodeID: nodeA.ID, Ordinal: 1, PlannedCount: 1, State: model.BotLoadBatchRunning, IdempotencyKey: "main-recover-a", ConnectStartAt: time.Now().UTC()},
		{StressSessionID: session.ID, ExecutorNodeID: nodeB.ID, Ordinal: 2, PlannedCount: 1, State: model.BotLoadBatchRunning, IdempotencyKey: "main-recover-b", ConnectStartAt: time.Now().UTC()},
	}
	require.NoError(t, db.Create(&batches).Error)

	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest(nodeA.UUID, nil)
	subscriptions := &mainTestFleetSubscriptions{}
	execution := service.NewBotLoadExecutionService(db, nil, nil, nil, nil, nil, nil)
	execution.SetFleetSubscriptionManager(subscriptions)

	require.NoError(t, recoverConnectedBotFleetSubscriptions(context.Background(), execution, pool))
	require.Len(t, subscriptions.targets, 1)
	require.Equal(t, nodeA.UUID, subscriptions.targets[0].NodeUUID)
	require.NoError(t, recoverConnectedBotFleetSubscriptions(context.Background(), execution, pool, nodeB.UUID))
	require.Len(t, subscriptions.targets, 1)

	pool.SetWorkerClientForTest(nodeB.UUID, nil)
	require.NoError(t, recoverConnectedBotFleetSubscriptions(context.Background(), execution, pool, nodeB.UUID))
	require.Len(t, subscriptions.targets, 2)
	require.Equal(t, nodeB.UUID, subscriptions.targets[1].NodeUUID)
}

func TestRunControlPlaneServer_ClosesSubscriptionsBeforeReturningError(t *testing.T) {
	wantErr := errors.New("listen failed")
	closed := false

	err := runControlPlaneServer(
		func() error { return wantErr },
		nil,
		func() { closed = true },
		nil,
		make(chan os.Signal), // 永不触发：本用例走 run 自行返回这一支
	)

	require.ErrorIs(t, err, wantErr)
	require.True(t, closed)
}

// TestRunControlPlaneServer_ClosesLoggerOnSignal 守护 CP 的日志优雅关闭。
//
// 背景：修复前 CP 全无 signal 处理，且 closePlatformLogger 全仓零调用，
// 于是 rollog.Writer.Close 里「等后台 gzip 收尾」永不执行——SIGTERM 直接杀进程，
// 退出时留未压缩的 .gz.tmp。Worker 侧一直是对的，两边行为分叉。
//
// 断言的是可观测契约本身：收到退出信号 ⇒ closeLogger 被调用一次。
func TestRunControlPlaneServer_ClosesLoggerOnSignal(t *testing.T) {
	// 三个回调都在 runControlPlaneServer 的 goroutine 里执行，断言在主 goroutine。
	// 用带缓冲 channel 传递事实而不是写共享 bool：后者是真实的数据竞态
	// （-race 能抓到），且“没调到”与“还没跑到”会混成一个值。
	shutdownCh := make(chan struct{}, 1)
	subsCh := make(chan struct{}, 1)
	loggerCh := make(chan struct{}, 1)

	sigCh := make(chan os.Signal, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- runControlPlaneServer(
			func() error { select {} }, // 永不自行返回：必须靠信号退出
			func(context.Context) error { shutdownCh <- struct{}{}; return nil },
			func() { subsCh <- struct{}{} },
			func() { loggerCh <- struct{}{} },
			sigCh,
		)
	}()

	sigCh <- syscall.SIGTERM

	select {
	case err := <-errCh:
		require.NoError(t, err, "正常退出不应报错")
	case <-time.After(5 * time.Second):
		t.Fatal("收到信号后未在 5s 内退出")
	}

	// runControlPlaneServer 返回时三个回调必已执行完毕，故这里非阻塞收。
	select {
	case <-loggerCh:
	default:
		t.Fatal("收到退出信号必须调用 closeLogger，否则后台 gzip 收尾被硬切")
	}
	select {
	case <-subsCh:
	default:
		t.Fatal("收到退出信号必须关闭订阅")
	}
	select {
	case <-shutdownCh:
	default:
		t.Fatal("必须先优雅关闭 HTTP 服务，切断在途请求是旧行为")
	}
}

// TestRunControlPlaneServer_ClosesLoggerWhenRunFailsWithoutSignal 守护启动失败路径。
//
// 旧实现只在 err != nil 时关 subscriptions，日志则从未关过；
// 端口被占这类启动失败同样需要把日志文件收尾。
func TestRunControlPlaneServer_ClosesLoggerWhenRunFailsWithoutSignal(t *testing.T) {
	wantErr := errors.New("address already in use")
	loggerCalls := make(chan struct{}, 1)

	err := runControlPlaneServer(
		func() error { return wantErr },
		nil,
		nil,
		func() { loggerCalls <- struct{}{} },
		make(chan os.Signal),
	)

	require.ErrorIs(t, err, wantErr)
	select {
	case <-loggerCalls:
	default:
		t.Fatal("启动失败也必须收尾日志文件")
	}
}
