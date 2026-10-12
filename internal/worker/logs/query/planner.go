package query

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// PlanRequest 是 QueryPlanner 输入。
type PlanRequest struct {
	// Transient avoids registering an immutable view for an internal live cycle.
	Transient bool
	RequestID string
	// TimeRange 绝对 UTC；为空表示不限制分区日。
	TimeRange TimeRange
	// TargetIDs CP 计算后的授权目标（storage_namespace 或 ns/day）；空 = 本 Worker 全部 Catalog 分区。
	TargetIDs []string
	Budget    Budget
	// ViewRef 非空且 ViewID 非空时校验固定视图。
	ViewRef *ViewRef
	// RequireCold 为 true 时，若部署配置了 COLD 但目标冷层不可用 → partial。
	RequireCold bool
	// RequireArchive 为 true 时，若 ARCHIVE 未恢复 → partial / ARCHIVE_NOT_RESTORED。
	RequireArchive bool
	// AllowProjectionDrift 为 true 时，视图复用校验豁免「投影清单版本（ManifestVersion）变化」判 stale。
	//
	// 只允许「单次操作自己创建视图、自己读完」的导出路径置位（CP logcoord.Export 逐页 Search）：
	// 该路径不存在「前端旧 view_id 跨请求复用」场景，故投影漂移是源仍在实时追加内容的正常现象。
	// 其余维度一律不豁免：分区记录消失、无权威副本、目录/代次变化、closed_visible_seq 缩水
	// （数据变少）仍必须 VIEW_STALE。详见 validateViewAgainstCatalog。
	AllowProjectionDrift bool
}

// PlanResult 是 planner 输出。
type PlanResult struct {
	Ranges   []AuthoritativeRange
	Coverage Coverage
	View     *QueryView
	// Err 非空表示调用方必须原样返回，不得把 partial 转成空成功。
	Err *QueryError
	// Quality 从 projection 派生。
	Quality Quality
}

// Planner 按 Catalog 选择不重叠权威副本。
type Planner struct {
	cat    CatalogSource
	status StatusFunc
	// mu 保护 views/viewSeq（并发 Search/Stats/Tail）。
	mu sync.Mutex
	// views 内存视图注册表（foundation；生产持久化由后续 FR 承担）。
	views map[string]*QueryView
	// viewSeq 保证 ViewID 单调唯一（Windows 时钟粒度下 UnixNano 可能碰撞）。
	viewSeq uint64
	// now 可注入便于测试。
	now func() time.Time
}

// maxRetainedViews 内存视图注册表上限。
//
// 视图是可复用的固定查询集合，但每个「不带 view_id 的首次请求」都会新建一个视图。持续查询负载下
// 若不回收，注册表会随请求数无界增长（真机 30 分钟压测暴露：Worker RSS 由 158MiB 涨到 2.4GiB，超
// 契约 §6.6 的 1GiB）。插入前先清理过期视图，必要时按创建时间淘汰最旧视图；被淘汰视图的后续 Cursor
// 请求会得到显式 VIEW_STALE（契约允许「使旧 Cursor 明确失效」），而非无声变化。
const maxRetainedViews = 4096

// pruneViewsLocked 在插入前回收过期/超量的视图。调用方须持有 p.mu。
func (p *Planner) pruneViewsLocked() {
	if len(p.views) < maxRetainedViews {
		return
	}
	now := p.now()
	// 先删过期视图。
	for id, v := range p.views {
		if !v.ExpiresAt.IsZero() && !now.Before(v.ExpiresAt) {
			delete(p.views, id)
		}
	}
	// 仍超量：按创建时间淘汰最旧的，直至低于上限。
	for len(p.views) >= maxRetainedViews {
		oldestID := ""
		var oldest time.Time
		for id, v := range p.views {
			if oldestID == "" || v.CreatedAt.Before(oldest) {
				oldestID, oldest = id, v.CreatedAt
			}
		}
		if oldestID == "" {
			break
		}
		delete(p.views, oldestID)
	}
}

