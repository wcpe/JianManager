package logcoord

import (
	"context"
	"time"
)

// resolveForQueryFast 只在首次读取省去 OpenView；视图必须等实际读取快照汇总后才能发布。
// 复用仍执行与慢路径相同的主体、范围、过滤和权限校验，不得静默换成最新集合。
func (c *Coordinator) resolveForQueryFast(ctx context.Context, q Query) (View, *ResolvedTargets, error) {
	if q.ViewID != "" {
		return c.resolveForQuery(ctx, q)
	}
	if err := ValidateQuery(q); err != nil {
		return View{}, nil, err
	}
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
		CoverageTargets:          append([]TargetInfo(nil), resolved.All...),
		PrincipalKey:             q.PrincipalKey,
		OnlineOnly:               q.OnlineOnly,
		Budget:                   q.Budget,
		Filter:                   q.Filter,
		PermissionScope:          q.PermissionScope,
		WorkerViewIDs:            make(map[string]string),
		WorkerViewErrors:         make(map[string]string),
		WorkerCatalogGenerations: make(map[string]string),
		ClosedVisibleSeq:         make(map[string]string),
		TargetAuthorizationIDs:   make(map[string]string),
		CreatedAt:                time.Now().UTC(),
	}
	for _, target := range resolved.All {
		if target.AuthorizationID != "" {
			view.TargetAuthorizationIDs[target.ID] = target.AuthorizationID
		}
	}
	return view, resolved, nil
}
