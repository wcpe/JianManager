package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 桥能力接入的 CP 侧编排（FR-526）。
//
// ## 三条能力的共同形态
//
// 都走「实例 → 节点 → Worker gRPC → 探针 `core.*` 命令」这条既有通道，CP **不解析**
// 探针返回的业务内容，只做信封级映射：
//   - 桥状态：线路健康度快照，原样透传（字段演进不改 CP）；
//   - 域状态：域清单 + manifest 指纹，**只渲染不解释**（ADR-106）；
//   - 订阅：把 (topics, domains) 转成探针的 topic 表达式列表再下发。
//
// ## 与业务命令的分界
//
// 它们刻意不走 BusinessService.Dispatch：那里把一切失败压成 `available=false`，
// 而这里需要区分「探针不在」（可降级/可 stale）与「探针在但查询失败」（该报错）。
// 订阅还要回 409 PROBE_UNAVAILABLE，域状态要回 stale 缓存——都不是业务命令的语义。

// 桥能力相关超时。
const (
	// bridgeStatusTimeout 桥状态查询的 CP→Worker 超时。
	// 须大于 Worker 侧等探针回执的 5s，留网络与排队余量（与 businessDispatchTimeout 同量级）。
	bridgeStatusTimeout = 8 * time.Second

	// bridgeSubscriptionTimeout 订阅下发的 CP→Worker 超时（订阅是纯内存改写，与状态查询同量级）。
	bridgeSubscriptionTimeout = 8 * time.Second

	// businessStatusTimeout 域状态查询的 CP→Worker 超时。
	businessStatusTimeout = 8 * time.Second

	// BusinessStatusCacheTTL 业务域状态的内存缓存 TTL（spec FR-526 §2.4 / OpenAPI：30 秒）。
	//
	// 缓存的理由不是省一次 RPC，而是让面板在探针抖动时**还有东西可渲染**：
	// 过期后仍保留最后已知值，只在返回时打 stale=true（见 OpenAPI 的 `stale` 语义）。
	BusinessStatusCacheTTL = 30 * time.Second
)

// ErrBridgeProbeUnavailable 探针未连接（订阅设置必须探针在线，OpenAPI 规定回 409 PROBE_UNAVAILABLE）。
var ErrBridgeProbeUnavailable = errors.New("探针未连接")

// BridgeStatusResult 桥状态查询结果，字段与 OpenAPI `BridgeStatus` 一一对应。
//
// 全部字段来自探针 `core.bridge_status` 回执的快照；CP 只做**扁平化**，不做语义加工。
type BridgeStatusResult struct {
	InstanceID uint `json:"instanceId"`
	// Connected 探针是否已连入本机 Worker。
	Connected bool `json:"connected"`
	// ConnectedSince 本次连接建立的时刻（RFC3339）。从未连过时省略。
	ConnectedSince string `json:"connectedSince,omitempty"`
	// ReconnectCount 自探针启动以来的重连次数（探针侧 `reconnects`）。
	ReconnectCount int64 `json:"reconnectCount"`
	// LastDisconnectReason 最近一次断开的可读原因；从未断开为空串。
	LastDisconnectReason string `json:"lastDisconnectReason"`
	// QueueDepth 待发送帧队列深度（探针侧 sender 三个池的待发之和）。
	QueueDepth int64 `json:"queueDepth"`
	// DroppedFrames 因背压丢弃的帧数（可靠事件不应丢弃）。
	DroppedFrames int64 `json:"droppedFrames"`
	// PendingReloads 在途配置重载数（探针侧 reload 池 active）。
	PendingReloads int64 `json:"pendingReloads"`
	// ProbeVersion 探针协议版本（探针侧回执的 `version`）。
	ProbeVersion string `json:"probeVersion"`
	// FetchedAt 本次快照的采集时刻（RFC3339）。
	FetchedAt string `json:"fetchedAt,omitempty"`
	// Error 降级原因（节点未连/探针未连/查询失败）；成功时为空。
	//
	// OpenAPI 的 BridgeStatus 没有该字段，但"探针未连"必须能被前端区分于"连接正常但全为 0"；
	// 加性字段不影响既有消费者（同 ADR 的加性演进约定）。
	Error string `json:"error,omitempty"`
}