// NewPlanner 构造 Planner。status 为 nil 时使用 DefaultPartitionStatus。
func NewPlanner(cat CatalogSource, status StatusFunc) *Planner {
	if status == nil {
		status = DefaultPartitionStatus
	}
	return &Planner{
		cat:    cat,
		status: status,
		views:  make(map[string]*QueryView),
		now:    time.Now,
	}
}

// GetView 返回已登记视图。
func (p *Planner) GetView(viewID string) (*QueryView, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.views[viewID]
	return v, ok
}

// Views 返回全部视图副本（测试/诊断）。
func (p *Planner) Views() map[string]QueryView {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]QueryView, len(p.views))
	for k, v := range p.views {
		out[k] = *v
	}
	return out
}

// Plan 选择权威范围并构建/校验 Query View。
//
// 不变量：
//   - 每个 (storage_namespace, utc_day) 至多一个 AuthoritativeRange，且只来自 Catalog owner；
//   - ATTACHED_STAGING / residual 永不入选；
//   - 冷层/归档缺失只出现在 Coverage，不会并行再选一份 hot+cold range。
func (p *Planner) Plan(req PlanRequest) PlanResult {
	res := PlanResult{
		Coverage: Coverage{
			Complete:         true,
			EnumerationState: EnumOpen,
		},
		Quality: Quality{
			DuplicateQuality: DupExact,
			StatsQuality:     StatsExact,
		},
	}

	// —— 视图校验（有 view_id 时复用固定集合；generation 不匹配 → VIEW_STALE）——
	if req.ViewRef != nil && req.ViewRef.ViewID != "" {
		p.mu.Lock()
		view, ok := p.views[req.ViewRef.ViewID]
		p.mu.Unlock()
		if !ok {
			res.Err = newErr(ErrCodeViewStale, fmt.Sprintf("unknown view_id %q", req.ViewRef.ViewID))
			res.Coverage.EnumerationState = EnumStale
			res.Coverage.MarkIncomplete(ReasonConflictGeneration)
			return res
		}
		if view.OrderVersion != "" && req.ViewRef.OrderVersion != "" &&
			view.OrderVersion != req.ViewRef.OrderVersion {
			res.Err = newErr(ErrCodeViewStale, "order_version mismatch")
			res.Coverage.EnumerationState = EnumStale
			return res
		}
		if !view.ExpiresAt.IsZero() && !p.now().Before(view.ExpiresAt) {
			res.Err = newErr(ErrCodeViewStale, "view expired")
			res.Coverage.EnumerationState = EnumStale
			return res
		}
		stale := p.validateViewAgainstCatalog(view, &res.Coverage, req.AllowProjectionDrift)
		if stale != nil {
			res.Err = stale
			res.Coverage.EnumerationState = EnumStale
			return res
		}
		res.View = view
		// 分页：按 view 固定的 target 与 generation 选 range（不重新选最新集合）。
		p.planRangesForView(req, view, &res)
		return res
	}

	// —— 首次请求：创建固定视图 ——
	keys := p.selectKeys(req)
	var ranges []AuthoritativeRange
	view := &QueryView{
		ViewID:             p.newViewID(),
		OrderVersion:       SortVersion,
		TimeRange:          req.TimeRange,
		TargetIDs:          make([]string, 0, len(keys)),
		ClosedVisibleSeq:   make(map[string]uint64, len(keys)),
		Generations:        make(map[string]uint64, len(keys)),
		Owners:             make(map[string]string, len(keys)),
		DirIDs:             make(map[string]string, len(keys)),
		ProjectionVersions: make(map[string]string, len(keys)),
		QueryableTargets:   make(map[string]bool, len(keys)),
		TargetCoverage:     make(map[string]TargetCoverage, len(keys)),
		CreatedAt:          p.now(),
	}

	for _, key := range keys {
		targetID := key.String()
		view.TargetIDs = append(view.TargetIDs, targetID)

		rec, ok := p.cat.Get(key)
		if !ok {
			res.Coverage.AddTarget(TargetCoverage{
				TargetID: targetID,
				State:    CoverageNotReady,
				Reasons:  []string{ReasonNoCatalogRecord},
			})
			continue
		}

		// Catalog 权威：唯一查询副本入口（staging/residual 已被 OwnerForQuery 排除）。
		ref := catalog.OwnerForQuery(rec)
		if !ref.OK {
			// fallback：Catalog 实例方法
			if r2, ok2 := p.cat.OwnerForQuery(key); ok2 {
				ref = r2
			}
		}
		if !ref.OK {
			res.Coverage.AddTarget(TargetCoverage{
				TargetID: targetID,
				State:    CoverageNotReady,
				Reasons:  []string{ReasonNoCatalogAuthority},
			})
			continue
		}

		// 防御：staging 目录即使被误写进 ref 也不得入选。
		if ref.DirID != "" && rec.TargetDirID != "" && ref.DirID == rec.TargetDirID &&
			rec.MigrationState == catalog.StateAttachedStaging {
			res.Coverage.AddTarget(TargetCoverage{
				TargetID:          targetID,
				State:             CoveragePartial,
				Reasons:           []string{ReasonStagingExcluded},
				CatalogGeneration: ref.Generation,
				Owner:             string(ref.Owner),
				DirID:             ref.DirID,
			})
			continue
		}

		st := p.status(key, rec, ref)
		reasons := classifyStatus(req, rec, ref, st)
		for _, reason := range rec.PartialReasons {
			reasons = appendUniqueStr(reasons, reason)
		}

		// closed_visible_seq 来自已发布 projection（不可拆开组合）。
		cvs := uint64(0)
		manifest := ""
		if ref.Projection != nil {
			cvs = ClosedVisibleSeqOf(ref.Projection, targetID)
			if cvs == 0 {
				cvs = ClosedVisibleSeqOf(ref.Projection, "default")
			}
			manifest = ref.Projection.ManifestVersion
			if !ref.Projection.CoverageComplete {
				reasons = appendUniqueStr(reasons, ReasonProjectionIncomplete)
				res.Quality.DuplicateQuality = DupUnresolved
				res.Quality.StatsQuality = StatsPartial
			}
			if ref.Projection.ConflictCount > 0 {
				res.Quality.DuplicateQuality = DupConflict
				res.Quality.StatsQuality = StatsPartial
			}
		}

		// 恢复/冲突：coverage 降级，但 Catalog owner 若仍 queryable 仍给出 range。
		if catalog.IsFailure(rec.MigrationState) && rec.RecoveryRequired {
			reasons = appendUniqueStr(reasons, ReasonRecoveryRequired)
		}
		if rec.ConflictGeneration != "" {
			reasons = appendUniqueStr(reasons, ReasonConflictGeneration)
		}
		if catalog.JournalIncomplete(rec) && rec.RecoveryRequired {
			reasons = appendUniqueStr(reasons, ReasonJournalIncomplete)
		}

		view.ClosedVisibleSeq[targetID] = cvs
		view.Generations[targetID] = ref.Generation
		view.Owners[targetID] = string(ref.Owner)
		view.DirIDs[targetID] = ref.DirID
		view.ProjectionVersions[targetID] = manifest

		if !st.Queryable {
			state := CoverageNotReady
			if hasStr(st.NotReadyReasons, ReasonColdMissing) || hasStr(reasons, ReasonColdMissing) {
				state = CoveragePartial
			}
			if hasStr(st.NotReadyReasons, ReasonArchiveNotRestored) || hasStr(reasons, ReasonArchiveNotRestored) {
				state = CoverageArchiveMiss
			}
			all := append(append([]string{}, st.NotReadyReasons...), reasons...)
			res.Coverage.AddTarget(TargetCoverage{
				TargetID:           targetID,
				State:              state,
				Reasons:            all,
				ClosedVisibleSeq:   cvs,
				CatalogGeneration:  ref.Generation,
				Owner:              string(ref.Owner),
				DirID:              ref.DirID,
				ProjectionManifest: manifest,
			})
			continue
		}

		// 冷层缺失：不并行添加 cold range，只记 partial（契约：不重叠权威副本）。
		state := CoverageSuccess
		if len(reasons) > 0 {
			state = CoveragePartial
		}
		res.Coverage.AddTarget(TargetCoverage{
			TargetID:           targetID,
			State:              state,
			Reasons:            reasons,
			ClosedVisibleSeq:   cvs,
			CatalogGeneration:  ref.Generation,
			Owner:              string(ref.Owner),
			DirID:              ref.DirID,
			ProjectionManifest: manifest,
		})

		ranges = append(ranges, AuthoritativeRange{
			TargetID:         targetID,
			Key:              key,
			Owner:            ref.Owner,
			Tier:             TierOf(ref.Owner),
			DirID:            ref.DirID,
			Generation:       ref.Generation,
			ClosedVisibleSeq: cvs,
			Projection:       ref.Projection,
		})
	}

	assertDisjoint(ranges)
	res.Ranges = ranges
	for _, rng := range ranges {
		view.QueryableTargets[rng.TargetID] = true
	}
	for _, target := range res.Coverage.Targets {
		target.Reasons = append([]string(nil), target.Reasons...)
		view.TargetCoverage[target.TargetID] = target
	}

	// 查询窗内无分区的**授权目标**：按「空」而非「坏」纳入覆盖（与 #60 的 CP 侧同口径；
	// 2026-09-30 生产实证：静默源 inst:153 因此令平台 complete 恒 false）。
	//
	// 必须迭代 req.TargetIDs（授权目标）而不是 view.TargetIDs（2026-10-11 复审订正）：
	// res.Coverage 与 ranges 都按**分区**构造，而 CP 发的授权目标是**命名空间**粒度
	// （TargetInfo{ID: "inst:153"}）⇒ 窗口内没有分区的源在两边都不出现 ⇒ CP 判
	// missing_coverage ⇒ 一个静默源把整个平台判成未就绪。而这类源**根本不进 view.TargetIDs**
	// （selectKeys 按 dayOverlapsRange 过滤掉窗口外的分区）⇒ 迭代视图目标永远看不到它。
	//
	// record=true：此刻视图尚未登记（下面的 p.views[...] 还没执行）⇒ 无并发读者，可安全写回。
	p.coverEmptyAuthorizedTargets(req, view, &res)
	if !req.Transient {
		p.mu.Lock()
		p.pruneViewsLocked()
		p.views[view.ViewID] = view
		p.mu.Unlock()
	}
	res.View = view
	return res
}

