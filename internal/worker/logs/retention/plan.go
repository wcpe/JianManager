package retention

import (
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

// Plan 计算一次扫描的全部删除目标。纯函数（只依赖入参与 now），便于逐条断言。
//
// 目标的数量与来源数**不成正比**：只有写了来源覆盖的源才会单独成一条目标，
// 其余走「全实例 + 该级别」一条。这是有意的——按 (源 × 级别) 展开会产出
// 数百个删除任务，而删除接口每次都要扫一遍索引，代价与收益完全不成比例。
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

	var out []SweepTarget
	// 全局档：每个级别一条全实例目标。
	for _, key := range sortedLevels {
		ttl := n.ByLevel[key]
		if ttl <= 0 {
			continue
		}
		before := now.Add(-ttl)
		if before.Unix() <= 0 {
			continue
		}
		filter, err := buildFilter("", key)
		if err != nil {
			return nil, err
		}
		out = append(out, SweepTarget{Level: key, Filter: filter, Before: before, TTL: ttl})
	}

	// 来源覆盖档：只对写了覆盖的源单独成一条。
	//
	// 为什么要按 sourceIDs 过滤而不是「配置里有覆盖就生成」：配置里可能留着已经下线实例的
	// 覆盖项，逐条生成会产出一堆永远命中不到任何数据的删除任务（每次都要扫索引）。
	sortedSources := append([]string(nil), sourceIDs...)
	sort.Strings(sortedSources)
	seen := map[string]bool{}
	for _, sid := range sortedSources {
		if sid == "" || seen[sid] {
			continue
		}
		seen[sid] = true
		if !ValidSourceID(sid) {
			// 源标识非法就跳过这一条，但**不静默**：整份计划里少了谁必须能从错误里看出来。
			return out, fmt.Errorf("retention: 源标识 %q 含非法字符，已跳过其来源级覆盖", sid)
		}
		for _, key := range sortedLevels {
			ttl := p.EffectiveTTL(sid, key)
			if ttl <= 0 {
				continue
			}
			// 与全局档相同就不必单独下发：全局那条已经覆盖它。
			if n.ByLevel[key] == ttl {
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
	return out, nil
}

// DefaultScanLevels 返回扫描默认覆盖的级别键（全白名单）。
func DefaultScanLevels() []string { return KnownLevelKeys() }
