package service

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
	grpcapi "google.golang.org/grpc"
	"gorm.io/gorm"
)

func TestBotExecutorResolver_ResolveExecutorOrFallback(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}, &model.BotLoadBatch{}, &model.Bot{}))

	targetNode := &model.Node{Name: "target", Host: "127.0.0.1", Secret: "target-secret"}
	executorNode := &model.Node{Name: "executor", Host: "127.0.0.2", Secret: "executor-secret"}
	require.NoError(t, db.Create(targetNode).Error)
	require.NoError(t, db.Create(executorNode).Error)
	instance := &model.Instance{NodeID: targetNode.ID, Name: "target", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, WorkDir: "var/servers/target", StartCommand: "java"}
	require.NoError(t, db.Create(instance).Error)

	executorID := executorNode.ID
	distributed := &model.Bot{InstanceID: instance.ID, ExecutorNodeID: &executorID, Name: "distributed"}
	legacy := &model.Bot{InstanceID: instance.ID, Name: "legacy"}
	require.NoError(t, db.Create(distributed).Error)
	require.NoError(t, db.Create(legacy).Error)

	resolver := NewBotExecutorResolver(db)
	resolved, resolvedInstance, err := resolver.Resolve(distributed)
	require.NoError(t, err)
	require.Equal(t, executorNode.ID, resolved.ID)
	require.Equal(t, instance.ID, resolvedInstance.ID)

	resolved, _, err = resolver.Resolve(legacy)
	require.NoError(t, err)
	require.Equal(t, targetNode.ID, resolved.ID)
}

func TestApplyFilter_NodeUsesExecutorNodeWithLegacyFallback(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}, &model.BotLoadBatch{}, &model.Bot{}))

	targetNode := &model.Node{Name: "target", Host: "127.0.0.1", Secret: "target-secret"}
	executorNode := &model.Node{Name: "executor", Host: "127.0.0.2", Secret: "executor-secret"}
	require.NoError(t, db.Create(targetNode).Error)
	require.NoError(t, db.Create(executorNode).Error)
	instance := &model.Instance{NodeID: targetNode.ID, Name: "target", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, WorkDir: "var/servers/target", StartCommand: "java"}
	require.NoError(t, db.Create(instance).Error)

	executorID := executorNode.ID
	require.NoError(t, db.Create(&model.Bot{InstanceID: instance.ID, ExecutorNodeID: &executorID, Name: "distributed"}).Error)
	require.NoError(t, db.Create(&model.Bot{InstanceID: instance.ID, Name: "legacy"}).Error)

	filterExecutor := BotFilter{NodeID: &executorNode.ID}
	var executorCount int64
	require.NoError(t, applyFilter(db.Model(&model.Bot{}), filterExecutor, nil, false).Count(&executorCount).Error)
	require.EqualValues(t, 1, executorCount)

	filterTarget := BotFilter{NodeID: &targetNode.ID}
	var targetCount int64
	require.NoError(t, applyFilter(db.Model(&model.Bot{}), filterTarget, nil, false).Count(&targetCount).Error)
	require.EqualValues(t, 1, targetCount)
}

type fakeExecutorRouteWorker struct {
	workerpb.WorkerServiceClient
	botUUID       string
	createCalls   int
	deleteCalls   int
	behaviorCalls int
	commandCalls  int
	listCalls     int
	streamCalls   int
}

func (f *fakeExecutorRouteWorker) CreateBot(context.Context, *workerpb.CreateBotRequest, ...grpcapi.CallOption) (*workerpb.CreateBotResponse, error) {
	f.createCalls++
	return &workerpb.CreateBotResponse{Success: true, Status: "connecting"}, nil
}

func (f *fakeExecutorRouteWorker) DeleteBot(context.Context, *workerpb.DeleteBotRequest, ...grpcapi.CallOption) (*workerpb.DeleteBotResponse, error) {
	f.deleteCalls++
	return &workerpb.DeleteBotResponse{Success: true}, nil
}