// coverEmptyAuthorizedTargets 为「查询窗内没有任何分区的**授权目标**」补一条**空成功覆盖**。
//
// 症状与口径：CP 的授权目标是命名空间粒度，而覆盖与 range 都按分区构造 ⇒ 窗口内无数据的源在
// 两边都不出现 ⇒ CP 判 missing_coverage ⇒ 一个静默源（最后分区早于窗口，如 inst:153）把整个平台
// 判成未就绪。这与 #60 在 CP 侧「无数据 ≠ 故障」同口径：按**空结果**计入成功。
//
// 判据（缺一不可）：
//  1. 视图内没有任何条目承载该授权目标（判据与 remapCoverageTargets/CP 侧一致：条目 ID 等于
//     授权目标，或条目是它的分区子项）——已有覆盖就不得重复计入；
//  2. Catalog 里确实存在同命名空间、且 `OwnerForQuery` 给得出权威的记录（按命名空间找**任意**
//     一天：静默源的最后分区可能早于窗口，故绝不能用窗口内那一天去 Get，那必然落空）；
//  3. **不**把它标为可查（不进 QueryableTargets）——否则会为它追加一个指向不存在分区的 range，
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

// validateViewAgainstCatalog 检查固定视图是否仍可对齐 Catalog。
// 纯迁移（旧租约仍可读原集合）不自动失效；generation 变化 / 数据集合破坏 closed_visible_seq → VIEW_STALE。
//
// allowProjectionDrift 只豁免「投影清单版本变化」这一条分支（见下方标注），其余分支一律保留。
func (p *Planner) validateViewAgainstCatalog(view *QueryView, cov *Coverage, allowProjectionDrift bool) *QueryError {
	for targetID, wantGen := range view.Generations {
		key, ok := parseTargetKey(targetID)
		if !ok {
			continue
		}
		rec, ok := p.cat.Get(key)
		if !ok {
			cov.AddTarget(TargetCoverage{
				TargetID:          targetID,
				State:             CoverageStale,
				Reasons:           []string{ReasonNoCatalogRecord},
				ClosedVisibleSeq:  view.ClosedVisibleSeq[targetID],
				CatalogGeneration: wantGen,
			})
			return newErr(ErrCodeViewStale, fmt.Sprintf("view %s target %s catalog record missing", view.ViewID, targetID))
		}
		ref := catalog.OwnerForQuery(rec)
		if !ref.OK {
			return newErr(ErrCodeViewStale, fmt.Sprintf("view %s target %s no catalog authority", view.ViewID, targetID))
		}
		if ref.Generation != wantGen {
			cov.AddTarget(TargetCoverage{
				TargetID:          targetID,
				State:             CoverageStale,
				Reasons:           []string{ReasonConflictGeneration},
				ClosedVisibleSeq:  view.ClosedVisibleSeq[targetID],
				CatalogGeneration: ref.Generation,
				Owner:             string(ref.Owner),
				DirID:             ref.DirID,
			})
			return newErr(ErrCodeViewStale,
				fmt.Sprintf("view %s target %s generation mismatch view=%d catalog=%d",
					view.ViewID, targetID, wantGen, ref.Generation))
		}
		// owner/dir 切换但 generation 未变时以 Catalog 为准仍可能 stale（不应发生）。
		if wantDir, ok := view.DirIDs[targetID]; ok && wantDir != "" && ref.DirID != wantDir {
			return newErr(ErrCodeViewStale,
				fmt.Sprintf("view %s target %s dir mismatch view=%s catalog=%s",
					view.ViewID, targetID, wantDir, ref.DirID))
		}
		if wantVersion, ok := view.ProjectionVersions[targetID]; ok {
			currentVersion := ""
			if ref.Projection != nil {
				currentVersion = ref.Projection.ManifestVersion
			}
			// 投影清单版本漂移：内容清单变化（源仍在实时追加日志时**必然**发生）。
			//
			// 为什么可以按请求豁免（而非放宽校严）：ManifestVersion 只哈希 SourceProjections 内容清单,
			// 「数据变少」由紧随其后的 closed_visible_seq 缩水分支独立把关，因此内容追加走本分支、
			// 数据丢失走下一分支——豁免本分支不会放过任何数据丢失场景。
			//
			// 为什么必须能豁免：导出是「单次操作自己 CreateView、自己逐页读完」，耗时可达数十秒，
			// 期间任意源写入新投影都会让 165 个目标全部降级 partial，导出永远拿不到附件
			// （生产实证：只要玩家的业务服务器在跑，导出就永远失败）。
			// 该路径不存在「前端旧 view_id 跨请求复用」，故原设计意图（防旧 view_id 复用）在此不适用。
			if wantVersion != currentVersion && !allowProjectionDrift {
				cov.AddTarget(TargetCoverage{TargetID: targetID, State: CoverageStale, Reasons: []string{ReasonConflictGeneration}})
				return newErr(ErrCodeViewStale, fmt.Sprintf("view %s target %s projection changed", view.ViewID, targetID))
			}
		}
		// closed_visible_seq 破坏：Catalog projection 前缀小于视图固定值。
		if ref.Projection != nil {
			cur := ClosedVisibleSeqOf(ref.Projection, targetID)
			want := view.ClosedVisibleSeq[targetID]
			if cur > 0 && want > 0 && cur < want {
				cov.AddTarget(TargetCoverage{
					TargetID:          targetID,
					State:             CoverageStale,
					Reasons:           []string{ReasonProjectionIncomplete},
					ClosedVisibleSeq:  cur,
					CatalogGeneration: ref.Generation,
				})
				return newErr(ErrCodeViewStale,
					fmt.Sprintf("view %s target %s closed_visible_seq shrunk view=%d catalog=%d",
						view.ViewID, targetID, want, cur))
			}
		}
	}
	return nil
}

