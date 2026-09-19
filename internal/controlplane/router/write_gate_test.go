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

// R-01~R-08 提权面回归（FR-432 / ADR-089 / spec §5.4）。

func gateUserToken(t *testing.T, r *gin.Engine, db *gorm.DB, authz *service.AuthzService, userSvc *service.UserService, username string, nodes []string, groupID uint) string {
	t.Helper()
	u, err := userSvc.Create(username, "password123", model.RoleMember, model.UserStatusActive)
	if err != nil {
		var eu model.User
		require.NoError(t, db.Where("username = ?", username).First(&eu).Error)
		u = &eu
	}
	role, err := authz.Permissions().CreateRole("gate_"+username, "", nodes)
	require.NoError(t, err)
	require.NoError(t, authz.Permissions().BindUserRole(u.ID, role.ID))
	if groupID != 0 {
		require.NoError(t, db.Create(&model.GroupMember{GroupID: groupID, UserID: u.ID, Role: model.GroupMemberRoleMember}).Error)
	}
	return loginUserToken(t, r, username, "password123")
}

// gormDB = *gorm.DB via testhelper setupTestDB
func TestWriteGate_NodeReadCannotDrainOrDelete(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	userSvc := service.NewUserService(db)
	authz := service.NewAuthzService(db)
	node := createTestNode(t, db)

	tok := gateUserToken(t, r, db, authz, userSvc, "nreader", []string{"node.read"}, 0)
	w := makeRequest(r, http.MethodPost, "/api/v1/nodes/"+itoa(node.ID)+"/drain", nil, tok)
	require.Equal(t, http.StatusForbidden, w.Code, "R-01 node.read→drain must 403: %s", w.Body.String())
	w = makeRequest(r, http.MethodDelete, "/api/v1/nodes/"+itoa(node.ID), nil, tok)
	require.Equal(t, http.StatusForbidden, w.Code, "R-01 node.read→delete must 403: %s", w.Body.String())
	// 列表仍可读
	w = makeRequest(r, http.MethodGet, "/api/v1/nodes", nil, tok)
	require.NotEqual(t, http.StatusForbidden, w.Code, "node.read should list nodes")
}

func TestWriteGate_ViewerCannotOperateOrDeleteInstance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)
	userSvc := service.NewUserService(db)
	authz := service.NewAuthzService(db)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, adminTok, "gw")
	id := makeInstanceInGroup(t, db, node.ID, g, "gview", model.InstanceStatusStopped)

	tok := gateUserToken(t, r, db, authz, userSvc, "vwr1",
		[]string{"instance.read", "file.read", "monitor.read"}, g)

	w := makeRequest(r, http.MethodPost, "/api/v1/instances/"+itoa(id)+"/start", nil, tok)
	require.Equal(t, http.StatusForbidden, w.Code, "R-02 viewer start: %s", w.Body.String())
	w = makeRequest(r, http.MethodPost, "/api/v1/instances/"+itoa(id)+"/stop", nil, tok)
	require.Equal(t, http.StatusForbidden, w.Code, "R-02 viewer stop")
	w = makeRequest(r, http.MethodPost, "/api/v1/instances/"+itoa(id)+"/restart", nil, tok)
	require.Equal(t, http.StatusForbidden, w.Code, "R-02 viewer restart")
	w = makeRequest(r, http.MethodDelete, "/api/v1/instances/"+itoa(id), nil, tok)
	require.Equal(t, http.StatusForbidden, w.Code, "R-02 viewer delete")
}

func TestWriteGate_ViewerCannotWriteFileScheduleTemplate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)
	userSvc := service.NewUserService(db)
	authz := service.NewAuthzService(db)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, adminTok, "gw2")
	id := makeInstanceInGroup(t, db, node.ID, g, "wfile", model.InstanceStatusRunning)

	tok := gateUserToken(t, r, db, authz, userSvc, "vwr2",
		[]string{"instance.read", "file.read", "schedule.read", "template.read"}, g)

	w := makeRequest(r, http.MethodPost, "/api/v1/instances/"+itoa(id)+"/files/write",
		map[string]any{"path": "server.properties", "content": "x"}, tok)
	require.Equal(t, http.StatusForbidden, w.Code, "R-03 file.write: %s", w.Body.String())

	w = makeRequest(r, http.MethodPost, "/api/v1/schedules",
		map[string]any{"name": "s", "cronExpr": "0 0 * * *", "type": "backup"}, tok)
	require.Equal(t, http.StatusForbidden, w.Code, "R-05 schedule.write: %s", w.Body.String())

	w = makeRequest(r, http.MethodPost, "/api/v1/templates",
		map[string]any{"name": "tpl"}, tok)
	require.Equal(t, http.StatusForbidden, w.Code, "R-06 template.manage: %s", w.Body.String())
}

