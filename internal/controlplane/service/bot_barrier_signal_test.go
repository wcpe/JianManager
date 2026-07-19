package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

func TestBarrierCoordinator_AllDuplicateLateReconnectAndGeneration(t *testing.T) {
	clock := &botLoadFakeClock{now: time.Date(2026, 7, 19, 13, 0, 0, 0, time.UTC)}
	coordinator := NewBarrierCoordinator(clock)
	scope := BarrierScope{RunID: "run", StageIndex: 2, CohortKey: "combat", BarrierKey: "ready", Round: 1}
	require.NoError(t, coordinator.Ensure(BarrierDefinition{
		Scope: scope, ExpectedBots: map[string]int64{"bot-a": 3, "bot-b": 4},
		Release: ScenarioBarrierRelease{Type: "all"}, Deadline: clock.now.Add(time.Minute),
	}))
	// 后续断线/失败视图即使缩小，也不能改变首次冻结分母。
	require.NoError(t, coordinator.Ensure(BarrierDefinition{
		Scope: scope, ExpectedBots: map[string]int64{"bot-a": 3},
		Release: ScenarioBarrierRelease{Type: "all"}, Deadline: clock.now.Add(time.Minute),
	}))

	first := coordinator.Arrive(BarrierArrival{Scope: scope, BotUUID: "bot-a", Generation: 3, ActionRunID: "action-a", CorrelationToken: "token-a"})
	require.Equal(t, BarrierWaiting, first.Decision)
	duplicate := coordinator.Arrive(BarrierArrival{Scope: scope, BotUUID: "bot-a", Generation: 3, ActionRunID: "action-a", CorrelationToken: "token-a"})
	require.Equal(t, BarrierDuplicate, duplicate.Decision)
	stale := coordinator.Arrive(BarrierArrival{Scope: scope, BotUUID: "bot-b", Generation: 3, ActionRunID: "action-b", CorrelationToken: "token-b"})
	require.Equal(t, BarrierStaleGeneration, stale.Decision)

	released := coordinator.Arrive(BarrierArrival{Scope: scope, BotUUID: "bot-b", Generation: 4, ActionRunID: "action-b", CorrelationToken: "token-b"})
	require.Equal(t, BarrierReleased, released.Decision)
	require.NotNil(t, released.Release)
	require.Equal(t, int64(1), released.Release.Round)
	require.Equal(t, clock.now.Add(barrierReleaseLead).UnixMilli(), released.Release.ReleaseAtUnixMS)
	require.Len(t, released.Release.Pending, 2)

	coordinator.MarkDelivered(scope, []string{"bot-a", "bot-b"})
	reconnect := coordinator.Arrive(BarrierArrival{Scope: scope, BotUUID: "bot-a", Generation: 3, ActionRunID: "action-a", CorrelationToken: "token-a"})
	require.Equal(t, BarrierAlreadyReleased, reconnect.Decision)
	require.Equal(t, released.Release.ReleaseAtUnixMS, reconnect.Release.ReleaseAtUnixMS)
	require.Len(t, reconnect.Release.Pending, 1)
	require.Equal(t, "bot-a", reconnect.Release.Pending[0].BotUUID)
}

func TestBarrierCoordinator_CountAndPercentCeil(t *testing.T) {
	tests := []struct {
		name     string
		release  ScenarioBarrierRelease
		expected int
		arrivals int
	}{
		{name: "固定数量", release: ScenarioBarrierRelease{Type: "count", Value: 2}, expected: 5, arrivals: 2},
		{name: "百分比向上取整", release: ScenarioBarrierRelease{Type: "percent", Value: 99}, expected: 101, arrivals: 100},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clock := &botLoadFakeClock{now: time.Date(2026, 7, 19, 13, 0, 0, 0, time.UTC)}
			coordinator := NewBarrierCoordinator(clock)
			scope := BarrierScope{RunID: test.name, CohortKey: "combat", BarrierKey: "ready", Round: 1}
			expected := make(map[string]int64, test.expected)
			for i := 0; i < test.expected; i++ {
				expected[fmt.Sprintf("bot-%03d", i)] = 1
			}
			require.NoError(t, coordinator.Ensure(BarrierDefinition{Scope: scope, ExpectedBots: expected, Release: test.release, Deadline: clock.now.Add(time.Minute)}))
			for i := 0; i < test.arrivals-1; i++ {
				result := coordinator.Arrive(barrierTestArrival(scope, i))
				require.NotEqual(t, BarrierReleased, result.Decision)
			}
			result := coordinator.Arrive(barrierTestArrival(scope, test.arrivals-1))
			require.Equal(t, BarrierReleased, result.Decision)
		})
	}
}

