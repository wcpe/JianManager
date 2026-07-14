package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestUser_List_Success(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/users", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)

	users := parseJSONArray(t, w)
	require.Len(t, users, 1) // admin
	assert.Equal(t, "admin", users[0].(map[string]interface{})["username"])
}

func TestUser_List_WithMembers(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	getMemberToken(t, r, "user1", "password123")
	getMemberToken(t, r, "user2", "password123")

	w := makeRequest(r, "GET", "/api/v1/users", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)

	users := parseJSONArray(t, w)
	assert.Len(t, users, 3) // admin + user1 + user2
}

// seedUsers 直接落库若干用户（绕过注册接口的用户名规则，便于覆盖含 _/% 的转义用例）。
func seedUsers(t *testing.T, db *gorm.DB, usernames ...string) {
	t.Helper()
	for _, name := range usernames {
		require.NoError(t, db.Create(&model.User{Username: name, Password: "x"}).Error)
	}
}

// usernamesOf 提取裸数组响应中的 username 序列（保持响应顺序）。
func usernamesOf(rows []interface{}) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.(map[string]interface{})["username"].(string)
	}
	return out
}

// TestUser_List_QFuzzy_BareArray 无 limit 带 q：仍返回裸数组（旧形态兼容），q 模糊命中且按 username 升序（FR-336）。
func TestUser_List_QFuzzy_BareArray(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	seedUsers(t, db, "alina", "bob", "alice")

	w := makeRequest(r, "GET", "/api/v1/users?q=ali", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)

	rows := parseJSONArray(t, w)
	assert.Equal(t, []string{"alice", "alina"}, usernamesOf(rows))
}

// TestUser_List_QEscapesLikeWildcards q 中的 %/_ 按字面匹配而非通配符（FR-336）。
func TestUser_List_QEscapesLikeWildcards(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	seedUsers(t, db, "user_one", "userXone")

	w := makeRequest(r, "GET", "/api/v1/users?q=user_", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, []string{"user_one"}, usernamesOf(parseJSONArray(t, w)), "_ 应按字面匹配，不吞任意单字符")

	w = makeRequest(r, "GET", "/api/v1/users?q=%25", nil, token) // q=%
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, parseJSONArray(t, w), "%% 应按字面匹配，无用户名含 %% 时命中为空")
}

