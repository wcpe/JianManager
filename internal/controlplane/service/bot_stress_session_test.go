package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

func newBotStressSessionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}, &model.Bot{}, &model.BotStressSession{}, &model.BotLoadBatch{}))
	return db
}

func createBotStressInstance(t *testing.T, db *gorm.DB) *model.Instance {
	t.Helper()
	node := &model.Node{Name: "node", Host: "127.0.0.1", GRPCPort: 9100, WSPort: 9101, Secret: "secret"}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{
		NodeID:       node.ID,
		Name:         "inst",
		Type:         model.InstanceTypeMinecraftJava,
		ProcessType:  model.ProcessTypeDirect,
		StartCommand: "java -jar server.jar",
		ServerPort:   25565,
	}
	require.NoError(t, db.Create(inst).Error)
	return inst
}

func newBotStressSessionService(t *testing.T, db *gorm.DB) *BotStressSessionService {
	t.Helper()
	var node model.Node
	require.NoError(t, db.First(&node).Error)

	worker := &fakeStressCreateBotWorker{}
	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest(node.UUID, worker)
	return NewBotStressSessionService(db, NewBotService(db, pool))
}

func TestBotStressSession_CreatePersistsFields(t *testing.T) {
	db := newBotStressSessionTestDB(t)
	inst := createBotStressInstance(t, db)
	svc := newBotStressSessionService(t, db)

	sess, err := svc.Create(CreateBotStressSessionRequest{
		InstanceID: inst.ID,
		Count:      3,
		Behavior:   "idle",
		NamePrefix: "load",
		Config:     json.RawMessage(`{"server":"127.0.0.1","port":25565}`),
	})

	require.NoError(t, err)
	assert.Equal(t, model.BotStressSessionPending, sess.Status)
	assert.Equal(t, 3, sess.Count)
	assert.Equal(t, "idle", sess.Behavior)
	assert.Equal(t, "load", sess.NamePrefix)
	assert.JSONEq(t, `{"server":"127.0.0.1","port":25565}`, string(sess.Config))
}

func TestBotStressSession_CreatePersistsOrchestrationYAML(t *testing.T) {
	db := newBotStressSessionTestDB(t)
	inst := createBotStressInstance(t, db)
	svc := newBotStressSessionService(t, db)

	sess, err := svc.Create(CreateBotStressSessionRequest{
		InstanceID:        inst.ID,
		Count:             3,
		NamePrefix:        "load",
		Config:            json.RawMessage(`{"server":"127.0.0.1","port":25565}`),
		OrchestrationYAML: validStressOrchestrationYAML,
	})

	require.NoError(t, err)
	assert.Equal(t, "idle", sess.Behavior)
	assert.Equal(t, validStressOrchestrationYAML, sess.OrchestrationYAML)
	require.NotNil(t, sess.OrchestrationSummary)
	assert.Equal(t, 4, sess.OrchestrationSummary.PhaseCount)
	assert.Equal(t, []string{"idle", "patrol", "guard", "custom"}, sess.OrchestrationSummary.Behaviors)

	var row model.BotStressSession
	require.NoError(t, db.First(&row, sess.ID).Error)
	assert.Equal(t, validStressOrchestrationYAML, row.OrchestrationYAML)
	assert.Contains(t, row.OrchestrationSummary, `"phaseCount":4`)
}

func TestBotStressSession_CreateKeepsLegacyBehaviorWithoutOrchestration(t *testing.T) {
	db := newBotStressSessionTestDB(t)
	inst := createBotStressInstance(t, db)
	svc := newBotStressSessionService(t, db)

	sess, err := svc.Create(CreateBotStressSessionRequest{
		InstanceID: inst.ID,
		Count:      1,
		Behavior:   "guard",
		NamePrefix: "legacy",
	})

	require.NoError(t, err)
	assert.Equal(t, "guard", sess.Behavior)
	assert.Empty(t, sess.OrchestrationYAML)
	assert.Nil(t, sess.OrchestrationSummary)
}

