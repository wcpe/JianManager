package grpc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/wcpe/JianManager/internal/worker/ws"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 桥能力接入：把 ServerProbe 已具备、JM 尚未消费的 `core.*` 治理能力
// （桥状态 / 主题订阅 / 业务域状态 / 配置重载）在 Worker gRPC 面显式化。
//
// ## 为什么不复用 SendPluginCommand
//
// 这几条命令的**超时口径互不相同**，而 SendPluginCommand 把 5s 写死在
// `pluginCommandTimeout`：
//   - 桥状态 / 域状态 / 订阅是内存快照或纯内存改写，秒级足够；
// 配置重载跑在探针独立的 reload 协调池上，默认 **10s**（§2.2），
//     且绝不能与业务命令争用同一个超时预算——5s 会把正常的重载判成超时。
//
// 另外它们的**降级形态也不同**：业务命令失败要 available=false + 原因，而桥状态
// 在探针未连时应该明确返回 connected=false，让 CP 走 stale 缓存而不是当成错误。
// 把这些差异塞进 SendPluginCommand 的 success/error 语义里，调用方就只能靠解析
// 错误字符串来分流——所以拆成独立 RPC。

const (
	// 探针内建治理域。`core.*` 路由不受主题订阅过滤（见 ServerProbe OutboundFrame.isBusinessDomain）。
	probeCoreDomain = "core"

	// 三条只读/轻量治理命令的动作名（与 ServerProbe 路由常量逐字一致）。
	probeActionBridgeStatus   = "bridge_status"
	probeActionSubscribe      = "subscribe"
	probeActionBusinessStatus = "business_status"
	probeActionConfigReload   = "config_reload"

	// probeMetaTimeout 是桥状态/订阅/域状态三条命令等回执的上限。
	//
	// 它们都只读内存快照或做纯内存改写（ServerProbe 侧不走 Provider、不做 IO），
	// 5s 与业务命令同量级；独立成常量是为了让"重载为什么是 10s"这条差异有对照物。
	probeMetaTimeout = 5 * time.Second

	// ProbeConfigReloadTimeout 是配置重载等回执的默认上限（§2.2：独立配置，默认 10s）。
	//
	// 与业务命令的 5s **刻意不等**：探针侧重载会逐个通知已注册监听器（可能含插件热重载），
	// 5s 会把正常重载判成超时；而共用一个超时预算就等于让重载拖慢业务命令。
	ProbeConfigReloadTimeout = 10 * time.Second
)

// probeCommandError 把探针/桥的失败统一成"可判别"的错误，供各 RPC 分流降级。
var (
	// errProbeBridgeDisabled 本节点未启用插件桥（Worker 配置层面没开）。
	errProbeBridgeDisabled = errors.New("本节点未启用插件桥")
	// errProbeNotConnected 实例当前无活动探针会话。
	errProbeNotConnected = errors.New("探针未连接")
)

// probeRoundTrip 向探针下发一条 `core.*` 命令并同步等回执。
//
// connected=false 时**不下发**：探针不在，等待只会白等到超时，而调用方要的答案
// （"探针不在"）现在已经有了。这与 QueryServerState 的既有利落降级一致。
func (s *Server) probeRoundTrip(instanceUUID, action, payloadJSON string, timeout time.Duration) (ws.CommandResult, bool, error) {
	if s.bridge == nil {
		return ws.CommandResult{}, false, errProbeBridgeDisabled
	}
	if !s.bridge.IsConnected(instanceUUID) {
		return ws.CommandResult{}, false, errProbeNotConnected
	}
	requestID := uuid.NewString()
	frame := pluginCommandFrame(&workerpb.PluginCommand{
		Domain:      probeCoreDomain,
		Action:      action,
		PayloadJson: payloadJSON,
		RequestId:   requestID,
	})
	res, err := s.bridge.SendCommandAndWait(instanceUUID, requestID, frame, timeout)
	if err != nil {
		if errors.Is(err, ws.ErrBridgeNotConnected) {
			return ws.CommandResult{}, false, errProbeNotConnected
		}
		return ws.CommandResult{}, true, err
	}
	return res, true, nil
}