// planRangesForView 按视图固定 generation/dir 选择 range（禁止重新选最新集合）。
func (p *Planner) planRangesForView(req PlanRequest, view *QueryView, res *PlanResult) {
	for _, targetID := range view.TargetIDs {
		if !view.QueryableTargets[targetID] {
			if original, ok := view.TargetCoverage[targetID]; ok {
				res.Coverage.AddTarget(original)
			} else {
				res.Coverage.AddTarget(TargetCoverage{TargetID: targetID, State: CoverageNotReady,
					Reasons: []string{ReasonRangeUnavailable}})
			}
			continue
		}
		key, ok := parseTargetKey(targetID)
		if !ok {
			continue
		}
		if req.TargetIDs != nil && !matchTarget(key, req.TargetIDs) {
			// 视图目标已在创建时授权，但本次请求的授权集合不再覆盖它——必须显式降级，
			// 不能继续读取。否则「先建宽 View、再以窄授权复用」会绕过调用方的授权收窄，
			// 使权限撤销在该 Worker 上不生效（M-9）。
			res.Coverage.AddTarget(TargetCoverage{TargetID: targetID, State: CoverageNotReady,
				Reasons: []string{ReasonUnauthorizedTarget}})
			continue
		}
		wantGen := view.Generations[targetID]
		wantDir := view.DirIDs[targetID]
		ownerStr := view.Owners[targetID]
		cvs := view.ClosedVisibleSeq[targetID]

		rec, ok := p.cat.Get(key)
		if !ok {
			res.Coverage.AddTarget(TargetCoverage{
				TargetID: targetID,
				State:    CoverageNotReady,
				Reasons:  []string{ReasonNoCatalogRecord},
			})
			continue
		}
		ref := catalog.OwnerForQuery(rec)
		if ref.Generation != wantGen || (wantDir != "" && ref.DirID != wantDir) {
			res.Err = newErr(ErrCodeViewStale, "view generation/dir changed during plan")
			res.Coverage.EnumerationState = EnumStale
			return
		}

		var owner catalog.Owner
		switch ownerStr {
		case string(catalog.OwnerHot):
			owner = catalog.OwnerHot
		case string(catalog.OwnerCold):
			owner = catalog.OwnerCold
		case string(catalog.OwnerArchive):
			owner = catalog.OwnerArchive
		default:
			owner = ref.Owner
		}

		if original, ok := view.TargetCoverage[targetID]; ok {
			res.Coverage.AddTarget(original)
		} else {
			// 防御分支（当前**不可达**，如实标注，不冒充修复）：可查目标在创建视图时必已写入
			// view.TargetCoverage（Plan 先按 res.Coverage 全量填充，再补空成功覆盖）。
			// 真出现「可查却无覆盖记录」说明创建侧漏了记账——按未就绪如实上报，
			// 绝不伪造成空成功把缺口掩盖掉。
			res.Coverage.AddTarget(TargetCoverage{
				TargetID:          targetID,
				State:             CoverageNotReady,
				Reasons:           []string{ReasonRangeUnavailable},
				ClosedVisibleSeq:  cvs,
				CatalogGeneration: wantGen,
				Owner:             ownerStr,
				DirID:             wantDir,
			})
			continue
		}
		res.Ranges = append(res.Ranges, AuthoritativeRange{
			TargetID:         targetID,
			Key:              key,
			Owner:            owner,
			Tier:             TierOf(owner),
			DirID:            wantDir,
			Generation:       wantGen,
			ClosedVisibleSeq: cvs,
			Projection:       ref.Projection,
		})
	}
	// 复用路径的**未规划授权目标**必须显式上报「未就绪」（F1，2026-10-12 终审修复）：
	// 既不得补空成功、也不得沉默——详见 markUnplannedAuthorizedTargetsForReuse 的论证
	// （沉默会被 CP 按 readiness 判 success，与补空成功是同一个后果）。
	p.markUnplannedAuthorizedTargetsForReuse(req, view, res)
	assertDisjoint(res.Ranges)
}

