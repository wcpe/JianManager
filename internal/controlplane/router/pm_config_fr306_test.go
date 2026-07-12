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
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/proto/workerpb"
)

type fakePMWorker struct {
	workerpb.WorkerServiceClient
	lastSet *workerpb.SetPMConfigRequest
	getResp *workerpb.GetPMConfigResponse
}

func (f *fakePMWorker) GetPMConfig(_ context.Context, _ *workerpb.GetPMConfigRequest, _ ...grpc.CallOption) (*workerpb.GetPMConfigResponse, error) {
	if f.getResp != nil {
		return f.getResp, nil
	}
	return &workerpb.GetPMConfigResponse{Pm: "npm", CorepackAvailable: true, PmVersion: "10.0.0"}, nil
}

func (f *fakePMWorker) SetPMConfig(_ context.Context, req *workerpb.SetPMConfigRequest, _ ...grpc.CallOption) (*workerpb.SetPMConfigResponse, error) {
	f.lastSet = req
	return &workerpb.SetPMConfigResponse{Success: true, PmVersion: "9.1.0"}, nil
}

func setupPMRouter(t *testing.T, db *gorm.DB, pool *cpgrpc.ClientPool) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	jwtCfg := config.JWTConfig{Secret: "test-secret-pm306", AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour}
	groupSvc := service.NewGroupService(db)
	instanceSvc := service.NewInstanceService(db, groupSvc, pool)
	instanceSvc.Shutdown()
	svcs := &Services{
		Auth:     service.NewAuthService(db, jwtCfg),
		User:     service.NewUserService(db),
		Group:    groupSvc,
		Node:     service.NewNodeService(db),
		Authz:    service.NewAuthzService(db),
		Audit:    service.NewAuditService(db),
		PMConfig: service.NewPMConfigService(db, pool),
	}
	return Setup(svcs, jwtCfg.Secret)
}

// TestFR306PMConfigSetGet 保存 PM 偏好+registry → 下发 Worker → 落库 → 回读脱敏（FR-306）。
func TestFR306PMConfigSetGet(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupPMRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	fake := &fakePMWorker{}
	pool.SetWorkerClientForTest(node.UUID, fake)

	// PUT：选 pnpm + 两条 registry（默认源 + 带 token 的 @myco 域源）
	putBody := map[string]any{
		"pm": "pnpm",
		"registries": []map[string]any{
			{"url": "https://registry.npmmirror.com"},
			{"scope": "myco", "url": "https://npm.myco.com", "token": "sekret"},
		},
	}
	put := makeRequest(r, http.MethodPut, fmt.Sprintf("/api/v1/nodes/%d/pm-config", node.ID), putBody, adminToken)
	require.Equal(t, http.StatusOK, put.Code, put.Body.String())

	// Worker 收到明文 token 下发
	require.NotNil(t, fake.lastSet)
	require.Equal(t, "pnpm", fake.lastSet.Pm)
	require.Len(t, fake.lastSet.Registries, 2)
	var sawToken bool
	for _, rg := range fake.lastSet.Registries {
		if rg.Scope == "myco" {
			require.Equal(t, "sekret", rg.Token, "Worker 应收到明文 token")
			sawToken = true
		}
	}
	require.True(t, sawToken)

	// 回读：token 脱敏、tokenMasked=true
	var putView struct {
		PM         string `json:"pm"`
		Registries []struct {
			Scope       string `json:"scope"`
			Token       string `json:"token"`
			TokenMasked bool   `json:"tokenMasked"`
		} `json:"registries"`
	}
	require.NoError(t, json.Unmarshal(put.Body.Bytes(), &putView))
	require.Equal(t, "pnpm", putView.PM)
	for _, rg := range putView.Registries {
		if rg.Scope == "myco" {
			require.True(t, rg.TokenMasked)
			require.NotEqual(t, "sekret", rg.Token, "回读不得暴露明文 token")
		}
	}

	// GET 融合 Worker corepack 状态
	get := makeRequest(r, http.MethodGet, fmt.Sprintf("/api/v1/nodes/%d/pm-config", node.ID), nil, adminToken)
	require.Equal(t, http.StatusOK, get.Code)
	require.Contains(t, get.Body.String(), "corepackAvailable")
}

// TestFR306MaskedTokenPreserved 回传掩码 token 不清空源凭据（同 proxy.url 保存语义）。
func TestFR306MaskedTokenPreserved(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupPMRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	fake := &fakePMWorker{}
	pool.SetWorkerClientForTest(node.UUID, fake)

	// 首次存带 token 的源
	makeRequest(r, http.MethodPut, fmt.Sprintf("/api/v1/nodes/%d/pm-config", node.ID), map[string]any{
		"pm": "npm", "registries": []map[string]any{{"scope": "myco", "url": "https://npm.myco.com", "token": "orig-token"}},
	}, adminToken)

	// 二次保存回传掩码 token（模拟前端拿脱敏值直接回传）
	makeRequest(r, http.MethodPut, fmt.Sprintf("/api/v1/nodes/%d/pm-config", node.ID), map[string]any{
		"pm": "npm", "registries": []map[string]any{{"scope": "myco", "url": "https://npm.myco.com", "token": "********"}},
	}, adminToken)

	// Worker 最后一次仍收到原始 token（掩码被解析回原值）
	require.Equal(t, "orig-token", fake.lastSet.Registries[0].Token, "掩码回传应沿用原 token 不清空")
}
