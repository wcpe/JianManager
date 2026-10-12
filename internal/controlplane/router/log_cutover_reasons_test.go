package router

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 事故对照（2026-09-30 生产）：cutover 启用被拒时 CP 只回两个布尔，而 Worker 响应里
// 本已带的具体原因（unresolved_gaps / acquire_paused / durable_behind_read / ...）
// 在 CP 侧被丢弃 → 为「为何按不下切换」反复盲猜数小时。以下两个用例锁死「原因必须转述」：
//
//	① 适配器层：Worker 的 reasons 必须进入 watermark；
//	② 路由层：未就绪 400 响应必须携带 reasons 数组（排障现场）。
//
// 任一转述被移除，对应用例即转红。
type cutoverReadinessBlockedWorker struct{ workerpb.WorkerServiceClient }

func (cutoverReadinessBlockedWorker) LogCutoverReadiness(context.Context, *workerpb.LogCutoverReadinessRequest, ...grpc.CallOption) (*workerpb.LogCutoverReadinessResponse, error) {
	return &workerpb.LogCutoverReadinessResponse{
		CapabilityConfirmed: true,
		LedgerReady:         false,
		Reasons: []string{
			"inst:9/file/instance:x@worker:y:unresolved_gaps",
			"inst:9/file/instance:x@worker:y:acquire_paused",
		},
	}, nil
}

func TestNodeWorkerUUIDListerSurfacesWorkerReasons(t *testing.T) {
	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest("worker-blocked", cutoverReadinessBlockedWorker{})
	lister := nodeWorkerUUIDLister{svcs: &Services{LogRuntimePool: pool}}
	mark, err := lister.CutoverReadiness(context.Background(), "worker-blocked")
	require.NoError(t, err)
	require.False(t, mark.LedgerReady)
	assert.Contains(t, fmt.Sprint(mark.Reasons), "unresolved_gaps",
		"Worker 的具体未就绪原因必须被转述，不能只剩布尔")
	assert.Contains(t, fmt.Sprint(mark.Reasons), "acquire_paused")
}

func TestLogCutover_Put_WatermarkRejectionSurfacesReasons(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)

	w := makeRequest(r, "PUT", "/api/v1/logs/cutover", map[string]interface{}{
		"enabled": true,
		"workerWatermarks": []map[string]interface{}{
			{"workerUuid": "w-blocked", "capabilityConfirmed": true, "ledgerReady": false, "apply": true,
				"reasons": []string{"inst:9/file/instance:x@worker:y:unresolved_gaps", "inst:9/file/instance:x@worker:y:acquire_paused"}},
		},
	}, adminToken)
	require.Equal(t, http.StatusBadRequest, w.Code)
	m := parseJSON(t, w)
	require.Equal(t, "INVALID_REQUEST", m["error"])
	reasons, ok := m["reasons"].([]interface{})
	require.True(t, ok, "400 响应必须携带 reasons 数组（排障现场）")
	assert.Contains(t, fmt.Sprint(reasons), "unresolved_gaps")
	assert.Contains(t, fmt.Sprint(reasons), "acquire_paused")
}
