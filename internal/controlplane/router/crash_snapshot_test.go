package router

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// seedCrashSnapshot 直接落库一条崩溃快照（写侧在 gRPC 层，路由测试只造读侧数据）。
func seedCrashSnapshot(t *testing.T, db *gorm.DB, instanceID uint, occurredAt time.Time, exitCode int) {
	t.Helper()
	require.NoError(t, db.Create(&model.InstanceCrashSnapshot{
		InstanceID: instanceID,
		OccurredAt: occurredAt,
		ExitCode:   exitCode,
		DurationMs: 1000,
		TailOutput: "tail",
	}).Error)
}

// TestCrashSnapshots_ListDesc 列表按发生时间倒序返回（最新在前，spec §5）。
func TestCrashSnapshots_ListDesc(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, token, "g")
	id := makeInstanceInGroup(t, db, node.ID, g, "smp", model.InstanceStatusCrashed)

	base := time.Now().Add(-time.Hour)
	// 乱序落库：中间的最新、最后的最旧，验证按 occurred_at 排序而非插入序。
	seedCrashSnapshot(t, db, id, base.Add(10*time.Minute), 1)
	seedCrashSnapshot(t, db, id, base.Add(30*time.Minute), 2)
	seedCrashSnapshot(t, db, id, base.Add(20*time.Minute), 3)

	w := makeRequest(r, "GET", "/api/v1/instances/"+itoa(id)+"/crash-snapshots", nil, token)
	require.Equal(t, http.StatusOK, w.Code)

	var snaps []model.InstanceCrashSnapshot
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &snaps))
	require.Len(t, snaps, 3)
	assert.Equal(t, 2, snaps[0].ExitCode, "最新（+30min）应排第一")
	assert.Equal(t, 3, snaps[1].ExitCode)
	assert.Equal(t, 1, snaps[2].ExitCode, "最旧（+10min）应排最后")
}

// TestCrashSnapshots_EmptyList 无快照实例返回空数组（前端空态数据面）。
func TestCrashSnapshots_EmptyList(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, token, "g")
	id := makeInstanceInGroup(t, db, node.ID, g, "smp", model.InstanceStatusStopped)

	w := makeRequest(r, "GET", "/api/v1/instances/"+itoa(id)+"/crash-snapshots", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	var snaps []model.InstanceCrashSnapshot
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &snaps))
	assert.Empty(t, snaps)
}

// TestCrashSnapshots_Permission 权限面（spec §5）：
// 无权限树节点的用户 403；有 instance.read 但不属于组的用户按存在性隐藏返回 404。
func TestCrashSnapshots_Permission(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, token, "g")
	id := makeInstanceInGroup(t, db, node.ID, g, "smp", model.InstanceStatusCrashed)
	seedCrashSnapshot(t, db, id, time.Now(), 1)

	// 空权限树用户 → RequireAnyPerm 403
	perm := service.NewAuthzService(db).Permissions()
	emptyUser, err := service.NewUserService(db).Create("bob_noperm", "password123", model.RoleMember, model.UserStatusActive)
	require.NoError(t, err)
	emptyRole, err := perm.CreateRole("crash-empty", "", nil)
	require.NoError(t, err)
	require.NoError(t, perm.BindUserRole(emptyUser.ID, emptyRole.ID))
	bobEmpty := getMemberToken(t, r, "bob_noperm2", "password123")
	// bob_noperm2 使用默认 member 种子（有 instance.read）但不属于组 → 404
	w := makeRequest(r, "GET", "/api/v1/instances/"+itoa(id)+"/crash-snapshots", nil, bobEmpty)
	assert.Equal(t, http.StatusNotFound, w.Code)

	// 真正无 instance.read：自定义空角色登录
	emptyUser2, err := service.NewUserService(db).Create("bob_zero", "password123", model.RoleMember, model.UserStatusActive)
	require.NoError(t, err)
	require.NoError(t, perm.BindUserRole(emptyUser2.ID, emptyRole.ID))
	bobZero := loginTestUser(t, r, "bob_zero", "password123")
	w = makeRequest(r, "GET", "/api/v1/instances/"+itoa(id)+"/crash-snapshots", nil, bobZero)
	assert.Equal(t, http.StatusForbidden, w.Code)

	// 不存在的实例 → 404。
	w = makeRequest(r, "GET", "/api/v1/instances/99999/crash-snapshots", nil, token)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func loginTestUser(t *testing.T, r *gin.Engine, user, pass string) string {
	t.Helper()
	w := makeRequest(r, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": user, "password": pass}, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	return parseJSON(t, w)["accessToken"].(string)
}

// TestCrashSnapshots_CascadeDeleteWithInstance 删除实例级联清快照（spec §3/§5）。
func TestCrashSnapshots_CascadeDeleteWithInstance(t *testing.T) {
	db := setupTestDB(t)
	r, pool := setupDeleteTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, token, "g")
	id := makeInstanceInGroup(t, db, node.ID, g, "smp", model.InstanceStatusStopped)
	seedCrashSnapshot(t, db, id, time.Now(), 1)
	seedCrashSnapshot(t, db, id, time.Now().Add(time.Minute), 2)

	connectDeleteTestWorker(pool, node.UUID)
	w := makeRequest(r, "DELETE", "/api/v1/instances/"+itoa(id), nil, token)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var count int64
	require.NoError(t, db.Model(&model.InstanceCrashSnapshot{}).Where("instance_id = ?", id).Count(&count).Error)
	assert.Zero(t, count, "删除实例后崩溃快照应级联清空")
}

