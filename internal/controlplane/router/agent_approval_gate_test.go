package router

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// 三入口同策略：agent 运维面 HTTP/CLI 入口的审批闸。
//
// ## 为什么需要这组用例
//
// 审批拦截原先只接在 MCP 的 CallTool 上，而 `POST /agent/instances/:id/stop`
// 直接调用领域方法。同一个 Agent Token 只要改用 curl/CLI 就能跳过人工审批——
// 等于 PRD 要求的三入口同策略只落实了一条。
//
// ## 这里**不**断言「出票后 503/403」这类间接表现
//
// 直接断言两件可证伪的事：① 响应 202 + requestId；② **实例状态没有被改**（零副作用）。
// 只断言状态码的话，一个「先停服再出票」的实现也能通过。

// newAgentApprovalTestEnv 装配一个只挂 agent 运维路由的最小环境。
//
// 不走 setupTestRouter：那套完整装配没有审批服务（会命中「未装配→失败关闭」分支），
// 且本组用例只关心审批闸本身，不需要几十个无关服务。
func newAgentApprovalTestEnv(t *testing.T) (db *gorm.DB, r *gin.Engine, ops *AgentOpsHandler, inst *model.Instance, p *service.AgentPrincipal) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db = setupTestDB(t)
	r = gin.New()

	node := &model.Node{Name: "approval-node", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)

	inst = &model.Instance{
		NodeID:       node.ID,
		Name:         "approval-gate-inst",
		Type:         model.InstanceTypeGeneric,
		ProcessType:  model.ProcessTypeDirect,
		StartCommand: "echo",
		Status:       model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(inst).Error)

	agentSvc := service.NewAgentTokenService(db)
	instanceSvc := service.NewInstanceService(db, service.NewGroupService(db), nil)
	instanceSvc.SetDelegateBypassForTest(true)
	nodeSvc := service.NewNodeService(db)

	ops = NewAgentOpsHandler(agentSvc, instanceSvc, nodeSvc, service.NewAuditService(db), service.NewAgentCallLogService(db))
	ops.SetApproval(service.NewApprovalService(db))

	p = &service.AgentPrincipal{
		TokenID:           1,
		Name:              "approval-agent",
		ScopedInstanceIDs: []uint{inst.ID},
		WriteAllowlist:    []string{service.AgentWriteInstanceLife},
	}

	// 以中间件注入 principal，替代真实 AgentAuth：本组用例测的是审批闸，
	// 不是 token 解析（那由 agent_gate_test.go 覆盖）。
	group := r.Group("/api/v1")
	group.Use(func(c *gin.Context) {
		c.Set(middleware.CtxAgentPrincipal, p)
		c.Next()
	})
	ops.RegisterOpsRoutes(group)
	return db, r, ops, inst, p
}

// TestAgentOps_StopRequiresApprovalAndHasNoSideEffect stop 出票、实例状态不变。
func TestAgentOps_StopRequiresApprovalAndHasNoSideEffect(t *testing.T) {
	db, r, _, inst, _ := newAgentApprovalTestEnv(t)

	w := makeRequest(r, http.MethodPost, "/api/v1/agent/instances/"+itoa(inst.ID)+"/stop", nil, "")
	require.Equal(t, http.StatusAccepted, w.Code, "需审操作应返回 202（已受理、未执行），实际 body=%s", w.Body.String())

	body := parseJSON(t, w)
	assert.Equal(t, "awaiting_approval", body["status"])
	require.NotEmpty(t, body["requestId"], "必须返回 requestId 供轮询")

	// 零副作用：实例状态未被改动。
	var after model.Instance
	require.NoError(t, db.First(&after, inst.ID).Error)
	assert.Equal(t, model.InstanceStatusStopped, after.Status, "出票阶段不得改变实例状态")

	// 票据已落库，且目标与动作正确。
	var ticket model.ApprovalTicket
	require.NoError(t, db.Where("uuid = ?", body["requestId"]).First(&ticket).Error)
	assert.Equal(t, "instance_stop", ticket.Action)
	assert.Equal(t, "instance", ticket.TargetType)
	assert.Equal(t, strconv.FormatUint(uint64(inst.ID), 10), ticket.TargetID)
	assert.Equal(t, model.ApprovalApplicantAgent, ticket.ApplicantKind)
}

// TestAgentOps_StartDoesNotRequireApproval start 免审，不被审批闸拦下。
//
// 这条守住「不是所有写操作都要审」：一律要审会让批准人闭眼点同意，
// 审批就退化成橡皮图章（PRD 明确只要求 stop 进审批）。
//
// 断言手段是直接调 gateApproval 而不是发 HTTP 请求：放行后 start 会真的进入领域执行
// （本环境没有 Worker 连接池，会在 preflightStart 里失败），而那属于实例生命周期自身的
// 行为、由 instance_lifecycle_test.go 覆盖；这里要证明的只有「闸没拦它」。
func TestAgentOps_StartDoesNotRequireApproval(t *testing.T) {
	db, r, ops, inst, p := newAgentApprovalTestEnv(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/agent/instances/"+itoa(inst.ID)+"/start", nil)
	c.Params = gin.Params{{Key: "id", Value: itoa(inst.ID)}}

	blocked := ops.gateApproval(c, p, service.AgentActionInstanceStart, inst.ID)
	assert.False(t, blocked, "start 不应被审批闸拦下")

	var tickets int64
	require.NoError(t, db.Model(&model.ApprovalTicket{}).Count(&tickets).Error)
	assert.Zero(t, tickets, "start 不应产生审批票据")
	_ = r
}

// TestAgentOps_StopWithoutApprovalServiceFailsClosed 审批服务未装配时 stop 失败关闭。
//
// 放行一个该审的操作是安全事故，拒绝只是功能不可用——这个取向必须与 MCP 侧一致。
func TestAgentOps_StopWithoutApprovalServiceFailsClosed(t *testing.T) {
	_, r, ops, inst, _ := newAgentApprovalTestEnv(t)
	ops.SetApproval(nil)

	w := makeRequest(r, http.MethodPost, "/api/v1/agent/instances/"+itoa(inst.ID)+"/stop", nil, "")
	assert.Equal(t, http.StatusServiceUnavailable, w.Code, "未装配审批应失败关闭（body=%s）", w.Body.String())
	assert.NotEqual(t, http.StatusAccepted, w.Code)
}

// TestAgentActionNeedsApproval_Mapping 只认「去 agent. 前缀后命中登记表」的确定映射。
func TestAgentActionNeedsApproval_Mapping(t *testing.T) {
	name, ok := service.AgentActionNeedsApproval(service.AgentActionInstanceStop)
	assert.True(t, ok)
	assert.Equal(t, "instance_stop", name)

	// 免审 action 不得被判需审。
	if _, ok := service.AgentActionNeedsApproval(service.AgentActionInstanceStart); ok {
		t.Fatal("instance_start 已在登记表外，不应被判需审")
	}
	// 空值/未知 action 不猜。
	if _, ok := service.AgentActionNeedsApproval(""); ok {
		t.Fatal("空 action 不应被判需审")
	}
	if _, ok := service.AgentActionNeedsApproval("agent.not_a_real_action"); ok {
		t.Fatal("未知 action 不应被判需审")
	}
}
