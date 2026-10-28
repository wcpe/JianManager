package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/ai"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 用户委托执行器（FR-516 §5）。
//
// 核心矛盾（ADR-105）：既有 MCP 工具面是给「持有显式 scope 的自动化凭证」用的，
// 人类 JWT 不是 MCP 凭据。直接把 JWT 塞进去会让「模型说什么就做什么」变成提权面。
// 因此这里**不构造 AgentPrincipal、不复用 /api/v1/mcp**，而是新建一条用户委托链：
// 每轮执行前重新装载 UserAccess 并重跑完整权限判定，写操作一律走票据确认。
//
// 三条禁止事项（spec §5.2）：
//   - 不为人类用户签发 jmat_ Token；
//   - 不构造 (userID=0, isAdmin=true) 之类的隐式全平台身份；
//   - 不把「发现阶段按权限裁剪」当作授权——发现只是减噪，执行必重验。

const (
	// assistantConfirmTTL 确认票据有效期（spec §8：10 分钟）。
	// 与平台审批的 24 小时区分：聊天确认是「我现在就要执行」的即时意图。
	assistantConfirmTTL = 10 * time.Minute
	// assistantReceiptSummaryMax 回执摘要上限（spec §8：8 KiB）。
	assistantReceiptSummaryMax = 8 * 1024
	// assistantToolTimeout 单次工具执行超时（spec §8：30 秒）。
	assistantToolTimeout = 30 * time.Second
)

// assistantToolOutcome 一次工具执行的结局。
type assistantToolOutcome struct {
	// Result 回灌给模型的内容（成功时）。
	Result string
	// Err 执行错误（失败时）。
	Err error
	// PendingConfirmation 非空表示「需要用户确认」，本轮不执行。
	// 执行器会据此把 run 置为 waiting_confirmation 并结束这一轮。
	PendingConfirmation *assistantConfirmationView
}

// assistantConfirmationView 给前端核对的票据详情（不含内部指纹）。
type assistantConfirmationView struct {
	ID         uint            `json:"id"`
	ToolName   string          `json:"toolName"`
	Operation  string          `json:"operation"`
	Args       json.RawMessage `json:"args"`
	TargetType string          `json:"targetType"`
	TargetID   string          `json:"targetId"`
	ExpiresAt  time.Time       `json:"expiresAt"`
	Risk       string          `json:"risk"`
}

// assistantDelegatedExecutor 实现 ToolExecutor，但**不直接执行写操作**：
// 写操作先出票、等用户确认，确认后由 `DecideConfirmation` 触发真正的执行。
type assistantDelegatedExecutor struct {
	svc *AssistantChatService
	// run / owner 在构造时绑定：执行器的生命周期就是一轮 run。
	runID          uint
	conversationID uint
	owner          uint
	// sink 用于把工具状态变化推给前端（tool_status / tool_result 事件）。
	sink *assistantEventSink
}

// Available 返回本轮要下发给模型的工具（渐进式披露，FR-544）。
//
// ## 为什么只下发三个元工具
//
// 平台 MCP 目录有 108 个工具，全量 schema 约 45 KB（≈11k tokens）。把它整包塞进
// 每一轮上下文，等于「用户打招呼也要为 108 个工具的 JSON Schema 付费」。
// 因此这里只给固定的三件套：
//
//	search_tools  → 看到有哪些工具（名字 + 一句话用途）
//	describe_tool → 取某个工具的完整参数 schema
//	call_tool     → 真正调用（内部走 MCP 执行路径，审批/权限口径完全一致）
//
// 常驻体积从此与工具总数**无关**：MCP 加到 200 个工具，这里仍是三件套。
//
// ## 为什么元工具也要看权限
//
// 一个没有任何平台权限的用户，连 search_tools 都不该看到——否则他会反复搜索、
// 反复拿到「无权」的失败，而失败原因不是模型能处理的。因此先判断该用户
// 是否派生得出任何能力（与执行期同一套派生逻辑，不另造判定）。
func (e *assistantDelegatedExecutor) Available(_ context.Context, owner uint) []ai.ToolSpec {
	access, err := e.svc.authz().LoadUserAccess(owner)
	if err != nil {
		// 装不出权限就给空目录：宁可模型「没有工具可用」，也不能凭猜测放行。
		return nil
	}
	// 平台自带工具开关（FR-540 §4.3）：关闭时不提供任何元工具。
	// 元工具是自带工具的唯一入口，关掉它等于关掉全部平台工具。
	if !e.svc.builtinToolsEnabled() {
		return nil
	}
	out := make([]ai.ToolSpec, 0, 4)
	// 三件套对「有任意平台能力」的用户开放。权限的具体判定发生在执行期
	// （call_tool → MCP Authorize），这里只做「有没有东西可探索」的粗筛。
	if principal, perr := AssistantPrincipalForOwner(e.svc.authz(), owner); perr == nil && principal != nil {
		out = append(out, assistantMetaToolSpecs()...)
	}
	// 外部 MCP 连接的工具仍按其自身策略下发（它们有独立的风险/启停模型）。
	out = append(out, e.svc.externalToolSpecs(access)...)
	return out
}

