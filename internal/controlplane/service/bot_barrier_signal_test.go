package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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

type fakeWaitingActionFinder struct {
	waiting map[string]*WaitingAction
	err     error
}

func (f fakeWaitingActionFinder) FindWaitingAction(_ context.Context, runID, botUUID, actionRunID, token string) (*WaitingAction, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.waiting[runID+"|"+botUUID+"|"+actionRunID+"|"+token], nil
}

type fakeActionSignalClient struct {
	mu        sync.Mutex
	calls     map[string][][]*workerpb.BotActionSignal
	responses map[string]*workerpb.SignalBotActionsResponse
	errors    map[string]error
	handler   func(context.Context, string, *workerpb.SignalBotActionsRequest) (*workerpb.SignalBotActionsResponse, error)
}

func (f *fakeActionSignalClient) SignalBotActions(ctx context.Context, nodeUUID string, request *workerpb.SignalBotActionsRequest) (*workerpb.SignalBotActionsResponse, error) {
	f.mu.Lock()
	if f.calls == nil {
		f.calls = map[string][][]*workerpb.BotActionSignal{}
	}
	f.calls[nodeUUID] = append(f.calls[nodeUUID], append([]*workerpb.BotActionSignal(nil), request.Signals...))
	handler, err, response := f.handler, f.errors[nodeUUID], f.responses[nodeUUID]
	f.mu.Unlock()
	if handler != nil {
		return handler(ctx, nodeUUID, request)
	}
	if err != nil {
		return nil, err
	}
	return response, nil
}

func (f *fakeActionSignalClient) callCount(nodeUUID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls[nodeUUID])
}

func (f *fakeActionSignalClient) callSizes(nodeUUID string) []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	sizes := make([]int, 0, len(f.calls[nodeUUID]))
	for _, call := range f.calls[nodeUUID] {
		sizes = append(sizes, len(call))
	}
	return sizes
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

func TestActionSignalRouter_ReportsLookupFailureAsRetriable(t *testing.T) {
	router := NewActionSignalRouter(fakeWaitingActionFinder{err: errors.New("数据库繁忙")}, &fakeActionSignalClient{}, &botLoadFakeClock{now: time.Now()})
	report := router.Route(context.Background(), []ActionSignalInput{{RunID: "run", BotUUID: "bot-a", ActionRunID: "action-a", CorrelationToken: "token-a", Type: "probe", Payload: []byte(`{}`)}})
	require.True(t, report.Items[0].Retriable)
	require.Equal(t, "LOOKUP_FAILED", report.Items[0].ErrorCode)
}