// bridgeStatusWire 是探针 `core.bridge_status` 回执的**最小**结构化视图。
//
// CP 只取 OpenAPI 契约要求的几个字段；探针回执里还有 pools/routes/inbound 等更细的结构，
// 这里刻意不建模——建模了就会有人问"能不能按 in字段筛选"，
// 于是 CP 就长出了本不该有的语义（ADR-106 的同一条红线）。
type bridgeStatusWire struct {
	Connected            bool   `json:"connected"`
	Reconnects           int64  `json:"reconnects"`
	LastDisconnectReason string `json:"lastDisconnectReason"`
	LastConnectedAtMs    int64  `json:"lastConnectedAtMs"`
	Version              string `json:"version"`
	Sender               struct {
		CommandResultQueue int64 `json:"commandResultQueue"`
		ReliableQueue      int64 `json:"reliableQueue"`
		// BestEffortDropped 可丢事件被丢的数量；可靠通道的丢弃另有 spilled/spillDropped。
		BestEffortDropped int64 `json:"bestEffortDropped"`
		ReliableRejected  int64 `json:"reliableRejected"`
		SpillDropped      int64 `json:"spillDropped"`
	} `json:"sender"`
	Pools struct {
		Reload struct {
			Queued int64 `json:"queued"`
			Active int64 `json:"active"`
		} `json:"reload"`
		Command struct {
			Queued int64 `json:"queued"`
		} `json:"cmd"`
		Event struct {
			Queued int64 `json:"queued"`
		} `json:"event"`
	} `json:"pools"`
}

// DomainStatusResult 单个业务域的状态，字段与 OpenAPI `DomainStatus` 对齐。
//
// **只渲染不解释**：`manifestDigest` 只是指纹，CP 不解析 manifest 内容，
// 也不按 domain 做任何业务语义归类。
type DomainStatusResult struct {
	Domain string `json:"domain"`
	// Available 该域当前是否可用。
	Available bool `json:"available"`
	// DegradedReason 不可用原因（如 PLUGIN_DISABLED / INIT_FAILED），原样来自探针。
	DegradedReason string `json:"degradedReason,omitempty"`
	// ManifestDigest manifest 内容指纹。**先用 digest 判断是否变化，再决定是否拉全量**。
	ManifestDigest string `json:"manifestDigest,omitempty"`
	// LastDispatchAt 最近一次 dispatch 时间（epoch 秒），0 表示从未。
	LastDispatchAt int64 `json:"lastDispatchAt"`
	// DispatchCount 自探针启动以来的累计调用数（重启归零）。
	DispatchCount int64 `json:"dispatchCount"`
	// FailureCount 累计失败数。
	FailureCount int64 `json:"failureCount"`
}

// BusinessStatusResult 实例的业务域注册状态，字段与 OpenAPI `BusinessStatus` 对齐。
type BusinessStatusResult struct {
	InstanceID   uint   `json:"instanceId"`
	InstanceName string `json:"instanceName"`
	// ProbeConnected 探针当前是否连入。
	ProbeConnected bool `json:"probeConnected"`
	// FetchedAt 这份数据的采集时刻（RFC3339）。stale 时是**上次成功**的时刻。
	FetchedAt string `json:"fetchedAt,omitempty"`
	// Stale true 表示探针不可达，返回的是最后一次已知状态（缓存 TTL 30 秒）。
	Stale bool `json:"stale"`
	// Domains 已注册域清单（按域名排序，保证渲染稳定）。
	Domains []DomainStatusResult `json:"domains"`
	// Error 降级原因（节点未连/查询失败）；stale 且有缓存时给出，便于前端提示。
	Error string `json:"error,omitempty"`
}

// bridgeBusinessStatusCache 是一次成功查询的缓存条目。
type bridgeBusinessStatusCache struct {
	domains  []DomainStatusResult
	fetched  time.Time
	name     string
	instance uint

	// probeConnected 是**写入缓存那一刻**探针的连接态。
	//
	// 【为什么必须记它】真机验收发现：探针刚连上时，bridge/status（无缓存，实时查）
	// 返回 connected=true，而 business/status（30s 缓存）仍返回 probeConnected=false + stale=true，
	// 两块面板同时展示就自相矛盾。
	//
	// 修法不是取消缓存（会放大探针压力），而是**连接态一变就让缓存失效**：
	// 连接是廉价的运行期事实（Worker 侧会话表），域清单才是值得缓存的探针往返。
	cachedConnected bool
}