// Execute 执行一次工具调用。
//
// 读：直接执行。
// 写：建票据 → 返回 PendingConfirmation（调用方据此进入 waiting_confirmation），**不执行**。
func (e *assistantDelegatedExecutor) Execute(ctx context.Context, owner uint, call ai.ToolCall) (string, error) {
	outcome := e.execute(ctx, owner, call)
	if outcome.PendingConfirmation != nil {
		// 用哨兵错误让 run loop 知道「本轮到此为止，等确认」。
		// 不用返回值表示，是因为 loop 会把返回值当工具结果回灌给模型。
		return "", &assistantConfirmationPendingError{view: outcome.PendingConfirmation}
	}
	return outcome.Result, outcome.Err
}

// assistantConfirmationPendingError 表示「已出票、等待用户确认」。
type assistantConfirmationPendingError struct {
	view *assistantConfirmationView
}

func (e *assistantConfirmationPendingError) Error() string {
	return "该操作需要用户确认后才会执行"
}

// execute 统一入口：解析参数 → 查登记 → 风险分类 → 重验 → 执行/出票。
func (e *assistantDelegatedExecutor) execute(ctx context.Context, owner uint, call ai.ToolCall) assistantToolOutcome {
	// 渐进式披露的三件套（FR-544）先于其它判定：它们不是业务工具，
	// 没有目标资源可校验，自身的权限边界由内部逻辑保证。
	if outcome, handled := e.executeMetaTool(ctx, owner, call); handled {
		return outcome
	}

	// 外部 MCP 工具（conn.<slug>.<remote>）走独立分派：它的风险分类来自工具行、
	// 目标由上游解释，与内置 action 的判定路径不同（FR-517 闭环）。
	if isAssistantExternalTool(call.ToolName) {
		argsExt, errExt := normalizeToolArgs(call.Args)
		if errExt != nil {
			return assistantToolOutcome{Err: fmt.Errorf("%w: %v", ErrAssistantInvalid, errExt)}
		}
		accessExt, errAcc := e.svc.authz().LoadUserAccess(owner)
		if errAcc != nil {
			return assistantToolOutcome{Err: fmt.Errorf("%w: 无法确认当前权限", ErrAssistantForbidden)}
		}
		return e.executeExternalTool(ctx, accessExt, call, argsExt)
	}

	// 业务工具**不再有本地执行分支**（FR-544）：它们经 call_tool 转给 MCP 的
	// CallTool 执行，审批/权限/scope/精确确认由 MCP 保证。走到这里说明调用方
	// 直接点了业务工具名，而它已不在常驻目录里——如实拒绝并指路，不要静默执行。
	return assistantToolOutcome{Err: fmt.Errorf(
		"%w: 工具 %q 不在常驻目录中，请先用 %s 搜索、%s 取参数后用 %s 调用",
		ErrAssistantInvalid, call.ToolName,
		assistantMetaToolSearch, assistantMetaToolDescribe, assistantMetaToolCall)}
}

func (e *assistantDelegatedExecutor) resolveToolTarget(
	access *UserAccess, tool *AssistantToolAction, args map[string]any,
) (string, string, error) {
	switch tool.ResourceType {
	case "", "platform", AgentResourceNone:
		// `none` 是目录里表示「该动作没有资源目标」的值（如 whoami / 概览类）。
		// 漏掉它会让所有无目标工具在上游就被判「不支持的目标类型」——
		// 表现在界面上是「工具全都失败」，而根因在这里一行判定。
		return "", "", nil
	case "instance":
		id := uintArg(args, "instanceId", 0)
		if id == 0 {
			// 没有明确目标时按「无特定目标」处理：具体归属由后续领域调用再判。
			return "instance", "", nil
		}
		ok, err := e.svc.authz().CanAccessInstance(access, id)
		if err != nil {
			return "", "", err
		}
		if !ok {
			return "", "", fmt.Errorf("%w: 实例不可访问", ErrAssistantForbidden)
		}
		return "instance", fmt.Sprintf("%d", id), nil
	case "node":
		id := uintArg(args, "nodeId", 0)
		if id == 0 {
			return "node", "", nil
		}
		var node model.Node
		if err := e.svc.db.First(&node, id).Error; err != nil {
			return "", "", fmt.Errorf("%w: 节点不存在", ErrAssistantForbidden)
		}
		if !access.HasNode(node.UUID) {
			return "", "", fmt.Errorf("%w: 节点不可访问", ErrAssistantForbidden)
		}
		return "node", fmt.Sprintf("%d", id), nil
	default:
		// 未知资源类型：不回落到「放行」，而是明确拒绝。
		return "", "", fmt.Errorf("%w: 不支持的目标类型 %s", ErrAssistantInvalid, tool.ResourceType)
	}
}

