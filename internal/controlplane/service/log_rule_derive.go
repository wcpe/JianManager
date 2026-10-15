package service

import (
	"strings"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 日志采集规则名（FR-502），取值与 Worker 侧 `ingest.RuleKey*` 常量一致。
const (
	logRuleKeyBukkit    = "bukkit"
	logRuleKeyBungee    = "bungee"
	logRuleKeyWaterfall = "waterfall"
	logRuleKeyVelocity  = "velocity"
)

// deriveLogRuleKey 由实例角色与核心 jar 名推导日志采集规则名（FR-502 §3.1）。
//
// 背景：Worker 侧原先把实例活动日志路径硬编码为 `WorkDir/logs/latest.log`，
// 而 BungeeCord 的日志在**工作目录根**的 `proxy.log.0`，因此永远采集不到。
// 本函数在 CP 侧把「核心类型」这一信息推导出来并经 proto 下发，
// 使 Worker 能按规则选择正确的物理路径。
//
// 判定顺序（先特化后泛化，避免误判）：
//  1. Waterfall —— 它是 BungeeCord 的分支，jar 名可能同时含两者字样，须优先识别
//  2. Velocity  —— 独立实现，日志文件名与 Bukkit 系不同
//  3. BungeeCord
//  4. 其余（含后端与未识别代理）一律 bukkit —— 与历史行为一致
//
// 只对 proxy 角色做特化判定：后端服即使 jar 名含关键字也不应改变规则。
func deriveLogRuleKey(role, coreJar string) string {
	if role != "proxy" {
		return logRuleKeyBukkit
	}
	name := strings.ToLower(coreJar)
	switch {
	case strings.Contains(name, "waterfall"):
		return logRuleKeyWaterfall
	case strings.Contains(name, "velocity"):
		return logRuleKeyVelocity
	case strings.Contains(name, "bungee"):
		return logRuleKeyBungee
	default:
		// 代理但核心未识别（如自造代理、旧数据）：回退 bukkit，
		// 与「不下发规则」的历史行为一致；运维可用实例级路径覆盖纠正。
		return logRuleKeyBukkit
	}
}

// logRuleKeyForInstance 读取实例的 launch_spec.coreJar 并推导规则名。
//
// launch_spec 解析失败或为空时不报错——日志规则是增强信息，
// 不应因它阻断实例注册；此时回退 bukkit（历史行为）。
func (s *InstanceService) logRuleKeyForInstance(instance *model.Instance) string {
	spec, err := parseLaunchSpec(instance.LaunchSpec)
	if err != nil || spec == nil {
		return deriveLogRuleKey(string(instance.Role), "")
	}
	return deriveLogRuleKey(string(instance.Role), spec.CoreJar)
}