func TestBotStressSession_StartCreatesAssociatedBots(t *testing.T) {
	db := newBotStressSessionTestDB(t)
	inst := createBotStressInstance(t, db)
	var node model.Node
	require.NoError(t, db.First(&node, inst.NodeID).Error)
	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest(node.UUID, &fakeStressCreateBotWorker{})
	svc := NewBotStressSessionService(db, NewBotService(db, pool))
	sess, err := svc.Create(CreateBotStressSessionRequest{
		InstanceID: inst.ID,
		Count:      2,
		Behavior:   "guard",
		NamePrefix: "stress",
	})
	require.NoError(t, err)

	view, err := svc.Start(sess.ID)

	require.NoError(t, err)
	assert.Equal(t, model.BotStressSessionRunning, view.Status)
	assert.Equal(t, int64(2), view.Counts.Total)
	assert.Equal(t, int64(2), view.Counts.ByStatus[string(model.BotStatusConnecting)])

	var bots []model.Bot
	require.NoError(t, db.Where("stress_session_id = ?", sess.ID).Order("id ASC").Find(&bots).Error)
	require.Len(t, bots, 2)
	assert.Equal(t, "stress-001", bots[0].Name)
	assert.Equal(t, "guard", bots[0].Behavior)
}

type fakeStressCreateBotWorker struct {
	workerpb.WorkerServiceClient
	requests  []*workerpb.CreateBotRequest
	listBots  []*workerpb.BotInfo
	listCalls int
}

func (f *fakeStressCreateBotWorker) CreateBot(_ context.Context, req *workerpb.CreateBotRequest, _ ...grpc.CallOption) (*workerpb.CreateBotResponse, error) {
	f.requests = append(f.requests, proto.Clone(req).(*workerpb.CreateBotRequest))
	return &workerpb.CreateBotResponse{Success: true, Status: "connecting"}, nil
}

func (f *fakeStressCreateBotWorker) DeleteBot(_ context.Context, _ *workerpb.DeleteBotRequest, _ ...grpc.CallOption) (*workerpb.DeleteBotResponse, error) {
	return &workerpb.DeleteBotResponse{Success: true}, nil
}

func (f *fakeStressCreateBotWorker) ListBots(_ context.Context, _ *workerpb.ListBotsRequest, _ ...grpc.CallOption) (*workerpb.ListBotsResponse, error) {
	f.listCalls++
	return &workerpb.ListBotsResponse{Bots: f.listBots}, nil
}

func TestBotStressSession_StartUsesOrchestratedBehaviorConfig(t *testing.T) {
	db := newBotStressSessionTestDB(t)
	inst := createBotStressInstance(t, db)
	var node model.Node
	require.NoError(t, db.First(&node, inst.NodeID).Error)

	worker := &fakeStressCreateBotWorker{}
	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest(node.UUID, worker)
	svc := NewBotStressSessionService(db, NewBotService(db, pool))
	sess, err := svc.Create(CreateBotStressSessionRequest{
		InstanceID:        inst.ID,
		Count:             2,
		NamePrefix:        "stress",
		OrchestrationYAML: validStressOrchestrationYAML,
	})
	require.NoError(t, err)

	view, err := svc.Start(sess.ID)

	require.NoError(t, err)
	assert.Equal(t, model.BotStressSessionRunning, view.Status)
	require.Len(t, worker.requests, 2)
	assert.Equal(t, "orchestrated", worker.requests[0].Behavior)
	assert.NotEmpty(t, worker.requests[0].BehaviorConfig)
	assert.Equal(t, "orchestrated", worker.requests[1].Behavior)

	var cfg struct {
		StartDelayMS int `json:"startDelayMs"`
		Phases       []struct {
			DurationMS int    `json:"durationMs"`
			Behavior   string `json:"behavior"`
		} `json:"phases"`
	}
	require.NoError(t, json.Unmarshal([]byte(worker.requests[1].BehaviorConfig), &cfg))
	assert.Equal(t, 500, cfg.StartDelayMS)
	require.Len(t, cfg.Phases, 4)
	assert.Equal(t, "idle", cfg.Phases[0].Behavior)

	var bots []model.Bot
	require.NoError(t, db.Where("stress_session_id = ?", sess.ID).Order("id ASC").Find(&bots).Error)
	require.Len(t, bots, 2)
	assert.Equal(t, "orchestrated", bots[0].Behavior)
	assert.Equal(t, "orchestrated", bots[1].Behavior)
}