// issueConfirmation 为写操作出票（spec §6.1）。
func (e *assistantDelegatedExecutor) issueConfirmation(
	access *UserAccess, tool *AssistantToolAction, call ai.ToolCall, args map[string]any,
	targetType, targetID string,
) (*assistantConfirmationView, error) {
	frozen, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("冻结参数失败: %w", err)
	}
	argsHash := hashToolArgs(args)
	targetFP := targetType + ":" + targetID

	ticket := &model.AssistantConfirmation{
		Source:            model.AssistantConfirmSourceChat,
		OwnerUserID:       e.owner,
		ConversationID:    e.conversationID,
		RunID:             e.runID,
		CallID:            call.CallID,
		ToolName:          tool.Name,
		SchemaFingerprint: assistantToolSchemaVersion,
		PolicyVersion:     assistantToolPolicyVersion,
		ArgsHash:          argsHash,
		ArgsFrozen:        string(frozen),
		AuthVersion:       access.AuthVersion,
		Policy:            model.AssistantPolicySelfConfirm,
		TargetFingerprint: targetFP,
		Status:            model.AssistantConfirmStatusPending,
		ExpiresAt:         time.Now().Add(assistantConfirmTTL),
	}

	// `(run_id, call_id)` 唯一：同一调用不重复出票。
	// 用 FirstOrCreate 语义而不是直接 Create：模型重试同一调用时不该刷出第二张票。
	err = e.svc.db.Where("run_id = ? AND call_id = ?", e.runID, call.CallID).FirstOrCreate(ticket).Error
	if err != nil {
		if isUniqueViolation(err) {
			// 并发下另一个 goroutine 已出票：回读它。
			var existing model.AssistantConfirmation
			if e2 := e.svc.db.Where("run_id = ? AND call_id = ?", e.runID, call.CallID).
				First(&existing).Error; e2 == nil {
				return confirmationViewOf(&existing, tool), nil
			}
			// 回读不到，说明撞上的是**另一个**约束：同 run 单张 pending
			// （`ux_assistant_confirmations_pending`）。这不是并发同一调用，
			// 而是同一工具轮次里出现了第二个写调用。
			return nil, ErrAssistantConfirmPendingExists
		}
		return nil, fmt.Errorf("创建确认票据失败: %w", err)
	}
	return confirmationViewOf(ticket, tool), nil
}

// confirmationViewOf 构造给前端核对的视图。
func confirmationViewOf(t *model.AssistantConfirmation, tool *AssistantToolAction) *assistantConfirmationView {
	risk := "write"
	if tool != nil && tool.Operation == "destructive" {
		risk = "destructive"
	}
	targetType, targetID := "", ""
	if t.TargetFingerprint != "" {
		if i := strings.Index(t.TargetFingerprint, ":"); i >= 0 {
			targetType = t.TargetFingerprint[:i]
			targetID = t.TargetFingerprint[i+1:]
		}
	}
	return &assistantConfirmationView{
		ID:         t.ID,
		ToolName:   t.ToolName,
		Operation:  risk,
		Args:       json.RawMessage(t.ArgsFrozen),
		TargetType: targetType,
		TargetID:   targetID,
		ExpiresAt:  t.ExpiresAt,
		Risk:       risk,
	}
}

// recordReceipt 写回执（默认以当前时刻为开始）。
func (e *assistantDelegatedExecutor) recordReceipt(
	confirmationID *uint, call ai.ToolCall, outcome, code, message, summary string,
) {
	e.recordReceiptAt(confirmationID, call, outcome, code, message, summary, time.Now())
}

// recordReceiptAt 写回执。
//
// 只读也写（confirmationID 为 nil）：否则审计里串不起只读调用。
func (e *assistantDelegatedExecutor) recordReceiptAt(
	confirmationID *uint, call ai.ToolCall, outcome, code, message, summary string, started time.Time,
) {
	rec := &model.AssistantReceipt{
		ConfirmationID: confirmationID,
		RunID:          e.runID,
		CallID:         call.CallID,
		ToolName:       call.ToolName,
		Outcome:        outcome,
		ErrorCode:      truncateRunes(code, 60),
		ErrorMessage:   truncateRunes(message, 500),
		ResultSummary:  truncateRunes(summary, assistantReceiptSummaryMax),
		StartedAt:      started,
	}
	if outcome != model.AssistantReceiptInFlight {
		now := time.Now()
		rec.FinishedAt = &now
	}
	if err := e.svc.db.Create(rec).Error; err != nil {
		// 回执写失败不该打断执行——但它会被审计缺口的告警照出来。
		_ = err
	}
}

// normalizeToolArgs 把模型给的参数解析成 map，并做**键排序的规范化**。
//
// 规范化是 args_hash 的前提：键序不同但内容相同的两个参数必须得到同一个哈希，
// 否则模型换个写法就能绕过「参数未变」的判定，让旧票据意外匹配或意外失配。
func normalizeToolArgs(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("参数不是合法 JSON 对象")
	}
	if m == nil {
		return map[string]any{}, nil
	}
	return m, nil
}

// hashToolArgs 规范化参数的 SHA-256（键排序后序列化）。
func hashToolArgs(args map[string]any) string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		vb, err := json.Marshal(args[k])
		if err != nil {
			vb = []byte("null")
		}
		b.Write(vb)
	}
	b.WriteByte('}')
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// ── 参数读取小工具（容忍模型给的类型抖动） ──────────────────────────

