package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// M-4B 回归：/log-archive/* 的目标必须经受权校验，不能被请求方任意指定。
//
// 缺陷形态：这两个端点只挂 node.manage，Handler 无 AuthzService，target 直接来自请求，
// Worker 侧再由首个 target 生成归档分区键。默认角色下 node.manage 仅属平台管理员（本就
// 有全局权限），但权限树允许自定义角色——非平台用户一旦获得该权限即可提交任意 target，
// 绕过实例组/目标校验探测归档存在性，甚至对不属于该路由节点的 namespace 触发 rehydrate。
//
// 本测试只覆盖校验分支（不触达 Worker）：用真实 DB 的 AuthzService + 构造的访问上下文。
func TestLogArchiveTargetAuthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)

	setup := func(t *testing.T) (*LogRuntimeHandler, *service.UserAccess) {
		t.Helper()
		db := setupTestDB(t)

		// 节点 1：本路由节点；节点 2：用于跨节点用例。
		node1 := model.Node{Name: "n1", UUID: "uuid-n1", Status: model.NodeStatusOnline}
		node2 := model.Node{Name: "n2", UUID: "uuid-n2", Status: model.NodeStatusOnline}
		require.NoError(t, db.Create(&node1).Error)
		require.NoError(t, db.Create(&node2).Error)

		grp := model.Group{Name: "g1", UUID: "grp-1"}
		require.NoError(t, db.Create(&grp).Error)
		// 实例在本节点；实例与组通过 GroupInstance 关联表绑定（AuthzService 的口径）。
		ownInst := model.Instance{Name: "own", UUID: "uuid-own", NodeID: node1.ID}
		require.NoError(t, db.Create(&ownInst).Error)
		// 另一节点上的实例：用于「实例不属于本节点」用例。
		otherInst := model.Instance{Name: "other", UUID: "uuid-other", NodeID: node2.ID}
		require.NoError(t, db.Create(&otherInst).Error)
		require.NoError(t, db.Create(&model.GroupInstance{GroupID: grp.ID, InstanceID: ownInst.ID}).Error)
		require.NoError(t, db.Create(&model.GroupInstance{GroupID: grp.ID, InstanceID: otherInst.ID}).Error)

		instSvc := service.NewInstanceService(db, nil, nil)
		handler := NewLogRuntimeHandler(service.NewNodeService(db), nil, service.NewAuthzService(db), instSvc)

		// 非平台管理员，但可访问该组（模拟「自定义角色获得 node.manage」）。
		access := &service.UserAccess{
			UserID:           7,
			IsPlatformAdmin:  false,
			MemberGroupIDs:   map[uint]struct{}{grp.ID: {}},
			AccessibleGroups: map[uint]struct{}{grp.ID: {}},
		}
		return handler, access
	}

	newCtx := func(nodeID string, access *service.UserAccess) (*gin.Context, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "id", Value: nodeID}}
		if access != nil {
			c.Set(middleware.CtxAccess, access)
		}
		return c, w
	}

	t.Run("empty_target_is_route_node_itself", func(t *testing.T) {
		h, access := setup(t)
		c, w := newCtx("1", access)
		assert.True(t, h.authorizeArchiveTarget(c, ""))
		assert.Equal(t, http.StatusOK, w.Code, "空 target 表示本节点自身，应放行")
	})

	t.Run("own_node_instance_allowed", func(t *testing.T) {
		h, access := setup(t)
		c, _ := newCtx("1", access)
		// 实例 id 从 1 起（ownInst）。
		assert.True(t, h.authorizeArchiveTarget(c, "inst:1"))
	})

	t.Run("cross_node_instance_rejected", func(t *testing.T) {
		h, access := setup(t)
		c, w := newCtx("1", access)
		// otherInst 属于节点 2，却经节点 1 的路由访问。
		refused := h.authorizeArchiveTarget(c, "inst:2")
		assert.False(t, refused, "不得借本节点通道读取其它节点实例的归档")
		assert.Equal(t, http.StatusForbidden, w.Code)
		assert.Contains(t, w.Body.String(), "FORBIDDEN")
	})

	t.Run("foreign_node_target_rejected", func(t *testing.T) {
		h, access := setup(t)
		c, w := newCtx("1", access)
		assert.False(t, h.authorizeArchiveTarget(c, "node:2"))
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("own_node_target_allowed", func(t *testing.T) {
		h, access := setup(t)
		c, _ := newCtx("1", access)
		assert.True(t, h.authorizeArchiveTarget(c, "node:1"))
	})

	t.Run("unknown_format_rejected", func(t *testing.T) {
		h, access := setup(t)
		c, w := newCtx("1", access)
		assert.False(t, h.authorizeArchiveTarget(c, "*"), "无法识别的目标不得交给 Worker 解释")
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("missing_access_rejected", func(t *testing.T) {
		h, _ := setup(t)
		c, w := newCtx("1", nil)
		assert.False(t, h.authorizeArchiveTarget(c, ""), "无访问上下文时不得放行")
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("nil_authz_fails_closed", func(t *testing.T) {
		// 未接线授权服务时必须拒绝，而不是默认信任。
		h := &LogRuntimeHandler{}
		c, w := newCtx("1", nil)
		assert.False(t, h.authorizeArchiveTarget(c, ""))
		assert.Equal(t, http.StatusForbidden, w.Code)
	})
}
