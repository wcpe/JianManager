package service

// FR-515 Phase C：对话运行时服务。
//
// 职责边界（与本包既有 assistant*.go 的关系）：
//   - assistant.go / assistant_probe.go 管「模型从哪来」（FR-514 配置面与协议适配），本文件
//     **复用**它们的 resolveCredential/buildStreamer，不重写任何协议层；
//   - 本文件管「一轮对话怎么跑完并持久化」：会话/消息/run/事件、状态机、预算、取消、重启回收；
//   - 工具的实际执行与确认属 FR-516，本文件只留 ToolExecutor 缝，**不注册任何业务工具**。
//
// 四条不变量在这里落地，改动前请先读对应 spec 段：
//  1. 单会话单活跃 run 由数据库**部分唯一索引**保证（§5.2），不是进程内锁；
//  2. 幂等判序固定为「按唯一键查 → 比摘要 → 判会话活跃 → 插入 → 冲突回读」（§6.1）；
//  3. 事件 seq 每 run 从 1 连续、不允许空洞，故 seq 只能由单一写入者的内存计数器分配（§5.3）；
//  4. 部署形态是 V1 单 CP 实例（§8.1），启动回收因此才允许「一律置 interrupted」。

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/ai"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// ── 预算与上限（数值严格按 spec §7）──────────────────────────────────────

const (
	// assistantChatMaxToolRounds 单轮 run 内工具往返轮次上限（MAX_TOOL_ROUNDS）。
	assistantChatMaxToolRounds = 8
	// assistantChatMaxToolCalls 单轮 run 内工具调用次数上限（MAX_TOOL_ROUNDS 的 16 次调用）。
	assistantChatMaxToolCalls = 16
	// assistantChatDefaultMaxOutputTokens 模型未声明输出上限时的默认值（MAX_OUTPUT_TOKENS）。
	assistantChatDefaultMaxOutputTokens = 2048
	// assistantChatRunWallClock run 总运行预算（RUN_WALL_CLOCK）。等待人工确认的时间按 §7
	// 单独计时，但本 FR 不实现确认环节（FR-516），故此处无需扣除。
	assistantChatRunWallClock = 300 * time.Second
	// assistantChatModelRequestTimeout 单次模型请求超时（MODEL_REQUEST_TIMEOUT）。
	assistantChatModelRequestTimeout = 120 * time.Second
	// assistantChatIdleStreamTimeout 供应商无任何字节即判失败（IDLE_STREAM_TIMEOUT）。
	assistantChatIdleStreamTimeout = 90 * time.Second
	// assistantChatEventPayloadMax 单条事件 data 上限（EVENT_PAYLOAD_MAX）。
	assistantChatEventPayloadMax = 32 << 10
	// assistantChatHistoryWindow 单次**外发**窗口（HISTORY_WINDOW）：库内全量保留，
	// 只截取最近 N 条喂给模型。它不参与任何保留策略——事件与消息永不因它被删（§10.1）。
	assistantChatHistoryWindow = 200
	// assistantChatToolResultMax 单条工具结果入库上限（TOOL_RESULT_MAX）。
	assistantChatToolResultMax = 64 << 10
	// assistantChatConcurrentRunsPerUser 同一用户跨会话的活跃 run 上限（CONCURRENT_RUNS_PER_USER）。
	assistantChatConcurrentRunsPerUser = 2

	// assistantChatDefaultPageLimit / assistantChatMaxPageLimit 分页默认值与上限。
	assistantChatDefaultPageLimit = 20
	assistantChatMaxPageLimit     = 100
	// assistantChatTitleMaxRunes 会话标题长度上限（§4.1：1..200）。
	assistantChatTitleMaxRunes = 200
	// assistantChatAutoTitleRunes 首轮自动截取标题的长度。
	assistantChatAutoTitleRunes = 50
	// assistantChatUserMessageMaxRunes 单轮用户消息正文上限。超过这个量级已经不是「一轮对话」
	// 而是批量投喂，落库后会让每次外发窗口都撑满，故在入口处拒绝而不是静默截断。
	assistantChatUserMessageMaxRunes = 16000
	// assistantChatAssistantMessageMax 单条助手消息正文入库上限。模型输出没有硬上限，
	// 不设界等于给「模型复读」开一条无上限存储通道（与 TOOL_RESULT_MAX 同口径）。
	assistantChatAssistantMessageMax = 64 << 10
	// assistantChatToolPreviewRunes tool_result 事件预览长度：事件是给 UI 折叠展示的，
	// 全文由 messages 表承载，预览过长只会把补读流量撑大。
	assistantChatToolPreviewRunes = 2048
	// assistantChatCancelRetries 取消与收尾的 CAS 重试次数：并发写入方（执行体 vs 取消）
	// 会互相让 version 前进，一次失败不代表取消失败，重读重试即可收敛。
	assistantChatCancelRetries = 3
)

// assistantChatSystemPrompt 助手的基础系统提示。
//
// 刻意不谈任何具体工具：工具知识写进系统提示会让「模型以为能用某工具、实际没注册」
// 变成一串必然失败的调用；工具描述的唯一来源是 ToolExecutor.Available（FR-516）。
const assistantChatSystemPrompt = "你是 JianManager 平台内置的运维助手。回答使用简体中文，简洁、可执行；" +
	"不确定的信息要明确说明不确定，不要编造平台功能、数据或操作结果。"

// ── 业务错误 ────────────────────────────────────────────────────────────

var (
	// ErrAssistantChatNotFound 会话或 run 不存在**或不属于当前用户**。
	//
	// 越权与不存在共用同一个错误是刻意的（§6「RUN_NOT_FOUND（404，含越权）」）：
	// 分开回 403 与 404，等于给攻击者一个「这个 id 存在」的探测接口。
	ErrAssistantChatNotFound = errors.New("会话或运行不存在")
	// ErrAssistantChatConflict 乐观并发失败：run 已被另一方推进到别的状态。
	ErrAssistantChatConflict = errors.New("会话或运行已被他人修改")
	// ErrAssistantIdempotencyConflict 同一幂等键被用于内容不同的请求（§6.1）。
	ErrAssistantIdempotencyConflict = errors.New("同一幂等键的请求内容不一致")
	// ErrAssistantConversationBusy 该会话已有活跃 run（409 CONVERSATION_BUSY）。
	// 具体活跃 runId 由 AssistantConversationBusyError 带出。
	ErrAssistantConversationBusy = errors.New("该会话已有进行中的运行")
	// ErrAssistantTooManyRuns 该用户跨会话的活跃 run 已达上限（CONCURRENT_RUNS_PER_USER）。
	ErrAssistantTooManyRuns = errors.New("并发运行数已达上限")
	// ErrAssistantModelUnavailable 会话选定/默认模型不可用（503 MODEL_UNAVAILABLE）。
	ErrAssistantModelUnavailable = errors.New("没有可用的模型")
	// ErrAssistantInvalidCursor 分页游标不合法（400 INVALID_CURSOR，§6.2）。
	ErrAssistantInvalidCursor = errors.New("分页游标不合法")
	// ErrAssistantConfirmPendingExists 同一 run 已有待确认票据（§8「同 run 最多 1 张 pending」）。
	//
	// 触发场景：模型在**同一个工具轮次**里回了多个写调用。此时 run 只能表达
	// 「等待确认」一个状态，多张卡片的批准顺序无从界定，因此这里明确拒绝第二个，
	// 让模型先完成当前这张——而不是把两张都堆给用户。
	ErrAssistantConfirmPendingExists = errors.New("该轮已有待确认的写操作，请先处理它")
)

// AssistantToolExecError 动作**已批准、已尝试执行**，但领域执行失败。//
// 与其它确认错误的区别是「票据本身完全有效」：CAS 已消费、参数与目标都已核对。
// 因此它不能被映射成 409/410（那会引导客户端去重新出票，而重试只会得到「已被消费」），
// 也不该是 500（那读起来像平台 bug）。对调用方正确的语义是
// 「请求没问题，是执行侧失败」→ 502，并带上真实原因。
type AssistantToolExecError struct {
	Err error
}

func (e *AssistantToolExecError) Error() string { return e.Err.Error() }

// Unwrap 让 errors.Is/As 能穿过包装，调用方仍可判具体领域错误。
func (e *AssistantToolExecError) Unwrap() error { return e.Err }

// AssistantConversationBusyError 会话已有活跃 run。
//
// 为什么不用裸哨兵错误：前端要能「接续观察」正在跑的那一轮（§5.2 末段），只回一句
// 「忙碌」等于给了一个无解的错误。errors.Is(err, ErrAssistantConversationBusy) 仍成立，
// 调用方用 errors.As 取 RunID。
type AssistantConversationBusyError struct {
	// RunID 当前占住槽位的活跃 run。
	RunID uint
}

// Error 实现 error。
func (e *AssistantConversationBusyError) Error() string {
	return fmt.Sprintf("%s（活跃 run %d）", ErrAssistantConversationBusy.Error(), e.RunID)
}

// Unwrap 让 errors.Is(err, ErrAssistantConversationBusy) 成立。
func (e *AssistantConversationBusyError) Unwrap() error { return ErrAssistantConversationBusy }

// ── 工具执行缝 ──────────────────────────────────────────────────────────

// ToolExecutor 由 FR-516 实现；本 FR **不提供任何业务工具**。
//
// 之所以现在就把缝留出来：运行循环的形状（何时暴露工具、何时回灌结果、何时判定预算耗尽）
// 是 FR-515 的产出，若等工具实现到位再改循环，等于让 FR-516 重写一次编排。
type ToolExecutor interface {
	// Available 返回本轮可用工具描述；返回空切片表示无工具。
	Available(ctx context.Context, owner uint) []ai.ToolSpec
	// Execute 执行一次工具调用。FR-516 负责权限重验、确认与副作用。
	Execute(ctx context.Context, owner uint, call ai.ToolCall) (result string, err error)
}

// ── 模型快照与运行参数 ───────────────────────────────────────────────────

