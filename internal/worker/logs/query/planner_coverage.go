package query

import (
	"sync/atomic"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// coverEmptyAuthorizedTargets 为「查询窗内没有任何分区的**授权目标**」补一条**空成功覆盖**。
//
// 症状与口径：CP 的授权目标是命名空间粒度，而覆盖与 range 都按分区构造 ⇒ 窗口内无数据的源在
// 两边都不出现 ⇒ CP 判 missing_coverage ⇒ 一个静默源（最后分区早于窗口，如 inst:153）把整个平台
// 判成未就绪。这与 #60 在 CP 侧「无数据 ≠ 故障」同口径：按**空结果**计入成功。
//
// 判据（缺一不可）：
//  1. 视图内没有任何条目承载该授权目标（判据与 remapCoverageTargets/CP 侧一致：条目 ID 等于
//     授权目标，或条目是它的分区子项）——已有覆盖就不得重复计入；
//  2. 窗口**不早于**该命名空间在 Catalog 里最早的分区（
//     `namespaceWindowPrecedesEarliestDay` 为假）——窗口横跨历史且该源有的天无记录时我们
//     **无法区分「那天确实无数据」与「那天的记录已不在本地」**，不得粉饰成空；
//     反之「窗口晚于该源最后数据」（静默源）仍按空成功计入，那是 #60 的既有裁定；
//  3. Catalog 里确实存在同命名空间、且 `OwnerForQuery` 给得出权威的记录；
//  4. **不**把它标为可查（不进 QueryableTargets）——否则会为它追加一个指向不存在分区的 range，
//     反而制造 range-unavailable。
//
// 找不到可查记录的授权目标**保持未就绪**（不发明空成功）：worker 侧无法区分「该源在本 Worker
// 上没有记录」与「窗口内无数据」，宁可让 CP 判 missing_coverage，也不把「不知道」粉饰成「空」。
//
// 只在**创建路径**调用（视图尚未登记 ⇒ 无并发读者），故结果一律写回视图：写回后
// planRangesForView 能原样复用同一条空成功覆盖；**复用路径绝不再调用本函数**（F1：
// 那里补空会把「有数据未读」粉饰成「空」，见 planRangesForView 的论证）。
func (p *Planner) coverEmptyAuthorizedTargets(req PlanRequest, view *QueryView, res *PlanResult) {
	if len(req.TargetIDs) == 0 {
		// 未指定授权目标 = 本 Worker 全部 Catalog 分区：没有分区就没有目标，不发明覆盖。
		return
	}
	// 一次 O(条数) 建立「已被覆盖」集合：条目 ID 直接命中，或按 parseTargetKey 归到命名空间/天
	// （与 matchTarget 的三条命中规则等价）。此前写法对每个目标整份遍历 p.cat.Keys() ⇒ O(T×K)
	// 且每次整份拷贝，而触发条件本身恒假（迭代的是必然已被覆盖的 view.TargetIDs）。
	covered := make(map[string]bool, len(view.TargetCoverage)*2)
	for id := range view.TargetCoverage {
		covered[id] = true
		if key, ok := parseTargetKey(id); ok {
			covered[key.StorageNamespace] = true
			covered[key.UTCDay] = true
		}
	}
	// Catalog 键只取一次（惰性：没有待补目标时零成本）。
	var allKeys []catalog.PartitionKey
	for _, auth := range req.TargetIDs {
		if auth == "" || covered[auth] {
			continue
		}
		if allKeys == nil {
			allKeys = p.cat.Keys()
		}
		// 2026-11-05 生产真机（日志页「这天没日志」但 VL 里确有数据）：
		// 原实现只问「该命名空间在**任意**一天有没有可查记录」，于是「有 11-04 的记录」
		// 被当成「11-02 也可以合法判空」⇒ `items=0` + `complete=true` + `EXHAUSTED`，
		// 一个静默的错误空。
		//
		// 守卫：窗口整体早于该源最早分区 ⇒ 本地记录无法覆盖这段历史，不得判空。
		// 它用的是「记录本身能证明的范围」（earliest 来自 Catalog 真实分区行），
		// 而不是对源状态的时间推断，因此是有信息量的判据。
		//
		// ----- 已被否决的两条推断（切勿重加）-----
		//
		// ① 「窗口晚于最早分区且源仍在采集 ⇒ 判空」的反面——把「源仍在采集」当作
		//    「记录可能没重建」的代理信号。已否决，理由是它区分不了两件事：
		//
		//      2026-11-02  VL=1746 条  ← 有数据，Catalog 记录丢了
		//      2026-11-03  VL=0   条  ← 真空（那天服务器没产生这类日志）
		//      2026-11-05  VL=0   条  ← 真空
		//
		//    三者都发生在「活着的源」身上。按「是否在采集」判，两个真空天会被一起
		//    打成 not_ready ⇒ 平台级 complete 永远为 false（把 #60 换个方向重演），
		//    而且报出的 ENGINE_NOT_READY 会把现场引向「引擎没起来」的错误方向。
		//
		// ② 任何基于「源状态 / 时间窗方向」的新推断。根因不在这里：Catalog journal
		//    是纯内存的（2026-10-30 裁定），重启后 `ensureCatalogPartition` 只在投递
		//    成功时补建分区记录 ⇒ 一个源**重启前**所有天的记录永远回不来。这是写入
		//    侧的事实缺失，查询侧拿自己的信息去补只会越补越多坑。
		//    正确的修法是让记录能重建（恢复 journal 持久化 + 启动重放），那之后
		//    11-02 查得到、11-03/11-05 是正当空，查询侧**不需要任何猜测**。
		//
		// 因此在记录重建落地之前，这里的口径是：**窗口整体早于最早分区**才 fail-closed；
		// 其余情形沿用 #60 的「按空成功计入」。已知残余风险：记录缺失且窗口落在
		// [最早分区, 窗口终点] 之内时仍可能静默判空——这属于记录重建要解决的问题，
		// 不是本函数能修的，已在报告里列为真实限制。
		if p.namespaceWindowPrecedesEarliestDay(allKeys, auth, req.TimeRange) {
			NamespaceNotProvablyEmptyCount.Add(1)
			res.Coverage.AddTarget(TargetCoverage{
				TargetID: auth,
				State:    CoverageNotReady,
				Reasons:  []string{ReasonNoCatalogRecord},
			})
			covered[auth] = true
			continue
		}
		// 授权目标可能是命名空间（其自身可含 '/'，如 ns/game-1），也可能是 ns/day：
		// 先按整串当命名空间找；找不到再按 parseTargetKey 拆出的命名空间找一次。
		if !p.catalogHasQueryableRecord(allKeys, auth) {
			key, ok := parseTargetKey(auth)
			if !ok || key.StorageNamespace == auth || !p.catalogHasQueryableRecord(allKeys, key.StorageNamespace) {
				continue
			}
		}
		cov := TargetCoverage{TargetID: auth, State: CoverageSuccess}
		res.Coverage.AddTarget(cov)
		covered[auth] = true
		view.TargetCoverage[auth] = cov
		// 登记进 TargetIDs：planRangesForView 按它产出同一条覆盖；它不在 QueryableTargets 里，
		// 故走「不可查」分支 ⇒ 只给覆盖、不给 range（空 = 结果里 0 行）。
		view.TargetIDs = append(view.TargetIDs, auth)
	}
}

// NamespaceNotProvablyEmptyCount 记录「因无法证明为空而拒绝判空」的次数。
//
// 为什么需要计数而不是只打日志（2026-11-05）：这是「本来会静默返回 0 行」被拦下的
// 次数，是最有诊断价值的指标。没有它时，用户只能看到「查询无数据」而不知道
// 自己的时间窗早于平台数据起点——这正是本次事故的报告形态（「这天没日志」）。
var NamespaceNotProvablyEmptyCount atomic.Int64

// namespaceWindowPrecedesEarliestDay 报告该授权目标的查询窗口是否**整体早于**其最早分区。
//
// 2026-11-05 真机：VL 里 inst:318 的 11-02 有 1746 条，但接口返回 0 行 +
// `complete=true` + `EXHAUSTED`（一个静默的错误空）。根因是 `coverEmptyAuthorizedTargets`
// 只问「该命名空间在**任意**一天有没有可查记录」：重启后 Catalog 纯内存，只有重启后
// 补建的天（11-04）有记录 ⇒ 命中任意一天 ⇒ 给 11-02 也补了「空成功」。
//
// 为什么不能简单改成「窗口内那一天必须有记录」：那会同时杀掉 #60 的正当情形——
// 静默源（最后分区早于窗口，如 inst:153）本应判为「空」；若严格要求窗口内有记录，
// 一个已停产的源会把整个平台判成 missing_coverage。
//
// 所以判据取**方向**而非交集：
//   - 窗口**晚于**该源最早分区 ⇒ 保留空成功（源在那段时间还没开工或已停产，无从推翻）；
//   - 窗口**整体早于**该源最早分区 ⇒ 判未就绪（本地记录无法覆盖这段历史，
//     「那天真没数据」与「那天的记录不在了」不可区分，按 fail-closed 不粉饰）。
//
// 时间窗缺失时（`FromUTC`/`ToUTC` 都空）不做判断：那是「不限分区日」，
// 已由 selectKeys 取全部分区，走到这里就说明确实无分区，按既有口径处理。
//
// 判据的适用边界（2026-11-05 复议）：它只能拦住「窗口完全落在该源有记录之前」。
// 「窗口夹在已有记录之间而该天记录缺失」（如分区 10-30 与 11-04、查 11-02）它拦不住，
// 因为方向判据看不到那段区间里发生过什么。那段属于 Catalog 记录重建的职责，
// 不在本函数里靠推断补充（原因见 coverEmptyAuthorizedTargets 的「已否决推断」段）。
func (p *Planner) namespaceWindowPrecedesEarliestDay(allKeys []catalog.PartitionKey, namespace string, tr TimeRange) bool {
	if namespace == "" || tr.ToUTC == "" {
		return false
	}
	to, err := time.Parse(time.RFC3339, tr.ToUTC)
	if err != nil {
		return false
	}
	earliest := ""
	for _, k := range allKeys {
		// 授权目标可能带 "/day" 后缀，故命中命名空间或 ns/day 两种形态都算。
		if k.StorageNamespace != namespace && namespace != k.String() {
			continue
		}
		if k.UTCDay != "" && (earliest == "" || k.UTCDay < earliest) {
			earliest = k.UTCDay
		}
	}
	if earliest == "" {
		return false
	}
	dayStart, err := time.Parse("2006-01-02", earliest)
	if err != nil {
		return false
	}
	// 窗口终点不晚于最早分区的起点 ⇒ 整段窗口全在该分区之前。
	return !to.After(dayStart)
}

// catalogHasQueryableRecord 报告 Catalog 中是否存在该命名空间下**可查**的记录（任意一天）。
// 只看 OwnerForQuery：staging / residual / 无权威一律不算（与主循环同一判据）。
func (p *Planner) catalogHasQueryableRecord(allKeys []catalog.PartitionKey, namespace string) bool {
	if namespace == "" {
		return false
	}
	for _, k := range allKeys {
		if k.StorageNamespace != namespace {
			continue
		}
		rec, ok := p.cat.Get(k)
		if !ok || rec == nil {
			continue
		}
		if ref := catalog.OwnerForQuery(rec); ref.OK {
			return true
		}
	}
	return false
}