// markUnplannedAuthorizedTargetsForReuse 为本请求授权、但**当前视图无法服务**的目标显式并入
// 「未就绪」覆盖（F1，2026-10-12 终审修复）。
//
// 什么目标会落到这里：视图在创建时按当时的 Catalog 定死了 TargetIDs/TargetCoverage；此后才
// 出现的可查目标不在其中——① 视图创建后新源开始写日志；② 复用请求把授权集合**放宽**。
// 这两种情形下主循环不会为它产出任何 range ⇒ **本次查询从未读过它的行**。
//
// 为什么两条路都堵死（缺一不可）：
//   - **不得**补 CoverageSuccess ✗：那是把「有数据未读」粉饰成「空」，`res.Coverage` 会判
//     complete=true ⇒ CP 的 BuildCoverage 放行 ⇒ 导出附件「完整」却静默缺失该源；
//   - **也不得**保持沉默 ✗（实测教训，2026-10-12）：CP 侧 `absentTargetReasons` 在**响应非空**时
//     返回 nil，`CoverageFromReadiness` 随即给 `ReadyOnline` 的目标判 success
//     （targets_absent_test.go 固定的既有语义：未出现 = 该目标无数据）⇒ 沉默会被读成「空成功」，
//     与补空成功是**同一个后果**，只是粉饰从 Worker 挪到了 CP。
//     ⇒ 唯一正确处置是**显式并入 CoverageNotReady**，让 CP 的 BuildCoverage 判 coverage_incomplete。
//
// 与创建路径的空成功口径不冲突：创建路径补 success 的前提是「此刻 Catalog 里确实查到了可查
// 记录」，证明的是**当下就能核验**的「该源在窗口内无分区」；复用路径的目标连「能不能读」都随
// 视图冻结而未知 ⇒ 按 fail-closed 判未就绪，与「宁可判 missing_coverage，也不把不知道粉饰成空」同口径。
//
// 只写本次响应：view 是共享对象、在 p.mu 之外被并发请求读，绝不就地改写。
func (p *Planner) markUnplannedAuthorizedTargetsForReuse(req PlanRequest, view *QueryView, res *PlanResult) {
	if len(req.TargetIDs) == 0 {
		// 未指定授权目标 = 本 Worker 全部 Catalog 分区：没有目标可点名，不发明覆盖。
		return
	}
	covered := make(map[string]bool, len(view.TargetCoverage)*2)
	for id := range view.TargetCoverage {
		covered[id] = true
		if key, ok := parseTargetKey(id); ok {
			covered[key.StorageNamespace] = true
			covered[key.UTCDay] = true
		}
	}
	for _, auth := range req.TargetIDs {
		if auth == "" || covered[auth] {
			continue
		}
		res.Coverage.AddTarget(TargetCoverage{
			TargetID: auth,
			State:    CoverageNotReady,
			Reasons:  []string{ReasonRangeUnavailable},
		})
	}
}

