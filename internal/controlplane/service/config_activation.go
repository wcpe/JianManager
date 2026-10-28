package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 配置生效编排（FR-532）。
//
// 三种方式（spec §13）：hot_reload / restart / push_only。本文件的唯一难点不是「怎么下发」，
// 而是 **怎么诚实地说明结果**：
//
//   - 插件回执明确列出 reloaded → 才能说「配置已重新加载」；
//   - 插件只给了 bestEffort（Bukkit 的 reloadConfig() 是 void 返回，异常被插件吞掉后
//     探针无从得知）→ 只能说「已请求重载，插件未提供确认」，**绝不能说已生效**；
//   - 超时 → 「重载超时，配置可能未生效」，且该路径进熔断名单，短时间内不自动重试；
//   - restart 停了没起来 → 必须判失败（这是最危险的失败模式，spec §15）。
//
// ActivationOutcome 用三个正交布尔（Confirmed / BestEffort / TimedOut）表达这一切，
// 而不是一个枚举：状态机需要的是「能不能算成功」与「能不能对外声称已生效」两个独立答案，
// 压进一个枚举后 UI 就只能在两者之间二选一，必然有一边是假的。

// 生效原因码（与 FR-528 错误码表同风格，大写蛇形）。
const (
	// ActivateCodeReloaded 插件明确回报已重载。
	ActivateCodeReloaded = "RELOADED"
	// ActivateCodeBestEffort 已下发但插件未提供确切回执。
	ActivateCodeBestEffort = "BEST_EFFORT"
	// ActivateCodeTimeout 等待重载回执超时。
	ActivateCodeTimeout = "TIMEOUT"
	// ActivateCodeProbeUnavailable 探针未连入，重载无法下发。
	ActivateCodeProbeUnavailable = "PROBE_UNAVAILABLE"
	// ActivateCodeBridgeDisabled 本节点未启用插件桥。
	ActivateCodeBridgeDisabled = "BRIDGE_DISABLED"
	// ActivateCodePluginFailed 插件回执里明确报了失败。
	ActivateCodePluginFailed = "PLUGIN_RELOAD_FAILED"
	// ActivateCodeReceiptUnparsed 回执无法解析（只能算 best effort）。
	ActivateCodeReceiptUnparsed = "RECEIPT_UNPARSED"
	// ActivateCodeProbeNotApplicable 实例不适用探针（非 MC 服务端）。
	ActivateCodeProbeNotApplicable = "PROBE_NOT_APPLICABLE"
	// ActivateCodeRestartFailed 停/起实例本身失败。
	ActivateCodeRestartFailed = "RESTART_FAILED"
	// ActivateCodeActivateTimeout 重启后等待健康回归超时。
	ActivateCodeActivateTimeout = "ACTIVATE_TIMEOUT"
	// ActivateCodePushOnly 只落盘，未触发生效。
	ActivateCodePushOnly = "PUSH_ONLY"
	// ActivateCodeCooldown 该路径刚超时过，仍在熔断名单冷静期内，不自动重试。
	ActivateCodeCooldown = "RELOAD_COOLDOWN"
)

// 默认值（spec §6 / §14.2）。
const (
	// DefaultActivateTimeoutSec 生效等待上限默认 300s。
	DefaultActivateTimeoutSec = 300
	// DefaultReloadTimeoutSec core.config_reload 独立超时默认 10s，**不复用业务命令的 5s**。
	DefaultReloadTimeoutSec = 10
	// DefaultObserveWindowSec 批间观察窗默认 120s。
	DefaultObserveWindowSec = 120
	// reloadCooldownWindow 重载超时后的冷静期（spec §18 待定 3：暂定 5 分钟）。
	reloadCooldownWindow = 5 * time.Minute
	// restartHealthPollInterval 重启后健康回归的轮询间隔。
	restartHealthPollInterval = 2 * time.Second
)

// ErrDeliveryActivateCooldown 该路径处于重载超时冷静期内，拒绝自动重试。
var ErrDeliveryActivateCooldown = errors.New("该配置路径近期重载超时，仍在冷静期内")