// assistantModelSnapshot 本轮模型配置的**冻结**快照（§4.3），以 JSON 存进 run 行。
//
// 只记「事后要还原的行为事实」：协议、上游模型标识、能力声明、输出上限。
// **不含** base_url 与任何凭据形态：凭据会轮换、且属于传输层参数，抄进长期保留的
// run 行等于给密钥开了第二条持久化通道。
type assistantModelSnapshot struct {
	// PlatformModelID 平台 model 行 ID（方便事后反查，不代表「今天仍存在」）。
	PlatformModelID uint `json:"platformModelId"`
	// ProviderID 供应商行 ID，同样只是引用而非配置副本。
	ProviderID uint `json:"providerId"`
	// Protocol 本轮使用的协议（决定适配器分支）。
	Protocol string `json:"protocol"`
	// UpstreamModelID 上游原始模型标识：执行期用它，不重新读配置。
	UpstreamModelID string `json:"upstreamModelId"`
	// DisplayName 便于事后阅读时不必再连表。
	DisplayName string `json:"displayName,omitempty"`
	// Streaming / Tools 是模型能力声明，决定是否真的按流式消费、是否暴露工具。
	Streaming bool `json:"streaming"`
	Tools     bool `json:"tools"`
	// MaxOutputTokens / ContextWindow 能力声明；未知为 nil（不补 0）。
	MaxOutputTokens *int `json:"maxOutputTokens,omitempty"`
	ContextWindow   *int `json:"contextWindow,omitempty"`
	// ReasoningEffort 思考强度（FR-541 §3.2）；空 = 不指定。
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

// maxOutputTokens 返回本轮应下发的输出上限（未声明则用 spec §7 的默认 2048）。
func (s assistantModelSnapshot) maxOutputTokens() int {
	if s.MaxOutputTokens != nil && *s.MaxOutputTokens > 0 {
		return *s.MaxOutputTokens
	}
	return assistantChatDefaultMaxOutputTokens
}

// SubmitRunBody 提交一轮的请求体。
type SubmitRunBody struct {
	// Text 本轮用户消息正文（必填）。
	Text string
	// ContextSnapshot 页面上下文快照（FR-518 写入）。本 FR 只原样落库，不解析、不外发。
	ContextSnapshot *string
}

// assistantChatLimits 运行时硬上限。
//
// 为什么集中成结构体而不是散落常量：预算耗尽路径（budget_exhausted）只有把轮次上限
// 调到 1 才跑得动，否则一个用例要真跑 8 轮工具往返；能调的是**测试**用的注入点，
// 生产默认值仍由 defaultAssistantChatLimits 固化，不接受前端传值（§7「预算是服务端硬约束」）。
type assistantChatLimits struct {
	MaxToolRounds       int
	MaxToolCalls        int
	MaxOutputTokens     int
	RunWallClock        time.Duration
	ModelRequestTimeout time.Duration
	IdleStreamTimeout   time.Duration
	EventPayloadMax     int
	HistoryWindow       int
	// ContextTokenBudget 模型声明的上下文窗口（CONTEXT_TOKEN_BUDGET）。
	// **0 表示未知**：此时不从预算侧裁剪，只按 HistoryWindow 条数上限走。
	// 未知就不装懂——不拿估算值冒充模型能力。
	ContextTokenBudget    int
	ToolResultMax         int
	ConcurrentRunsPerUser int
}

// defaultAssistantChatLimits 返回 spec §7 的默认值。
func defaultAssistantChatLimits() assistantChatLimits {
	return assistantChatLimits{
		MaxToolRounds:         assistantChatMaxToolRounds,
		MaxToolCalls:          assistantChatMaxToolCalls,
		MaxOutputTokens:       assistantChatDefaultMaxOutputTokens,
		RunWallClock:          assistantChatRunWallClock,
		ModelRequestTimeout:   assistantChatModelRequestTimeout,
		IdleStreamTimeout:     assistantChatIdleStreamTimeout,
		EventPayloadMax:       assistantChatEventPayloadMax,
		HistoryWindow:         assistantChatHistoryWindow,
		ToolResultMax:         assistantChatToolResultMax,
		ConcurrentRunsPerUser: assistantChatConcurrentRunsPerUser,
	}
}

// runModelPreparer 把会话选定的模型解析成「本轮冻结快照 + 流式适配器」。
//
// 为什么把「解析配置」和「构造适配器」合成一个注入点：这两步在生产路径上必须一起成功
// （模型不可用就不该插入 run），在测试路径上必须一起被替换（否则会真的连上游）。
// 拆成两个注入点只会让「一半真一半假」的装配成为可能。
type runModelPreparer func(conv *model.AssistantConversation) (assistantModelSnapshot, ai.Streamer, error)

// ── 服务 ────────────────────────────────────────────────────────────────

// AssistantChatService 对话运行时服务（FR-515 Phase C）。
//
// 所有会话读写都按 owner_user_id 收敛：会话是**私有**的，组管理员也不是他人会话的所有者
// （§3 冻结项），因此越权一律返回 ErrAssistantChatNotFound（不区分不存在与无权）。
type AssistantChatService struct {
	db *gorm.DB
	// assistant 复用 FR-514 的凭据解析与协议适配构造。允许为 nil（测试/未装配）：
	// 此时默认的模型准备函数会明确报 MODEL_UNAVAILABLE，而不是静默用假实现跑起来。
	assistant *AssistantService
	limits    assistantChatLimits

	toolsMu sync.RWMutex
	tools   ToolExecutor

	// mcp 外部 MCP 连接服务（FR-517 闭环的最后一跳）。
	// nil 表示未装配：此时工具目录只含 system.*，不暴露任何外部工具。
	mcp *AssistantMCPService

	// instances/nodes 领域服务（FR-516 工具覆盖度）。
	// 由装配阶段经 SetDomainServices 注入：写操作需要 ClientPool 才能真的下发，
	// 就地构造会拿到 pool=nil 的空壳。nil 表示只读剪裁部署。
	domainMu  sync.RWMutex
	instances *InstanceService
	nodes     *NodeService

	// authz 用于**执行期/提交期重验**（FR-516 §5.1、FR-518 §6）。
	// 惰性构造：它无状态、只包 db 与权限服务，构造开销可忽略；这样不必改所有
	// NewAssistantChatService 调用点（大量测试直接用两参构造）。
	authzOnce sync.Once
	authzSvc  *AuthzService

	// audit 用于把助手执行的写操作落进平台审计（FR-516 §9）。
	// 惰性构造理由同 authz：无状态、只包 db，不必改构造签名。
	auditOnce sync.Once
	auditSvc  *AuditService

	preparerMu sync.RWMutex
	preparer   runModelPreparer

	// runCtx 所有执行体的父上下文：随构造建立、Stop 取消。
	// 在构造期就建立（而不是 Start 里）是为了让「未 Start 直接 Submit」不会 panic 地
	// 起一个无主的 goroutine——生命周期由 Stop 收口，不由 Start 开启。
	runCtx    context.Context
	runCancel context.CancelFunc
	wg        sync.WaitGroup

	// cancelMu 保护 cancels：管理运行中 run 的取消函数，终态时清理。
	cancelMu sync.Mutex
	cancels  map[uint]context.CancelFunc

	// hub 进程内事件广播：SSE 订阅即时收帧，轮询退化为兜底。
	// V1 单 CP 实例（§8.1），故不做跨进程扇出。
	hub *assistantEventHub

	// ── 技能（FR-541）──
	//
	// 技能是「目录 + SKILL.md」的纯文本指令，随仓库发布、可在数据根里增删。
	// 装载结果缓存在此：run 构建系统提示词是热路径，不能每轮去读盘。
	skillsMu sync.RWMutex
	skills   []Skill
	// skillSkipped 装载时被跳过的目录与原因，供管理端点与排障展示。
	skillSkipped map[string]string
	// skillRoot 技能根目录；空串表示未配置（此时不装载、也不拼技能）。
	skillRoot string
	// skillEnabled 显式启用的技能名；**空表示全部已装载技能都启用**。
	//
	// 为什么默认全启用：官方技能随产品发布，若要求逐个手工启用，
	// 该功能在默认安装下等于不存在。技能只是提示词文本，不放宽任何工具权限；
	// 需要收窄时由管理员给出白名单。
	skillEnabled []string
}

// NewAssistantChatService 构造对话运行时服务。
//
// assistant 允许为 nil，但只有测试或未装配场景会这样用；生产装配层应传入同一个
// AssistantService 实例，以便复用凭据解析与出站准入。
func NewAssistantChatService(db *gorm.DB, assistant *AssistantService) *AssistantChatService {
	ctx, cancel := context.WithCancel(context.Background())
	s := &AssistantChatService{
		db:        db,
		assistant: assistant,
		limits:    defaultAssistantChatLimits(),
		runCtx:    ctx,
		runCancel: cancel,
		cancels:   make(map[uint]context.CancelFunc),
		hub:       newAssistantEventHub(),
	}
	s.preparer = s.prepareRunModel
	return s
}

// authz 返回（必要时构造）授权服务。
//
// 用惰性构造而不是新增构造参数：AuthzService 无状态，重复构造只多一个结构体；
// 而改 NewAssistantChatService 签名会波及全部既有调用点（含大量测试）。
func (s *AssistantChatService) authz() *AuthzService {
	s.authzOnce.Do(func() {
		s.authzSvc = NewAuthzService(s.db)
	})
	return s.authzSvc
}

// audit 返回（必要时构造）审计服务。
//
// 与 authz 同样的惰性构造理由：AuditService 无状态、只包 db。
// 助手执行写操作必须留审计——否则管理员在平台审计里看不到「实例是助手启的」，
// 而这是 ADR-105 最关心的提权面。
func (s *AssistantChatService) audit() *AuditService {
	s.auditOnce.Do(func() {
		s.auditSvc = NewAuditService(s.db)
	})
	return s.auditSvc
}

// SetDomainServices 注入领域服务（FR-516 工具覆盖度的装配点）。
//
// 为什么必须**由装配阶段注入**而不是像 authz() 那样就地 new 一个：
//
//	`InstanceService` 的启动/停止/重启要走 `spawnDelegate`，它依赖构造期注入的
//
// `*cpgrpc.ClientPool` 才能真的把动作下发到 Worker。就地 `NewInstanceService(db, nil, nil)`
// 拿到的实例 pool=nil，调用会失败或（更糟）静默不执行——「助手说已启动、实际没动」。
// 因此这里只接受外部注入，**不提供回退构造**；未注入时相关工具直接不在目录里。
//
// 允许为 nil：只跑只读工具是合法剪裁部署。
func (s *AssistantChatService) SetDomainServices(instances *InstanceService, nodes *NodeService) {
	if s == nil {
		return
	}
	s.domainMu.Lock()
	defer s.domainMu.Unlock()
	s.instances = instances
	s.nodes = nodes
}

// domain 返回已注入的领域服务（可能为 nil，调用方必须自行判空）。
func (s *AssistantChatService) domainServices() (*InstanceService, *NodeService) {
	s.domainMu.RLock()
	defer s.domainMu.RUnlock()
	return s.instances, s.nodes
}

// SetMCPService 注入外部 MCP 连接服务（FR-517）。
//
// 未注入时外部工具不进目录——剪裁部署（只跑内置工具）是合法配置，
// 因此这里不做强制校验。
func (s *AssistantChatService) SetMCPService(m *AssistantMCPService) {
	if s == nil {
		return
	}
	s.mcp = m
}

// SetToolExecutor 注入工具执行器（FR-516 的实现点）。
//
// 未注入时 Available 视为空、请求里不注册任何工具，循环里也**不会**产生工具调用：
// 若让模型凭空看到工具却没人能执行，只会得到一串必然失败的调用。
func (s *AssistantChatService) SetToolExecutor(e ToolExecutor) {
	if s == nil {
		return
	}
	s.toolsMu.Lock()
	defer s.toolsMu.Unlock()
	s.tools = e
}

// setLimits 覆盖预算（仅测试使用；生产默认值来自 defaultAssistantChatLimits）。
func (s *AssistantChatService) setLimits(l assistantChatLimits) {
	if s == nil {
		return
	}
	s.limits = l
}

// setModelPreparer 替换模型准备函数（仅测试使用：注入假适配器，避免打真实网络）。
func (s *AssistantChatService) setModelPreparer(p runModelPreparer) {
	if s == nil {
		return
	}
	s.preparerMu.Lock()
	defer s.preparerMu.Unlock()
	s.preparer = p
}

// toolExecutor 取当前工具执行器（可能为 nil）。
func (s *AssistantChatService) toolExecutor() ToolExecutor {
	s.toolsMu.RLock()
	defer s.toolsMu.RUnlock()
	return s.tools
}

// modelPreparer 取当前模型准备函数。
func (s *AssistantChatService) modelPreparer() runModelPreparer {
	s.preparerMu.RLock()
	defer s.preparerMu.RUnlock()
	return s.preparer
}

// Start 启动运行时：执行一次重启回收（§8）。
//
// **部署前提（§8.1，冻结）**：V1 只支持单个 CP 实例。启动回收的逻辑是「一律把非终态 run
// 置 interrupted」，它在多实例下会误伤另一个实例正在正常工作的 run 并释放它占用的会话槽。
// 要让回收在多实例下安全，需要 run 所有者标识 + 租约续期 + 心跳失效判定 + fencing
// generation 一整套分布式协调，与本 FR「轻量运行时」的定位冲突，**不在本 FR 范围**：
// 因此这里既不做租约也不做心跳，而是把「单实例」写成部署约束。
func (s *AssistantChatService) Start() {
	if s == nil || s.db == nil {
		return
	}
	reaped, err := s.ReapOnStartup()
	if err != nil {
		slog.Warn("助手运行时：启动回收失败", "error", err)
		return
	}
	if reaped > 0 {
		slog.Info("助手运行时：已回收非终态 run", "count", reaped)
	}
}

// Stop 取消所有在途 run 并等待执行体退出。
//
// 取消而不等待自然结束：进程正在关闭，继续推进只会在半途被 SIGKILL 打断；
// 显式取消能让执行体走完「写 run_end + 清 active_run_id」的收尾，把状态留在可解释的地方。
func (s *AssistantChatService) Stop() {
	if s == nil {
		return
	}
	s.runCancel()
	s.wg.Wait()
}

// ── 会话 ────────────────────────────────────────────────────────────────

// assistantChatCursorPrefix 游标编码版本前缀，便于将来换编码时明确拒绝旧游标。
const assistantChatCursorPrefix = "v1:"

// encodeAssistantChatCursor 把边界行的 id 编码成不透明游标。
//
// 游标 = 边界行 id + 版本前缀。为什么**不带**时间戳：
//   - 同一时间戳可能被多行共享（批量创建的会话就是这样），只靠时间戳会在翻页时
//     重复或漏掉边界行，id 提供了确定的全序兜底；
//   - 更关键的是比较语义：分页条件里的 updated_at 必须与库里存的那一份比较，而 SQLite
//     把时间存成 TEXT、按字符串比较。把时间读进 Go 再序列化回去参与比较时，只要格式
//     有一点差异（UTC 化、时区偏移、精度）就会既不「<」也不「=」——实测表现是第二页
//     直接空掉（库里是 11:58+08:00，游标里成了 03:58Z）。
//     让数据库自己从边界行取 updated_at（见 ListConversations 的子查询），比较双方就都是
//     库里的原值，与驱动的序列化实现、进程时区都无关。
func encodeAssistantChatCursor(id uint) string {
	return base64.RawURLEncoding.EncodeToString([]byte(assistantChatCursorPrefix + strconv.FormatUint(uint64(id), 10)))
}

// decodeAssistantChatCursor 解析游标；非法游标回 ErrAssistantInvalidCursor（400，§6.2）。
//
// 带版本前缀（"v1:"）：将来若换编码，旧游标会被明确判为非法而不是被误解析成
// 另一个 id——后者会让用户看到一页「看起来正常但其实是随机起点」的数据。
func decodeAssistantChatCursor(s string) (uint, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, fmt.Errorf("不是合法的游标编码: %w", err)
	}
	body, ok := strings.CutPrefix(string(raw), assistantChatCursorPrefix)
	if !ok {
		return 0, fmt.Errorf("游标版本不被识别")
	}
	id, err := strconv.ParseUint(body, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("游标 ID 不合法: %w", err)
	}
	return uint(id), nil
}

