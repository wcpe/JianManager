package service

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// setupAgentBotScopeDB 建立含 Bot/压测会话/批次的内存库，供 bot 与 botrun 授权矩阵使用。
func setupAgentBotScopeDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.AgentToken{}, &model.Node{}, &model.Instance{},
		&model.Bot{}, &model.BotStressSession{}, &model.BotLoadBatch{},
	))
	return db
}

// botScopeFixture 是授权矩阵共用的地形：两个节点、各一实例，会话目标为 A 实例。
type botScopeFixture struct {
	nodeA, nodeB *model.Node
	instA, instB *model.Instance
	bot          *model.Bot
	session      *model.BotStressSession
}

func seedBotScopeFixture(t *testing.T, db *gorm.DB) botScopeFixture {
	t.Helper()
	f := botScopeFixture{}
	f.nodeA = &model.Node{Name: "exec-a", Host: "127.0.0.1", Secret: "sa", Status: model.NodeStatusOnline}
	f.nodeB = &model.Node{Name: "exec-b", Host: "127.0.0.1", Secret: "sb", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(f.nodeA).Error)
	require.NoError(t, db.Create(f.nodeB).Error)
	f.instA = &model.Instance{
		NodeID: f.nodeA.ID, Name: "target-a", Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDirect, WorkDir: "wa", StartCommand: "java",
	}
	f.instB = &model.Instance{
		NodeID: f.nodeB.ID, Name: "target-b", Type: model.InstanceTypeMinecraftJava,
		ProcessType: model.ProcessTypeDirect, WorkDir: "wb", StartCommand: "java",
	}
	require.NoError(t, db.Create(f.instA).Error)
	require.NoError(t, db.Create(f.instB).Error)
	f.bot = &model.Bot{InstanceID: f.instA.ID, Name: "bot-1", Status: model.BotStatusPending}
	require.NoError(t, db.Create(f.bot).Error)
	f.session = &model.BotStressSession{
		InstanceID: f.instA.ID, Name: "run-1", NamePrefix: "load",
		BotCount: 10, Status: model.BotStressSessionPending,
	}
	require.NoError(t, db.Create(f.session).Error)
	return f
}

// addExecutorBatch 为会话追加一个执行节点批次，用于构造 executor 集合。
func addExecutorBatch(t *testing.T, db *gorm.DB, sessionID, nodeID uint, ordinal int) {
	t.Helper()
	batch := &model.BotLoadBatch{
		StressSessionID: sessionID, ExecutorNodeID: nodeID, Ordinal: ordinal,
		PlannedCount: 5, State: model.BotLoadBatchPlanned,
		IdempotencyKey: fmt.Sprintf("test-batch-%d-%d-%d", sessionID, nodeID, ordinal),
	}
	require.NoError(t, db.Create(batch).Error)
}

func TestAuthorizeBotAction_InstanceScopeMatrix(t *testing.T) {
	db := setupAgentBotScopeDB(t)
	svc := NewAgentTokenService(db)
	f := seedBotScopeFixture(t, db)

	tests := []struct {
		name      string
		principal *AgentPrincipal
		wantErr   bool
	}{
		{
			name: "V2 显式实例 scope 放行",
			principal: &AgentPrincipal{
				PolicyVersion: AgentPolicyVersionV2, ScopedInstanceIDs: []uint{f.instA.ID},
				Capabilities: []string{AgentCapabilityBotRead},
			},
		},
		{
			name: "V2 节点 scope 继承实例放行",
			principal: &AgentPrincipal{
				PolicyVersion: AgentPolicyVersionV2, ScopedNodeIDs: []uint{f.nodeA.ID},
				Capabilities: []string{AgentCapabilityBotRead},
			},
		},
		{
			name: "V2 缺 bot.read 能力拒绝",
			principal: &AgentPrincipal{
				PolicyVersion: AgentPolicyVersionV2, ScopedInstanceIDs: []uint{f.instA.ID},
				Capabilities: []string{AgentCapabilityInstanceRead},
			},
			wantErr: true,
		},
		{
			name: "V2 实例越界拒绝",
			principal: &AgentPrincipal{
				PolicyVersion: AgentPolicyVersionV2, ScopedInstanceIDs: []uint{f.instB.ID},
				Capabilities: []string{AgentCapabilityBotRead},
			},
			wantErr: true,
		},
		{
			name: "V1 Token 不可见 bot 域",
			principal: &AgentPrincipal{
				PolicyVersion: AgentPolicyVersionV1, ScopedInstanceIDs: []uint{f.instA.ID},
				WriteAllowlist: []string{AgentWriteInstanceLife},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth, bot, err := svc.AuthorizeBotAction(tt.principal, AgentActionBotGet, f.bot.ID)
			if tt.wantErr {
				assert.ErrorIs(t, err, ErrAgentForbidden)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, bot)
			assert.Equal(t, f.bot.ID, bot.ID)
			assert.Equal(t, AgentCapabilityBotRead, auth.Capability)
		})
	}
}