func TestBotStressSession_GetRefreshesCountsFromWorker(t *testing.T) {
	db := newBotStressSessionTestDB(t)
	inst := createBotStressInstance(t, db)
	var node model.Node
	require.NoError(t, db.First(&node, inst.NodeID).Error)

	worker := &fakeStressCreateBotWorker{}
	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest(node.UUID, worker)
	svc := NewBotStressSessionService(db, NewBotService(db, pool))
	sess, err := svc.Create(CreateBotStressSessionRequest{
		InstanceID: inst.ID,
		Count:      2,
		Behavior:   "idle",
		NamePrefix: "stress",
	})
	require.NoError(t, err)
	_, err = svc.Start(sess.ID)
	require.NoError(t, err)
	require.Len(t, worker.requests, 2)
	worker.listBots = []*workerpb.BotInfo{
		{BotUuid: worker.requests[0].BotUuid, Status: "connected"},
		{BotUuid: worker.requests[1].BotUuid, Status: "connected"},
	}

	view, err := svc.Get(sess.ID)

	require.NoError(t, err)
	assert.Equal(t, int64(2), view.Counts.Total)
	assert.Equal(t, int64(2), view.Counts.ByStatus[string(model.BotStatusConnected)])
	assert.Equal(t, 2, worker.listCalls)
}

func TestBotStressSession_ReadsDoNotOverwriteFleetRuntimeLedger(t *testing.T) {
	db := newBotStressSessionTestDB(t)
	inst := createBotStressInstance(t, db)
	var node model.Node
	require.NoError(t, db.First(&node, inst.NodeID).Error)

	session := &model.BotStressSession{
		InstanceID: inst.ID, Name: "fleet", NamePrefix: "fleet", BotCount: 1,
		Status: model.BotStressSessionRunning,
	}
	require.NoError(t, db.Create(session).Error)
	batch := &model.BotLoadBatch{
		StressSessionID: session.ID, ExecutorNodeID: node.ID, Ordinal: 1, PlannedCount: 1,
		AcceptedCount: 1, ConnectedCount: 1, State: model.BotLoadBatchRunning,
		IdempotencyKey: "fleet-read-ledger", ConnectStartAt: time.Now().UTC(),
	}
	require.NoError(t, db.Create(batch).Error)
	executorNodeID, batchID := node.ID, batch.ID
	bot := &model.Bot{
		InstanceID: inst.ID, StressSessionID: &session.ID, ExecutorNodeID: &executorNodeID, LoadBatchID: &batchID,
		Name: "fleet-001", Status: model.BotStatusConnected,
	}
	require.NoError(t, db.Create(bot).Error)

	worker := &fakeStressCreateBotWorker{listBots: []*workerpb.BotInfo{{BotUuid: bot.UUID, Status: "disconnected"}}}
	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest(node.UUID, worker)
	botSvc := NewBotService(db, pool)
	svc := NewBotStressSessionService(db, botSvc)

	detail, err := svc.Get(session.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), detail.Counts.ByStatus[string(model.BotStatusConnected)])
	list, err := svc.List(BotStressSessionListQuery{}, nil, false)
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	require.Equal(t, int64(1), list.Items[0].Counts.ByStatus[string(model.BotStatusConnected)])
	botDetail, err := botSvc.GetByID(bot.ID)
	require.NoError(t, err)
	require.Equal(t, model.BotStatusConnected, botDetail.Status)
	botList, err := botSvc.ListPaged(BotListQuery{Filter: BotFilter{NodeID: &node.ID}}, nil, false)
	require.NoError(t, err)
	require.Len(t, botList.Items, 1)
	require.Equal(t, model.BotStatusConnected, botList.Items[0].Status)
	require.Zero(t, worker.listCalls)

	var loadedBot model.Bot
	require.NoError(t, db.First(&loadedBot, bot.ID).Error)
	require.Equal(t, model.BotStatusConnected, loadedBot.Status)
	var loadedBatch model.BotLoadBatch
	require.NoError(t, db.First(&loadedBatch, batch.ID).Error)
	require.Equal(t, 1, loadedBatch.ConnectedCount)
}

