package router

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakeFR301Worker 假 Worker：ListJDKs 回预置清单（refresh 强制同步路径）。
type fakeFR301Worker struct {
	workerpb.WorkerServiceClient
	jdks []*workerpb.JDKInfo
}

func (f *fakeFR301Worker) ListJDKs(context.Context, *workerpb.ListJDKsRequest, ...grpc.CallOption) (*workerpb.ListJDKsResponse, error) {
	return &workerpb.ListJDKsResponse{Jdks: f.jdks}, nil
}

// refreshOutcome 反序列化 POST /runtime-assets/refresh 载荷。
type refreshOutcome struct {
	Results []struct {
		NodeID   uint    `json:"nodeId"`
		NodeName string  `json:"nodeName"`
		OK       bool    `json:"ok"`
		Error    string  `json:"error"`
		SyncedAt *string `json:"syncedAt"`
	} `json:"results"`
	SyncedAt *string `json:"syncedAt"`
}

// POST /runtime-assets/refresh：平台管理员强制全节点同步，失败容忍逐节点回报，写审计。
func TestFR301RuntimeAssetsRefresh(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupTestRouterWithPool(db, pool)
	adminToken := getAdminToken(t, r)
	memberToken := getMemberToken(t, r, "member-fr301", "password123")

	online := createTestNode(t, db)
	offline := createTestNodeWithSuffix(t, db, "fr301-offline")
	pool.SetWorkerClientForTest(online.UUID, &fakeFR301Worker{jdks: []*workerpb.JDKInfo{
		{Vendor: "Temurin", MajorVersion: 21, Version: "21.0.4+9", Arch: "x64", Path: "/opt/jdks/temurin-21", Managed: true},
	}})

	// 非平台管理员 403。
	forbidden := makeRequest(r, http.MethodPost, "/api/v1/runtime-assets/refresh", nil, memberToken)
	require.Equal(t, http.StatusForbidden, forbidden.Code)

	resp := makeRequest(r, http.MethodPost, "/api/v1/runtime-assets/refresh", nil, adminToken)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())

	var out refreshOutcome
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &out))
	require.Len(t, out.Results, 2)
	byNode := map[uint]int{}
	for i, res := range out.Results {
		byNode[res.NodeID] = i
	}
	okRes := out.Results[byNode[online.ID]]
	require.True(t, okRes.OK, resp.Body.String())
	require.NotNil(t, okRes.SyncedAt)
	failRes := out.Results[byNode[offline.ID]]
	require.False(t, failRes.OK)
	require.Contains(t, failRes.Error, "节点未连接")
	require.NotNil(t, out.SyncedAt)

	// 同步副作用：在线节点的 Worker 清单已入库。
	var count int64
	require.NoError(t, db.Model(&model.NodeJDK{}).Where("node_id = ?", online.ID).Count(&count).Error)
	require.Equal(t, int64(1), count)

	// 写审计（action runtime_assets.refresh）。
	var audits int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ?", "runtime_assets.refresh").Count(&audits).Error)
	require.GreaterOrEqual(t, audits, int64(1))
}

// GET /runtime-assets/overview：加性扩展 runtimes / runtimeSyncs / syncedAt，老字段不动。
func TestFR301OverviewRuntimesFields(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)

	node := createTestNode(t, db)
	require.NoError(t, db.Create(&model.NodeJDK{NodeID: node.ID, Vendor: "Temurin", MajorVersion: 21, Version: "21.0.4+9", Arch: "x64", Path: "/opt/jdks/temurin-21"}).Error)
	require.NoError(t, db.Create(&model.NodeRuntime{NodeID: node.ID, Type: "nodejs", Name: "Node.js 22", Version: "22.17.0", Major: 22, Arch: "x64", Path: "/usr/local/bin/node"}).Error)

	resp := makeRequest(r, http.MethodGet, "/api/v1/runtime-assets/overview", nil, adminToken)
	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())

	var body struct {
		JDKs     []map[string]any `json:"jdks"`
		Runtimes []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"runtimes"`
		RuntimeSyncs []struct {
			NodeID   uint    `json:"nodeId"`
			SyncedAt *string `json:"syncedAt"`
		} `json:"runtimeSyncs"`
		SyncedAt *string `json:"syncedAt"`
	}
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &body))
	require.Len(t, body.JDKs, 1, "老字段 jdks 不受影响")
	require.Len(t, body.Runtimes, 2)
	types := map[string]string{}
	for _, rt := range body.Runtimes {
		types[rt.Type] = rt.Name
	}
	require.Equal(t, "Temurin", types["jdk"])
	require.Equal(t, "Node.js 22", types["nodejs"])
	require.Len(t, body.RuntimeSyncs, 1)
	require.Equal(t, node.ID, body.RuntimeSyncs[0].NodeID)
	require.Nil(t, body.RuntimeSyncs[0].SyncedAt, "从未同步节点 syncedAt 为 null")
	require.Nil(t, body.SyncedAt)
}
