package router

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// TestBotManageGate_BehaviorAndCommand 钉住 behavior / command 两个端点的 **权限节点** 闸门。
//
// # 为什么要这个用例
//
// 这两个端点原本只调 `CanManageBot`（资源级），而它最终落到 `CanManageInstance`，
// 后者的判据是 `IsPlatformAdmin || CanAccessGroup`——**完全不看 `bot:manage`**。
// 而 `docs/API.md` 对两个端点都写明需 `bot:manage`（且须可访问该 Bot 所属实例所在组），
// 也就是「权限节点 **且** 资源级」。缺了前一半就在生产上构成越权：
// 有实例组访问权、但权限树里没有 `bot:manage` 的普通组员，能切 Bot 行为、发游戏内命令；
// 而同样的能力走 `/bots/batch` 却被拒——同类操作两条路一开一闭。
//
// # 为何要造真实权限树而不是直接拼 UserAccess
//
// `HasPermission` 在 `Nodes == nil` 时走的是历史启发式（属于任一组即放行 bot:manage），
// 那是纯单测桩才会遇到的分支。生产上 `LoadUserAccess` 一定会填 `Nodes`，
// 此时**只认权限树**。用 SeedSystemRoles + CreateRole + BindUserRole 造出真实树，
// 才能复现生产语义——否则用例会因为走启发式而永远绿，变成假报。
func TestBotManageGate_BehaviorAndCommand(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	authz := service.NewAuthzService(db)
	require.NoError(t, authz.Permissions().SeedSystemRoles())

	createTestNode(t, db)
	adminToken := getAdminToken(t, r)
	groupID := createGroupViaAPI(t, r, adminToken, "权限测试组")
	instID := createInstanceViaAPI(t, r, adminToken, 1, groupID)
	botID := makeBot(t, db, instID, "gate-bot", model.BotStatusConnected, "idle")

	// 造两个用户：都在同一组（享有资源级访问），差别只在权限树。
	//
	// viewer：权限树里**没有** bot.manage ⇒ 必须被拒。
	// manager：权限树里**有** bot.manage ⇒ 必须放行（否则这条闸就变成一刀切封死）。
	//
	// 注意节点写法：权限目录（permission_catalog.go）里注册的是 **点号** `bot.manage`，
	// 而代码里用的常量是 `bot:manage`。写错格式会被 CreateRole 的
	// IsValidPermissionNode 静默跳过——角色建成功、节点却是空的，用例就会假绿。
	// HasPermission 自身会做冒号→点号兼容，所以生产两种写法都能命中；
	// 但**建角色时必须用目录里的规范写法**。
	mkUser := func(name string, nodes []string) string {
		token := getMemberToken(t, r, name, "Password123")
		userID := findUserIDByUsername(t, db, name)
		role, err := authz.Permissions().CreateRole(name+"-role", "", nodes)
		require.NoError(t, err)
		require.NoError(t, authz.Permissions().BindUserRole(userID, role.ID))
		addMemberViaAPI(t, r, adminToken, groupID, userID, model.GroupMemberRoleMember)
		return token
	}

	viewerToken := mkUser("bot_viewer", []string{"bot.read"})
	managerToken := mkUser("bot_manager", []string{"bot.read", "bot.manage"})

	// 先确认构造出来的权限树真的生效——否则下面的断言测不到真东西。
	viewerAccess, err := authz.LoadUserAccess(findUserIDByUsername(t, db, "bot_viewer"))
	require.NoError(t, err)
	require.NotNil(t, viewerAccess.Nodes, "生产路径必须带权限树；为 nil 会走启发式，用例失效")
	require.False(t, viewerAccess.HasPermission(service.PermBotManage), "viewer 不应拥有 bot:manage")

	managerAccess, err := authz.LoadUserAccess(findUserIDByUsername(t, db, "bot_manager"))
	require.NoError(t, err)
	require.True(t, managerAccess.HasPermission(service.PermBotManage), "manager 应拥有 bot:manage")

	behaviorPath := fmt.Sprintf("/api/v1/bots/%d/behavior", botID)
	commandPath := fmt.Sprintf("/api/v1/bots/%d/command", botID)

	// 前置事实：两者都**能访问该实例**（资源级条件相同）。
	// 这样下面的 403 就只能来自权限节点闸，而不是被资源级顺手拦掉。
	for _, tc := range []struct {
		name  string
		token string
	}{
		{"viewer", viewerToken},
		{"manager", managerToken},
	} {
		w := makeRequest(r, "POST", behaviorPath, map[string]interface{}{"behavior": "idle"}, tc.token)
		require.NotEqual(t, http.StatusNotFound, w.Code,
			"%s 应能访问该 Bot（资源级通过），实际 404 说明前置事实不成立", tc.name)
	}

	// 核心断言 1：无 bot:manage ⇒ 两个端点都必须 403。
	w := makeRequest(r, "POST", behaviorPath, map[string]interface{}{"behavior": "idle"}, viewerToken)
	require.Equal(t, http.StatusForbidden, w.Code, "无 bot:manage 时 behavior 必须 403")

	w = makeRequest(r, "POST", commandPath, map[string]interface{}{"command": "/list"}, viewerToken)
	require.Equal(t, http.StatusForbidden, w.Code, "无 bot:manage 时 command 必须 403")

	// 核心断言 2：有 bot:manage ⇒ 不得被权限节点闸拦住（403 视为回归）。
	w = makeRequest(r, "POST", behaviorPath, map[string]interface{}{"behavior": "idle"}, managerToken)
	require.NotEqual(t, http.StatusForbidden, w.Code, "有 bot:manage 时 behavior 不应 403")

	w = makeRequest(r, "POST", commandPath, map[string]interface{}{"command": "/list"}, managerToken)
	require.NotEqual(t, http.StatusForbidden, w.Code, "有 bot:manage 时 command 不应 403")
}