// normalizeLimit 归一化分页大小。
func normalizeLimit(limit int) int {
	if limit <= 0 {
		return assistantChatDefaultPageLimit
	}
	if limit > assistantChatMaxPageLimit {
		return assistantChatMaxPageLimit
	}
	return limit
}

// ListConversations 列出当前用户的会话，按 updated_at DESC 倒序，游标分页。
//
// 返回的 nextCursor 为空表示没有更多数据。
func (s *AssistantChatService) ListConversations(owner uint, cursor string, limit int) ([]model.AssistantConversation, string, error) {
	return s.ListConversationsFiltered(owner, AssistantConversationFilter{}, cursor, limit)
}

// AssistantConversationFilter 会话列表筛选（FR-542 §5.1）。
type AssistantConversationFilter struct {
	// Keyword 关键词：命中标题**或任一消息正文**。
	//
	// 只做 LIKE 而不是全文索引：会话量级是「每人几十到几百」，LIKE 足够；
	// 引入 FTS 需要维护索引与重建路径，收益与成本不成比例。
	// 复查条件：单用户会话数进入千级后应改 FTS。
	Keyword string
}

// ListConversationsFiltered 带筛选的会话列表。
func (s *AssistantChatService) ListConversationsFiltered(owner uint, filter AssistantConversationFilter, cursor string, limit int) ([]model.AssistantConversation, string, error) {
	limit = normalizeLimit(limit)
	q := s.db.Where("owner_user_id = ?", owner)
	// 关键词命中：标题 或 未被取代的消息正文。
	//
	// 归属条件仍然在**外层**（owner_user_id），子查询里再带一次是为了
	// 让「伪造会话 id 探测他人消息」也拿不到结果——两道都要有。
	if kw := strings.TrimSpace(filter.Keyword); kw != "" {
		pattern := "%" + escapeLike(kw) + "%"
		q = q.Where(
			"title LIKE ? ESCAPE '!' OR id IN ("+
				"SELECT conversation_id FROM assistant_messages "+
				"WHERE superseded_at IS NULL AND content LIKE ? ESCAPE '!')",
			pattern, pattern)
	}
	if cursor != "" {
		boundaryID, err := decodeAssistantChatCursor(cursor)
		if err != nil {
			return nil, "", fmt.Errorf("%w: %s", ErrAssistantInvalidCursor, err.Error())
		}
		// 子查询同时限定 owner 与未软删：伪造的游标指到别人的行时取不到边界值，
		// 于是返回空页而不是把他人会话的时间戳当作切片点（存在性也不泄露）。
		boundary := "(SELECT updated_at FROM assistant_conversations WHERE id = ? AND owner_user_id = ? AND deleted_at IS NULL)"
		// 括号不可省：与 owner 条件按 AND 拼装时，裸露的 OR 会把 owner 条件一起绕过去。
		q = q.Where("(updated_at < "+boundary+" OR (updated_at = "+boundary+" AND id < ?))",
			boundaryID, owner, boundaryID, owner, boundaryID)
	}
	var rows []model.AssistantConversation
	// 多取一条用于判断「还有下一页」，避免额外一次 count 查询。
	if err := q.Order("updated_at desc, id desc").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", fmt.Errorf("查询会话列表失败: %w", err)
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = encodeAssistantChatCursor(rows[len(rows)-1].ID)
	}
	return rows, next, nil
}

// CreateConversation 新建会话。
//
// title 留空时用「新会话」占位，真正的标题在首轮提交时由用户消息截取（§4.1）。
// 不在这里解析/校验 modelID 是否存在：会话允许指向一个暂时不可用的模型，
// 「可用性」在提交 run 时判定（那时才需要真的调用它）。
func (s *AssistantChatService) CreateConversation(owner uint, title string, modelID *uint) (*model.AssistantConversation, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "新会话"
	}
	if utf8.RuneCountInString(title) > assistantChatTitleMaxRunes {
		return nil, fmt.Errorf("%w: 标题最长 %d 个字符", ErrAssistantInvalid, assistantChatTitleMaxRunes)
	}
	row := model.AssistantConversation{OwnerUserID: owner, Title: title, ModelID: modelID}
	if err := s.db.Create(&row).Error; err != nil {
		return nil, fmt.Errorf("创建会话失败: %w", err)
	}
	return &row, nil
}

// GetConversation 取会话详情（仅限所有者）。
func (s *AssistantChatService) GetConversation(owner, id uint) (*model.AssistantConversation, error) {
	var row model.AssistantConversation
	err := s.db.Where("id = ? AND owner_user_id = ?", id, owner).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAssistantChatNotFound
		}
		return nil, fmt.Errorf("查询会话失败: %w", err)
	}
	return &row, nil
}

