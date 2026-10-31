package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 平台级审批服务。
//
// 三条不变量，改动前请先读 spec §2：
//  1. **批准仅人类 JWT**：Agent Token 在**服务层**永久不可批准——判据是调用方身份，
//     不是「有没有 approval.decide 权限节点」。隐藏工具列表挡不住直接打 HTTP。
//  2. **幂等由数据库部分唯一索引保证**，不是应用层「先查再插」（后者并发下双双插入）。
//  3. **只有 pending 可决策**；CAS + RowsAffected 判定，不依赖进程内锁。

// 审批相关错误（HTTP 层据此映射状态码）。
var (
	// ErrApprovalNotFound 票据不存在**或不属于查看者**。
	// 越权与不存在共用同一错误：分开回 403/404 等于给出「这个 id 存在吗」的探测接口。
	ErrApprovalNotFound = errors.New("审批票据不存在")
	// ErrApprovalNotPending 票据当前状态不允许该决策（已决策/已撤回/已过期等）。
	ErrApprovalNotPending = errors.New("该审批已不是待审批状态")
	// ErrApprovalAgentCannotDecide **Agent 身份永久不可批准/驳回**。
	ErrApprovalAgentCannotDecide = errors.New("Agent 身份不可批准或驳回审批")
	// ErrApprovalExpired 已超过 24 小时。
	ErrApprovalExpired = errors.New("该审批已过期")
	// ErrApprovalTargetDrift 执行前目标漂移（资源版本与证据快照不符）。
	ErrApprovalTargetDrift = errors.New("目标已漂移，审批不再适用")
	// ErrApprovalPayloadTampered 冻结参数与哈希不符。
	ErrApprovalPayloadTampered = errors.New("审批参数已被篡改")
	// ErrApprovalNotRegistered 该 action 未登记审批策略（失败关闭）。
	ErrApprovalNotRegistered = errors.New("该操作未登记审批策略")
)

// ApprovalTTL 审批有效期（spec §2 冻结：24 小时，与聊天确认的 10 分钟刻意区分）。
const ApprovalTTL = 24 * time.Hour

// ApprovalService 审批票据服务。
type ApprovalService struct {
	db *gorm.DB
	// now 便于测试替换时钟（过期判定与 24h TTL 都依赖它）。
	now func() time.Time

	// 到期巡检的启停（与 artifact_reconcile 同构）。
	loopMu  sync.Mutex
	stopCh  chan struct{}
	running bool
}

// NewApprovalService 创建审批服务。
func NewApprovalService(db *gorm.DB) *ApprovalService {
	return &ApprovalService{db: db, now: time.Now}
}

// ── 申请 ────────────────────────────────────────────────────────────────

// SubmitApprovalParams 提交审批申请的入参。
type SubmitApprovalParams struct {
	ApplicantUserID uint
	// ApplicantKind human / agent。
	ApplicantKind string
	// TokenID Agent 调用时记录是哪把 Token（人类为空）。
	TokenID string
	// Action 按 action 粒度（如 mcp.instance_stop）。
	Action string
	// TargetType / TargetID 由**服务端**解析后传入，客户端不得自报归属。
	TargetType string
	TargetID   string
	// Payload 冻结参数原文（调用方已规范化）。
	Payload map[string]any
	// Evidence 申请时的资源快照。
	Evidence map[string]any
	// Reason 理由（必填）。
	Reason string
}