// ActivationOutcome 一次生效动作的诚实结论。
type ActivationOutcome struct {
	// Confirmed 生效得到**确切**确认（插件明确回报该路径已重载 / 重启后健康回归）。
	Confirmed bool `json:"confirmed"`
	// BestEffort 已请求生效但无法确认（插件未提供确切回执）。
	// Confirmed=false + BestEffort=true → 目标记 activated 但文案必须写「未提供确认」。
	BestEffort bool `json:"bestEffort"`
	// TimedOut 等待超时。此时**不能**断言生效失败，只能说「可能未生效」。
	TimedOut bool `json:"timedOut"`
	// Code 机器可读原因码。
	Code string `json:"code,omitempty"`
	// Message 面向运维的诚实文案（UI 直接展示，不得夸大）。
	Message string `json:"message,omitempty"`
	// ReceiptJSON 探针回执原文，原样留存便于对账。
	ReceiptJSON string `json:"-"`
	// CostMs 本次生效耗时（毫秒）。
	CostMs int64 `json:"costMs,omitempty"`
	// CoolingDown 本次是否因冷静期被拒绝下发。
	CoolingDown bool `json:"coolingDown,omitempty"`
}

// Succeeded 返回本次生效是否可计入「成功」。
//
// 注意：best effort **也算成功**（spec §14.1 明确记 activated + 标记），
// 但它与 confirmed 的成功在 UI 上是两句话——这正是两个字段分开的原因。
func (o ActivationOutcome) Succeeded() bool {
	if o.TimedOut {
		return false
	}
	return o.Confirmed || o.BestEffort || o.Code == ActivateCodePushOnly
}

// ReloadReply 是一次 core.config_reload 下发的原始结果（由传输层填充）。
type ReloadReply struct {
	Success     bool
	Connected   bool
	Error       string
	ReceiptJSON string
	Duration    time.Duration
	// TimedOut 传输层判定为超时（探针在但没在期限内回执）。
	TimedOut bool
}

// ConfigActivationService 配置生效编排（FR-532）。
type ConfigActivationService struct {
	db   *gorm.DB
	pool *cpgrpc.ClientPool

	// reloadSender 下发 core.config_reload；测试注入。
	reloadSender func(inst *model.Instance, paths []string, timeout time.Duration) ReloadReply
	// restarter 停→起实例；测试注入。
	restarter func(inst *model.Instance) error
	// probeConnected 探针是否在线（重启后健康回归判定用）；nil 表示不校验探针。
	probeConnectedChecker func(instanceUUID string) bool
	// sleepFn 等待原语；测试注入为 no-op，避免真的睡满观察/超时时间。
	sleepFn func(d time.Duration)
	clock   func() time.Time

	// cooldownMu/cooldowns 重载超时的熔断名单（实例 UUID+路径 → 解禁时刻）。
	cooldownMu sync.Mutex
	cooldowns  map[string]time.Time
}

// NewConfigActivationService 创建生效编排服务。
func NewConfigActivationService(db *gorm.DB, pool *cpgrpc.ClientPool) *ConfigActivationService {
	return &ConfigActivationService{
		db:        db,
		pool:      pool,
		sleepFn:   time.Sleep,
		clock:     time.Now,
		cooldowns: map[string]time.Time{},
	}
}

// SetReloadSenderForTest 注入重载下发实现（生产走 Worker gRPC）。
func (s *ConfigActivationService) SetReloadSender(fn func(inst *model.Instance, paths []string, timeout time.Duration) ReloadReply) {
	s.reloadSender = fn
}

// SetRestarter 注入实例停/起实现（生产走 InstanceService）。
func (s *ConfigActivationService) SetRestarter(fn func(inst *model.Instance) error) {
	s.restarter = fn
}

// SetProbeConnectedChecker 注入探针在线判定（生产走 PlayerEventService.IsProbeConnected）。
// 未注入时重启后的健康回归只校验进程存活——这是**明确的降级**，注释在 waitRestartHealthy 里。
func (s *ConfigActivationService) SetProbeConnectedChecker(fn func(instanceUUID string) bool) {
	s.probeConnectedChecker = fn
}

