package router

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakeFR298RuntimeWorker 假 Worker：ScanRuntimes 回预置候选，ListJDKs 回空（同步无副作用）。
type fakeFR298RuntimeWorker struct {
	workerpb.WorkerServiceClient
	scanReq    *workerpb.ScanRuntimesRequest
	candidates []*workerpb.RuntimeCandidate
}

func (f *fakeFR298RuntimeWorker) ScanRuntimes(_ context.Context, req *workerpb.ScanRuntimesRequest, _ ...grpc.CallOption) (*workerpb.ScanRuntimesResponse, error) {
	f.scanReq = req
	return &workerpb.ScanRuntimesResponse{Candidates: f.candidates}, nil
}

func (f *fakeFR298RuntimeWorker) ListJDKs(context.Context, *workerpb.ListJDKsRequest, ...grpc.CallOption) (*workerpb.ListJDKsResponse, error) {
	return &workerpb.ListJDKsResponse{}, nil
}

func setupFR298Router(t *testing.T, db *gorm.DB, pool *cpgrpc.ClientPool) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	jwtCfg := config.JWTConfig{Secret: "test-secret-key-for-fr298", AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour}
	groupSvc := service.NewGroupService(db)
	authzSvc := service.NewAuthzService(db)
	instanceSvc := service.NewInstanceService(db, groupSvc, pool)
	instanceSvc.Shutdown()
	nodeSvc := service.NewNodeService(db)
	nodeSvc.SetInstanceService(instanceSvc)
	jdkSvc := service.NewJDKService(db, pool)
	runtimeLibSvc := service.NewRuntimeLibraryService(db, pool, jdkSvc)

	svcs := &Services{
		Auth:           service.NewAuthService(db, jwtCfg),
		User:           service.NewUserService(db),
		Group:          groupSvc,
		Node:           nodeSvc,
		JDK:            jdkSvc,
		RuntimeLibrary: runtimeLibSvc,
		Instance:       instanceSvc,
		InstanceBatch:  service.NewInstanceBatchService(db, pool),
		Audit:          service.NewAuditService(db),
		Authz:          authzSvc,
	}
	return Setup(svcs, jwtCfg.Secret)
}

// runtimeViewRow 反序列化统一 Runtime 视图行。
type runtimeViewRow struct {
	ID           uint   `json:"id"`
	NodeID       uint   `json:"nodeId"`
	Type         string `json:"type"`
	Name         string `json:"name"`
	MajorVersion int    `json:"majorVersion"`
	Version      string `json:"version"`
	Arch         string `json:"arch"`
	Path         string `json:"path"`
	Managed      bool   `json:"managed"`
}

