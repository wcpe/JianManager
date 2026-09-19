package router

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// permRouteCase：路由组权限门禁用例（FR-432）。期望无节点用户 403、有节点用户非 403。
type permRouteCase struct {
	name   string
	method string
	path   string
	// grants 该用户应拥有的权限节点（空 = 全无）
	grants []string
}

// TestPermissionNodes_RouteGate 逐域验证：清空权限后业务 API 403；授予对应节点后放行（非 403）。
func TestPermissionNodes_RouteGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db) // 完整路由
	authz := service.NewAuthzService(db)

	empty, err := service.NewUserService(db).Create("perm_empty", "password123", model.RoleMember, model.UserStatusActive)
	require.NoError(t, err)
	emptyRole, err := authz.Permissions().CreateRole("empty-role", "none", nil)
	require.NoError(t, err)
	require.NoError(t, authz.Permissions().BindUserRole(empty.ID, emptyRole.ID))

	instUser, err := service.NewUserService(db).Create("perm_inst", "password123", model.RoleMember, model.UserStatusActive)
	require.NoError(t, err)
	instRole, err := authz.Permissions().CreateRole("inst-read", "", []string{"instance.read"})
	require.NoError(t, err)
	require.NoError(t, authz.Permissions().BindUserRole(instUser.ID, instRole.ID))

	logUser, err := service.NewUserService(db).Create("perm_log", "password123", model.RoleMember, model.UserStatusActive)
	require.NoError(t, err)
	logRole, err := authz.Permissions().CreateRole("log-read", "", []string{"log.read"})
	require.NoError(t, err)
	require.NoError(t, authz.Permissions().BindUserRole(logUser.ID, logRole.ID))

	access, err := authz.LoadUserAccess(empty.ID)
	require.NoError(t, err)
	require.Empty(t, access.Nodes)

	emptyTok := loginUserToken(t, r, "perm_empty", "password123")
	instTok := loginUserToken(t, r, "perm_inst", "password123")
	logTok := loginUserToken(t, r, "perm_log", "password123")

	cases := []struct {
		token  string
		path   string
		forbid bool
	}{
		{emptyTok, "/api/v1/instances", true},
		{emptyTok, "/api/v1/nodes", true},
		{emptyTok, "/api/v1/bots", true},
		{emptyTok, "/api/v1/logs", true},
		{emptyTok, "/api/v1/metrics/overview?range=24h", true},
		{emptyTok, "/api/v1/players", true},
		{emptyTok, "/api/v1/groups", true},
		{emptyTok, "/api/v1/schedules", true},
		{emptyTok, "/api/v1/networks", true},
		{emptyTok, "/api/v1/templates", true},
		{emptyTok, "/api/v1/alerts/rules", true},
		{emptyTok, "/api/v1/rbac/catalog", false},
		{instTok, "/api/v1/instances", false},
		{instTok, "/api/v1/logs", true},
		{instTok, "/api/v1/nodes", true},
		{logTok, "/api/v1/logs", false},
		{logTok, "/api/v1/instances", true},
	}

	for _, tc := range cases {
		w := makeRequest(r, http.MethodGet, tc.path, nil, tc.token)
		if tc.forbid {
			require.Equal(t, http.StatusForbidden, w.Code, "path=%s body=%s", tc.path, w.Body.String())
		} else {
			require.NotEqual(t, http.StatusForbidden, w.Code, "path=%s body=%s", tc.path, w.Body.String())
		}
	}
}

func loginUserToken(t *testing.T, r *gin.Engine, user, pass string) string {
	t.Helper()
	w := makeRequest(r, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": user, "password": pass}, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	resp := parseJSON(t, w)
	tok, _ := resp["accessToken"].(string)
	require.NotEmpty(t, tok, "login response %v", resp)
	return tok
}

// TestPermissionCatalog_AllNodesValid 目录节点与有效算法一致性。
func TestPermissionCatalog_AllNodesValid(t *testing.T) {
	nodes := service.AllPermissionNodes()
	require.GreaterOrEqual(t, len(nodes), 40)
	seen := map[string]bool{}
	for _, n := range nodes {
		require.True(t, service.IsValidPermissionNode(n), n)
		require.False(t, seen[n], "dup node %s", n)
		seen[n] = true
	}
	// 六域均非空
	domains := map[string]int{}
	for _, d := range service.PermissionCatalog {
		domains[d.Domain] = len(d.Nodes)
	}
	for _, key := range []string{"platform", "runtime", "observability", "distribution", "agent", "workspace"} {
		require.Greater(t, domains[key], 0, key)
	}
}

// TestRequireAnyPerm_Middleware 单元级：任意节点命中即放行。
func TestRequireAnyPerm_Middleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	authz := service.NewAuthzService(db)
	require.NoError(t, authz.Permissions().SeedSystemRoles())
	usr, err := service.NewUserService(db).Create("perm_mid", "Password@123", model.RoleMember, model.UserStatusActive)
	require.NoError(t, err)
	role, err := authz.Permissions().CreateRole("mid", "", []string{"monitor.read"})
	require.NoError(t, err)
	require.NoError(t, authz.Permissions().BindUserRole(usr.ID, role.ID))

	access, err := authz.LoadUserAccess(usr.ID)
	require.NoError(t, err)
	require.True(t, access.HasNode("monitor.read"))
	require.False(t, access.HasNode("user.manage"))
	// R-14：无 Nodes 的角色启发式——组内可 instance.read，不可平台 user.manage
	legacy := &service.UserAccess{Role: model.RoleMember, AccessibleGroups: map[uint]struct{}{1: {}}}
	require.False(t, legacy.HasPermission(service.PermUserManage))
	require.True(t, legacy.HasPermission(service.PermInstanceRead))
}