func TestAuthorizeBotAction_MissingBotConvergesToForbidden(t *testing.T) {
	db := setupAgentBotScopeDB(t)
	svc := NewAgentTokenService(db)
	f := seedBotScopeFixture(t, db)

	p := &AgentPrincipal{
		PolicyVersion: AgentPolicyVersionV2, ScopedInstanceIDs: []uint{f.instA.ID},
		Capabilities: []string{AgentCapabilityBotRead},
	}
	// 不存在的 Bot 不泄露存在性，统一收敛为拒绝。
	_, _, err := svc.AuthorizeBotAction(p, AgentActionBotGet, 999999)
	assert.ErrorIs(t, err, ErrAgentForbidden)
}

func TestAuthorizeBotRunAction_ExecutorNodesFullCoverage(t *testing.T) {
	db := setupAgentBotScopeDB(t)
	svc := NewAgentTokenService(db)
	f := seedBotScopeFixture(t, db)
	addExecutorBatch(t, db, f.session.ID, f.nodeA.ID, 0)
	addExecutorBatch(t, db, f.session.ID, f.nodeB.ID, 1)

	// 同时持有两个执行节点 scope + 目标实例 scope → 无越界节点。
	p := &AgentPrincipal{
		PolicyVersion:     AgentPolicyVersionV2,
		ScopedInstanceIDs: []uint{f.instA.ID},
		ScopedNodeIDs:     []uint{f.nodeA.ID, f.nodeB.ID},
		Capabilities:      []string{AgentCapabilityBotLoad},
	}
	auth, session, outOfScope, err := svc.AuthorizeBotRunAction(p, AgentActionLoadTestRunStop, f.session.ID)
	require.NoError(t, err)
	require.NotNil(t, session)
	assert.Equal(t, f.session.ID, session.ID)
	assert.Equal(t, AgentCapabilityBotLoad, auth.Capability)
	assert.Empty(t, outOfScope, "全部 executor 节点在 scope 内时不应有越界节点")
}

func TestAuthorizeBotRunAction_PartialExecutorOutOfScope(t *testing.T) {
	db := setupAgentBotScopeDB(t)
	svc := NewAgentTokenService(db)
	f := seedBotScopeFixture(t, db)
	addExecutorBatch(t, db, f.session.ID, f.nodeA.ID, 0)
	addExecutorBatch(t, db, f.session.ID, f.nodeB.ID, 1)

	// 只持 nodeA scope：授权本身通过（供 stop 安全方向使用），但须报告 nodeB 越界。
	p := &AgentPrincipal{
		PolicyVersion:     AgentPolicyVersionV2,
		ScopedInstanceIDs: []uint{f.instA.ID},
		ScopedNodeIDs:     []uint{f.nodeA.ID},
		Capabilities:      []string{AgentCapabilityBotLoad},
	}
	_, session, outOfScope, err := svc.AuthorizeBotRunAction(p, AgentActionLoadTestRunStop, f.session.ID)
	require.NoError(t, err)
	require.NotNil(t, session)
	assert.Equal(t, []uint{f.nodeB.ID}, outOfScope)
}

func TestAuthorizeBotRunAction_TargetInstanceOutOfScopeRejected(t *testing.T) {
	db := setupAgentBotScopeDB(t)
	svc := NewAgentTokenService(db)
	f := seedBotScopeFixture(t, db)
	addExecutorBatch(t, db, f.session.ID, f.nodeA.ID, 0)

	// 目标实例不在 scope（只给了 instB），执行节点 scope 也不含目标实例所属的 nodeA，
	// 故 V2 节点继承同样不成立 → 整体拒绝。
	p := &AgentPrincipal{
		PolicyVersion:     AgentPolicyVersionV2,
		ScopedInstanceIDs: []uint{f.instB.ID},
		ScopedNodeIDs:     []uint{f.nodeB.ID},
		Capabilities:      []string{AgentCapabilityBotLoad},
	}
	_, _, _, err := svc.AuthorizeBotRunAction(p, AgentActionLoadTestRunStop, f.session.ID)
	assert.ErrorIs(t, err, ErrAgentForbidden)
}

func TestAuthorizeBotRunAction_V1NotDiscoverable(t *testing.T) {
	db := setupAgentBotScopeDB(t)
	svc := NewAgentTokenService(db)
	f := seedBotScopeFixture(t, db)
	addExecutorBatch(t, db, f.session.ID, f.nodeA.ID, 0)

	p := &AgentPrincipal{
		PolicyVersion:     AgentPolicyVersionV1,
		ScopedInstanceIDs: []uint{f.instA.ID},
		ScopedNodeIDs:     []uint{f.nodeA.ID},
		WriteAllowlist:    []string{AgentWriteInstanceLife, AgentWriteNodeMaintenance},
	}
	_, _, _, err := svc.AuthorizeBotRunAction(p, AgentActionLoadTestRunStop, f.session.ID)
	assert.ErrorIs(t, err, ErrAgentForbidden)
}

