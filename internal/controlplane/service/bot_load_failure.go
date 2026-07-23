package service

import "strings"

// BotLoadFailureCategory 固定五类失败分类。
type BotLoadFailureCategory string

const (
	BotLoadFailureTarget   BotLoadFailureCategory = "target"
	BotLoadFailureExecutor BotLoadFailureCategory = "executor"
	BotLoadFailureNetwork  BotLoadFailureCategory = "network"
	BotLoadFailureScenario BotLoadFailureCategory = "scenario"
	BotLoadFailureInternal BotLoadFailureCategory = "internal"
)

// AllBotLoadFailureCategories 固定顺序，供 CSV/报告使用。
var AllBotLoadFailureCategories = []BotLoadFailureCategory{
	BotLoadFailureTarget,
	BotLoadFailureExecutor,
	BotLoadFailureNetwork,
	BotLoadFailureScenario,
	BotLoadFailureInternal,
}

// ClassifyBotLoadError 将错误码映射到失败分类；未知归 internal。
// 历史 probe 归一为 scenario，调用方可从第二个返回值得知 legacyCategory。
func ClassifyBotLoadError(errorCode string) (category BotLoadFailureCategory, legacyCategory string) {
	code := strings.ToUpper(strings.TrimSpace(errorCode))
	if code == "" {
		return BotLoadFailureInternal, ""
	}
	// 历史 probe 分类归一。
	if code == "PROBE" || strings.HasPrefix(code, "PROBE_") || code == "BOT_LOAD_PROBE_REQUIRED" {
		return BotLoadFailureScenario, "probe"
	}
	switch {
	case strings.Contains(code, "TARGET") || strings.Contains(code, "INSTANCE_CRASH") || strings.Contains(code, "SERVER_CRASH"):
		return BotLoadFailureTarget, ""
	case strings.Contains(code, "WORKER") || strings.Contains(code, "EXECUTOR") ||
		strings.Contains(code, "BOT_WORKER") || strings.Contains(code, "EVENT_LOOP") ||
		strings.Contains(code, "RSS") || strings.Contains(code, "ADMISSION") ||
		code == "NODE_OFFLINE" || code == "CAPACITY_RPC_FAILED" || code == "CAPACITY_RPC_TIMEOUT" ||
		code == "FLEET_FEATURE_MISSING" || code == "LEGACY_WORKER" || code == "BOT_WORKER_NOT_READY":
		return BotLoadFailureExecutor, ""
	case strings.Contains(code, "ECONN") || strings.Contains(code, "TIMEOUT") && strings.Contains(code, "CONNECT") ||
		strings.Contains(code, "KICKED") || strings.Contains(code, "NETWORK") ||
		strings.Contains(code, "CONNECTION") || code == "CONNECT_TIMEOUT" || code == "ECONNREFUSED":
		return BotLoadFailureNetwork, ""
	case strings.Contains(code, "COMMAND") || strings.Contains(code, "SCHEDULE") ||
		strings.Contains(code, "BARRIER") || strings.Contains(code, "SCENARIO") ||
		strings.Contains(code, "ACTION") || strings.Contains(code, "STEP"):
		return BotLoadFailureScenario, ""
	default:
		return BotLoadFailureInternal, ""
	}
}

// EmptyFailureSummary 返回五类计数均为 0 的 map。
func EmptyFailureSummary() map[string]int {
	out := make(map[string]int, len(AllBotLoadFailureCategories))
	for _, c := range AllBotLoadFailureCategories {
		out[string(c)] = 0
	}
	return out
}