func stringArg(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func intArg(args map[string]any, key string, dflt int) int {
	v, ok := args[key]
	if !ok || v == nil {
		return dflt
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		var out int
		if _, err := fmt.Sscanf(n, "%d", &out); err == nil {
			return out
		}
	}
	return dflt
}

func uintArg(args map[string]any, key string, dflt uint) uint {
	n := intArg(args, key, int(dflt))
	if n < 0 {
		return dflt
	}
	return uint(n)
}

// ── 确认决策（用户侧） ────────────────────────────────────────────

// AssistantConfirmationDecision 用户对票据的决定。
type AssistantConfirmationDecision struct {
	Decision string `json:"decision"` // approve / reject（只这两个值）
}

// DecideConfirmation 处理用户对一张票据的批准/拒绝（spec §7）。
//
// 严格只接受 approve / reject：**不接受新参数或新目标**（防止「确认即改参」）。
func (s *AssistantChatService) DecideConfirmation(
	ctx context.Context, owner, confirmationID uint, decision string,
) (*model.AssistantConfirmation, error) {
	decision = strings.ToLower(strings.TrimSpace(decision))
	if decision != "approve" && decision != "reject" {
		return nil, fmt.Errorf("%w: decision 只能是 approve 或 reject", ErrAssistantInvalid)
	}

	var ticket model.AssistantConfirmation
	if err := s.db.WithContext(ctx).First(&ticket, confirmationID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: 确认票据不存在", ErrAssistantChatNotFound)
		}
		return nil, err
	}
	// 仅所有者：别人（哪怕是平台管理员）不能替我确认。
	if ticket.OwnerUserID != owner {
		return nil, fmt.Errorf("%w: 只能处理自己的确认", ErrAssistantForbidden)
	}

	now := time.Now()
	if ticket.Status != model.AssistantConfirmStatusPending {
		switch ticket.Status {
		case model.AssistantConfirmStatusConsumed:
			return nil, fmt.Errorf("%w: 该确认已被执行过", ErrAssistantConfirmConsumed)
		case model.AssistantConfirmStatusRejected:
			return nil, fmt.Errorf("%w: 该确认已被拒绝", ErrAssistantConfirmStale)
		case model.AssistantConfirmStatusExpired:
			return nil, fmt.Errorf("%w: 该确认已过期", ErrAssistantConfirmExpired)
		case model.AssistantConfirmStatusInvalidated:
			return nil, fmt.Errorf("%w: 该确认已失效（参数或权限已变）", ErrAssistantConfirmStale)
		}
	}
	if now.After(ticket.ExpiresAt) {
		_ = s.db.Model(&model.AssistantConfirmation{}).
			Where("id = ? AND status = ?", ticket.ID, model.AssistantConfirmStatusPending).
			Updates(map[string]any{"status": model.AssistantConfirmStatusExpired, "decided_at": now}).Error
		return nil, fmt.Errorf("%w: 确认已超过有效期", ErrAssistantConfirmExpired)
	}

	if decision == "reject" {
		// 保留票据记录（审计需要），仅标 rejected。
		if err := s.db.Model(&model.AssistantConfirmation{}).
			Where("id = ? AND status = ?", ticket.ID, model.AssistantConfirmStatusPending).
			Updates(map[string]any{"status": model.AssistantConfirmStatusRejected, "decided_at": now}).Error; err != nil {
			return nil, err
		}
		s.writeToolRejectedEvent(&ticket)
		return &ticket, nil
	}

	// 批准 → 立即尝试执行（CAS 消费 + in_flight 回执 + 调领域 service）。
	return s.approveAndRun(ctx, owner, &ticket)
}

