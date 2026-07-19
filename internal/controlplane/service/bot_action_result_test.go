package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

var botActionResultDBSequence atomic.Uint64

const (
	testActionCorrelationToken      = "00000000-0000-4000-8000-000000000352"
	testOtherActionCorrelationToken = "00000000-0000-4000-8000-000000000353"
)

type botActionResultHarness struct {
	db      *gorm.DB
	service *ActionResultService
	node    *model.Node
	session *model.BotStressSession
	bot     *model.Bot
	now     time.Time
}

func newBotActionResultHarness(t *testing.T) *botActionResultHarness {
	t.Helper()
	dsn := fmt.Sprintf("file:%s-%d?mode=memory&cache=shared", t.Name(), botActionResultDBSequence.Add(1))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Node{}, &model.Instance{}, &model.BotStressSession{},
		&model.BotLoadBatch{}, &model.Bot{}, &model.BotLoadActionResult{},
	))
	node := &model.Node{UUID: "node-action", Name: "动作节点", Host: "127.0.0.1", Secret: "secret"}
	require.NoError(t, db.Create(node).Error)
	instance := &model.Instance{NodeID: node.ID, UUID: "instance-action", Name: "动作实例", WorkDir: t.TempDir(), Status: model.InstanceStatusRunning}
	require.NoError(t, db.Create(instance).Error)
	session := &model.BotStressSession{UUID: "run-action", InstanceID: instance.ID, Name: "动作运行", NamePrefix: "load", BotCount: 1}
	require.NoError(t, db.Create(session).Error)
	executorNodeID := node.ID
	bot := &model.Bot{
		UUID: "bot-action", InstanceID: instance.ID, StressSessionID: &session.ID,
		ExecutorNodeID: &executorNodeID, Name: "load-001", Status: model.BotStatusConnected,
		DesiredStateGeneration: 3, CohortKey: "combat", Config: `{}`, Behavior: "idle",
	}
	require.NoError(t, db.Create(bot).Error)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	return &botActionResultHarness{db: db, service: NewActionResultService(db, botFleetTestClock{now: now}), node: node, session: session, bot: bot, now: now}
}

func (h *botActionResultHarness) event(status string) *workerpb.BotActionEvent {
	return &workerpb.BotActionEvent{
		BotUuid: h.bot.UUID, SessionUuid: h.session.UUID, Generation: h.bot.DesiredStateGeneration,
		ActionRunId: "00000000-0000-0000-0000-000000000352", StepId: "wait-room", Attempt: 1,
		Status: status, CorrelationToken: testActionCorrelationToken, ObservedAtUnixMs: h.now.UnixMilli(),
	}
}

func (h *botActionResultHarness) reload(t *testing.T) model.BotLoadActionResult {
	t.Helper()
	var result model.BotLoadActionResult
	require.NoError(t, h.db.Where("action_run_id = ?", "00000000-0000-0000-0000-000000000352").First(&result).Error)
	return result
}

