package router

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	"github.com/wcpe/JianManager/internal/controlplane/database"
	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

func TestRBAC_MeAndCatalogAndRoles(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	require.NoError(t, database.AutoMigrate(db))

	jwtCfg := config.JWTConfig{Secret: "test-secret-rbac", AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour}
	authSvc := service.NewAuthService(db, jwtCfg)
	userSvc := service.NewUserService(db)
	authz := service.NewAuthzService(db)
	perm := authz.Permissions()
	require.NoError(t, perm.SeedSystemRoles())

	adminUser, err := userSvc.Create("rbacadmin", "password12345", model.RolePlatformAdmin, model.UserStatusActive)
	require.NoError(t, err)
	memberUser, err := userSvc.Create("rbacmember", "password12345", model.RoleMember, model.UserStatusActive)
	require.NoError(t, err)

	adminPair, err := authSvc.Login("rbacadmin", "password12345")
	require.NoError(t, err)
	memberPair, err := authSvc.Login("rbacmember", "password12345")
	require.NoError(t, err)

	r := gin.New()
	api := r.Group("/api/v1")
	protected := api.Group("")
	protected.Use(middleware.JWTAuth(jwtCfg.Secret))
	protected.Use(middleware.LoadAccess(authz))
	NewAuthMeHandler(authz, perm).RegisterRoutes(protected)
	NewRBACHandler(perm, userSvc).RegisterRoutes(protected)

	w := doRBACReq(r, "GET", "/api/v1/auth/me", nil, adminPair.AccessToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var me map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &me))
	require.Equal(t, true, me["isPlatformAdmin"])
	nodes, _ := me["nodes"].([]any)
	require.NotEmpty(t, nodes)

	w = doRBACReq(r, "GET", "/api/v1/rbac/catalog", nil, memberPair.AccessToken)
	require.Equal(t, http.StatusOK, w.Code)
	var cat struct {
		Domains []struct {
			Domain string `json:"domain"`
			Nodes  []struct {
				ID string `json:"id"`
			} `json:"nodes"`
		} `json:"domains"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &cat))
	require.NotEmpty(t, cat.Domains)

	w = doRBACReq(r, "POST", "/api/v1/rbac/roles", map[string]any{"name": "x", "nodes": []string{"log.read"}}, memberPair.AccessToken)
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())

	w = doRBACReq(r, "POST", "/api/v1/rbac/roles", map[string]any{"name": "日志只读", "nodes": []string{"log.read"}}, adminPair.AccessToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var created service.RoleDetail
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))

	w = doRBACReq(r, "PUT", "/api/v1/rbac/users/"+strconv.FormatUint(uint64(memberUser.ID), 10)+"/role",
		map[string]any{"roleId": created.ID}, adminPair.AccessToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	access, err := authz.LoadUserAccess(memberUser.ID)
	require.NoError(t, err)
	require.Equal(t, created.Key, access.RoleKey)
	require.True(t, access.HasNode("log.read"))
	require.False(t, access.HasNode("user.manage"))

	w = doRBACReq(r, "PUT", "/api/v1/rbac/users/"+strconv.FormatUint(uint64(memberUser.ID), 10)+"/overrides",
		map[string]any{"overrides": []map[string]string{{"node": "log.read", "effect": "deny"}}}, adminPair.AccessToken)
	require.Equal(t, http.StatusOK, w.Code)
	access, err = authz.LoadUserAccess(memberUser.ID)
	require.NoError(t, err)
	require.False(t, access.HasNode("log.read"))
	_ = adminUser
}

func doRBACReq(r *gin.Engine, method, path string, body any, token string) *httptest.ResponseRecorder {
	var req *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		req = httptest.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}
