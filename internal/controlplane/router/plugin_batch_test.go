package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestPluginBatchDeploy_RouteScopesTargets(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminToken := getAdminToken(t, r)
	assetID := uploadPluginAssetForBatch(t, r, adminToken)
	node := createTestNode(t, db)

	groupA := createGroupViaAPI(t, r, adminToken, "组A")
	groupB := createGroupViaAPI(t, r, adminToken, "组B")
	aliceToken := getMemberToken(t, r, "alice-plugin", "password123")
	aliceID := findUserIDByUsername(t, db, "alice-plugin")
	addMemberViaAPI(t, r, adminToken, groupA, aliceID, model.GroupMemberRoleMember)

	idA := makePluginBatchInstance(t, db, node.ID, groupA, "batch-a")
	idB := makePluginBatchInstance(t, db, node.ID, groupB, "batch-b")
	body := map[string]interface{}{"assetIds": []uint{assetID}, "ids": []uint{idA, idB}}
	w := makeRequest(r, http.MethodPost, "/api/v1/plugins/batch-deploy", body, aliceToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	m := parseJSON(t, w)
	assert.Equal(t, float64(2), m["total"])
	assert.Equal(t, float64(0), m["success"])
	assert.Equal(t, float64(1), m["failed"])  // 有权实例因测试环境无 Worker 连接而失败
	assert.Equal(t, float64(1), m["skipped"]) // 越权实例被收敛为 skipped，不泄露存在性
}

func TestPluginBatchDeploy_Validation(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, http.MethodPost, "/api/v1/plugins/batch-deploy", map[string]interface{}{"ids": []uint{1}}, token)
	require.Equal(t, http.StatusBadRequest, w.Code)

	w = makeRequest(r, http.MethodPost, "/api/v1/plugins/batch-deploy", map[string]interface{}{"assetIds": []uint{1}}, token)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func uploadPluginAssetForBatch(t *testing.T, r http.Handler, token string) uint {
	t.Helper()
	w := uploadAsset(t, r, token, "plugin", "BatchPlugin.jar", []byte("plugin-bytes"), nil)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	return uint(parseJSON(t, w)["id"].(float64))
}

func makePluginBatchInstance(t *testing.T, db *gorm.DB, nodeID, groupID uint, name string) uint {
	t.Helper()
	inst := &model.Instance{
		UUID:         name + "-uuid",
		NodeID:       nodeID,
		Name:         name,
		Type:         model.InstanceTypeMinecraftJava,
		Role:         model.InstanceRoleBackend,
		ProcessType:  model.ProcessTypeDirect,
		StartCommand: "java -jar server.jar",
		WorkDir:      "/srv/" + name,
		Status:       model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(inst).Error)
	require.NoError(t, db.Create(&model.GroupInstance{GroupID: groupID, InstanceID: inst.ID}).Error)
	return inst.ID
}