// UpdateConversation 改标题 / 换模型。
//
// 用 *string 与 **uint 表达三态：nil = 不改；**uint 的 nil 内层 = 显式清空（回跟随默认模型）。
// 用零值表示「不改」会让「清空模型」无法与「不动模型」区分。
func (s *AssistantChatService) UpdateConversation(owner, id uint, title *string, modelID **uint) (*model.AssistantConversation, error) {
	if _, err := s.GetConversation(owner, id); err != nil {
		return nil, err
	}
	updates := make(map[string]any, 2)
	if title != nil {
		t := strings.TrimSpace(*title)
		if t == "" {
			return nil, fmt.Errorf("%w: 标题不能为空", ErrAssistantInvalid)
		}
		if utf8.RuneCountInString(t) > assistantChatTitleMaxRunes {
			return nil, fmt.Errorf("%w: 标题最长 %d 个字符", ErrAssistantInvalid, assistantChatTitleMaxRunes)
		}
		updates["title"] = t
	}
	if modelID != nil {
		// 内层 nil 交给 GORM 写成 NULL。
		updates["model_id"] = *modelID
	}
	if len(updates) == 0 {
		return s.GetConversation(owner, id)
	}
	res := s.db.Model(&model.AssistantConversation{}).
		Where("id = ? AND owner_user_id = ?", id, owner).
		Updates(updates)
	if res.Error != nil {
		return nil, fmt.Errorf("更新会话失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		// 可能与上面的 Get 之间被删除了。
		return nil, ErrAssistantChatNotFound
	}
	return s.GetConversation(owner, id)
}

// DeleteConversation 软删除会话（仅限所有者）。
//
// 已有活跃 run 时拒绝（CONVERSATION_BUSY）而不是「删了让它继续跑」：执行体还会往这个
// 会话写事件与消息，删掉之后那些写入会落进一个用户看不见也删不掉的地方——
// 与其留一个幽灵 run，不如让用户先取消。
func (s *AssistantChatService) DeleteConversation(owner, id uint) error {
	if _, err := s.GetConversation(owner, id); err != nil {
		return err
	}
	if active, err := s.findActiveRun(id); err != nil {
		return err
	} else if active != nil {
		return &AssistantConversationBusyError{RunID: active.ID}
	}
	res := s.db.Where("id = ? AND owner_user_id = ?", id, owner).Delete(&model.AssistantConversation{})
	if res.Error != nil {
		return fmt.Errorf("删除会话失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrAssistantChatNotFound
	}
	return nil
}

// ListMessages 分页读取会话消息：倒序取、按正序返回。
//
// 倒序取是为了让 before 游标语义简单（「比这个 id 更早的 N 条」）；正序返回是因为
// 调用方（UI 与历史装配）都按时间顺序消费，让它们各自再 reverse 一次是纯粹的浪费。
func (s *AssistantChatService) ListMessages(owner, conversationID uint, before *uint, limit int) ([]model.AssistantMessage, error) {
	if _, err := s.GetConversation(owner, conversationID); err != nil {
		return nil, err
	}
	limit = normalizeLimit(limit)
	q := s.db.Where("conversation_id = ?", conversationID)
	if before != nil {
		q = q.Where("id < ?", *before)
	}
	var rows []model.AssistantMessage
	if err := q.Order("id desc").Limit(limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询消息失败: %w", err)
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows, nil
}

// ── 提交 run ────────────────────────────────────────────────────────────

// SubmitRun 提交一轮对话，插入 run 后异步执行（202 语义由 router 负责）。
//
// 判序严格照 §6.1，**不要调换**：
//  1. 按 (owner, conversation, idempotencyKey) 查已有 run（键非空时）→ 命中且摘要一致即返回原 run；
//  2. 命中但摘要不同 → ErrAssistantIdempotencyConflict（同一个键被用来提交了不同内容）；
//  3. 未命中再判该会话是否已有活跃 run → ErrAssistantConversationBusy（带出活跃 runId）；
//  4. 插入（run + 用户消息同一事务）；
//  5. 插入若因唯一键冲突失败，**不做重试插入**，而是回读已存在那条并按摘要规则返回。
//
// 为什么 5 不能改成「重试插入」：两个请求同时未命中时，重试只会把竞争窗口再打开一次；
// 而回读能保证「即使 A 已结束并释放了活跃槽，B 也不会插入第二条」。
//
// 模型解析（第 4 步之前）刻意放在幂等/busy 判定**之后**：幂等重放不应因模型配置后来
// 变更（停用/删除）而失败——重放要回答的是「上一次那个 run 是哪一条」，与今天的配置无关。
func (s *AssistantChatService) SubmitRun(ctx context.Context, owner, conversationID uint, body SubmitRunBody, idempotencyKey string) (*model.AssistantRun, error) {
	conv, err := s.GetConversation(owner, conversationID)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		return nil, fmt.Errorf("%w: 本轮消息正文不能为空", ErrAssistantInvalid)
	}
	if utf8.RuneCountInString(text) > assistantChatUserMessageMaxRunes {
		return nil, fmt.Errorf("%w: 单轮消息正文最长 %d 个字符", ErrAssistantInvalid, assistantChatUserMessageMaxRunes)
	}
	key := strings.TrimSpace(idempotencyKey)
	digest := digestRequestBody(text, conv.ModelID)

	// ① 唯一键命中
	if key != "" {
		existing, err := s.findRunByIdemKey(owner, conversationID, key)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return replayRun(existing, digest)
		}
	}

	// ③ 会话活跃 run
	active, err := s.findActiveRun(conversationID)
	if err != nil {
		return nil, err
	}
	if active != nil {
		// 同键并发：自己的另一路刚把它插进来。这不是「忙碌」，是幂等的第二条路径——
		// 把并发同键请求判成 409 会让正常的「重试按钮」变成不可恢复的错误。
		if key != "" && active.IdempotencyKey != nil && *active.IdempotencyKey == key {
			return replayRun(active, digest)
		}
		return nil, &AssistantConversationBusyError{RunID: active.ID}
	}

	// 跨会话并发上限。注意这是**资源护栏**而不是硬不变量：真正的硬约束是单会话单活跃
	// run 的数据库唯一索引；此处是「先数再插」，极端并发下可能放过一个，
	// 代价是多一轮上游调用，远小于给它加一张计数器表并让所有终态写入都与它同步。
	var activeCount int64
	if err := s.db.WithContext(ctx).Model(&model.AssistantRun{}).
		Where("owner_user_id = ? AND status IN ?", owner, model.AssistantRunActiveStatuses()).
		Count(&activeCount).Error; err != nil {
		return nil, fmt.Errorf("统计活跃 run 失败: %w", err)
	}
	if activeCount >= int64(s.limits.ConcurrentRunsPerUser) {
		return nil, ErrAssistantTooManyRuns
	}

	snapshot, streamer, err := s.modelPreparer()(conv)
	if err != nil {
		return nil, err
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("序列化模型快照失败: %w", err)
	}
	// FR-518 §6：上下文先做**结构与长度**校验（超 4 KiB 直接拒收），
	// 再按调用者权限重验实体（不存在/无权只剔除该实体）。
	//
	// 顺序不能反：先重验会为了「可能被拒收的请求」白跑一堆查询；
	// 而且拒收时不该留下任何查询痕迹。
	sanitizedCtx, ctxWarnings, err := sanitizeAssistantContext(body.ContextSnapshot)
	if err != nil {
		return nil, err
	}
	revalidated, moreWarnings := s.revalidateAssistantContextEntities(ctx, owner, sanitizedCtx)
	ctxWarnings = append(ctxWarnings, moreWarnings...)
	var contextSnapshot *string
	if revalidated != nil {
		encoded, encErr := json.Marshal(revalidated)
		if encErr != nil {
			return nil, fmt.Errorf("序列化页面上下文失败: %w", encErr)
		}
		v := string(encoded)
		contextSnapshot = &v
	}

	run := &model.AssistantRun{
		ConversationID:    conversationID,
		OwnerUserID:       owner,
		ModelSnapshot:     string(snapshotJSON),
		ToolPolicyVersion: 1,
		ContextSnapshot:   contextSnapshot,
		Status:            model.AssistantRunStatusQueued,
		Version:           1,
	}
	if key != "" {
		run.IdempotencyKey = &key
		d := digest
		run.RequestDigest = &d
	}

	// ④ 插入：run 与用户消息同事务，避免「run 建了但用户消息没落库」导致历史里少一轮输入。
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(run).Error; err != nil {
			return err
		}
		turn, err := nextTurn(tx, conversationID)
		if err != nil {
			return err
		}
		content, _ := truncateToBytes(text, assistantChatAssistantMessageMax)
		msg := model.AssistantMessage{
			ConversationID: conversationID,
			Role:           model.AssistantMessageRoleUser,
			Content:        content,
			Turn:           turn,
			State:          model.AssistantMessageStateComplete,
		}
		if err := tx.Create(&msg).Error; err != nil {
			return err
		}
		// 首轮顺带把标题从占位改成用户消息截取（§4.1）。turn==1 即本会话的第一条输入。
		if turn == 1 {
			title := truncateRunes(text, assistantChatAutoTitleRunes)
			if err := tx.Model(&model.AssistantConversation{}).Where("id = ?", conversationID).
				Updates(map[string]any{"title": title}).Error; err != nil {
				return err
			}
		}
		// 上下文裁剪告警（FR-518 §6）：**与 run 同事务**落成事件。
		// 放在这里而不是执行器启动后，是为了让「提交成功」与「告警可见」保持一致——
		// 否则 run 若立刻被取消，用户永远看不到「上下文有项被剔除」。
		seq := 1
		for _, w := range ctxWarnings {
			payload, encErr := marshalBoundedEventPayload(
				map[string]any{"field": w.Field, "reason": w.Reason},
				s.limits.EventPayloadMax,
			)
			if encErr != nil {
				continue
			}
			ev := model.AssistantEvent{
				ConversationID: conversationID,
				RunID:          run.ID,
				Seq:            seq,
				Type:           model.AssistantEventTypeContextWarning,
				Data:           payload,
			}
			if err := tx.Create(&ev).Error; err != nil {
				return err
			}
			seq++
		}
		return nil
	})
	if err != nil {
		if isUniqueViolation(err) {
			// ⑤ 冲突即回读：不重试插入。
			return s.resolveSubmitConflict(owner, conversationID, key, digest)
		}
		return nil, fmt.Errorf("创建 run 失败: %w", err)
	}

	// active_run_id 是冗余指针（不是约束）：只在确实空着时写，避免覆盖并发的另一次提交。
	if err := s.db.WithContext(ctx).Model(&model.AssistantConversation{}).
		Where("id = ? AND active_run_id IS NULL", conversationID).
		Updates(map[string]any{"active_run_id": run.ID}).Error; err != nil {
		slog.Warn("助手运行时：写 active_run_id 失败", "runId", run.ID, "error", err)
	}

	s.startExecutor(owner, run, snapshot, streamer)
	return run, nil
}

// replayRun 按 §6.1 的摘要规则返回或拒绝重放。
func replayRun(existing *model.AssistantRun, digest string) (*model.AssistantRun, error) {
	if existing.RequestDigest == nil || *existing.RequestDigest != digest {
		return nil, ErrAssistantIdempotencyConflict
	}
	return existing, nil
}

// resolveSubmitConflict 处理插入时的唯一键冲突（§6.1「插入冲突即回读」）。
//
// 两条约束可能触发冲突，必须按顺序辨认：先看幂等键（命中就按摘要规则返回），
// 再看会话活跃 run（回 CONVERSATION_BUSY 并带出 runId）。都不命中说明是第三种冲突
// （理论上不存在），回 ErrAssistantChatConflict 而不是假装成功。
func (s *AssistantChatService) resolveSubmitConflict(owner, conversationID uint, key, digest string) (*model.AssistantRun, error) {
	if key != "" {
		existing, err := s.findRunByIdemKey(owner, conversationID, key)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return replayRun(existing, digest)
		}
	}
	active, err := s.findActiveRun(conversationID)
	if err != nil {
		return nil, err
	}
	if active != nil {
		return nil, &AssistantConversationBusyError{RunID: active.ID}
	}
	return nil, ErrAssistantChatConflict
}

