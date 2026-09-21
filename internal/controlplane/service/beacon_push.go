package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// Beacon 推送侧实现（FR-443，见 ADR-090 §6）。
//
// 触发时机刻意**只覆盖拓扑变更**：实例创建 / 删除 / 改名 / 改归属（role、tags）。
// 实例启停与重启**绝不推送**——高频操作（批量重启 60 台 = 120 次推送），且 Beacon 的在线
// 状态本就由 agent 心跳自维护，推送只会造成重复真源。
//
// 传输层刻意复用 FR-444 的 BeaconClient（同一 endpoint / token / 出站代理持有者），
// 本文件只在它之上加一个写端点，不另起一套 HTTP 配置。

const (
	// BeaconAPIPathAgentRegister 是 Beacon 的 v1 agent 注册端点（FR-222 内部信任通道）。
	// JianManager 作为受信内部调用方走此通道推送拓扑变更：命中共享 token 的请求在 Beacon 侧
	// 被标记为受信调用方，开启 allow-machine-register 时注册直落 active 并绑定 serverId。
	BeaconAPIPathAgentRegister = "/beacon/v1/agent/register"

	// 推送审计动作（FR-443 §3.3）。与 FR-444 的 instance.beacon_pull_* 分列，便于按方向排查。
	AuditActionBeaconPushOK   = "instance.beacon_push_ok"
	AuditActionBeaconPushFail = "instance.beacon_push_fail"

	// AuditTargetBeaconPush 是推送审计的目标类型（单个实例，target 为实例名）。
	AuditTargetBeaconPush = "instance"
)

// 推送事件类型（写入审计 detail，便于按变更原因甄别）。
const (
	beaconPushEventCreate    = "create"
	beaconPushEventDelete    = "delete"
	beaconPushEventRename    = "rename"
	beaconPushEventOwnership = "ownership"
)

// BeaconPushClientConfig 是 Beacon 推送客户端配置（FR-443）。
//
// **可选协同，绝非依赖**：Endpoint 为空即整体跳过（连 goroutine 都不起）；PushEnabled=false
// 时同样不发起任何请求。未部署 Beacon 时实例创建/删除/改名全部照常成功、无任何报错。
type BeaconPushClientConfig struct {
	// Endpoint Beacon 基址；留空表示未配置协同。
	Endpoint string
	// Token 共享 token，以 `X-Beacon-Token` 发送（FR-222 §3.1）。
	Token string
	// PushEnabled 是否启用推送；false 时 PushServer 返回 ErrBeaconPushDisabled。
	PushEnabled bool
	// Namespace 推送目标 Beacon namespace code（如 prod）。
	//
	// 留空时推送在后台被跳过并留痕：Beacon 的机器注册要解析 namespace 才能落身份，未知/空
	// namespace 会被拒；与其盲推被拒（每次变更留一条 fail 审计），不如显式记为「未配置」。
	// 多环境映射见 ADR-090 §7 未决项：首版单配置项。
	Namespace string
}

// 推送侧错误。全部在后台 goroutine 中被消费（写审计），绝不冒泡到用户操作。
var (
	// ErrBeaconPushDisabled 已配置端点但 beacon.push-enabled=false（或服务未构造）。
	ErrBeaconPushDisabled = fmt.Errorf("Beacon 拓扑推送未启用（beacon.push-enabled=false）")
	// errBeaconNamespaceUnset 已开启推送但未配置 beacon.namespace。
	errBeaconNamespaceUnset = fmt.Errorf("未配置推送目标 namespace（beacon.namespace 为空）")
)

