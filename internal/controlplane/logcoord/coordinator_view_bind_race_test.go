package logcoord

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// 并发复用必须读取首屏已经绑定的同一快照，不重建 Worker 视图，也不修改共享 map。
// 原先空绑定再 OpenView 的交错既存在共享写竞态，也会把并发请求绑定到不同逻辑集合。
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

	// 首屏只有采用真实 Worker 引用后才允许公开 CP 视图。
	q := pr61Query()
	q.Fast = true
	created, err := c.Search(context.Background(), q)
	require.NoError(t, err)
	require.NotEmpty(t, created.View.ViewID, "前置：fast 路径必须登记视图并返回 viewId")

	// 前置核实：注册表已原子保存与首屏完全相同的 Worker 引用。
	c.mu.Lock()
	stored, ok := c.views[created.View.ViewID]
	c.mu.Unlock()
	require.True(t, ok, "前置：视图必须已登记")
	require.NotEmpty(t, stored.WorkerViewIDs)
	require.Equal(t, created.View.WorkerViewIDs, stored.WorkerViewIDs)

	// 并发复用不能重采快照，保留原强度与轮数检测共享状态竞态。
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
	require.Equal(t, created.View.WorkerViewIDs, final.View.WorkerViewIDs)
	require.Zero(t, w.openViewCalls, "并发复用不得通过 OpenView 补采新快照")
}