// QueryBridgeStatus 取探针桥线路健康度（`core.bridge_status`§2.3）。
//
// 探针不在 → connected=false + 空 status_json，**不算错误**：CP 据此走"无缓存可降级"
// 分支，而不是把它记成一次失败（探针不在是常态，不是事故）。
// 探针在但查询超时 → connected=true + error（让 CP 能区分"不在"与"卡住"）。
func (s *Server) QueryBridgeStatus(_ context.Context, req *workerpb.QueryBridgeStatusRequest) (*workerpb.QueryBridgeStatusResponse, error) {
	res, connected, err := s.probeRoundTrip(req.InstanceUuid, probeActionBridgeStatus, "", probeMetaTimeout)
	now := time.Now().UnixMilli()
	switch {
	case errors.Is(err, errProbeBridgeDisabled):
		return &workerpb.QueryBridgeStatusResponse{Success: false, Error: err.Error(), FetchedAtMs: now}, nil
	case errors.Is(err, errProbeNotConnected):
		return &workerpb.QueryBridgeStatusResponse{Success: true, Connected: false, FetchedAtMs: now}, nil
	case err != nil:
		return &workerpb.QueryBridgeStatusResponse{Success: false, Connected: connected, Error: err.Error(), FetchedAtMs: now}, nil
	case !res.Success:
		// 探针在线但拒绝了查询（例如老版本没有该路由）：把探针给的原因原样带回。
		return &workerpb.QueryBridgeStatusResponse{Success: false, Connected: true, Error: res.Error, FetchedAtMs: now}, nil
	}
	return &workerpb.QueryBridgeStatusResponse{
		Success:     true,
		Connected:   true,
		StatusJson:  res.Output,
		FetchedAtMs: now,
	}, nil
}

// SetBridgeSubscription 设置事件主题订阅（`core.subscribe`§2.1）。
//
// ## 语义（务必与 OpenAPI 对齐）
//
//   - **整批覆盖**，不是追加：探针侧 `subscribe` 就是 replace 语义；
//   - **空 topics + 空 domains = 订阅全部**：探针把空数组当 clear（恢复全量上报），
//     与"从未设置过订阅"等价；
//   - **domains 展开成 topic 通配**：探针侧的过滤维度只有 `topic`（没有独立 domain 维度），
//     故把 `activity` 展开成 `activity.*` 并入 topics 下发。这样对外契约里有 domains，
//     却不需要改探针帧协议，也不会出现"我订阅了域却收不到"。
//   - `applied_topics` 回传探针的**生效集合**（去重、字典序），可能与请求值不同
//     （通配展开、去重、清空），CP 以此为准回前端。
func (s *Server) SetBridgeSubscription(_ context.Context, req *workerpb.SetBridgeSubscriptionRequest) (*workerpb.SetBridgeSubscriptionResponse, error) {
	topics := expandSubscriptionTopics(req.Topics, req.Domains)
	payload := marshalJSON(map[string]any{"topics": topics})

	res, connected, err := s.probeRoundTrip(req.InstanceUuid, probeActionSubscribe, payload, probeMetaTimeout)
	switch {
	case errors.Is(err, errProbeBridgeDisabled):
		return &workerpb.SetBridgeSubscriptionResponse{Success: false, Error: err.Error()}, nil
	case errors.Is(err, errProbeNotConnected):
		// 订阅要探针在线才谈得上生效，故这里显式 connected=false 让 CP 回 409 PROBE_UNAVAILABLE。
		return &workerpb.SetBridgeSubscriptionResponse{Success: false, Connected: false, Error: err.Error()}, nil
	case err != nil:
		return &workerpb.SetBridgeSubscriptionResponse{Success: false, Connected: connected, Error: err.Error()}, nil
	case !res.Success:
		return &workerpb.SetBridgeSubscriptionResponse{Success: false, Connected: true, Error: res.Error}, nil
	}
	return &workerpb.SetBridgeSubscriptionResponse{
		Success:       true,
		Connected:     true,
		AppliedTopics: parseAppliedTopics(res.Output),
	}, nil
}

// QueryBusinessStatus 取探针已注册业务域与健康状态（`core.business_status`§2.4）。
//
// status_json 原样透传：CP **不解析** manifest / 健康语义（ADR-106：JM 只渲染不解释）。
func (s *Server) QueryBusinessStatus(_ context.Context, req *workerpb.QueryBusinessStatusRequest) (*workerpb.QueryBusinessStatusResponse, error) {
	payload := ""
	if domain := strings.TrimSpace(req.Domain); domain != "" {
		payload = marshalJSON(map[string]any{"domain": domain})
	}
	res, connected, err := s.probeRoundTrip(req.InstanceUuid, probeActionBusinessStatus, payload, probeMetaTimeout)
	now := time.Now().UnixMilli()
	switch {
	case errors.Is(err, errProbeBridgeDisabled):
		return &workerpb.QueryBusinessStatusResponse{Success: false, Error: err.Error(), FetchedAtMs: now}, nil
	case errors.Is(err, errProbeNotConnected):
		return &workerpb.QueryBusinessStatusResponse{Success: true, Connected: false, FetchedAtMs: now}, nil
	case err != nil:
		return &workerpb.QueryBusinessStatusResponse{Success: false, Connected: connected, Error: err.Error(), FetchedAtMs: now}, nil
	case !res.Success:
		return &workerpb.QueryBusinessStatusResponse{Success: false, Connected: true, Error: res.Error, FetchedAtMs: now}, nil
	}
	return &workerpb.QueryBusinessStatusResponse{
		Success:     true,
		Connected:   true,
		StatusJson:  res.Output,
		FetchedAtMs: now,
	}, nil
}

