package retention

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// SweepTarget 是一次待执行的删除：某级别（可选限定来源）在 Before 之前的条目。
type SweepTarget struct {
	// Level 级别键（白名单内）。
	Level string
	// SourceID 为空表示**全实例**这一级别；非空表示只删该来源的这一级别。
	SourceID string
	// Filter 删除过滤器。由本包从级别白名单与已校验的源标识拼出，
	// **不是**来自配置的自由文本（见 doc.go 的说明）。
	Filter string
	// Before 截止时间：早于它的条目会被删除。
	Before time.Time
	// TTL 生成该目标的保留期（0 = 永久保留，不会出现在计划里）。
	TTL time.Duration
}

// String 返回可读描述（日志与观测面用）。
func (t SweepTarget) String() string {
	scope := "all"
	if t.SourceID != "" {
		scope = t.SourceID
	}
	return fmt.Sprintf("level=%s scope=%s before=%s ttl=%s filter=%q",
		t.Level, scope, t.Before.UTC().Format(time.RFC3339), humanTTL(t.TTL), t.Filter)
}

// buildFilter 由**级别白名单**与**已校验的源标识**拼出删除过滤器。
//
// 这是本包唯一生成过滤器的地方，也是删错东西的唯一入口，故两条硬约束：
//   - 级别必须命中白名单，否则报错（不接受调用方传来的任意字符串）；
//   - 源标识必须通过字符集白名单（ValidSourceID），否则报错。
//
// 之所以用 `level:DEBUG` 这种无引号形态：级别键全是 `[A-Z]`，LogsQL 不需要引号也不会
// 有歧义；而源标识含冒号（`inst:147`），**必须加引号**，否则会被解析成字段过滤。
func buildFilter(sourceID, level string) (string, error) {
	key := LevelKeyOf(level)
	if key == "" {
		return "", fmt.Errorf("retention: 级别 %q 不在白名单内（%s）", level, strings.Join(levelKeys, "/"))
	}
	if sourceID == "" {
		return "level:" + key, nil
	}
	if !ValidSourceID(sourceID) {
		return "", fmt.Errorf("retention: 源标识 %q 含非法字符，拒绝拼入删除过滤器", sourceID)
	}
	return fmt.Sprintf("level:%s AND log_source_id:%q", key, sourceID), nil
}

// hasExtendingOverride 报告该级别上是否存在**延长型**来源覆盖：
// 覆盖值 > 全局值（含显式写 0 = 永久保留，它比任何有限的全局值都长）。
//
// 判定**不要求**覆盖命中 sourceIDs：前缀通配（`inst:*`）可能命中尚未列举的来源，
// 只要配置声明了「延长」，该级别就必须按逐源口径下发（否则全局档会按更短的 TTL 删掉它）。
//
// 口径与 resolveOverride 逐字对齐（只认规范化后的级别键）：配置里写别名（如 `warning`）
// 时 resolveOverride 找不到它，这里也必须同样视为「未写覆盖」，否则会出现
// 「计划按延长展开、实际 EffectiveTTL 却回落到全局」的不一致。
func (p Policy) hasExtendingOverride(levelKey string) bool {
	global := p.Normalize().ByLevel[levelKey]
	for _, o := range p.Sources {
		v, ok := o.ByLevel[levelKey]
		if !ok {
			continue
		}
		if v == 0 || v > global {
			return true
		}
	}
	return false
}

