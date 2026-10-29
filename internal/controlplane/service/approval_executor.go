package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 审批执行器。
//
// 职责边界：本文件只管「把一张已批准的票据变成一次真实执行」，
// **不知道**任何具体领域（实例启停、文件写）。领域动作由注册进来的 adapter 提供。
//
// 三条不变量：
//  1. **认领靠 CAS**（executing + consumed_at 同事务写 in_flight 回执），不靠进程内锁；
//  2. **执行前重校验**：payload 哈希、目标漂移、审批人身份；任一不符即 failed 且**不执行**；
//  3. **长操作经 TaskService.RunAsync**，不另造调度（否则任务中心会出现两条互不相识的链路）。

// ApprovalAdapter 领域执行适配器（按 action 登记时一并提供）。
//
// 签名刻意收窄成「票据 + 冻结参数」：adapter 不能自己去看请求上下文里的原始参数，
// 只能执行**批准时冻结的那一份**——否则「批准的」与「执行的」可能不是同一件事。
type ApprovalAdapter func(ctx context.Context, t *model.ApprovalTicket, payload map[string]any) error

// ApprovalExecutor 审批执行器。
type ApprovalExecutor struct {
	db  *gorm.DB
	svc *ApprovalService
	// tasks 用于承载长操作；为 nil 时退化为同步执行（仅测试/未装配场景）。
	tasks *TaskService
	// adapters action → 领域执行器（登记）。
	adapters map[string]ApprovalAdapter
	// verifyTarget 目标漂移校验；为 nil 时视为不校验（调用方负责保证已装配）。
	verifyTarget func(t *model.ApprovalTicket, evidence map[string]any) error
	// audit 审计写入。为 nil 时只跳过审计，不影响执行结论。
	audit *AuditService
}

// SetAudit 注入审计服务（装配层调用；缺省 nil 时执行仍可用，但批准/执行不留痕）。
func (e *ApprovalExecutor) SetAudit(a *AuditService) { e.audit = a }

// NewApprovalExecutor 创建执行器。
func NewApprovalExecutor(db *gorm.DB, svc *ApprovalService, tasks *TaskService) *ApprovalExecutor {
	return &ApprovalExecutor{db: db, svc: svc, tasks: tasks, adapters: map[string]ApprovalAdapter{}}
}

// RegisterAdapter 登记某 action 的执行器（供的登记表装配）。
func (e *ApprovalExecutor) RegisterAdapter(action string, fn ApprovalAdapter) {
	if e.adapters == nil {
		e.adapters = map[string]ApprovalAdapter{}
	}
	e.adapters[action] = fn
}

// SetTargetVerifier 注入目标漂移校验。
func (e *ApprovalExecutor) SetTargetVerifier(fn func(t *model.ApprovalTicket, evidence map[string]any) error) {
	e.verifyTarget = fn
}

