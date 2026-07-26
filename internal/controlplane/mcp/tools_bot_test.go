package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// stubBotLoadExecutor 记录 start 收到的 planToken，用于断言 MCP 原样透传不解析。
type stubBotLoadExecutor struct {
	onStart func(ctx context.Context, sessionID uint, planToken string) error
}

func (s *stubBotLoadExecutor) Start(ctx context.Context, sessionID uint, planToken string) (*model.BotStressSession, error) {
	if s.onStart != nil {
		if err := s.onStart(ctx, sessionID, planToken); err != nil {
			return nil, err
		}
	}
	return &model.BotStressSession{ID: sessionID}, nil
}

func (s *stubBotLoadExecutor) Stop(_ context.Context, sessionID uint, _ ...string) (*model.BotStressSession, error) {
	return &model.BotStressSession{ID: sessionID}, nil
}

func (s *stubBotLoadExecutor) RetryFailed(_ context.Context, _ uint, _ service.BotLoadRetryRequest) (*service.BotLoadRetryResult, error) {
	return &service.BotLoadRetryResult{Errors: []service.BotLoadRetryItemError{}}, nil
}

// newBotToolDB 建立 Bot/压测工具契约测试所需的内存库。
func newBotToolDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Node{}, &model.Instance{}, &model.Bot{},
		&model.BotStressSession{}, &model.BotLoadBatch{}, &model.BotLoadTemplate{},
	))
	return db
}

type botToolFixture struct {
	node     *model.Node
	instance *model.Instance
	bot      *model.Bot
	session  *model.BotStressSession
}

func seedBotToolFixture(t *testing.T, db *gorm.DB) botToolFixture {
	t.Helper()
	f := botToolFixture{}
	f.node = &model.Node{Name: "n1", Host: "127.0.0.1", Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(f.node).Error)
	f.instance = &model.Instance{
		NodeID: f.node.ID, Name: "inst", Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDirect, WorkDir: "w", StartCommand: "java",
	}
	require.NoError(t, db.Create(f.instance).Error)
	f.bot = &model.Bot{InstanceID: f.instance.ID, Name: "bot-alpha", Status: model.BotStatusPending}
	require.NoError(t, db.Create(f.bot).Error)
	f.session = &model.BotStressSession{
		InstanceID: f.instance.ID, Name: "run", NamePrefix: "load",
		BotCount: 5, Status: model.BotStressSessionPending,
	}
	require.NoError(t, db.Create(f.session).Error)
	return f
}

// botLoadPrincipal 构造持全部 Bot 域能力的 V2 主体。
func botLoadPrincipal(f botToolFixture) *service.AgentPrincipal {
	return &service.AgentPrincipal{
		TokenID: 1, Name: "bot-agent", PolicyVersion: service.AgentPolicyVersionV2,
		ScopedInstanceIDs: []uint{f.instance.ID},
		ScopedNodeIDs:     []uint{f.node.ID},
		Capabilities: []string{
			service.AgentCapabilityBotRead, service.AgentCapabilityBotManage,
			service.AgentCapabilityBotLoad, service.AgentCapabilityObservabilityRead,
		},
	}
}

func TestRegisteredTools_IncludesBotAndLoadTestDomain(t *testing.T) {
	names := make(map[string]bool)
	for _, tl := range RegisteredTools() {
		names[tl.Name] = true
	}
	want := []string{
		"bot_list", "bot_get", "bot_create", "bot_set_behavior", "bot_send_command", "bot_delete",
		"loadtest_template_list", "loadtest_template_get", "loadtest_template_create",
		"loadtest_template_update", "loadtest_template_delete",
		"loadtest_run_create", "loadtest_run_list", "loadtest_run_get", "loadtest_node_capacity",
		"loadtest_run_preflight", "loadtest_run_start", "loadtest_run_stop", "loadtest_run_retry_failed",
		"loadtest_run_bots", "loadtest_run_failures", "loadtest_run_events",
		"loadtest_run_metrics", "loadtest_run_report",
	}
	for _, n := range want {
		assert.True(t, names[n], "应注册 %s", n)
	}
}

