package router

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// B-2：快照 / 二进制版本写端点的**权限树节点**门禁测试（FR-466 §2.5、FR-468 §2.6）。
//
// 缺陷现场：这五个写 handler 只挂在 permRead("instance.read") 组下，handler 内仅做
// canManageInstance（只判「组归属」不看权限树节点）。而 group_viewer 只读角色
// （仅 instance.read）的 CanAccessGroup 同样为真 → 只读角色可越权回滚快照、替换实例可执行文件。
//
// 本文件断言两件事：
//  1. 只有 instance.read 节点的组内成员被 403 拒绝（五个写端点全覆盖）；
//  2. 具备相应写节点的实例管理员正常放行（不得因加固而把合法路径一起堵住）。

// newSnapshotBinaryGateFixture 建基座：节点 + 组 + 实例 + 一条 completed 快照。
// 返回 (router, db, groupID, instanceID, snapshotID)。
func newSnapshotBinaryGateFixture(t *testing.T) (*gin.Engine, *gorm.DB, uint, uint, uint) {
	t.Helper()
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)
	node := createTestNode(t, db)
	groupID := createGroupViaAPI(t, r, adminTok, "快照权限门禁组")
	instanceID := createInstanceViaAPI(t, r, adminTok, node.ID, groupID)

	// 直接落库一条可回滚快照（避免依赖 Worker 通道）。
	snap := &model.InstanceSnapshot{
		InstanceID: instanceID, Name: "门禁测试快照", Kind: model.SnapshotKindManual,
		State: model.SnapshotStateCompleted,
	}
	require.NoError(t, db.Create(snap).Error)
	return r, db, groupID, instanceID, snap.ID
}

// TestSnapshotBinaryWriteGate_ViewerForbidden 只读角色对五个写端点必须全部 403。
func TestSnapshotBinaryWriteGate_ViewerForbidden(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r, db, groupID, instanceID, snapshotID := newSnapshotBinaryGateFixture(t)
	userSvc := service.NewUserService(db)
	authz := service.NewAuthzService(db)

	// 组内只读成员：权限树只有 instance.read（等价 group_viewer 剖面）。
	tok := gateUserToken(t, r, db, authz, userSvc, "snapviewer", []string{"instance.read"}, groupID)

	cases := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"创建快照", http.MethodPost, "/api/v1/instances/" + itoa(instanceID) + "/snapshots", map[string]any{"name": "x"}},
		{"快照回滚", http.MethodPost, "/api/v1/snapshots/" + itoa(snapshotID) + "/rollback",
			map[string]any{"confirmSnapshotId": snapshotID}},
		{"快照删除", http.MethodDelete, "/api/v1/snapshots/" + itoa(snapshotID), nil},
		{"二进制升级", http.MethodPost, "/api/v1/instances/" + itoa(instanceID) + "/binary-upgrade", map[string]any{"assetId": 1}},
		{"二进制回滚", http.MethodPost, "/api/v1/instances/" + itoa(instanceID) + "/binary-rollback", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := makeRequest(r, tc.method, tc.path, tc.body, tok)
			require.Equal(t, http.StatusForbidden, w.Code,
				"%s：只有 instance.read 的组内成员必须 403（B-2 越权面）: %s", tc.name, w.Body.String())
		})
	}

	// 读路径不受影响：列表仍可读（加固只针对写）。
	w := makeRequest(r, http.MethodGet, "/api/v1/instances/"+itoa(instanceID)+"/snapshots", nil, tok)
	require.Equal(t, http.StatusOK, w.Code, "instance.read 仍可读快照列表: %s", w.Body.String())
}

// TestSnapshotBinaryWriteGate_WriterAllowed 具备写节点的实例管理员必须放行（不得误伤）。
func TestSnapshotBinaryWriteGate_WriterAllowed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r, db, groupID, instanceID, snapshotID := newSnapshotBinaryGateFixture(t)
	userSvc := service.NewUserService(db)
	authz := service.NewAuthzService(db)

	// 组内实例写成员：instance.read + instance.write + instance.delete。
	tok := gateUserToken(t, r, db, authz, userSvc, "snapwriter",
		[]string{"instance.read", "instance.write", "instance.delete"}, groupID)

	// 删除快照：应放行（200）。
	w := makeRequest(r, http.MethodDelete, "/api/v1/snapshots/"+itoa(snapshotID), nil, tok)
	require.Equal(t, http.StatusOK, w.Code, "instance.delete 节点应可删除快照: %s", w.Body.String())

	// 创建快照：应通过权限门禁（后续失败只可能是业务原因，不能是 403）。
	w = makeRequest(r, http.MethodPost, "/api/v1/instances/"+itoa(instanceID)+"/snapshots",
		map[string]any{"name": "写权限快照"}, tok)
	require.NotEqual(t, http.StatusForbidden, w.Code, "instance.write 节点不得被门禁误拒: %s", w.Body.String())
}

// TestSnapshotBinaryWriteGate_DeleteNeedsDeleteNode 删除快照只认 instance.delete，不认 instance.write。
//
// 与实例删除同口径：instance.write 可以改配置，但不等于可以销毁归档数据。
func TestSnapshotBinaryWriteGate_DeleteNeedsDeleteNode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r, db, groupID, _, snapshotID := newSnapshotBinaryGateFixture(t)
	userSvc := service.NewUserService(db)
	authz := service.NewAuthzService(db)
	tok := gateUserToken(t, r, db, authz, userSvc, "snapwriteonly",
		[]string{"instance.read", "instance.write"}, groupID)

	w := makeRequest(r, http.MethodDelete, "/api/v1/snapshots/"+itoa(snapshotID), nil, tok)
	require.Equal(t, http.StatusForbidden, w.Code,
		"删除快照须 instance.delete 节点，instance.write 不足: %s", w.Body.String())
}

// TestSnapshotRollback_RequiresConfirmElement m-1：回滚要求服务端确认要素。
func TestSnapshotRollback_RequiresConfirmElement(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)
	node := createTestNode(t, db)
	groupID := createGroupViaAPI(t, r, adminTok, "确认要素组")
	instanceID := createInstanceViaAPI(t, r, adminTok, node.ID, groupID)
	snap := &model.InstanceSnapshot{
		InstanceID: instanceID, Name: "确认要素快照", Kind: model.SnapshotKindManual,
		State: model.SnapshotStateCompleted,
	}
	require.NoError(t, db.Create(snap).Error)

	// 平台管理员：不因权限被拒，用于隔离验证「确认要素」这一层。
	w := makeRequest(r, http.MethodPost, "/api/v1/snapshots/"+itoa(snap.ID)+"/rollback", map[string]any{}, adminTok)
	require.Equal(t, http.StatusBadRequest, w.Code, "缺少确认要素必须 400: %s", w.Body.String())
	require.Contains(t, w.Body.String(), "CONFIRM_REQUIRED")

	// 确认要素不匹配（ID 不一致）同样拒绝。
	w = makeRequest(r, http.MethodPost, "/api/v1/snapshots/"+itoa(snap.ID)+"/rollback",
		map[string]any{"confirmSnapshotId": snap.ID + 999}, adminTok)
	require.Equal(t, http.StatusBadRequest, w.Code, "确认 ID 不一致必须 400: %s", w.Body.String())

	// confirmName 与目标不一致同样拒绝。
	w = makeRequest(r, http.MethodPost, "/api/v1/snapshots/"+itoa(snap.ID)+"/rollback",
		map[string]any{"confirmName": "别的快照"}, adminTok)
	require.Equal(t, http.StatusBadRequest, w.Code, "确认名不一致必须 400: %s", w.Body.String())
}
