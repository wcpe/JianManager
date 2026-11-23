package vlrange

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

// selectorBeforeFactoring 是**未经分组重构**的直白渲染（用于与分组实现交叉验证）。
//
// **契约变更（2026-11-02 简化投递路径）**：不再渲染 `projection_generation` 谓词。
// 原先它渲染的是「重构前」的算法，而算法本身已经随简化一起改了；
// 这里保留的是「不分组、逐 scope 平铺」这个**独立形状**，等价性证据的意义不变。
func selectorBeforeFactoring(rng query.AuthoritativeRange, q query.RangeQuery) string {
	parts := make([]string, 0, 3)
	scoped := rng.Projection != nil && len(rng.Projection.SourceProjections) > 0
	var covered []string
	if scoped {
		for _, scope := range rng.Projection.SourceProjections {
			covered = append(covered, `(log_source_id:="`+escape(scope.LogSourceID)+`" AND source_generation:="`+
				escape(scope.SourceGeneration)+`" AND record_end:<=`+
				strconv.FormatUint(scope.ClosedVisibleSeq, 10)+`)`)
		}
	} else if rng.Projection != nil {
		for _, src := range rng.Projection.CoveredSourceGenerations {
			if src.LogSourceID != "" && src.SourceGeneration != "" {
				covered = append(covered, `(log_source_id:="`+escape(src.LogSourceID)+`" AND source_generation:="`+escape(src.SourceGeneration)+`")`)
			}
		}
	}
	switch {
	case len(covered) > 0:
		parts = append(parts, "("+strings.Join(covered, " OR ")+")")
	default:
		if rng.Key.StorageNamespace != "" {
			// 与 selectorQuery 同口径（2026-11-05 真机修复）：命名空间回退必须
			// 同时匹配裸命名空间与 `<ns>/...` 的子源，否则恒查不到。
			parts = append(parts, "("+namespaceSelector(rng.Key.StorageNamespace)+")")
		}
	}
	// 与 selectorQuery 同口径（2026-11-05）：只有**明确**水位（>0）才加上界。
	// 原先这里是 `!scoped || cvs > 0`，详见 selectorOracle 的注释与
	// scope_less_projection_regression_test.go 的真机证据。
	if q.ClosedVisibleSeq > 0 {
		parts = append(parts, "record_end:<="+strconv.FormatUint(q.ClosedVisibleSeq, 10))
	}
	base := "*"
	if len(parts) > 0 {
		base = strings.Join(parts, " AND ")
	}
	return base
}

type selectorCandidate struct {
	source, generation, projection string
	end                            uint64
}