// selectKeys 按授权目标 + 时间范围筛选 Catalog 分区键。
func (p *Planner) selectKeys(req PlanRequest) []catalog.PartitionKey {
	all := p.cat.Keys()
	sort.Slice(all, func(i, j int) bool { return all[i].String() < all[j].String() })
	out := make([]catalog.PartitionKey, 0, len(all))
	for _, k := range all {
		if !matchTarget(k, req.TargetIDs) {
			continue
		}
		if !dayOverlapsRange(k.UTCDay, req.TimeRange) {
			continue
		}
		out = append(out, k)
	}
	return out
}

func matchTarget(key catalog.PartitionKey, authorized []string) bool {
	if len(authorized) == 0 {
		return true
	}
	id := key.String()
	for _, a := range authorized {
		if a == id || a == key.StorageNamespace || a == key.UTCDay {
			return true
		}
	}
	return false
}

// dayOverlapsRange 报告 utc_day（YYYY-MM-DD）是否与 [from,to) 有交集。
func dayOverlapsRange(day string, tr TimeRange) bool {
	if tr.FromUTC == "" && tr.ToUTC == "" {
		return true
	}
	dayStart, err := time.Parse(time.RFC3339, day+"T00:00:00Z")
	if err != nil {
		return true // 无法解析时不过滤，交给 Catalog 权威
	}
	dayEnd := dayStart.Add(24 * time.Hour)

	if tr.FromUTC != "" {
		from, err := time.Parse(time.RFC3339, tr.FromUTC)
		if err == nil && !dayEnd.After(from) {
			return false
		}
	}
	if tr.ToUTC != "" {
		to, err := time.Parse(time.RFC3339, tr.ToUTC)
		if err == nil && !dayStart.Before(to) {
			return false
		}
	}
	return true
}