func TestAuthorizeBotRunExecutorNodes_StartRejectsAnyOutOfScope(t *testing.T) {
	db := setupAgentBotScopeDB(t)
	svc := NewAgentTokenService(db)
	f := seedBotScopeFixture(t, db)

	p := &AgentPrincipal{
		PolicyVersion:     AgentPolicyVersionV2,
		ScopedInstanceIDs: []uint{f.instA.ID},
		ScopedNodeIDs:     []uint{f.nodeA.ID},
		Capabilities:      []string{AgentCapabilityBotLoad},
	}
	// 启动方向：显式请求的 executor 集合中任一越界 → 整体拒绝，不静默缩减。
	err := svc.AuthorizeBotRunExecutorNodes(p, []uint{f.nodeA.ID, f.nodeB.ID})
	assert.ErrorIs(t, err, ErrAgentForbidden)

	require.NoError(t, svc.AuthorizeBotRunExecutorNodes(p, []uint{f.nodeA.ID}))
}

func TestAgentBotActionCatalog_V1DeniedAndCapabilitiesRegistered(t *testing.T) {
	wantCapabilities := map[string]string{
		AgentActionBotList:              AgentCapabilityBotRead,
		AgentActionBotGet:               AgentCapabilityBotRead,
		AgentActionBotCreate:            AgentCapabilityBotManage,
		AgentActionBotSetBehavior:       AgentCapabilityBotManage,
		AgentActionBotSendCommand:       AgentCapabilityBotManage,
		AgentActionBotDelete:            AgentCapabilityBotManage,
		AgentActionLoadTestTemplateList: AgentCapabilityBotRead,
		AgentActionLoadTestTemplateGet:  AgentCapabilityBotRead,
		AgentActionLoadTestTemplateCreate: AgentCapabilityBotLoad,
		AgentActionLoadTestTemplateUpdate: AgentCapabilityBotLoad,
		AgentActionLoadTestTemplateDelete: AgentCapabilityBotLoad,
		AgentActionLoadTestRunCreate:      AgentCapabilityBotLoad,
		AgentActionLoadTestRunList:        AgentCapabilityBotRead,
		AgentActionLoadTestRunGet:         AgentCapabilityBotRead,
		AgentActionLoadTestNodeCapacity:   AgentCapabilityBotRead,
		AgentActionLoadTestRunPreflight:   AgentCapabilityBotLoad,
		AgentActionLoadTestRunStart:       AgentCapabilityBotLoad,
		AgentActionLoadTestRunStop:        AgentCapabilityBotLoad,
		AgentActionLoadTestRunRetryFailed: AgentCapabilityBotLoad,
		AgentActionLoadTestRunBots:        AgentCapabilityBotRead,
		AgentActionLoadTestRunFailures:    AgentCapabilityBotRead,
		AgentActionLoadTestRunEvents:      AgentCapabilityBotRead,
		AgentActionLoadTestRunMetrics:     AgentCapabilityObservabilityRead,
		AgentActionLoadTestRunReport:      AgentCapabilityBotRead,
	}
	for action, wantCap := range wantCapabilities {
		d, ok := DescribeAgentAction(action)
		require.True(t, ok, "action 未登记: %s", action)
		assert.Equal(t, wantCap, d.V2Capability, "action %s 能力不符", action)
		assert.False(t, d.V1Allowed, "action %s 不得对 V1 Token 开放", action)
		assert.False(t, d.HTTPInContract, "action %s 不进 HTTP 契约投影", action)
	}
}

func TestPrincipalHasPotentialScope_BotResources(t *testing.T) {
	// V2 仅有节点 scope 时，bot/botrun 复用实例分支即可被发现。
	v2Node := &AgentPrincipal{
		PolicyVersion: AgentPolicyVersionV2, ScopedNodeIDs: []uint{1},
		Capabilities: []string{AgentCapabilityBotRead},
	}
	assert.True(t, principalHasPotentialScope(v2Node, AgentResourceBot))
	assert.True(t, principalHasPotentialScope(v2Node, AgentResourceBotRun))

	// V1 无实例 scope 时不可发现。
	v1NodeOnly := &AgentPrincipal{PolicyVersion: AgentPolicyVersionV1, ScopedNodeIDs: []uint{1}}
	assert.False(t, principalHasPotentialScope(v1NodeOnly, AgentResourceBot))
	assert.False(t, principalHasPotentialScope(v1NodeOnly, AgentResourceBotRun))
}