func TestWriteGate_GroupListScopedWithoutGroupManage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)
	userSvc := service.NewUserService(db)
	authz := service.NewAuthzService(db)
	createGroupViaAPI(t, r, adminTok, "secret-group")

	tok := gateUserToken(t, r, db, authz, userSvc, "greader", []string{"group.read", "instance.read"}, 0)
	w := makeRequest(r, http.MethodGet, "/api/v1/groups", nil, tok)
	require.Equal(t, http.StatusOK, w.Code)
	require.NotContains(t, w.Body.String(), "secret-group", "R-08 group.read must not list all platform groups")
}

func TestWriteGate_InstanceKillCommandNeedNodes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)
	userSvc := service.NewUserService(db)
	authz := service.NewAuthzService(db)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, adminTok, "gw3")
	id := makeInstanceInGroup(t, db, node.ID, g, "kil1", model.InstanceStatusRunning)

	tok := gateUserToken(t, r, db, authz, userSvc, "vwr3",
		[]string{"instance.read", "terminal.access"}, g)

	w := makeRequest(r, http.MethodPost, "/api/v1/instances/"+itoa(id)+"/kill", nil, tok)
	require.Equal(t, http.StatusForbidden, w.Code, "R2-01 kill needs instance.operate: %s", w.Body.String())
	// 有 terminal.access 可发命令（非 403 权限层）
	w = makeRequest(r, http.MethodPost, "/api/v1/instances/"+itoa(id)+"/command",
		map[string]any{"command": "list"}, tok)
	require.NotEqual(t, http.StatusForbidden, w.Code, "terminal.access should allow command")
	// 无 terminal 仅 instance.read → command 403
	tok2 := gateUserToken(t, r, db, authz, userSvc, "vwr3b",
		[]string{"instance.read"}, g)
	w = makeRequest(r, http.MethodPost, "/api/v1/instances/"+itoa(id)+"/command",
		map[string]any{"command": "list"}, tok2)
	require.Equal(t, http.StatusForbidden, w.Code, "R2-01 command needs terminal/operate")
	w = makeRequest(r, http.MethodPut, "/api/v1/instances/"+itoa(id),
		map[string]any{"name": "x"}, tok2)
	require.Equal(t, http.StatusForbidden, w.Code, "R2-01 update needs instance.write")
}

func TestWriteGate_BackupWriteNeedsNodeAndScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)
	userSvc := service.NewUserService(db)
	authz := service.NewAuthzService(db)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, adminTok, "gwbk")
	id := makeInstanceInGroup(t, db, node.ID, g, "bk1", model.InstanceStatusStopped)
	// 植入一条备份
	require.NoError(t, db.Create(&model.Backup{
		InstanceID: id, Name: "b1", Status: model.BackupStatusCompleted,
	}).Error)
	var bk model.Backup
	require.NoError(t, db.Where("instance_id = ?", id).First(&bk).Error)

	// viewer 仅 backup.read + 组成员 → 不可 restore/delete/create
	tok := gateUserToken(t, r, db, authz, userSvc, "bkr",
		[]string{"backup.read", "instance.read"}, g)
	w := makeRequest(r, http.MethodPost, "/api/v1/instances/"+itoa(id)+"/backups",
		map[string]any{"name": "x"}, tok)
	require.Equal(t, http.StatusForbidden, w.Code, "R2-02 create needs backup.write")
	w = makeRequest(r, http.MethodPost, "/api/v1/backups/"+itoa(bk.ID)+"/restore", nil, tok)
	require.Equal(t, http.StatusForbidden, w.Code, "R2-02 restore needs backup.write")
	w = makeRequest(r, http.MethodDelete, "/api/v1/backups/"+itoa(bk.ID), nil, tok)
	require.Equal(t, http.StatusForbidden, w.Code, "R2-02 delete needs backup.write")

	// 有 backup.write 但不属于该组 → 404（范围校验）
	tokOut := gateUserToken(t, r, db, authz, userSvc, "bkrout",
		[]string{"backup.write", "instance.read"}, 0)
	w = makeRequest(r, http.MethodPost, "/api/v1/backups/"+itoa(bk.ID)+"/restore", nil, tokOut)
	require.Equal(t, http.StatusNotFound, w.Code, "R2-02 restore out-of-group: %s", w.Body.String())
}