func parseTargetKey(targetID string) (catalog.PartitionKey, bool) {
	// storage_namespace 可能含 '/'（如 ns/game-1）；utc_day 固定为最后一段。
	i := strings.LastIndex(targetID, "/")
	if i <= 0 || i == len(targetID)-1 {
		return catalog.PartitionKey{}, false
	}
	return catalog.PartitionKey{
		StorageNamespace: targetID[:i],
		UTCDay:           targetID[i+1:],
	}, true
}

func classifyStatus(req PlanRequest, rec *catalog.Record, ref catalog.QueryRef, st PartitionStatus) []string {
	var reasons []string
	if st.ColdConfigured && !st.ColdReady && (req.RequireCold || ref.Owner == catalog.OwnerCold) {
		reasons = appendUniqueStr(reasons, ReasonColdMissing)
	}
	if st.ArchiveConfigured && !st.ArchiveRestored && (req.RequireArchive || ref.Owner == catalog.OwnerArchive) {
		reasons = appendUniqueStr(reasons, ReasonArchiveNotRestored)
	}
	// 冷层已配置但不可用，且查询覆盖需要历史层时：不阻塞 HOT range，但必须 partial。
	if req.RequireCold && st.ColdConfigured && !st.ColdReady {
		reasons = appendUniqueStr(reasons, ReasonColdMissing)
	}
	if req.RequireArchive && st.ArchiveConfigured && !st.ArchiveRestored {
		reasons = appendUniqueStr(reasons, ReasonArchiveNotRestored)
	}
	_ = rec
	return reasons
}

