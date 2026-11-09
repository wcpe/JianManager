package query

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// PlanRequest 是 QueryPlanner 输入。
type PlanRequest struct {
	// Transient 使用每轮短租约，不登记可跨请求复用的实时尾流视图。
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
	// AllowProjectionDrift 保留旧导出请求的线缆兼容；不再豁免原集合或租约校验。
	// 正常追加统一读取冻结投影，任何请求都不能把最新逐源列表混入旧 view。
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
	// mu 保护本进程活动视图与到期回收器（并发 Search/Stats/Tail）。
	mu sync.Mutex
	// views 保存本进程固定视图；重启不复活旧 ID，物理保护由 Catalog 有界租约承担。
	views map[string]*QueryView
	// now 可注入便于测试。
	now         func() time.Time
	ttl         time.Duration
	maxViews    int
	timers      map[string]*time.Timer
	activeViews map[string]time.Time
	closed      bool
	// 注：曾尝试接一路「仍在采集的命名空间」信号来区分「真空」与「丢记录」，
	// 2026-11-05 回退——源是否在采集与「那天有没有数据」无关（见 coverEmptyAuthorizedTargets 论证）。
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
	now := p.now()
	drop := func(id string) {
		delete(p.views, id)
		delete(p.activeViews, id)
		if timer := p.timers[id]; timer != nil {
			timer.Stop()
			delete(p.timers, id)
		}
		if p.cat != nil {
			logLeaseCleanupError(p.cat.ReleaseQueryView(id))
		}
	}
	for id, v := range p.views {
		if !now.Before(v.ExpiresAt) {
			drop(id)
		}
	}
	for len(p.views) >= p.maxViews {
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
		drop(oldestID)
	}
}

// NewPlanner 构造 Planner。status 为 nil 时使用 DefaultPartitionStatus。
func NewPlanner(cat CatalogSource, status StatusFunc) *Planner {
	return NewPlannerWithOptions(cat, status, Options{})
}

// GetView 返回已登记视图。
func (p *Planner) GetView(viewID string) (*QueryView, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.views[viewID]
	return cloneQueryView(v), ok
}

