package router

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
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

// TestCrashSnapshots_Permission 权限面（spec §5）：无 instance:read 的用户 403；
// 不存在的实例 404（存在性隐藏）。
func TestCrashSnapshots_Permission(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, token, "g")
	id := makeInstanceInGroup(t, db, node.ID, g, "smp", model.InstanceStatusCrashed)
	seedCrashSnapshot(t, db, id, time.Now(), 1)

	// 不属于任何组的普通成员：无 instance:read → 403。
	bobToken := getMemberToken(t, r, "bob", "password123")
	w := makeRequest(r, "GET", "/api/v1/instances/"+itoa(id)+"/crash-snapshots", nil, bobToken)
	assert.Equal(t, http.StatusForbidden, w.Code)

	// 不存在的实例 → 404。
	w = makeRequest(r, "GET", "/api/v1/instances/99999/crash-snapshots", nil, token)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestCrashSnapshots_CascadeDeleteWithInstance 删除实例级联清快照（spec §3/§5）。
func TestCrashSnapshots_CascadeDeleteWithInstance(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, token, "g")
	id := makeInstanceInGroup(t, db, node.ID, g, "smp", model.InstanceStatusStopped)
	seedCrashSnapshot(t, db, id, time.Now(), 1)
	seedCrashSnapshot(t, db, id, time.Now().Add(time.Minute), 2)

	// 测试环境节点未连接：removeWorkerData 跳过 Worker 清理，记录删除照常走。
	w := makeRequest(r, "DELETE", "/api/v1/instances/"+itoa(id), nil, token)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var count int64
	require.NoError(t, db.Model(&model.InstanceCrashSnapshot{}).Where("instance_id = ?", id).Count(&count).Error)
	assert.Zero(t, count, "删除实例后崩溃快照应级联清空")
}