// BeaconServerPush 是一次 server 拓扑推送的请求体（对齐 Beacon v1 注册端点契约）。
//
// 字段刻意取投影视图而非整行实例：Beacon 只关心「这台 server 叫什么、什么角色、地址多少」，
// 推整行会把 CP 内部字段（配置、环境变量）泄给对端，也把两侧契约绑死在 JM 的表结构上。
type BeaconServerPush struct {
	Namespace string `json:"namespace"`
	ServerID  string `json:"serverId"`
	// Role 取 Beacon 侧 agent 角色取值（bukkit / bungee），由 JM 的实例角色映射而来。
	Role    string            `json:"role"`
	Address string            `json:"address,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// BeaconPushClient 是 Beacon 推送客户端（FR-443）：在 FR-444 的 BeaconClient 传输层上
// 加一个写端点。endpoint / token / 出站代理持有者全部复用既有客户端，不重复配置。
type BeaconPushClient struct {
	// base 复用拉取侧客户端的 endpoint 归一化、token 与运行时出站持有者（FR-185/ADR-043）。
	base        *BeaconClient
	namespace   string
	pushEnabled bool
}

// NewBeaconPushClient 构造推送客户端。endpoint 为空返回 nil（调用方据此整体跳过协同）。
func NewBeaconPushClient(cfg BeaconPushClientConfig) *BeaconPushClient {
	base := NewBeaconClient(BeaconClientConfig{Endpoint: cfg.Endpoint, Token: cfg.Token})
	if base == nil {
		return nil
	}
	return &BeaconPushClient{
		base:        base,
		namespace:   strings.TrimSpace(cfg.Namespace),
		pushEnabled: cfg.PushEnabled,
	}
}

// SetHTTPClientProvider 注入「取当前出站 *http.Client」的提供者（FR-185 Provider.Client）。
// 与拉取客户端同源：main 用同一 provider 装配两者，平台设置里改出站代理对推送即时生效。
func (c *BeaconPushClient) SetHTTPClientProvider(fn func() *http.Client) {
	if c == nil {
		return
	}
	c.base.SetHTTPClientProvider(fn)
}

// Enabled 报告推送是否可用（已配置端点 + 已开启 push-enabled）。
func (c *BeaconPushClient) Enabled() bool {
	return c != nil && c.base.Enabled() && c.pushEnabled
}

// Namespace 返回推送目标 namespace code。
func (c *BeaconPushClient) Namespace() string {
	if c == nil {
		return ""
	}
	return c.namespace
}

// PushServer 经机器注册通道推送一台 server 的拓扑（FR-443 §3.2）。
//
// 语义严格对齐「不重试、不回滚、不阻塞」（决策 3A）：本方法只做一次请求并把结果如实返回，
// 不含任何重试或补偿。返回 nil 表示**已成功投递**，不表示「已在 Beacon 生效」——Beacon 侧
// allow-machine-register 关闭时注册落 pending（成功投递但不生效），该状态由 Beacon 侧审计呈现
//（spec §3.2 明文：JianManager 侧视为成功投递但不生效）。
func (c *BeaconPushClient) PushServer(ctx context.Context, payload BeaconServerPush) error {
	if c == nil || !c.base.Enabled() {
		return ErrBeaconNotConfigured
	}
	if !c.pushEnabled {
		return ErrBeaconPushDisabled
	}
	if strings.TrimSpace(payload.Namespace) == "" {
		return errBeaconNamespaceUnset
	}
	if strings.TrimSpace(payload.ServerID) == "" {
		return fmt.Errorf("推送 serverId 为空")
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化推送体失败: %w", err)
	}
	endpoint := c.base.Endpoint() + BeaconAPIPathAgentRegister
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%w: 构造请求失败: %v", ErrBeaconUnreachable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// 共享 token 走 X-Beacon-Token（FR-222 §3.1）：Beacon 中间件据此把请求标记为受信内部调用方，
	// 这是机器注册分支的唯一依据（不取自请求体，调用方无法伪造）。
	if token := strings.TrimSpace(c.base.cfg.Token); token != "" {
		req.Header.Set("X-Beacon-Token", token)
	}

	resp, err := c.base.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("%w: 请求 %s 失败: %v", ErrBeaconUnreachable, BeaconAPIPathAgentRegister, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%w: 读取注册响应失败: %v", ErrBeaconUnreachable, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: %s 返回 HTTP %d: %s",
			ErrBeaconUnreachable, BeaconAPIPathAgentRegister, resp.StatusCode, truncateBeaconBody(body))
	}
	return nil
}

// BeaconPushService 实现 FR-443「向 Beacon 推送拓扑变更」。
//
// 三条硬约束：
//  1. **可选协同，绝非依赖**：未配置 endpoint（或未开 push-enabled）时 PushInstance 立即返回，
//     不起 goroutine、不写审计、不返回错误——实例创建/删除/改名全部照常成功。
//  2. **异步**：推送在 goroutine 中执行，绝不进入用户操作的响应路径（决策 3A）。
//  3. **不重试、不回滚、不阻塞**：失败只写审计与告警；本机操作已成功，不因外部系统不可达而撤销。
type BeaconPushService struct {
	client *BeaconPushClient
	db     *gorm.DB
	// audit 可选；nil 时跳过审计（单测轻量场景）。
	audit *AuditService
	// bgCtx/bgCancel 给在途推送提供超时上限与兜底取消；bgClosed 标记不再接受新推送。
	// bgWG 用于退出/测试收尾时 join 在途推送（避免退出后仍向已关闭的 DB 写审计）。
	// 与 InstanceService 的后台委托同口径，但收尾策略不同：先等在途推送自然结束（完成投递
	// 与审计），仅在超过 grace 后取消——否则正常收尾会被误记成一批送达成功/失败的噪声。
	bgCtx    context.Context
	bgCancel context.CancelFunc
	bgClosed bool
	bgWG     sync.WaitGroup
	bgMu     sync.Mutex
}

// beaconPushShutdownGrace 是优雅收尾等待在途推送的上限；超过即取消，避免退出被挂死请求拖住。
const beaconPushShutdownGrace = 3 * time.Second

// NewBeaconPushService 创建推送服务。client 为 nil（未配置 beacon.endpoint）时服务仍构造成功，
// Enabled 恒为 false、PushInstance 直接返回。
func NewBeaconPushService(db *gorm.DB, client *BeaconPushClient) *BeaconPushService {
	ctx, cancel := context.WithCancel(context.Background())
	return &BeaconPushService{db: db, client: client, bgCtx: ctx, bgCancel: cancel}
}

// SetAuditService 注入审计服务（main 装配阶段调用）。nil 时不写审计。
func (s *BeaconPushService) SetAuditService(a *AuditService) { s.audit = a }

// Configured 报告是否已配置 Beacon 协同端点（beacon.endpoint 非空）。
// 与 Enabled 的区别：配置了端点但没开 push-enabled 时 Configured=true、Enabled=false。
func (s *BeaconPushService) Configured() bool {
	return s != nil && s.client != nil && s.client.base.Enabled()
}

// Enabled 报告推送是否可用（已配置端点 + beacon.push-enabled=true）。
func (s *BeaconPushService) Enabled() bool {
	return s != nil && s.client.Enabled()
}

// Shutdown 停止接受新的推送并等待在途推送收尾（进程优雅关闭 / 测试收尾用）。
//
// 先给在途推送一段 grace 让它自然结束（把「已送达/已失败」如实写入审计），超时才取消：
// 一上来就 cancel 会让所有在途请求变成 context canceled 的 fail 审计，把正常退出染成一片失败噪声。
func (s *BeaconPushService) Shutdown() {
	if s == nil {
		return
	}
	s.bgMu.Lock()
	s.bgClosed = true
	s.bgMu.Unlock()

	done := make(chan struct{})
	go func() {
		s.bgWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(beaconPushShutdownGrace):
		s.bgMu.Lock()
		s.bgCancel()
		s.bgMu.Unlock()
		s.bgWG.Wait()
	}
}

// PushInstance 异步推送一次实例拓扑变更（FR-443 §3.1/§3.3）。
//
// 调用点只应是**拓扑变更**：实例创建 / 删除 / 改名 / 改归属（role、tags）。
// 实例启停与重启**不得**调用本方法（刻意决策：高频且与 agent 心跳形成重复真源）。
//
// 未配置端点或未开启 push-enabled 时**整体跳过**：不起 goroutine、不写审计、不返回错误，
// 调用方（instance.go 的创建/删除/更新路径）因此完全不必感知 Beacon 是否存在。
//
// userID / ip 是触发本次变更的操作者与来源（审计主体）；删除场景传入删除前的实例快照。
func (s *BeaconPushService) PushInstance(userID uint, ip, event string, inst *model.Instance) {
	if s == nil || inst == nil || !s.Enabled() {
		return
	}

	// 起 goroutine 之前把要推送的数据**投影成纯数据**：实例行随后可能被改名/删除，
	// 持指针进 goroutine 会读到竞态后的内容（推的就不是本次变更的事实了）。
	payload := s.buildPayload(inst)

	s.async(func() { s.push(userID, ip, event, payload) })
}

// async 登记一个后台推送任务；服务已关闭时丢弃（不阻塞、不报错）。
func (s *BeaconPushService) async(fn func()) {
	s.bgMu.Lock()
	if s.bgClosed {
		s.bgMu.Unlock()
		return
	}
	s.bgWG.Add(1)
	s.bgMu.Unlock()

	go func() {
		defer s.bgWG.Done()
		fn()
	}()
}

// push 是 goroutine 内的推送执行体：一次请求 + 一条审计，绝不重试。
func (s *BeaconPushService) push(userID uint, ip, event string, payload BeaconServerPush) {
	ctx, cancel := context.WithTimeout(s.bgCtx, beaconDefaultTimeout)
	defer cancel()

	err := s.client.PushServer(ctx, payload)
	if err != nil {
		// 失败处理（决策 3A）：只写审计与告警——不重试、不回滚、不阻塞。
		s.recordPush(userID, ip, event, payload, err)
		slog.Warn("Beacon 拓扑推送失败（不重试、不影响本机操作）",
			"event", event, "serverId", payload.ServerID, "error", err)
		return
	}
	s.recordPush(userID, ip, event, payload, nil)
	slog.Info("Beacon 拓扑推送成功", "event", event, "serverId", payload.ServerID)
}

// buildPayload 把实例行投影为推送请求体（纯数据，脱离 DB 与指针）。
func (s *BeaconPushService) buildPayload(inst *model.Instance) BeaconServerPush {
	return BeaconServerPush{
		Namespace: s.client.Namespace(),
		ServerID:  strings.TrimSpace(inst.Name),
		Role:      beaconAgentRole(inst.Role),
		Address:   s.instanceAddress(inst),
		Metadata:  beaconPushMetadata(inst),
	}
}

// instanceAddress 拼装 server 的可达地址（节点 host:实例端口）。
// 取实例所属节点的 Host + 实例的 server 端口；任一缺失返回空串（Beacon 侧 address 可选）。
// 查询失败不报错：地址只是推送的附带信息，缺它不应让整条拓扑推送失败。
func (s *BeaconPushService) instanceAddress(inst *model.Instance) string {
	if inst.ServerPort <= 0 {
		return ""
	}
	var node model.Node
	if err := s.db.Select("host").First(&node, inst.NodeID).Error; err != nil {
		return ""
	}
	host := strings.TrimSpace(node.Host)
	if host == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d", host, inst.ServerPort)
}

// recordPush 写推送审计（成功 ok / 失败 fail + 错误详情）。
// 审计失败不阻断（与 FR-444 拉取同口径）。
func (s *BeaconPushService) recordPush(userID uint, ip, event string, payload BeaconServerPush, pushErr error) {
	if s.audit == nil {
		return
	}
	if pushErr != nil {
		s.audit.RecordResultSafe(userID, AuditActionBeaconPushFail, AuditTargetBeaconPush, payload.ServerID,
			fmt.Sprintf(`{"event":%q,"namespace":%q}`, event, payload.Namespace), ip, false, pushErr.Error())
		return
	}
	detail := fmt.Sprintf(`{"event":%q,"namespace":%q,"role":%q,"address":%q}`,
		event, payload.Namespace, payload.Role, payload.Address)
	s.audit.RecordResultSafe(userID, AuditActionBeaconPushOK, AuditTargetBeaconPush, payload.ServerID, detail, ip, true, "")
}

// beaconUpdateEvent 由「是否改名 / 是否改归属」组合出更新类推送事件；两者皆否返回空串（不推送）。
//
// 两个维度同时变更时只推送**一次**：一次注册即携带改名后的完整归属（Beacon 侧按 serverId
// 幂等 upsert），推两次是纯浪费。事件名用 "+" 连接，让审计能看出本次变更含哪些维度。
func beaconUpdateEvent(renamed, reowned bool) string {
	parts := make([]string, 0, 2)
	if renamed {
		parts = append(parts, beaconPushEventRename)
	}
	if reowned {
		parts = append(parts, beaconPushEventOwnership)
	}
	return strings.Join(parts, "+")
}

// beaconAgentRole 把 JianManager 的实例角色映射为 Beacon v1 注册的 agent 角色取值。
//
// Beacon 的 v1 注册只认 bukkit / bungee 两档（其 agent 角色即如此），映射为：
//   - proxy（BungeeCord/Waterfall/Velocity）→ bungee（Beacon 侧归为 proxy）
//   - 其余（backend/universal/beacon）→ bukkit（Beacon 侧归为 backend）
func beaconAgentRole(role model.InstanceRole) string {
	if role == model.InstanceRoleProxy {
		return "bungee"
	}
	return "bukkit"
}

// beaconPushMetadata 生成推送的附加元信息：带 JM 侧 UUID / 角色 / 标签，
// 供 Beacon 侧排障时区分「同名不同机」并核对归属。
func beaconPushMetadata(inst *model.Instance) map[string]string {
	meta := map[string]string{
		"source":         "jianmanager",
		"jmUuid":         inst.UUID,
		"jmRole":         string(inst.Role),
		"jmInstanceType": string(inst.Type),
	}
	if tags := model.ParseTags(inst.Tags); len(tags) > 0 {
		meta["jmTags"] = strings.Join(tags, ",")
	}
	return meta
}
