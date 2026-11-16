package mcp

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// 审批执行结果的回读（file_issue_transfer_ticket 的「批准了拿不到」缺口）。
//
// 这组用例守住三件事：
//  1. 执行产出的票据确实落到票据上，能被申请人取回（核心缺口）；
//  2. 票据过期后取回时按冻结参数重签，而不是给一张失效凭据；
//  3. 别人的票依然取不到——回读结果不能成为绕过限域的新入口。

// approvalResultDeps 造一张已 succeeded、带执行结果的票据。
func approvalResultDeps(t *testing.T, resultJSON string) (ToolDeps, *model.ApprovalTicket) {
	t.Helper()
	db := newStatelessTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.ApprovalTicket{}, &model.ApprovalReceipt{}))
	svc := service.NewApprovalService(db)
	ticket, _, err := svc.SubmitApproval(service.SubmitApprovalParams{
		ApplicantUserID:  1,
		ApplicantKind:    model.ApprovalApplicantAgent,
		TokenID:          "9",
		Action:           "mcp.file_issue_transfer_ticket",
		TargetType:       "instance",
		TargetID:         "7",
		Payload:          map[string]any{"id": 7, "direction": "upload", "path": "plugins/folia.jar"},
		Evidence:         map[string]any{"status": "RUNNING"},
		Reason:           "上传 Folia jar",
		RequireApplicant: true,
	})
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.ApprovalTicket{}).Where("id = ?", ticket.ID).
		Updates(map[string]any{
			"status":      model.ApprovalStatusSucceeded,
			"result_json": resultJSON,
		}).Error)
	return ToolDeps{Approval: svc}, ticket
}

func TestApprovalGet_ReturnsTransferTicketResult(t *testing.T) {
	future := time.Now().Add(3 * time.Minute).UTC().Format(time.RFC3339Nano)
	deps, ticket := approvalResultDeps(t,
		`{"ticket":"tk-abc","direction":"upload","path":"plugins/folia.jar","expiresAt":"`+future+`"}`)

	res := CallTool(t.Context(), deps, tokenPrincipal(9), "approval_get",
		map[string]any{"id": float64(ticket.ID)})
	require.False(t, res.IsError, firstText(res))
	txt := firstText(res)
	// 核心断言：票据明文能取回。这正是修复前丢失的那一项。
	require.Contains(t, txt, "tk-abc", "执行产出的票据必须能被申请人事后取回")
	require.Contains(t, txt, "folia.jar")
}

func TestApprovalGet_ReissuesExpiredTransferTicket(t *testing.T) {
	// 过期票据：直接返回它只会让调用方白白撞一次 401。
	past := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	deps, ticket := approvalResultDeps(t,
		`{"ticket":"tk-stale","direction":"upload","path":"plugins/folia.jar","expiresAt":"`+past+`"}`)

	// 重签需要 Transfer 服务；未装配时应明确报错而不是回退成旧票。
	res := CallTool(t.Context(), deps, tokenPrincipal(9), "approval_get",
		map[string]any{"id": float64(ticket.ID)})
	require.False(t, res.IsError, "取票本身不应失败"+firstText(res))
	require.NotContains(t, firstText(res), "tk-stale", "过期票据不得原样返回")
	require.Contains(t, firstText(res), "重新签发失败", "应明确告知重签失败的原因")
}

func TestApprovalGet_ResultNotVisibleToOtherToken(t *testing.T) {
	future := time.Now().Add(3 * time.Minute).UTC().Format(time.RFC3339Nano)
	deps, ticket := approvalResultDeps(t,
		`{"ticket":"tk-secret","direction":"upload","path":"plugins/folia.jar","expiresAt":"`+future+`"}`)

	// 限域先于结果读取：别人的票连「存在」都不该暴露。
	res := CallTool(t.Context(), deps, tokenPrincipal(10), "approval_get",
		map[string]any{"id": float64(ticket.ID)})
	require.True(t, res.IsError)
	require.Contains(t, firstText(res), "不存在")
	require.NotContains(t, firstText(res), "tk-secret", "越权调用不得泄露票据明文")
}