// SetSleepForTest 注入等待原语（仅测试）。
func (s *ConfigActivationService) SetSleepForTest(fn func(d time.Duration)) {
	if fn != nil {
		s.sleepFn = fn
	}
}

// Activate 按交付单的 activation_method 触发生效。
func (s *ConfigActivationService) Activate(inst *model.Instance, order *model.ConfigDeliveryOrder, filePath string) ActivationOutcome {
	switch order.ActivationMethod {
	case model.ActivationHotReload:
		return s.HotReload(inst, order, filePath)
	case model.ActivationRestart:
		return s.RestartAndWait(inst, order)
	case model.ActivationPushOnly:
		// 只落盘：不触发任何动作，也**不谎称已生效**（下次启动自然生效）。
		return ActivationOutcome{
			Code:    ActivateCodePushOnly,
			Message: "仅落盘，未触发生效（实例下次启动后生效）",
		}
	}
	return ActivationOutcome{
		Code:    ActivateCodePluginFailed,
		Message: fmt.Sprintf("未知的生效方式: %s", order.ActivationMethod),
	}
}

// HotReload 下发 core.config_reload 并据实翻译回执（spec §14.1）。
//
// 回执判定的优先级刻意是「失败 → best effort → 确认」：
// 只要插件报了 failed，无论它是否同时说了 bestEffort，都按失败记——
// 把「插件自己都说失败了」的和成「已请求」是比夸大更严重的失真。
func (s *ConfigActivationService) HotReload(inst *model.Instance, order *model.ConfigDeliveryOrder, filePath string) ActivationOutcome {
	if !model.IsProbeApplicable(inst.Type, inst.Role) {
		// 非 MC 服务端（通用二进制 / Beacon / 代理不一定有 Bukkit 探针）：没有可下发重载的对象。
		// 这里不伪造 bestEffort —— 我们连请求都没发出去。
		return ActivationOutcome{
			Code:    ActivateCodeProbeNotApplicable,
			Message: "实例不适用插件探针，无法下发配置重载",
		}
	}
	if cooling, until := s.inCooldown(inst.UUID, filePath); cooling {
		return ActivationOutcome{
			Code:        ActivateCodeCooldown,
			CoolingDown: true,
			Message:     fmt.Sprintf("该路径近期重载超时，冷静期至 %s，暂不自动重试", until.Format(time.RFC3339)),
		}
	}

	timeout := time.Duration(order.ReloadTimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = DefaultReloadTimeoutSec * time.Second
	}
	paths := []string{}
	if strings.TrimSpace(filePath) != "" {
		paths = []string{filePath}
	}
	reply := s.sendReload(inst, paths, timeout)

	out := ActivationOutcome{
		ReceiptJSON: reply.ReceiptJSON,
		CostMs:      reply.Duration.Milliseconds(),
	}
	switch {
	case reply.TimedOut:
		// 超时后**不能强杀主线程**（Bukkit 不允许），只能标失败并把路径放进冷静期，
		// 避免反复重试把服务器卡死（spec §14.2）。
		s.markCooldown(inst.UUID, filePath)
		out.TimedOut = true
		out.Code = ActivateCodeTimeout
		out.Message = "重载超时，配置可能未生效"
		return out
	case !reply.Connected && reply.Success == false && strings.Contains(reply.Error, "探针"):
		out.Code = ActivateCodeProbeUnavailable
		out.Message = "探针未连入，无法下发配置重载"
		return out
	case !reply.Success && reply.ReceiptJSON == "":
		// 传输层失败且没有可透传的明细：区分「桥未启用」与一般失败。
		if strings.Contains(reply.Error, "插件桥") {
			out.Code = ActivateCodeBridgeDisabled
		} else {
			out.Code = ActivateCodePluginFailed
		}
		out.Message = "重载失败：" + fallbackActivateMsg(reply.Error, "未知原因")
		return out
	}
	return translateReloadReceipt(reply.ReceiptJSON, filePath, reply.Success, reply.Error)
}