// Views 返回全部视图副本（测试/诊断）。
func (p *Planner) Views() map[string]QueryView {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]QueryView, len(p.views))
	for k, v := range p.views {
		out[k] = *cloneQueryView(v)
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

	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed || p.cat == nil {
		res.Err = newErr(ErrCodeNotReady, "query planner unavailable")
		res.Coverage.MarkIncomplete(ReasonNoCatalogAuthority)
		return res
	}
	logLeaseCleanupError(p.cat.PruneQueryLeases(p.now()))

	// —— 视图校验：读取位置与逐源集合必须仍由原租约保护 ——
	if req.ViewRef != nil && req.ViewRef.ViewID != "" {
		p.mu.Lock()
		stored, ok := p.views[req.ViewRef.ViewID]
		view := cloneQueryView(stored)
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
			logLeaseCleanupError(p.ReleaseView(view.ViewID))
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
		res.Quality = view.Quality
		// 分页：按 view 固定的 target 与 generation 选 range（不重新选最新集合）。
		p.planRangesForView(req, view, &res)
		return res
	}

	// —— 首次请求：创建固定视图 ——
	keys := p.selectKeys(req)
	var ranges []AuthoritativeRange
	createdAt := p.now()
	// 签发前先回收本地过期/超量视图，不能被 Catalog 容量闸挡在淘汰动作之前。
	p.mu.Lock()
	p.pruneViewsLocked()
	p.mu.Unlock()
	viewID, issueErr := p.cat.NewQueryViewID(createdAt)
	if issueErr != nil {
		res.Err = snapshotQueryError(issueErr)
		res.Coverage.MarkIncomplete(ReasonBudgetExceeded)
		return res
	}
	view := &QueryView{
		ViewID:             viewID,
		OrderVersion:       SortVersion,
		TimeRange:          req.TimeRange,
		TargetIDs:          make([]string, 0, len(keys)),
		ClosedVisibleSeq:   make(map[string]uint64, len(keys)),
		Generations:        make(map[string]uint64, len(keys)),
		Owners:             make(map[string]string, len(keys)),
		DirIDs:             make(map[string]string, len(keys)),
		ProjectionVersions: make(map[string]string, len(keys)),
		Projections:        make(map[string]*catalog.PublishedProjection, len(keys)),
		LeaseIDs:           make(map[string]string, len(keys)),
		Transient:          req.Transient,
		QueryableTargets:   make(map[string]bool, len(keys)),
		TargetCoverage:     make(map[string]TargetCoverage, len(keys)),
		CreatedAt:          createdAt,
	}
	view.ExpiresAt = view.CreatedAt.Add(p.ttl)
	snapshots, err := p.cat.SnapshotAndLease(catalog.QuerySnapshotRequest{ViewID: view.ViewID,
		Holder: "worker-query", Keys: keys, Now: view.CreatedAt, TTL: p.ttl})
	if err != nil {
		logLeaseCleanupError(p.cat.ReleaseQueryView(view.ViewID))
		res.Err = snapshotQueryError(err)
		if res.Err.Code == ErrCodeBudgetExceeded {
			res.Coverage.MarkIncomplete(ReasonBudgetExceeded)
		} else {
			res.Coverage.MarkIncomplete(ReasonRangeUnavailable)
		}
		return res
	}
	byKey := make(map[catalog.PartitionKey]catalog.QuerySnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		byKey[snapshot.Key] = snapshot
		if snapshot.ExpiresAt.Before(view.ExpiresAt) {
			view.ExpiresAt = snapshot.ExpiresAt
		}
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

		// 权威位置只用同锁捕获并登记租约的快照；Get 仅提供诊断状态。
		snapshot, snapOK := byKey[key]
		if !snapOK {
			// 未获取物理租约只能保留不可读覆盖；诊断分类不产生历史读取权。
			view.Projections[targetID] = nil
			diagnostic := catalog.OwnerForQuery(rec)
			status := p.status(key, rec, diagnostic)
			// 归档权威不能报成「没有 Catalog 权威」：那是配置/记录缺失的措辞，
			// 而这里的真相是「权威在归档、本地未恢复」。原因不同，处置动作也不同
			// （一个是查配置，一个是执行 rehydrate），故按 owner 分开取首个原因。
			lead := ReasonNoCatalogAuthority
			// 归档对象已按保留期清除（归档对象回收）时，原因**必须**是 ARCHIVE_PURGED。
			//
			// 与 ARCHIVE_NOT_RESTORED 分开的理由是**处置动作相反**：前者是「平台按保留期
			// 删掉了，取不回来」，后者是「副本还在归档里，去 rehydrate」。
			// 复用 MISSING 会让用户反复点恢复、反复得到第二条错误，最后仍要人工解释。
			//
			// 这里也不能返回空成功：数据确实不在了，静默的 0 行会被读成「那天没有日志」。
			if purgedArchive(rec) {
				lead = ReasonArchivePurged
			} else if rec != nil && rec.Owner == catalog.OwnerArchive {
				lead = ReasonArchiveNotRestored
			}
			reasons := append([]string{lead}, status.NotReadyReasons...)
			reasons = append(reasons, classifyStatus(req, rec, diagnostic, status)...)
			for _, reason := range rec.PartialReasons {
				reasons = appendUniqueStr(reasons, reason)
			}
			state := CoverageNotReady
			// 两种「归档副本不可读」共用 CoverageArchiveMiss（proto 没有更细的枚举，
			// 不为此扩已冻结的 wire 契约），区分度由 reason 与 error code 承担。
			if hasStr(reasons, ReasonArchiveNotRestored) || hasStr(reasons, ReasonArchivePurged) {
				state = CoverageArchiveMiss
			}
			res.Coverage.AddTarget(TargetCoverage{
				TargetID: targetID,
				State:    state,
				Reasons:  reasons,
				// 归档权威的位置如实带上：审计要看的是「正本在哪」，
				// 而不是一个空白的 dir_id（那会让现场以为记录坏了）。
				Owner: string(recOwner(rec)),
				DirID: recOwnerDirID(rec),
			})
			continue
		}

		projection, prefixKnown := freezeProjection(key, snapshot.Projection)
		ref := catalog.QueryRef{OK: true, Owner: snapshot.Owner, DirID: snapshot.DirID,
			Generation: snapshot.Generation, Projection: projection}
		view.LeaseIDs[targetID] = snapshot.LeaseID
		view.Projections[targetID] = cloneProjection(projection)

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
		if !prefixKnown {
			st.Queryable = false
			st.NotReadyReasons = appendUniqueStr(st.NotReadyReasons, ReasonProjectionIncomplete)
		}
		reasons := classifyStatus(req, rec, ref, st)
		for _, reason := range rec.PartialReasons {
			reasons = appendUniqueStr(reasons, reason)
		}

		// closed_visible_seq 来自已发布 projection（不可拆开组合）。
		cvs := uint64(0)
		manifest := ""
		if ref.Projection != nil {
			// 明确的零上界不能再借用 default 或其他来源的更大水位。
			cvs = ClosedVisibleSeqOf(ref.Projection, targetID)
			manifest = ref.Projection.ManifestVersion
			// 质量必须按 rank 取 worst 合并，不能无条件覆盖：keys 由 selectKeys 按
			// key.String() 升序给出，无条件赋值会让「后处理的 target」冲掉「先处理的
			// target」已判定的更差等级——同一份数据仅因 namespace 命名顺序不同就上报
			// 不同等级（conflict 被 unresolved 覆盖即丢失唯一的人工修复入口）。
			if !ref.Projection.CoverageComplete && !projectionlessScope(ref.Projection) {
				reasons = appendUniqueStr(reasons, ReasonProjectionIncomplete)
				res.Quality.DuplicateQuality = worstDuplicateQuality(res.Quality.DuplicateQuality, DupUnresolved)
				res.Quality.StatsQuality = worstStatsQuality(res.Quality.StatsQuality, StatsPartial)
			}
			if ref.Projection.ConflictCount > 0 {
				res.Quality.DuplicateQuality = worstDuplicateQuality(res.Quality.DuplicateQuality, DupConflict)
				res.Quality.StatsQuality = worstStatsQuality(res.Quality.StatsQuality, StatsPartial)
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
			Projection:       cloneProjection(ref.Projection),
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
	view.Quality = res.Quality
	if err := p.registerView(view); err != nil {
		logLeaseCleanupError(p.cat.ReleaseQueryView(view.ViewID))
		res.Err = newErr(ErrCodeNotReady, err.Error())
		res.Coverage.MarkIncomplete(ReasonRangeUnavailable)
		return res
	}
	res.View = cloneQueryView(view)
	return res
}

// purgedArchive 报告该分区的归档副本是否已按保留期清除（归档对象回收）。
//
// 抽成单函数而不是在多处各写一遍判断：这个条件决定「用户看到的是『已清除』还是
// 『去恢复一下』」，而两处措辞一旦分叉，用户就会在两条互斥的引导之间来回。
func purgedArchive(rec *catalog.Record) bool {
	return rec != nil && rec.Owner == catalog.OwnerArchive && rec.ArchiveEvidence.ArchiveObjectPurged()
}

func classifyStatus(req PlanRequest, rec *catalog.Record, ref catalog.QueryRef, st PartitionStatus) []string {
	var reasons []string
	// 归档权威的判定必须以**记录**为准，不能只看 ref：
	// `OwnerForQuery` 在 owner_dir 为空时会返回 OK=false，此时 ref.Owner 是空串，
	// 若只看 ref 就会把「已归档但本地无副本」误报成「没有 Catalog 权威」——
	// 运维看到的将是 NO_CATALOG_AUTHORITY（像配置丢了），而不是
	// ARCHIVE_NOT_RESTORED（像归档需要恢复）。两者处置动作完全不同。
	archivedAuthority := (rec != nil && rec.Owner == catalog.OwnerArchive) || ref.Owner == catalog.OwnerArchive
	// 归档对象已按保留期清除时**不再**报「未恢复」：那条建议此刻是死路。
	//
	// 注意这里不能只把 archivedAuthority 置假——下面还有一条按 `req.RequireArchive`
	// 独立追加的分支（那是「调用方要求归档层」的判据，不走 archivedAuthority）。
	// 只改一处会在「显式要求归档层」的请求上留下「已清除 + 请去恢复」并存，
	// 而这正是本用例集里 M14 变异的形态：单点修复看着对，实际只堵了一半。
	purged := purgedArchive(rec)
	if purged {
		archivedAuthority = false
	}
	if st.ColdConfigured && !st.ColdReady && (req.RequireCold || ref.Owner == catalog.OwnerCold) {
		reasons = appendUniqueStr(reasons, ReasonColdMissing)
	}
	if st.ArchiveConfigured && !st.ArchiveRestored && (req.RequireArchive || archivedAuthority) && !purged {
		reasons = appendUniqueStr(reasons, ReasonArchiveNotRestored)
	}
	// 归档权威：正本在归档，热层可能已被回收。本地没有对齐的已恢复投影时，
	// 除了「未恢复」还必须显式标出「该读归档」，否则调用方会按 HOT 缺失去猜。
	if archivedAuthority && !st.ArchiveRestored {
		reasons = appendUniqueStr(reasons, ReasonArchiveNotRestored)
	}
	// 冷层已配置但不可用，且查询覆盖需要历史层时：不阻塞 HOT range，但必须 partial。
	if req.RequireCold && st.ColdConfigured && !st.ColdReady {
		reasons = appendUniqueStr(reasons, ReasonColdMissing)
	}
	if req.RequireArchive && st.ArchiveConfigured && !st.ArchiveRestored && !purged {
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

// snapshotQueryError 保留容量拒绝的可恢复语义，不把合法负载过载误报为永久 stale。
func snapshotQueryError(err error) *QueryError {
	if errors.Is(err, catalog.ErrQueryAdmission) {
		return &QueryError{Code: ErrCodeBudgetExceeded, Message: err.Error(), Retryable: true, RetryAfter: 1000}
	}
	return newErr(ErrCodeNotReady, fmt.Sprintf("query snapshot unavailable: %v", err))
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

// projectionlessScope 报告该投影是不是「简化投递路径补建的空壳」。
//
// ## 背景
//
// 2026-11-02 投递路径简化后，`deliverCtx` 不再走 `publishSnapshot`，也就不再维护
// 「已发布投影」这个概念。为了让查得到数据，写入侧会补建一个**最小分区记录**
// （`ingest.ensureCatalogPartition`）：owner / dirID / 一个代次为空的投影结构。
//
// 那个空壳的 `CoverageComplete` 是 false（没有「发布」这回事）。若照旧据此推
// `PROJECTION_INCOMPLETE` + `DupUnresolved/StatsPartial`，**所有新数据都会被标成
// “不完整/未对账”**——用户每次查都会看到一条降低信任度的横幅，而实际上什么都没错。
//
// ## 判据
//
// 三条同时成立才算空壳（三条都只对简化路径产生，`publishSnapshot` 走不到）：
//   - `ManifestVersion` 为空：`publishSnapshot` 末尾**总会**写 manifest（`manifest-<sha256>`）；
//   - `SourceProjections` 为空；
//   - `ClosedVisibleSeq` 为空。
//
// 旧数据（简化前的已发布投影）三者至少有一个非空，不会被误判。
func projectionlessScope(proj *catalog.PublishedProjection) bool {
	if proj == nil {
		return false
	}
	return proj.ManifestVersion == "" && len(proj.SourceProjections) == 0 && len(proj.ClosedVisibleSeq) == 0
}

// recOwner / recOwnerDirID 是 nil 安全的记录读取，供覆盖登记使用。
func recOwner(rec *catalog.Record) catalog.Owner {
	if rec == nil {
		return ""
	}
	return rec.Owner
}

func recOwnerDirID(rec *catalog.Record) string {
	if rec == nil {
		return ""
	}
	return rec.OwnerDirID
}
