package service

// 回归：执行收尾不得覆盖已被别人推进的终态。
//
// 缺陷成因：FinishExecution 的票据更新早期没有任何状态条件，
// 只要 id 匹配就整行改成 succeeded/failed。而同一张票上还有其他写入者：
//   - MarkExecutionPanicked（panic 时把票推到 failed）；
//   - RecoverInterruptedExecutions（进程重启时 executing → failed）。
// 于是会出现【一张 panic 过的票显示成功】：panic 先落 failed，
// 执行体的 defer 再调 FinishExecution(nil) 把它改成 succeeded，
// 而动作实际只做了一半。这也违反了本文件开头的「不变量 3：CAS + RowsAffected 判定」。

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestFinishExecutionDoesNotOverridePanickedTicket panic 收口后，正常收尾不得把它改成 succeeded。
func TestFinishExecutionDoesNotOverridePanickedTicket(t *testing.T) {
	db := newApprovalTestDB(t)
	svc := NewApprovalService(db)

	tk := &model.ApprovalTicket{
		ApplicantUserID: 1, ApplicantKind: model.ApprovalApplicantHuman,
		Action: "instance_stop", TargetType: "instance", TargetID: "1",
		PayloadHash: "h", PayloadFrozen: "{}", Evidence: "{}", Reason: "test",
		Status: model.ApprovalStatusExecuting, ExpiresAt: time.Now().Add(time.Hour),
	}
	require.NoError(t, db.Create(tk).Error)

	receipt := &model.ApprovalReceipt{
		TicketID: tk.ID, Outcome: model.ApprovalReceiptInFlight, StartedAt: time.Now(),
	}
	require.NoError(t, db.Create(receipt).Error)

	// 执行体 panic：票被推到 failed。
	require.NoError(t, svc.MarkExecutionPanicked(tk.ID, "nil map write"))

	// 执行体的 defer 随后正常收尾（execErr=nil）。
	require.NoError(t, svc.FinishExecution(tk.ID, receipt.ID, nil))

	var got model.ApprovalTicket
	require.NoError(t, db.First(&got, tk.ID).Error)
	require.Equal(t, model.ApprovalStatusFailed, got.Status,
		"已被 panic 收口为 failed 的票不得被正常收尾覆盖成 succeeded")
	require.Contains(t, got.FailureReason, "EXECUTION_PANIC",
		"失败原因应保留 panic 现场")
}

// TestFinishExecutionDoesNotResurrectRestartRecoveredTicket 启动收口后的票也不得被晚到的收尾改写。
func TestFinishExecutionDoesNotResurrectRestartRecoveredTicket(t *testing.T) {
	db := newApprovalTestDB(t)
	svc := NewApprovalService(db)

	tk := &model.ApprovalTicket{
		ApplicantUserID: 1, ApplicantKind: model.ApprovalApplicantHuman,
		Action: "instance_stop", TargetType: "instance", TargetID: "1",
		PayloadHash: "h", PayloadFrozen: "{}", Evidence: "{}", Reason: "test",
		Status: model.ApprovalStatusExecuting, ExpiresAt: time.Now().Add(time.Hour),
	}
	require.NoError(t, db.Create(tk).Error)
	receipt := &model.ApprovalReceipt{
		TicketID: tk.ID, Outcome: model.ApprovalReceiptInFlight, StartedAt: time.Now(),
	}
	require.NoError(t, db.Create(receipt).Error)

	_, _, err := svc.RecoverInterruptedExecutions()
	require.NoError(t, err)

	require.NoError(t, svc.FinishExecution(tk.ID, receipt.ID, nil))

	var got model.ApprovalTicket
	require.NoError(t, db.First(&got, tk.ID).Error)
	require.Equal(t, model.ApprovalStatusFailed, got.Status,
		"重启已收口为 failed 的票不得被晚到的收尾改成 succeeded")
	require.Contains(t, got.FailureReason, "PROCESS_RESTART")
}

// TestFinishExecutionNormalPathSucceeds 钉住正常路径仍然生效（避免修成“永不写终态”）。
func TestFinishExecutionNormalPathSucceeds(t *testing.T) {
	db := newApprovalTestDB(t)
	svc := NewApprovalService(db)

	tk := &model.ApprovalTicket{
		ApplicantUserID: 1, ApplicantKind: model.ApprovalApplicantHuman,
		Action: "instance_stop", TargetType: "instance", TargetID: "1",
		PayloadHash: "h", PayloadFrozen: "{}", Evidence: "{}", Reason: "test",
		Status: model.ApprovalStatusExecuting, ExpiresAt: time.Now().Add(time.Hour),
	}
	require.NoError(t, db.Create(tk).Error)
	receipt := &model.ApprovalReceipt{
		TicketID: tk.ID, Outcome: model.ApprovalReceiptInFlight, StartedAt: time.Now(),
	}
	require.NoError(t, db.Create(receipt).Error)

	require.NoError(t, svc.FinishExecution(tk.ID, receipt.ID, nil))

	var got model.ApprovalTicket
	require.NoError(t, db.First(&got, tk.ID).Error)
	require.Equal(t, model.ApprovalStatusSucceeded, got.Status)

	var rec model.ApprovalReceipt
	require.NoError(t, db.First(&rec, receipt.ID).Error)
	require.Equal(t, model.ApprovalReceiptOK, rec.Outcome)
}