func TestBarrierCoordinator_TimeoutPoliciesAndStopCleanup(t *testing.T) {
	for _, policy := range []string{"fail", "release-arrived"} {
		t.Run(policy, func(t *testing.T) {
			clock := &botLoadFakeClock{now: time.Date(2026, 7, 19, 13, 0, 0, 0, time.UTC)}
			coordinator := NewBarrierCoordinator(clock)
			scope := BarrierScope{RunID: "run-" + policy, CohortKey: "combat", BarrierKey: "ready", Round: 1}
			require.NoError(t, coordinator.Ensure(BarrierDefinition{
				Scope: scope, ExpectedBots: map[string]int64{"bot-a": 1, "bot-b": 1},
				Release: ScenarioBarrierRelease{Type: "all"}, TimeoutPolicy: policy, Deadline: clock.now.Add(time.Second),
			}))
			coordinator.Arrive(BarrierArrival{Scope: scope, BotUUID: "bot-a", Generation: 1, ActionRunID: "action-a", CorrelationToken: "token-a"})
			clock.Advance(2 * time.Second)
			result := coordinator.CheckTimeout(scope)
			if policy == "fail" {
				require.Equal(t, BarrierTimedOut, result.Decision)
				require.Nil(t, result.Release)
			} else {
				require.Equal(t, BarrierReleasedOnTimeout, result.Decision)
				require.Len(t, result.Release.Pending, 1)
			}
			coordinator.StopRun(scope.RunID)
			require.False(t, coordinator.Exists(scope))
		})
	}
}

func barrierTestArrival(scope BarrierScope, ordinal int) BarrierArrival {
	return BarrierArrival{Scope: scope, BotUUID: fmt.Sprintf("bot-%03d", ordinal), Generation: 1, ActionRunID: fmt.Sprintf("action-%03d", ordinal), CorrelationToken: fmt.Sprintf("token-%03d", ordinal)}
}

type fakeWaitingActionFinder struct{ waiting map[string]*WaitingAction }

func (f fakeWaitingActionFinder) FindWaitingAction(_ context.Context, runID, botUUID, actionRunID, token string) (*WaitingAction, error) {
	return f.waiting[runID+"|"+botUUID+"|"+actionRunID+"|"+token], nil
}

type fakeActionSignalClient struct {
	calls     map[string][][]*workerpb.BotActionSignal
	responses map[string]*workerpb.SignalBotActionsResponse
	errors    map[string]error
	handler   func(string, *workerpb.SignalBotActionsRequest) (*workerpb.SignalBotActionsResponse, error)
}

func (f *fakeActionSignalClient) SignalBotActions(_ context.Context, nodeUUID string, request *workerpb.SignalBotActionsRequest) (*workerpb.SignalBotActionsResponse, error) {
	if f.calls == nil {
		f.calls = map[string][][]*workerpb.BotActionSignal{}
	}
	f.calls[nodeUUID] = append(f.calls[nodeUUID], request.Signals)
	if f.handler != nil {
		return f.handler(nodeUUID, request)
	}
	if err := f.errors[nodeUUID]; err != nil {
		return nil, err
	}
	return f.responses[nodeUUID], nil
}

func TestActionSignalRouter_RejectsWrongCorrelationAndGroupsByExecutor(t *testing.T) {
	finder := fakeWaitingActionFinder{waiting: map[string]*WaitingAction{
		"run|bot-a|action-a|token-a": waitingActionFixture("bot-a", "action-a", "token-a", "node-a"),
		"run|bot-b|action-b|token-b": waitingActionFixture("bot-b", "action-b", "token-b", "node-b"),
	}}
	client := &fakeActionSignalClient{responses: map[string]*workerpb.SignalBotActionsResponse{
		"node-a": signalResponse("action-a", true, false, "", ""),
		"node-b": signalResponse("action-b", true, false, "", ""),
	}}
	router := NewActionSignalRouter(finder, client, &botLoadFakeClock{now: time.Now()})
	inputs := []ActionSignalInput{
		{RunID: "run", BotUUID: "bot-a", ActionRunID: "action-a", CorrelationToken: "token-a", Type: "barrier-release", Payload: []byte(`{"round":1}`)},
		{RunID: "run", BotUUID: "bot-b", ActionRunID: "action-b", CorrelationToken: "token-b", Type: "barrier-release", Payload: []byte(`{"round":1}`)},
		{RunID: "wrong", BotUUID: "bot-a", ActionRunID: "action-a", CorrelationToken: "token-a", Type: "probe", Payload: []byte(`{}`)},
		{RunID: "run", BotUUID: "bot-a", ActionRunID: "action-a", CorrelationToken: "wrong", Type: "probe", Payload: []byte(`{}`)},
	}

	report := router.Route(context.Background(), inputs)
	require.Len(t, client.calls["node-a"], 1)
	require.Len(t, client.calls["node-b"], 1)
	require.Len(t, report.Items, 4)
	require.True(t, report.Items[0].Accepted)
	require.True(t, report.Items[1].Accepted)
	require.Equal(t, ActionSignalRejected, report.Items[2].Status)
	require.Equal(t, ActionSignalRejected, report.Items[3].Status)
	require.Equal(t, int64(7), client.calls["node-a"][0][0].Generation)
}