func (f *fakeExecutorRouteWorker) SetBotBehavior(context.Context, *workerpb.SetBotBehaviorRequest, ...grpcapi.CallOption) (*workerpb.SetBotBehaviorResponse, error) {
	f.behaviorCalls++
	return &workerpb.SetBotBehaviorResponse{Success: true}, nil
}

func (f *fakeExecutorRouteWorker) SendBotCommand(context.Context, *workerpb.SendBotCommandRequest, ...grpcapi.CallOption) (*workerpb.SendBotCommandResponse, error) {
	f.commandCalls++
	return &workerpb.SendBotCommandResponse{Success: true}, nil
}

func (f *fakeExecutorRouteWorker) ListBots(context.Context, *workerpb.ListBotsRequest, ...grpcapi.CallOption) (*workerpb.ListBotsResponse, error) {
	f.listCalls++
	return &workerpb.ListBotsResponse{Bots: []*workerpb.BotInfo{{BotUuid: f.botUUID, Status: "connected"}}}, nil
}

func (f *fakeExecutorRouteWorker) StreamBotEvents(context.Context, *workerpb.StreamBotEventsRequest, ...grpcapi.CallOption) (workerpb.WorkerService_StreamBotEventsClient, error) {
	f.streamCalls++
	return nil, nil
}

func TestBotService_RoutesExistingOperationsToExecutorNode(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}, &model.BotLoadBatch{}, &model.Bot{}))

	targetNode := &model.Node{Name: "target", UUID: "target-node", Host: "127.0.0.1", Secret: "target-secret"}
	executorNode := &model.Node{Name: "executor", UUID: "executor-node", Host: "127.0.0.2", Secret: "executor-secret"}
	require.NoError(t, db.Create(targetNode).Error)
	require.NoError(t, db.Create(executorNode).Error)
	instance := &model.Instance{NodeID: targetNode.ID, Name: "target", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, WorkDir: "var/servers/target", StartCommand: "java", ServerPort: 25565}
	require.NoError(t, db.Create(instance).Error)

	executorID := executorNode.ID
	botRecord := &model.Bot{InstanceID: instance.ID, ExecutorNodeID: &executorID, Name: "distributed", Behavior: "idle", Config: `{"auth":"offline"}`}
	require.NoError(t, db.Create(botRecord).Error)

	worker := &fakeExecutorRouteWorker{botUUID: botRecord.UUID}
	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest(executorNode.UUID, worker)
	svc := NewBotService(db, pool)

	require.NoError(t, svc.delegateCreateBot(botRecord, nil))
	loaded, err := svc.GetByID(botRecord.ID)
	require.NoError(t, err)
	require.Equal(t, model.BotStatusConnected, loaded.Status)
	require.NoError(t, svc.UpdateBehavior(botRecord.ID, "guard"))
	require.NoError(t, svc.SendCommand(botRecord.ID, "/spawn"))
	_, _, err = svc.StreamEvents(context.Background(), botRecord.ID)
	require.NoError(t, err)

	stopResult, err := svc.Batch(BotBatchRequest{Action: BotBatchStop, IDs: []uint{botRecord.ID}}, nil, false)
	require.NoError(t, err)
	require.Equal(t, 1, stopResult.Succeeded)
	startResult, err := svc.Batch(BotBatchRequest{Action: BotBatchStart, IDs: []uint{botRecord.ID}}, nil, false)
	require.NoError(t, err)
	require.Equal(t, 1, startResult.Succeeded)
	require.NoError(t, svc.Delete(botRecord.ID))

	require.Equal(t, 2, worker.createCalls)
	require.Equal(t, 2, worker.deleteCalls)
	require.Equal(t, 1, worker.behaviorCalls)
	require.Equal(t, 1, worker.commandCalls)
	require.GreaterOrEqual(t, worker.listCalls, 1)
	require.Equal(t, 1, worker.streamCalls)
}