// SubmitApproval 提交一张审批票据；pending 期间重复提交返回**原票据**。
func (s *ApprovalService) SubmitApproval(p SubmitApprovalParams) (*model.ApprovalTicket, bool, error) {
	if strings.TrimSpace(p.Reason) == "" {
		// 没有理由的审批请求等于让批准人凭空判断，直接拒收。
		return nil, false, fmt.Errorf("%w: 审批理由不能为空", ErrAssistantInvalid)
	}
	if p.Action == "" {
		return nil, false, fmt.Errorf("%w: 缺少 action", ErrAssistantInvalid)
	}
	if p.ApplicantKind == "" {
		p.ApplicantKind = model.ApprovalApplicantHuman
	}
	hash, err := hashApprovalPayload(p.Payload)
	if err != nil {
		return nil, false, fmt.Errorf("序列化参数失败: %w", err)
	}
	frozen, err := json.Marshal(p.Payload)
	if err != nil {
		return nil, false, fmt.Errorf("冻结参数失败: %w", err)
	}
	evidence, err := json.Marshal(p.Evidence)
	if err != nil {
		return nil, false, fmt.Errorf("序列化证据失败: %w", err)
	}

	now := s.now()
	ticket := &model.ApprovalTicket{
		ApplicantUserID: p.ApplicantUserID,
		ApplicantKind:   p.ApplicantKind,
		TokenID:         p.TokenID,
		Action:          p.Action,
		TargetType:      p.TargetType,
		TargetID:        p.TargetID,
		PayloadHash:     hash,
		PayloadFrozen:   string(frozen),
		Evidence:        string(evidence),
		Reason:          truncateRunes(p.Reason, 512),
		Status:          model.ApprovalStatusPending,
		ExpiresAt:       now.Add(ApprovalTTL),
	}

	// 幂等：靠部分唯一索引 `ux_approval_tickets_pending`。
	// 先查再插在并发下会双双插入，幂等当场破功——与 assistant_runs 幂等键同一个坑。
	err = s.db.Create(ticket).Error
	if err != nil {
		if isUniqueViolation(err) {
			// 已有同条件的 pending 票据：回读它，语义是「你之前提过，这就是那张」。
			var existing model.ApprovalTicket
			e2 := s.db.Where(
				"applicant_user_id = ? AND action = ? AND target_type = ? AND target_id = ? AND payload_hash = ? AND status = ?",
				p.ApplicantUserID, p.Action, p.TargetType, p.TargetID, hash, model.ApprovalStatusPending).
				First(&existing).Error
			if e2 == nil {
				return &existing, false, nil
			}
			return nil, false, fmt.Errorf("提交审批失败: %w", err)
		}
		return nil, false, fmt.Errorf("提交审批失败: %w", err)
	}
	return ticket, true, nil
}

