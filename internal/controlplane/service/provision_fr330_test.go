package service

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// cacheHitProvisionWorker 核心缓存命中的 worker 替身（FR-330）：记录请求并回 cache_hit。
type cacheHitProvisionWorker struct {
	fakeProvisionWorker
	download *workerpb.DownloadCoreRequest
}

func (f *cacheHitProvisionWorker) DownloadCore(_ context.Context, in *workerpb.DownloadCoreRequest, _ ...grpc.CallOption) (*workerpb.DownloadCoreResponse, error) {
	f.download = in
	return &workerpb.DownloadCoreResponse{Success: true, Size: 47 << 20, CacheHit: true}, nil
}

// TestProvisionServerAsync_CacheHitStage 缓存命中链路（FR-330）：
// ① DownloadCore 请求携带组合缓存键成分（CoreType/McVersion/Build，latest 已在 CP 解析为具体构建）；
// ② worker 回 cache_hit 时任务 stage 轨迹出现「缓存命中」文案（与「下载」区分）。
func TestProvisionServerAsync_CacheHitStage(t *testing.T) {
	worker := &cacheHitProvisionWorker{}
	svc, taskSvc, node, done := newFR319Harness(t, worker)
	defer done()

	_, taskID, err := svc.ProvisionServerAsync(context.Background(), ProvisionServerRequest{
		NodeID: node.ID, Name: "cache-hit", CoreType: "spongevanilla", MCVersion: "1.21.1", MemoryMb: 1024,
	}, 1)
	require.NoError(t, err)

	task := waitTaskTerminal(t, taskSvc, taskID)
	require.Equal(t, model.TaskStateSucceeded, task.State)

	// ① 组合键成分已下发（假源仅有 1.21.1-12.0.4-RC2665 → build 2665）。
	require.NotNil(t, worker.download)
	require.Equal(t, "spongevanilla", worker.download.CoreType)
	require.Equal(t, "1.21.1", worker.download.McVersion)
	require.Equal(t, int32(2665), worker.download.Build)

	// ② stage 轨迹含「缓存命中」。
	require.True(t, stageLogContains(t, taskSvc, taskID, "缓存命中"),
		"cache_hit 时任务 stage 轨迹应出现「缓存命中」文案")
}

// TestProvisionServerAsync_DownloadStage 未命中链路（FR-330 对照）：worker 未回 cache_hit 时
// stage 轨迹是「下载核心…下载完成」，不得出现「缓存命中」。
func TestProvisionServerAsync_DownloadStage(t *testing.T) {
	svc, taskSvc, node, done := newFR319Harness(t, &fakeProvisionWorker{})
	defer done()

	_, taskID, err := svc.ProvisionServerAsync(context.Background(), ProvisionServerRequest{
		NodeID: node.ID, Name: "cache-miss", CoreType: "spongevanilla", MCVersion: "1.21.1", MemoryMb: 1024,
	}, 1)
	require.NoError(t, err)

	task := waitTaskTerminal(t, taskSvc, taskID)
	require.Equal(t, model.TaskStateSucceeded, task.State)

	require.True(t, stageLogContains(t, taskSvc, taskID, "下载核心"), "未命中应有下载中 stage")
	require.True(t, stageLogContains(t, taskSvc, taskID, "核心下载完成"), "未命中应有下载完成 stage")
	require.False(t, stageLogContains(t, taskSvc, taskID, "缓存命中"), "未命中不得谎报缓存命中")
}

// stageLogContains 查任务 TaskLog 轨迹中是否有包含 substr 的阶段行。
func stageLogContains(t *testing.T, taskSvc *TaskService, taskID, substr string) bool {
	t.Helper()
	var lines []model.TaskLog
	require.NoError(t, taskSvc.db.Where("task_id = ?", taskID).Find(&lines).Error)
	for _, l := range lines {
		if strings.Contains(l.Line, substr) {
			return true
		}
	}
	return false
}