// translateReloadReceipt 把探针回执翻译成诚实结论（抽成纯函数便于单测覆盖四种形态）。
func translateReloadReceipt(receiptJSON, filePath string, success bool, transportErr string) ActivationOutcome {
	out := ActivationOutcome{ReceiptJSON: receiptJSON}
	if strings.TrimSpace(receiptJSON) == "" {
		if success {
			// 成功但没有任何回执明细：无法断言该路径已重载。按 best effort 处理，
			// 绝不能因为 success=true 就写「配置已重新加载」。
			out.BestEffort = true
			out.Code = ActivateCodeBestEffort
			out.Message = "已请求重载，插件未提供确认"
			return out
		}
		out.Code = ActivateCodePluginFailed
		out.Message = "重载失败：" + fallbackActivateMsg(transportErr, "未知原因")
		return out
	}

	var receipt probeReloadReceipt
	if err := json.Unmarshal([]byte(receiptJSON), &receipt); err != nil {
		// 回执解析不出来：只能算 best effort。宁可能力弱一点，也不谎报生效。
		out.BestEffort = true
		out.Code = ActivateCodeReceiptUnparsed
		out.Message = "已请求重载，但回执无法解析，无法确认是否生效"
		return out
	}

	// 1. 插件明确报失败：优先，且带上插件给的错误码。
	if len(receipt.Failed) > 0 {
		first := receipt.Failed[0]
		out.Code = ActivateCodePluginFailed
		detail := first.Message
		if detail == "" {
			detail = first.Code
		} else if first.Code != "" {
			detail = first.Code + ": " + detail
		}
		if len(receipt.Failed) > 1 {
			detail = fmt.Sprintf("%s（另有 %d 处失败）", detail, len(receipt.Failed)-1)
		}
		out.Message = "重载失败：" + fallbackActivateMsg(detail, "插件未提供原因")
		return out
	}

	// 2. 插件自述 best effort（未实现确切回执）。
	if receipt.BestEffort {
		out.BestEffort = true
		out.Code = ActivateCodeBestEffort
		out.Message = "已请求重载，插件未提供确认"
		return out
	}

	// 3. 回执明确列出该路径已重载 / 已应用 → 才算确认。
	if receiptConfirms(receipt, filePath) {
		out.Confirmed = true
		out.Code = ActivateCodeReloaded
		out.Message = "配置已重新加载"
		return out
	}

	// 4. 既没失败也没确认：仍然只能算 best effort。
	out.BestEffort = true
	out.Code = ActivateCodeBestEffort
	out.Message = "已请求重载，插件未提供确认"
	return out
}