// assertDisjoint 断言同一 partition 不会出现多条 range（禁止 hot+cold 并行）。
func assertDisjoint(ranges []AuthoritativeRange) {
	seen := make(map[string]string, len(ranges))
	for _, r := range ranges {
		k := r.RangeKey()
		if prev, ok := seen[k]; ok {
			panic(fmt.Sprintf("query: overlapping authoritative ranges for %s: %s and %s", k, prev, r.DirID))
		}
		seen[k] = r.DirID
	}
}

// newViewID 生成不可预测的 Worker 本地视图 ID。
//
// 原实现用 UnixNano + 自增序号，可被枚举：视图复用路径的授权校验（`planRangesForView`）
// 依赖「视图创建时的目标」作为读取范围，猜中 ID 就能以更窄的当前授权复用更宽的视图
// （M-4A/M-9）。改用 128 位加密随机数；失败时退回原方案以保证仍能生成唯一 ID
// （viewSeq 单调，不依赖时钟粒度）。
func (p *Planner) newViewID() string {
	p.mu.Lock()
	p.viewSeq++
	seq := p.viewSeq
	p.mu.Unlock()
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("view-%d-%d", p.now().UnixNano(), seq)
	}
	return "view-" + strconv.FormatUint(seq, 36) + "-" + hex.EncodeToString(buf[:])
}

func appendUniqueStr(list []string, v string) []string {
	if v == "" {
		return list
	}
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

func hasStr(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
