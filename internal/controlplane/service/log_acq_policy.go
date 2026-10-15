package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// nodeScopedSettingSuffix 是 Worker 级覆盖键的作用域后缀分隔符（FR-503）。
//
// 键形态：`<平台键>@node:<nodeUUID>`。例如平台级 `log.acq.realtime_tail` 的
// Worker 级覆盖写作 `log.acq.realtime_tail@node:20fd52ed-…`。
//
// 为什么用后缀而非独立表：platform_settings 已有完备的读写服务（白名单、值语义校验、
// 「可即时生效项写库后立即应用」分工）与既有 API。复用后本项只需扩白名单 + 一层节点维度
// 解析，无需建表与迁移。代价是键名略绕、且节点删除后需清理（见 spec §6）。
const nodeScopedSettingSuffix = "@node:"

// NodeScopedSettingKey 构造某节点的 Worker 级覆盖键（FR-503）。
//
// nodeUUID 为空时返回平台级键本身（调用方无需分支）。
func NodeScopedSettingKey(platformKey, nodeUUID string) string {
	if nodeUUID == "" {
		return platformKey
	}
	return platformKey + nodeScopedSettingSuffix + nodeUUID
}

// LogAcqPolicy 返回指定节点当前生效的日志采集配置（FR-503），供心跳下发。
//
// 合并语义（高 → 低）：**Worker 级覆盖 > 平台级 > 内置默认**。
// nodeUUID 为空表示只取平台级（供设置页展示平台默认）。
//
// 与 HealthScanPolicy 一样，本方法**不返回错误**：配置非法一律回退安全默认，
// 保证心跳下发永不因配置问题失败（那会导致节点被判离线）。
func (s *SettingsService) LogAcqPolicy(nodeUUID string) cpgrpc.LogAcqPolicySnapshot {
	return cpgrpc.LogAcqPolicySnapshot{
		RulesEnabled:    s.effectiveBoolForNode(SettingKeyLogAcqRulesEnabled, nodeUUID, true),
		RealtimeTail:    s.effectiveBoolForNode(SettingKeyLogAcqRealtimeTail, nodeUUID, true),
		GzImportEnabled: s.effectiveBoolForNode(SettingKeyLogAcqGzImport, nodeUUID, false),
		GzScanInterval:  parseDurationDefault(s.effectiveValueForNode(SettingKeyLogAcqGzScanInterval, nodeUUID), 0),
		PathOverrides:   parsePathOverrides(s.effectiveValueForNode(SettingKeyLogAcqPathOverrides, nodeUUID)),
	}
}

// effectiveValueForNode 按「Worker 级覆盖 > 平台级 > 默认」取值。
func (s *SettingsService) effectiveValueForNode(platformKey, nodeUUID string) string {
	if nodeUUID != "" {
		if v := s.rawOverride(NodeScopedSettingKey(platformKey, nodeUUID)); v != "" {
			return v
		}
	}
	return s.EffectiveValue(platformKey)
}

// effectiveBoolForNode 取布尔型生效值：仅显式 "true" 为真（与 HealthScanPolicy 同口径）。
func (s *SettingsService) effectiveBoolForNode(platformKey, nodeUUID string, fallback bool) bool {
	v := strings.TrimSpace(strings.ToLower(s.effectiveValueForNode(platformKey, nodeUUID)))
	switch v {
	case "true":
		return true
	case "false":
		return false
	default:
		return fallback
	}
}

// rawOverride 读取 DB 覆盖层的原始值；不存在返回空串。
//
// 与 EffectiveValue 的区别：后者会回退到基线默认值，而本函数只回答
// 「DB 里是否显式配置过」。Worker 级解析需要这个区分——否则平台默认会
// 被误当成「Worker 已覆盖」。
func (s *SettingsService) rawOverride(key string) string {
	overrides, err := s.loadOverrides()
	if err != nil {
		return ""
	}
	return overrides[key]
}

