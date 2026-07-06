package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func instanceUUIDByID(t *testing.T, db *gorm.DB, id uint) string {
	t.Helper()
	var inst model.Instance
	require.NoError(t, db.First(&inst, id).Error)
	return inst.UUID
}

func TestBusinessEvents_FilterByAccessibleInstances(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	admin := getAdminToken(t, r)
	node := createTestNode(t, db)
	groupA := createGroupViaAPI(t, r, admin, "biz-a")
	groupB := createGroupViaAPI(t, r, admin, "biz-b")
	instA := createInstanceViaAPI(t, r, admin, node.ID, groupA)
	instB := createInstanceViaAPI(t, r, admin, node.ID, groupB)

	memberToken := getMemberToken(t, r, "biz-member", "password123")
	memberID := findUserIDByUsername(t, db, "biz-member")
	addMemberViaAPI(t, r, admin, groupA, memberID, model.GroupMemberRoleMember)

	instAUUID := instanceUUIDByID(t, db, instA)
	instBUUID := instanceUUIDByID(t, db, instB)
	require.NoError(t, db.Create(&model.BusinessEvent{
		Domain:       "inventory",
		DedupKey:     "a",
		Action:       "DROP",
		NodeUUID:     node.UUID,
		InstanceUUID: instAUUID,
		PayloadJSON:  `{"domain":"inventory"}`,
	}).Error)
	require.NoError(t, db.Create(&model.BusinessEvent{
		Domain:       "inventory",
		DedupKey:     "b",
		Action:       "DROP",
		NodeUUID:     node.UUID,
		InstanceUUID: instBUUID,
		PayloadJSON:  `{"domain":"inventory"}`,
	}).Error)

	memberResp := makeRequest(r, http.MethodGet, "/api/v1/business/events?domain=inventory", nil, memberToken)
	require.Equal(t, http.StatusOK, memberResp.Code, memberResp.Body.String())
	memberEvents := parseJSON(t, memberResp)["events"].([]interface{})
	require.Len(t, memberEvents, 1)
	assert.Equal(t, instAUUID, memberEvents[0].(map[string]interface{})["instanceUuid"])

	adminResp := makeRequest(r, http.MethodGet, "/api/v1/business/events?domain=inventory", nil, admin)
	require.Equal(t, http.StatusOK, adminResp.Code, adminResp.Body.String())
	assert.Len(t, parseJSON(t, adminResp)["events"].([]interface{}), 2)
}

func TestBusinessEconomyMirror_FilterByAccessibleInstances(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	admin := getAdminToken(t, r)
	node := createTestNode(t, db)
	groupA := createGroupViaAPI(t, r, admin, "mirror-a")
	groupB := createGroupViaAPI(t, r, admin, "mirror-b")
	instA := createInstanceViaAPI(t, r, admin, node.ID, groupA)
	instB := createInstanceViaAPI(t, r, admin, node.ID, groupB)

	memberToken := getMemberToken(t, r, "mirror-member", "password123")
	memberID := findUserIDByUsername(t, db, "mirror-member")
	addMemberViaAPI(t, r, admin, groupA, memberID, model.GroupMemberRoleMember)

	instAUUID := instanceUUIDByID(t, db, instA)
	instBUUID := instanceUUIDByID(t, db, instB)
	require.NoError(t, db.Create(&model.EconomyBalanceMirror{
		NodeUUID: node.UUID, InstanceUUID: instAUUID, ZoneID: "a", PlayerName: "Steve", Currency: "coin", Balance: "10",
	}).Error)
	require.NoError(t, db.Create(&model.EconomyBalanceMirror{
		NodeUUID: node.UUID, InstanceUUID: instBUUID, ZoneID: "b", PlayerName: "Alex", Currency: "coin", Balance: "20",
	}).Error)

	w := makeRequest(r, http.MethodGet, "/api/v1/business/economy/mirror?currency=coin", nil, memberToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	rows := parseJSON(t, w)["balances"].([]interface{})
	require.Len(t, rows, 1)
	assert.Equal(t, "Steve", rows[0].(map[string]interface{})["playerName"])
}