func TestActionSignalRouter_PartialFailureCanRetryWithStableReceiptIdentity(t *testing.T) {
	finder := fakeWaitingActionFinder{waiting: map[string]*WaitingAction{
		"run|bot-a|action-a|token-a": waitingActionFixture("bot-a", "action-a", "token-a", "node-a"),
		"run|bot-b|action-b|token-b": waitingActionFixture("bot-b", "action-b", "token-b", "node-a"),
	}}
	client := &fakeActionSignalClient{responses: map[string]*workerpb.SignalBotActionsResponse{
		"node-a": {Results: []*workerpb.SignalBotActionItemResult{
			{SignalId: stableActionSignalID(ActionSignalInput{RunID: "run", BotUUID: "bot-a", ActionRunID: "action-a", CorrelationToken: "token-a", Type: "probe", Payload: []byte(`{}`)}), Accepted: true},
			{SignalId: stableActionSignalID(ActionSignalInput{RunID: "run", BotUUID: "bot-b", ActionRunID: "action-b", CorrelationToken: "token-b", Type: "probe", Payload: []byte(`{}`)}), ErrorCode: "BUSY", Error: "稍后重试"},
		}},
	}}
	router := NewActionSignalRouter(finder, client, &botLoadFakeClock{now: time.Now()})
	inputs := []ActionSignalInput{
		{RunID: "run", BotUUID: "bot-a", ActionRunID: "action-a", CorrelationToken: "token-a", Type: "probe", Payload: []byte(`{}`)},
		{RunID: "run", BotUUID: "bot-b", ActionRunID: "action-b", CorrelationToken: "token-b", Type: "probe", Payload: []byte(`{}`)},
	}

	first := router.Route(context.Background(), inputs)
	require.True(t, first.Items[0].Accepted)
	require.True(t, first.Items[1].Retriable)
	retryInputs := first.RetryableInputs()
	require.Len(t, retryInputs, 1)
	client.responses["node-a"] = signalResponse("action-b", false, true, "", "")
	second := router.Route(context.Background(), retryInputs)
	require.True(t, second.Items[0].Skipped)
	require.False(t, second.Items[0].Retriable)
	require.Equal(t, first.Items[1].SignalID, second.Items[0].SignalID)
}

func TestActionSignalRouter_ReportsNodeCallFailureAsRetriable(t *testing.T) {
	finder := fakeWaitingActionFinder{waiting: map[string]*WaitingAction{"run|bot-a|action-a|token-a": waitingActionFixture("bot-a", "action-a", "token-a", "node-a")}}
	client := &fakeActionSignalClient{errors: map[string]error{"node-a": errors.New("节点断开")}, responses: map[string]*workerpb.SignalBotActionsResponse{}}
	router := NewActionSignalRouter(finder, client, &botLoadFakeClock{now: time.Now()})
	report := router.Route(context.Background(), []ActionSignalInput{{RunID: "run", BotUUID: "bot-a", ActionRunID: "action-a", CorrelationToken: "token-a", Type: "probe", Payload: []byte(`{}`)}})
	require.True(t, report.Items[0].Retriable)
	require.Contains(t, report.Items[0].Error, "节点断开")
}

func waitingActionFixture(botUUID, actionRunID, token, nodeUUID string) *WaitingAction {
	return &WaitingAction{
		Result: model.BotLoadActionResult{ActionRunID: actionRunID, StepID: "wait", CorrelationToken: token, Status: model.BotLoadActionRunning},
		Bot:    model.Bot{UUID: botUUID, DesiredStateGeneration: 7}, SessionUUID: "run",
		ExecutorNodeID: 1, ExecutorNodeUUID: nodeUUID, Generation: 7,
	}
}

func signalResponse(actionRunID string, accepted, skipped bool, errorCode, message string) *workerpb.SignalBotActionsResponse {
	input := ActionSignalInput{RunID: "run", BotUUID: "bot-" + actionRunID[len(actionRunID)-1:], ActionRunID: actionRunID, CorrelationToken: "token-" + actionRunID[len(actionRunID)-1:], Type: "barrier-release", Payload: []byte(`{"round":1}`)}
	if actionRunID == "action-b" && accepted == false {
		input.Type, input.Payload = "probe", []byte(`{}`)
	}
	return &workerpb.SignalBotActionsResponse{Results: []*workerpb.SignalBotActionItemResult{{SignalId: stableActionSignalID(input), Accepted: accepted, Skipped: skipped, ErrorCode: errorCode, Error: message}}}
}

