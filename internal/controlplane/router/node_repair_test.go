package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestNodeRepair_Suspects_ListsRenamed 去重改名节点经 /nodes/repair/suspects 暴露（ADR-039 §2）。
func TestNodeRepair_Suspects_ListsRenamed(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	createTestNodeWithSuffix(t, db, "edge-x-dup-3")

	w := makeRequest(r, http.MethodGet, "/api/v1/nodes/repair/suspects", nil, token)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	arr := parseJSONArray(t, w)
	require.Len(t, arr, 1, "去重改名节点应被列为疑似坏节点")
}

// TestNodeRepair_Reenroll_RequiresConfirm 重新 enroll 未确认回 409 CONFIRM_REQUIRED（ADR-039 §2）。
func TestNodeRepair_Reenroll_RequiresConfirm(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)

	w := makeRequest(r, http.MethodPost, "/api/v1/nodes/"+itoa(node.ID)+"/reenroll",
		map[string]any{"confirm": false}, token)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())

	// 身份未变。
	var fromDB model.Node
	require.NoError(t, db.First(&fromDB, node.ID).Error)
	require.Equal(t, node.UUID, fromDB.UUID)
}

// TestNodeRepair_Reenroll_ConfirmedRotatesAndAudits 确认后轮换身份并写审计（ADR-039 §2，FR-015）。
func TestNodeRepair_Reenroll_ConfirmedRotatesAndAudits(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	oldUUID := node.UUID

	w := makeRequest(r, http.MethodPost, "/api/v1/nodes/"+itoa(node.ID)+"/reenroll",
		map[string]any{"confirm": true}, token)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var fromDB model.Node
	require.NoError(t, db.First(&fromDB, node.ID).Error)
	require.NotEqual(t, oldUUID, fromDB.UUID, "确认后应轮换 UUID")

	// 审计已记录 node.reenroll。
	var auditCnt int64
	db.Model(&model.AuditLog{}).Where("action = ?", "node.reenroll").Count(&auditCnt)
	require.Equal(t, int64(1), auditCnt, "重新 enroll 应入审计")
}

// TestNodeRepair_PurgeOrphans_Confirmed 清理孤儿并写审计（ADR-039 §2）。
func TestNodeRepair_PurgeOrphans_Confirmed(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	require.NoError(t, db.Create(&model.NodeJDK{NodeID: node.ID, Vendor: "temurin", MajorVersion: 21, Version: "21", Arch: "amd64", Path: "/jdk"}).Error)

	w := makeRequest(r, http.MethodPost, "/api/v1/nodes/"+itoa(node.ID)+"/purge-orphans",
		map[string]any{"confirm": true}, token)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var jdkCnt int64
	db.Model(&model.NodeJDK{}).Where("node_id = ?", node.ID).Count(&jdkCnt)
	require.Equal(t, int64(0), jdkCnt, "孤立 JDK 应被清理")
}

// TestNodeRepair_Forbidden_NonAdmin 非平台管理员访问修复入口回 403（ADR-039 §2，限平台管理员）。
func TestNodeRepair_Forbidden_NonAdmin(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	_ = getAdminToken(t, r) // 先建平台管理员，使后续注册者为普通成员
	memberToken := getMemberToken(t, r, "bob", "password123")

	w := makeRequest(r, http.MethodGet, "/api/v1/nodes/repair/suspects", nil, memberToken)
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}
