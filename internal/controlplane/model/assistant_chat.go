package model

import (
	"time"

	"gorm.io/gorm"
)

// 助手 run 状态（§5.1）。
//
// 状态机的活跃态与非活跃态**不是**两个独立概念：活跃态集合直接参与数据库部分唯一索引
// 的谓词（见 AssistantRunActiveStatuses），改这里就等于改索引语义。
const (
	// AssistantRunStatusQueued 已受理未开始。
	AssistantRunStatusQueued = "queued"
	// AssistantRunStatusRunning 正在推进（模型流或工具执行中）。
	AssistantRunStatusRunning = "running"
	// AssistantRunStatusWaitingConfirmation 等待人工确认工具调用（由触发）。
	AssistantRunStatusWaitingConfirmation = "waiting_confirmation"
	// AssistantRunStatusCompleted 正常收尾（含预算耗尽后的强制收尾，见 termination_reason）。
	AssistantRunStatusCompleted = "completed"
	// AssistantRunStatusFailed 运行期失败（已脱敏分类）。
	AssistantRunStatusFailed = "failed"
	// AssistantRunStatusCancelled 用户主动停止。
	AssistantRunStatusCancelled = "cancelled"
	// AssistantRunStatusTimedOut 超出 run 总预算。
	AssistantRunStatusTimedOut = "timed_out"
	// AssistantRunStatusInterrupted 进程重启回收（**只**能由启动回收器写入）。
	AssistantRunStatusInterrupted = "interrupted"
)

// AssistantRunActiveStatuses 返回占住「单会话单活跃 run」槽位的状态集合（§5.2）。
//
// 为什么用函数返回副本而不是导出 var：这是数据库部分唯一索引谓词的**唯一真源**
// （migrateAssistantRunUniqueIndexes 按它拼 SQL）。共享可变切片迟早会被调用方就地 append
// 而后悔，副本成本可忽略。
func AssistantRunActiveStatuses() []string {
	return []string{
		AssistantRunStatusQueued,
		AssistantRunStatusRunning,
		AssistantRunStatusWaitingConfirmation,
	}
}

// 助手 run 终态原因（§4.3）：仅终态行有值，与 Status 组合表达「怎么结束的」。
const (
	AssistantTerminationCompleted       = "completed"
	AssistantTerminationCancelled       = "cancelled"
	AssistantTerminationFailed          = "failed"
	AssistantTerminationTimedOut        = "timed_out"
	AssistantTerminationInterrupted     = "interrupted"
	AssistantTerminationBudgetExhausted = "budget_exhausted"
	// AssistantTerminationAwaitingConfirm 本轮因等待用户确认而未走完。
	// 它不是「失败」也不是「完成」：run 停在 waiting_confirmation，
	// 用户决定后由新的执行恢复或终结。
	AssistantTerminationAwaitingConfirm = "awaiting_confirm"
)

// 助手消息角色（§4.2）。
const (
	AssistantMessageRoleUser      = "user"
	AssistantMessageRoleAssistant = "assistant"
	AssistantMessageRoleTool      = "tool"
)

// 助手消息状态（§4.2）。
const (
	AssistantMessageStateComplete    = "complete"
	AssistantMessageStateInterrupted = "interrupted"
	AssistantMessageStateTruncated   = "truncated"
)

// 助手事件类型（§5.3 最小集）。
const (
	AssistantEventTypeTextDelta      = "text_delta"
	AssistantEventTypeToolCall       = "tool_call"
	AssistantEventTypeToolStatus     = "tool_status"
	AssistantEventTypeToolResult     = "tool_result"
	AssistantEventTypeUsage          = "usage"
	AssistantEventTypeContextWarning = "context_warning"
	AssistantEventTypeError          = "error"
	AssistantEventTypeRunEnd         = "run_end"
)