// 结构化真值机不读取渲染文本，也不复用候选分组键；显式枚举来源、代次和水位关系。
// selectorOracle 是 selectorQuery 的**独立真值定义**（不复用被测实现）。
//
// **契约变更（2026-11-02 简化投递路径）**：真值不再考虑 `projection_generation`。
// 旧真值在 scoped 分支要求「行的代次 ∈ 该 scope 的白名单」、在非 scoped 分支要求
// 「行的代次 ∈ 全局白名单」—— 那套语义依赖「同一事件在 VL 里有 N 份副本需要筛掉」，
// 而简化后写入端不再重写，该前提消失（详见 selectorQuery 里的说明）。
//
// 保留的约束（一条都没放宽，仍逐项断言）：
//   - scoped：log_source_id + source_generation 精确匹配 + record_end<=scope 水位
//   - 非 scoped 有 CoveredSourceGenerations：按 log_source_id+source_generation 匹配
//   - 非 scoped 无覆盖：回退 namespace 相等
//   - range 级水位 record_end<=q.ClosedVisibleSeq
func selectorOracle(rng query.AuthoritativeRange, q query.RangeQuery, row selectorCandidate) bool {
	projection := rng.Projection
	scoped := projection != nil && len(projection.SourceProjections) > 0
	matched := false
	if scoped {
		for _, scope := range projection.SourceProjections {
			if row.source != scope.LogSourceID || row.generation != scope.SourceGeneration || row.end > scope.ClosedVisibleSeq {
				continue
			}
			matched = true
		}
	} else {
		hasCoverage := false
		if projection != nil {
			for _, source := range projection.CoveredSourceGenerations {
				if source.LogSourceID == "" || source.SourceGeneration == "" {
					continue
				}
				hasCoverage = true
				matched = matched || source.LogSourceID == row.source && source.SourceGeneration == row.generation
			}
		}
		if !hasCoverage {
			// 非 scoped 无覆盖：回退命名空间——**“属于该命名空间”**，即裸命名空间本身
			// 或其 `/` 子流（2026-11-05 真机：VL 行的 log_source_id 恒为
			// `<TargetID>/<stream>`，只比等值就恒不命中）。前缀必须以 `/` 收口，
			// 否则 `a` 会命中 `ab`（universe 里两条都有，靠它钉住）。
			ns := rng.Key.StorageNamespace
			matched = ns == "" || ns == row.source || strings.HasPrefix(row.source, ns+"/")
		}
	}
	// 契约变更（2026-11-05）：零水位不再按 `scoped` 分支。
	//
	// # 旧真值为什么在这里是错的
	//
	// 旧式：`matched && (scoped && cvs == 0 || row.end <= cvs)`。
	// 它把「零水位放行」绑定在 `scoped`（= 有 SourceProjections）上。这在那时成立：
	// 记录只由 `publishSnapshot` → `PublishProjection` 创建，而它总会填 SourceProjections。
	//
	// 简化投递路径后，Catalog 记录改由 `ingest.ensureCatalogPartition` 补建，它**刻意不填**
	// SourceProjections 与 ClosedVisibleSeq（一批数据只写一次、行上没有代次标签）。
	// 于是真实记录恒为 `scoped=false && cvs=0` ⇒ 旧真值要求 `row.end <= 0`
	// ⇒ **声明「这些行一条都不该被返回」**，与生产事实相反。
	//
	// 真机证据（inst:320/file，同一时间窗）：
	//   加   record_end:<=0     -> 0 条
	//   去掉 record_end 上界   -> 194 条
	// 且接口报 `complete=true` / `EXHAUSTED` / `quality=exact`——空结果被当成「完整无数据」。
	//
	// # 新真值没有放宽任何一条实质约束
	//
	// 上面的 `matched` 仍然逐条要求：
	//   - scoped：log_source_id + source_generation 精确匹配 + row.end <= scope 水位
	//   - 非 scoped 有 CoveredSourceGenerations：按 log_source_id + source_generation 匹配
	//   - 非 scoped 无覆盖：回退 namespace 相等
	// 这里只改 range 级水位那一层：`cvs == 0` 表示**没有封窗信息**（不知道），
	// 不能解释成「封锁一切」（`row.end <= 0`）。`cvs > 0` 时 `row.end <= cvs` 一字未变。
	// 这与 client.go 的 Search 行级归因谓词逐字同口径。
	return matched && (q.ClosedVisibleSeq == 0 || row.end <= q.ClosedVisibleSeq)
}

type selectorTruthNode struct {
	op, field, value string
	bound            uint64
	left, right      *selectorTruthNode
}

func (n *selectorTruthNode) matches(row selectorCandidate) bool {
	switch n.op {
	case "OR":
		return n.left.matches(row) || n.right.matches(row)
	case "AND":
		return n.left.matches(row) && n.right.matches(row)
	case "deny":
		return false
	case "*":
		return true
	case "<=":
		return row.end <= n.bound
	case "prefix":
		// 只有 log_source_id 有前缀语义（命名空间回退）；其它字段用了就是渲染写错了。
		if n.field != "log_source_id" {
			panic("前缀匹配只能用于 log_source_id")
		}
		return strings.HasPrefix(row.source, n.value)
	default:
		switch n.field {
		case "log_source_id":
			return row.source == n.value
		case "source_generation":
			return row.generation == n.value
		case "projection_generation":
			return row.projection == n.value
		}
		panic("未支持的测试谓词字段")
	}
}

// 此解析器只接受已有的精确相等、数字上界和布尔语法；新增 in/regex 等词法会直接失败。
// 按 AND 高于 OR 的优先级解析，不能把缺失括号造成的越界误判为等价。
type selectorTruthParser struct {
	t    *testing.T
	text string
}