// BridgeService 桥能力接入的 CP 侧编排（FR-526）。
//
// 与 BusinessService / ServerStateService 同构（db + pool 两依赖），
// 额外持有一份内存缓存给域状态用——**不落库**：域状态是探针运行期快照，
// 落库会让人误以为它是平台数据（ADR-106 的红线），且探针重启后即失效。
type BridgeService struct {
	db   *gorm.DB
	pool *cpgrpc.ClientPool

	// 域状态缓存：实例 ID → 最后一次成功查询。
	//
	// 用 mutex + map 而不是单飞（singleflight）：面板打开会同时打几个实例，
	// 每个实例的缓存互不影响，而同一实例的并发查询最多多打一次探针（幂等只读）。
	cacheMu sync.RWMutex
	cache   map[uint]bridgeBusinessStatusCache

	// now 注入时钟，供单测推进 TTL 而不真等 30 秒。
	now func() time.Time
}

// NewBridgeService 创建桥能力编排服务。
func NewBridgeService(db *gorm.DB, pool *cpgrpc.ClientPool) *BridgeService {
	return &BridgeService{
		db:    db,
		pool:  pool,
		cache: make(map[uint]bridgeBusinessStatusCache),
		now:   time.Now,
	}
}

// resolveInstanceNode 解析实例与其所属节点（桥能力三条路由共用）。
//
// 与 BusinessService.resolveTarget 的分工一致：实例不存在是硬错误（供路由回 404），
// 节点缺失是软降级（由调用方决定怎么表达）。
func (s *BridgeService) resolveInstanceNode(instanceID uint) (model.Instance, model.Node, error) {
	var inst model.Instance
	if err := s.db.First(&inst, instanceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return inst, model.Node{}, ErrInstanceNotFound
		}
		return inst, model.Node{}, fmt.Errorf("查询实例失败: %w", err)
	}
	var node model.Node
	if err := s.db.First(&node, inst.NodeID).Error; err != nil {
		// 节点缺失不当作硬错误：实例记录还在，前端应看到"节点不存在"而不是 500。
		return inst, model.Node{}, nil
	}
	return inst, node, nil
}

// QueryBridgeStatus 查询某实例的探针桥线路健康度（FR-526 §2.3）。
//
// 降级矩阵：
//   - 实例不存在 → ErrInstanceNotFound（路由回 404）；
//   - 节点记录缺失 / 节点未连 → connected=false + error（200，不 5xx）；
//   - 探针未连 → connected=false（200）——这是排障的**正常答案**，不是错误；
//   - 探针在线但查询失败 → connected=true + error；
//   - 成功 → 扁平化探针快照。
func (s *BridgeService) QueryBridgeStatus(instanceID uint) (*BridgeStatusResult, error) {
	inst, node, err := s.resolveInstanceNode(instanceID)
	if err != nil {
		return nil, err
	}
	result := &BridgeStatusResult{InstanceID: instanceID}
	if node.UUID == "" {
		result.Error = "节点不存在"
		return result, nil
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		result.Error = "节点未连接"
		return result, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), bridgeStatusTimeout)
	defer cancel()
	resp, err := client.Worker.QueryBridgeStatus(ctx, &workerpb.QueryBridgeStatusRequest{InstanceUuid: inst.UUID})
	if err != nil {
		result.Error = "桥状态查询调用失败"
		return result, nil
	}
	applyBridgeStatusResponse(result, resp)
	return result, nil
}

