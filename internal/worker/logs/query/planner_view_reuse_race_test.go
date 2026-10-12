package query

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// F4 核查（2026-10-12 终审）：**视图复用路径是否真的在 p.mu 之外读 view 的 map**？
//
// 上一轮子代理报告（F4）称：`Plan` 从 `p.views` 取出 `view` 指针后立刻 Unlock，
// 而 `planRangesForView` 遍历 `view.TargetIDs` 并读 `view.TargetCoverage` 两个 map；
// 一旦有人在复用路径写这两个容器就是 `fatal error: concurrent map writes`，
// 属「已埋好引信」型缺陷。
//
// **核查结论（本用例即证据）**：引信不成立——视图一经登记即**不可变**：
//   - 所有写入点（planner.go 的 `view.TargetIDs = append(...)` 与 `view.TargetCoverage[...] = ...`）
//     全部位于**创建路径**，且执行顺序上都在 `p.views[view.ViewID] = view`（登记）**之前**；
//   - 登记后唯一触及视图的函数 `validateViewAgainstCatalog` 只读，`pruneViewsLocked` 只做
//     `delete(p.views, id)`（从 map 移除条目，不改视图内容）；
//   - 全仓 grep `*.TargetIDs =` / `*.TargetCoverage =` 等字段写入仅命中创建路径两处。
//
// 本用例用**高并发混合负载**（同一视图被多个 goroutine 反复复用，同时不断创建新视图触发
// `pruneViewsLocked` 淘汰）在 `-race` 下跑，若无报告即证明「登记后只读」这条不变量成立。
//
// 转红方式（实测，属**假设性**验证——把不变量破坏掉看本用例是否红）：在 `planRangesForView`
// 里对 `view.TargetCoverage` 做一次写入（例如 `view.TargetCoverage["__probe__"] = TargetCoverage{}`）
// ⇒ `-race` 下立即报 `concurrent map writes`，本用例红。
func TestPlannerConcurrentViewReuseIsRaceFree(t *testing.T) {
	cat := catalog.New(nil)
	// 造多个分区，使视图有实质内容（多个 target 进 TargetIDs/TargetCoverage）。
	const targets = 8
	targetIDs := make([]string, 0, targets)
	for i := 0; i < targets; i++ {
		ns := fmt.Sprintf("inst:%d", i)
		targetIDs = append(targetIDs, ns)
		require.NoError(t, cat.Put(catalog.NewStableRecord(
			catalog.PartitionKey{StorageNamespace: ns, UTCDay: "2026-10-11"}, catalog.OwnerHot, 1, "hot-"+ns)))
	}
	// 另造两个「窗口内无分区」的授权目标（覆盖空成功覆盖路径，使 TargetCoverage 更满）。
	for i := 100; i < 102; i++ {
		ns := fmt.Sprintf("inst:%d", i)
		targetIDs = append(targetIDs, ns)
		require.NoError(t, cat.Put(catalog.NewStableRecord(
			catalog.PartitionKey{StorageNamespace: ns, UTCDay: "2026-09-01"}, catalog.OwnerHot, 1, "hot-"+ns)))
	}

	tr := TimeRange{FromUTC: "2026-10-11T00:00:00Z", ToUTC: "2026-10-12T00:00:00Z"}
	p := NewPlanner(cat, nil)

	// 建一个固定视图供高并发复用。
	created := p.Plan(PlanRequest{RequestID: "f4-base", TargetIDs: targetIDs, TimeRange: tr})
	require.Nil(t, created.Err)
	require.NotEmpty(t, created.View.ViewID)
	viewRef := &ViewRef{ViewID: created.View.ViewID, OrderVersion: SortVersion}

	const goroutines = 16
	const rounds = 40
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				if id%3 == 0 {
					// 一部分 goroutine 持续**创建新视图**（触发登记与 pruneViewsLocked 淘汰）。
					_ = p.Plan(PlanRequest{
						RequestID: fmt.Sprintf("f4-new-%d-%d", id, r),
						TargetIDs: targetIDs, TimeRange: tr,
					})
					continue
				}
				// 其余 goroutine 持续**复用同一视图**（遍历 view.TargetIDs、读 view.TargetCoverage）。
				res := p.Plan(PlanRequest{
					RequestID: fmt.Sprintf("f4-reuse-%d-%d", id, r),
					TargetIDs: targetIDs, TimeRange: tr, ViewRef: viewRef,
				})
				// 允许 VIEW_STALE（并发淘汰/校验），但绝不能 panic 或数据竞争。
				_ = res
			}
		}(g)
	}
	wg.Wait()

	// 收尾：复用路径仍应可用（视图未被并发写坏）。
	final := p.Plan(PlanRequest{RequestID: "f4-final", TargetIDs: targetIDs, TimeRange: tr, ViewRef: viewRef})
	require.Nil(t, final.Err, "高并发后视图仍必须可复用（若被并发写坏，此处会失败或已被 -race 捕获）")
	require.NotEmpty(t, final.Coverage.Targets)
}
