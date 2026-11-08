package service

import (
	"fmt"
	"sort"
	"strings"
)

// 探针配制键的三档分级（协议规格 §7.1，66 键权威表）。
//
// ## 为什么 CP 侧必须自己再校一遍
// 探针会在收到 config_push 后自行校档并拒绝整包，但等到那时已经多走一个往返；
// 更重要的是**错误文案在探针那边是笼统的"整包拒绝"**，运维看不到到底是哪个键越界。
// CP 先校一遍能在下发前就把具体的键名报出来——这是一种止损而不是重复实现：
// 规则真源仍是探针的 ConfigTier，本表是它的镜像，二者偏离时以探针为准
// （探针拒绝是最终事实，CP 的校验只是提前提醒）。

// ManagedConfigTier 是配制键的远程可写档位。
type ManagedConfigTier int

const (
	// ConfigTierForbidden 禁止远程写入：开启监听面或承载凭证的键。
	// 出现在下发载荷里就整包拒绝（不是跳过该键）——否则运维会以为"改成功了"。
	ConfigTierForbidden ManagedConfigTier = iota
	// ConfigTierGuarded 危险档：影响数据量/资源/观测覆盖/隐式边界，需逐键校验物理边界。
	ConfigTierGuarded
	// ConfigTierSafe 安全档：仅展示或日志级别，无副作用。
	ConfigTierSafe
)

// String 返回档位的中文名（错误文案与前端展示用）。
func (t ManagedConfigTier) String() string {
	switch t {
	case ConfigTierForbidden:
		return "禁止"
	case ConfigTierGuarded:
		return "危险"
	case ConfigTierSafe:
		return "安全"
	}
	return "未知"
}

// managedConfigTiers 是探针 ConfigTier.TIER_BY_KEY 的逐条镜像（66 键）。
//
// 顺序与探针侧定档表一致，便于两边对账。新增键必须在两边同时定档：
// 探针侧未收录的键按 FORBIDDEN 处理（fail-close），CP 侧同样——**不收录就不下发**。
var managedConfigTiers = map[string]ManagedConfigTier{
	// 由 ServerProbe ConfigTier.kt 的 TIER_BY_KEY 逐行导出，不要手改：
	"agent-stack-max-samples":                ConfigTierGuarded,
	"aggregation.window":                     ConfigTierGuarded,
	"alert.channels.in-game":                 ConfigTierGuarded,
	"alert.channels.log":                     ConfigTierGuarded,
	"alert.channels.webhook.enabled":         ConfigTierGuarded,
	"alert.channels.webhook.timeout-ms":      ConfigTierGuarded,
	"alert.channels.webhook.url":             ConfigTierGuarded,
	"alert.enabled":                          ConfigTierGuarded,
	"alert.rules":                            ConfigTierGuarded,
	"bridge.enabled":                         ConfigTierForbidden,
	"bridge.instance":                        ConfigTierForbidden,
	"bridge.token":                           ConfigTierForbidden,
	"bridge.url":                             ConfigTierForbidden,
	"collect-period-ticks":                   ConfigTierGuarded,
	"cpu.enabled":                            ConfigTierGuarded,
	"cpu.sample-period-ticks":                ConfigTierGuarded,
	"cpu.window-rounds":                      ConfigTierGuarded,
	"debug":                                  ConfigTierSafe,
	"folia.observed-region-expire-seconds":   ConfigTierGuarded,
	"history-capacity":                       ConfigTierGuarded,
	"history-file.archive-days":              ConfigTierGuarded,
	"history-file.enabled":                   ConfigTierGuarded,
	"history-file.max-total-mb":              ConfigTierGuarded,
	"history-file.retention-days":            ConfigTierGuarded,
	"history-retention":                      ConfigTierGuarded,
	"http-monitor.drain-period-ticks":        ConfigTierGuarded,
	"http-monitor.enabled":                   ConfigTierGuarded,
	"http-monitor.file-archive-days":         ConfigTierGuarded,
	"http-monitor.file-enabled":              ConfigTierGuarded,
	"http-monitor.file-retention-days":       ConfigTierGuarded,
	"http-monitor.log-headers":               ConfigTierGuarded,
	"http-monitor.log-to-console":            ConfigTierGuarded,
	"http-monitor.recent-capacity":           ConfigTierGuarded,
	"incision.enabled":                       ConfigTierGuarded,
	"mcp.artifact-max-gib":                   ConfigTierGuarded,
	"mcp.artifact-retention-hours":           ConfigTierGuarded,
	"mcp.audit-max-file-mb":                  ConfigTierGuarded,
	"mcp.audit-retention-days":               ConfigTierGuarded,
	"mcp.enabled":                            ConfigTierForbidden,
	"mcp.host":                               ConfigTierForbidden,
	"mcp.port":                               ConfigTierForbidden,
	"mcp.secret":                             ConfigTierForbidden,
	"mcp.task-max-concurrent":                ConfigTierGuarded,
	"mcp.task-output-mebibytes":              ConfigTierGuarded,
	"mcp.task-retention-minutes":             ConfigTierGuarded,
	"mcp.task-timeout-minutes":               ConfigTierGuarded,
	"metrics.allowed-ips":                    ConfigTierForbidden,
	"metrics.enabled":                        ConfigTierForbidden,
	"metrics.host":                           ConfigTierForbidden,
	"metrics.port":                           ConfigTierForbidden,
	"metrics.token":                          ConfigTierForbidden,
	"network-forensics.enabled":              ConfigTierGuarded,
	"network-forensics.max-payload-bytes":    ConfigTierGuarded,
	"network-forensics.max-total-gib":        ConfigTierGuarded,
	"network-forensics.payload-channels":     ConfigTierGuarded,
	"network-forensics.payload-packet-types": ConfigTierGuarded,
	"network-forensics.retention-days":       ConfigTierGuarded,
	"server-name":                            ConfigTierSafe,
	"startup-top-n":                          ConfigTierSafe,
	"web.allowed-ips":                        ConfigTierForbidden,
	"web.enabled":                            ConfigTierForbidden,
	"web.host":                               ConfigTierForbidden,
	"web.port":                               ConfigTierForbidden,
	"web.token":                              ConfigTierForbidden,
	"world.entity-types":                     ConfigTierGuarded,
	"world.sample-period-ticks":              ConfigTierGuarded,
}