func TestToolsForPrincipal_BotCapabilityMatrix(t *testing.T) {
	inst := []uint{7}
	tests := []struct {
		name       string
		caps       []string
		visible    []string
		notVisible []string
	}{
		{
			name:       "仅 bot.read 只见读工具",
			caps:       []string{service.AgentCapabilityBotRead},
			visible:    []string{"bot_list", "bot_get", "loadtest_run_list", "loadtest_run_report"},
			notVisible: []string{"bot_create", "bot_delete", "loadtest_run_start", "loadtest_template_create"},
		},
		{
			name:       "bot.manage 开放普通 Bot 写",
			caps:       []string{service.AgentCapabilityBotManage},
			visible:    []string{"bot_create", "bot_set_behavior", "bot_send_command", "bot_delete"},
			notVisible: []string{"loadtest_run_start", "loadtest_template_create"},
		},
		{
			name:       "bot.load 开放压测编排",
			caps:       []string{service.AgentCapabilityBotLoad},
			visible:    []string{"loadtest_run_preflight", "loadtest_run_start", "loadtest_run_stop", "loadtest_template_create"},
			notVisible: []string{"bot_create", "bot_list"},
		},
		{
			name:       "observability.read 才见运行指标",
			caps:       []string{service.AgentCapabilityObservabilityRead},
			visible:    []string{"loadtest_run_metrics"},
			notVisible: []string{"loadtest_run_bots", "loadtest_run_events"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &service.AgentPrincipal{
				PolicyVersion: service.AgentPolicyVersionV2,
				ScopedInstanceIDs: inst, Capabilities: tt.caps,
			}
			names := make(map[string]bool)
			for _, tl := range ToolsForPrincipal(p) {
				names[tl.Name] = true
			}
			for _, n := range tt.visible {
				assert.True(t, names[n], "%s 应可见", n)
			}
			for _, n := range tt.notVisible {
				assert.False(t, names[n], "%s 不应可见", n)
			}
		})
	}
}

func TestToolsForPrincipal_V1CannotSeeBotDomain(t *testing.T) {
	p := &service.AgentPrincipal{
		PolicyVersion:     service.AgentPolicyVersionV1,
		ScopedInstanceIDs: []uint{1}, ScopedNodeIDs: []uint{1},
		WriteAllowlist: []string{service.AgentWriteInstanceLife, service.AgentWriteNodeMaintenance},
	}
	names := make(map[string]bool)
	for _, tl := range ToolsForPrincipal(p) {
		names[tl.Name] = true
	}
	forbidden := []string{
		"bot_list", "bot_create", "bot_delete", "loadtest_run_start",
		"loadtest_template_create", "loadtest_run_report",
	}
	for _, n := range forbidden {
		assert.False(t, names[n], "V1 Token 不得看到 %s", n)
	}
}

func TestCallTool_BotSendCommand_ADR075Wording(t *testing.T) {
	db := newBotToolDB(t)
	f := seedBotToolFixture(t, db)
	deps := ToolDeps{
		Agent: service.NewAgentTokenService(db),
		Bot:   service.NewBotService(db, cpgrpc.NewClientPool()),
	}
	res := CallTool(context.Background(), deps, botLoadPrincipal(f), "bot_send_command",
		map[string]any{"id": float64(f.bot.ID), "command": "/list"})

	// 无论成功失败，措辞都不得宣称服务器已接受或业务已生效（ADR-075）。
	text := res.Content[0].Text
	assert.NotContains(t, text, "服务器已接受")
	assert.NotContains(t, text, "业务已生效")
	assert.NotContains(t, text, "执行成功")

	// 工具描述同样必须写明发送边界，防 Agent 误报。
	var desc string
	for _, tl := range RegisteredTools() {
		if tl.Name == "bot_send_command" {
			desc = tl.Description
		}
	}
	assert.Contains(t, desc, "bot.chat")
	assert.Contains(t, desc, "不代表服务器已接受或业务已生效")
}

func TestCallTool_BotSendCommand_SuccessTextIsSendOnly(t *testing.T) {
	// 该用例断言成功路径文案：只能说「已发送（bot.chat 调用成功）」。
	assert.Equal(t, "已发送（bot.chat 调用成功）；不代表服务器已接受或业务已生效",
		botSendCommandSuccessText())
}

func TestCallTool_BotDelete_RequiresExactConfirm(t *testing.T) {
	db := newBotToolDB(t)
	f := seedBotToolFixture(t, db)
	deps := ToolDeps{
		Agent: service.NewAgentTokenService(db),
		Bot:   service.NewBotService(db, cpgrpc.NewClientPool()),
	}
	p := botLoadPrincipal(f)

	// 缺确认参数 → 拒绝且 Bot 仍在。
	res := CallTool(context.Background(), deps, p, "bot_delete", map[string]any{"id": float64(f.bot.ID)})
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "confirmBotName")

	// 确认串不匹配 → 拒绝。
	res = CallTool(context.Background(), deps, p, "bot_delete",
		map[string]any{"id": float64(f.bot.ID), "confirmBotName": "wrong-name"})
	assert.True(t, res.IsError)

	var remaining int64
	require.NoError(t, db.Model(&model.Bot{}).Count(&remaining).Error)
	assert.Equal(t, int64(1), remaining, "确认失败不得删除 Bot")
}