// AssistantConversation 助手私有会话（§4.1）。
//
// 归属用单列 owner_user_id 而不是复用用户组 / 组管理员链：会话是**私有**的，
// 组管理员也不是他人会话的所有者，所有读写都强制带 owner_user_id 条件。
type AssistantConversation struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// OwnerUserID 归属用户，所有查询强制带此条件。
	OwnerUserID uint `gorm:"not null;index" json:"ownerUserId"`
	// Title 会话标题（1..200）：首轮由用户消息截取，之后可手改。
	Title string `gorm:"size:200;not null" json:"title"`
	// ModelID 可空：空表示「跟随全局默认模型」，而不是创建时固化一份指针副本。
	ModelID *uint `json:"modelId"`
	// ActiveRunID 可空：当前活跃 run 的冗余指针，读路径省一次 run 表扫描。
	// 它**不是约束**——单活跃语义由 assistant_runs 的部分唯一索引保证，
	// 本列指向哪个 run 与索引是否放行互不推导（§4.1 要求两者互为校验）。
	ActiveRunID *uint `json:"activeRunId"`
	// DeletedAt 软删除：用户只能删自己的会话；消息与事件随之不可读但**不物理删除**
	// （§10.1 会话记录永久保留，是操作追责材料）。
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
}

// TableName 固定表名。
func (AssistantConversation) TableName() string { return "assistant_conversations" }

// AssistantMessage 会话内一条可见消息（§4.2）。
//
// 消息与事件分开：消息是「用户看到的对话」，事件是「run 的过程流」。
// 二者条数与生命周期都不同（一次 run 产生多个事件、少量消息），合并会让补读游标语义变乱。
type AssistantMessage struct {
	ID             uint `gorm:"primaryKey" json:"id"`
	ConversationID uint `gorm:"not null;index" json:"conversationId"`
	// RunID 可空：用户消息不由任何 run 产生。
	RunID *uint `gorm:"index" json:"runId"`
	// Role 取值见 AssistantMessageRole* 常量。
	Role string `gorm:"size:16;not null" json:"role"`
	// Content 可见正文。role=tool 时存**有界**结果：超出 TOOL_RESULT_MAX（64KiB）的部分
	// 直接丢弃并置 state=truncated，全文不另存——保留全文等于给模型输出开了条无上限存储通道。
	Content string `gorm:"type:text;not null" json:"content"`
	// ToolCallID 与 run 内 callId 对应，role=tool 时非空。
	ToolCallID *string `gorm:"size:64" json:"toolCallId"`
	// Turn 轮次序号，从 1 递增。
	Turn int `gorm:"not null" json:"turn"`
	// State 取值见 AssistantMessageState* 常量。
	State     string    `gorm:"size:16;not null" json:"state"`
	CreatedAt time.Time `json:"createdAt"`

	// ── 编辑与回退──
	//
	// OriginalContent 首次编辑前的原文。**只保留最初一版**而不是历史栈：
	// 需求是「能看出改了什么」，不是版本管理；保留原文已足够审计追溯，
	// 无限历史会让消息表随编辑次数线性膨胀。
	OriginalContent *string    `gorm:"column:original_content;type:text" json:"originalContent,omitempty"`
	EditedAt        *time.Time `gorm:"column:edited_at" json:"editedAt,omitempty"`
	EditCount       int        `gorm:"column:edit_count;not null;default:0" json:"editCount"`
	// SupersededAt 被回退取代的时刻（§6.3）。
	//
	// **不物理删除**：工具调用消息关联着确认票据与平台审计，硬删会让
	// 「这条副作用是谁批准的」失去线索。读取上下文时按 `IS NULL` 过滤。
	SupersededAt *time.Time `gorm:"column:superseded_at;index" json:"supersededAt,omitempty"`
}

// TableName 固定表名。
func (AssistantMessage) TableName() string { return "assistant_messages" }