// probeReloadReceipt 是探针 `core.config_reload` 回执的**宽松**解析形态。
//
// 只声明我们真正据以判定的字段：reloaded/applied 用于确认，failed 用于失败，
// bestEffort 用于诚实降级。其余字段（costMs 等）原样留在 ReceiptJSON 里，CP 不解释。
type probeReloadReceipt struct {
	Reloaded   []string `json:"reloaded"`
	Applied    []string `json:"applied"`
	BestEffort bool     `json:"bestEffort"`
	Failed     []struct {
		Path    string `json:"path"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"failed"`
	CostMs int64 `json:"costMs"`
}

// receiptConfirms 判断回执是否明确确认了目标路径已生效。
//
// 路径比对用「归一化后相等或互为后缀」：探针回报的可能是 `plugins/Foo/config.yml`，
// 而我们请求的是同一路径的相对形式，二者的斜杠风格与起始 `./` 可能不同。
// 请求了具体路径却只拿到空列表时**不算确认**——「什么都没重载」不是「这个文件重载了」。
func receiptConfirms(receipt probeReloadReceipt, filePath string) bool {
	candidates := append([]string{}, receipt.Reloaded...)
	candidates = append(candidates, receipt.Applied...)
	if len(candidates) == 0 {
		return false
	}
	want := normalizeReceiptPath(filePath)
	if want == "" {
		// 未指定路径（全量重载）：只要回报了具体条目即可视为确认。
		return len(candidates) > 0
	}
	for _, c := range candidates {
		got := normalizeReceiptPath(c)
		if got == want || strings.HasSuffix(got, "/"+want) || strings.HasSuffix(want, "/"+got) {
			return true
		}
	}
	return false
}

// normalizeReceiptPath 归一化回执路径用于宽松比对。
func normalizeReceiptPath(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	p = strings.TrimPrefix(p, "./")
	p = strings.Trim(p, "/")
	return strings.ToLower(p)
}

// RestartAndWait 停实例 → 起实例 → 等健康回归（spec §15）。
//
// 「关了没起来」必须判失败：这是最危险的失败模式，绝不能因为「stop 成功」就放过。
func (s *ConfigActivationService) RestartAndWait(inst *model.Instance, order *model.ConfigDeliveryOrder) ActivationOutcome {
	timeout := time.Duration(order.ActivateTimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = DefaultActivateTimeoutSec * time.Second
	}
	start := s.clock()
	if s.restarter != nil {
		if err := s.restarter(inst); err != nil {
			return ActivationOutcome{
				Code:    ActivateCodeRestartFailed,
				Message: "实例重启失败：" + err.Error(),
				CostMs:  s.clock().Sub(start).Milliseconds(),
			}
		}
	} else {
		return ActivationOutcome{
			Code:    ActivateCodeRestartFailed,
			Message: "实例重启失败：未装配实例生命周期能力",
			CostMs:  s.clock().Sub(start).Milliseconds(),
		}
	}
	if err := s.waitRestartHealthy(inst, timeout); err != nil {
		if errors.Is(err, errRestartHealthTimeout) {
			return ActivationOutcome{
				TimedOut: true,
				Code:     ActivateCodeActivateTimeout,
				Message:  "重启后等待健康回归超时，配置可能未生效",
				CostMs:   s.clock().Sub(start).Milliseconds(),
			}
		}
		return ActivationOutcome{
			Code:    ActivateCodeRestartFailed,
			Message: "实例重启失败：" + err.Error(),
			CostMs:  s.clock().Sub(start).Milliseconds(),
		}
	}
	return ActivationOutcome{
		Confirmed: true,
		Code:      ActivateCodeReloaded,
		Message:   "实例已重启并恢复正常",
		CostMs:    s.clock().Sub(start).Milliseconds(),
	}
}

// errRestartHealthTimeout 重启后健康回归超时。
var errRestartHealthTimeout = errors.New("等待实例健康回归超时")

// waitRestartHealthy 轮询等待「进程存活 + 探针重连」。
//
// 探针校验只在实例**适用探针**时进行；不适用时只校验进程状态为 RUNNING。
// 这是明确的降级（没有探针就没有「插件已加载新配置」的证据），因此重启系的结论
// 只能到「进程健康」这一层，注释与 UI 文案都不越界。
func (s *ConfigActivationService) waitRestartHealthy(inst *model.Instance, timeout time.Duration) error {
	deadline := s.clock().Add(timeout)
	needProbe := model.IsProbeApplicable(inst.Type, inst.Role) && s.probeConnectedChecker != nil
	for {
		if s.processRunning(inst.ID) && (!needProbe || s.probeConnectedChecker(inst.UUID)) {
			return nil
		}
		if !s.clock().Before(deadline) {
			return errRestartHealthTimeout
		}
		s.sleepFn(restartHealthPollInterval)
	}
}

// processRunning 读库判断实例是否处于 RUNNING（重启后由心跳/委托回写）。
func (s *ConfigActivationService) processRunning(instanceID uint) bool {
	if s.db == nil {
		return false
	}
	var inst model.Instance
	if err := s.db.Select("id", "status").First(&inst, instanceID).Error; err != nil {
		return false
	}
	return inst.Status == model.InstanceStatusRunning
}

// sendReload 下发重载：优先注入实现，否则走 Worker gRPC。
func (s *ConfigActivationService) sendReload(inst *model.Instance, paths []string, timeout time.Duration) ReloadReply {
	if s.reloadSender != nil {
		return s.reloadSender(inst, paths, timeout)
	}
	return s.sendReloadViaWorker(inst, paths, timeout)
}

// sendReloadViaWorker 经 Worker 的 ReloadProbeConfig 下发（FR-526 已建好通道）。
//
// CP→Worker 的 gRPC 超时比探针回执预算多留 5s：让**探针侧**的超时先触发，
// 这样我们能拿到「探针超时」这个明确结论，而不是 CP 侧先断连后只剩一句 context deadline。
func (s *ConfigActivationService) sendReloadViaWorker(inst *model.Instance, paths []string, timeout time.Duration) ReloadReply {
	if s.pool == nil {
		return ReloadReply{Error: "未装配节点连接池"}
	}
	var node model.Node
	if s.db == nil || s.db.Select("uuid").First(&node, inst.NodeID).Error != nil || node.UUID == "" {
		return ReloadReply{Error: "节点不存在"}
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return ReloadReply{Error: "节点未连接"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout+5*time.Second)
	defer cancel()
	start := s.clock()
	resp, err := client.Worker.ReloadProbeConfig(ctx, &workerpb.ReloadProbeConfigRequest{
		InstanceUuid: inst.UUID,
		Paths:        paths,
		TimeoutMs:    timeout.Milliseconds(),
	})
	elapsed := s.clock().Sub(start)
	if err != nil {
		return ReloadReply{
			Error:    err.Error(),
			Duration: elapsed,
			TimedOut: isTimeoutErr(err),
		}
	}
	if resp == nil {
		return ReloadReply{Error: "无重载响应", Duration: elapsed}
	}
	reply := ReloadReply{
		Success:     resp.Success,
		Connected:   resp.Connected,
		Error:       resp.Error,
		ReceiptJSON: resp.ReceiptJson,
		Duration:    elapsed,
	}
	// Worker 侧已把超时折进 error（receipt 为空）；据此判定超时，而不是靠字符串猜协议。
	if !resp.Success && resp.ReceiptJson == "" && isTimeoutErr(errors.New(resp.Error)) {
		reply.TimedOut = true
	}
	return reply
}

// isTimeoutErr 判断错误文本是否代表超时（gRPC deadline / context 超时 / 网络读超时）。
func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "context deadline") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "超时")
}