func TestFR298RuntimeScanEndpoint(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupFR298Router(t, db, pool)
	adminToken := getAdminToken(t, r)
	memberToken := getMemberToken(t, r, "member-fr298", "password123")
	node := createTestNode(t, db)
	fake := &fakeFR298RuntimeWorker{candidates: []*workerpb.RuntimeCandidate{
		{Type: "jdk", Vendor: "Temurin", Version: "21.0.4+9", MajorVersion: 21, Arch: "x64", Path: "/usr/lib/jvm/temurin-21"},
		{Type: "nodejs", Vendor: "Node.js", Version: "22.17.0", MajorVersion: 22, Arch: "x64", Path: "/usr/local/bin/node"},
	}}
	pool.SetWorkerClientForTest(node.UUID, fake)

	// 非平台管理员 403。
	forbidden := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/runtimes/scan", node.ID), map[string]any{}, memberToken)
	require.Equal(t, http.StatusForbidden, forbidden.Code)

	// 预登记一条与候选同路径的 JDK：CP 侧应按 DB 记录补标 alreadyRegistered。
	require.NoError(t, db.Create(&model.NodeJDK{NodeID: node.ID, Vendor: "Temurin", MajorVersion: 21, Version: "21.0.4+9", Arch: "x64", Path: "/usr/lib/jvm/temurin-21"}).Error)

	scanResp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/runtimes/scan", node.ID), map[string]any{
		"types": []string{"jdk", "nodejs"},
	}, adminToken)
	require.Equal(t, http.StatusOK, scanResp.Code, scanResp.Body.String())
	require.NotNil(t, fake.scanReq)
	require.Equal(t, []string{"jdk", "nodejs"}, fake.scanReq.Types)

	var scanBody struct {
		Candidates []struct {
			Type              string `json:"type"`
			Vendor            string `json:"vendor"`
			Path              string `json:"path"`
			MajorVersion      int    `json:"majorVersion"`
			AlreadyRegistered bool   `json:"alreadyRegistered"`
		} `json:"candidates"`
	}
	require.NoError(t, json.Unmarshal(scanResp.Body.Bytes(), &scanBody))
	require.Len(t, scanBody.Candidates, 2)
	byPath := map[string]bool{}
	for _, c := range scanBody.Candidates {
		byPath[c.Path] = c.AlreadyRegistered
	}
	require.True(t, byPath["/usr/lib/jvm/temurin-21"], "已在库路径应补标 alreadyRegistered")
	require.False(t, byPath["/usr/local/bin/node"])

	// 未知扫描类型 422。
	badType := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/runtimes/scan", node.ID), map[string]any{
		"types": []string{"ruby"},
	}, adminToken)
	require.Equal(t, http.StatusUnprocessableEntity, badType.Code, badType.Body.String())

	// 节点离线 503。
	offline := createTestNodeWithSuffix(t, db, "fr298-offline")
	offlineResp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/runtimes/scan", offline.ID), map[string]any{}, adminToken)
	require.Equal(t, http.StatusServiceUnavailable, offlineResp.Code, offlineResp.Body.String())

	// 扫描写审计。
	var scanAudits int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ?", "node.runtime.scan").Count(&scanAudits).Error)
	require.GreaterOrEqual(t, scanAudits, int64(1))
}