func parseSelectorTruth(t *testing.T, text string) *selectorTruthNode {
	t.Helper()
	p := selectorTruthParser{t: t, text: text}
	n := p.expression("OR")
	require.Empty(t, p.text, "测试解析器必须消费完整谓词")
	return n
}

func (p *selectorTruthParser) take(prefix string) bool {
	p.text = strings.TrimLeft(p.text, " ")
	if !strings.HasPrefix(p.text, prefix) {
		return false
	}
	p.text = strings.TrimLeft(p.text[len(prefix):], " ")
	return true
}

func (p *selectorTruthParser) expression(op string) *selectorTruthNode {
	var left *selectorTruthNode
	if op == "OR" {
		left = p.expression("AND")
	} else {
		left = p.atom()
	}
	for p.take(op + " ") {
		var right *selectorTruthNode
		if op == "OR" {
			right = p.expression("AND")
		} else {
			right = p.atom()
		}
		left = &selectorTruthNode{op: op, left: left, right: right}
	}
	return left
}

func (p *selectorTruthParser) atom() *selectorTruthNode {
	if p.take("(") {
		n := p.expression("OR")
		require.True(p.t, p.take(")"), "缺少右括号: %s", p.text)
		return n
	}
	if p.take("*") {
		return &selectorTruthNode{op: "*"}
	}
	if p.take("record_end:<0") {
		return &selectorTruthNode{op: "deny"}
	}
	if p.take("record_end:<=") {
		i := 0
		for i < len(p.text) && p.text[i] >= '0' && p.text[i] <= '9' {
			i++
		}
		n, err := strconv.ParseUint(p.text[:i], 10, 64)
		require.NoError(p.t, err)
		p.text = strings.TrimLeft(p.text[i:], " ")
		return &selectorTruthNode{op: "<=", bound: n}
	}
	// 两种来源谓词写法都必须认：精确等值 `field:="…"` 与字面前缀 `field:"…"*`。
	// operator 只含 `:` 与可选的 `=`（**不吞开引号**）——后面从 1 开始扫描闭合引号，
	// 前提就是 p.text[0] 是开引号。
	idxEq := strings.Index(p.text, `:="`)
	idxPrefix := strings.Index(p.text, `:"`)
	i, operator := idxEq, `:=`
	// `:"` 是 `:="` 的前缀，两者可能命中同一位置 ⇒ 有 `:="` 时必须优先它，
	// 否则会把精确等值误读成前缀（Unquote 收到多出的 `=` 直接报 invalid syntax）。
	if i < 0 {
		i, operator = idxPrefix, `:`
	}
	require.Positive(p.t, i, "必须为已有来源匹配语法: %s", p.text)
	field := p.text[:i]
	p.text = p.text[i+len(operator):]
	end := 1
	for end < len(p.text) {
		if p.text[end] == '\\' {
			end += 2
			continue
		}
		if p.text[end] == '"' {
			break
		}
		end++
	}
	require.Less(p.t, end, len(p.text), "匹配引号必须闭合")
	value, err := strconv.Unquote(p.text[:end+1])
	require.NoError(p.t, err)
	p.text = strings.TrimLeft(p.text[end+1:], " ")
	// VL 的字面前缀语法：`field:"…"*`（`*` 紧跟闭合引号）。
	// 命名空间回退靠它匹配 `<ns>/...` 的子源（2026-11-05 真机修复），
	// 真值机必须认识它，否则“形状变了”这件事在等价性测试里看不出来。
	if strings.HasPrefix(p.text, "*") {
		require.Equal(p.t, ":", operator, "前缀语法只能写成 `field:\"…\"*`（无等号）")
		p.text = strings.TrimLeft(p.text[1:], " ")
		return &selectorTruthNode{op: "prefix", field: field, value: value}
	}
	require.Equal(p.t, ":=", operator, "精确相等必须写成 `field:=\"…\"`")
	return &selectorTruthNode{field: field, value: value}
}