func TestApprovalGet_CorruptResultDoesNotHideTicket(t *testing.T) {
	deps, ticket := approvalResultDeps(t, `{not-json`)

	res := CallTool(t.Context(), deps, tokenPrincipal(9), "approval_get",
		map[string]any{"id": float64(ticket.ID)})
	// 结果损坏只是结果不可用，票据状态本身仍必须可见——
	// 否则申请人会误以为「票没批」而重复提交。
	require.False(t, res.IsError, firstText(res))
	require.Contains(t, firstText(res), "已损坏")
}

// TestApprovalGet_ReissuedTicketIsUsable 重签成功的完整路径：过期票换成新票且能真用。
//
// 这条用例把「取回」与「能用」连起来——只断言响应里有 ticket 字段是不够的，
// 关键是签出来的票必须真的能通过 Consume（一次性 + 签名 + 授权三重校验）。
func TestApprovalGet_ReissuedTicketIsUsable(t *testing.T) {
	db := newStatelessTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.ApprovalTicket{}, &model.ApprovalReceipt{},
		&model.Node{}, &model.Instance{}, &model.AgentTransferTicket{},
	))
	agentSvc, principal, inst := seedContentPrincipal(t, db)
	tickets, terr := service.NewAgentTransferTicketService(
		service.DeriveAgentTransferTicketSecret([]byte("master")), agentSvc, nil)
	require.NoError(t, terr)

	svc := service.NewApprovalService(db)
	past := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	ticket, _, err := svc.SubmitApproval(service.SubmitApprovalParams{
		ApplicantUserID:  1,
		ApplicantKind:    model.ApprovalApplicantAgent,
		TokenID:          "1",
		Action:           "file_issue_transfer_ticket",
		TargetType:       "instance",
		TargetID:         "1",
		Payload:          map[string]any{"id": inst.ID, "direction": "upload", "path": "plugins/folia.jar"},
		Evidence:         map[string]any{"status": model.InstanceStatusStopped},
		Reason:           "上传 Folia jar",
		RequireApplicant: true,
	})
	require.NoError(t, err)
	// 造一张「执行时签过、但现在已经过期」的结果。
	require.NoError(t, db.Model(&model.ApprovalTicket{}).Where("id = ?", ticket.ID).
		Updates(map[string]any{
			"status":      model.ApprovalStatusSucceeded,
			"result_json": `{"ticket":"tk-stale","direction":"upload","path":"plugins/folia.jar","expiresAt":"` + past + `"}`,
		}).Error)

	deps := ToolDeps{Approval: svc, Transfer: tickets, Agent: agentSvc}
	res := CallTool(t.Context(), deps, principal, "approval_get", map[string]any{"id": float64(ticket.ID)})
	require.False(t, res.IsError, firstText(res))
	txt := firstText(res)
	require.Contains(t, txt, `"reissued":true`, "过期票应被重签而不是原样返回")
	require.NotContains(t, txt, "tk-stale", "过期票不得出现在响应里")

	// 从响应里取出新票，验证它真的可用。
	var payload struct {
		Result struct {
			Ticket string `json:"ticket"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(txt), &payload))
	require.NotEmpty(t, payload.Result.Ticket, "响应应包含重签后的票据")
	claims, cerr := tickets.Consume(payload.Result.Ticket)
	require.NoError(t, cerr, "重签的票必须真能消费")
	require.Equal(t, inst.ID, claims.InstanceID)
}

// TestApprovalGet_ReissueFailsAfterScopeRevoked 收权之后重签必须失败。
//
// 批准不等于永久授权：票据上记录的 tokenId 在批准后可能已被吊销或收窄。
// 重签走调用方**当前**主体的授权，因此收权会当场生效——
// 若改成按历史 tokenId 重建主体，审批就成了绕过收权的手段。
func TestApprovalGet_ReissueFailsAfterScopeRevoked(t *testing.T) {
	db := newStatelessTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.ApprovalTicket{}, &model.ApprovalReceipt{},
		&model.Node{}, &model.Instance{}, &model.AgentTransferTicket{},
	))
	agentSvc, principal, inst := seedContentPrincipal(t, db)
	tickets, terr := service.NewAgentTransferTicketService(
		service.DeriveAgentTransferTicketSecret([]byte("master")), agentSvc, nil)
	require.NoError(t, terr)

	svc := service.NewApprovalService(db)
	past := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	ticket, _, err := svc.SubmitApproval(service.SubmitApprovalParams{
		ApplicantUserID:  1,
		ApplicantKind:    model.ApprovalApplicantAgent,
		TokenID:          "1",
		Action:           "file_issue_transfer_ticket",
		TargetType:       "instance",
		TargetID:         "1",
		Payload:          map[string]any{"id": inst.ID, "direction": "upload", "path": "plugins/folia.jar"},
		Evidence:         map[string]any{"status": model.InstanceStatusStopped},
		Reason:           "上传 Folia jar",
		RequireApplicant: true,
	})
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.ApprovalTicket{}).Where("id = ?", ticket.ID).
		Updates(map[string]any{
			"status":      model.ApprovalStatusSucceeded,
			"result_json": `{"ticket":"tk-stale","direction":"upload","path":"plugins/folia.jar","expiresAt":"` + past + `"}`,
		}).Error)

	// 调用方主体被收窄到另一个实例：重签的授权判定必然失败。
	revoked := &service.AgentPrincipal{
		TokenID:          principal.TokenID,
		PolicyVersion:    service.AgentPolicyVersionV2,
		Capabilities:     []string{service.AgentCapabilityInstanceContent},
		ScopedInstanceIDs: []uint{inst.ID + 999},
	}
	deps := ToolDeps{Approval: svc, Transfer: tickets, Agent: agentSvc}
	res := CallTool(t.Context(), deps, revoked, "approval_get", map[string]any{"id": float64(ticket.ID)})
	require.False(t, res.IsError, firstText(res))
	txt := firstText(res)
	require.Contains(t, txt, "重新签发失败", "收权后重签必须失败")
	require.NotContains(t, txt, "tk-stale", "失败时也不得把过期票交出去")
}

// 保证测试用的 DB 类型显式可见（helper 返回 *gorm.DB）。
var _ *gorm.DB

// seedContentPrincipal 造节点 + 实例 + 一把持 instance.content 的 V2 Token，
// 返回鉴权服务、已认证主体与实例。重签用例需要真实授权链路才能验证
// 「收权后重签会失败」——用假主体测不出这一点。
func seedContentPrincipal(t *testing.T, db *gorm.DB) (*service.AgentTokenService, *service.AgentPrincipal, *model.Instance) {
	t.Helper()
	node := &model.Node{
		Name: "n1", Host: "127.0.0.1", GRPCPort: 9100, WSPort: 9101,
		Secret: "s1", Status: model.NodeStatusOnline,
	}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{
		NodeID: node.ID, Name: "i1", Type: model.InstanceTypeGeneric,
		ProcessType: model.ProcessTypeDirect, StartCommand: "x",
		Status: model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(inst).Error)

	svc := service.NewAgentTokenService(db)
	_, plain, err := svc.Issue(service.IssueAgentTokenRequest{
		Name:                 "content-agent",
		ScopedInstanceIDs:    []uint{inst.ID},
		PolicyVersion:        service.AgentPolicyVersionV2,
		CapabilitiesProvided: true,
		Capabilities:         []string{service.AgentCapabilityInstanceContent},
		CreatedBy:            1,
	})
	require.NoError(t, err)
	p, err := svc.Authenticate(plain)
	require.NoError(t, err)
	return svc, p, inst
}