func TestFR298RuntimeRegisterListDelete(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupFR298Router(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	pool.SetWorkerClientForTest(node.UUID, &fakeFR298RuntimeWorker{})

	// 登记 type=jdk：转发现有 JDK 登记链路（落 node_jdks）。
	jdkResp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/runtimes", node.ID), map[string]any{
		"type": "jdk", "vendor": "Temurin", "majorVersion": 21, "version": "21.0.4+9", "arch": "x64", "path": "/opt/jdks/temurin-21",
	}, adminToken)
	require.Equal(t, http.StatusCreated, jdkResp.Code, jdkResp.Body.String())
	var jdkCount int64
	require.NoError(t, db.Model(&model.NodeJDK{}).Where("node_id = ? AND path = ?", node.ID, "/opt/jdks/temurin-21").Count(&jdkCount).Error)
	require.Equal(t, int64(1), jdkCount)

	// 登记 type=nodejs：落新表 node_runtimes。
	nodeResp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/runtimes", node.ID), map[string]any{
		"type": "nodejs", "name": "Node.js 22", "majorVersion": 22, "version": "22.17.0", "arch": "x64", "path": "/usr/local/bin/node",
	}, adminToken)
	require.Equal(t, http.StatusCreated, nodeResp.Code, nodeResp.Body.String())
	var created runtimeViewRow
	require.NoError(t, json.Unmarshal(nodeResp.Body.Bytes(), &created))
	require.Equal(t, "nodejs", created.Type)
	require.Equal(t, node.ID, created.NodeID)
	var rtCount int64
	require.NoError(t, db.Model(&model.NodeRuntime{}).Where("node_id = ? AND type = ? AND path = ?", node.ID, "nodejs", "/usr/local/bin/node").Count(&rtCount).Error)
	require.Equal(t, int64(1), rtCount)

	// 同 (node,type,path) 重复登记拒绝。
	dup := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/runtimes", node.ID), map[string]any{
		"type": "nodejs", "name": "Node.js 22", "majorVersion": 22, "version": "22.17.0", "arch": "x64", "path": "/usr/local/bin/node",
	}, adminToken)
	require.Equal(t, http.StatusUnprocessableEntity, dup.Code, dup.Body.String())

	// 未知类型 422。
	badType := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/runtimes", node.ID), map[string]any{
		"type": "ruby", "version": "3.3.0", "path": "/usr/bin/ruby",
	}, adminToken)
	require.Equal(t, http.StatusUnprocessableEntity, badType.Code, badType.Body.String())

	// 统一视图：node_jdks(type=jdk) + node_runtimes 读侧拼装。
	listResp := makeRequest(r, http.MethodGet, fmt.Sprintf("/api/v1/nodes/%d/runtimes", node.ID), nil, adminToken)
	require.Equal(t, http.StatusOK, listResp.Code, listResp.Body.String())
	var rows []runtimeViewRow
	require.NoError(t, json.Unmarshal(listResp.Body.Bytes(), &rows))
	require.Len(t, rows, 2)
	types := map[string]runtimeViewRow{}
	for _, row := range rows {
		types[row.Type] = row
	}
	require.Equal(t, "Temurin", types["jdk"].Name)
	require.Equal(t, 21, types["jdk"].MajorVersion)
	require.Equal(t, "Node.js 22", types["nodejs"].Name)
	require.Equal(t, "/usr/local/bin/node", types["nodejs"].Path)

	// 删除 nodejs 登记（外部登记只删记录）。
	delResp := makeRequest(r, http.MethodDelete, fmt.Sprintf("/api/v1/nodes/%d/runtimes/%d?type=nodejs", node.ID, created.ID), nil, adminToken)
	require.Equal(t, http.StatusOK, delResp.Code, delResp.Body.String())
	require.NoError(t, db.Model(&model.NodeRuntime{}).Where("node_id = ?", node.ID).Count(&rtCount).Error)
	require.Equal(t, int64(0), rtCount)

	// 删除 type=jdk 走现有 JDK 删除链路。
	jdkRow := types["jdk"]
	delJDK := makeRequest(r, http.MethodDelete, fmt.Sprintf("/api/v1/nodes/%d/runtimes/%d?type=jdk", node.ID, jdkRow.ID), nil, adminToken)
	require.Equal(t, http.StatusOK, delJDK.Code, delJDK.Body.String())
	require.NoError(t, db.Model(&model.NodeJDK{}).Where("node_id = ?", node.ID).Count(&jdkCount).Error)
	require.Equal(t, int64(0), jdkCount)

	// 缺 type 的删除 400。
	noType := makeRequest(r, http.MethodDelete, fmt.Sprintf("/api/v1/nodes/%d/runtimes/%d", node.ID, created.ID), nil, adminToken)
	require.Equal(t, http.StatusBadRequest, noType.Code)

	// 登记/删除写审计。
	var regAudits, delAudits int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ?", "node.runtime.register").Count(&regAudits).Error)
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ?", "node.runtime.delete").Count(&delAudits).Error)
	require.GreaterOrEqual(t, regAudits, int64(2))
	require.GreaterOrEqual(t, delAudits, int64(2))
}

// TestFR298JDKInUseDeleteBlocked 删除被实例占用的 JDK（经统一运行时端点）仍被 409 拦截。
func TestFR298JDKInUseDeleteBlocked(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupFR298Router(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	pool.SetWorkerClientForTest(node.UUID, &fakeFR298RuntimeWorker{})

	inUse := &model.NodeJDK{NodeID: node.ID, Vendor: "Temurin", MajorVersion: 17, Version: "17.0.12+7", Arch: "x64", Path: "/opt/jdks/temurin-17"}
	require.NoError(t, db.Create(inUse).Error)
	inst := &model.Instance{NodeID: node.ID, Name: "fr298-bound", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDaemon, Status: model.InstanceStatusStopped, JDKID: inUse.ID}
	require.NoError(t, db.Create(inst).Error)

	blocked := makeRequest(r, http.MethodDelete, fmt.Sprintf("/api/v1/nodes/%d/runtimes/%d?type=jdk", node.ID, inUse.ID), nil, adminToken)
	require.Equal(t, http.StatusConflict, blocked.Code, blocked.Body.String())
	require.Equal(t, "JDK_IN_USE", parseJSON(t, blocked)["error"])
}
