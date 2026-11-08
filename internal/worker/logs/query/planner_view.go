package query

import (
	"fmt"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// validateViewAgainstCatalog 由 Catalog 原子验证租约及原集合仍然可读。
// 正常追加不改旧 projection；历史读取权不会借默认 OwnerForQuery 获得。
//
// 第三个参数（原为 `AllowProjectionDrift bool`）**目前被显式丢弃**，不是遗漏：
// 2026-11 之後这里不再因为「投影清单版本变化」而判 VIEW_STALE——视图复用一律以
// Catalog 的租约与「原集合是否仍可读」为准，所以没有需要豁免的检查。
// 保留 `req.AllowProjectionDrift` 字段与它的调用点，是因为它在**别处仍然生效**：
// `resource_budget.go` 用它为该请求预留一份导出读额度（`vlsup.ReservationExport`）。
// 删字段会把那条预算预留一起丢掉，那是行为变更，不是清理。
//
// 若将来真要让本函数重新分支，把 `_ bool` 改回具名参数即可；在此之前不要删。
func (p *Planner) validateViewAgainstCatalog(view *QueryView, cov *Coverage, _ bool) *QueryError {
	for id, projection := range view.Projections {
		if projection != nil {
			continue
		}
		key, ok := parseTargetKey(id)
		if !ok {
			continue
		}
		if record, exists := p.cat.Get(key); exists && record.PublishedProjection != nil {
			return newErr(ErrCodeViewStale, "previously unavailable target has a new projection")
		}
	}
	release, err := p.cat.PinLeasedRead(view.ViewID, p.now())
	if err != nil {
		for _, id := range view.TargetIDs {
			cov.AddTarget(TargetCoverage{TargetID: id, State: CoverageStale, Reasons: []string{ReasonConflictGeneration}})
		}
		return newErr(ErrCodeViewStale, fmt.Sprintf("view %s lease stale: %v", view.ViewID, err))
	}
	release()
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

		owner := catalog.Owner(ownerStr)

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
			Projection:       cloneProjection(view.Projections[targetID]),
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