// approveAndRun 批准后执行（spec §6.2 的三步同一事务）。
func (s *AssistantChatService) approveAndRun(
	ctx context.Context, owner uint, ticket *model.AssistantConfirmation,
) (*model.AssistantConfirmation, error) {
	tool := findAssistantTool(ticket.ToolName)
	if tool == nil && !isAssistantExternalTool(ticket.ToolName) {
		// 内置工具不存在 = 已被移除；外部工具的登记在另一张表，由
		// executeApprovedExternalTool 自行校验，故这里只拦内置。
		return nil, fmt.Errorf("%w: 工具已不可用", ErrAssistantConfirmStale)
	}

	// **执行前重验权限**（§6.3：AuthVersion 不覆盖角色模板改权/绑定变更/组成员移除）。
	access, err := s.authz().LoadUserAccess(owner)
	if err != nil {
		return nil, fmt.Errorf("%w: 无法确认当前权限", ErrAssistantForbidden)
	}
	if tool != nil && tool.Capability != "" && !access.Can(tool.Capability) {
		s.invalidateTicket(ticket.ID, "权限已变更")
		return nil, fmt.Errorf("%w: 权限已变更，确认失效", ErrAssistantConfirmStale)
	}

	// 参数哈希与目标指纹必须仍然一致（防「确认即改参」）。
	var frozen map[string]any
	if err := json.Unmarshal([]byte(ticket.ArgsFrozen), &frozen); err != nil {
		return nil, fmt.Errorf("%w: 票据参数已损坏", ErrAssistantConfirmStale)
	}
	if hashToolArgs(frozen) != ticket.ArgsHash {
		s.invalidateTicket(ticket.ID, "参数已变更")
		return nil, fmt.Errorf("%w: 参数已变更，确认失效", ErrAssistantConfirmStale)
	}

	now := time.Now()
	var receiptID uint

	// ① CAS 消费 + ② 创建 in_flight 回执 + 置票据 consumed ——**同一事务**。
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// CAS：只有 pending/approved 且未消费的票据能被消费。
		res := tx.Model(&model.AssistantConfirmation{}).
			Where("id = ? AND status = ? AND consumed_at IS NULL", ticket.ID, model.AssistantConfirmStatusPending).
			Updates(map[string]any{
				"status":      model.AssistantConfirmStatusConsumed,
				"decided_at":  now,
				"consumed_at": now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// 已被别人消费（重复 approve）：CAS 生效，只执行一次。
			return fmt.Errorf("%w: 该确认已被执行过", ErrAssistantConfirmConsumed)
		}
		rec := &model.AssistantReceipt{
			ConfirmationID: &ticket.ID,
			RunID:          ticket.RunID,
			CallID:         ticket.CallID,
			ToolName:       ticket.ToolName,
			Outcome:        model.AssistantReceiptInFlight,
			StartedAt:      now,
		}
		if err := tx.Create(rec).Error; err != nil {
			return err
		}
		receiptID = rec.ID
		return nil
	})
	if err != nil {
		return nil, err
	}

	// ③ 事务提交后才真正调用领域 service。
	execCtx, cancel := context.WithTimeout(ctx, assistantToolTimeout)
	defer cancel()
	var execErr error
	// 判据必须用**票据里的工具名**，不能解引用 tool。
	//
	// `findAssistantTool` 只查内置注册表，对外部工具名（`conn.<slug>.<tool>`）**必为 nil**。
	// 这里曾经写成 `isAssistantExternalTool(tool.Name)`，于是任何外部写工具一批准就
	// 空指针 panic（500 空体）：票据已被 CAS 消费、回执卡在 in_flight、run 永久停在
	// waiting_confirmation，用户既拿不到结果也无法重来（再批只会得到 409 已消费）。
	// 换言之 FR-517 的写侧闭环 100% 不可用。
	if isAssistantExternalTool(ticket.ToolName) {
		execErr = s.executeApprovedExternalTool(execCtx, access, ticket, frozen)
	} else {
		// 内置/平台工具：**转给 MCP 执行**（FR-544 单一执行路径）。
		//
		// 走到这里说明用户已在助手侧确认过、参数哈希与目标指纹都核对过。
		// 但执行仍交给 MCP 的 CallTool —— 平台的写操作只应有一条实现路径，
		// 否则助手这条路会慢慢漏掉 MCP 侧的检查（历史上正是这样漂移的）。
		//
		// 注意：MCP 侧自身的高危审批仍然会在这一层触发。也就是说用户会经历
		// 「助手确认 → 平台审批」两道，这是**有意的双闸**，不是重复。
		execFn := assistantToolExecutor()
		if execFn == nil {
			execErr = fmt.Errorf("%w: 工具执行入口未装配", ErrAssistantInvalid)
		} else {
			_, execErr = execFn(execCtx, ticket.OwnerUserID, ticket.ToolName, frozen)
		}
	}

	finish := time.Now()
	if execErr != nil {
		// 超时/连接类错误 → unverified（已可能产生副作用，不得重放）。
		outcome := model.AssistantReceiptFailed
		code := "TOOL_FAILED"
		if errors.Is(execErr, context.DeadlineExceeded) || errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			outcome = model.AssistantReceiptUnverified
			code = "TOOL_TIMEOUT"
		}
		if err := s.db.Model(&model.AssistantReceipt{}).Where("id = ?", receiptID).
			Updates(map[string]any{
				"outcome":       outcome,
				"error_code":    code,
				"error_message": truncateRunes(execErr.Error(), 500),
				"finished_at":   finish,
			}).Error; err != nil {
			_ = err
		}
		// 失败也留审计：审计要能回答「助手尝试过什么、为什么没成」。
		// 只记成功的写，会让「模型反复尝试但一直被拒」这类行为在审计里完全隐形。
		s.auditToolExecution(ticket, false, execErr.Error())
		// 票据**已消费**、动作**已尝试**——这不是「票据无效」，而是「执行失败」。
		// 用独立包装回给上层，让 HTTP 层能给出「已执行但失败」的语义（502 + 原因），
		// 而不是笼统的 500 INTERNAL_ERROR：那既误导用户（像是平台 bug），
		// 也让调用方以为可以原样重试（实际票据已消费，重试只会得到 409）。
		return nil, &AssistantToolExecError{Err: execErr}
	}

	if err := s.db.Model(&model.AssistantReceipt{}).Where("id = ?", receiptID).
		Updates(map[string]any{"outcome": model.AssistantReceiptOK, "finished_at": finish}).Error; err != nil {
		_ = err
	}
	s.auditToolExecution(ticket, true, "")

	var updated model.AssistantConfirmation
	_ = s.db.First(&updated, ticket.ID).Error
	return &updated, nil
}

