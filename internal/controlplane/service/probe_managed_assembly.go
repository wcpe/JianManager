package service

import (
	"fmt"
	"log/slog"
)

// 托管模式（ServerProbe PR#64）的 CP 侧装配：把“身份文件 + 托管版 config.yml”一起下发。
//
// ## 为什么要成对下发，不能只发一半
// 探针的两个开关是独立的，但托管模式下它们必须同时到位：
//   - `agent-identity.json` 存在 → 探针进托管模式 → **metrics/web/mcp 三个本地监听面全部关断**；
//   - 但 config.yml 里 `metrics.enabled: true` 还在 → 探针只是“不启动监听”，配置语义仍自相矛盾。
// 更关键的是：托管模式下 `/metrics` 没了，而 Worker 的心跳采集默认会去拓它——
// 拓不到就只能靠 SLP 直探，TPS/MSPT/堆内存这些只有探针能给的数据全丢。
// 所以托管实例必须走指标推送（metric_snapshot），而那要求探针真的进入托管模式并声明能力位。
//
// ## 失败一律不阻断建服
// 托管是“增强”不是“前提”：身份签发失败时退回独立模式（虽然监控退化但服务照常），
// 比因为一个监控特性而拒绝建服合理得多。

// buildManagedIdentity 为一个实例生成托管身份文件内容。
//
// 返回空串表示“本次不下发身份”（未启用 / 签发失败）。空串会被 Worker 当作“不碰现有身份”，
// 因此重部署不会把一台已在托管的探针退回独立模式。
//
// @param instanceUUID 实例 UUID（同时是握手 query 的 instance= 与上行帧的 instance）。
// @param wsPort 该节点 Worker 的 WS 端口（探针反向连入 ws://127.0.0.1:<wsPort>/ws/plugin-bridge）。
func (p *ProvisionService) buildManagedIdentity(instanceUUID string, wsPort int) string {
	if p.bridge == nil {
		// 没有插件桥就无法提供 credential（identity 的必填字段）。
		// 不发身份 → 探针保持独立模式，与改造前一致。
		return ""
	}
	token, err := p.bridge.IssueToken(instanceUUID)
	if err != nil {
		slog.Warn("签发托管身份凭据失败（探针将保持独立模式，不阻断建服）",
			"instance", instanceUUID, "err", err)
		return ""
	}
	txt, err := defaultManagedMode.BuildIdentityFile(
		pluginBridgeWSURL(wsPort), instanceUUID, token)
	if err != nil {
		slog.Warn("生成托管身份文件失败（探针将保持独立模式，不阻断建服）",
			"instance", instanceUUID, "err", err)
		return ""
	}
	return txt
}

// buildManagedProbeConfig 生成托管模式下的探针 config.yml。
//
// ## 与独立模式版的差异（都不是风格问题）
//  1. **不写 bridge 段**：托管模式下连接目标来自身份文件，`bridge.*` 会被探针忽略；
//     写进去只会让排障时误以为还是独立模式（且迁移逻辑会认为存在残留）。
//  2. **metrics.enabled 仍写 true**：这是历史行为，但托管模式下探针会**强制关断**监听面，
//     不看这个值。保留 true 是为了让“以后退回独立模式”时立即恢复监控，不必再改文件。
//     （探针侧零监听是按运行模式判定的，不是按配置值——见 managed-mode.md §3.9）
func buildManagedProbeConfig(probePort int) string {
	return buildServerProbeConfig(probePort, "")
}

// managedModeService 是托管身份生成的单例（无状态，纯函数式）。
var defaultManagedMode = NewManagedModeService()

// describeManagedMode 给日志用的一行描述。
func describeManagedMode(instanceUUID, identity string) string {
	if identity == "" {
		return "独立模式"
	}
	return fmt.Sprintf("托管模式(instance=%s)", instanceUUID)
}