// applyBridgeStatusResponse 把 Worker 回执映射为契约形态（抽出便于单测）。
func applyBridgeStatusResponse(result *BridgeStatusResult, resp *workerpb.QueryBridgeStatusResponse) {
	if resp == nil {
		result.Error = "无桥状态响应"
		return
	}
	result.Connected = resp.Connected
	if resp.FetchedAtMs > 0 {
		result.FetchedAt = formatEpochMillis(resp.FetchedAtMs)
	}
	if !resp.Success {
		result.Error = fallbackBridgeMsg(resp.Error, "桥状态查询失败")
		return
	}
	if !resp.Connected {
		// 探针不在：字段保持零值，前端据此展示「探针未连入」。
		result.Error = fallbackBridgeMsg(resp.Error, "探针未连入")
		return
	}
	var wire bridgeStatusWire
	if strings.TrimSpace(resp.StatusJson) != "" {
		if err := json.Unmarshal([]byte(resp.StatusJson), &wire); err != nil {
			// 回执解析不出来：仍然把 connected 填上（探针确实在），其余留零值并说明原因。
			result.Error = "桥状态回执无法解析"
			return
		}
	}
	result.ReconnectCount = wire.Reconnects
	result.LastDisconnectReason = wire.LastDisconnectReason
	result.ProbeVersion = wire.Version
	result.QueueDepth = wire.Sender.CommandResultQueue + wire.Sender.ReliableQueue +
		wire.Pools.Command.Queued + wire.Pools.Event.Queued
	// 丢弃 = 可丢事件被丢 + 可靠通道被拒 + 落盘缓冲挤掉。
	// 三者都是"已受理但没送到"，对排障是同一个问题（可靠事件不应出现在这里）。
	result.DroppedFrames = wire.Sender.BestEffortDropped + wire.Sender.ReliableRejected + wire.Sender.SpillDropped
	result.PendingReloads = wire.Pools.Reload.Active
	if wire.LastConnectedAtMs > 0 {
		result.ConnectedSince = formatEpochMillis(wire.LastConnectedAtMs)
	}
}

