package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 审批执行产出的落库（file_issue_transfer_ticket 的核心修复）。
//
// 修复前该 adapter 写的是 `_, _, err = d.Transfer.Issue(...)`——票据签发后明文被丢弃，
// 而服务端只存 sha256(digest) 无法还原，等于「批准了却永远拿不到」。
// 这组用例钉住的正是「执行产出必须能被申请人事后取回」。

// setupApprovalResultDB 造审批所需的全部表。
func setupApprovalResultDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	require.NoError(t, db.AutoMigrate(
		&model.ApprovalTicket{}, &model.ApprovalReceipt{},
		&model.AgentToken{}, &model.Instance{}, &model.Node{}, &model.AgentTransferTicket{},
	))
	return db
}

// TestApprovalResult_TransferTicketPersistedToTicket 执行器应把签发的票据写进 result_json。
func TestApprovalResult_TransferTicketPersistedToTicket(t *testing.T) {
	db := setupApprovalResultDB(t)
	agentSvc, principal, inst := seedTransferPrincipal(t, db)
	tickets, err := NewAgentTransferTicketService(
		DeriveAgentTransferTicketSecret([]byte("master")), agentSvc, nil)
	require.NoError(t, err)

	svc := NewApprovalService(db)
	ex := NewApprovalExecutor(db, svc, nil)
	missing := RegisterApprovalAdapters(ex, ApprovalAdapterDeps{
		Transfer: tickets,
		Agent:    agentSvc,
	})
	require.NotContains(t, missing, "file_issue_transfer_ticket", "该 action 必须已登记 adapter")

	// 出票 → 批准 → 执行，走与真实审批完全相同的编排。
	ticket, _, err := svc.SubmitApproval(SubmitApprovalParams{
		ApplicantUserID:  1,
		ApplicantKind:    model.ApprovalApplicantAgent,
		TokenID:          itoaUint(principal.TokenID),
		Action:           "file_issue_transfer_ticket",
		TargetType:       "instance",
		TargetID:         itoaUint(inst.ID),
		Payload:          map[string]any{"id": inst.ID, "direction": "upload", "path": "plugins/folia.jar"},
		Evidence:         map[string]any{"status": model.InstanceStatusStopped},
		Reason:           "上传 Folia jar",
		RequireApplicant: true,
	})
	require.NoError(t, err)

	_, derr := svc.Decide(context.Background(), DecideApprovalParams{
		TicketID: ticket.ID, ActorUserID: 1, Approve: true,
	})
	require.NoError(t, derr)
	require.NoError(t, ex.Execute(context.Background(), ticket.ID))

	var got model.ApprovalTicket
	require.NoError(t, db.First(&got, ticket.ID).Error)
	require.Equal(t, model.ApprovalStatusSucceeded, got.Status, "执行应成功")
	require.NotEmpty(t, got.ResultJSON, "执行产出必须落库——否则申请人取不到票据")

	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(got.ResultJSON), &result))
	ticketPlain, _ := result["ticket"].(string)
	require.NotEmpty(t, ticketPlain, "结果里必须带票据明文")
	require.Equal(t, "upload", result["direction"])
	require.Equal(t, "plugins/folia.jar", result["path"])

	// 最终闭环：拿回来的明文必须真的能用（一次性消费成功）。
	claims, cerr := tickets.Consume(ticketPlain)
	require.NoError(t, cerr, "取回的票据必须可用")
	require.Equal(t, inst.ID, claims.InstanceID)
}

// TestApprovalResult_FailedExecutionWritesNothing 执行失败时不写结果。
//
// 失败意味着产出没有真正成立（票据可能签发了一半、或根本没走到签发），
// 把它当结果回传等于给申请人一份假凭据。
func TestApprovalResult_FailedExecutionWritesNothing(t *testing.T) {
	db := setupApprovalResultDB(t)
	svc := NewApprovalService(db)
	ex := NewApprovalExecutor(db, svc, nil)
	ex.RegisterAdapterWithResult("file_issue_transfer_ticket",
		func(context.Context, *model.ApprovalTicket, map[string]any) (any, error) {
			return map[string]any{"ticket": "should-not-appear"}, errFakeExec
		})

	ticket, _, err := svc.SubmitApproval(SubmitApprovalParams{
		ApplicantUserID:  1,
		ApplicantKind:    model.ApprovalApplicantAgent,
		TokenID:          "9",
		Action:           "file_issue_transfer_ticket",
		TargetType:       "instance",
		TargetID:         "7",
		Payload:          map[string]any{"id": 7, "direction": "upload", "path": "x.jar"},
		Evidence:         map[string]any{"status": "RUNNING"},
		Reason:           "测试",
		RequireApplicant: true,
	})
	require.NoError(t, err)
	_, derr := svc.Decide(context.Background(), DecideApprovalParams{
		TicketID: ticket.ID, ActorUserID: 1, Approve: true,
	})
	require.NoError(t, derr)
	_ = ex.Execute(context.Background(), ticket.ID)

	var got model.ApprovalTicket
	require.NoError(t, db.First(&got, ticket.ID).Error)
	require.Equal(t, model.ApprovalStatusFailed, got.Status)
	require.Empty(t, got.ResultJSON, "失败时不得留下结果——那会是一份假凭据")
}

type fakeExecError struct{}

func (fakeExecError) Error() string { return "模拟执行失败" }

var errFakeExec = fakeExecError{}

// itoaUint 把 id 转成票据里的 TokenID / TargetID 字符串形态。
func itoaUint(v uint) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