func TestActionResultService_StartUpsertsRunningWithoutDuplicating(t *testing.T) {
	h := newBotActionResultHarness(t)

	first, err := h.service.Ingest(context.Background(), h.node.ID, h.session.UUID, h.event("running"))
	require.NoError(t, err)
	second, err := h.service.Ingest(context.Background(), h.node.ID, h.session.UUID, h.event("running"))
	require.NoError(t, err)
	require.Equal(t, ActionResultApplied, first.Decision)
	require.Equal(t, ActionResultIgnoredDuplicate, second.Decision)

	stored := h.reload(t)
	require.Equal(t, model.BotLoadActionRunning, stored.Status)
	require.Equal(t, h.session.ID, stored.StressSessionID)
	require.Equal(t, h.bot.ID, stored.BotID)
	require.Equal(t, "combat", stored.CohortKey)
	require.Equal(t, "wait-room", stored.StepID)
	require.Equal(t, testActionCorrelationToken, stored.CorrelationToken)
	require.Equal(t, h.now, stored.StartedAt)
	var count int64
	require.NoError(t, h.db.Model(&model.BotLoadActionResult{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestActionResultService_FirstTerminalWinsAndLateEventsAreIdempotent(t *testing.T) {
	h := newBotActionResultHarness(t)
	require.NoError(t, ingestAction(t, h, h.event("running")))

	failed := h.event("failed")
	failed.ErrorCode = ActionErrorTargetNotFound
	failed.Message = "未找到目标"
	failed.DurationMs = 1500
	failed.ResultJson = `{"attempts":3}`
	failed.ObservedAtUnixMs = h.now.Add(1500 * time.Millisecond).UnixMilli()
	first, err := h.service.Ingest(context.Background(), h.node.ID, h.session.UUID, failed)
	require.NoError(t, err)
	require.Equal(t, ActionResultApplied, first.Decision)

	late := h.event("timed_out")
	late.ErrorCode = ActionErrorProbeEventTimeout
	late.Message = "迟到终态"
	late.ObservedAtUnixMs = h.now.Add(3 * time.Second).UnixMilli()
	duplicate, err := h.service.Ingest(context.Background(), h.node.ID, h.session.UUID, late)
	require.NoError(t, err)
	require.Equal(t, ActionResultIgnoredTerminal, duplicate.Decision)
	startLate, err := h.service.Ingest(context.Background(), h.node.ID, h.session.UUID, h.event("running"))
	require.NoError(t, err)
	require.Equal(t, ActionResultIgnoredTerminal, startLate.Decision)

	stored := h.reload(t)
	require.Equal(t, model.BotLoadActionFailed, stored.Status)
	require.Equal(t, ActionErrorTargetNotFound, stored.ErrorCode)
	require.Equal(t, "未找到目标", stored.Message)
	require.Equal(t, int64(1500), stored.DurationMS)
	require.Equal(t, `{"attempts":3}`, stored.ResultJSON)
	require.NotNil(t, stored.EndedAt)
	require.Equal(t, h.now.Add(1500*time.Millisecond), *stored.EndedAt)
}

func TestActionResultService_AcceptsAttackAssertionUnmet(t *testing.T) {
	h := newBotActionResultHarness(t)
	event := h.event("failed")
	event.ErrorCode = ActionErrorAttackAssertionUnmet
	event.Message = "可信攻击条件未满足"

	result, err := h.service.Ingest(context.Background(), h.node.ID, h.session.UUID, event)
	require.NoError(t, err)
	require.Equal(t, ActionResultApplied, result.Decision)
	require.Equal(t, ActionErrorAttackAssertionUnmet, h.reload(t).ErrorCode)
}

func TestActionResultService_TruncatesResultJSONWithRecognizableMetadata(t *testing.T) {
	h := newBotActionResultHarness(t)
	payload, err := json.Marshal(map[string]string{"blob": strings.Repeat("x", actionResultJSONLimit*2)})
	require.NoError(t, err)
	event := h.event("succeeded")
	event.ResultJson = string(payload)

	result, err := h.service.Ingest(context.Background(), h.node.ID, h.session.UUID, event)
	require.NoError(t, err)
	require.Equal(t, ActionResultApplied, result.Decision)
	stored := h.reload(t)
	require.LessOrEqual(t, len([]byte(stored.ResultJSON)), actionResultJSONLimit)
	var metadata struct {
		Truncated     bool   `json:"truncated"`
		OriginalBytes int    `json:"originalBytes"`
		Preview       string `json:"preview"`
	}
	require.NoError(t, json.Unmarshal([]byte(stored.ResultJSON), &metadata))
	require.True(t, metadata.Truncated)
	require.Equal(t, len(payload), metadata.OriginalBytes)
	require.NotEmpty(t, metadata.Preview)
}

func TestActionResultService_RejectsMissingOrInvalidCorrelationTokenBeforeStartOrFinish(t *testing.T) {
	for _, status := range []string{"running", "succeeded"} {
		for _, tokenCase := range []struct {
			name  string
			value string
		}{{name: "空值"}, {name: "非法UUID", value: "not-a-uuid"}} {
			t.Run(status+"/"+tokenCase.name, func(t *testing.T) {
				h := newBotActionResultHarness(t)
				event := h.event(status)
				event.CorrelationToken = tokenCase.value

				result, err := h.service.Ingest(context.Background(), h.node.ID, h.session.UUID, event)
				require.NoError(t, err)
				require.Equal(t, ActionResultIgnoredInvalid, result.Decision)
				require.Contains(t, result.Diagnostic, "correlationToken")
				var count int64
				require.NoError(t, h.db.Model(&model.BotLoadActionResult{}).Count(&count).Error)
				require.Zero(t, count)
			})
		}
	}
}

func TestActionResultService_RejectsInvalidOrMismatchedEventsWithoutLedgerPollution(t *testing.T) {
	h := newBotActionResultHarness(t)
	tests := []*workerpb.BotActionEvent{
		nil,
		h.event("unknown"),
		h.event("failed"),
		h.event("running"),
	}
	tests[2].ErrorCode = "NOT_FROZEN"
	tests[3].Generation++
	for _, event := range tests {
		result, err := h.service.Ingest(context.Background(), h.node.ID, h.session.UUID, event)
		require.NoError(t, err)
		require.NotEqual(t, ActionResultApplied, result.Decision)
	}
	var count int64
	require.NoError(t, h.db.Model(&model.BotLoadActionResult{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestBotFleetRuntimeCoordinator_HandsActionEventToResultService(t *testing.T) {
	h := newBotActionResultHarness(t)
	coordinator := NewBotFleetRuntimeCoordinator(NewBotFleetRuntimeService(h.db, botFleetTestClock{now: h.now}), &botFleetFakeClient{}, nil)
	coordinator.SetActionEventHandler(h.service)
	event := &workerpb.BotFleetEvent{Event: &workerpb.BotFleetEvent_ActionEvent{ActionEvent: h.event("running")}}

	result, err := coordinator.HandleEvent(context.Background(), h.node.ID, h.node.UUID, h.session.UUID, event)
	require.NoError(t, err)
	require.Equal(t, BotFleetRuntimeActionApplied, result.Decision)
	require.Equal(t, model.BotLoadActionRunning, h.reload(t).Status)
}

func TestActionResultService_TerminalIdentityConflictsDoNotMutateRunningAction(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *botActionResultHarness, *workerpb.BotActionEvent) string
	}{
		{name: "步骤冲突", mutate: func(_ *testing.T, _ *botActionResultHarness, event *workerpb.BotActionEvent) string {
			event.StepId = "other-step"
			return event.SessionUuid
		}},
		{name: "尝试次数冲突", mutate: func(_ *testing.T, _ *botActionResultHarness, event *workerpb.BotActionEvent) string {
			event.Attempt = 2
			return event.SessionUuid
		}},
		{name: "关联令牌冲突", mutate: func(_ *testing.T, _ *botActionResultHarness, event *workerpb.BotActionEvent) string {
			event.CorrelationToken = testOtherActionCorrelationToken
			return event.SessionUuid
		}},
		{name: "Bot 冲突", mutate: func(t *testing.T, h *botActionResultHarness, event *workerpb.BotActionEvent) string {
			other := createActionResultBot(t, h, h.session, "bot-other", "combat")
			event.BotUuid = other.UUID
			return event.SessionUuid
		}},
		{name: "运行冲突", mutate: func(t *testing.T, h *botActionResultHarness, event *workerpb.BotActionEvent) string {
			otherSession := &model.BotStressSession{UUID: "run-other", InstanceID: h.bot.InstanceID, Name: "其他运行", NamePrefix: "other", BotCount: 1}
			require.NoError(t, h.db.Create(otherSession).Error)
			other := createActionResultBot(t, h, otherSession, "bot-other-run", "combat")
			event.BotUuid, event.SessionUuid = other.UUID, otherSession.UUID
			return otherSession.UUID
		}},
		{name: "分组冲突", mutate: func(t *testing.T, h *botActionResultHarness, event *workerpb.BotActionEvent) string {
			require.NoError(t, h.db.Model(h.bot).Update("cohort_key", "lobby").Error)
			h.bot.CohortKey = "lobby"
			return event.SessionUuid
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newBotActionResultHarness(t)
			require.NoError(t, ingestAction(t, h, h.event("running")))
			terminal := h.event("failed")
			terminal.ErrorCode = ActionErrorTargetNotFound
			expectedSession := test.mutate(t, h, terminal)

			result, err := h.service.Ingest(context.Background(), h.node.ID, expectedSession, terminal)
			require.NoError(t, err)
			require.Equal(t, ActionResultIgnoredIdentity, result.Decision)
			stored := h.reload(t)
			require.Equal(t, model.BotLoadActionRunning, stored.Status)
			require.Equal(t, h.session.ID, stored.StressSessionID)
			require.Equal(t, "wait-room", stored.StepID)
			require.Equal(t, 1, stored.Attempt)
			require.Equal(t, testActionCorrelationToken, stored.CorrelationToken)
		})
	}
}

func createActionResultBot(t *testing.T, h *botActionResultHarness, session *model.BotStressSession, botUUID, cohort string) *model.Bot {
	t.Helper()
	bot := &model.Bot{
		UUID: botUUID, InstanceID: h.bot.InstanceID, StressSessionID: &session.ID,
		ExecutorNodeID: h.bot.ExecutorNodeID, Name: botUUID, Status: model.BotStatusConnected,
		DesiredStateGeneration: h.bot.DesiredStateGeneration, CohortKey: cohort, Config: `{}`, Behavior: "idle",
	}
	require.NoError(t, h.db.Create(bot).Error)
	return bot
}

func ingestAction(t *testing.T, h *botActionResultHarness, event *workerpb.BotActionEvent) error {
	t.Helper()
	_, err := h.service.Ingest(context.Background(), h.node.ID, h.session.UUID, event)
	return err
}