// ReloadProbeConfig 下发配置重载（`core.config_reload`§2.2）。
//
// ## 独立超时
//
// 默认 [ProbeConfigReloadTimeout]（10s），**不复用**业务命令的 5s；调用方可经
// `timeout_ms` 覆盖（编排可能需要按目标调整）。Worker 侧再夹一层上限，
// 避免调用方传个离谱值把 gRPC 连接拖住。
//
// ## 只提供通道
//
// （生效编排）尚未实现：本 RPC 只保证"能下发并拿回探针的结构化回执"，
// 不维护生效状态机、不做重试、不落库。回执 JSON 原样透传（reloaded/bestEffort/failed）。
func (s *Server) ReloadProbeConfig(_ context.Context, req *workerpb.ReloadProbeConfigRequest) (*workerpb.ReloadProbeConfigResponse, error) {
	timeout := reloadTimeout(req.TimeoutMs)
	// paths 为空 = 全量重载（探针语义）。显式带上空数组而不是空 payload，
	// 让探针的 decode 走"有 paths 键"分支，语义更明确。
	paths := req.Paths
	if paths == nil {
		paths = []string{}
	}
	payload := marshalJSON(map[string]any{"paths": paths})

	start := time.Now()
	res, connected, err := s.probeRoundTrip(req.InstanceUuid, probeActionConfigReload, payload, timeout)
	elapsed := time.Since(start).Milliseconds()
	switch {
	case errors.Is(err, errProbeBridgeDisabled):
		return &workerpb.ReloadProbeConfigResponse{Success: false, Error: err.Error(), DurationMs: elapsed}, nil
	case errors.Is(err, errProbeNotConnected):
		return &workerpb.ReloadProbeConfigResponse{Success: false, Connected: false, Error: err.Error(), DurationMs: elapsed}, nil
	case err != nil:
		// 超时在此分支：探针在但没在期限内回执。回执 JSON 为空（没有可透传的明细）。
		return &workerpb.ReloadProbeConfigResponse{Success: false, Connected: connected, Error: err.Error(), DurationMs: elapsed}, nil
	}
	// success=false 时**仍带 receipt_json**：探针用 ofFailure 而非 fail 正是为了保住机器可读明细
	//（哪些路径 reloaded、哪些 failed），丢了它 CP 就只能看到一句中文。
	return &workerpb.ReloadProbeConfigResponse{
		Success:     res.Success,
		Connected:   connected,
		Error:       res.Error,
		ReceiptJson: res.Output,
		DurationMs:  elapsed,
	}, nil
}

// reloadTimeout 解析并夹紧重载超时：0 用默认，负值/过大一律回落到默认或上限。
func reloadTimeout(ms int64) time.Duration {
	if ms <= 0 {
		return ProbeConfigReloadTimeout
	}
	// 上限 60s：重载比业务命令宽松，但也不该让一条 gRPC 无限期挂着。
	const maxReloadTimeout = 60 * time.Second
	d := time.Duration(ms) * time.Millisecond
	if d > maxReloadTimeout {
		return maxReloadTimeout
	}
	return d
}

// expandSubscriptionTopics 把对外契约的 (topics, domains) 合并成探针侧只认的 topic 表达式列表。
//
// 规则：
//   - topics 原样保留（支持 `.*` 后缀通配，由对端匹配）；
//   - domains 逐个展开成 `<domain>.*`——前缀匹配在探针侧覆盖该域下的一切主题；
//   - 已带 `.*` 或 `*` 的 domain 不重复展开；空串忽略；
//   - 去重保序，便于回执与日志对账。
//
// 注意**不能**在 domains 为空时补一个 `*`：探针的 TopicMatcher 只认 `expr` 或 `expr.*`，
// 裸 `*` 不匹配任何主题（会被当成字面量），补它等于静默吞掉全部事件。
func expandSubscriptionTopics(topics, domains []string) []string {
	out := make([]string, 0, len(topics)+len(domains))
	seen := make(map[string]struct{}, len(topics)+len(domains))
	add := func(expr string) {
		expr = strings.TrimSpace(expr)
		if expr == "" {
			return
		}
		if _, dup := seen[expr]; dup {
			return
		}
		seen[expr] = struct{}{}
		out = append(out, expr)
	}
	for _, t := range topics {
		add(t)
	}
	for _, d := range domains {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if strings.HasSuffix(d, ".*") || d == "*" {
			add(d)
			continue
		}
		add(d + ".*")
	}
	return out
}

// parseAppliedTopics 解析探针 `core.subscribe` 回执里的生效集合（JSON 字符串数组）。
//
// 解析失败返回 nil 而不是报错：订阅本身已生效（探针 success=true），
// 回执解析不出来只影响"回显哪些表达式"，不该把一次成功的订阅变成失败。
func parseAppliedTopics(output string) []string {
	if strings.TrimSpace(output) == "" {
		return nil
	}
	var topics []string
	if err := json.Unmarshal([]byte(output), &topics); err != nil {
		return nil
	}
	return topics
}

// marshalJSON 序列化下发给探针的 payload（结构简单，失败退化为空对象）。
func marshalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