// hashApprovalPayload 参数规范化后的 SHA-256（键排序，保证同内容同哈希）。
//
// 必须与「执行前重算」用**同一个函数**，否则「批准后参数没变」这件事无法证明。
func hashApprovalPayload(payload map[string]any) (string, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	keys := make([]string, 0, len(payload))
	for k := range payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	norm := make([]any, 0, len(keys)*2)
	for _, k := range keys {
		norm = append(norm, k, payload[k])
	}
	raw, err := json.Marshal(norm)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// ── 申请人自查（对齐 Beacon 语义）──────────────────────────────────────

// ApprovalListFilter 查询过滤。
type ApprovalListFilter struct {
	// ApplicantUserID 非空时只查该申请人的票（own.list）。
	ApplicantUserID *uint
	Status          string
	Action          string
	Limit           int
	Offset          int
}

// ListApprovals 列表查询（调用方负责传入正确的范围）。
func (s *ApprovalService) ListApprovals(f ApprovalListFilter) ([]model.ApprovalTicket, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	q := s.db.Model(&model.ApprovalTicket{})
	if f.ApplicantUserID != nil {
		q = q.Where("applicant_user_id = ?", *f.ApplicantUserID)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Action != "" {
		q = q.Where("action = ?", f.Action)
	}
	var out []model.ApprovalTicket
	if err := q.Order("created_at DESC, id DESC").Limit(limit).Offset(f.Offset).Find(&out).Error; err != nil {
		return nil, err
	}
	// 惰性过期：读到的 pending 若已超期，**回写状态**再返回——
	// 只显示过期而不回写，会让列表「看着待审但点不动」。
	for i := range out {
		if out[i].Status == model.ApprovalStatusPending && s.isExpired(&out[i]) {
			if err := s.expireTicket(out[i].ID); err == nil {
				out[i].Status = model.ApprovalStatusExpired
			}
		}
	}
	return out, nil
}

// GetApproval 单张详情；applicantID 非空时校验归属（越权与不存在共用 not found）。
func (s *ApprovalService) GetApproval(id uint, applicantID *uint) (*model.ApprovalTicket, error) {
	var t model.ApprovalTicket
	if err := s.db.First(&t, id).Error; err != nil {
		return nil, ErrApprovalNotFound
	}
	if applicantID != nil && t.ApplicantUserID != *applicantID {
		// 不区分「不存在」与「不是你的」——不给探测接口。
		return nil, ErrApprovalNotFound
	}
	if t.Status == model.ApprovalStatusPending && s.isExpired(&t) {
		if err := s.expireTicket(t.ID); err == nil {
			t.Status = model.ApprovalStatusExpired
		}
	}
	return &t, nil
}

// Withdraw 申请人撤回（**仅 pending**）。
func (s *ApprovalService) Withdraw(id uint, applicantID uint) (*model.ApprovalTicket, error) {
	var t model.ApprovalTicket
	if err := s.db.First(&t, id).Error; err != nil {
		return nil, ErrApprovalNotFound
	}
	if t.ApplicantUserID != applicantID {
		return nil, ErrApprovalNotFound
	}
	if t.Status != model.ApprovalStatusPending {
		return nil, fmt.Errorf("%w: 当前状态 %s", ErrApprovalNotPending, t.Status)
	}
	now := s.now()
	// CAS：WHERE status='pending'，靠 RowsAffected 判断是否抢到。
	res := s.db.Model(&model.ApprovalTicket{}).
		Where("id = ? AND status = ?", id, model.ApprovalStatusPending).
		Updates(map[string]any{
			"status":     model.ApprovalStatusWithdrawn,
			"decided_at": now,
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrApprovalNotPending
	}
	return s.GetApproval(id, &applicantID)
}

// ── 人类决策 ────────────────────────────────────────────────────────────

// DecideApprovalParams 决策入参。
type DecideApprovalParams struct {
	TicketID uint
	// ActorUserID 决策人。
	ActorUserID uint
	// ActorIsAgent 调用方是否 Agent 身份。**为 true 时一律拒绝**（spec §2 第 5 条）。
	ActorIsAgent bool
	// Approve true=批准，false=驳回。
	Approve bool
	// Note 驳回理由（可选）。
	Note string
}

// Decide 人类批准/驳回。
func (s *ApprovalService) Decide(ctx context.Context, p DecideApprovalParams) (*model.ApprovalTicket, error) {
	// 最重要的一道：Agent **永久**不可决策。
	// 放在最前面而不是藏在某个权限判断里——即使 Token 拥有 approval.decide 节点，
	// 也必须在这里被拒。判据是身份，不是权限。
	if p.ActorIsAgent {
		return nil, ErrApprovalAgentCannotDecide
	}

	var t model.ApprovalTicket
	if err := s.db.First(&t, p.TicketID).Error; err != nil {
		return nil, ErrApprovalNotFound
	}
	if t.Status != model.ApprovalStatusPending {
		if t.Status == model.ApprovalStatusExpired || s.isExpired(&t) {
			return nil, ErrApprovalExpired
		}
		return nil, fmt.Errorf("%w: 当前状态 %s", ErrApprovalNotPending, t.Status)
	}
	if s.isExpired(&t) {
		_ = s.expireTicket(t.ID)
		return nil, ErrApprovalExpired
	}

	next := model.ApprovalStatusRejected
	if p.Approve {
		// 批准后进入 executing，由执行器认领。
		next = model.ApprovalStatusExecuting
	}
	now := s.now()

	res := s.db.WithContext(ctx).Model(&model.ApprovalTicket{}).
		Where("id = ? AND status = ?", p.TicketID, model.ApprovalStatusPending).
		Updates(map[string]any{
			"status":             next,
			"decided_by_user_id": p.ActorUserID,
			"decided_at":         now,
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		// 并发：别人已经决策过了。CAS 生效，只允许一次。
		return nil, ErrApprovalNotPending
	}
	return s.GetApproval(p.TicketID, nil)
}

// ── 执行器支撑─────────────────────────────────────────────────

// ClaimForExecution 认领票据并写 in_flight 回执（**同一事务**）。
//
// 与的 approveAndRun 三步同构：CAS 消费 → 写回执 → 提交后才执行。
func (s *ApprovalService) ClaimForExecution(ctx context.Context, ticketID uint) (uint, error) {
	var receiptID uint
	now := s.now()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// CAS 必须带 `consumed_at IS NULL`，否则这个「认领」不是原子操作。
		//
		// 为什么缺了它就失效：`Updates` 每次都写入新的 `now`（`s.now = time.Now`，
		// 两次调用必然不同），SQLite/MySQL 都会把这行算作「有变化」→ `RowsAffected`
		// 恒为 1。于是两个并发执行器都能认领成功，各自写一条 in_flight 回执、各执行一次
		// adapter——审批的「执行且仅执行一次」在最需要它的场景（并发重试）失效。
		// 加上 `consumed_at IS NULL` 之后，第二个认领者命中 0 行，得到 ErrApprovalNotPending。
		res := tx.Model(&model.ApprovalTicket{}).
			Where("id = ? AND status = ? AND consumed_at IS NULL", ticketID, model.ApprovalStatusExecuting).
			Updates(map[string]any{"consumed_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// 不是在 executing 状态，或已被别的执行器认领（consumed_at 非空）。
			return ErrApprovalNotPending
		}
		rec := &model.ApprovalReceipt{
			TicketID:  ticketID,
			Outcome:   model.ApprovalReceiptInFlight,
			StartedAt: now,
		}
		if err := tx.Create(rec).Error; err != nil {
			return err
		}
		receiptID = rec.ID
		return nil
	})
	return receiptID, err
}

// FinishExecution 收尾：写回执结果 + 票据终态。
func (s *ApprovalService) FinishExecution(ticketID, receiptID uint, execErr error) error {
	now := s.now()
	outcome := model.ApprovalReceiptOK
	code := ""
	msg := ""
	status := model.ApprovalStatusSucceeded
	if execErr != nil {
		outcome = model.ApprovalReceiptFailed
		code = "EXEC_FAILED"
		msg = truncateRunes(execErr.Error(), 500)
		status = model.ApprovalStatusFailed
		// 超时/连接类错误：可能已产生副作用，标 unverified 且**不自动重试**。
		if errors.Is(execErr, context.DeadlineExceeded) {
			outcome = model.ApprovalReceiptUnverified
			code = "TOOL_TIMEOUT"
		}
	}
	if err := s.db.Model(&model.ApprovalReceipt{}).Where("id = ?", receiptID).
		Updates(map[string]any{
			"outcome": outcome, "error_code": code,
			"error_message": msg, "finished_at": now,
		}).Error; err != nil {
		return err
	}
	updates := map[string]any{"status": status}
	if msg != "" {
		updates["failure_reason"] = msg
	}
	return s.db.Model(&model.ApprovalTicket{}).Where("id = ?", ticketID).Updates(updates).Error
}

// ExpireStale 巡检兜底：把超期未决策的 pending 置 expired（幂等）。
func (s *ApprovalService) ExpireStale() (int64, error) {
	now := s.now()
	res := s.db.Model(&model.ApprovalTicket{}).
		Where("status = ? AND expires_at < ?", model.ApprovalStatusPending, now).
		Updates(map[string]any{"status": model.ApprovalStatusExpired, "decided_at": now})
	return res.RowsAffected, res.Error
}

// StartExpiryLoop 启动到期巡检。
//
// 为什么需要它：读取时的惰性判定只覆盖「有人看」的票。
// 一张没人看的过期票会一直留在待审列表里，让批准人以为还有待办。
// 巡检负责把它们收口，与读取路径互为补充（两条都不够，缺一条就有残留）。
func (s *ApprovalService) StartExpiryLoop() {
	s.loopMu.Lock()
	if s.running {
		s.loopMu.Unlock()
		return
	}
	s.running = true
	s.stopCh = make(chan struct{})
	stop := s.stopCh
	s.loopMu.Unlock()

	go func() {
		// 5 分钟一次：许可期是 24 小时，巡检频率不必更高；
		// 但也不能更低——否则「昨天就该过期的票」会在列表里多躺一天。
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if n, err := s.ExpireStale(); err != nil {
					slog.Warn("审批到期巡检失败", "error", err)
				} else if n > 0 {
					slog.Info("审批到期巡检已收口过期票据", "count", n)
				}
			}
		}
	}()
	slog.Info("审批到期巡检已启动")
}

// Stop 停止到期巡检。
func (s *ApprovalService) Stop() {
	s.loopMu.Lock()
	defer s.loopMu.Unlock()
	if !s.running {
		return
	}
	s.running = false
	close(s.stopCh)
}

// isExpired 是否已超期（纯判定，不回写）。
func (s *ApprovalService) isExpired(t *model.ApprovalTicket) bool {
	return !t.ExpiresAt.IsZero() && s.now().After(t.ExpiresAt)
}

// expireTicket 把单张 pending 回写为 expired（CAS）。
func (s *ApprovalService) expireTicket(id uint) error {
	return s.db.Model(&model.ApprovalTicket{}).
		Where("id = ? AND status = ?", id, model.ApprovalStatusPending).
		Updates(map[string]any{
			"status": model.ApprovalStatusExpired, "decided_at": s.now(),
		}).Error
}