// SetSubscription 设置某实例的事件主题订阅（FR-526 §2.1）。
//
// ## 语义
//
//   - **整批覆盖**，不是追加（探针侧 `core.subscribe` 就是 replace）；
//   - **topics 与 domains 都为空 = 订阅全部**（探针 clear → 全量上报，等价于从未设置）；
//   - 重复 PUT 覆盖之前的订阅；
//   - 探针未连接 → ErrBridgeProbeUnavailable（路由回 409 PROBE_UNAVAILABLE，OpenAPI 明确规定）。
//
// 返回值是探针回执的**实际生效集合**，可能与请求不同（通配展开、去重、清空）。
func (s *BridgeService) SetSubscription(instanceID uint, topics, domains []string) (applied []string, err error) {
	inst, node, err := s.resolveInstanceNode(instanceID)
	if err != nil {
		return nil, err
	}
	if node.UUID == "" {
		return nil, ErrBridgeProbeUnavailable
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return nil, ErrBridgeProbeUnavailable
	}

	ctx, cancel := context.WithTimeout(context.Background(), bridgeSubscriptionTimeout)
	defer cancel()
	resp, err := client.Worker.SetBridgeSubscription(ctx, &workerpb.SetBridgeSubscriptionRequest{
		InstanceUuid: inst.UUID,
		Topics:       topics,
		Domains:      domains,
	})
	if err != nil {
		return nil, fmt.Errorf("订阅下发调用失败: %w", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("订阅下发无响应")
	}
	if !resp.Connected {
		return nil, ErrBridgeProbeUnavailable
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", fallbackBridgeMsg(resp.Error, "订阅被探针拒绝"))
	}
	applied = resp.AppliedTopics
	if applied == nil {
		applied = []string{}
	}
	return applied, nil
}

// QueryBusinessStatus 查询某实例已注册的业务域（FR-526 §2.4）。
//
// ## 缓存策略（TTL 30 秒）
//
//   - 命中且未过期 → 直接返回缓存（`stale=false`）；
//   - 过期或未命中 → 实时查探针；成功则刷新缓存，失败/探针不在则回退**最后一次已知**
//     并置 `stale=true`（比返回空更有用，OpenAPI 明确要求）；
//   - 从未成功查过且本次也失败 → 200 + 空 domains + stale=true + error。
//
// 缓存只在内存、不落库：域状态是探针运行期快照，落库会让人当它是平台数据（ADR-106）。
func (s *BridgeService) QueryBusinessStatus(instanceID uint) (*BusinessStatusResult, error) {
	inst, node, err := s.resolveInstanceNode(instanceID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	if cached, ok := s.lookupBusinessStatusCache(instanceID, now); ok {
		return cached, nil
	}

	result := &BusinessStatusResult{InstanceID: instanceID, InstanceName: inst.Name}
	fresh := s.fetchBusinessStatus(result, inst, node)
	if fresh {
		// 记录当次连接态：之后连接态一变，这份缓存即失效（见 lookupBusinessStatusCache）。
		s.storeBusinessStatusCache(instanceID, inst.Name, result.Domains, now, result.ProbeConnected)
		return result, nil
	}
	// 实时查询不可得：回退最后已知（含刚过期的那份），并说明原因。
	if cached, ok := s.lookupLastKnown(instanceID); ok {
		cached.Stale = true
		cached.ProbeConnected = false
		if result.Error != "" {
			cached.Error = result.Error
		}
		return cached, nil
	}
	result.Stale = true
	result.ProbeConnected = false
	if result.Error == "" {
		result.Error = "探针不可达，暂无已知域状态"
	}
	// 契约里 domains 是 array：空也必须序列化成 []，不能是 null（前端对两者处理不一致）。
	if result.Domains == nil {
		result.Domains = []DomainStatusResult{}
	}
	return result, nil
}

// fetchBusinessStatus 实时查探针并把结果写入 result；返回是否拿到了可信数据。
func (s *BridgeService) fetchBusinessStatus(result *BusinessStatusResult, inst model.Instance, node model.Node) bool {
	if node.UUID == "" {
		result.Error = "节点不存在"
		return false
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		result.Error = "节点未连接"
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), businessStatusTimeout)
	defer cancel()
	resp, err := client.Worker.QueryBusinessStatus(ctx, &workerpb.QueryBusinessStatusRequest{InstanceUuid: inst.UUID})
	if err != nil {
		// 把 gRPC 的真实原因带出来，不要压成一句固定文案。
		//
		// ## 为什么这一行值得单独写一段注释
		//
		// 这里原本是 `result.Error = "域状态查询调用失败"`，把 err 整个丢掉。
		// 代价是：Worker 侧 gRPC 通道因为「string field contains invalid UTF-8」
		// 从 2026-09-30 起每 10 秒断一次、累计刷了 6 万余条日志，
		// 而 CP 面板上只显示一句无信息量的「域状态查询调用失败」，
		// 排查时完全看不出是网络、超时还是数据脏。
		//
		// 调用方（面板/运维）需要的是「为什么」，不是「失败了」。
		result.Error = "域状态查询调用失败: " + err.Error()
		return false
	}
	return applyBusinessStatusResponse(result, resp)
}

// applyBusinessStatusResponse 把 Worker 回执映射为契约形态（抽出便于单测）；返回是否可信。
func applyBusinessStatusResponse(result *BusinessStatusResult, resp *workerpb.QueryBusinessStatusResponse) bool {
	if resp == nil {
		result.Error = "无域状态响应"
		return false
	}
	result.ProbeConnected = resp.Connected
	if resp.FetchedAtMs > 0 {
		result.FetchedAt = formatEpochMillis(resp.FetchedAtMs)
	}
	if !resp.Success {
		result.Error = fallbackBridgeMsg(resp.Error, "域状态查询失败")
		return false
	}
	if !resp.Connected {
		result.Error = fallbackBridgeMsg(resp.Error, "探针未连入")
		return false
	}
	domains := parseBusinessStatusDomains(resp.StatusJson)
	if domains == nil {
		result.Error = "域状态回执无法解析"
		return false
	}
	result.Domains = domains
	return true
}

// businessStatusWire 是探针 `core.business_status` 回执的最小视图。
//
// 只取 OpenAPI `DomainStatus` 需要的字段：探针回执里另有 health/reason/detailJson/
// quarantine/dispatch 等更细结构，CP 刻意不建模（同上，避免长出业务查询能力）。
type businessStatusWire struct {
	Domains map[string]struct {
		Available      bool   `json:"available"`
		DegradedReason string `json:"degradedReason"`
		ManifestDigest string `json:"manifestDigest"`
		LastDispatchAt int64  `json:"lastDispatchAt"`
		DispatchCount  int64  `json:"dispatchCount"`
		FailureCount   int64  `json:"failureCount"`
	} `json:"domains"`
}

// parseBusinessStatusDomains 解析探针域状态回执为契约域列表（按域名排序）。
//
// 回执非法返回 nil（与"域数为 0"区分开：后者是合法的空域清单）。
func parseBusinessStatusDomains(statusJSON string) []DomainStatusResult {
	trimmed := strings.TrimSpace(statusJSON)
	if trimmed == "" {
		return nil
	}
	var wire businessStatusWire
	if err := json.Unmarshal([]byte(trimmed), &wire); err != nil {
		return nil
	}
	out := make([]DomainStatusResult, 0, len(wire.Domains))
	for name, d := range wire.Domains {
		out = append(out, DomainStatusResult{
			Domain:         name,
			Available:      d.Available,
			DegradedReason: d.DegradedReason,
			ManifestDigest: d.ManifestDigest,
			LastDispatchAt: d.LastDispatchAt,
			DispatchCount:  d.DispatchCount,
			FailureCount:   d.FailureCount,
		})
	}
	// 排序只为渲染稳定（探针回执本身就是 map，顺序不保证）。
	sortDomainStatus(out)
	return out
}

// lookupBusinessStatusCache 取未过期的缓存。
// lookupBusinessStatusCache 查域状态缓存。
//
// 【设计取舍】连接态一变就失效，但**不在查询路径上反查连接态**——
// 那会让每次命中缓存都多打一次 QueryBridgeStatus RPC（真机验证过：TTL 内也会 +1 次调用）。
// 改为由 [InvalidateOnProbeConnected] 在收到探针 connected/disconnected 事件时主动清缓存：
// 事件驱动一次，胜过查询路径上每次多一跳。
func (s *BridgeService) lookupBusinessStatusCache(instanceID uint, now time.Time) (*BusinessStatusResult, bool) {
	s.cacheMu.RLock()
	entry, ok := s.cache[instanceID]
	s.cacheMu.RUnlock()
	if !ok || now.Sub(entry.fetched) >= BusinessStatusCacheTTL {
		return nil, false
	}
	return entry.toResult(instanceID, false), true
}

// InvalidateOnProbeConnected 探针连入时清掉该实例的域状态缓存（供 PlayerEventService 回调）。
//
// 【为什么连入时也要清】真机验收发现：探针刚连上时，bridge/status（无缓存、实时查）
// 已返回 connected=true，而 business/status 的 30s 缓存还停在探针离线时的
// `probeConnected=false + stale=true`，两块面板同时展示就自相矛盾。
//
// 连入瞬间清一次，下一次查询即可拿到真实域清单；断开后同样清掉，
// 避免展示"早已失效的域列表"却标称非 stale。
//
// @param instanceUUID 实例 UUID（事件里带来的形态）；非 UUID 或查不到实例时静默忽略。
func (s *BridgeService) InvalidateOnProbeConnected(instanceUUID string) {
	if instanceUUID == "" {
		return
	}
	var inst model.Instance
	if err := s.db.Select("id").Where("uuid = ?", instanceUUID).First(&inst).Error; err != nil {
		return // 实例可能已删除；缓存清不清无所谓
	}
	s.cacheMu.Lock()
	delete(s.cache, inst.ID)
	s.cacheMu.Unlock()
}

// lookupLastKnown 取最后已知状态（**不看 TTL**）：用于探针不可达时的 stale 回退。
func (s *BridgeService) lookupLastKnown(instanceID uint) (*BusinessStatusResult, bool) {
	s.cacheMu.RLock()
	entry, ok := s.cache[instanceID]
	s.cacheMu.RUnlock()
	if !ok {
		return nil, false
	}
	return entry.toResult(instanceID, true), true
}

// storeBusinessStatusCache 写入缓存（浅拷贝 domains，避免调用方后续改动污染缓存）。
//
// cachedConnected 由调用方传入当次查询的连接态：之后连接态一变，这份缓存即失效。
func (s *BridgeService) storeBusinessStatusCache(instanceID uint, name string, domains []DomainStatusResult, now time.Time, connected bool) {
	copied := make([]DomainStatusResult, len(domains))
	copy(copied, domains)
	s.cacheMu.Lock()
	s.cache[instanceID] = bridgeBusinessStatusCache{
		domains:         copied,
		fetched:         now,
		name:            name,
		instance:        instanceID,
		cachedConnected: connected,
	}
	s.cacheMu.Unlock()
}

// toResult 把缓存条目转成对外的结果形态（每次都拷一份，调用方改不到缓存）。
func (e bridgeBusinessStatusCache) toResult(instanceID uint, stale bool) *BusinessStatusResult {
	domains := make([]DomainStatusResult, len(e.domains))
	copy(domains, e.domains)
	return &BusinessStatusResult{
		InstanceID:   instanceID,
		InstanceName: e.name,
		// stale 表示「这是探针不可达时的最后已知」，此时连接态必为 false；
		// 非 stale 时用**写入缓存那一刻**的真实连接态，而不是无脑 true ——
		// 后者会让「缓存写入时本就离线」的条目伪装成在线。
		ProbeConnected: !stale && e.cachedConnected,
		FetchedAt:      e.fetched.Format(time.RFC3339),
		Stale:          stale,
		Domains:        domains,
	}
}

// formatEpochMillis 把 epoch 毫秒转 RFC3339（UTC）。<=0 调用方已拦，这里只做格式化。
func formatEpochMillis(ms int64) string {
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// fallbackBridgeMsg 优先用 Worker/探针透传的具体原因，否则用兜底文案。
func fallbackBridgeMsg(detail, fallback string) string {
	if strings.TrimSpace(detail) == "" {
		return fallback
	}
	return detail
}

// sortDomainStatus 按域名升序排序（稳定渲染）。
func sortDomainStatus(list []DomainStatusResult) {
	sort.Slice(list, func(i, j int) bool { return list[i].Domain < list[j].Domain })
}

// BusinessStatusBatchMaxIDs 批量域状态查询的单次上限（spec FR-534 §5.2：50）。
//
// 上限的存在理由是**保护探针**：每条查询都会实打实打到该实例的探针（未命中缓存时），
// 一次几百条会把节点上的插件桥打满。
const BusinessStatusBatchMaxIDs = 50

// BusinessStatusBatchConcurrency 批量查询的并发上限。
//
// 与 BusinessStatusBatchMaxIDs 分开：上限管"调用方能传多少"，
// 并发管"同一时刻打多少个探针"。过小会让 50 台的批量退化成串行，
// 过大则一次把节点连接池占满、拖慢其他面板请求。
const BusinessStatusBatchConcurrency = 8

// BusinessStatusBatchItem 是批量查询里单个实例的结果。
//
// 刻意**不返回 error 字段**：查询失败已经从 QueryBusinessStatus 的契约里
// 表达成 `stale=true` + `error` 文本，这里只需要把结果按实例归位即可。
// 但实例不存在/无权限这类**调用方问题**要单独标出，否则调用方会以为
// 只是"探针没连上"而反复重试。
type BusinessStatusBatchItem struct {
	InstanceID uint `json:"instanceId"`
	// NotFound 表示该实例不存在或调用方无权访问（其余字段为空）。
	//
	// 存在性隐藏原则与单实例接口一致：不区分"不存在"与"无权"，
	// 否则调用方能靠批量接口枚举出自己无权访问的实例 ID 是否存在。
	NotFound bool                 `json:"notFound"`
	Status   *BusinessStatusResult `json:"status,omitempty"`
}

// QueryBusinessStatusBatch 批量查询多个实例的域状态。
//
// 语义与单实例完全一致（同一份缓存、同样的 stale 回退），只是并发化 + 数量受限。
// 逐实例独立成败：某台失败不影响其他台，也不让整体返回错误。
//
// @param instanceIDs 实例 ID 列表；调用方须先去重，重复 ID 会产生重复结果。
// @return 与入参**等长同序**的结果切片（便于前端按下标对应），以及是否触发了上限拒绝。
func (s *BridgeService) QueryBusinessStatusBatch(instanceIDs []uint) ([]BusinessStatusBatchItem, error) {
	if len(instanceIDs) == 0 {
		return []BusinessStatusBatchItem{}, nil
	}
	if len(instanceIDs) > BusinessStatusBatchMaxIDs {
		return nil, ErrBusinessStatusBatchTooLarge
	}

	out := make([]BusinessStatusBatchItem, len(instanceIDs))
	sem := make(chan struct{}, BusinessStatusBatchConcurrency)
	var wg sync.WaitGroup

	for i, id := range instanceIDs {
		wg.Add(1)
		go func(idx int, instanceID uint) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			res, err := s.QueryBusinessStatus(instanceID)
			if err != nil {
				// ErrInstanceNotFound 是唯一可预期的"调用方问题"；
				// 其他错误在单实例契约里已被降级成 stale 结果，理论到不了这里。
				out[idx] = BusinessStatusBatchItem{InstanceID: instanceID, NotFound: true}
				return
			}
			out[idx] = BusinessStatusBatchItem{InstanceID: instanceID, Status: res}
		}(i, id)
	}
	wg.Wait()
	return out, nil
}

// ErrBusinessStatusBatchTooLarge 批量查询实例数超上限。
var ErrBusinessStatusBatchTooLarge = errors.New("批量查询实例数超过上限")