func selectorTruthUniverse() []selectorCandidate {
	var rows []selectorCandidate
	for _, source := range []string{"a", "ab", `a/子"\`, "(a) OR (*)", "", "B", "a:b", `path\leaf`} {
		for _, generation := range []string{"", "g1", "g2", `g"\`} {
			for _, projection := range []string{"", "p1", "p2", `p"\`} {
				for _, end := range []uint64{0, 1, 2, 3, 4, 5, ^uint64(0)} {
					rows = append(rows, selectorCandidate{source, generation, projection, end})
				}
			}
		}
	}
	return rows
}

func selectorScope(source, generation string, end uint64, projections ...string) catalog.SourceProjection {
	return catalog.SourceProjection{SourceGenerationRef: catalog.SourceGenerationRef{LogSourceID: source, SourceGeneration: generation}, ClosedVisibleSeq: end, ProjectionGenerations: projections}
}

func TestSelectorCompleteTupleTruthEquivalence(t *testing.T) {
	rows := selectorTruthUniverse()
	rng := rand.New(rand.NewSource(7349502))
	cases := []query.AuthoritativeRange{
		{},
		{Key: catalog.PartitionKey{StorageNamespace: "a"}},
		{Projection: &catalog.PublishedProjection{ProjectionGeneration: "p1"}},
		{Projection: &catalog.PublishedProjection{CoveredSourceGenerations: []catalog.SourceGenerationRef{{LogSourceID: "a", SourceGeneration: "g1"}, {LogSourceID: "ab", SourceGeneration: "g2"}}, ProjectionGenerations: []string{"p1", "p2"}}},
		{Key: catalog.PartitionKey{StorageNamespace: "B"}, Projection: &catalog.PublishedProjection{CoveredSourceGenerations: []catalog.SourceGenerationRef{{LogSourceID: "a"}}}},
		{Projection: &catalog.PublishedProjection{SourceProjections: []catalog.SourceProjection{selectorScope("a", "g1", 0), selectorScope("B", "g2", 5)}}},
		{Projection: &catalog.PublishedProjection{SourceProjections: []catalog.SourceProjection{selectorScope("a", "g1", 1, "p1"), selectorScope("ab", "g2", 1, "p1"), selectorScope("B", "g1", 4, "p1"), selectorScope(`path\leaf`, "g1", 1, "p2")}}},
		{Projection: &catalog.PublishedProjection{SourceProjections: []catalog.SourceProjection{selectorScope("a", "g1", 2, "p1"), selectorScope("ab", "g1", 2, "p1"), selectorScope("a", "g2", 4, "p2"), selectorScope("B", "g1", 0, ""), selectorScope(`a/子"\`, `g"\`, 1, `p"\`)}}},
	}
	for trial := 0; trial < 64; trial++ {
		var scopes []catalog.SourceProjection
		for j := 0; j < 3+rng.Intn(8); j++ {
			row := rows[rng.Intn(len(rows))]
			var generations []string
			for k := 0; k < rng.Intn(4); k++ {
				generations = append(generations, rows[rng.Intn(len(rows))].projection)
			}
			scope := selectorScope(row.source, row.generation, row.end, generations...)
			scopes = append(scopes, scope)
			if j%3 == 0 {
				// 相同完整条件但不同来源、原样重复来源都进入相同候选全集。
				scopes = append(scopes, scope)
				scope.LogSourceID = rows[rng.Intn(len(rows))].source
				scopes = append(scopes, scope)
			}
		}
		cases = append(cases, query.AuthoritativeRange{Key: catalog.PartitionKey{StorageNamespace: "a"}, Projection: &catalog.PublishedProjection{SourceProjections: scopes}})
	}
	var comparisons int
	for index, r := range cases {
		for _, cutoff := range []uint64{0, 1, 4, ^uint64(0)} {
			q := query.RangeQuery{ClosedVisibleSeq: cutoff}
			before := parseSelectorTruth(t, selectorBeforeFactoring(r, q))
			text := selectorQuery(r, q)
			after := parseSelectorTruth(t, text)
			require.Equal(t, text, selectorQuery(r, q), "同一输入渲染必须稳定")
			for _, row := range rows {
				want := selectorOracle(r, q, row)
				if before.matches(row) != want || after.matches(row) != want {
					t.Fatalf("用例=%d cutoff=%d 候选=%+v 真值=%v 旧=%v 新=%v\n%s", index, cutoff, row, want, before.matches(row), after.matches(row), text)
				}
				comparisons++
			}
			if r.Projection != nil && len(r.Projection.SourceProjections) > 0 {
				reversed := r
				projection := *r.Projection
				projection.SourceProjections = catalog.CloneSourceProjections(r.Projection.SourceProjections)
				for i, j := 0, len(projection.SourceProjections)-1; i < j; i, j = i+1, j-1 {
					projection.SourceProjections[i], projection.SourceProjections[j] = projection.SourceProjections[j], projection.SourceProjections[i]
				}
				reversed.Projection = &projection
				permuted := parseSelectorTruth(t, selectorQuery(reversed, q))
				for _, row := range rows {
					require.Equal(t, after.matches(row), permuted.matches(row), "输入重排不能改变集合")
				}
			}
		}
	}
	t.Logf("种子=7349502 用例=%d 每例候选全集=%d cutoff=4 新旧与独立真值对照=%d", len(cases), len(rows), comparisons)
}

func TestSelectorTruthDetectsCartesianGenerationAndCutoff(t *testing.T) {
	rng := query.AuthoritativeRange{Projection: &catalog.PublishedProjection{SourceProjections: []catalog.SourceProjection{
		selectorScope("a", "g1", 1, "p1"), selectorScope("ab", "g2", 4, "p2"),
	}}}
	q := query.RangeQuery{}
	wrong := parseSelectorTruth(t, `(log_source_id:="a" OR log_source_id:="ab") AND (source_generation:="g1" OR source_generation:="g2") AND (projection_generation:="p1" OR projection_generation:="p2") AND record_end:<=4`)
	for _, row := range []selectorCandidate{{"a", "g2", "p2", 1}, {"a", "g1", "p1", 4}, {"ab", "g1", "p1", 1}} {
		require.True(t, wrong.matches(row), "故意错误的笛卡尔积必须实际放行负样本")
		require.False(t, selectorOracle(rng, q, row))
		require.False(t, parseSelectorTruth(t, selectorQuery(rng, q)).matches(row))
	}
}

func selectorSixtySources(twoCutoffs bool) query.AuthoritativeRange {
	rng := query.AuthoritativeRange{Key: catalog.PartitionKey{StorageNamespace: "node:1", UTCDay: "2026-10-22"}, Projection: &catalog.PublishedProjection{}}
	for i := 0; i < 60; i++ {
		cutoff := uint64(26052)
		if twoCutoffs && i >= 40 {
			cutoff = 27396
		}
		rng.Projection.SourceProjections = append(rng.Projection.SourceProjections, selectorScope(fmt.Sprintf("node:1/bench-%03d", i), "fixture-1", cutoff, "projection-1"))
	}
	return rng
}

func TestSelectorSixtySourceTruthAndByteMeasurement(t *testing.T) {
	for _, twoCutoffs := range []bool{false, true} {
		rng := selectorSixtySources(twoCutoffs)
		before := selectorBeforeFactoring(rng, query.RangeQuery{})
		after := selectorQuery(rng, query.RangeQuery{})
		oldTruth, newTruth := parseSelectorTruth(t, before), parseSelectorTruth(t, after)
		var checked int
		for _, scope := range rng.Projection.SourceProjections {
			for _, end := range []uint64{0, 26052, 26053, 27396, 27397} {
				row := selectorCandidate{scope.LogSourceID, scope.SourceGeneration, "projection-1", end}
				require.Equal(t, selectorOracle(rng, query.RangeQuery{}, row), oldTruth.matches(row))
				require.Equal(t, oldTruth.matches(row), newTruth.matches(row))
				checked++
			}
		}
		require.Less(t, len(after), len(before)/2, "保留全部60源，只有相同完整条件才可显著缩短")
		t.Logf("两种cutoff=%v 完整来源=%d 候选=%d 旧字节=%d 新字节=%d", twoCutoffs, len(rng.Projection.SourceProjections), checked, len(before), len(after))
	}
}