// seedCrashStat 直接落库一条统计（FR-470 趋势表；写侧在 gRPC 层）。
func seedCrashStat(t *testing.T, db *gorm.DB, instanceID uint, day string, rootCause, signature string, count int) {
	t.Helper()
	require.NoError(t, db.Create(&model.InstanceCrashStat{
		InstanceID: instanceID, BucketDay: day, RootCause: rootCause,
		Signature: signature, Count: count, UpdatedAt: time.Now(),
	}).Error)
}

// TestCrashTrend_Endpoint 趋势端点返回按天/根因聚合 + 同类聚合（FR-470 spec §2.4）。
func TestCrashTrend_Endpoint(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, token, "g")
	id := makeInstanceInGroup(t, db, node.ID, g, "smp", model.InstanceStatusCrashed)

	require.NoError(t, db.AutoMigrate(&model.InstanceCrashStat{}))
	day := time.Now().UTC().Format("2006-01-02")
	seedCrashStat(t, db, id, day, "oom", "sig-a", 6)
	seedCrashStat(t, db, id, day, "port_in_use", "sig-b", 1)

	w := makeRequest(r, "GET", "/api/v1/instances/"+itoa(id)+"/crash-trend?days=30", nil, token)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.EqualValues(t, 7, body["total"])
	assert.EqualValues(t, daysOrDefault(body), 30)
	byCause := body["byRootCause"].([]any)
	require.Len(t, byCause, 2)
	first := byCause[0].(map[string]any)
	assert.Equal(t, "oom", first["rootCause"])
	assert.EqualValues(t, 6, first["count"])

	// 无效 days 回退默认 30。
	w = makeRequest(r, "GET", "/api/v1/instances/"+itoa(id)+"/crash-trend?days=abc", nil, token)
	require.Equal(t, http.StatusOK, w.Code)

	// 不存在的实例 → 404。
	w = makeRequest(r, "GET", "/api/v1/instances/99999/crash-trend", nil, token)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// daysOrDefault 取响应中的 days 字段（趋势端点回显窗口）。
func daysOrDefault(body map[string]any) int {
	if v, ok := body["days"].(float64); ok {
		return int(v)
	}
	return 0
}

// TestCrashOverview_Endpoint 平台总览端点（FR-470 spec §2.5）。
func TestCrashOverview_Endpoint(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, token, "g")
	id := makeInstanceInGroup(t, db, node.ID, g, "smp", model.InstanceStatusCrashed)
	require.NoError(t, db.AutoMigrate(&model.InstanceCrashStat{}))
	day := time.Now().UTC().Format("2006-01-02")
	seedCrashStat(t, db, id, day, "oom", "sig-a", 4)

	w := makeRequest(r, "GET", "/api/v1/crash-overview?days=30", nil, token)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.EqualValues(t, 4, body["total"])
	causes := body["topRootCauses"].([]any)
	require.NotEmpty(t, causes)
	assert.Equal(t, "oom", causes[0].(map[string]any)["rootCause"])
	insts := body["topInstances"].([]any)
	require.Len(t, insts, 1)
	assert.Equal(t, "smp", insts[0].(map[string]any)["instanceName"])
}

