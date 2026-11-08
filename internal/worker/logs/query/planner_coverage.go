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
		if p.windowDaysAllMissing(allKeys, auth, req.TimeRange) {
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

// windowDaysAllMissing 报告「该授权目标在本命名空间有记录，但查询窗内的天**一条都没有**」。
//
// 2026-11-05 真机：VL 里 inst:318 的 11-01 有 2880 条、11-02 有 1746 条、11-04 有 877 条，
// 而接口对这三天的返回都是 `items=0` + `complete=true` + `state=success`
// —— 一个**静默的错误空**，比报错危险得多（用户看到「这天没日志」，不会去查）。
//
// # 为什么不能用「窗口是否早于最早分区」判
//
// 早前版本用的是方向判据（`namespaceWindowPrecedesEarliestDay`）：只有窗口**整体早于**
// 最早分区才拒绝判空。journal 恢复落盘后它立刻失效——因为最早分区变成了 10-28
// （journal 里真实存在的老记录），于是查 11-01/11-02 时「窗口不早于 10-28」⇒ 放行 ⇒ 静默判空。
//
// 方向判据的根本局限：它只看窗口与**已有记录**的相对位置，不看**窗口内本身有没有记录**。
// 而真正的判据应当是后者——`coverEmptyAuthorizedTargets` 要证明的是「窗口内确实无分区」，
// 那就有两个必须区分的世界：
//
//	① 该命名空间在 Catalog 里**完全没有记录** ⇒ 本 Worker 不认识这个源
//	   （既可能真没数据，也可能它从未采集过）⇒ 不发明空成功，保持未就绪（既有口径）。
//	② 该命名空间**有记录**，但窗口内每一天都没有 ⇒ **记录缺失**的典型形态
//	   （重启丢了、journal 没覆盖到、投递还没发生）⇒ 不得粉饰成空。
//
// ② 正是本函数拦的情形。它不依赖任何对「源状态」或「时间窗方向」的推断，
// 只依赖 Catalog 自己记录的事实：**这个命名空间我认识，但这一天我确实没有**。
// 这与 SelectKeys 的口径一致——那边已经按窗口过滤掉了窗口外的分区，
// 所以「窗口内无分区」在这两个世界里的含义必须分开处理。
//
// # 为什么不会误伤 #60 的正当空
//
// #60 的场景是「静默源：最后分区早于窗口」——该源**本进程从未在窗口内有过分区**，
// 但它在自己的数据区间之外确实无数据。若按本判据，它会被判未就绪，
// 进而让平台级 complete 恒 false。
//
// 这里的关键区别是**该源是否仍在产出**：一个已停产的源（#60 的 inst:153），
// 它的最后分区本就早于窗口，窗口内无记录是**正确事实**；
// 而一个仍在产的源，窗口内**整段**无记录几乎必然意味着记录缺失。
//
// 本实现不引入「是否在采集」这类外部信号（已在 coverEmptyAuthorizedTargets 的
// 「已否决推断」里论证过：它区分不了真空与丢记录）。取而代之的判据是：
// **只在该源在窗口之后仍有分区（即窗口不是其数据区间的末尾）时才拒绝判空**。
// 这样：
//   - 静默源（窗口晚于其全部记录）⇒ 保留空成功 ✓ #60 不退步
//   - 中段缺天（窗口内有缺、窗口之后仍有记录）⇒ 拒绝判空 ✓ 本次事故被拦下
//
// 时间窗缺失时（`FromUTC`/`ToUTC` 都空）不判断：那是不限分区日，已由 selectKeys 取全部分区，
// 走到这里就说明确实无分区。
func (p *Planner) windowDaysAllMissing(allKeys []catalog.PartitionKey, namespace string, tr TimeRange) bool {
	if namespace == "" || tr.ToUTC == "" {
		return false
	}
	to, err := time.Parse(time.RFC3339, tr.ToUTC)
	if err != nil {
		return false
	}
	var from time.Time
	if tr.FromUTC != "" {
		if parsed, parseErr := time.Parse(time.RFC3339, tr.FromUTC); parseErr == nil {
			from = parsed
		}
	}
	// 单次遍历同时取两件事：该命名空间是否有记录、其最后分区日。
	hasRecord := false
	latest := ""
	for _, k := range allKeys {
		// 授权目标可能带 "/day" 后缀，故命中命名空间或 ns/day 两种形态都算。
		if k.StorageNamespace != namespace && namespace != k.String() {
			continue
		}
		hasRecord = true
		if k.UTCDay != "" && k.UTCDay > latest {
			latest = k.UTCDay
		}
	}
	if !hasRecord || latest == "" {
		// 世界 ①：本 Worker 不认识这个命名空间 ⇒ 交给既有口径（不发明空成功）。
		return false
	}
	// 该源的数据区间是否**延续到窗口之后**。只有延续时才说明「窗口内缺天」是异常。
	// 最后分区日的次日 00:00 严格晚于窗口终点 ⇒ 该源在窗口之后仍有数据。
	latestStart, err := time.Parse("2006-01-02", latest)
	if err != nil {
		return false
	}
	continuesAfterWindow := latestStart.AddDate(0, 0, 1).After(to)
	if !continuesAfterWindow {
		// 静默源 / 已停产：窗口晚于其全部数据 ⇒ 窗口内无记录是正确事实，保留空成功（#60）。
		return false
	}
	// 走到这里：该源在窗口之后仍有分区，而窗口内一条都没有 ⇒ 记录缺失，不得判空。
	// 窗口起点为空时（FromUTC 未给）保守按「窗口内无记录」处理。
	if from.IsZero() {
		return true
	}
	// 窗口与数据区间无交集时仍属正当空（如查很久以前、而该源是后来才上线的）。
	return !latestStart.Before(from)
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
