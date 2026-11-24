package service

import (
	"fmt"
	"sort"
	"strings"
)

// 探针配制键的三档分级（协议规格 §3.3，66 键权威表）。
//
// 协议权威定义：docs/specs/probe-managed-mode/spec.md。
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

// WireName 是档位在接口与存储里的**稳定标识**（forbidden / guarded / safe）。
//
// 与 String() 分工明确：String() 返回中文，给人看（日志、错误文案）；
// WireName() 返回英文枚举，给机器判断。
//
// ## 为什么必须分开
// 早期元数据接口直接把 String() 的中文塞进 JSON 的 `tier` 字段，前端据此判断
// `tier === 'forbidden'` 永远不成立——于是**禁止档键渲染出了可编辑控件**：
// 用户在界面上改了一个 bridge.* 键，下发时才被拒，而界面从没提示过它不能改。
// 中文一旦进入协议字段，任何调用方都得硬编码中文才能正确比较。
func (t ManagedConfigTier) WireName() string {
	switch t {
	case ConfigTierForbidden:
		return "forbidden"
	case ConfigTierGuarded:
		return "guarded"
	case ConfigTierSafe:
		return "safe"
	}
	return "forbidden" // 未知档位按最严格处理（fail-close）
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

// managedForbiddenRoots 是**整组结构性禁止**的根段。
//
// 与"逐键禁止"不同：逐键只堵登记过的那几个叶子（`bridge.enabled`/`bridge.url`/…），
// 而整组禁止堵的是整棵子树。探针侧 ConfigTier 的 `FORBIDDEN_ROOTS = setOf("bridge")`
// 就是这个语义——理由是 `bridge` 段下有 line/chunk/subscribe/health/reload 等大量子键，
// 逐键登记必然漏，而漏掉的那个键同样能改监听面与凭证行为。
//
// CP 侧早期只做了逐键禁止、缺了这一段，于是 `bridge.line.max-inbound-bytes` 这类
// 未登记的深层子键会被判成"可远程写"——探针仍会拒（它有自己的 FORBIDDEN_ROOTS），
// 但 CP 的预检就此失效，用户与探针各说各话。
var managedForbiddenRoots = map[string]struct{}{
	"bridge": {},
}

// IsForbiddenConfigPath 判定某路径是否属于禁止远程面。
//
// 三条判定，任一命中即禁止：
//  1. 整串命中禁止根段（整组结构性禁止，见 managedForbiddenRoots）；
//  2. 逐键命中禁止档；
//  3. 沿点分前缀逐段回溯，任一祖先命中上面两条。
//
// 第 3 条的存在理由：`alert.channels.webhook.url` 这类深层路径若其祖先已定档，
// 就该按祖先算。与探针 ConfigTier.isForbiddenPath 同语义。
//
// ## 为什么不是手写索引循环
// 曾用 `for idx := strings.IndexByte(key, '.'); idx > 0; idx = strings.IndexByte(key[idx+1:], '.') + idx + 1`
// 这种写法，它在**三段以上**的键上会死循环：当剩余串里已无 `.` 时 `IndexByte` 返回 -1，
// 而 `-1 + idx + 1 == idx`，索引原地踏步。两段键（`bridge.enabled`）恰好能正常退出，
// 所以这个缺陷在只测两段键的用例里完全不可见——真机上表现为下发含
// `alert.channels.webhook.url` 的配置时 CP 线程卡死。
//
// 改用 Split 逐段拼接：长度严格递减，不可能不推进；且"前缀"的定义一眼可见。
func IsForbiddenConfigPath(key string) bool {
	if isForbiddenSegment(key) {
		return true
	}
	segments := strings.Split(key, ".")
	if len(segments) < 2 {
		return false
	}
	// 首段必须单独判：整组禁止的根段（`bridge`）只出现在这一段。
	// 早期版本把首段直接当作初始 prefix 进入循环，于是 `bridge` 自己从未被检查过——
	// `bridge.line.max-inbound-bytes`（三段）回溯到 `bridge.line` 就结束了，
	// 而 `bridge.line` 没登记过档位，整条判定就此漏掉。
	prefix := segments[0]
	if isForbiddenSegment(prefix) {
		return true
	}
	// 继续拼到倒数第二段：整串已在函数开头判过，再查一次没有意义。
	for _, seg := range segments[1 : len(segments)-1] {
		prefix += "." + seg
		if isForbiddenSegment(prefix) {
			return true
		}
	}
	return false
}

// isForbiddenSegment 判定单段（整串或某个前缀）是否属于禁止面。
func isForbiddenSegment(segment string) bool {
	if _, ok := managedForbiddenRoots[segment]; ok {
		return true
	}
	tier, known := ManagedConfigTierOf(segment)
	return known && tier == ConfigTierForbidden
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