// parsePathOverrides 解析路径覆盖 JSON。
//
// 非法 JSON 一律回退空映射而非报错：配置错误不应让心跳下发失败
// （那会导致节点被判离线），代价只是覆盖不生效——可在设置页提示。
func parsePathOverrides(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	// 过滤空键/空值：它们没有语义，留着只会让下游多做无谓判断。
	for k, v := range m {
		if strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
			delete(m, k)
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// 编译期断言：SettingsService 满足 grpc 侧的日志采集策略解析接口（FR-503）。
var _ cpgrpc.LogAcqPolicyResolver = (*SettingsService)(nil)

// —— 设置视图与覆盖写入（API 层用，FR-503 §3.3C）——

// LogAcqSettingItem 是一项日志采集配置的三层取值，供前端对照展示与逐项恢复默认。
type LogAcqSettingItem struct {
	// Key 平台键（不带 `@node:` 后缀）。
	Key string `json:"key"`
	// Default 是 worker.yml 基线（内置默认）值。
	Default string `json:"default"`
	// Platform 是平台级生效值（平台 DB 覆盖 > 基线）。
	Platform string `json:"platform"`
	// Effective 是本视图作用域的生效值（节点视图 = Worker 覆盖 > 平台；平台视图 = Platform）。
	Effective string `json:"effective"`
	// Override 是本节点的覆盖值（平台视图恒为空）。
	Override string `json:"override"`
	// Overridden 表示本节点是否显式覆盖过该项。
	Overridden bool `json:"overridden"`
}

// LogAcqSettingsView 是日志采集配置视图。
type LogAcqSettingsView struct {
	// NodeUUID 非空表示某节点的对照视图（含覆盖列）；空表示平台默认视图。
	NodeUUID string `json:"nodeUuid,omitempty"`
	// NodeName 节点名（仅节点视图；供前端标题展示，查询失败时为空）。
	NodeName string              `json:"nodeName,omitempty"`
	Items    []LogAcqSettingItem `json:"items"`
}

// LogAcqPlatformKeys 返回日志采集的全部平台键，顺序即前端展示顺序。
func LogAcqPlatformKeys() []string {
	return []string{
		SettingKeyLogAcqRulesEnabled,
		SettingKeyLogAcqRealtimeTail,
		SettingKeyLogAcqGzImport,
		SettingKeyLogAcqGzScanInterval,
		SettingKeyLogAcqPathOverrides,
	}
}

// NodeLogAcqKeys 返回某节点全部日志采集覆盖键（平台键 + `@node:<uuid>` 后缀）。
func NodeLogAcqKeys(nodeUUID string) []string {
	keys := LogAcqPlatformKeys()
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, NodeScopedSettingKey(k, nodeUUID))
	}
	return out
}

// LogAcqSettings 返回平台默认视图（不含节点覆盖列）。
func (s *SettingsService) LogAcqSettings() (LogAcqSettingsView, error) {
	return s.logAcqSettingsView("")
}

// LogAcqSettingsForNode 返回某节点的对照视图（默认 / 平台 / 生效 / 覆盖）。
func (s *SettingsService) LogAcqSettingsForNode(nodeUUID string) (LogAcqSettingsView, error) {
	if nodeUUID == "" {
		return LogAcqSettingsView{}, fmt.Errorf("%w: 节点 UUID 为空", ErrSettingValueInvalid)
	}
	return s.logAcqSettingsView(nodeUUID)
}

// logAcqSettingsView 组装视图。nodeUUID 为空时只出平台列。
func (s *SettingsService) logAcqSettingsView(nodeUUID string) (LogAcqSettingsView, error) {
	overrides, err := s.loadOverrides()
	if err != nil {
		return LogAcqSettingsView{}, err
	}
	view := LogAcqSettingsView{NodeUUID: nodeUUID}
	items := make([]LogAcqSettingItem, 0, len(LogAcqPlatformKeys()))
	for _, key := range LogAcqPlatformKeys() {
		item := LogAcqSettingItem{Key: key, Default: s.defaultValue(key)}
		// 平台列：DB 覆盖优先，无覆盖显示基线（对 path_overrides/scan_interval 而言
		// 基线就是空串，与「未配置」同形，前端按空值渲染即正确）。
		if v, ok := overrides[key]; ok {
			item.Platform = v
		} else {
			item.Platform = item.Default
		}
		item.Effective = item.Platform
		if nodeUUID != "" {
			if v, ok := overrides[NodeScopedSettingKey(key, nodeUUID)]; ok {
				item.Override = v
				item.Overridden = true
				item.Effective = v
			}
		}
		items = append(items, item)
	}
	view.Items = items
	return view, nil
}

// deleteNodeScopedSettings 删除某节点的全部 Worker 级覆盖键（FR-503）。
//
// 键形态 `<平台键>@node:<uuid>`（见 NodeScopedSettingKey），故按后缀匹配删除——
// 后缀含完整 UUID，不会误伤其它节点的键（LIKE 通配符只出现在前缀侧的 `%`，
// UUID 本身只含十六进制与连字符，无需转义）。
func deleteNodeScopedSettings(tx *gorm.DB, nodeUUID string) error {
	if nodeUUID == "" {
		return nil
	}
	pattern := "%" + nodeScopedSettingSuffix + nodeUUID
	return tx.Where("key LIKE ?", pattern).Delete(&model.PlatformSetting{}).Error
}

// DeleteOverrides 删除一批覆盖键（用于「恢复默认」）；键不存在时静默成功（幂等）。
//
// 与 Update 同口径：删除后该键立即回到「基线默认」——节点级删除即恢复继承平台默认。
func (s *SettingsService) DeleteOverrides(keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	if err := s.db.Where("key IN ?", keys).Delete(&model.PlatformSetting{}).Error; err != nil {
		return fmt.Errorf("删除配置覆盖失败: %w", err)
	}
	return nil
}