// cooldownKey 构造冷静期键（实例 UUID + 归一化路径）。
func cooldownKey(instanceUUID, filePath string) string {
	return instanceUUID + "\x00" + normalizeReceiptPath(filePath)
}

// inCooldown 判断该路径是否在重载超时的冷静期内。
func (s *ConfigActivationService) inCooldown(instanceUUID, filePath string) (bool, time.Time) {
	s.cooldownMu.Lock()
	defer s.cooldownMu.Unlock()
	until, ok := s.cooldowns[cooldownKey(instanceUUID, filePath)]
	if !ok {
		return false, time.Time{}
	}
	if s.clock().After(until) {
		delete(s.cooldowns, cooldownKey(instanceUUID, filePath))
		return false, time.Time{}
	}
	return true, until
}

// markCooldown 把超时的路径放进熔断名单。
func (s *ConfigActivationService) markCooldown(instanceUUID, filePath string) {
	s.cooldownMu.Lock()
	defer s.cooldownMu.Unlock()
	s.cooldowns[cooldownKey(instanceUUID, filePath)] = s.clock().Add(reloadCooldownWindow)
	slog.Warn("配置重载超时，路径进入冷静期",
		"instanceUUID", instanceUUID, "path", filePath, "window", reloadCooldownWindow)
}

// CoolingDown 对外暴露冷静期查询（路由/测试用）。
func (s *ConfigActivationService) CoolingDown(instanceUUID, filePath string) bool {
	ok, _ := s.inCooldown(instanceUUID, filePath)
	return ok
}

// ResetCooldown 清除冷静期（人工重试时调用）。
func (s *ConfigActivationService) ResetCooldown(instanceUUID, filePath string) {
	s.cooldownMu.Lock()
	defer s.cooldownMu.Unlock()
	delete(s.cooldowns, cooldownKey(instanceUUID, filePath))
}

// fallbackActivateMsg 空文本时回落到给定的兜底文案。
func fallbackActivateMsg(msg, fallback string) string {
	if strings.TrimSpace(msg) == "" {
		return fallback
	}
	return msg
}
