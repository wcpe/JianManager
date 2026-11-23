package logcoord

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 本文件验证「导出豁免投影版本漂移」在 CP 侧的置位边界：**只有导出路径置位**。
//
// 为什么必须双向断言：豁免是一个请求级授权标记，一旦列表/翻页路径也置位，就会重新打开
// 原设计要防的口子——前端旧 view_id 跨请求复用会静默读到跨代次数据
//（见 internal/controlplane/router/log_federation.go 的 2026-10-07 VIEW_STALE 复盘）。

func newDriftProbeCoord(t *testing.T) (*Coordinator, *fakeWorker) {
	t.Helper()
	resolver := &fakeTargetResolver{targets: []TargetInfo{
		{ID: "t1", WorkerID: "w1", Readiness: ReadyOnline},
	}}
	w1 := &fakeWorker{
		workerID:     "w1",
		searchItems:  []Event{mkEvent("e1", "s", "g", 1, "2026-09-20T12:00:00Z", "I", "hello")},
		searchTarget: []WorkerTargetResult{successTarget("t1", "w1", "s/g:1", "1")},
	}
	dialer := newFakeDialer()
	dialer.clients["w1"] = w1
	return New(resolver, dialer), w1
}

// 导出路径（自建视图）必须在 Worker 请求上置位豁免标记。
func TestExportSetsAllowProjectionDrift(t *testing.T) {
	coord, w1 := newDriftProbeCoord(t)

	res, err := coord.Export(context.Background(), Query{
		AuthorizedTargetIDs: []string{"t1"},
		PrincipalKey:        "user:1|role:10|targets:test",
		Budget:              QueryBudget{Limit: 100},
	})
	require.NoError(t, err)
	require.NotNil(t, res.Artifact)

	w1.mu.Lock()
	defer w1.mu.Unlock()
	require.Positive(t, w1.searchCalls)
	assert.True(t, w1.lastSearchDrift, "导出路径必须在 Worker 请求上置位豁免标记")
}

// 列表（Search）路径不得置位豁免标记——否则旧 view_id 复用会静默读到跨代次数据。
func TestSearchDoesNotSetAllowProjectionDrift(t *testing.T) {
	coord, w1 := newDriftProbeCoord(t)

	_, err := coord.Search(context.Background(), Query{
		AuthorizedTargetIDs: []string{"t1"},
		PrincipalKey:        "user:1|role:10|targets:test",
		Budget:              QueryBudget{Limit: 100},
	})
	require.NoError(t, err)

	w1.mu.Lock()
	defer w1.mu.Unlock()
	require.Positive(t, w1.searchCalls)
	assert.False(t, w1.lastSearchDrift, "列表路径不得置位豁免标记")
}

// 导出复用调用方传入的既有 viewId 时，同样必须置位（导出仍属「自我读完」语义）。
func TestExportWithExplicitViewIDSetsAllowProjectionDrift(t *testing.T) {
	coord, w1 := newDriftProbeCoord(t)

	q := Query{
		AuthorizedTargetIDs: []string{"t1"},
		PrincipalKey:        "user:1|role:10|targets:test",
		Budget:              QueryBudget{Limit: 50},
	}
	searchResp, err := coord.Search(context.Background(), q)
	require.NoError(t, err)
	require.NotNil(t, searchResp.View)

	exportQ := q
	exportQ.ViewID = searchResp.View.ViewID
	res, err := coord.Export(context.Background(), exportQ)
	require.NoError(t, err)
	require.NotNil(t, res.Artifact)

	w1.mu.Lock()
	defer w1.mu.Unlock()
	assert.True(t, w1.lastSearchDrift, "导出（含复用既有 viewId）必须置位豁免标记")
}

// 建视图（OpenView）路径不得置位：视图创建本身不读取内容，漂移豁免对它没有意义，
// 置位只会扩大豁免面。
func TestOpenViewDoesNotSetAllowProjectionDrift(t *testing.T) {
	coord, w1 := newDriftProbeCoord(t)

	_, err := coord.CreateView(context.Background(), Query{
		AuthorizedTargetIDs: []string{"t1"},
		PrincipalKey:        "user:1|role:10|targets:test",
		Budget:              QueryBudget{Limit: 100},
	})
	require.NoError(t, err)

	w1.mu.Lock()
	defer w1.mu.Unlock()
	require.Positive(t, w1.openViewCalls)
	assert.False(t, w1.lastOpenDrift, "OpenView 路径不得置位豁免标记")
}

// 同参缓存键必须含豁免标记：否则一份「带豁免」算出的响应会被不带豁免的调用命中，
// 把豁免静默扩散到导出之外的路径（Export 走 c.Search ⇒ 确实经本缓存读写）。
func TestSearchCacheKeySeparatesAllowProjectionDrift(t *testing.T) {
	base := Query{
		AuthorizedTargetIDs: []string{"t1"},
		PrincipalKey:        "user:1|role:10|targets:test",
		FromUTC:             "2026-09-20T00:00:00Z",
		ToUTC:               "2026-09-21T00:00:00Z",
		Budget:              QueryBudget{Limit: 100},
		ViewID:              "view-1",
		Cursor:              "cursor-1",
	}
	without := base
	with := base
	with.AllowProjectionDrift = true

	assert.NotEqual(t, searchCacheKey(without), searchCacheKey(with),
		"豁免标记必须参与缓存键，否则豁免会经缓存扩散到导出之外的路径")
}
