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

// deriveLogRuleKey 由实例角色、核心类型与核心 jar 名推导日志采集规则名（FR-502 §3.1）。
//
// 背景：Worker 侧原先把实例活动日志路径硬编码为 `WorkDir/logs/latest.log`，
// 而 BungeeCord 的日志在**工作目录根**的 `proxy.log.0`，因此永远采集不到。
// 本函数在 CP 侧把「核心类型」这一信息推导出来并经 proto 下发，
// 使 Worker 能按规则选择正确的物理路径。
//
// 判定顺序：
//  1. 先看 coreKind（一键搭建填入的核心类型）——这是**唯一不会丢信息**的来源：
//     一键搭建把核心统一落盘为 `server.jar`（见 provisionCoreJar），jar 名不含
//     bungee/waterfall/velocity 任何字样，只看 jar 名会让平台自己搭建/重建的代理
//     恒得 bukkit，代理日志再次采不到（即本项要修的生产缺陷静默复现）。
//  2. coreKind 缺失（历史数据、导入的服务器）时回退按 jar 名字面量匹配：
//     Waterfall 是 BungeeCord 的分支，jar 名可能同时含两者字样，须优先识别；
//     其次 Velocity（独立实现，日志文件名与 Bukkit 系不同）；再次 BungeeCord。
//  3. 都识别不出时一律 bukkit —— 与历史行为一致。
//
// 只对 proxy 角色做特化判定：后端服即使核心类型/jar 名含关键字也不应改变规则。
func deriveLogRuleKey(role, coreKind, coreJar string) string {
	if role != "proxy" {
		return logRuleKeyBukkit
	}
	if key := logRuleKeyFromCoreKind(coreKind); key != "" {
		return key
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

// logRuleKeyFromCoreKind 把核心类型映射为日志规则名；无法识别时返回空串，
// 由调用方回退按 jar 名匹配（避免把未知类型静默当成 bukkit）。
//
// 取值与 CoreInfo.Type 一致（paper/spongevanilla/spongeforge/velocity/waterfall/bungeecord）。
// 非代理类核心统一走 bukkit：它们的活动日志都在 `logs/latest.log`。
func logRuleKeyFromCoreKind(coreKind string) string {
	switch project(coreKind) {
	case "waterfall":
		return logRuleKeyWaterfall
	case "velocity":
		return logRuleKeyVelocity
	// bungeecord 是核心类型名，bungee 是写进 launch_spec 的简写；两者都接受。
	case "bungeecord", "bungee":
		return logRuleKeyBungee
	case "paper", "spigot", "bukkit", "spongevanilla", "spongeforge", "forge", "fabric":
		return logRuleKeyBukkit
	default:
		return ""
	}
}

// logRuleKeyForInstance 读取实例的 launch_spec（核心类型 + 核心 jar 名）并推导规则名。
//
// launch_spec 解析失败或为空时不报错——日志规则是增强信息，
// 不应因它阻断实例注册；此时回退 bukkit（历史行为）。
func (s *InstanceService) logRuleKeyForInstance(instance *model.Instance) string {
	spec, err := parseLaunchSpec(instance.LaunchSpec)
	if err != nil || spec == nil {
		return deriveLogRuleKey(string(instance.Role), "", "")
	}
	return deriveLogRuleKey(string(instance.Role), spec.CoreKind, spec.CoreJar)
}
