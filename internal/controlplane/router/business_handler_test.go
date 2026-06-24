package router

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestBusinessDispatch_Write_Forbidden 无可访问组的成员对写动作被拒（缺 instance:business:write，FR-121）。
func TestBusinessDispatch_Write_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	admin := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, admin, "g1")
	instID := createInstanceViaAPI(t, r, admin, node.ID, g)

	// 注册一个不属于任何组的成员 → HasPermission(instance:business:write)=false。
	member := getMemberToken(t, r, "nogroup", "password123")

	body := map[string]any{"domain": "economy", "action": "deposit", "payload": `{"player":"a","amount":"10"}`, "write": true, "operationId": "op-forbidden"}
	w := makeRequest(r, "POST", "/api/v1/instances/"+itoa(instID)+"/business", body, member)
	require.Equal(t, http.StatusForbidden, w.Code, "无组成员的业务写应 403")

	// 未记 business.write 审计（被权限拦在下发前）。
	assert.Equal(t, int64(0), countAudit(t, db, "business.write"))
}

// TestBusinessDispatch_Write_AdminDegradeAndAudit 平台管理员写动作：节点未连降级（200+available=false）+ 记 business.write 审计（FR-121）。
func TestBusinessDispatch_Write_AdminDegradeAndAudit(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	admin := getAdminToken(t, r)
	node := createTestNode(t, db) // 仅入库，无 gRPC 连接 → 下发降级
	g := createGroupViaAPI(t, r, admin, "g1")
	instID := createInstanceViaAPI(t, r, admin, node.ID, g)

	body := map[string]any{
		"domain": "economy", "action": "deposit",
		"payload":     `{"player":"alice","currency":"coin","amount":"100"}`,
		"write":       true,
		"operationId": "op-abc-123",
		"reason":      "活动补偿",
	}
	w := makeRequest(r, "POST", "/api/v1/instances/"+itoa(instID)+"/business", body, admin)
	require.Equalf(t, http.StatusOK, w.Code, "写动作应 200 降级而非 5xx: %s", w.Body.String())
	res := parseJSON(t, w)
	assert.Equal(t, false, res["available"], "节点未连接应降级 available=false")
	assert.NotEmpty(t, res["error"])

	// 记了一条 business.write 审计，detail 含 operationId/reason/domain/action。
	var logs []model.AuditLog
	require.NoError(t, db.Where("action = ?", "business.write").Find(&logs).Error)
	require.Len(t, logs, 1, "写动作应恰好记一条 business.write 审计")
	assert.Equal(t, "instance", logs[0].TargetType)
	assert.Equal(t, itoa(instID), logs[0].TargetID)
	var detail map[string]any
	require.NoError(t, json.Unmarshal([]byte(logs[0].Detail), &detail))
	assert.Equal(t, "economy", detail["domain"])
	assert.Equal(t, "deposit", detail["action"])
	assert.Equal(t, "op-abc-123", detail["operationId"])
	assert.Equal(t, "活动补偿", detail["reason"])
}

// TestBusinessDispatch_Read_NoBusinessWriteAudit 读动作（write=false）走 instance:operate，不记 business.write 审计（FR-121）。
func TestBusinessDispatch_Read_NoBusinessWriteAudit(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	admin := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, admin, "g1")
	instID := createInstanceViaAPI(t, r, admin, node.ID, g)

	body := map[string]any{"domain": "economy", "action": "balance", "payload": `{"player":"alice"}`}
	w := makeRequest(r, "POST", "/api/v1/instances/"+itoa(instID)+"/business", body, admin)
	require.Equalf(t, http.StatusOK, w.Code, "读动作应 200 降级: %s", w.Body.String())
	res := parseJSON(t, w)
	assert.Equal(t, false, res["available"], "节点未连接降级")

	assert.Equal(t, int64(0), countAudit(t, db, "business.write"), "读动作不应记 business.write")
}

// countAudit 统计指定 action 的审计条数。
func countAudit(t *testing.T, db *gorm.DB, action string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&model.AuditLog{}).Where("action = ?", action).Count(&n).Error)
	return n
}