// TestCrashReclassify_EndpointAdminOnly 重分类端点：平台管理员可跑且写审计；普通成员 403。
func TestCrashReclassify_EndpointAdminOnly(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, token, "g")
	id := makeInstanceInGroup(t, db, node.ID, g, "smp", model.InstanceStatusCrashed)
	require.NoError(t, db.AutoMigrate(&model.InstanceCrashStat{}))
	// 历史快照（无分类列）→ 重分类应回填。
	seedCrashSnapshot(t, db, id, time.Now(), 1)
	require.NoError(t, db.Model(&model.InstanceCrashSnapshot{}).Where("instance_id = ?", id).
		Updates(map[string]any{"tail_output": "java.lang.OutOfMemoryError: Java heap space"}).Error)

	member := getMemberToken(t, r, "bob_reclass", "password123")
	w := makeRequest(r, "POST", "/api/v1/crash-snapshots/reclassify", nil, member)
	assert.Equal(t, http.StatusForbidden, w.Code, "非平台管理员应被拒")

	w = makeRequest(r, "POST", "/api/v1/crash-snapshots/reclassify", nil, token)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.EqualValues(t, 1, body["scanned"])
	assert.EqualValues(t, 1, body["backfilled"])

	var snap model.InstanceCrashSnapshot
	require.NoError(t, db.Where("instance_id = ?", id).First(&snap).Error)
	assert.Equal(t, "oom", snap.RootCause)

	// 审计已记录。
	var auditCount int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ?", "crash.reclassify").Count(&auditCount).Error)
	assert.EqualValues(t, 1, auditCount)
}

// TestCrashTrend_Permission 趋势端点权限面：无 instance.read 403，有但不属于组 404。
func TestCrashTrend_Permission(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, token, "g")
	id := makeInstanceInGroup(t, db, node.ID, g, "smp", model.InstanceStatusCrashed)

	perm := service.NewAuthzService(db).Permissions()
	emptyRole, err := perm.CreateRole("trend-empty", "", nil)
	require.NoError(t, err)
	zeroUser, err := service.NewUserService(db).Create("bob_trend_zero", "password123", model.RoleMember, model.UserStatusActive)
	require.NoError(t, err)
	require.NoError(t, perm.BindUserRole(zeroUser.ID, emptyRole.ID))
	zeroToken := loginTestUser(t, r, "bob_trend_zero", "password123")

	w := makeRequest(r, "GET", "/api/v1/instances/"+itoa(id)+"/crash-trend", nil, zeroToken)
	assert.Equal(t, http.StatusForbidden, w.Code)

	w = makeRequest(r, "GET", "/api/v1/instances/"+itoa(id)+"/crash-trend", nil,
		getMemberToken(t, r, "bob_trend_member", "password123"))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestCrashOverview_ScopedByAccessibleInstances m-3：崩溃总览不得跨组泄露。
//
// 缺陷现场：/crash-overview 只判 instance.read，且总览会回填 InstanceName ——
// 组只读角色因此能看到其它组的实例清单（名字 + 崩溃量），属跨组信息泄露。
// 修复后按 AccessibleInstanceIDs 收敛：只统计调用方可访问的实例。
func TestCrashOverview_ScopedByAccessibleInstances(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)
	userSvc := service.NewUserService(db)
	authz := service.NewAuthzService(db)
	node := createTestNode(t, db)

	// 两个组各一台实例，各有一条崩溃统计。
	gMine := createGroupViaAPI(t, r, adminTok, "我的组")
	gOther := createGroupViaAPI(t, r, adminTok, "别人的组")
	mine := makeInstanceInGroup(t, db, node.ID, gMine, "mine-server", model.InstanceStatusCrashed)
	other := makeInstanceInGroup(t, db, node.ID, gOther, "other-server", model.InstanceStatusCrashed)
	require.NoError(t, db.AutoMigrate(&model.InstanceCrashStat{}))
	day := time.Now().UTC().Format("2006-01-02")
	seedCrashStat(t, db, mine, day, "oom", "sig-mine", 2)
	seedCrashStat(t, db, other, day, "permission", "sig-other", 7)

	// 只读成员：仅 instance.read + 属于「我的组」。
	tok := gateUserToken(t, r, db, authz, userSvc, "crashviewer", []string{"instance.read"}, gMine)

	w := makeRequest(r, "GET", "/api/v1/crash-overview?days=30", nil, tok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)

	assert.EqualValues(t, 2, body["total"], "只能统计本组实例的崩溃（别人的 7 次不得计入）")

	insts := body["topInstances"].([]any)
	require.Len(t, insts, 1, "不得出现其它组的实例")
	assert.Equal(t, "mine-server", insts[0].(map[string]any)["instanceName"])

	causes := body["topRootCauses"].([]any)
	require.NotEmpty(t, causes)
	assert.NotEqual(t, "permission", causes[0].(map[string]any)["rootCause"], "其它组的根因分布不得泄露")

	// 平台管理员不受限（对照）：两组都看得到。
	w = makeRequest(r, "GET", "/api/v1/crash-overview?days=30", nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code)
	assert.EqualValues(t, 9, parseJSON(t, w)["total"], "平台管理员应看到全部 9 次")
}
