package router

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakePkgWorker 假 Worker：全局包三 RPC（FR-307）。
type fakePkgWorker struct {
	workerpb.WorkerServiceClient
	installReq *workerpb.InstallGlobalPackageRequest
	removedPkg string
}

func (f *fakePkgWorker) ListGlobalPackages(context.Context, *workerpb.ListGlobalPackagesRequest, ...grpc.CallOption) (*workerpb.ListGlobalPackagesResponse, error) {
	return &workerpb.ListGlobalPackagesResponse{Success: true, Packages: []*workerpb.GlobalPackage{
		{Name: "mineflayer", Version: "4.20.0", Latest: "4.21.0"},
	}}, nil
}

func (f *fakePkgWorker) InstallGlobalPackage(_ context.Context, req *workerpb.InstallGlobalPackageRequest, _ ...grpc.CallOption) (*workerpb.InstallGlobalPackageResponse, error) {
	f.installReq = req
	return &workerpb.InstallGlobalPackageResponse{Success: true, TaskId: req.TaskId}, nil
}

func (f *fakePkgWorker) RemoveGlobalPackage(_ context.Context, req *workerpb.RemoveGlobalPackageRequest, _ ...grpc.CallOption) (*workerpb.RemoveGlobalPackageResponse, error) {
	f.removedPkg = req.Name
	return &workerpb.RemoveGlobalPackageResponse{Success: true}, nil
}

// TestFR307GlobalPackagesEndpoints 列表代理 / 异步安装 202+taskId / 卸载（含 @scope 名经 query）。
func TestFR307GlobalPackagesEndpoints(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupFR033Router(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	fake := &fakePkgWorker{}
	pool.SetWorkerClientForTest(node.UUID, fake)

	// 列表：代理 Worker，含 pm 与可更新标记。
	list := makeRequest(r, http.MethodGet, fmt.Sprintf("/api/v1/nodes/%d/packages", node.ID), nil, adminToken)
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	require.Contains(t, list.Body.String(), `"mineflayer"`)
	require.Contains(t, list.Body.String(), `"4.21.0"`)

	// 安装：202 + taskId，PM 取节点配置（默认 npm）、task_id 下发 Worker。
	inst := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/packages", node.ID), map[string]any{
		"name": "prismarine-viewer",
	}, adminToken)
	require.Equal(t, http.StatusAccepted, inst.Code, inst.Body.String())
	require.NotNil(t, fake.installReq)
	require.Equal(t, "prismarine-viewer", fake.installReq.Name)
	require.Equal(t, "npm", fake.installReq.Pm)
	require.NotEmpty(t, fake.installReq.TaskId)

	// 卸载：@scope 包名经 query 传递不破路由。
	del := makeRequest(r, http.MethodDelete, fmt.Sprintf("/api/v1/nodes/%d/packages?name=%s", node.ID, "@scope%2Fpkg"), nil, adminToken)
	require.Equal(t, http.StatusOK, del.Code, del.Body.String())
	require.Equal(t, "@scope/pkg", fake.removedPkg)

	_ = service.GlobalPackageView{} // 视图类型编译期引用
}
