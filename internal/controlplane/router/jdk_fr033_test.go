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

type fakeFR033RouterJDKWorker struct {
	workerpb.WorkerServiceClient
	installReq *workerpb.InstallJDKRequest
}

func (f *fakeFR033RouterJDKWorker) ListJDKs(context.Context, *workerpb.ListJDKsRequest, ...grpc.CallOption) (*workerpb.ListJDKsResponse, error) {
	return &workerpb.ListJDKsResponse{}, nil
}

func (f *fakeFR033RouterJDKWorker) InstallJDK(_ context.Context, req *workerpb.InstallJDKRequest, _ ...grpc.CallOption) (*workerpb.InstallJDKResponse, error) {
	f.installReq = req
	return &workerpb.InstallJDKResponse{Success: true, TaskId: req.TaskId}, nil
}

func setupFR033Router(t *testing.T, db *gorm.DB, pool *cpgrpc.ClientPool) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	jwtCfg := config.JWTConfig{Secret: "test-secret-key-for-fr033", AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour}
	groupSvc := service.NewGroupService(db)
	authzSvc := service.NewAuthzService(db)
	instanceSvc := service.NewInstanceService(db, groupSvc, pool)
	instanceSvc.Shutdown()
	nodeSvc := service.NewNodeService(db)
	nodeSvc.SetInstanceService(instanceSvc)
	notificationSvc := service.NewNotificationService(db)
	taskSvc := service.NewTaskService(db)
	taskSvc.SetNotificationService(notificationSvc)
	jdkSvc := service.NewJDKService(db, pool)
	jdkSvc.SetTaskService(taskSvc)

	svcs := &Services{
		Auth:          service.NewAuthService(db, jwtCfg),
		User:          service.NewUserService(db),
		Group:         groupSvc,
		Node:          nodeSvc,
		JDK:           jdkSvc,
		Instance:      instanceSvc,
		InstanceBatch: service.NewInstanceBatchService(db, pool),
		Audit:         service.NewAuditService(db),
		Authz:         authzSvc,
		Task:          taskSvc,
		Notification:  notificationSvc,
	}
	return Setup(svcs, jwtCfg.Secret)
}

func TestFR033JDKResourceRoutes(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupFR033Router(t, db, pool)
	adminToken := getAdminToken(t, r)
	memberToken := getMemberToken(t, r, "member-fr033", "password123")
	node := createTestNode(t, db)
	fake := &fakeFR033RouterJDKWorker{}
	pool.SetWorkerClientForTest(node.UUID, fake)

	forbidden := makeRequest(r, http.MethodGet, fmt.Sprintf("/api/v1/nodes/%d/jdks", node.ID), nil, memberToken)
	require.Equal(t, http.StatusForbidden, forbidden.Code)

	createResp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/jdks", node.ID), map[string]any{
		"vendor": "Temurin", "majorVersion": 21, "version": "21.0.4+9", "arch": "x64", "path": "/opt/jdks/temurin-21", "managed": false,
	}, adminToken)
	require.Equal(t, http.StatusCreated, createResp.Code, createResp.Body.String())
	var created model.NodeJDK
	require.NoError(t, json.Unmarshal(createResp.Body.Bytes(), &created))
	require.Equal(t, node.ID, created.NodeID)
	require.Equal(t, "Temurin", created.Vendor)
	require.False(t, created.Managed)

	listResp := makeRequest(r, http.MethodGet, fmt.Sprintf("/api/v1/nodes/%d/jdks", node.ID), nil, adminToken)
	require.Equal(t, http.StatusOK, listResp.Code, listResp.Body.String())
	var list []model.NodeJDK
	require.NoError(t, json.Unmarshal(listResp.Body.Bytes(), &list))
	require.Len(t, list, 1)
	require.Equal(t, "/opt/jdks/temurin-21", list[0].Path)

	inUse := &model.NodeJDK{NodeID: node.ID, Vendor: "Temurin", MajorVersion: 17, Version: "17.0.12+7", Arch: "x64", Path: "/opt/jdks/temurin-17"}
	require.NoError(t, db.Create(inUse).Error)
	inst := &model.Instance{NodeID: node.ID, Name: "fr033-bound-instance", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDaemon, Status: model.InstanceStatusStopped, JDKID: inUse.ID}
	require.NoError(t, db.Create(inst).Error)
	blockedDelete := makeRequest(r, http.MethodDelete, fmt.Sprintf("/api/v1/nodes/%d/jdks/%d", node.ID, inUse.ID), nil, adminToken)
	require.Equal(t, http.StatusConflict, blockedDelete.Code)
	require.Equal(t, "JDK_IN_USE", parseJSON(t, blockedDelete)["error"])

	installResp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/jdks/install", node.ID), map[string]any{
		"vendor": "Temurin", "majorVersion": 21, "arch": "x64", "version": "21.0.4+9",
	}, adminToken)
	require.Equal(t, http.StatusAccepted, installResp.Code, installResp.Body.String())
	require.NotNil(t, fake.installReq)
	require.Equal(t, "Temurin", fake.installReq.Vendor)
	require.Equal(t, int32(21), fake.installReq.MajorVersion)
	require.NotEmpty(t, fake.installReq.TaskId)
	require.NotEmpty(t, parseJSON(t, installResp)["taskId"])

	deleteResp := makeRequest(r, http.MethodDelete, fmt.Sprintf("/api/v1/nodes/%d/jdks/%d", node.ID, created.ID), nil, adminToken)
	require.Equal(t, http.StatusOK, deleteResp.Code, deleteResp.Body.String())
}
