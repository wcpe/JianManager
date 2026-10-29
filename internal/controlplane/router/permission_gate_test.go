package router

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/logcoord"
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
	// 注入一个联邦协调器：`/logs/federation` 是 `log.read` 现在保护的唯一端点，
	// 未注入协调器时它不注册（避免暴露一个必 503 的端点），权限门禁就无从验证。
	// 本用例只验鉴权层级，不驱动真实查询，因此空 resolver/dialer 足够。
	r, db := setupFederationRouter(t, logcoord.New(nil, nil))
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

	// 日志端点的门禁：`/logs` 已随 CP `logs` 表删除而下线（2026-10-29），
	// 现在受 `log.read` 保护的是联邦查询入口 `/logs/federation`。
	// 必须同步换探测路径：旧路径现在返回 404，会让「禁止/放行」两类断言
	// 都失去检验力（404 既不是 403、也不是通过鉴权的业务响应）。
	const logPath = "/api/v1/logs/federation"
	// 注意：本用例只验**权限节点闸**（是否有 log.read）这一层。
	//
	// 联邦端点通过鉴权后还会做**资源级**授权（调用方没有任何授权日志目标时
	// 仍回 403「无授权日志目标」）——那是下一层语义，不是本用例的靶子。
	// 因此日志路径的断言只看“是否进入业务层”：进入则放行，否则必须是 403。
	// 两个用户在本用例里都没有日志目标，所以 logTok 也会拿到 403；
	// 为保留“有权限 vs 无权限”的区分力，这里改为断言错误体里的 nodes 字段。
	cases := []struct {
		token  string
		path   string
		forbid bool
	}{
		{emptyTok, "/api/v1/instances", true},
		{emptyTok, "/api/v1/nodes", true},
		{emptyTok, "/api/v1/bots", true},
		{emptyTok, logPath, true},
		{emptyTok, "/api/v1/metrics/overview?range=24h", true},
		{emptyTok, "/api/v1/players", true},
		{emptyTok, "/api/v1/groups", true},
		{emptyTok, "/api/v1/schedules", true},
		{emptyTok, "/api/v1/networks", true},
		{emptyTok, "/api/v1/templates", true},
		{emptyTok, "/api/v1/alerts/rules", true},
		{emptyTok, "/api/v1/rbac/catalog", false},
		{instTok, "/api/v1/instances", false},
		{instTok, logPath, true},
		{instTok, "/api/v1/nodes", true},
		{logTok, "/api/v1/instances", true},
	}

	// 日志路径单独验证：拥有 log.read 的用户必须跨过权限闸。
	// 至于跨过之后是否因“无授权目标”再被拒，那是资源授权层的事，
	// 用错误体区分：权限闸返的 nodes 含 log.read，资源闸返无授权日志目标。
	logGateCases := []struct {
		token string
		want  bool
	}{
		{emptyTok, false},
		{instTok, false},
		{logTok, true},
	}

	for _, tc := range cases {
		w := makeRequest(r, http.MethodGet, tc.path, nil, tc.token)
		if tc.forbid {
			require.Equal(t, http.StatusForbidden, w.Code, "path=%s body=%s", tc.path, w.Body.String())
		} else {
			require.NotEqual(t, http.StatusForbidden, w.Code, "path=%s body=%s", tc.path, w.Body.String())
		}
	}

	// 日志路径：区分「被权限闸拦住」与「跨过权限闸、被资源授权拦住」。
	for _, tc := range logGateCases {
		w := makeRequest(r, http.MethodGet, logPath, nil, tc.token)
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		// 权限闸的拒绝体带 nodes 字段（告诉调用方缺哪个权限节点）。
		_, blockedByPermGate := body["nodes"]
		require.Equal(t, !tc.want, blockedByPermGate,
			"token=%s want=%v body=%s（期望：无 log.read 被权限闸拒；有 log.read 则进入资源授权层）",
			tc.token, tc.want, w.Body.String())
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
