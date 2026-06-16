package router

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

// createGroupViaAPI 通过平台管理员 API 创建用户组，返回组 ID。
func createGroupViaAPI(t *testing.T, r *gin.Engine, adminToken, name string) uint {
	t.Helper()
	body := map[string]interface{}{"name": name}
	w := makeRequest(r, "POST", "/api/v1/groups", body, adminToken)
	require.Equalf(t, http.StatusCreated, w.Code, "创建用户组失败: %s", w.Body.String())
	g := parseJSON(t, w)
	return uint(g["id"].(float64))
}

// findUserIDByUsername 通过用户名查询用户 ID。
func findUserIDByUsername(t *testing.T, db *gorm.DB, username string) uint {
	t.Helper()
	var user model.User
	require.NoErrorf(t, db.Where("username = ?", username).First(&user).Error, "查询用户 %s 失败", username)
	return user.ID
}

// setGlobalRole 直接更新用户全局角色（测试构造用）。
func setGlobalRole(t *testing.T, db *gorm.DB, userID uint, role model.UserRole) {
	t.Helper()
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", userID).Update("role", role).Error)
}

// addMemberViaAPI 通过平台管理员 API 将用户加入组。
func addMemberViaAPI(t *testing.T, r *gin.Engine, adminToken string, groupID, userID uint, role model.GroupMemberRole) {
	t.Helper()
	body := map[string]interface{}{"userId": userID, "role": role}
	w := makeRequest(r, "POST", "/api/v1/groups/"+itoa(groupID)+"/members", body, adminToken)
	require.Equalf(t, http.StatusOK, w.Code, "添加组成员失败: %s", w.Body.String())
}

// createInstanceViaAPI 通过指定 token 创建实例并分配到组，返回实例 ID。
func createInstanceViaAPI(t *testing.T, r *gin.Engine, token string, nodeID, groupID uint) uint {
	t.Helper()
	body := map[string]interface{}{
		"nodeId":       nodeID,
		"name":         "inst-" + itoa(groupID),
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
		"groupId":      groupID,
	}
	w := makeRequest(r, "POST", "/api/v1/instances", body, token)
	require.Equalf(t, http.StatusCreated, w.Code, "创建实例失败: %s", w.Body.String())
	inst := parseJSON(t, w)
	return uint(inst["id"].(float64))
}

// setGroupQuota 直接更新组配额（测试构造用）。
func setGroupQuota(t *testing.T, db *gorm.DB, groupID uint, maxInstances, maxBots, maxStorageMB int) {
	t.Helper()
	err := db.Model(&model.GroupQuota{}).Where("group_id = ?", groupID).
		Updates(map[string]interface{}{
			"max_instances":  maxInstances,
			"max_bots":       maxBots,
			"max_storage_mb": maxStorageMB,
		}).Error
	require.NoError(t, err)
}

// createBotsInDB 直接向数据库插入指定数量的 Bot（关联到实例）。
func createBotsInDB(t *testing.T, db *gorm.DB, instanceID uint, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		b := &model.Bot{
			InstanceID: instanceID,
			Name:       "bot-" + itoa(instanceID) + "-" + itoa(uint(i)),
			Status:     model.BotStatusPending,
		}
		require.NoError(t, db.Create(b).Error)
	}
}