// Plan 计算一次扫描的全部删除目标。纯函数（只依赖入参与 now），便于逐条断言。
//
// 目标的数量与来源数**不成正比**：只有写了「缩短型」来源覆盖的源才会单独成一条目标，
// 其余走「全实例 + 该级别」一条。这是有意的——按 (源 × 级别) 展开会产出
// 数百个删除任务，而删除接口每次都要扫一遍索引，代价与收益完全不成比例。
//
// # 延长型覆盖会让该级别的全局档退场（RET-1，2026-10-11 修复）
//
// VL 的删除任务是**并集**语义：各目标独立执行，没有「更具体者覆盖更笼统者」的规则。
// 因此全实例档一旦下发，它匹配到的每一条数据都会被按全局 TTL 删掉——来源级覆盖
// 无论写多长（哪怕写 0 = 永久保留）都救不回来。这正是「删掉配置明确要求保留的数据」。
//
// 修法：只要某级别存在延长型覆盖（hasExtendingOverride），该级别就**不再产出全实例档**，
// 改为对每个已知来源发出它自己的档（保留期取 EffectiveTTL；写 0 的来源一条档都不发＝不删）。
//
// 为什么选「逐源下发」而不是「给全局档拼 NOT log_source_id:"…"」：
//   - 排除项只能对**已知来源**逐条展开，而来源覆盖支持前缀通配（`inst:*`）。对
//     「配置里声明了、当前却不在 sourceIDs 里」的来源（例如刚下线的实例仍有数据在 VL），
//     排除项补不全 ⇒ 全局档照旧按短 TTL 删掉它 ⇒ 误删路径只是变窄、并未消失。
//   - 逐源下发让「未被列举的来源」落进「没有任何档匹配它」＝不删。方向是 fail-safe：
//     配置声明要留更久的数据绝不会被删，最坏后果只是「盘没省下来」——这正是 ADR-098
//     对「配置错误最坏后果」的设计意图。
//   - 两案都不引入自由文本：过滤器始终由 buildFilter 从级别白名单 + ValidSourceID 拼出。
//
// 代价如实登记：一个级别一旦有延长型覆盖，目标数 = 已知来源数（而不是 1 条）。
// 这只在运维**显式**配置了「某源留更久 / 永久保留」时发生；默认配置与「只缩短」的
// 配置仍然只产出 1 条全实例档，不会因为这条规则把目标数放大到数百条。
//
// 不会出现在计划里的三种情形（都是「不动手」而不是「删错」）：
//   - 保留期 <= 0：永久保留；
//   - 级别无法归类：按最保守处理（EffectiveTTL 返回 0）；
//   - Before 早于 Unix 纪元：说明保留期长到超过数据存在时间，无事可做。
func (p Policy) Plan(now time.Time, sourceIDs []string, levels []string) ([]SweepTarget, error) {
	if !p.Enabled {
		return nil, nil
	}
	n := p.Normalize()

	levelSet := map[string]bool{}
	for _, l := range levels {
		key := LevelKeyOf(l)
		if key == "" {
			// 采集面上会出现「无法归一级别的行」（堆栈延续行等），它们不参与按级别的保留。
			// 这不是错误：策略只对能归类的级别生效，无法归类的按 EffectiveTTL 的保守口径永久保留。
			continue
		}
		levelSet[key] = true
	}
	sortedLevels := make([]string, 0, len(levelSet))
	for k := range levelSet {
		sortedLevels = append(sortedLevels, k)
	}
	sort.Strings(sortedLevels)

	// 来源集归一：去空、去重、排序（顺序决定计划与过滤器的确定性），非法标识不静默。
	//
	// 为什么要按 sourceIDs 过滤而不是「配置里有覆盖就生成来源档」：配置里可能留着已经下线
	// 实例的覆盖项，逐条生成会产出一堆永远命中不到任何数据的删除任务（每次都要扫索引）。
	// 注意这只影响**来源档**；延长型覆盖对全局档的抑制不受此限制（见 hasExtendingOverride）。
	sortedSources := append([]string(nil), sourceIDs...)
	sort.Strings(sortedSources)
	seen := map[string]bool{}
	sources := make([]string, 0, len(sortedSources))
	// invalidErrs 聚合**全部**非法源标识（N-7，2026-10-12 修复）：此前用单个 error 逐次覆盖，
	// 同时配错多个源时只有最后一个出现在错误里，排障要一轮轮修。errors.Join 保留全部。
	var invalidErrs []error
	for _, sid := range sortedSources {
		if sid == "" || seen[sid] {
			continue
		}
		seen[sid] = true
		if !ValidSourceID(sid) {
			// 源标识非法就跳过这一条，但**不静默**：整份计划里少了谁必须能从错误里看出来。
			invalidErrs = append(invalidErrs,
				fmt.Errorf("retention: 源标识 %q 含非法字符，已跳过其来源级覆盖", sid))
			continue
		}
		sources = append(sources, sid)
	}
	invalidErr := errors.Join(invalidErrs...)

	var out []SweepTarget
	for _, key := range sortedLevels {
		global := n.ByLevel[key]
		if p.hasExtendingOverride(key) {
			// 延长型覆盖存在 ⇒ 该级别逐源下发：全实例档会按全局 TTL 删掉被声明保留更久的源。
			for _, sid := range sources {
				ttl := p.EffectiveTTL(sid, key)
				if ttl <= 0 {
					continue // 永久保留：不产出档 ⇒ 该源这一级一条都不会被删
				}
				before := now.Add(-ttl)
				if before.Unix() <= 0 {
					continue
				}
				filter, err := buildFilter(sid, key)
				if err != nil {
					return out, err
				}
				out = append(out, SweepTarget{Level: key, SourceID: sid, Filter: filter, Before: before, TTL: ttl})
			}
			continue
		}

		// 全局档：该级别没有任何延长型覆盖，按全局 TTL 一条全实例目标即可。
		if global > 0 {
			before := now.Add(-global)
			if before.Unix() > 0 {
				filter, err := buildFilter("", key)
				if err != nil {
					return out, err
				}
				out = append(out, SweepTarget{Level: key, Filter: filter, Before: before, TTL: global})
			}
		}
		// 仅缩短的来源覆盖：补一条更紧的来源档即可（并集 = 该源按更短的档删，正是配置的意图）；
		// 与全局档相同的来源不必单独下发。
		for _, sid := range sources {
			ttl := p.EffectiveTTL(sid, key)
			if ttl <= 0 || ttl >= global {
				continue
			}
			before := now.Add(-ttl)
			if before.Unix() <= 0 {
				continue
			}
			filter, err := buildFilter(sid, key)
			if err != nil {
				return out, err
			}
			out = append(out, SweepTarget{Level: key, SourceID: sid, Filter: filter, Before: before, TTL: ttl})
		}
	}
	return out, invalidErr
}

// DefaultScanLevels 返回扫描默认覆盖的级别键（全白名单）。
func DefaultScanLevels() []string { return KnownLevelKeys() }