// managedReservedSafeKeys 是不在 66 键权威表内、但已接入配置面的 SAFE 档键。
//
// 探针侧把它们单列在 RESERVED_SAFE_KEYS（不计入 66 键口径），校验仍按 SAFE 走。
var managedReservedSafeKeys = map[string]struct{}{
	"config-state-interval-ms": {},
}

// managedReservedGuardedKeys 同理，按 GUARDED 校验。
var managedReservedGuardedKeys = map[string]struct{}{
	"metrics-push-interval-ms": {},
}

// ManagedConfigTierOf 查一个配置键的档位。
//
// 第二返回值 ok=false 表示**未收录**。调用方必须把它当禁止处理（fail-close）：
// 未收录意味着定档表没跟上，此时默认放行等于给新键开一个无人审过的远程写口。
func ManagedConfigTierOf(key string) (ManagedConfigTier, bool) {
	if tier, ok := managedConfigTiers[key]; ok {
		return tier, true
	}
	if _, ok := managedReservedSafeKeys[key]; ok {
		return ConfigTierSafe, true
	}
	if _, ok := managedReservedGuardedKeys[key]; ok {
		return ConfigTierGuarded, true
	}
	return ConfigTierForbidden, false
}

// IsForbiddenConfigPath 判定某路径是否属于禁止远程面。
//
// 逐键命中之外，还要沿点分前缀逐段回溯：`alert.channels.webhook.url` 这类深层路径
// 若其祖先已被定档，就按祖先算。与探针 ConfigTier.isForbiddenPath 同语义。
func IsForbiddenConfigPath(key string) bool {
	if tier, ok := ManagedConfigTierOf(key); ok && tier == ConfigTierForbidden {
		return true
	}
	for idx := strings.IndexByte(key, '.'); idx > 0; idx = strings.IndexByte(key[idx+1:], '.') + idx + 1 {
		prefix := key[:idx]
		if tier, ok := ManagedConfigTierOf(prefix); ok && tier == ConfigTierForbidden {
			return true
		}
		if idx+1 >= len(key) {
			break
		}
	}
	return false
}

// ManagedConfigPushValidation 是一次下发前的校验结果。
//
// Errors 非空即整包不可下发。这里刻意**不返回部分通过**：
// 探针侧就是整包拒绝语义，CP 侧若只报"哪些键被跳过了"，
// 运维会误以为剩下的键已经生效。
type ManagedConfigPushValidation struct {
	// Errors 是逐键的拒绝原因（已排序，便于稳定输出与断言）。
	Errors []string
}

// OK 报告校验是否通过。
func (v ManagedConfigPushValidation) OK() bool { return len(v.Errors) == 0 }

// Error 把校验结果拼成一条可读的错误（含全部被拒键）。
func (v ManagedConfigPushValidation) Error() string {
	if v.OK() {
		return ""
	}
	return "探针配置下发被拒：" + strings.Join(v.Errors, "；")
}

// ValidateConfigPush 在下发前校验一次 config_push 载荷。
//
// 检查项：
//  1. **禁档键一律拒绝**（含沿点分前缀回溯），而不是默默跳过；
//  2. **未收录键拒绝**（fail-close）；
//  3. removals 同样不得指向禁档路径——删掉一个禁档键与写入它同属越界。
func ValidateConfigPush(payload ManagedConfigPush) ManagedConfigPushValidation {
	var errs []string
	for key := range payload.Values {
		if IsForbiddenConfigPath(key) {
			errs = append(errs, fmt.Sprintf("键 %q 属于禁止远程写入的监听面/凭证（整包拒绝）", key))
			continue
		}
		if _, ok := ManagedConfigTierOf(key); !ok {
			errs = append(errs, fmt.Sprintf("键 %q 未登记档位，按禁止处理（需先在两端的定档表里收录）", key))
		}
	}
	for _, key := range payload.Removals {
		if IsForbiddenConfigPath(key) {
			errs = append(errs, fmt.Sprintf("removals 中的 %q 属于禁止远程写入的路径", key))
		}
	}
	sort.Strings(errs)
	return ManagedConfigPushValidation{Errors: errs}
}