func TestBotStressSession_StartIsIdempotent(t *testing.T) {
	db := newBotStressSessionTestDB(t)
	inst := createBotStressInstance(t, db)
	svc := newBotStressSessionService(t, db)
	sess, err := svc.Create(CreateBotStressSessionRequest{
		InstanceID: inst.ID,
		Count:      2,
		Behavior:   "guard",
		NamePrefix: "stress",
	})
	require.NoError(t, err)

	_, err = svc.Start(sess.ID)
	require.NoError(t, err)
	view, err := svc.Start(sess.ID)

	require.NoError(t, err)
	assert.Equal(t, int64(2), view.Counts.Total)
	var count int64
	require.NoError(t, db.Model(&model.Bot{}).Where("stress_session_id = ?", sess.ID).Count(&count).Error)
	assert.Equal(t, int64(2), count)
}

func TestBotStressSession_GetReturnsOrchestration(t *testing.T) {
	db := newBotStressSessionTestDB(t)
	inst := createBotStressInstance(t, db)
	svc := newBotStressSessionService(t, db)
	sess, err := svc.Create(CreateBotStressSessionRequest{
		InstanceID:        inst.ID,
		Count:             1,
		NamePrefix:        "load",
		OrchestrationYAML: validStressOrchestrationYAML,
	})
	require.NoError(t, err)

	got, err := svc.Get(sess.ID)

	require.NoError(t, err)
	assert.Equal(t, validStressOrchestrationYAML, got.OrchestrationYAML)
	require.NotNil(t, got.OrchestrationSummary)
	assert.True(t, got.OrchestrationSummary.Enabled)
	assert.Equal(t, 330, got.OrchestrationSummary.DurationSec)
}

func TestBotStressSession_StopMarksAssociatedBotsStopped(t *testing.T) {
	db := newBotStressSessionTestDB(t)
	inst := createBotStressInstance(t, db)
	svc := newBotStressSessionService(t, db)
	sess, err := svc.Create(CreateBotStressSessionRequest{
		InstanceID: inst.ID,
		Count:      2,
		Behavior:   "idle",
		NamePrefix: "stress",
	})
	require.NoError(t, err)
	_, err = svc.Start(sess.ID)
	require.NoError(t, err)

	view, err := svc.Stop(sess.ID)

	require.NoError(t, err)
	assert.Equal(t, model.BotStressSessionStopped, view.Status)
	assert.Equal(t, int64(2), view.Counts.Total)
	assert.Equal(t, int64(2), view.Counts.ByStatus[string(model.BotStatusStopped)])
}

func TestBotStressSession_CreateRejectsInvalidInput(t *testing.T) {
	db := newBotStressSessionTestDB(t)
	inst := createBotStressInstance(t, db)
	svc := newBotStressSessionService(t, db)

	_, err := svc.Create(CreateBotStressSessionRequest{InstanceID: inst.ID, Count: 0, Behavior: "idle", NamePrefix: "stress"})
	assert.Error(t, err)

	_, err = svc.Create(CreateBotStressSessionRequest{InstanceID: inst.ID, Count: 1, Behavior: "idle"})
	assert.Error(t, err)
}

const validScenarioV2JSON = `{"version":2,"seed":20260719,"cohorts":[{"key":"all","percent":100,"steps":[{"id":"observe","type":"roam_in_area","observationStep":true,"durationMs":1000,"area":{"type":"radius","center":{"x":0,"y":64,"z":0},"radius":2}}]}]}`

const validScenarioV2YAML = `version: 2
seed: 20260719
cohorts:
  - key: all
    percent: 100
    steps:
      - id: observe
        type: roam_in_area
        observationStep: true
        durationMs: 1000
        area:
          type: radius
          center: {x: 0, y: 64, z: 0}
          radius: 2
`