// auditToolExecution 把一次**已批准并执行过**的写操作写进平台审计（FR-516 §9）。
//
// 为什么写在这里而不是 router 层：执行发生在确认端点内部，runId/callId 只有这一层有；
// router 拿到的是「某张票据被批准」，拼不出「哪一轮助手的哪个工具调用」。
//
// Action 用 `assistant.tool.<动作名>` 前缀：审计页按 action 过滤时能一眼区分
// 「用户直接点的启停」与「助手代办的启停」——这两件事的追责路径不同。
//
// IP 记空串：执行发生在后台/确认请求内，service 层拿不到客户端 IP。
// 刻意不强塞一个占位值（如 "assistant"）——那会让人误以为是真实来源地址。
// 需要定位 IP 时可经 runId 关联到确认请求的访问日志。
func (s *AssistantChatService) auditToolExecution(
	ticket *model.AssistantConfirmation, success bool, errMsg string,
) {
	targetType, targetID := "", ""
	if ticket.TargetFingerprint != "" {
		if i := strings.Index(ticket.TargetFingerprint, ":"); i >= 0 {
			targetType = ticket.TargetFingerprint[:i]
			targetID = ticket.TargetFingerprint[i+1:]
		}
	}
	detail, _ := json.Marshal(map[string]any{
		"tool":           ticket.ToolName,
		"policy":         ticket.Policy,
		"confirmationId": ticket.ID,
		// 参数摘要而非原文：审计详情会被更多人看到，工具参数可能含敏感值
		// （如文件路径、命令内容）。原文在票据的 args_frozen 里，需要时按 id 取。
		"argsHash": ticket.ArgsHash,
	})
	convID, runID := ticket.ConversationID, ticket.RunID
	s.audit().RecordCorrelatedSafe(
		ticket.OwnerUserID,
		"assistant.tool."+ticket.ToolName,
		targetType, targetID,
		string(detail), "",
		success, errMsg,
		AuditCorrelation{ConversationID: &convID, RunID: &runID, CallID: ticket.CallID},
	)
}

// invalidateTicket 把票据标为失效（权限/参数变化）。
func (s *AssistantChatService) invalidateTicket(id uint, _ string) {
	now := time.Now()
	_ = s.db.Model(&model.AssistantConfirmation{}).
		Where("id = ? AND status = ?", id, model.AssistantConfirmStatusPending).
		Updates(map[string]any{"status": model.AssistantConfirmStatusInvalidated, "decided_at": now}).Error
}

// writeToolRejectedEvent 拒绝时补一条 tool_status 事件，让流里有记录。
func (s *AssistantChatService) writeToolRejectedEvent(ticket *model.AssistantConfirmation) {
	sink := newAssistantEventSink(s.db, &model.AssistantRun{ID: ticket.RunID, ConversationID: ticket.ConversationID}, s.limits)
	_ = sink.append(model.AssistantEventTypeToolStatus, map[string]any{
		"callId": ticket.CallID,
		"status": "rejected",
		"tool":   ticket.ToolName,
	}, nil, &ticket.CallID)
}

// ListPendingConfirmations 某一轮 run 的待确认票据（spec §7，仅所有者）。
func (s *AssistantChatService) ListPendingConfirmations(owner, runID uint) ([]assistantConfirmationView, error) {
	var run model.AssistantRun
	if err := s.db.First(&run, runID).Error; err != nil {
		return nil, fmt.Errorf("%w: 运行不存在", ErrAssistantChatNotFound)
	}
	if run.OwnerUserID != owner {
		return nil, fmt.Errorf("%w: 只能查看自己的确认", ErrAssistantForbidden)
	}
	var tickets []model.AssistantConfirmation
	if err := s.db.Where("run_id = ? AND status = ?", runID, model.AssistantConfirmStatusPending).
		Order("id ASC").Find(&tickets).Error; err != nil {
		return nil, err
	}
	out := make([]assistantConfirmationView, 0, len(tickets))
	for i := range tickets {
		out = append(out, *confirmationViewOf(&tickets[i], findAssistantTool(tickets[i].ToolName)))
	}
	return out, nil
}

// GetConfirmation 票据详情（仅所有者）。
func (s *AssistantChatService) GetConfirmation(owner, id uint) (*assistantConfirmationView, error) {
	var ticket model.AssistantConfirmation
	if err := s.db.First(&ticket, id).Error; err != nil {
		return nil, fmt.Errorf("%w: 确认票据不存在", ErrAssistantChatNotFound)
	}
	if ticket.OwnerUserID != owner {
		return nil, fmt.Errorf("%w: 只能查看自己的确认", ErrAssistantForbidden)
	}
	v := confirmationViewOf(&ticket, findAssistantTool(ticket.ToolName))
	return v, nil
}

// ExpireStaleConfirmations 把过期票据标记为 expired（可被定时调用）。
//
// 不依赖「读的时候顺手判过期」：那样在没人读时会一直挂 pending，
// 列表与审计都会显示「等待确认」而实际早已无效。
func (s *AssistantChatService) ExpireStaleConfirmations(now time.Time) (int64, error) {
	res := s.db.Model(&model.AssistantConfirmation{}).
		Where("status = ? AND expires_at < ?", model.AssistantConfirmStatusPending, now).
		Updates(map[string]any{"status": model.AssistantConfirmStatusExpired})
	return res.RowsAffected, res.Error
}

// RecoverInFlightReceipts 启动时把遗留的 in_flight 回执标为 unverified（§6.2 崩溃恢复）。
//
// 语义：已发出写调用但未取得确定结果 → **不得重放、不得当成功**，只能人工核实。
func (s *AssistantChatService) RecoverInFlightReceipts() (int64, error) {
	now := time.Now()
	res := s.db.Model(&model.AssistantReceipt{}).
		Where("outcome = ?", model.AssistantReceiptInFlight).
		Updates(map[string]any{
			"outcome":       model.AssistantReceiptUnverified,
			"error_code":    "PROCESS_RESTART",
			"error_message": "进程重启时该调用仍在执行中，结果未知",
			"finished_at":   now,
		})
	return res.RowsAffected, res.Error
}

