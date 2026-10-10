package logcoord

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// resolveForQueryFast 是 resolveForQuery 的**首屏快路径**变体：
// 与慢路径完全同源，唯一差别＝**不调 CreateView**（其中含 `bindWorkerViews` 的"每 Worker 一次 OpenView 往返" ✗），
// 改为内联构造视图对象（不绑定 Worker 视图、不登记进 c.views）。
// 目标解析（授权收敛 ✗）与慢路径**一字不差**；返回的 View.WorkerViewIDs 为空 ⇒
// fanoutOne 必须走**显式 View=nil 第二路径**（见其 fast 形参 ✗）。
// 复用分支（q.ViewID != ""）与慢路径逐字相同：复用已登记视图时本就走既有绑定 ✓，无需第二路径。
func (c *Coordinator) resolveForQueryFast(ctx context.Context, q Query) (View, *ResolvedTargets, error) {
	if q.ViewID != "" {
		v, ok := c.GetView(q.ViewID)
		if !ok {
			return View{}, nil, fmt.Errorf("%w: %s", errNoView, q.ViewID)
		}
		if err := authorizeViewReuse(v, q); err != nil {
			// 首屏/翻页快路径容忍"固定条件变化"（时间范围/过滤/onlineOnly/permission scope/order ✗）：
			// 不再 409，而是按当前条件重建视图继续（等价一次新查询 ✓）。
			// 安全线不动 ✗：授权主体不匹配与授权集合不覆盖走 ErrViewAuthz，原样上抛 ⇒ 越权复用不可能。
			if !errors.Is(err, ErrViewReuseMismatch) {
				return View{}, nil, err
			}
			goto newViewBranch
		}
		// 复用同一逻辑目标集合；不因当前 readiness 变化而重新选择集合。
		// 仍需 TargetInfo 以便 fanout/coverage：按 view.TargetIDs 再解析身份映射，
		// 但查询目标集合锁定为 view.TargetIDs。
		infos, err := c.resolver.Resolve(ctx, v.AuthorizedIDs)
		if err != nil {
			return View{}, nil, err
		}
		byID := make(map[string]TargetInfo, len(infos))
		for _, t := range infos {
			byID[t.ID] = t
		}
		rt := &ResolvedTargets{All: infos, OnlineOnly: v.OnlineOnly}
		for _, id := range v.TargetIDs {
			if t, ok := byID[id]; ok {
				rt.Queried = append(rt.Queried, t)
			} else {
				// view 中的目标已消失：保留占位，coverage 记 not_ready。
				rt.Queried = append(rt.Queried, TargetInfo{ID: id, Readiness: ReadyNotReady})
			}
		}
		// 排除项（online_only）从 All-Queried 推导。
		inView := make(map[string]bool, len(rt.Queried))
		for _, t := range rt.Queried {
			inView[t.ID] = true
		}
		for _, t := range infos {
			if !inView[t.ID] {
				rt.Excluded = append(rt.Excluded, t)
			}
		}
		v.Budget = readBudgetForQuery(v, q)
		v = c.bindExistingView(ctx, v, rt)
		return v, rt, nil
	}
	// 快路径的"新视图"支：内联 CreateView 的构造段，**跳过** bindWorkerViews 与 c.views 登记 ✗。
	// 上方复用分支在"条件变化被容忍"时也跳到此处（按当前条件重建 ✗）。
newViewBranch:
	resolved, err := ResolveTargets(ctx, c.resolver, q.AuthorizedTargetIDs, q.OnlineOnly)
	if err != nil {
		return View{}, nil, err
	}
	view := View{
		ViewID:                   NewViewID(),
		OrderVersion:             OrderVersion,
		FromUTC:                  q.FromUTC,
		ToUTC:                    q.ToUTC,
		TargetIDs:                resolved.TargetIDs(),
		AuthorizedIDs:            append([]string(nil), q.AuthorizedTargetIDs...),
		PrincipalKey:             q.PrincipalKey,
		OnlineOnly:               q.OnlineOnly,
		Budget:                   q.Budget,
		Filter:                   q.Filter,
		PermissionScope:          q.PermissionScope,
		WorkerViewIDs:            make(map[string]string),
		WorkerViewErrors:         make(map[string]string),
		WorkerCatalogGenerations: make(map[string]string),
		TargetAuthorizationIDs:   make(map[string]string),
		CreatedAt:                time.Now().UTC(),
	}
	for _, target := range resolved.All {
		if target.AuthorizationID != "" {
			view.TargetAuthorizationIDs[target.ID] = target.AuthorizationID
		}
	}
	// 登记进 c.views（新契约 ✓）：快查询返回的 viewId 必须二次可用（分页/翻页会带回来），
	// 否则第二次请求落 400「view 不存在」✗。该视图 WorkerViewIDs 为空 ⇒ 属"未绑定"形态：
	// 复用分支已按新契约容忍（resolve_fast.go 顶部 ✓），慢路径重推/绑定另有其判定 ✗。
	c.mu.Lock()
	if c.views == nil {
		c.views = make(map[string]View)
	}
	c.views[view.ViewID] = view
	c.pruneViewsLocked()
	c.mu.Unlock()
	return view, resolved, nil
}
