package router

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// F-01~F-06 平台管理域写路径门禁（FR-432 全量重审）。

func TestWriteGate_PlatformDomainWrites(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)
	userSvc := service.NewUserService(db)
	authz := service.NewAuthzService(db)

	// 只读节点用户（模拟 group_viewer + node.read）
	tokViewer := gateUserToken(t, r, db, authz, userSvc, "platread",
		[]string{
			"node.read", "user.read", "settings.read", "license.read",
			"network.read", "alert.read", "backup.read",
			"channel.read", "dist.ops.read", "file.read", "instance.read",
		}, 0)

	// F-02 用户写
	w := makeRequest(r, http.MethodPost, "/api/v1/users",
		map[string]any{"username": "hack", "password": "Password@123", "role": 1, "status": 0}, tokViewer)
	require.Equal(t, http.StatusForbidden, w.Code, "F-02 create user needs user.manage: %s", w.Body.String())
	w = makeRequest(r, http.MethodPut, "/api/v1/users/1", map[string]any{"role": 1}, tokViewer)
	require.Equal(t, http.StatusForbidden, w.Code, "F-02 update user")

	// F-03 settings write — group path /settings，PUT ""
	w = makeRequest(r, http.MethodPut, "/api/v1/settings", map[string]any{"values": map[string]string{"x": "y"}}, tokViewer)
	require.Equal(t, http.StatusForbidden, w.Code, "F-03 settings.write: %s", w.Body.String())

	// F-04 network write
	w = makeRequest(r, http.MethodPost, "/api/v1/networks", map[string]any{"name": "evil"}, tokViewer)
	require.Equal(t, http.StatusForbidden, w.Code, "F-04 network.manage")

	// F-05 alert write
	w = makeRequest(r, http.MethodPost, "/api/v1/alerts/rules", map[string]any{"name": "r"}, tokViewer)
	require.Equal(t, http.StatusForbidden, w.Code, "F-05 alert.manage")

	// F-06 backup storage write
	w = makeRequest(r, http.MethodPost, "/api/v1/backup-storages", map[string]any{"name": "s", "type": "local"}, tokViewer)
	require.Equal(t, http.StatusForbidden, w.Code, "F-06 backup storage node.manage")

	// F-07 channel write — 测试装配注入 ClientChannel 后应 403
	w = makeRequest(r, http.MethodPost, "/api/v1/client-channels", map[string]any{"channelId": "c1", "name": "n"}, tokViewer)
	require.Equal(t, http.StatusForbidden, w.Code, "F-07 channel.write: %s", w.Body.String())

	// F-07b 发布：仅有 channel.read 不得 PublishVersion
	w = makeRequest(r, http.MethodPost, "/api/v1/client-channels/ch1/versions",
		map[string]any{"version": "1.0.0"}, tokViewer)
	if w.Code == http.StatusNotFound {
		// 路由可能挂在 /client-versions/... 兜底再试常见前缀
		w = makeRequest(r, http.MethodPost, "/api/v1/client-versions/ch1",
			map[string]any{"version": "1.0.0"}, tokViewer)
	}
	require.Equal(t, http.StatusForbidden, w.Code, "F-07b publish needs channel.write/dist.publish: %s", w.Body.String())

	// F-07c IP 规则写：仅 dist.ops.read 不得 Add
	w = makeRequest(r, http.MethodPost, "/api/v1/client-dist/ip-rules",
		map[string]any{"ip": "1.2.3.4", "action": "block"}, tokViewer)
	require.Equal(t, http.StatusForbidden, w.Code, "F-07c IP rule write needs dist.ops.write: %s", w.Body.String())

	// F-07d 安全写：CreateGroup 仅 dist.ops.write
	w = makeRequest(r, http.MethodPost, "/api/v1/client-dist/security/groups",
		map[string]any{"name": "g"}, tokViewer)
	require.Equal(t, http.StatusForbidden, w.Code, "F-07d security group write needs dist.ops.write: %s", w.Body.String())

	// F-01 node.read 不可 JDK 写
	node := createTestNode(t, db)
	w = makeRequest(r, http.MethodPost, "/api/v1/nodes/"+itoa(node.ID)+"/jdks",
		map[string]any{"name": "jdk", "path": "/x"}, tokViewer)
	require.Equal(t, http.StatusForbidden, w.Code, "F-01 jdk write needs node.manage")
	// 只读节点列表：node.read 可过（非 403）
	w = makeRequest(r, http.MethodGet, "/api/v1/nodes", nil, tokViewer)
	require.NotEqual(t, http.StatusForbidden, w.Code, "node.read should list nodes")

	// F5-01 network members / actions
	w = makeRequest(r, http.MethodPost, "/api/v1/networks/1/members", map[string]any{"instanceIds": []uint{1}}, tokViewer)
	require.Equal(t, http.StatusForbidden, w.Code, "F5-01 network members: %s", w.Body.String())
	w = makeRequest(r, http.MethodPost, "/api/v1/networks/1/actions", map[string]any{"action": "start"}, tokViewer)
	require.Equal(t, http.StatusForbidden, w.Code, "F5-01 network actions")
	// F5-02 registration write
	w = makeRequest(r, http.MethodPost, "/api/v1/proxies/1/registrations", map[string]any{"networkId": 1, "instanceId": 1}, tokViewer)
	require.Equal(t, http.StatusForbidden, w.Code, "F5-02 registration write needs network.manage: %s", w.Body.String())
	// F5-03 group member write（viewer 无 group.member.write）
	w = makeRequest(r, http.MethodPost, "/api/v1/groups/1/members", map[string]any{"userId": 1, "role": 0}, tokViewer)
	require.Equal(t, http.StatusForbidden, w.Code, "F5-03 group.member.write")
	w = makeRequest(r, http.MethodDelete, "/api/v1/groups/1/members/1", nil, tokViewer)
	require.Equal(t, http.StatusForbidden, w.Code, "F5-03 remove member")

	// 管理员仍可读列表（不 403）
	w = makeRequest(r, http.MethodGet, "/api/v1/users", nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}
