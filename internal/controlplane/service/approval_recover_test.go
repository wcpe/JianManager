package service

/*
 * 审批执行中断的启动回收（P1 回归）。
 *
 * 修复前：票据停在 executing 后无人问津——
 * `ExpireStale` 只扫 pending，重试被 CAS 拒，且 executing 不在终态集里。
 * 这组用例钉住“重启后一定收口”与“宁可说未知也不要诱人重试”两条。
 */

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestRecoverInterruptedExecutions_UnsticksExecutingTickets 重启后 executing 票必须离开该态。
func TestRecoverInterruptedExecutions_UnsticksExecutingTickets(t *testing.T) {
	db := newApprovalTestDB(t)
	svc := NewApprovalService(db)

	tk := &model.ApprovalTicket{
		ApplicantUserID: 1, ApplicantKind: model.ApprovalApplicantHuman,
		Action: "instance_stop", TargetType: "instance", TargetID: "1",
		PayloadHash: "h", PayloadFrozen: "{}", Evidence: "{}", Reason: "test",
		Status: model.ApprovalStatusExecuting, ExpiresAt: time.Now().Add(time.Hour),
	}
	require.NoError(t, db.Create(tk).Error)
	require.NoError(t, db.Create(&model.ApprovalReceipt{
		TicketID: tk.ID, Outcome: model.ApprovalReceiptInFlight, StartedAt: time.Now(),
	}).Error)

	n, r, err := svc.RecoverInterruptedExecutions()
	require.NoError(t, err)
	require.Equal(t, int64(1), n, "应有一张票被收口")
	require.Equal(t, int64(1), r, "应有一条回执被标未知")

	var got model.ApprovalTicket
	require.NoError(t, db.First(&got, tk.ID).Error)
	require.Equal(t, model.ApprovalStatusFailed, got.Status, "必须离开 executing")
	require.Contains(t, got.FailureReason, "PROCESS_RESTART")

	// 回执标未知而不是 failed：动作可能已生效，标 failed 会诱人重试。
	var rec model.ApprovalReceipt
	require.NoError(t, db.First(&rec).Error)
	require.Equal(t, model.ApprovalReceiptUnverified, rec.Outcome)
	require.Equal(t, "PROCESS_RESTART", rec.ErrorCode)
}

// TestRecoverInterruptedExecutions_Idempotent 重复启动不重复改动。
func TestRecoverInterruptedExecutions_Idempotent(t *testing.T) {
	db := newApprovalTestDB(t)
	svc := NewApprovalService(db)

	tk := &model.ApprovalTicket{
		ApplicantUserID: 1, ApplicantKind: model.ApprovalApplicantHuman,
		Action: "instance_stop", TargetType: "instance", TargetID: "1",
		PayloadHash: "h", PayloadFrozen: "{}", Evidence: "{}", Reason: "test",
		Status: model.ApprovalStatusExecuting, ExpiresAt: time.Now().Add(time.Hour),
	}
	require.NoError(t, db.Create(tk).Error)

	first, _, err := svc.RecoverInterruptedExecutions()
	require.NoError(t, err)
	require.Equal(t, int64(1), first)

	second, _, err := svc.RecoverInterruptedExecutions()
	require.NoError(t, err)
	require.Equal(t, int64(0), second, "已收口的票不该被再次选中")
}

// TestRecoverInterruptedExecutions_LeavesOtherStatesAlone 不动 pending / 终态票。
func TestRecoverInterruptedExecutions_LeavesOtherStatesAlone(t *testing.T) {
	db := newApprovalTestDB(t)
	svc := NewApprovalService(db)

	pending := &model.ApprovalTicket{
		ApplicantUserID: 1, ApplicantKind: model.ApprovalApplicantHuman,
		Action: "instance_stop", TargetType: "instance", TargetID: "1",
		PayloadHash: "h1", PayloadFrozen: "{}", Evidence: "{}", Reason: "test",
		Status: model.ApprovalStatusPending, ExpiresAt: time.Now().Add(time.Hour),
	}
	succeeded := &model.ApprovalTicket{
		ApplicantUserID: 1, ApplicantKind: model.ApprovalApplicantHuman,
		Action: "instance_stop", TargetType: "instance", TargetID: "2",
		PayloadHash: "h2", PayloadFrozen: "{}", Evidence: "{}", Reason: "test",
		Status: model.ApprovalStatusSucceeded, ExpiresAt: time.Now().Add(time.Hour),
	}
	require.NoError(t, db.Create(pending).Error)
	require.NoError(t, db.Create(succeeded).Error)

	n, _, err := svc.RecoverInterruptedExecutions()
	require.NoError(t, err)
	require.Equal(t, int64(0), n)

	var p, s model.ApprovalTicket
	require.NoError(t, db.First(&p, pending.ID).Error)
	require.NoError(t, db.First(&s, succeeded.ID).Error)
	require.Equal(t, model.ApprovalStatusPending, p.Status, "pending 票不该被动")
	require.Equal(t, model.ApprovalStatusSucceeded, s.Status, "终态票不该被动")
}