// executeMetaTool 执行渐进式披露的三件套（FR-544）。
//
// handled=false 表示「不是元工具」，交回原分发路径。
//
// ## 为什么元工具本身不做确认
//
// search / describe 是纯读（只读工具目录，不碰业务数据）。
// call_tool 的**写操作**仍会经过完整确认：它在内部转交给 MCP 执行路径，
// 因此写类工具的「逐次确认 / 平台审批」一个都不会少。
// 在这里再加一层确认只会变成「确认确认」，多一次点击而不增加安全性。
func (e *assistantDelegatedExecutor) executeMetaTool(ctx context.Context, owner uint, call ai.ToolCall) (assistantToolOutcome, bool) {
	switch call.ToolName {
	case assistantMetaToolSearch:
		return assistantToolOutcome{Result: e.metaSearchTools(call.Args)}, true
	case assistantMetaToolDescribe:
		return assistantToolOutcome{Result: e.metaDescribeTool(call.Args)}, true
	case assistantMetaToolCall:
		return e.metaCallTool(ctx, owner, call), true
	default:
		return assistantToolOutcome{}, false
	}
}

// metaSearchTools 返回匹配的工具清单（名字 + 用途 + 操作类型）。
//
// 输出**不含参数 schema**：那正是下一步（describe_tool）要按需取的东西。
// 这里若顺手带上 schema，就等于把渐进式披露退化成一次性全量下发。
func (e *assistantDelegatedExecutor) metaSearchTools(rawArgs string) string {
	args, err := normalizeToolArgs(rawArgs)
	if err != nil {
		return metaToolError("参数不是合法 JSON 对象")
	}
	keyword, _ := args["keyword"].(string)
	category, _ := args["category"].(string)

	catalog := assistantToolCatalog()
	if len(catalog) == 0 {
		// 目录未注入（mcp 包未装配）：如实说不可用，而不是回「没有工具」——
		// 后者会让模型以为平台真的没有能力，转而去编造。
		return metaToolError("平台工具目录不可用（MCP 未装配）")
	}
	matched := searchAssistantTools(catalog, keyword, category)
	if len(matched) == 0 {
		// 空结果要给出「怎么再试」，否则模型通常直接放弃使用工具。
		return fmt.Sprintf("没有匹配 keyword=%q category=%q 的工具。可换关键词（例如「实例」「日志」「配置」「文件」）或省略 category 再试。共 %d 个工具可用。",
			keyword, category, len(catalog))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "共 %d 个匹配（先 describe_tool 取参数定义，再 call_tool 调用）：\n", len(matched))
	for _, d := range matched {
		fmt.Fprintf(&b, "- %s [%s]%s：%s\n", d.Name, d.Operation, confirmMark(d.RequiresConfirm), d.Summary)
	}
	return strings.TrimRight(b.String(), "\n")
}

// confirmMark 标出「调用前会要求用户确认」。
//
// 让模型提前知道这件事很重要：它可以在提议前先告知用户「这一步需要你点确认」，
// 而不是发出调用后才暴露。
func confirmMark(need bool) string {
	if need {
		return "（需用户确认）"
	}
	return ""
}

// metaDescribeTool 返回单个工具的完整定义。
func (e *assistantDelegatedExecutor) metaDescribeTool(rawArgs string) string {
	args, err := normalizeToolArgs(rawArgs)
	if err != nil {
		return metaToolError("参数不是合法 JSON 对象")
	}
	name, _ := args["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return metaToolError("缺少 name")
	}
	d, ok := assistantToolDetail(name)
	if !ok {
		return metaToolError(fmt.Sprintf("工具 %q 不存在。先用 %s 搜索。", name, assistantMetaToolSearch))
	}
	// schema 原样序列化返回：二次加工会丢掉 anyOf/oneOf 这类结构，反而误导模型。
	blob, merr := json.Marshal(d.Parameters)
	if merr != nil {
		return metaToolError("无法序列化参数定义")
	}
	return fmt.Sprintf("工具：%s\n操作类型：%s%s\n用途：%s\n参数定义（JSON Schema）：\n%s",
		d.Name, d.Operation, confirmMark(d.RequiresConfirm), d.Summary, string(blob))
}

