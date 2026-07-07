package service

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	grpcapi "google.golang.org/grpc"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

type fakeFR038BotWorker struct {
	workerpb.WorkerServiceClient
	listed            int
	setBehaviorCalls  []*workerpb.SetBotBehaviorRequest
	createBotCalls    []*workerpb.CreateBotRequest
	deleteBotCalls    []*workerpb.DeleteBotRequest
	listBotsResponse  *workerpb.ListBotsResponse
}

func (w *fakeFR038BotWorker) ListBots(context.Context, *workerpb.ListBotsRequest, ...grpcapi.CallOption) (*workerpb.ListBotsResponse, error) {
	w.listed++
	if w.listBotsResponse != nil {
		return w.listBotsResponse, nil
	}
	return &workerpb.ListBotsResponse{}, nil
}

func (w *fakeFR038BotWorker) SetBotBehavior(_ context.Context, req *workerpb.SetBotBehaviorRequest, _ ...grpcapi.CallOption) (*workerpb.SetBotBehaviorResponse, error) {
	w.setBehaviorCalls = append(w.setBehaviorCalls, req)
	return &workerpb.SetBotBehaviorResponse{Success: true}, nil
}

func (w *fakeFR038BotWorker) CreateBot(_ context.Context, req *workerpb.CreateBotRequest, _ ...grpcapi.CallOption) (*workerpb.CreateBotResponse, error) {
	w.createBotCalls = append(w.createBotCalls, req)
	return &workerpb.CreateBotResponse{Success: true, Status: "connecting"}, nil
}

func (w *fakeFR038BotWorker) DeleteBot(_ context.Context, req *workerpb.DeleteBotRequest, _ ...grpcapi.CallOption) (*workerpb.DeleteBotResponse, error) {
	w.deleteBotCalls = append(w.deleteBotCalls, req)
	return &workerpb.DeleteBotResponse{Success: true}, nil
}

func newFR038BotScaleDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}, &model.Bot{}))
	return db
}

func createFR038Node(t *testing.T, db *gorm.DB, name, uuid string) model.Node {
	t.Helper()
	node := model.Node{Name: name, UUID: uuid, Host: "127.0.0.1", GRPCPort: 9100, WSPort: 9200, Secret: "secret", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&node).Error)
	return node
}

func createFR038Instance(t *testing.T, db *gorm.DB, nodeID uint, name string, port int) model.Instance {
	t.Helper()
	inst := model.Instance{
		NodeID:       nodeID,
		Name:         name,
		Type:         model.InstanceTypeMinecraftJava,
		Role:         model.InstanceRoleBackend,
		ProcessType:  model.ProcessTypeDirect,
		Status:       model.InstanceStatusRunning,
		StartCommand: "java -jar server.jar",
		WorkDir:      "/srv/" + name,
		ServerPort:   port,
	}
	require.NoError(t, db.Create(&inst).Error)
	return inst
}

func createFR038Bot(t *testing.T, db *gorm.DB, instanceID uint, name string, status model.BotStatus, behavior string) model.Bot {
	t.Helper()
	bot := model.Bot{InstanceID: instanceID, Name: name, Status: status, Behavior: behavior, Config: `{"auth":"offline"}`}
	require.NoError(t, db.Create(&bot).Error)
	return bot
}

func TestFR038BotScaleServiceListSummaryAndWorkerRefresh(t *testing.T) {
	db := newFR038BotScaleDB(t)
	pool := cpgrpc.NewClientPool()
	svc := NewBotService(db, pool)
	nodeA := createFR038Node(t, db, "节点A", "node-a")
	nodeB := createFR038Node(t, db, "节点B", "node-b")
	instA := createFR038Instance(t, db, nodeA.ID, "生存服", 25565)
	instB := createFR038Instance(t, db, nodeB.ID, "空岛服", 25566)
	botA := createFR038Bot(t, db, instA.ID, "GuardBot", model.BotStatusConnecting, "guard")
	createFR038Bot(t, db, instB.ID, "PatrolBot", model.BotStatusError, "patrol")

	worker := &fakeFR038BotWorker{listBotsResponse: &workerpb.ListBotsResponse{Bots: []*workerpb.BotInfo{{BotUuid: botA.UUID, Status: "connected"}}}}
	pool.SetWorkerClientForTest(nodeA.UUID, worker)

	query := BotListQuery{Filter: BotFilter{NodeID: &nodeA.ID}, Page: 0, PageSize: 200}
	list, err := svc.ListPaged(query, []uint{instA.ID}, true)
	require.NoError(t, err)
	require.Equal(t, int64(1), list.Total)
	require.Equal(t, 1, list.Page)
	require.Equal(t, 100, list.PageSize)
	require.Len(t, list.Items, 1)
	require.Equal(t, model.BotStatusConnected, list.Items[0].Status)
	require.Equal(t, 1, worker.listed)

	summary, err := svc.Summary(BotFilter{}, "node", []uint{instA.ID}, true)
	require.NoError(t, err)
	require.Equal(t, int64(1), summary.Total)
	require.Equal(t, int64(1), summary.ByStatus[string(model.BotStatusConnected)])
	require.Equal(t, "node", summary.GroupBy)
	require.Len(t, summary.Groups, 1)
	require.Equal(t, "节点A", summary.Groups[0].Label)
	require.Equal(t, int64(1), summary.Groups[0].Online)
}

func TestFR038BotScaleBatchDelegatesAndSkipsOutOfScope(t *testing.T) {
	db := newFR038BotScaleDB(t)
	pool := cpgrpc.NewClientPool()
	svc := NewBotService(db, pool)
	nodeA := createFR038Node(t, db, "节点A", "node-a")
	nodeB := createFR038Node(t, db, "节点B", "node-b")
	instA := createFR038Instance(t, db, nodeA.ID, "生存服", 25565)
	instB := createFR038Instance(t, db, nodeB.ID, "空岛服", 25566)
	botA := createFR038Bot(t, db, instA.ID, "GuardBot", model.BotStatusConnected, "idle")
	botB := createFR038Bot(t, db, instB.ID, "PatrolBot", model.BotStatusConnected, "idle")
	worker := &fakeFR038BotWorker{}
	pool.SetWorkerClientForTest(nodeA.UUID, worker)

	res, err := svc.Batch(BotBatchRequest{
		Action:   BotBatchSetBehavior,
		IDs:      []uint{botA.ID, botB.ID, 99999},
		Behavior: "follow",
		Target:   "Steve",
	}, []uint{instA.ID}, true)
	require.NoError(t, err)
	require.Equal(t, 1, res.Requested)
	require.Equal(t, 1, res.Succeeded)
	require.Equal(t, 0, res.Failed)
	require.Equal(t, 2, res.Skipped)
	require.Len(t, worker.setBehaviorCalls, 1)
	require.Equal(t, botA.UUID, worker.setBehaviorCalls[0].BotUuid)
	require.Equal(t, "follow", worker.setBehaviorCalls[0].Behavior)
	require.Equal(t, "Steve", worker.setBehaviorCalls[0].Target)

	var updatedA model.Bot
	require.NoError(t, db.First(&updatedA, botA.ID).Error)
	require.Equal(t, "follow", updatedA.Behavior)
	var updatedB model.Bot
	require.NoError(t, db.First(&updatedB, botB.ID).Error)
	require.Equal(t, "idle", updatedB.Behavior)
}