// TestUser_List_Envelope_Pagination 带 limit 返回信封：窗口正确、total 为全量命中数、排序稳定（FR-336）。
func TestUser_List_Envelope_Pagination(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	seedUsers(t, db, "bob", "carol", "dave", "erin") // + admin = 5

	w := makeRequest(r, "GET", "/api/v1/users?limit=2", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
	resp := parseJSON(t, w)
	assert.Equal(t, float64(5), resp["total"])
	assert.Equal(t, float64(2), resp["limit"])
	assert.Equal(t, float64(0), resp["offset"])
	items := resp["items"].([]interface{})
	assert.Equal(t, []string{"admin", "bob"}, usernamesOf(items))

	w = makeRequest(r, "GET", "/api/v1/users?limit=2&offset=2", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
	resp = parseJSON(t, w)
	assert.Equal(t, float64(5), resp["total"])
	assert.Equal(t, float64(2), resp["offset"])
	assert.Equal(t, []string{"carol", "dave"}, usernamesOf(resp["items"].([]interface{})))
}

// TestUser_List_Envelope_QTotalSameSource 信封 total 与 q 条件同源（非全表计数）（FR-336）。
func TestUser_List_Envelope_QTotalSameSource(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	seedUsers(t, db, "alina", "alice", "bob")

	w := makeRequest(r, "GET", "/api/v1/users?q=ali&limit=1", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
	resp := parseJSON(t, w)
	assert.Equal(t, float64(2), resp["total"], "total 应为 q 命中总数而非全表")
	assert.Equal(t, []string{"alice"}, usernamesOf(resp["items"].([]interface{})))
}

// TestUser_List_Envelope_OffsetBeyond offset 越界：items 空、total 不受影响（FR-336）。
func TestUser_List_Envelope_OffsetBeyond(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	seedUsers(t, db, "bob")

	w := makeRequest(r, "GET", "/api/v1/users?limit=10&offset=999", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
	resp := parseJSON(t, w)
	assert.Equal(t, float64(2), resp["total"])
	assert.Empty(t, resp["items"].([]interface{}))
	assert.Equal(t, float64(999), resp["offset"])
}

// TestUser_List_LimitClampAndNegativeOffset limit 钳制 [1,500] 且回显生效值；offset 负值归 0（FR-336）。
func TestUser_List_LimitClampAndNegativeOffset(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/users?limit=1000&offset=-5", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
	resp := parseJSON(t, w)
	assert.Equal(t, float64(500), resp["limit"], ">500 应钳制为 500")
	assert.Equal(t, float64(0), resp["offset"], "负 offset 应归 0")

	w = makeRequest(r, "GET", "/api/v1/users?limit=0", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
	resp = parseJSON(t, w)
	assert.Equal(t, float64(1), resp["limit"], "<1 应钳制为 1")
	assert.Len(t, resp["items"].([]interface{}), 1)
}

// TestUser_List_InvalidParams400 limit/offset 非法整数回 400 INVALID_REQUEST（FR-336）。
func TestUser_List_InvalidParams400(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	for _, path := range []string{"/api/v1/users?limit=abc", "/api/v1/users?limit=10&offset=xyz", "/api/v1/users?limit="} {
		w := makeRequest(r, "GET", path, nil, token)
		assert.Equal(t, http.StatusBadRequest, w.Code, path)
		assert.Equal(t, "INVALID_REQUEST", parseJSON(t, w)["error"], path)
	}
}

// TestUser_List_NoLimit_KeepsBareArray 无 limit 参数（含带 offset）仍返回裸数组，旧调用方零改动不破（FR-002 兼容）。
func TestUser_List_NoLimit_KeepsBareArray(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	seedUsers(t, db, "bob")

	w := makeRequest(r, "GET", "/api/v1/users?offset=1", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
	rows := parseJSONArray(t, w)
	assert.Equal(t, []string{"admin", "bob"}, usernamesOf(rows), "无 limit 时 offset 不生效、全量裸数组")
}

func TestUser_Get_Success(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/users", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	users := parseJSONArray(t, w)
	adminID := uint(users[0].(map[string]interface{})["id"].(float64))

	w = makeRequest(r, "GET", "/api/v1/users/"+itoa(adminID), nil, token)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := parseJSON(t, w)
	assert.Equal(t, "admin", resp["username"])
	assert.Equal(t, float64(10), resp["role"]) // RolePlatformAdmin = 10
}

func TestUser_Get_NotFound(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/users/999", nil, token)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestUser_Delete_Success(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	getMemberToken(t, r, "deleteme", "password123")

	w := makeRequest(r, "GET", "/api/v1/users", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	users := parseJSONArray(t, w)

	var targetID float64
	for _, u := range users {
		um := u.(map[string]interface{})
		if um["username"] == "deleteme" {
			targetID = um["id"].(float64)
			break
		}
	}
	require.Greater(t, targetID, float64(0))

	w = makeRequest(r, "DELETE", "/api/v1/users/"+itoa(uint(targetID)), nil, token)
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestUser_Update_ResetPassword 管理员重置用户密码后，旧密码失效、新密码可登录（FR-156 验收）。
func TestUser_Update_ResetPassword(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	getMemberToken(t, r, "resetme", "oldpassword123")

	w := makeRequest(r, "GET", "/api/v1/users", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	var uid uint
	for _, u := range parseJSONArray(t, w) {
		um := u.(map[string]interface{})
		if um["username"] == "resetme" {
			uid = uint(um["id"].(float64))
		}
	}
	require.Greater(t, uid, uint(0))

	w = makeRequest(r, "PUT", "/api/v1/users/"+itoa(uid), map[string]any{"password": "newpassword456"}, token)
	require.Equal(t, http.StatusOK, w.Code)

	wOld := makeRequest(r, "POST", "/api/v1/auth/login", map[string]any{"username": "resetme", "password": "oldpassword123"}, "")
	assert.NotEqual(t, http.StatusOK, wOld.Code, "旧密码应失效")
	wNew := makeRequest(r, "POST", "/api/v1/auth/login", map[string]any{"username": "resetme", "password": "newpassword456"}, "")
	assert.Equal(t, http.StatusOK, wNew.Code, "新密码应可登录")
}

// TestUser_Update_RejectShortPassword 重置密码长度不足 8 时被路由 binding 拒绝（与初始化/创建一致）。
func TestUser_Update_RejectShortPassword(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	getMemberToken(t, r, "shortpw", "password123")

	w := makeRequest(r, "GET", "/api/v1/users", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	var uid uint
	for _, u := range parseJSONArray(t, w) {
		um := u.(map[string]interface{})
		if um["username"] == "shortpw" {
			uid = uint(um["id"].(float64))
		}
	}
	require.Greater(t, uid, uint(0))

	w = makeRequest(r, "PUT", "/api/v1/users/"+itoa(uid), map[string]any{"password": "short"}, token)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}
