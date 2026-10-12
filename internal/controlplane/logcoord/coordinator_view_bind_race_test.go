package logcoord

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// 回归（B-1，2026-10-12 修复）：复用同一 viewId 的**并发**查询不得对共享 View 的 map 做无锁写。
//
// 缺陷形态：`bindExistingView` 按值接收 `view View`，但 `View.WorkerViewIDs` /
// `WorkerViewErrors` 是 **map（引用类型）**——按值传只复制了 map 头，底层仍是同一张表。
// 当视图处于「已登记但未绑定」形态时（`len(WorkerViewIDs)==0 && len(WorkerViewErrors)==0`），
// **每次**复用请求都会走到 `bindWorkerViews` 并 `view.WorkerViewIDs[k]=v` 写同一张共享 map。
//
// 「已登记但未绑定」是**生产可达**形态：首屏快路径（`q.Fast=true`）经 `resolveForQueryFast`
// 的 newViewBranch 登记视图（`WorkerViewIDs` 已 make 但为空）并把 viewId 返回给浏览器；
// 浏览器的分页/翻页/多组件扇出带着该 viewId 回来。两个请求在 OpenView 往返窗口内重叠即写同一 map。
//
// 后果不是「返回错误」而是 Go 运行时 `fatal error: concurrent map writes` **直接终止 CP 进程**
// —— gin.Recovery() 兜不住 fatal，全部 API 不可用、在线实例失联，需人工重启。
//
// 转红方式（实测）：把 `bindExistingView` 的 map 写改回「直接在传入 view 上写」
// （即去掉深拷贝，`c.bindWorkerViews(ctx, &view, resolved)` 原样写共享 map）⇒
// 本用例在 `-race` 下报 `DATA RACE`，在普通模式下报 `fatal error: concurrent map writes`。
func TestConcurrentViewReuseDoesNotWriteSharedViewMaps(t *testing.T) {
	w := &pr61Worker{
		fakeWorker: &fakeWorker{workerID: "w1"},
		searchPB: &workerpb.LogSearchResponse{
			Coverage:  pr61Coverage(false),
			Exhausted: true,
			Quality: &workerpb.LogQuality{
				DuplicateQuality: workerpb.LogDuplicateQuality_LOG_QUALITY_DUPLICATE_EXACT,
				StatsQuality:     workerpb.LogStatsQuality_LOG_QUALITY_STATS_EXACT,
			},
		},
	}
	c := pr61Coord(map[string]*pr61Worker{"w1": w})

	// 造出「已登记但未绑定」的视图：Fast 路径登记视图但不绑 Worker。
	q := pr61Query()
	q.Fast = true
	created, err := c.Search(context.Background(), q)
	require.NoError(t, err)
	require.NotEmpty(t, created.View.ViewID, "前置：fast 路径必须登记视图并返回 viewId")

	// 前置核实：该视图确实是「未绑定」形态（WorkerViewIDs 为空但 map 非 nil）。
	c.mu.Lock()
	stored, ok := c.views[created.View.ViewID]
	c.mu.Unlock()
	require.True(t, ok, "前置：视图必须已登记")
	require.Empty(t, stored.WorkerViewIDs, "前置：快路径视图应为未绑定形态")
	require.NotNil(t, stored.WorkerViewIDs, "前置：map 必须已初始化（否则走 make 分支，测不到共享写）")

	// 并发复用同一 viewId（走慢路径 ⇒ 每次都会 bindExistingView ⇒ bindWorkerViews）。
	const goroutines = 8
	const rounds = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				_, _ = c.Search(context.Background(), Query{
					AuthorizedTargetIDs: []string{"inst:1"},
					PrincipalKey:        "user:1",
					PermissionScope:     "scope:1",
					FromUTC:             "2026-10-08T00:00:00Z",
					ToUTC:               "2026-10-09T00:00:00Z",
					Budget:              QueryBudget{Limit: 10, MaxBytes: 10000},
					ViewID:              created.View.ViewID,
				})
			}
		}()
	}
	wg.Wait()

	// 收尾：视图仍可用（若被并发写坏，此处失败或已被运行时终止）。
	final, err := c.Search(context.Background(), Query{
		AuthorizedTargetIDs: []string{"inst:1"},
		PrincipalKey:        "user:1",
		PermissionScope:     "scope:1",
		FromUTC:             "2026-10-08T00:00:00Z",
		ToUTC:               "2026-10-09T00:00:00Z",
		Budget:              QueryBudget{Limit: 10, MaxBytes: 10000},
		ViewID:              created.View.ViewID,
	})
	require.NoError(t, err)
	require.NotEmpty(t, final.View.ViewID)
}