type fixedBarrierExpectedBots map[string]int64

func (f fixedBarrierExpectedBots) ExpectedBots(context.Context, string, string) (map[string]int64, error) {
	return f, nil
}

func TestScenarioActionEventService_BarrierReleaseKeepsFailedDeliveriesForRetry(t *testing.T) {
	h := newBotActionResultHarness(t)
	botB := &model.Bot{
		UUID: "bot-barrier-b", InstanceID: h.bot.InstanceID, StressSessionID: &h.session.ID,
		ExecutorNodeID: h.bot.ExecutorNodeID, Name: "load-002", Status: model.BotStatusConnected,
		DesiredStateGeneration: 3, CohortKey: "combat", Config: `{}`, Behavior: "idle",
	}
	require.NoError(t, h.db.Create(botB).Error)
	clock := &botLoadFakeClock{now: h.now}
	client := &fakeActionSignalClient{}
	attempt := 0
	client.handler = func(_ string, request *workerpb.SignalBotActionsRequest) (*workerpb.SignalBotActionsResponse, error) {
		attempt++
		results := make([]*workerpb.SignalBotActionItemResult, 0, len(request.Signals))
		for index, signal := range request.Signals {
			result := &workerpb.SignalBotActionItemResult{SignalId: signal.SignalId, Accepted: true}
			if attempt == 1 && index == 1 {
				result.Accepted, result.ErrorCode, result.Error = false, "BUSY", "稍后重试"
			}
			results = append(results, result)
		}
		return &workerpb.SignalBotActionsResponse{Results: results}, nil
	}
	barriers := NewBarrierCoordinator(clock)
	router := NewActionSignalRouter(h.service, client, clock)
	events := NewScenarioActionEventService(h.service, barriers, router, fixedBarrierExpectedBots{h.bot.UUID: 3, botB.UUID: 3})
	coordinator := NewBotFleetRuntimeCoordinator(NewBotFleetRuntimeService(h.db, clock), &botFleetFakeClient{}, nil)
	coordinator.SetActionEventHandler(events)
	scope := BarrierScope{RunID: h.session.UUID, StageIndex: 1, CohortKey: "combat", BarrierKey: "ready", Round: 1}

	first := barrierActionEvent(h.bot.UUID, h.session.UUID, "00000000-0000-0000-0000-000000000401", "token-a", h.now)
	second := barrierActionEvent(botB.UUID, h.session.UUID, "00000000-0000-0000-0000-000000000402", "token-b", h.now)
	_, err := coordinator.HandleEvent(context.Background(), h.node.ID, h.node.UUID, h.session.UUID, &workerpb.BotFleetEvent{Event: &workerpb.BotFleetEvent_ActionEvent{ActionEvent: first}})
	require.NoError(t, err)
	require.Empty(t, client.calls)
	_, err = coordinator.HandleEvent(context.Background(), h.node.ID, h.node.UUID, h.session.UUID, &workerpb.BotFleetEvent{Event: &workerpb.BotFleetEvent_ActionEvent{ActionEvent: second}})
	require.NoError(t, err)
	require.Len(t, barriers.PendingRelease(scope).Pending, 1)
	releaseAt := barriers.PendingRelease(scope).ReleaseAtUnixMS

	report := events.RetryBarrierRelease(context.Background(), scope)
	require.Len(t, report.Items, 1)
	require.True(t, report.Items[0].Accepted)
	require.Empty(t, barriers.PendingRelease(scope).Pending)
	require.Equal(t, releaseAt, barriers.PendingRelease(scope).ReleaseAtUnixMS)
}

func barrierActionEvent(botUUID, runID, actionRunID, token string, now time.Time) *workerpb.BotActionEvent {
	payload := fmt.Sprintf(`{"type":"barrier-arrived","stageIndex":1,"cohortKey":"combat","barrierKey":"ready","round":1,"release":{"type":"all"},"deadlineUnixMs":%d}`, now.Add(time.Minute).UnixMilli())
	return &workerpb.BotActionEvent{
		BotUuid: botUUID, SessionUuid: runID, Generation: 3, ActionRunId: actionRunID,
		StepId: "ready", Attempt: 1, Status: "running", CorrelationToken: token,
		ResultJson: payload, ObservedAtUnixMs: now.UnixMilli(),
	}
}