func TestWriteGate_ConfigRollbackAndRead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)
	userSvc := service.NewUserService(db)
	authz := service.NewAuthzService(db)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, adminTok, "gwcfg")
	id := makeInstanceInGroup(t, db, node.ID, g, "cfg1", model.InstanceStatusRunning)

	// 空权限组成员 → 读配置 403（R-03 读侧）
	tokEmpty := gateUserToken(t, r, db, authz, userSvc, "cfgempty", nil, g)
	w := makeRequest(r, http.MethodGet, "/api/v1/instances/"+itoa(id)+"/configs", nil, tokEmpty)
	require.Equal(t, http.StatusForbidden, w.Code, "R2-03 empty perm config read: %s", w.Body.String())
	// 仅有 instance.read 可读（节点层通过；无 Worker 时业务层 422 可接受）
	tok := gateUserToken(t, r, db, authz, userSvc, "cfgr",
		[]string{"instance.read"}, g)
	w = makeRequest(r, http.MethodGet, "/api/v1/instances/"+itoa(id)+"/configs", nil, tok)
	require.NotEqual(t, http.StatusForbidden, w.Code, "instance.read may read configs")
	// 无写节点 → rollback 403
	w = makeRequest(r, http.MethodPost, "/api/v1/instances/"+itoa(id)+"/configs/rollback/server.properties",
		map[string]any{"versionId": 1}, tok)
	require.Equal(t, http.StatusForbidden, w.Code, "R2-03 rollback needs write nodes")
}

func TestSuperAdmin_BoundTemplateBlocksOverrideAndRebind(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	authz := service.NewAuthzService(db)
	perm := authz.Permissions()
	require.NoError(t, perm.SeedSystemRoles())
	userSvc := service.NewUserService(db)
	u, err := userSvc.Create("bindsuper", "password123", model.RoleMember, model.UserStatusActive)
	require.NoError(t, err)
	var pa model.RoleTemplate
	require.NoError(t, db.Where("key = ?", model.RoleKeyPlatformAdmin).First(&pa).Error)
	require.NoError(t, perm.BindUserRole(u.ID, pa.ID))
	// 写侧：覆盖拒绝
	err = perm.SetUserOverrides(u.ID, []model.UserPermissionOverride{{Node: "user.manage", Effect: "deny"}})
	require.ErrorIs(t, err, service.ErrSuperAdminLocked, "R-07b bound super cannot deny")
	// 写侧：改绑非超管模板拒绝
	var mv model.RoleTemplate
	require.NoError(t, db.Where("key = ?", model.RoleKeyMember).First(&mv).Error)
	err = perm.BindUserRole(u.ID, mv.ID)
	require.ErrorIs(t, err, service.ErrSuperAdminLocked, "R-07b bound super cannot rebind away")
}

func TestSuperAdmin_BindingTemplateSetsIsPlatformAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	authz := service.NewAuthzService(db)
	require.NoError(t, authz.Permissions().SeedSystemRoles())
	userSvc := service.NewUserService(db)
	u, err := userSvc.Create("tplsuper", "password123", model.RoleMember, model.UserStatusActive)
	require.NoError(t, err)
	var pa model.RoleTemplate
	require.NoError(t, db.Where("key = ?", model.RoleKeyPlatformAdmin).First(&pa).Error)
	require.NoError(t, authz.Permissions().BindUserRole(u.ID, pa.ID))
	access, err := authz.LoadUserAccess(u.ID)
	require.NoError(t, err)
	require.True(t, access.IsPlatformAdmin, "R-07 bound platform_admin template must set IsPlatformAdmin")
	require.True(t, access.HasNode("user.manage"))
}