func TestCallTool_BotScopeDeniedOutsideInstance(t *testing.T) {
	db := newBotToolDB(t)
	f := seedBotToolFixture(t, db)
	deps := ToolDeps{
		Agent: service.NewAgentTokenService(db),
		Bot:   service.NewBotService(db, cpgrpc.NewClientPool()),
	}
	// scope 指向另一个实例 ID → Bot 不可见。
	p := &service.AgentPrincipal{
		PolicyVersion: service.AgentPolicyVersionV2, ScopedInstanceIDs: []uint{f.instance.ID + 999},
		Capabilities: []string{service.AgentCapabilityBotRead},
	}
	res := CallTool(context.Background(), deps, p, "bot_get", map[string]any{"id": float64(f.bot.ID)})
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "拒绝")
}

func TestCallTool_LoadTestRunStart_PlanTokenIsOpaquePassThrough(t *testing.T) {
	db := newBotToolDB(t)
	f := seedBotToolFixture(t, db)
	captured := ""
	deps := ToolDeps{
		Agent: service.NewAgentTokenService(db),
		Execution: &stubBotLoadExecutor{onStart: func(_ context.Context, _ uint, planToken string) error {
			captured = planToken
			return nil
		}},
		StressSession: service.NewBotStressSessionService(db, service.NewBotService(db, cpgrpc.NewClientPool())),
	}
	const opaque = "eyJ2IjoxfQ.c2lnbmF0dXJl"
	CallTool(context.Background(), deps, botLoadPrincipal(f), "loadtest_run_start",
		map[string]any{"id": float64(f.session.ID), "planToken": opaque})

	assert.Equal(t, opaque, captured, "planToken 必须原样透传，不解析不重签")
}

func TestCallTool_ProjectionPageSizeClampedTo100(t *testing.T) {
	db := newBotToolDB(t)
	f := seedBotToolFixture(t, db)
	deps := ToolDeps{
		Agent:      service.NewAgentTokenService(db),
		Projection: service.NewBotLoadProjectionService(db),
	}
	res := CallTool(context.Background(), deps, botLoadPrincipal(f), "loadtest_run_bots",
		map[string]any{"id": float64(f.session.ID), "pageSize": float64(5000)})
	require.False(t, res.IsError, res.Content[0].Text)

	var payload struct {
		PageSize int `json:"pageSize"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].Text), &payload))
	assert.Equal(t, 100, payload.PageSize, "分页上限必须收敛到 100")
}

func TestCallTool_LoadTestTemplateDelete_RequiresExactConfirm(t *testing.T) {
	db := newBotToolDB(t)
	f := seedBotToolFixture(t, db)
	tplSvc := service.NewBotLoadTemplateService(db)
	tpl, err := tplSvc.Create(0, service.BotLoadTemplateInput{
		Name:            "tpl-a",
		CommandSchedule: json.RawMessage(`{"commands":[{"id":"c1","atMs":0,"command":"/say hi"}],"durationMs":1000,"jitterMs":0}`),
		LoadProfile:     json.RawMessage(`{"type":"stable","targetBots":5,"rampUpSeconds":5,"durationSeconds":60}`),
		Thresholds:      json.RawMessage(`{"minOnlineRate":0.99,"minCommandSentRate":0.99,"minScheduleCompletionRate":0.99,"minWorkerHealthRate":0.99,"minBarrierArrivalRate":0.99,"maxScheduleLagP95Ms":1000,"maxProcessCrashes":0}`),
	})
	require.NoError(t, err)

	deps := ToolDeps{Agent: service.NewAgentTokenService(db), LoadTemplate: tplSvc}
	res := CallTool(context.Background(), deps, botLoadPrincipal(f), "loadtest_template_delete",
		map[string]any{"id": float64(tpl.ID)})
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "confirmTemplateName")

	var remaining int64
	require.NoError(t, db.Model(&model.BotLoadTemplate{}).Count(&remaining).Error)
	assert.Equal(t, int64(1), remaining, "确认失败不得删除模板")
}