// AssistantRun 一轮对话运行（§4.3）。
//
// 单会话单活跃 run 由**数据库部分唯一索引**保证，不用进程内锁：进程锁在重启后丢失，
// 在扩容后直接失效（§5.2、§8.1）。
type AssistantRun struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// ConversationID 故意不带 GORM tag：约束是「同一 conversation_id 在活跃态下唯一」的
	// **部分**唯一索引，GORM 的 uniqueIndex tag 表达不了 WHERE 谓词；写成普通唯一索引会让
	// 一个会话永久只能有一个 run（历史 run 全被挡住）。索引由 migrateAssistantRunUniqueIndexes 建。
	ConversationID uint `gorm:"not null" json:"conversationId"`
	// OwnerUserID 冗余存一份：归属校验不必连表，且幂等唯一键以它打头。
	OwnerUserID uint `gorm:"not null;index" json:"ownerUserId"`
	// ModelSnapshot 本轮模型配置快照（JSON：协议 / 上游 modelId / 能力）。
	// **不随配置变更回写**：事后追责要还原的是「当时用的什么」，不是今天的配置。
	ModelSnapshot string `gorm:"type:text;not null" json:"modelSnapshot"`
	// ToolPolicyVersion 本轮工具策略版本，确认校验用（策略已变更的旧确认不得被执行）。
	ToolPolicyVersion int `gorm:"not null;default:1" json:"toolPolicyVersion"`
	// ContextSnapshot 可空：页面上下文快照，由写入。
	ContextSnapshot *string `gorm:"type:text" json:"contextSnapshot"`
	// Status 取值见 AssistantRunStatus* 常量。
	Status string `gorm:"size:32;not null" json:"status"`
	// TerminationReason 可空：仅终态有值，取值见 AssistantTermination* 常量。
	TerminationReason *string `gorm:"size:32" json:"terminationReason"`
	// UsageInput / UsageOutput 只记供应商**真实**用量；缺失保持 null，绝不估算（shared-contracts §4.1）。
	UsageInput  *int `json:"usageInput"`
	UsageOutput *int `json:"usageOutput"`
	// IdempotencyKey 可空：非空时与 owner_user_id + conversation_id 组成唯一约束，
	// 唯一的由部分索引（WHERE idempotency_key IS NOT NULL AND idempotency_key <> ''）实现。
	// 空值不参与唯一性——否则「不带该头的请求」会互相顶掉。
	IdempotencyKey *string `gorm:"size:128" json:"idempotencyKey"`
	// RequestDigest 可空：与 idempotency_key 配对的请求摘要，只用于判断同键是否同请求；
	// **不进唯一键**——若进键，异参请求会各自插入一条，幂等就失效了（§6.1）。
	RequestDigest *string `gorm:"size:64" json:"requestDigest"`
	// Version 乐观并发：状态迁移走 CAS（WHERE id=? AND version=?），不匹配回 409。
	Version   int       `gorm:"not null;default:1" json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	// StartedAt / EndedAt 可空：排队的 run 没有开始时间，未终态的 run 没有结束时间。
	// 不用零值时间代替——那会把「未开始」与公元 1 年混为一谈，前端也不得不特判。
	StartedAt *time.Time `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt"`
}

// TableName 固定表名。
func (AssistantRun) TableName() string { return "assistant_runs" }

// AssistantEvent 运行期 append-only 事件（§4.4）。
//
// 事件**永不因保留策略被删**（§10.1）：断流补读依赖「游标不会因数据被删而失效」，
// 否则前端一旦重连就得面对不可恢复的空洞。
type AssistantEvent struct {
	ID             uint `gorm:"primaryKey" json:"id"`
	ConversationID uint `gorm:"not null;index" json:"conversationId"`
	// RunID + Seq 组成唯一键：seq 每 run 从 1 连续递增且不允许空洞（空洞会让前端补读游标
	// 无法判定是否漏收，§5.3）。复合唯一索引的最左列即 run_id，故不再单建 run_id 索引。
	RunID uint `gorm:"not null;uniqueIndex:ux_assistant_events_run_seq,priority:1" json:"runId"`
	Seq   int  `gorm:"not null;uniqueIndex:ux_assistant_events_run_seq,priority:2" json:"seq"`
	// Type 取值见 AssistantEventType* 常量。
	Type string `gorm:"size:32;not null" json:"type"`
	// MessageID / CallID 关联锚点，可空。
	MessageID *uint   `json:"messageId"`
	CallID    *string `gorm:"size:64" json:"callId"`
	// Data 有界载荷（JSON）：单条上限 EVENT_PAYLOAD_MAX（32KiB），超出截断并置 truncated。
	Data      string    `gorm:"type:text;not null" json:"data"`
	CreatedAt time.Time `json:"createdAt"`
}

// TableName 固定表名。
func (AssistantEvent) TableName() string { return "assistant_events" }
