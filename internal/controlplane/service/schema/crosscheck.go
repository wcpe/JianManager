package schema

import (
	"strconv"
	"strings"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// ParsedConfig 单文件解析结果：路径 + 已解析字段列表。
type ParsedConfig struct {
	Path   string
	Fields []*workerpb.ConfigField
}

// fieldLookup 返回首个匹配 key 的字段值；找不到时返回空串。
func (p ParsedConfig) fieldLookup(key string) string {
	for _, f := range p.Fields {
		if f.Key == key {
			return f.Value
		}
	}
	return ""
}

// IsTruthy 判断布尔型字段是否启用。
func (p ParsedConfig) IsTruthy(key string) bool {
	switch strings.ToLower(strings.TrimSpace(p.fieldLookup(key))) {
	case "true", "yes", "1", "on":
		return true
	}
	return false
}

// CheckPortConflicts 跨实例文件检查 server-port / rcon.port / query.port 唯一性。
// 输入通常为同一节点上若干实例的同一文件；返回 warning 列表。
func CheckPortConflicts(cfgs []ParsedConfig) []*workerpb.ValidationIssue {
	issues := []*workerpb.ValidationIssue{}
	ports := map[string]string{} // port -> first path:key
	for _, c := range cfgs {
		for _, key := range []string{"server-port", "rcon.port", "query.port"} {
			v := c.fieldLookup(key)
			if v == "" {
				continue
			}
			if _, err := strconv.Atoi(v); err != nil {
				continue
			}
			tag := c.Path + "@" + key
			if other, ok := ports[v]; ok && other != tag {
				issues = append(issues, &workerpb.ValidationIssue{
					Level:   "warning",
					Message: "端口 " + v + " 重复：" + other + " 与 " + tag,
					Key:     key,
				})
			} else {
				ports[v] = tag
			}
		}
	}
	return issues
}

// CheckProxyConsistency 校验 online-mode 与代理转发配置是否自洽。
// 当 spigot.settings.bungeecord=true 或 paper proxies.bungeecord.enabled=true 时，
// online-mode 必须为 false，否则提示玩家可能因代理层校验被踢出。
func CheckProxyConsistency(cfgs []ParsedConfig) []*workerpb.ValidationIssue {
	issues := []*workerpb.ValidationIssue{}
	for _, c := range cfgs {
		if c.IsTruthy("settings.bungeecord") || c.IsTruthy("proxies.bungeecord.enabled") {
			for _, other := range cfgs {
				if other.IsTruthy("online-mode") {
					issues = append(issues, &workerpb.ValidationIssue{
						Level:   "warning",
						Message: other.Path + " 的 online-mode=true 与 " + c.Path + " 的代理转发配置冲突，应改为 false",
						Key:     "online-mode",
					})
				}
			}
		}
		if c.IsTruthy("proxies.velocity.enabled") {
			for _, other := range cfgs {
				if other.IsTruthy("online-mode") {
					issues = append(issues, &workerpb.ValidationIssue{
						Level:   "warning",
						Message: other.Path + " 的 online-mode=true 与 " + c.Path + " proxies.velocity.enabled=true 冲突",
						Key:     "online-mode",
					})
				}
			}
		}
	}
	return issues
}

// CheckForwardingSecret 校验 Velocity 的 forwarding-secret 与后端 Paper 配置一致。
func CheckForwardingSecret(cfgs []ParsedConfig) []*workerpb.ValidationIssue {
	issues := []*workerpb.ValidationIssue{}
	for _, proxy := range cfgs {
		if !proxy.IsTruthy("enabled") && proxy.fieldLookup("player-info-forwarding-mode") != "modern" {
			continue
		}
		secret := proxy.fieldLookup("forwarding-secret")
		if secret == "" {
			continue
		}
		for _, backend := range cfgs {
			if !backend.IsTruthy("proxies.velocity.enabled") {
				continue
			}
			if backend.fieldLookup("proxies.velocity.secret") != secret {
				issues = append(issues, &workerpb.ValidationIssue{
					Level:   "warning",
					Message: proxy.Path + " 的 forwarding-secret 与 " + backend.Path + " proxies.velocity.secret 不一致",
					Key:     "forwarding-secret",
				})
			}
		}
	}
	return issues
}

// CheckAll 汇总三项校验。
func CheckAll(cfgs []ParsedConfig) []*workerpb.ValidationIssue {
	out := []*workerpb.ValidationIssue{}
	out = append(out, CheckPortConflicts(cfgs)...)
	out = append(out, CheckProxyConsistency(cfgs)...)
	out = append(out, CheckForwardingSecret(cfgs)...)
	return out
}