// findRunByIdemKey 按幂等唯一键查已有 run（键为空时直接返回 nil）。
func (s *AssistantChatService) findRunByIdemKey(owner, conversationID uint, key string) (*model.AssistantRun, error) {
	if key == "" {
		return nil, nil
	}
	var rows []model.AssistantRun
	err := s.db.Where("owner_user_id = ? AND conversation_id = ? AND idempotency_key = ?", owner, conversationID, key).
		Order("id asc").Limit(1).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("查询幂等键失败: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// findActiveRun 查会话当前的活跃 run（不存在返回 nil）。
func (s *AssistantChatService) findActiveRun(conversationID uint) (*model.AssistantRun, error) {
	var rows []model.AssistantRun
	err := s.db.Where("conversation_id = ? AND status IN ?", conversationID, model.AssistantRunActiveStatuses()).
		Order("id desc").Limit(1).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("查询会话活跃 run 失败: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// startExecutor 注册取消函数并启动执行体。
//
// 先注册再起 goroutine：反过来的话，第一瞬间到达的 Cancel 会找不到 cancel 函数，
// 于是取消只改了数据库状态，执行体却在继续推进（它会一路跑完才发现自己被取消了）。
//
// 执行体拿到的是 run 的**副本**：原指针已经交回调用方（router 要序列化它），
// 而执行体会推进 Version/Status——共享同一个结构等于让调用方的读与执行体的写竞争。
func (s *AssistantChatService) startExecutor(owner uint, run *model.AssistantRun, snapshot assistantModelSnapshot, streamer ai.Streamer) {
	execRun := *run
	ctx, cancel := context.WithCancel(s.runCtx)
	s.cancelMu.Lock()
	s.cancels[execRun.ID] = cancel
	s.cancelMu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		s.executeRun(ctx, owner, &execRun, snapshot, streamer)
	}()
}

// forgetCancel 清理 run 的取消函数（终态时调用）。
func (s *AssistantChatService) forgetCancel(runID uint) {
	s.cancelMu.Lock()
	delete(s.cancels, runID)
	s.cancelMu.Unlock()
}

// takeCancel 取出并删除 run 的取消函数；没有则返回 nil。
func (s *AssistantChatService) takeCancel(runID uint) context.CancelFunc {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	cancel := s.cancels[runID]
	delete(s.cancels, runID)
	return cancel
}

// ── 执行体 ──────────────────────────────────────────────────────────────

// runExecutor 一次 run 的执行上下文。整个结构由单个 goroutine 独占，无需加锁；
// 唯一与其它 goroutine 共享的是事件写入器（sink 自带锁）。
type runExecutor struct {
	svc      *AssistantChatService
	owner    uint
	run      *model.AssistantRun
	snap     assistantModelSnapshot
	streamer ai.Streamer
	limits   assistantChatLimits
	sink     *assistantEventSink

	messages []ai.Message
	tools    []ai.ToolSpec

	// turn 本 run 内所有消息的轮次号：用户的输入与由它引发的回复属于同一轮，
	// 轮次只在会话层面随新 run 递增。
	turn       int
	toolRounds int
	toolCalls  int
	// usageInput / usageOutput 只累积供应商**真实**报告值；缺失保持 nil，不补 0。
	usageInput  *int
	usageOutput *int
	// endNote 收尾时要写进 run_end 的人类可读说明（如预算耗尽）；空表示无需说明。
	endNote string

	// pendingConfirmation 非空表示本轮因等待用户确认而结束（FR-516 §5.1）。
	// 收尾时据此把 run 置于 waiting_confirmation，而不是当成终态。
	pendingConfirmation *assistantConfirmationView

	// delegatedExec 本 run 专属的委托执行器（FR-516）。
	//
	// **必须每 run 一份**：它绑定 runID/owner/sink。若挂在 service 上做「临时注入」，
	// 两个 run 并发时会互相覆盖（后启动的那个把前一个的执行器换掉），
	// 于是事件写进错误的 run、票据记错调用方。
	delegatedExec ToolExecutor

	// toolExec 本次实际使用的执行器：装配级注入（测试/自定义）优先，
	// 否则用本 run 的委托执行器。
	//
	// 为什么装配级优先：`SetToolExecutor` 是既有 seam（FR-515 就有的注入点），
	// 测试与将来的自定义执行器都靠它；若被无条件覆盖，那批用例会静默变成
	// 「没有工具」而失败（实测：BudgetExhausted 因工具轮不发生而变成 completed）。
	toolExec ToolExecutor
}

// executeRun 执行体主体：拾取 → 循环 → 收尾。
func (s *AssistantChatService) executeRun(ctx context.Context, owner uint, run *model.AssistantRun, snap assistantModelSnapshot, streamer ai.Streamer) {
	// 拾取：queued → running。CAS 失败说明已被取消或已被回收：此时 run_end 由「谁把它
	// 推进到终态的人」写，但执行体是唯一活着的写入者的情况更常见，故统一在这里兜底写。
	moved, err := s.casRunStatus(run.ID, run.Version, []string{model.AssistantRunStatusQueued, model.AssistantRunStatusWaitingConfirmation},
		model.AssistantRunStatusRunning, map[string]any{"started_at": time.Now()})
	if err != nil {
		slog.Error("助手运行时：拾取 run 失败", "runId", run.ID, "error", err)
		return
	}
	if !moved {
		s.finishWithoutTransition(run)
		return
	}
	run.Version++
	run.Status = model.AssistantRunStatusRunning

	// 本 run 的 limits 在全局默认值之上叠加**本轮冻结快照**里的能力声明：
	// 上下文预算随模型而定，不能用一个全局常量冒充（同一实例上不同模型窗口可能差一个量级）。
	// 快照里的 ContextWindow 为 nil 表示上游未声明，此时保持 0（未知）→ 不做预算裁剪。
	runLimits := s.limits
	if snap.ContextWindow != nil && *snap.ContextWindow > 0 {
		runLimits.ContextTokenBudget = *snap.ContextWindow
	}

	sink := newAssistantEventSink(s.db, run, runLimits)
	sink.hub = s.hub

	prevTools := s.toolExecutor()
	ex := &runExecutor{
		svc:      s,
		owner:    owner,
		run:      run,
		snap:     snap,
		streamer: streamer,
		limits:   runLimits,
		sink:     sink,

		// 每 run 一份委托执行器（FR-516）：绑定 runID/owner/sink，负责出票与回执。
		delegatedExec: &assistantDelegatedExecutor{
			svc:            s,
			runID:          run.ID,
			conversationID: run.ConversationID,
			owner:          owner,
			sink:           sink,
		},
	}
	// 装配级注入优先（见 toolExec 字段注释）。
	ex.toolExec = prevTools
	if ex.toolExec == nil {
		ex.toolExec = ex.delegatedExec
	}
	// 本 run 的轮次号 = 会话当前最大轮次（用户消息已在 SubmitRun 里以该轮次落库）。
	if turn, err := nextTurn(s.db, run.ConversationID); err == nil {
		ex.turn = turn - 1
	}

	// run 总预算（墙钟）。它包住整个循环，因此工具执行也计在内。
	runCtx, cancel := context.WithTimeout(ctx, s.limits.RunWallClock)
	defer cancel()

	status, reason := ex.loop(runCtx)
	s.finishRun(run, sink, status, reason, ex.endNote, ex.usageInput, ex.usageOutput)
}

// finishWithoutTransition 处理「拾取时 run 已不在活跃态」的情形。
func (s *AssistantChatService) finishWithoutTransition(run *model.AssistantRun) {
	var cur model.AssistantRun
	if err := s.db.First(&cur, run.ID).Error; err != nil {
		slog.Warn("助手运行时：读取 run 失败", "runId", run.ID, "error", err)
		return
	}
	sink := newAssistantEventSink(s.db, &cur, s.limits)
	sink.hub = s.hub
	reason := ""
	if cur.TerminationReason != nil {
		reason = *cur.TerminationReason
	}
	if err := sink.ensureRunEnd(cur.Status, reason, ""); err != nil {
		slog.Warn("助手运行时：补写 run_end 失败", "runId", cur.ID, "error", err)
	}
	s.clearActiveRun(cur.ConversationID, cur.ID)
	s.forgetCancel(cur.ID)
}

// loop 驱动「模型流 → 工具 → 回灌」的循环，返回终态与原因。
func (ex *runExecutor) loop(ctx context.Context) (string, string) {
	history, err := ex.svc.buildHistory(ctx, ex.run.ConversationID, ex.sink, ex.limits)
	if err != nil {
		ex.emitError(ai.CodeUpstreamError, "装配对话历史失败")
		return model.AssistantRunStatusFailed, model.AssistantTerminationFailed
	}
	ex.messages = history

	// 工具只为「模型声明了 tools 且确实注入了执行器」的 run 注册。
	// 用 ex.toolExec（本 run 专属）而不是 service 级 getter：后者是「装配时注入的默认
	// 执行器」，对聊天路径而言应为本 run 的委托执行器，否则并发 run 会互相干扰。
	if ex.snap.Tools {
		if exec := ex.toolExec; exec != nil {
			ex.tools = exec.Available(ctx, ex.owner)
		}
	}

	for {
		result, idleFired, err := ex.streamOnce(ctx)
		if err != nil {
			return ex.classifyStreamFailure(ctx, err, idleFired)
		}
		// 文本增量合并成**一条** assistant 消息：逐 delta 落库会让消息表被碎片填满，
		// 而前端补读消息时拿到的是几百条半句话。
		msgID := ex.persistAssistantMessage(result.Text, result.Calls)

		if len(result.Calls) == 0 {
			return model.AssistantRunStatusCompleted, model.AssistantTerminationCompleted
		}
		// 预算：轮次与调用次数任一超限即强制收尾（§7 / §9）。
		// 说明写进 run_end 的 note（§9 要求终态事件说明「工具轮次已达上限」），
		// 而不是发 context_warning——后者语义是「上下文里有内容被剔除」，两件事。
		if ex.toolRounds >= ex.limits.MaxToolRounds || ex.toolCalls+len(result.Calls) > ex.limits.MaxToolCalls {
			ex.endNote = fmt.Sprintf("工具轮次已达上限（%d 轮 / %d 次调用），本轮强制收尾",
				ex.limits.MaxToolRounds, ex.limits.MaxToolCalls)
			return model.AssistantRunStatusCompleted, model.AssistantTerminationBudgetExhausted
		}
		if len(ex.tools) == 0 || ex.toolExec == nil {
			// 没有任何可用工具却拿到了工具调用：不接受也不执行（否则会凭空产生副作用），
			// 直接把这一轮按普通回复收尾。
			//
			// 判据必须是 **ex.toolExec**（本 run 的执行器），不能是 svc.toolExecutor()：
			// 后者只在装配级注入时才非 nil，而聊天路径从不调用 SetToolExecutor——
			// 它用的是本 run 的委托执行器。用 svc 级判据会让这里恒为真，
			// 于是模型**每一次**工具调用都被静默降级成普通回复：
			// 不执行、不报错、assistant 消息为空，只剩一条 run_end。
			return model.AssistantRunStatusCompleted, model.AssistantTerminationCompleted
		}
		if done, status, reason := ex.executeToolCalls(ctx, result.Calls, msgID); done {
			return status, reason
		}
		ex.toolRounds++
	}
}

// assistantStreamResult 一次模型流的结果。
type assistantStreamResult struct {
	// Text 本轮累积的文本（已合并所有增量）。
	Text string
	// Calls 本轮产生的完整工具调用（按到达顺序）。
	Calls []ai.ToolCall
}

// streamOnce 发起一次模型请求并把事件转成持久化事件。
//
// 三种超时分工不同，别合并：单次请求（MODEL_REQUEST_TIMEOUT）、供应商静默
// （IDLE_STREAM_TIMEOUT）、run 总预算（RUN_WALL_CLOCK，在 loop 外层）。
func (ex *runExecutor) streamOnce(parent context.Context) (assistantStreamResult, bool, error) {
	ctx, cancel := context.WithTimeout(parent, ex.limits.ModelRequestTimeout)
	defer cancel()

	var idleFired atomic.Bool
	idle := time.AfterFunc(ex.limits.IdleStreamTimeout, func() {
		idleFired.Store(true)
		cancel()
	})
	defer idle.Stop()

	var text strings.Builder
	var calls []ai.ToolCall
	var streamErr error
	var sinkErr error

	emit := func(ev ai.Event) error {
		// 任何事件都算「供应商还活着」，静默计时重置。
		idle.Reset(ex.limits.IdleStreamTimeout)
		switch ev.Kind {
		case ai.EventText:
			if ev.Text == "" {
				return nil
			}
			text.WriteString(ev.Text)
			if err := ex.sink.append(model.AssistantEventTypeTextDelta, map[string]any{"text": ev.Text}, nil, nil); err != nil {
				sinkErr = err
				return err
			}
		case ai.EventToolCallEnd:
			calls = append(calls, ai.ToolCall{CallID: ev.CallID, ToolName: ev.ToolName, Args: ev.Args})
		case ai.EventUsage:
			if ev.Usage == nil {
				return nil
			}
			ex.recordUsage(ev.Usage)
			payload := map[string]any{}
			if ev.Usage.InputTokens != nil {
				payload["input"] = *ev.Usage.InputTokens
			}
			if ev.Usage.OutputTokens != nil {
				payload["output"] = *ev.Usage.OutputTokens
			}
			// 缺失则不发此事件（§5.3）：一条 input/output 都为空的 usage 事件
			// 会被前端当成「本次消耗 0」。
			if len(payload) == 0 {
				return nil
			}
			if err := ex.sink.append(model.AssistantEventTypeUsage, payload, nil, nil); err != nil {
				sinkErr = err
				return err
			}
		}
		return nil
	}

	req := ai.Request{
		Model: ex.snap.UpstreamModelID,
		// 系统提示词 = 基础提示 + 已启用技能（FR-541）。
		// 技能只影响「怎么做事」的指令文本，不放宽任何工具权限——
		// 写操作仍然逐个走确认（FR-516）。
		// 系统提示词 = 基线 + 自定义 + 技能 + **页面引用标记**（FR-544）。
		//
		// 页面上下文只带「当前在哪、看的是哪个实体 id」——不带名称、状态、指标。
		// 原因：那些内容会过期（用户页面停留久了状态早变），也可能绕开工具的
		// 权限回填；让模型用工具现查，拿到的一定是最新且过了权限的数据。
		System:          ex.svc.systemPromptWithSkills(assistantChatSystemPrompt) + pageRefSuffix(ex.run),
		Messages:        ex.messages,
		Tools:           ex.tools,
		MaxOutputTokens: ex.snap.maxOutputTokens(),
		// 思考强度取自本轮冻结的模型快照（FR-541 §3.2）。
		ReasoningEffort: ex.snap.ReasoningEffort,
	}
	streamErr = ex.streamer.Stream(ctx, req, emit)
	if sinkErr != nil {
		// 事件写不进去（DB 故障）比上游失败更严重：此刻中断，让 run 落 failed，
		// 而不是继续产出一批无法补读的回复。
		return assistantStreamResult{}, false, sinkErr
	}
	if streamErr != nil {
		return assistantStreamResult{}, idleFired.Load(), streamErr
	}
	return assistantStreamResult{Text: text.String(), Calls: calls}, idleFired.Load(), nil
}

// recordUsage 累积供应商报告的真实用量。
//
// 多轮工具循环会有多次模型请求，run 级用量是各次**真实报告值**之和；
// 任一次未报告就保持 nil，不补 0（shared-contracts §4.1）。
func (ex *runExecutor) recordUsage(u *ai.Usage) {
	if u.InputTokens != nil {
		v := *u.InputTokens
		if ex.usageInput != nil {
			v += *ex.usageInput
		}
		ex.usageInput = &v
	}
	if u.OutputTokens != nil {
		v := *u.OutputTokens
		if ex.usageOutput != nil {
			v += *ex.usageOutput
		}
		ex.usageOutput = &v
	}
}

// classifyStreamFailure 把流失败映射成终态（§5.1 的可达终态 + 已脱敏分类码）。
func (ex *runExecutor) classifyStreamFailure(ctx context.Context, err error, idleFired bool) (string, string) {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		// run 总预算耗尽（单次请求超时不会让外层 ctx 变成 DeadlineExceeded）。
		ex.emitError(ai.CodeUpstreamTimeout, "本轮运行已超出总时长预算")
		return model.AssistantRunStatusTimedOut, model.AssistantTerminationTimedOut
	case errors.Is(ctx.Err(), context.Canceled):
		// 用户取消：不写 error 事件（取消不是错误，前端已收到 run_end cancelled）。
		return model.AssistantRunStatusCancelled, model.AssistantTerminationCancelled
	case idleFired:
		ex.emitError(ai.CodeUpstreamTimeout, "供应商长时间无响应，已判定失败")
		return model.AssistantRunStatusFailed, model.AssistantTerminationFailed
	default:
		code := ai.Classify(err)
		ex.emitError(code, err.Error())
		return model.AssistantRunStatusFailed, model.AssistantTerminationFailed
	}
}

// persistAssistantMessage 写入本该轮的 assistant 消息，返回其 ID。
//
// 有工具调用的轮次即使正文为空也落一条消息：它是「调用 → 结果」的配对锚点，
// 少了它，历史装配时那组工具调用就没有宿主，整轮会被丢弃。
//
// 同时把本轮追加进**内存里的**消息序列，且工具调用必须是结构化的（§7.1）：
// 下一轮请求要带上它，否则模型看不到自己刚才发起了哪些调用。
func (ex *runExecutor) persistAssistantMessage(text string, calls []ai.ToolCall) uint {
	if text == "" && len(calls) == 0 {
		return 0
	}
	content, truncated := truncateToBytes(text, assistantChatAssistantMessageMax)
	state := model.AssistantMessageStateComplete
	if truncated {
		state = model.AssistantMessageStateTruncated
	}
	runID := ex.run.ID
	msg := model.AssistantMessage{
		ConversationID: ex.run.ConversationID,
		RunID:          &runID,
		Role:           model.AssistantMessageRoleAssistant,
		Content:        content,
		Turn:           ex.turn,
		State:          state,
	}
	ex.messages = append(ex.messages, ai.Message{Role: "assistant", Content: content, ToolCalls: calls})
	if err := ex.svc.db.Create(&msg).Error; err != nil {
		slog.Warn("助手运行时：写入助手消息失败", "runId", ex.run.ID, "error", err)
		return 0
	}
	return msg.ID
}

// executeToolCalls 依次执行工具调用，返回是否应结束 run。
func (ex *runExecutor) executeToolCalls(ctx context.Context, calls []ai.ToolCall, assistantMsgID uint) (bool, string, string) {
	exec := ex.toolExec
	if exec == nil {
		return true, model.AssistantRunStatusCompleted, model.AssistantTerminationCompleted
	}
	for _, call := range calls {
		if ctx.Err() != nil {
			// 未派发的工具不执行（§9「停止 run：未派发工具不执行」）。
			return true, model.AssistantRunStatusCancelled, model.AssistantTerminationCancelled
		}
		callID := call.CallID
		args := json.RawMessage(call.Args)
		if !json.Valid(args) {
			args = json.RawMessage(`{}`)
		}
		if err := ex.sink.append(model.AssistantEventTypeToolCall, map[string]any{
			"callId":   call.CallID,
			"name":     call.ToolName,
			"argsJson": args,
		}, anchorID(assistantMsgID), &callID); err != nil {
			return true, model.AssistantRunStatusFailed, model.AssistantTerminationFailed
		}
		if err := ex.sink.appendStatus(call.CallID, "running", ""); err != nil {
			return true, model.AssistantRunStatusFailed, model.AssistantTerminationFailed
		}

		result, execErr := exec.Execute(ctx, ex.owner, call)

		// 写操作已出票、等待用户确认（FR-516 §5.1）：本轮到此为止，不执行、不回灌。
		//
		// 为什么让 run 停在这里而不是「跳过该工具继续跑」：
		// 模型是在「工具已经执行完」的假设下继续推理的，跳过会让它在错误前提上
		// 继续编造结论。停下并把状态交给用户，是唯一诚实的处理。
		var pending *assistantConfirmationPendingError
		if errors.As(execErr, &pending) {
			if err := ex.sink.appendStatus(call.CallID, "pending_confirmation", ""); err != nil {
				return true, model.AssistantRunStatusFailed, model.AssistantTerminationFailed
			}
			ex.pendingConfirmation = pending.view
			return true, model.AssistantRunStatusWaitingConfirmation, model.AssistantTerminationAwaitingConfirm
		}

		statusWord, errCode := "ok", ""
		if execErr != nil {
			statusWord, errCode = "failed", string(ai.Classify(execErr))
			// 失败也回灌一条 tool 消息：不回灌的话模型会一直重试同一个必然失败的调用，
			// 直到把轮次预算烧光。
			result = "工具执行失败：" + execErr.Error()
		}
		if err := ex.sink.appendStatus(call.CallID, statusWord, errCode); err != nil {
			return true, model.AssistantRunStatusFailed, model.AssistantTerminationFailed
		}
		content, truncated := truncateToBytes(result, ex.limits.ToolResultMax)
		preview := truncateRunes(content, assistantChatToolPreviewRunes)
		if err := ex.sink.append(model.AssistantEventTypeToolResult, map[string]any{
			"callId":    call.CallID,
			"preview":   preview,
			"truncated": truncated,
		}, nil, &callID); err != nil {
			return true, model.AssistantRunStatusFailed, model.AssistantTerminationFailed
		}

		state := model.AssistantMessageStateComplete
		if truncated {
			state = model.AssistantMessageStateTruncated
		}
		runID := ex.run.ID
		toolMsg := model.AssistantMessage{
			ConversationID: ex.run.ConversationID,
			RunID:          &runID,
			Role:           model.AssistantMessageRoleTool,
			Content:        content,
			ToolCallID:     &callID,
			Turn:           ex.turn,
			State:          state,
		}
		if err := ex.svc.db.Create(&toolMsg).Error; err != nil {
			slog.Warn("助手运行时：写入工具消息失败", "runId", ex.run.ID, "callId", call.CallID, "error", err)
			return true, model.AssistantRunStatusFailed, model.AssistantTerminationFailed
		}
		ex.messages = append(ex.messages, ai.Message{
			Role:       "tool",
			Content:    content,
			ToolCallID: call.CallID,
			ToolName:   call.ToolName,
		})
		ex.toolCalls++
	}
	return false, "", ""
}

// ── 事件写入 ────────────────────────────────────────────────────────────

// assistantEventSink 为一次 run 追加事件，维护从 1 起的连续 seq（§5.3）。
//
// 为什么用「内存计数器 + 单写入者」而不是 SQL 里的 max(seq)+1：读 max 与随后的写之间
// 有窗口，两个写入方会撞 (run_id, seq) 唯一键，结果是丢事件或留空洞——而空洞会直接
// 破坏前端的补读游标判定（它无法区分「漏收」与「本来就没有」）。
type assistantEventSink struct {
	db      *gorm.DB
	runID   uint
	convID  uint
	limit   int
	mu      sync.Mutex
	nextSeq int
	// hub 是可选的运行期广播器；nil 表示无人订阅（测试与单机路径常态）。
	// 广播**只在落库成功之后**发生（§5「先落库后发布」）：先发后写会让订阅者
	// 拿到一条库中并不存在的事件，断流补读时对不上 seq，前端会永久卡在空洞处。
	hub *assistantEventHub
}

// newAssistantEventSink 构造事件写入器，seq 从库内已有最大值 +1 续接。
func newAssistantEventSink(db *gorm.DB, run *model.AssistantRun, limits assistantChatLimits) *assistantEventSink {
	sink := &assistantEventSink{db: db, runID: run.ID, convID: run.ConversationID, limit: limits.EventPayloadMax, nextSeq: 1}
	var maxSeq int
	if err := db.Model(&model.AssistantEvent{}).Where("run_id = ?", run.ID).
		Select("COALESCE(MAX(seq), 0)").Scan(&maxSeq).Error; err == nil {
		sink.nextSeq = maxSeq + 1
	}
	return sink
}

// append 追加一条事件。seq 只在插入成功后前进，失败时下一次仍用同一个 seq。
func (s *assistantEventSink) append(typ string, data map[string]any, messageID *uint, callID *string) error {
	payload, err := marshalBoundedEventPayload(data, s.limit)
	if err != nil {
		return fmt.Errorf("序列化事件载荷失败: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := model.AssistantEvent{
		ConversationID: s.convID,
		RunID:          s.runID,
		Seq:            s.nextSeq,
		Type:           typ,
		MessageID:      messageID,
		CallID:         callID,
		Data:           payload,
	}
	if err := s.db.Create(&ev).Error; err != nil {
		return fmt.Errorf("写入事件失败: %w", err)
	}
	s.nextSeq++
	// 落库之后才广播：订阅者收到的事件一定能在库里补读到（§5「先落库后发布」）。
	if s.hub != nil {
		s.hub.publish(ev)
	}
	return nil
}

// appendStatus 追加一条 tool_status 事件。
func (s *assistantEventSink) appendStatus(callID, status, errCode string) error {
	data := map[string]any{"callId": callID, "status": status}
	if errCode != "" {
		data["errorCode"] = errCode
	}
	id := callID
	return s.append(model.AssistantEventTypeToolStatus, data, nil, &id)
}

// ensureRunEnd 保证该 run 恰好有一条 run_end 事件。
//
// 幂等：已经存在就跳过。回收器与「取消一条没有活执行体的 run」都会走到这里，
// 重复执行（例如两次启动回收）不得产出第二条终态事件——前端据它判定「本轮流结束了」。
//
// note 是可选的人类可读说明（如「工具轮次已达上限」）。放在 run_end 而不是另造事件类型：
// §5.3 的事件集是最小集，而「为什么这样收尾」本来就属于终态事件自己的语义。
func (s *assistantEventSink) ensureRunEnd(status, reason, note string) error {
	var count int64
	if err := s.db.Model(&model.AssistantEvent{}).
		Where("run_id = ? AND type = ?", s.runID, model.AssistantEventTypeRunEnd).Count(&count).Error; err != nil {
		return fmt.Errorf("查询 run_end 事件失败: %w", err)
	}
	if count > 0 {
		return nil
	}
	data := map[string]any{"status": status, "reason": reason}
	if note != "" {
		data["note"] = note
	}
	return s.append(model.AssistantEventTypeRunEnd, data, nil, nil)
}

// anchorID 把「可能为 0 的消息 ID」转成可空锚点。
func anchorID(id uint) *uint {
	if id == 0 {
		return nil
	}
	return &id
}

// marshalBoundedEventPayload 序列化事件载荷并保证不超过 limit 字节（EVENT_PAYLOAD_MAX）。
//
// 截断策略：只截**最长的字符串字段**并在对象里加 "truncated": true。
// 为什么不直接切 JSON 文本：切出来的载荷不是合法 JSON，前端解析失败就什么也显示不出来，
// 那比截断更糟。为什么不是通篇截断：事件载荷是结构化契约，键必须还在。
func marshalBoundedEventPayload(data map[string]any, limit int) (string, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	if limit <= 0 || len(raw) <= limit {
		return string(raw), nil
	}
	name, maxLen := "", 0
	for k, v := range data {
		if str, ok := v.(string); ok && len(str) > maxLen {
			name, maxLen = k, len(str)
		}
	}
	if name == "" {
		return `{"truncated":true}`, nil
	}
	orig := data[name].(string)
	total := utf8.RuneCountInString(orig)
	build := func(runes int) (string, bool, error) {
		clone := make(map[string]any, len(data)+1)
		for k, v := range data {
			clone[k] = v
		}
		head := truncateRunes(orig, runes)
		clone[name] = head
		clone["truncated"] = true
		b, err := json.Marshal(clone)
		if err != nil {
			return "", false, err
		}
		return string(b), len(b) <= limit, nil
	}
	// 二分最大的可容纳 rune 数（截断后的值必须仍能解析，且 marked）。
	lo, hi, best := 0, total, ""
	for lo <= hi {
		mid := (lo + hi) / 2
		payload, fits, err := build(mid)
		if err != nil {
			return "", err
		}
		if fits {
			best = payload
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if best == "" {
		return `{"truncated":true}`, nil
	}
	return best, nil
}

// ── 历史装配（§7.1）────────────────────────────────────────────────────

// assistantToolCallAnchor 从 tool_call 事件里还原出的工具调用锚点。
//
// Args 用 json.RawMessage 而不是 string：事件里的 argsJson 是以**内联 JSON 对象**写入的
// （见 executeToolCalls），声明成 string 会让 json.Unmarshal 直接失败，进而把这次调用
// 静默丢掉——历史里只剩一条没有宿主的 tool 消息，模型下一轮就看不到自己调过什么。
type assistantToolCallAnchor struct {
	CallID string          `json:"callId"`
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"argsJson"`
}

// argsText 取回参数原文。
//
// 兼容两种历史形态：早期/外部写入可能是「JSON 字符串」而非内联对象，故先看首字节，
// 是引号就按字符串解码一次，否则视作已是 JSON 原文。
func (a assistantToolCallAnchor) argsText() string {
	if len(a.Args) == 0 {
		return ""
	}
	if a.Args[0] == '"' {
		var s string
		if err := json.Unmarshal(a.Args, &s); err == nil {
			return s
		}
	}
	return string(a.Args)
}

// buildHistory 取最近 HISTORY_WINDOW 条消息并装配成模型输入。
//
// 两个必须分开的概念（§6.2）：这里的窗口只是**外发窗口**，库内消息全量保留；
// 因此补读游标不会因为窗口而失效。
//
// §7.1 的硬约束在这里落地：assistant 的工具调用必须作为结构化 Message.ToolCalls 外发，
// 与其配对的 tool 消息带 ToolCallID，二者成对、同序；缺任一半的轮次整体丢弃并记一条
// context_warning，**不得**只发结果或只发调用（那会让上游直接判请求非法）。
func (s *AssistantChatService) buildHistory(ctx context.Context, conversationID uint, sink *assistantEventSink, limits assistantChatLimits) ([]ai.Message, error) {
	var rows []model.AssistantMessage
	if err := s.db.WithContext(ctx).Where("conversation_id = ?", conversationID).
		Order("id desc").Limit(limits.HistoryWindow).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询历史消息失败: %w", err)
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	if len(rows) == 0 {
		return nil, nil
	}

	// 工具调用不在 assistant_messages 里（§4.2 没有该列），只存在于 tool_call 事件，
	// 且以 message_id 为锚点——这正是写入时把 tool_call 事件挂到 assistant 消息上的原因。
	runIDs := make([]uint, 0, len(rows))
	seen := make(map[uint]struct{}, len(rows))
	for _, r := range rows {
		if r.RunID == nil {
			continue
		}
		if _, ok := seen[*r.RunID]; ok {
			continue
		}
		seen[*r.RunID] = struct{}{}
		runIDs = append(runIDs, *r.RunID)
	}
	callsByMessage := make(map[uint][]ai.ToolCall, len(rows))
	if len(runIDs) > 0 {
		var events []model.AssistantEvent
		if err := s.db.WithContext(ctx).
			Where("run_id IN ? AND type = ?", runIDs, model.AssistantEventTypeToolCall).
			Order("run_id asc, seq asc").Find(&events).Error; err != nil {
			return nil, fmt.Errorf("查询工具调用事件失败: %w", err)
		}
		for _, ev := range events {
			if ev.MessageID == nil {
				continue
			}
			var anchor assistantToolCallAnchor
			if err := json.Unmarshal([]byte(ev.Data), &anchor); err != nil {
				// 载荷损坏时跳过这一次调用：宁可整轮被下面的配对检查丢弃（并有 warning），
				// 也不要凭残缺数据编一个可能有副作用的调用出来。
				slog.Warn("助手运行时：工具调用事件载荷不可解析", "eventId", ev.ID, "error", err)
				continue
			}
			callID := anchor.CallID
			if callID == "" && ev.CallID != nil {
				callID = *ev.CallID
			}
			callsByMessage[*ev.MessageID] = append(callsByMessage[*ev.MessageID], ai.ToolCall{
				CallID:   callID,
				ToolName: anchor.Name,
				Args:     anchor.argsText(),
			})
		}
	}

	out := make([]ai.Message, 0, len(rows))
	dropped := make([]map[string]any, 0)
	for i := 0; i < len(rows); {
		msg := rows[i]
		switch msg.Role {
		case model.AssistantMessageRoleUser:
			out = append(out, ai.Message{Role: "user", Content: msg.Content})
			i++
		case model.AssistantMessageRoleAssistant:
			calls := callsByMessage[msg.ID]
			if len(calls) == 0 {
				out = append(out, ai.Message{Role: "assistant", Content: msg.Content})
				i++
				continue
			}
			paired := i+len(calls) < len(rows)
			if paired {
				for k, call := range calls {
					next := rows[i+1+k]
					if next.Role != model.AssistantMessageRoleTool || next.ToolCallID == nil || *next.ToolCallID != call.CallID {
						paired = false
						break
					}
				}
			}
			if !paired {
				dropped = append(dropped, map[string]any{
					"type":   "message",
					"id":     msg.ID,
					"reason": "工具调用与其结果不成对或不同序",
				})
				i++
				continue
			}
			out = append(out, ai.Message{Role: "assistant", Content: msg.Content, ToolCalls: calls})
			for k, call := range calls {
				toolMsg := rows[i+1+k]
				out = append(out, ai.Message{
					Role:       "tool",
					Content:    toolMsg.Content,
					ToolCallID: call.CallID,
					ToolName:   call.ToolName,
				})
			}
			i += 1 + len(calls)
		case model.AssistantMessageRoleTool:
			// 没有宿主调用的孤立 tool 消息（窗口边界截断等）：不能单独外发。
			dropped = append(dropped, map[string]any{
				"type":   "message",
				"id":     msg.ID,
				"reason": "工具结果缺少配对的工具调用",
			})
			i++
		default:
			i++
		}
	}
	if len(dropped) > 0 && sink != nil {
		// 剔除不阻断 run（§5.3）：发一条 context_warning 让用户知道模型少看了一段。
		_ = sink.append(model.AssistantEventTypeContextWarning, map[string]any{
			"dropped": dropped,
			"message": "部分历史轮次因工具调用与结果不成对被排除",
		}, nil, nil)
	}
	out = trimToContextBudget(out, limits, dropped, sink)
	return out, nil
}

// assistantChatBytesPerToken 是「每 token 最多占多少 UTF-8 字节」的保守上界。
//
// 为什么不按真实 tokenizer 算：仓库没有 tokenizer 依赖，也不该为一个裁剪阈值引入一份
// 巨型词表。而**按字符数估算**是更糟的选择——它把中文当 1 个 token（实际约 1~2 字节/token），
// 会系统性低估、把请求喂到超过上游上限，然后拿到一个与本函数无关的 400。
//
// 因此这里反过来做：取「1 token 至少 4 字节」的常见下界，把预算换算成字节**下限**，
// 于是裁剪只会「裁得偏早」，不会「裁得偏晚」。偏早的代价是模型少看几条历史，
// 偏晚的代价是整个 run 被上游拒绝——两者不对称，故宁可保守。
const assistantChatMinBytesPerToken = 4

// trimToContextBudget 按 CONTEXT_TOKEN_BUDGET 从**最旧**的一端裁历史。
//
// 裁剪顺序固定为「先旧消息、后工具结果」，且必须成组裁：assistant 的 tool_calls 与
// 其配对的 tool 结果被裁掉一半会让整轮失效（§7.1），所以按「轮」为单位裁，
// 一旦某轮装不下就整轮丢弃并记一条 context_warning。
//
// 预算未知（模型未声明 ContextWindow）时**不裁**：宁可按 HISTORY_WINDOW 条数上限走，
// 也不要凭猜测设一个数字——未知就不装懂，与 ContextWindow 字段「未知不填 0」同一条原则。
func trimToContextBudget(in []ai.Message, limits assistantChatLimits, dropped []map[string]any, sink *assistantEventSink) []ai.Message {
	if limits.ContextTokenBudget <= 0 || len(in) == 0 {
		return in
	}
	budgetBytes := limits.ContextTokenBudget * assistantChatMinBytesPerToken

	// 从尾部（最新）往前累加，装不下就停；末尾必须落在「轮边界」上，
	// 不能把 assistant 的 tool_calls 与它的 tool 结果切开。
	total := 0
	cut := len(in)
	for i := len(in) - 1; i >= 0; i-- {
		size := len(in[i].Content) + len(in[i].Role)
		for _, c := range in[i].ToolCalls {
			size += len(c.ToolName) + len(c.Args) + len(c.CallID)
		}
		if total+size > budgetBytes {
			cut = i + 1
			break
		}
		total += size
		cut = i
	}
	if cut == 0 {
		return in
	}
	// 回退到安全边界：若切点落在某个 tool 结果上，连同它宿主的 assistant 轮一起保留，
	// 否则会出现「孤立的 tool 消息」——那正是 buildHistory 刚刚费力剔除的形态。
	if cut < len(in) && in[cut].Role == "tool" {
		for cut > 0 && in[cut-1].Role != "assistant" {
			cut--
		}
		if cut > 0 {
			cut--
		}
	}
	// **绝不裁空**：预算连最后一条都装不下时，仍保留它。
	//
	// 裁空的后果不是「模型少看历史」，而是发出一个没有任何输入的请求——上游直接报错、
	// run 失败；留一条超预算的消息，最坏是这次请求被上游以「超长」拒绝，错误信息还能指向真实原因。
	// 两种失败不对称，故保底留一条。
	if cut >= len(in) {
		cut = len(in) - 1
	}
	if cut == 0 {
		return in
	}
	if sink != nil {
		dropped = append(dropped, map[string]any{
			"type":   "history",
			"id":     cut,
			"reason": "超出上下文预算，从最旧端裁剪",
		})
		_ = sink.append(model.AssistantEventTypeContextWarning, map[string]any{
			"dropped": dropped,
			"message": "历史超出模型上下文预算，已从最旧一端裁剪",
		}, nil, nil)
	}
	return in[cut:]
}

// ── 收尾 ────────────────────────────────────────────────────────────────

// emitError 写一条已脱敏分类的 error 事件。
func (ex *runExecutor) emitError(code ai.ErrorCode, message string) {
	_ = ex.sink.append(model.AssistantEventTypeError, map[string]any{
		"code":    string(code),
		"message": message,
	}, nil, nil)
}

// finishRun 把 run 推进到终态并写 run_end、清会话活跃指针。
//
// 唯一写入原则：run_end 每 run **恰好一条**（§5.3）。执行体活着时只有它写；
// 若 CAS 发现 run 已被取消/回收推进到终态，则改用库内的状态写 run_end，而不是覆盖它。
//
// usageInput / usageOutput 只写供应商真报过的值：全为 nil 就不碰这两列（保持 null），
// 用 0 代替「未报告」会让用量面板显示「本次消耗 0 token」这种明确错误的信息。
func (s *AssistantChatService) finishRun(run *model.AssistantRun, sink *assistantEventSink, status, reason, note string, usageInput, usageOutput *int) {
	updates := map[string]any{
		"status":             status,
		"termination_reason": reason,
		"version":            run.Version + 1,
	}
	// waiting_confirmation **不是终态**（FR-516 §5.1）：run 之后还要被恢复执行或终结。
	// 写 ended_at 会让它看起来已经结束，重启回收与幂等判定都会因此误判。
	if status != model.AssistantRunStatusWaitingConfirmation {
		updates["ended_at"] = time.Now()
	}
	// 用量与终态一次落库：分两步写会留下「已完成但没用量」的可观测窗口。
	if usageInput != nil {
		updates["usage_input"] = *usageInput
	}
	if usageOutput != nil {
		updates["usage_output"] = *usageOutput
	}
	moved, err := s.casRunStatus(run.ID, run.Version, model.AssistantRunActiveStatuses(), status, updates)
	if err != nil {
		slog.Error("助手运行时：写终态失败", "runId", run.ID, "error", err)
	}
	if !moved {
		// 已被另一方推进到终态：以库内状态为准（取消优先于收尾），不覆盖。
		var cur model.AssistantRun
		if e := s.db.First(&cur, run.ID).Error; e == nil {
			status = cur.Status
			if cur.TerminationReason != nil {
				reason = *cur.TerminationReason
			}
		}
	}
	if sink == nil {
		sink = newAssistantEventSink(s.db, run, s.limits)
		sink.hub = s.hub
	}
	if err := sink.ensureRunEnd(status, reason, note); err != nil {
		slog.Warn("助手运行时：写 run_end 失败", "runId", run.ID, "error", err)
	}
	s.clearActiveRun(run.ConversationID, run.ID)
	s.forgetCancel(run.ID)
}

// clearActiveRun 清除指向该 run 的会话活跃指针。
func (s *AssistantChatService) clearActiveRun(conversationID, runID uint) {
	if err := s.db.Model(&model.AssistantConversation{}).
		Where("id = ? AND active_run_id = ?", conversationID, runID).
		Updates(map[string]any{"active_run_id": nil}).Error; err != nil {
		slog.Warn("助手运行时：清理 active_run_id 失败", "runId", runID, "error", err)
	}
}

// casRunStatus 以版本号做 CAS 状态迁移（§5.1：所有状态迁移走 CAS）。
//
// 为什么必须带 status IN 条件而不是只比 version：版本号只能证明「没人改过这一行」，
// 不能证明「它还该被推进」。加上来源状态集合后，一个已被取消的 run 不会被收尾逻辑
// 悄悄改回 completed——终态不可复活这条不变量靠的就是这个条件。
func (s *AssistantChatService) casRunStatus(runID uint, version int, from []string, to string, extra map[string]any) (bool, error) {
	updates := make(map[string]any, len(extra)+2)
	for k, v := range extra {
		updates[k] = v
	}
	updates["status"] = to
	updates["version"] = version + 1
	res := s.db.Model(&model.AssistantRun{}).
		Where("id = ? AND version = ? AND status IN ?", runID, version, from).
		Updates(updates)
	if res.Error != nil {
		return false, fmt.Errorf("迁移 run 状态失败: %w", res.Error)
	}
	return res.RowsAffected > 0, nil
}

// ── 取消 ────────────────────────────────────────────────────────────────

// Cancel 取消一轮运行（幂等）。
//
// 已终态再取消返回 nil 而不是报错：取消是「让这一轮停在这里」的声明式意图，
// 它已经停住了就是达成了目的；报错只会让前端弹一个无意义的失败提示。
func (s *AssistantChatService) Cancel(owner, runID uint) error {
	for attempt := 0; attempt < assistantChatCancelRetries; attempt++ {
		var run model.AssistantRun
		err := s.db.Where("id = ? AND owner_user_id = ?", runID, owner).First(&run).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAssistantChatNotFound
			}
			return fmt.Errorf("查询 run 失败: %w", err)
		}
		if isTerminalRunStatus(run.Status) {
			return nil
		}
		moved, err := s.casRunStatus(run.ID, run.Version, model.AssistantRunActiveStatuses(),
			model.AssistantRunStatusCancelled, map[string]any{
				"termination_reason": model.AssistantTerminationCancelled,
				"ended_at":           time.Now(),
			})
		if err != nil {
			return err
		}
		if !moved {
			// 并发的另一次状态迁移（执行体拾取或另一路取消）让 version 前进了，重读再试。
			continue
		}
		if cancel := s.takeCancel(run.ID); cancel != nil {
			// 执行体还活着：由它中断流并写 run_end（唯一写入原则）。
			cancel()
			return nil
		}
		// 没有活着的执行体（上一进程遗留、或还没被拾取）：本路径负责写终态事件。
		sink := newAssistantEventSink(s.db, &run, s.limits)
		sink.hub = s.hub
		if err := sink.ensureRunEnd(model.AssistantRunStatusCancelled, model.AssistantTerminationCancelled, ""); err != nil {
			return fmt.Errorf("写 run_end 失败: %w", err)
		}
		s.clearActiveRun(run.ConversationID, run.ID)
		return nil
	}
	return ErrAssistantChatConflict
}

// isTerminalRunStatus 判断是否终态。
func isTerminalRunStatus(status string) bool {
	switch status {
	case model.AssistantRunStatusCompleted, model.AssistantRunStatusFailed,
		model.AssistantRunStatusCancelled, model.AssistantRunStatusTimedOut,
		model.AssistantRunStatusInterrupted:
		return true
	}
	return false
}

// ── 启动回收（§8）───────────────────────────────────────────────────────

// ReapOnStartup 把上次进程遗留的非终态 run 全部置 interrupted，并补 run_end 事件。
//
// **部署前提（§8.1，冻结）**：V1 只支持单个 CP 实例。「启动即回收全部非终态 run」与
// 「多实例」不能同时成立——第二个实例启动会把第一个实例正在正常工作的 run 全部标成
// interrupted 并释放它的会话槽。本 FR 选择「单实例」并在这里写明，而不是引入租约/心跳：
// 那需要 run 所有者标识 + 续期 + 失效判定 + fencing generation 一整套协调机制，
// 与「轻量运行时」的定位冲突（§8.1 末段）。
//
// 作用面只有 run 与会话的活跃指针：
//   - 处于 waiting_confirmation 的确认失效、已派发的写调用标 unverified，属 FR-516 的
//     票据/回执，本 FR **不碰**（§8 第 2、3 条由 FR-516 落地）；
//   - 消息侧无需修补：助手消息是在一轮模型流**结束后**一次性写入的，中断不会留下半条
//     消息；因此这里不做任何消息改写（改写只会把已确定的内容变成假的「未知」）。
//
// 幂等：CAS 保证只有真正迁移成功的那次写 run_end，重复调用不会产出第二条终态事件。
func (s *AssistantChatService) ReapOnStartup() (int, error) {
	var runs []model.AssistantRun
	if err := s.db.Where("status IN ?", model.AssistantRunActiveStatuses()).Order("id asc").Find(&runs).Error; err != nil {
		return 0, fmt.Errorf("查询非终态 run 失败: %w", err)
	}
	reaped := 0
	for i := range runs {
		run := &runs[i]
		moved, err := s.casRunStatus(run.ID, run.Version, model.AssistantRunActiveStatuses(),
			model.AssistantRunStatusInterrupted, map[string]any{
				"termination_reason": model.AssistantTerminationInterrupted,
				"ended_at":           time.Now(),
			})
		if err != nil {
			return reaped, err
		}
		if !moved {
			continue
		}
		sink := newAssistantEventSink(s.db, run, s.limits)
		sink.hub = s.hub
		if err := sink.ensureRunEnd(model.AssistantRunStatusInterrupted, model.AssistantTerminationInterrupted, ""); err != nil {
			return reaped, err
		}
		s.forgetCancel(run.ID)
		reaped++
	}
	// 清理指向已终态 run 的活跃指针（§8 第 4 条）：用子查询而不是先查再逐条写，
	// 避免「查出来之后状态又变了」把仍然活跃的 run 指针误清。
	sub := s.db.Model(&model.AssistantRun{}).Select("id").Where("status NOT IN ?", model.AssistantRunActiveStatuses())
	if err := s.db.Model(&model.AssistantConversation{}).
		Where("active_run_id IS NOT NULL AND active_run_id IN (?)", sub).
		Updates(map[string]any{"active_run_id": nil}).Error; err != nil {
		return reaped, fmt.Errorf("清理会话活跃指针失败: %w", err)
	}
	return reaped, nil
}

// ── 模型解析 ────────────────────────────────────────────────────────────

// prepareRunModel 生产实现：读配置表得到冻结快照，并复用 FR-514 的协议层构造适配器。
func (s *AssistantChatService) prepareRunModel(conv *model.AssistantConversation) (assistantModelSnapshot, ai.Streamer, error) {
	mdl, provider, err := s.resolveModel(conv)
	if err != nil {
		return assistantModelSnapshot{}, nil, err
	}
	// 出站准入与凭据解析全部复用 AssistantService.buildStreamer（不重写协议层）。
	if s.assistant == nil {
		return assistantModelSnapshot{}, nil, fmt.Errorf("%w: 出站准入未初始化", ErrAssistantModelUnavailable)
	}
	streamer, err := s.assistant.buildStreamer(provider)
	if err != nil {
		return assistantModelSnapshot{}, nil, fmt.Errorf("%w: %s", ErrAssistantModelUnavailable, err.Error())
	}
	return assistantModelSnapshot{
		PlatformModelID: mdl.ID,
		ProviderID:      provider.ID,
		Protocol:        provider.Protocol,
		UpstreamModelID: mdl.ModelID,
		DisplayName:     mdl.DisplayName,
		Streaming:       mdl.Streaming,
		Tools:           mdl.Tools,
		MaxOutputTokens: mdl.MaxOutputTokens,
		ContextWindow:   mdl.ContextWindow,
		ReasoningEffort: mdl.ReasoningEffort,
	}, streamer, nil
}

// resolveModel 解析会话选定模型（空则用全局默认），并确认模型与供应商都处于启用态。
func (s *AssistantChatService) resolveModel(conv *model.AssistantConversation) (*model.AssistantModel, *model.AssistantProvider, error) {
	modelID := conv.ModelID
	if modelID == nil {
		var setting model.AssistantSetting
		if err := s.db.First(&setting, 1).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil, fmt.Errorf("%w: 尚未初始化助手设置，请先在管理端配置模型", ErrAssistantModelUnavailable)
			}
			return nil, nil, fmt.Errorf("查询助手设置失败: %w", err)
		}
		modelID = setting.DefaultModelID
	}
	if modelID == nil {
		return nil, nil, fmt.Errorf("%w: 未配置默认模型", ErrAssistantModelUnavailable)
	}
	var mdl model.AssistantModel
	if err := s.db.First(&mdl, *modelID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, fmt.Errorf("%w: 模型已不存在", ErrAssistantModelUnavailable)
		}
		return nil, nil, fmt.Errorf("查询模型失败: %w", err)
	}
	if !mdl.Enabled {
		return nil, nil, fmt.Errorf("%w: 该模型已停用", ErrAssistantModelUnavailable)
	}
	var provider model.AssistantProvider
	if err := s.db.First(&provider, mdl.ProviderID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, fmt.Errorf("%w: 模型所属供应商已不存在", ErrAssistantModelUnavailable)
		}
		return nil, nil, fmt.Errorf("查询供应商失败: %w", err)
	}
	if !provider.Enabled {
		return nil, nil, fmt.Errorf("%w: 该模型的供应商已停用", ErrAssistantModelUnavailable)
	}
	return &mdl, &provider, nil
}

// ── 小工具 ──────────────────────────────────────────────────────────────

// digestRequestBody 计算「同一个键是否同一个请求」的稳定摘要（SHA-256 十六进制）。
//
// 组成：本轮用户消息正文 + 会话当前选定的模型 ID（未选定记 0，表示跟随默认）。
// 为什么不把「解析后的默认模型 ID」算进去：默认模型变更不是请求内容变更，
// 把配置漂移算成异参会把一次正常重放判成 409；摘要只回答「这个键有没有被用来提交别的正文」。
// 摘要**不进唯一键**（§6.1）：进了唯一键，异参请求就会各自插入一条，幂等当场失效。
func digestRequestBody(text string, modelID *uint) string {
	var mid uint
	if modelID != nil {
		mid = *modelID
	}
	// 长度前缀而不是分隔符：正文里出现任何分隔符都不会让两组不同输入拼出同一串。
	input := strconv.Itoa(len(text)) + ":" + text + "|" + strconv.FormatUint(uint64(mid), 10)
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

// nextTurn 计算会话内下一轮的轮次号。
func nextTurn(tx *gorm.DB, conversationID uint) (int, error) {
	var maxTurn int
	if err := tx.Model(&model.AssistantMessage{}).Where("conversation_id = ?", conversationID).
		Select("COALESCE(MAX(turn), 0)").Scan(&maxTurn).Error; err != nil {
		return 0, fmt.Errorf("计算轮次失败: %w", err)
	}
	return maxTurn + 1, nil
}

// truncateBytes 按 UTF-8 边界把字符串截到不超过 max 字节，返回结果与是否发生截断。
func truncateToBytes(s string, max int) (string, bool) {
	if max <= 0 || len(s) <= max {
		return s, false
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}
