package logcoord

import "fmt"

func copyStringMap(src map[string]string) map[string]string {
	out := make(map[string]string, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

// cloneView 隔离注册表、读取任务与调用方，禁止对已公开快照的引用型字段就地写入。
func cloneView(view View) View {
	view.TargetIDs = append([]string(nil), view.TargetIDs...)
	view.AuthorizedIDs = append([]string(nil), view.AuthorizedIDs...)
	view.CoverageTargets = append([]TargetInfo(nil), view.CoverageTargets...)
	view.WorkerViewIDs = copyStringMap(view.WorkerViewIDs)
	view.WorkerViewErrors = copyStringMap(view.WorkerViewErrors)
	view.TargetAuthorizationIDs = copyStringMap(view.TargetAuthorizationIDs)
	view.ClosedVisibleSeq = copyStringMap(view.ClosedVisibleSeq)
	view.WorkerCatalogGenerations = copyStringMap(view.WorkerCatalogGenerations)
	return view
}

func cloneSearchResponse(response *SearchResponse) *SearchResponse {
	if response == nil {
		return nil
	}
	out := *response
	out.View = cloneView(response.View)
	out.Items = append([]Event(nil), response.Items...)
	out.IngestPositionGaps = append([]IngestPositionGap(nil), response.IngestPositionGaps...)
	out.Coverage.PartialReasons = append([]string(nil), response.Coverage.PartialReasons...)
	out.Coverage.Targets = append([]TargetCoverage(nil), response.Coverage.Targets...)
	for index := range out.Coverage.Targets {
		out.Coverage.Targets[index].Reasons = append([]string(nil), response.Coverage.Targets[index].Reasons...)
	}
	return &out
}

func pinTargetVector(view *View, targetID, seq, generation string) error {
	for _, value := range []struct {
		name string
		got  string
		vec  map[string]string
	}{
		{"closed_visible_seq", seq, view.ClosedVisibleSeq},
		{"catalog_generation", generation, view.WorkerCatalogGenerations},
	} {
		if want := value.vec[targetID]; want != "" && value.got != "" && want != value.got {
			return fmt.Errorf("%w: target %s %s changed from %q to %q", ErrViewReuseMismatch, targetID, value.name, want, value.got)
		}
	}
	if seq != "" {
		view.ClosedVisibleSeq[targetID] = seq
	}
	if generation != "" {
		view.WorkerCatalogGenerations[targetID] = generation
	}
	return nil
}

// publishSnapshotBindings 仅采用实际 Search 的引用与向量；失败路固定为不可续用，绝不补采最新快照。
func (c *Coordinator) publishSnapshotBindings(view *View, resolved *ResolvedTargets, bindings []WorkerSnapshotBinding) error {
	out := cloneView(*view)
	byWorker := make(map[string]WorkerSnapshotBinding, len(bindings))
	for _, binding := range bindings {
		byWorker[binding.WorkerID] = binding
	}
	order, targets := resolved.ByWorker()
	for _, workerID := range order {
		binding := byWorker[workerID]
		if binding.ViewID == "" {
			out.WorkerViewErrors[workerID] = firstNonEmpty(binding.Error, "worker snapshot is not ready")
			continue
		}
		out.WorkerViewIDs[workerID] = binding.ViewID
		got := make(map[string]WorkerTargetResult, len(binding.Targets))
		for _, result := range binding.Targets {
			got[result.TargetID] = result
		}
		for _, target := range targets[workerID] {
			result := got[workerTargetID(target)]
			if err := pinTargetVector(&out, target.ID, result.ClosedVisibleSeq, result.CatalogGeneration); err != nil {
				return err
			}
		}
	}
	c.mu.Lock()
	c.pruneViewsLocked()
	c.views[out.ViewID] = cloneView(out)
	c.mu.Unlock()
	*view = out
	return nil
}

// inheritCachedSnapshot 换 CP 身份时仍继承产生缓存页的相同 Worker 引用，不重新 OpenView。
func (c *Coordinator) inheritCachedSnapshot(view *View, resolved *ResolvedTargets, cached *SearchResponse) bool {
	if cached == nil || targetsDigest(view.TargetIDs) != targetsDigest(cached.View.TargetIDs) {
		return false
	}
	coverage := make(map[string]TargetCoverage, len(cached.Coverage.Targets))
	for _, target := range cached.Coverage.Targets {
		coverage[target.TargetID] = target
	}
	for _, target := range resolved.Queried {
		if old, ok := coverage[target.ID]; !ok || old.WorkerID != target.WorkerID {
			return false
		}
	}
	out := cloneView(*view)
	out.CoverageTargets = append([]TargetInfo(nil), cached.View.CoverageTargets...)
	out.TargetAuthorizationIDs = copyStringMap(cached.View.TargetAuthorizationIDs)
	out.WorkerViewIDs = copyStringMap(cached.View.WorkerViewIDs)
	out.WorkerViewErrors = copyStringMap(cached.View.WorkerViewErrors)
	out.ClosedVisibleSeq = copyStringMap(cached.View.ClosedVisibleSeq)
	out.WorkerCatalogGenerations = copyStringMap(cached.View.WorkerCatalogGenerations)
	c.mu.Lock()
	c.pruneViewsLocked()
	c.views[out.ViewID] = cloneView(out)
	c.mu.Unlock()
	*view = out
	return true
}
