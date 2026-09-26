package query

import (
	"fmt"
	"sort"
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
		stale := p.validateViewAgainstCatalog(view, &res.Coverage)
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
	if !req.Transient {
		p.mu.Lock()
		p.pruneViewsLocked()
		p.views[view.ViewID] = view
		p.mu.Unlock()
	}
	res.View = view
	return res
}

// validateViewAgainstCatalog 检查固定视图是否仍可对齐 Catalog。
// 纯迁移（旧租约仍可读原集合）不自动失效；generation 变化 / 数据集合破坏 closed_visible_seq → VIEW_STALE。
func (p *Planner) validateViewAgainstCatalog(view *QueryView, cov *Coverage) *QueryError {
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
			if wantVersion != currentVersion {
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
		if !matchTarget(key, req.TargetIDs) && len(req.TargetIDs) > 0 {
			// 视图目标已在创建时授权；后续请求授权收窄仍必须拒绝侧信道。
			// 此处允许视图内目标继续读取（权限撤销由调用方在 service 层拦截）。
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
			res.Coverage.AddTarget(TargetCoverage{
				TargetID: targetID, State: CoverageNotReady, Reasons: []string{ReasonRangeUnavailable},
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
	assertDisjoint(res.Ranges)
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

func (p *Planner) newViewID() string {
	p.mu.Lock()
	p.viewSeq++
	seq := p.viewSeq
	p.mu.Unlock()
	return fmt.Sprintf("view-%d-%d", p.now().UnixNano(), seq)
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