// metaCallTool 执行一次真实工具调用；**写操作先出助手侧确认票，不直接下发**。
//
// ## 为什么在这里拦（双闸）
//
// MCP 侧自己有一道闸（高危工具走平台审批、destructive 走精确确认）。但那是
// 「Agent Token 视角」的闸：它防的是无人监管的 Agent 自我批准。助手的用户可以
// 在对话里被模型说服、顺手点一下，和 Agent 的风险模型不同——所以助手侧保留
// 自己的逐次确认，形成**双闸**：
//
//	助手确认（人在对话里看清这一步要做什么）
//	  → MCP 审批/精确确认（平台层的第二道，按既有策略）
//	    → 真正执行
//
// 位置刻意选在「调用 MCP 之前」：与 Agent 客户端在 CallTool 前拦截完全同构，
// 两处都在**副作用发生前**停下来，谁也不依赖对方的实现细节。
//
// ## 读操作不拦
//
// 只读自动执行是既有不变量（FR-516 §4）。若读也要确认，用户会被确认卡淹没，
// 最终闭眼点同意——那比不做确认更危险。
func (e *assistantDelegatedExecutor) metaCallTool(ctx context.Context, owner uint, call ai.ToolCall) assistantToolOutcome {
	// 注意：执行入口的可用性**必须在写判定的出票之后**再检查。
	// 出票只依赖目录元数据与数据库，不依赖执行入口；提前返回会让「写操作先确认」
	// 这条不变量在「执行器未装配」的部署里被跳过——而那时恰恰更需要确认。
	args, err := normalizeToolArgs(call.Args)
	if err != nil {
		return assistantToolOutcome{Result: metaToolError("参数不是合法 JSON 对象")}
	}
	name, _ := args["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return assistantToolOutcome{Result: metaToolError("缺少 name")}
	}
	// 元工具自身不可再被调用：否则会递归成无限套娃，且掩盖真实意图。
	for _, meta := range assistantMetaToolNames() {
		if name == meta {
			return assistantToolOutcome{Result: metaToolError("不能通过 call_tool 调用元工具本身")}
		}
	}
	inner := map[string]any{}
	if raw, ok := args["arguments"]; ok && raw != nil {
		obj, ok2 := raw.(map[string]any)
		if !ok2 {
			return assistantToolOutcome{Result: metaToolError("arguments 必须是对象")}
		}
		inner = obj
	}

	// 取目标工具的元数据（单一真源 = MCP 目录）。取不到就直接拒绝：
	// 无法判定读写属性的工具**不能**先执行再补确认——那时后果已经发生了。
	tool, ok := assistantToolMeta(name)
	if !ok {
		return assistantToolOutcome{Result: metaToolError(
			fmt.Sprintf("工具 %q 不在平台目录中。先用 %s 搜索。", name, assistantMetaToolSearch))}
	}

	// 写类：出确认票，本轮到此为止（与既有写路径同一套票据与视图）。
	if tool.isWrite() {
		access, aerr := e.svc.authz().LoadUserAccess(owner)
		if aerr != nil {
			return assistantToolOutcome{Err: fmt.Errorf("%w: 无法确认当前权限", ErrAssistantForbidden)}
		}
		// 权限重验在出票前完成：没有权限的请求不该变成一张等人批准的票。
		if tool.Capability != "" && !access.Can(tool.Capability) {
			return assistantToolOutcome{Err: fmt.Errorf("%w: 缺少能力 %s", ErrAssistantForbidden, tool.Capability)}
		}
		targetType, targetID := assistantToolTargetOf(tool, inner)
		view, ierr := e.issueConfirmation(access, tool, call, inner, targetType, targetID)
		if ierr != nil {
			// 出票失败也要留回执：否则审计里这次调用凭空消失，
			// 而模型已经收到了一个错误结果——两边对不上。
			code := "TICKET_FAILED"
			if errors.Is(ierr, ErrAssistantConfirmPendingExists) {
				code = "PENDING_EXISTS"
			}
			e.recordReceipt(nil, call, model.AssistantReceiptFailed, code, ierr.Error(), "")
			return assistantToolOutcome{Err: ierr}
		}
		// 带上视图：Execute 会据此转成「等待确认」的哨兵错误。
		return assistantToolOutcome{PendingConfirmation: view}
	}

	exec := assistantToolExecutor()
	if exec == nil {
		return assistantToolOutcome{Result: metaToolError("工具执行入口不可用（MCP 未装配）")}
	}
	out, cerr := exec(ctx, owner, name, inner)
	if cerr != nil {
		// 失败原样回灌给模型（它要据此调整），但前缀标明是失败，
		// 避免模型把错误文本当成工具结果继续推理。
		return assistantToolOutcome{Result: "工具执行失败：" + cerr.Error()}
	}
	if strings.TrimSpace(out) == "" {
		return assistantToolOutcome{Result: "（工具执行成功，无返回内容）"}
	}
	return assistantToolOutcome{Result: out}
}

// assistantToolTargetOf 从工具参数里取「目标类型 + id」，用于确认卡展示与指纹。
//
// 取不到就返回空：确认卡上会显示「目标未知」。这比猜一个 id 安全——
// 猜错会让用户在卡上看到错误的服务器，从而批准一件他没看过的事。
// 参数名的多样性（id / instanceId / nodeId / botId…）正是不猜的理由。
func assistantToolTargetOf(tool *AssistantToolAction, args map[string]any) (string, string) {
	for _, key := range []string{"id", "instanceId", "nodeId", "botId"} {
		raw, ok := args[key]
		if !ok {
			continue
		}
		switch v := raw.(type) {
		case float64:
			return tool.ResourceType, fmt.Sprintf("%.0f", v)
		case string:
			return tool.ResourceType, v
		case int:
			return tool.ResourceType, fmt.Sprintf("%d", v)
		}
	}
	return tool.ResourceType, ""
}

// metaToolError 统一的元工具错误文本。
func metaToolError(msg string) string { return "元工具失败：" + msg }