func TestBotStressSession_CreatePersistsCanonicalScenarioFromJSONAndYAML(t *testing.T) {
	tests := []struct {
		name     string
		scenario json.RawMessage
	}{
		{name: "JSON 对象", scenario: json.RawMessage(validScenarioV2JSON)},
		{name: "YAML 字符串", scenario: mustJSONRawString(t, validScenarioV2YAML)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := newBotStressSessionTestDB(t)
			inst := createBotStressInstance(t, db)
			svc := newBotStressSessionService(t, db)

			created, err := svc.Create(CreateBotStressSessionRequest{
				InstanceID: inst.ID, Count: 2, NamePrefix: "scenario", Scenario: test.scenario,
			})
			require.NoError(t, err)
			require.Equal(t, "scenario_v2", created.Behavior)
			require.NotNil(t, created.Scenario)
			require.Equal(t, int64(20260719), created.Scenario.Seed)

			var row model.BotStressSession
			require.NoError(t, db.First(&row, created.ID).Error)
			stored, err := ParseScenarioSnapshot(row.ScenarioSnapshot)
			require.NoError(t, err)
			base := stored.Cohorts[0].Steps[0].Base()
			require.Equal(t, 3_600_000, *base.TimeoutMS)
			require.Equal(t, 1, *base.MaxAttempts)
			detail, err := svc.Get(created.ID)
			require.NoError(t, err)
			require.NotNil(t, detail.Scenario)
			require.Equal(t, "all", detail.Scenario.Cohorts[0].Key)
		})
	}
}

func TestBotStressSession_CreateV1BuildsSnapshotAndKeepsOriginalYAML(t *testing.T) {
	db := newBotStressSessionTestDB(t)
	inst := createBotStressInstance(t, db)
	svc := newBotStressSessionService(t, db)

	created, err := svc.Create(CreateBotStressSessionRequest{
		InstanceID: inst.ID, Count: 2, NamePrefix: "legacy", OrchestrationYAML: validStressOrchestrationYAML,
	})
	require.NoError(t, err)
	require.Equal(t, validStressOrchestrationYAML, created.OrchestrationYAML)
	require.Nil(t, created.Scenario)

	var row model.BotStressSession
	require.NoError(t, db.First(&row, created.ID).Error)
	require.Equal(t, validStressOrchestrationYAML, row.OrchestrationYAML)
	require.NotEmpty(t, row.ScenarioSnapshot)
	stored, err := ParseScenarioSnapshot(row.ScenarioSnapshot)
	require.NoError(t, err)
	require.Equal(t, ScenarioActionLegacyBehavior, stored.Cohorts[0].Steps[1].Type())
	assignments, cohortJSON, _, err := prepareBotLoadScenarioAssignments(&row)
	require.NoError(t, err)
	require.Len(t, assignments, row.BotCount)
	require.Contains(t, cohortJSON["legacy"], `"type":"legacy_behavior"`)
}

func TestBotStressSession_CreateLegacyBehaviorWithoutContinuousActionKeepsFallback(t *testing.T) {
	db := newBotStressSessionTestDB(t)
	inst := createBotStressInstance(t, db)
	svc := newBotStressSessionService(t, db)

	created, err := svc.Create(CreateBotStressSessionRequest{
		InstanceID: inst.ID, Count: 1, Behavior: "idle", NamePrefix: "legacy",
	})
	require.NoError(t, err)
	require.Equal(t, "idle", created.Behavior)
	require.Nil(t, created.Scenario)

	var row model.BotStressSession
	require.NoError(t, db.First(&row, created.ID).Error)
	require.Empty(t, row.ScenarioSnapshot)
}

func TestBotStressSession_CreateV1ConversionFailureFallsBackToOriginalFlow(t *testing.T) {
	db := newBotStressSessionTestDB(t)
	inst := createBotStressInstance(t, db)
	svc := newBotStressSessionService(t, db)
	raw := "phases:\n  - durationSec: 10\n    behavior: custom\n    steps: []\n"

	created, err := svc.Create(CreateBotStressSessionRequest{
		InstanceID: inst.ID, Count: 1, NamePrefix: "legacy", OrchestrationYAML: raw,
	})
	require.NoError(t, err)
	require.Equal(t, "custom", created.Behavior)
	require.Equal(t, raw, created.OrchestrationYAML)
	require.Nil(t, created.Scenario)

	var row model.BotStressSession
	require.NoError(t, db.First(&row, created.ID).Error)
	require.Empty(t, row.ScenarioSnapshot)
}

func mustJSONRawString(t *testing.T, value string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return raw
}