func TestActionSignalRouter_ChunksSameNodeAndBoundsRPCDeadline(t *testing.T) {
	for _, total := range []int{101, 200} {
		t.Run(fmt.Sprintf("%d条", total), func(t *testing.T) {
			finder := fakeWaitingActionFinder{waiting: make(map[string]*WaitingAction, total)}
			inputs := make([]ActionSignalInput, 0, total)
			for index := 0; index < total; index++ {
				botUUID := fmt.Sprintf("bot-%03d", index)
				actionRunID := fmt.Sprintf("action-%03d", index)
				token := fmt.Sprintf("token-%03d", index)
				finder.waiting["run|"+botUUID+"|"+actionRunID+"|"+token] = waitingActionFixture(botUUID, actionRunID, token, "node-a")
				inputs = append(inputs, ActionSignalInput{
					RunID: "run", BotUUID: botUUID, ActionRunID: actionRunID,
					CorrelationToken: token, Type: "probe", Payload: []byte(`{"eventType":"ready"}`),
				})
			}
			client := &fakeActionSignalClient{}
			client.handler = func(ctx context.Context, _ string, request *workerpb.SignalBotActionsRequest) (*workerpb.SignalBotActionsResponse, error) {
				deadline, ok := ctx.Deadline()
				require.True(t, ok, "每次 Worker RPC 必须有 3 秒 deadline")
				require.LessOrEqual(t, time.Until(deadline), 3*time.Second)
				results := make([]*workerpb.SignalBotActionItemResult, 0, len(request.Signals))
				for _, signal := range request.Signals {
					results = append(results, &workerpb.SignalBotActionItemResult{SignalId: signal.SignalId, Accepted: true})
				}
				return &workerpb.SignalBotActionsResponse{Results: results}, nil
			}
			router := NewActionSignalRouter(finder, client, nil)

			report := router.Route(context.Background(), inputs)
			require.Len(t, report.Items, total)
			for _, item := range report.Items {
				require.True(t, item.Accepted)
			}
			if total == 101 {
				require.Equal(t, []int{100, 1}, client.callSizes("node-a"))
			} else {
				require.Equal(t, []int{100, 100}, client.callSizes("node-a"))
			}
		})
	}
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

type countingBarrierExpectedBots struct {
	calls atomic.Int32
	bots  map[string]int64
}

func (p *countingBarrierExpectedBots) ExpectedBots(context.Context, string, string) (map[string]int64, error) {
	p.calls.Add(1)
	return cloneExpectedBots(p.bots), nil
}

func TestScenarioActionEventService_RejectsAuthoritativeBarrierPayloadTampering(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*barrierArrivedPayload)
	}{
		{name: "固定stageIndex", mutate: func(payload *barrierArrivedPayload) { payload.StageIndex = 1 }},
		{name: "cohortKey", mutate: func(payload *barrierArrivedPayload) { payload.CohortKey = "lobby" }},
		{name: "barrierKey", mutate: func(payload *barrierArrivedPayload) { payload.BarrierKey = "forged" }},
		{name: "release-count", mutate: func(payload *barrierArrivedPayload) {
			payload.Release = ScenarioBarrierRelease{Type: "count", Value: 1}
		}},
		{name: "release-percent", mutate: func(payload *barrierArrivedPayload) {
			payload.Release = ScenarioBarrierRelease{Type: "percent", Value: 50}
		}},
		{name: "timeoutPolicy", mutate: func(payload *barrierArrivedPayload) { payload.TimeoutPolicy = "release-arrived" }},
		{name: "deadline", mutate: func(payload *barrierArrivedPayload) { payload.DeadlineUnixMS++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newBotActionResultHarness(t)
			setBarrierScenario(t, h, time.Minute, ScenarioBarrierRelease{Type: "all"}, "fail")
			provider := &countingBarrierExpectedBots{bots: map[string]int64{h.bot.UUID: h.bot.DesiredStateGeneration}}
			events := NewScenarioActionEventService(h.service, NewBarrierCoordinator(&botLoadFakeClock{now: h.now}), NewActionSignalRouter(h.service, &fakeActionSignalClient{}, nil), provider)
			t.Cleanup(events.Close)
			event := barrierActionEvent(h.bot.UUID, h.session.UUID, "00000000-0000-0000-0000-000000000410", "token-a", h.now)
			payload := authoritativeBarrierPayload(h.now, time.Minute, ScenarioBarrierRelease{Type: "all"}, "fail")
			test.mutate(&payload)
			raw, err := json.Marshal(payload)
			require.NoError(t, err)
			event.ResultJson = string(raw)

			result, err := events.Ingest(context.Background(), h.node.ID, h.session.UUID, event)
			require.NoError(t, err)
			require.Equal(t, ActionResultIgnoredInvalid, result.Decision)
			require.Zero(t, provider.calls.Load(), "篡改 payload 不应触发 expected set 查询")
			var count int64
			require.NoError(t, h.db.Model(&model.BotLoadActionResult{}).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

func TestScenarioActionEventService_FreezesExpectedSetOnlyOnFirstArrival(t *testing.T) {
	h := newBotActionResultHarness(t)
	setBarrierScenario(t, h, time.Minute, ScenarioBarrierRelease{Type: "all"}, "fail")
	botB := &model.Bot{
		UUID: "bot-barrier-freeze-b", InstanceID: h.bot.InstanceID, StressSessionID: &h.session.ID,
		ExecutorNodeID: h.bot.ExecutorNodeID, Name: "load-002", Status: model.BotStatusConnected,
		DesiredStateGeneration: 3, CohortKey: "combat", Config: `{}`, Behavior: "idle",
	}
	require.NoError(t, h.db.Create(botB).Error)
	provider := &countingBarrierExpectedBots{bots: map[string]int64{h.bot.UUID: 3, botB.UUID: 3}}
	client := &fakeActionSignalClient{}
	client.handler = acceptActionSignals
	events := NewScenarioActionEventService(h.service, NewBarrierCoordinator(&botLoadFakeClock{now: h.now}), NewActionSignalRouter(h.service, client, nil), provider)
	t.Cleanup(events.Close)

	first := barrierActionEvent(h.bot.UUID, h.session.UUID, "00000000-0000-0000-0000-000000000411", "token-a", h.now)
	second := barrierActionEvent(botB.UUID, h.session.UUID, "00000000-0000-0000-0000-000000000412", "token-b", h.now)
	_, err := events.Ingest(context.Background(), h.node.ID, h.session.UUID, first)
	require.NoError(t, err)
	_, err = events.Ingest(context.Background(), h.node.ID, h.session.UUID, second)
	require.NoError(t, err)
	require.Equal(t, int32(1), provider.calls.Load())
}

func TestScenarioActionEventService_SchedulerRetriesReleaseArrivedWithBoundedBackoff(t *testing.T) {
	h := newBotActionResultHarness(t)
	setBarrierScenario(t, h, 100*time.Millisecond, ScenarioBarrierRelease{Type: "all"}, "release-arrived")
	now := time.Now().UTC()
	provider := fixedBarrierExpectedBots{h.bot.UUID: 3, "bot-never-arrives": 3}
	client := &fakeActionSignalClient{}
	var attempts atomic.Int32
	client.handler = func(_ context.Context, _ string, request *workerpb.SignalBotActionsRequest) (*workerpb.SignalBotActionsResponse, error) {
		if attempts.Add(1) == 1 {
			return nil, errors.New("节点暂不可用")
		}
		return acceptActionSignals(context.Background(), "", request)
	}
	barriers := NewBarrierCoordinator(nil)
	events := NewScenarioActionEventService(h.service, barriers, NewActionSignalRouter(h.service, client, nil), provider)
	t.Cleanup(events.Close)
	event := barrierActionEvent(h.bot.UUID, h.session.UUID, "00000000-0000-0000-0000-000000000413", "token-a", now)
	event.ResultJson = mustBarrierPayload(t, authoritativeBarrierPayload(now, 100*time.Millisecond, ScenarioBarrierRelease{Type: "all"}, "release-arrived"))
	event.ObservedAtUnixMs = now.UnixMilli()

	_, err := events.Ingest(context.Background(), h.node.ID, h.session.UUID, event)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return attempts.Load() == 1 }, time.Second, 10*time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, int32(1), attempts.Load(), "失败重试不得形成紧循环")
	require.Eventually(t, func() bool { return attempts.Load() >= 2 }, 2*time.Second, 10*time.Millisecond)
	scope := BarrierScope{RunID: h.session.UUID, StageIndex: 0, CohortKey: "combat", BarrierKey: "ready", Round: 1}
	require.Eventually(t, func() bool {
		release := barriers.PendingRelease(scope)
		return release != nil && len(release.Pending) == 0
	}, time.Second, 10*time.Millisecond)
}

func TestScenarioActionEventService_StopRunCancelsInFlightRelease(t *testing.T) {
	h := newBotActionResultHarness(t)
	setBarrierScenario(t, h, time.Second, ScenarioBarrierRelease{Type: "all"}, "fail")
	started := make(chan struct{})
	cancelled := make(chan struct{})
	client := &fakeActionSignalClient{}
	client.handler = func(ctx context.Context, _ string, _ *workerpb.SignalBotActionsRequest) (*workerpb.SignalBotActionsResponse, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	}
	barriers := NewBarrierCoordinator(nil)
	events := NewScenarioActionEventService(h.service, barriers, NewActionSignalRouter(h.service, client, nil), fixedBarrierExpectedBots{h.bot.UUID: 3})
	t.Cleanup(events.Close)
	now := time.Now().UTC()
	event := barrierActionEvent(h.bot.UUID, h.session.UUID, "00000000-0000-0000-0000-000000000414", "token-a", now)
	event.ResultJson = mustBarrierPayload(t, authoritativeBarrierPayload(now, time.Second, ScenarioBarrierRelease{Type: "all"}, "fail"))
	event.ObservedAtUnixMs = now.UnixMilli()

	_, err := events.Ingest(context.Background(), h.node.ID, h.session.UUID, event)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		select {
		case <-started:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)
	events.StopRun(h.session.UUID)
	require.Eventually(t, func() bool {
		select {
		case <-cancelled:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)
	require.False(t, barriers.Exists(BarrierScope{RunID: h.session.UUID, StageIndex: 0, CohortKey: "combat", BarrierKey: "ready", Round: 1}))
}

func TestScenarioActionEventService_BarrierReleaseKeepsFailedDeliveriesForRetry(t *testing.T) {
	h := newBotActionResultHarness(t)
	setBarrierScenario(t, h, time.Minute, ScenarioBarrierRelease{Type: "all"}, "fail")
	botB := &model.Bot{
		UUID: "bot-barrier-b", InstanceID: h.bot.InstanceID, StressSessionID: &h.session.ID,
		ExecutorNodeID: h.bot.ExecutorNodeID, Name: "load-002", Status: model.BotStatusConnected,
		DesiredStateGeneration: 3, CohortKey: "combat", Config: `{}`, Behavior: "idle",
	}
	require.NoError(t, h.db.Create(botB).Error)
	clock := &botLoadFakeClock{now: h.now}
	client := &fakeActionSignalClient{}
	var attempt atomic.Int32
	client.handler = func(_ context.Context, _ string, request *workerpb.SignalBotActionsRequest) (*workerpb.SignalBotActionsResponse, error) {
		current := attempt.Add(1)
		results := make([]*workerpb.SignalBotActionItemResult, 0, len(request.Signals))
		for index, signal := range request.Signals {
			result := &workerpb.SignalBotActionItemResult{SignalId: signal.SignalId, Accepted: true}
			if current == 1 && index == 1 {
				result.Accepted, result.ErrorCode, result.Error = false, "BUSY", "稍后重试"
			}
			results = append(results, result)
		}
		return &workerpb.SignalBotActionsResponse{Results: results}, nil
	}
	barriers := NewBarrierCoordinator(clock)
	router := NewActionSignalRouter(h.service, client, clock)
	events := NewScenarioActionEventService(h.service, barriers, router, fixedBarrierExpectedBots{h.bot.UUID: 3, botB.UUID: 3})
	t.Cleanup(events.Close)
	coordinator := NewBotFleetRuntimeCoordinator(NewBotFleetRuntimeService(h.db, clock), &botFleetFakeClient{}, nil)
	coordinator.SetActionEventHandler(events)
	scope := BarrierScope{RunID: h.session.UUID, StageIndex: 0, CohortKey: "combat", BarrierKey: "ready", Round: 1}

	first := barrierActionEvent(h.bot.UUID, h.session.UUID, "00000000-0000-0000-0000-000000000401", "token-a", h.now)
	second := barrierActionEvent(botB.UUID, h.session.UUID, "00000000-0000-0000-0000-000000000402", "token-b", h.now)
	_, err := coordinator.HandleEvent(context.Background(), h.node.ID, h.node.UUID, h.session.UUID, &workerpb.BotFleetEvent{Event: &workerpb.BotFleetEvent_ActionEvent{ActionEvent: first}})
	require.NoError(t, err)
	require.Zero(t, client.callCount(h.node.UUID))
	_, err = coordinator.HandleEvent(context.Background(), h.node.ID, h.node.UUID, h.session.UUID, &workerpb.BotFleetEvent{Event: &workerpb.BotFleetEvent_ActionEvent{ActionEvent: second}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		release := barriers.PendingRelease(scope)
		return attempt.Load() == 1 && release != nil && len(release.Pending) == 1
	}, time.Second, 10*time.Millisecond)
	releaseAt := barriers.PendingRelease(scope).ReleaseAtUnixMS

	report := events.RetryBarrierRelease(context.Background(), scope)
	require.Len(t, report.Items, 1)
	require.True(t, report.Items[0].Accepted)
	require.Empty(t, barriers.PendingRelease(scope).Pending)
	require.Equal(t, releaseAt, barriers.PendingRelease(scope).ReleaseAtUnixMS)
}

func TestScenarioActionEventService_RejectsInvalidBarrierBeforeWritingLedger(t *testing.T) {
	h := newBotActionResultHarness(t)
	setBarrierScenario(t, h, time.Minute, ScenarioBarrierRelease{Type: "all"}, "fail")
	clock := &botLoadFakeClock{now: h.now}
	barriers := NewBarrierCoordinator(clock)
	events := NewScenarioActionEventService(h.service, barriers, NewActionSignalRouter(h.service, &fakeActionSignalClient{}, clock), fixedBarrierExpectedBots{h.bot.UUID: 3})
	t.Cleanup(events.Close)
	event := barrierActionEvent(h.bot.UUID, h.session.UUID, "00000000-0000-0000-0000-000000000403", "token-a", h.now)
	event.ResultJson = fmt.Sprintf(`{"type":"barrier-arrived","stageIndex":0,"cohortKey":"combat","barrierKey":"ready","round":1,"release":{"type":"percent","value":101},"timeoutPolicy":"fail","deadlineUnixMs":%d}`, h.now.Add(time.Minute).UnixMilli())

	result, err := events.Ingest(context.Background(), h.node.ID, h.session.UUID, event)
	require.NoError(t, err)
	require.Equal(t, ActionResultIgnoredInvalid, result.Decision)
	var count int64
	require.NoError(t, h.db.Model(&model.BotLoadActionResult{}).Count(&count).Error)
	require.Zero(t, count)
}

func setBarrierScenario(t *testing.T, h *botActionResultHarness, timeout time.Duration, release ScenarioBarrierRelease, policy string) {
	t.Helper()
	timeoutMS := int(timeout / time.Millisecond)
	scenario := &ScenarioV2{
		Version:     2,
		Seed:        20260719,
		seedPresent: true,
		Cohorts: []ScenarioCohort{{
			Key: "combat", Percent: 100,
			Steps: []ScenarioAction{
				{Barrier: &BarrierAction{
					ScenarioActionBase: ScenarioActionBase{ID: "barrier-step", ActionType: ScenarioActionBarrier, TimeoutMS: &timeoutMS},
					Key:                "ready", Release: release, TimeoutPolicy: policy,
				}},
				{RoamInArea: &RoamInAreaAction{
					ScenarioActionBase: observedScenarioActionBase("observe", ScenarioActionRoamInArea),
					DurationMS:         1000,
					Area:               ScenarioArea{Type: "radius", Center: ScenarioPosition{}, Radius: 2},
				}},
			},
		}},
	}
	snapshot, err := CanonicalScenarioSnapshot(scenario, false)
	require.NoError(t, err)
	require.NoError(t, h.db.Model(&model.BotStressSession{}).Where("id = ?", h.session.ID).Update("scenario_snapshot", snapshot).Error)
	h.session.ScenarioSnapshot = snapshot
}

func authoritativeBarrierPayload(now time.Time, timeout time.Duration, release ScenarioBarrierRelease, policy string) barrierArrivedPayload {
	return barrierArrivedPayload{
		Type: barrierArrivedEventType, StageIndex: 0, CohortKey: "combat", BarrierKey: "ready", Round: 1,
		Release: release, TimeoutPolicy: policy, DeadlineUnixMS: now.Add(timeout).UnixMilli(),
	}
}

func mustBarrierPayload(t *testing.T, payload barrierArrivedPayload) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	return string(raw)
}

func acceptActionSignals(_ context.Context, _ string, request *workerpb.SignalBotActionsRequest) (*workerpb.SignalBotActionsResponse, error) {
	results := make([]*workerpb.SignalBotActionItemResult, 0, len(request.Signals))
	for _, signal := range request.Signals {
		results = append(results, &workerpb.SignalBotActionItemResult{SignalId: signal.SignalId, Accepted: true})
	}
	return &workerpb.SignalBotActionsResponse{Results: results}, nil
}

func barrierActionEvent(botUUID, runID, actionRunID, token string, now time.Time) *workerpb.BotActionEvent {
	return &workerpb.BotActionEvent{
		BotUuid: botUUID, SessionUuid: runID, Generation: 3, ActionRunId: actionRunID,
		StepId: "barrier-step", Attempt: 1, Status: "running", CorrelationToken: token,
		ResultJson:       mustBarrierPayloadNoTest(authoritativeBarrierPayload(now, time.Minute, ScenarioBarrierRelease{Type: "all"}, "fail")),
		ObservedAtUnixMs: now.UnixMilli(),
	}
}

func mustBarrierPayloadNoTest(payload barrierArrivedPayload) string {
	raw, _ := json.Marshal(payload)
	return string(raw)
}