// Execute 执行一张**已被批准**（status=executing）的票据。
//
// 调用前提：票据已由 Decide 推进到 executing。本函数负责认领、重校验、执行、收尾。
func (e *ApprovalExecutor) Execute(ctx context.Context, ticketID uint) error {
	var ticket model.ApprovalTicket
	if err := e.db.First(&ticket, ticketID).Error; err != nil {
		return ErrApprovalNotFound
	}
	if ticket.Status != model.ApprovalStatusExecuting {
		return fmt.Errorf("%w: 期望 executing，实际 %s", ErrApprovalNotPending, ticket.Status)
	}

	// ① 认领（CAS + in_flight 回执同事务）。RowsAffected==0 说明已被别的执行器认领。
	receiptID, err := e.svc.ClaimForExecution(ctx, ticketID)
	if err != nil {
		return err
	}

	// ② 重校验：参数未被篡改。
	var frozen map[string]any
	if err := json.Unmarshal([]byte(ticket.PayloadFrozen), &frozen); err != nil {
		return e.finish(ticketID, receiptID, fmt.Errorf("%w: 冻结参数损坏", ErrApprovalPayloadTampered))
	}
	recomputed, err := hashApprovalPayload(frozen)
	if err != nil {
		return e.finish(ticketID, receiptID, err)
	}
	if recomputed != ticket.PayloadHash {
		// 批准后参数被改：一律不执行。这是「批准的」与「执行的」必须同一件事的底线。
		return e.finish(ticketID, receiptID, ErrApprovalPayloadTampered)
	}

	// ③ 重校验：目标漂移（资源版本与证据快照不符则不执行）。
	if e.verifyTarget != nil {
		var evidence map[string]any
		if ticket.Evidence != "" {
			_ = json.Unmarshal([]byte(ticket.Evidence), &evidence)
		}
		if err := e.verifyTarget(&ticket, evidence); err != nil {
			return e.finish(ticketID, receiptID, err)
		}
	}

	// ④ 领域执行。未登记 adapter = 失败关闭（不是静默成功）。
	adapter, ok := e.adapters[ticket.Action]
	if !ok {
		return e.finish(ticketID, receiptID,
			fmt.Errorf("%w: %s", ErrApprovalNotRegistered, ticket.Action))
	}

	// 长操作经 RunAsync 承载：任务中心能看到，且与票据状态联动。
	// 注意不能把 execErr 直接跨出 RunAsync——它是 fire-and-forget 的，
	// 因此收尾（回执 + 票据终态）在 RunAsync 的 work 内部完成。
	if e.tasks != nil {
		e.tasks.RunAsync(RunSpec{
			InstanceID: instanceIDFromTarget(&ticket),
			Kind:       model.TaskKindApprovalExecute,
			Title:      fmt.Sprintf("审批执行：%s", ticket.Action),
			CreatedBy:  ticket.ApplicantUserID,
			Detail:     "审批已批准，开始执行",
		}, func(runCtx context.Context, stage func(int, string)) (string, error) {
			stage(10, "执行中")
			execErr := adapter(runCtx, &ticket, frozen)
			stage(90, "收尾")
			// 收尾在 work 内部完成，确保任务终态与票据终态一致。
			_ = e.finish(ticketID, receiptID, execErr)
			if execErr != nil {
				return "", execErr
			}
			return "审批执行完成", nil
		})
		return nil
	}

	// 未装配 tasks：同步执行（仅测试场景）。
	return e.finish(ticketID, receiptID, adapter(ctx, &ticket, frozen))
}

// finish 写回执与票据终态（收尾集中一处，避免成功/失败两条路各写一遍）。
// finish 落执行结果并写审计。
//
// 审计在这里写（而不是在 adapter 里各写一遍）：finish 是**所有**分支的唯一收尾点，
// 成功、失败、失败关闭都必须留痕。缺了它，「谁批准了哪个高危操作、最后成没成」
// 就只剩 approval_receipts（执行面回执）可查，而审计才是运维追溯的操作流水。
func (e *ApprovalExecutor) finish(ticketID, receiptID uint, execErr error) error {
	if err := e.svc.FinishExecution(ticketID, receiptID, execErr); err != nil {
		return err
	}
	e.recordAudit(ticketID, execErr)
	return execErr
}

// recordAudit 写一条审批执行审计（成功与失败都留痕）。
//
// 失败原因取 execErr 原文（RecordResult 内部会截断到 512 字节）。
// 不因审计失败改变执行结论：审计是旁路，不该让已发生的副作用看起来像没发生。
func (e *ApprovalExecutor) recordAudit(ticketID uint, execErr error) {
	if e.audit == nil {
		return
	}
	var ticket model.ApprovalTicket
	if err := e.db.First(&ticket, ticketID).Error; err != nil {
		return
	}
	detail := fmt.Sprintf(`{"ticketId":%d,"action":%q,"applicantUserId":%d}`,
		ticket.ID, ticket.Action, ticket.ApplicantUserID)
	if ticket.DecidedByUserID != nil {
		detail = fmt.Sprintf(`{"ticketId":%d,"action":%q,"applicantUserId":%d,"decidedByUserId":%d}`,
			ticket.ID, ticket.Action, ticket.ApplicantUserID, *ticket.DecidedByUserID)
	}
	// 审计的操作者记为**批准人**：执行是他的决策结果，不是申请人也不是系统。
	var actor uint
	if ticket.DecidedByUserID != nil {
		actor = *ticket.DecidedByUserID
	}
	if execErr != nil {
		e.audit.RecordSafe(actor, "approval.execute_failed", ticket.TargetType, ticket.TargetID, detail, "")
		return
	}
	e.audit.RecordSafe(actor, "approval.executed", ticket.TargetType, ticket.TargetID, detail, "")
}

// instanceIDFromTarget 从目标里取实例 id（任务归属用；非实例目标返回 0）。
func instanceIDFromTarget(t *model.ApprovalTicket) uint {
	if t.TargetType != "instance" {
		return 0
	}
	var id uint
	if _, err := fmt.Sscanf(strings.TrimSpace(t.TargetID), "%d", &id); err != nil {
		return 0
	}
	return id
}
