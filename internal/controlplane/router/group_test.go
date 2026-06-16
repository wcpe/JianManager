package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGroup_Create_Success(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	body := map[string]interface{}{
		"name":        "测试用户组",
		"description": "用于测试",
	}
	w := makeRequest(r, "POST", "/api/v1/groups", body, token)
	assert.Equal(t, http.StatusCreated, w.Code)

	resp := parseJSON(t, w)
	assert.Equal(t, "测试用户组", resp["name"])
	assert.NotNil(t, resp["uuid"])
	assert.NotNil(t, resp["quota"])
}

func TestGroup_Create_InvalidRequest(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	body := map[string]interface{}{}
	w := makeRequest(r, "POST", "/api/v1/groups", body, token)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGroup_List_Empty(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/groups", nil, token)
	assert.Equal(t, http.StatusOK, w.Code)

	resp := parseJSONArray(t, w)
	assert.Len(t, resp, 0)
}

func TestGroup_Get_NotFound(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/groups/999", nil, token)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestGroup_AddMember_Success(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	// 创建用户组
	groupBody := map[string]interface{}{
		"name":        "测试组",
		"description": "成员管理测试",
	}
	w := makeRequest(r, "POST", "/api/v1/groups", groupBody, token)
	require.Equal(t, http.StatusCreated, w.Code)
	group := parseJSON(t, w)
	groupID := uint(group["id"].(float64))

	// 注册一个普通用户
	getMemberToken(t, r, "member1", "password123")

	// 查询用户 ID（通过管理员接口）
	w = makeRequest(r, "GET", "/api/v1/users", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	users := parseJSONArray(t, w)

	// 找到 member1 的 ID（跳过 admin）
	var memberID float64
	for _, u := range users {
		um := u.(map[string]interface{})
		if um["username"] == "member1" {
			memberID = um["id"].(float64)
			break
		}
	}
	require.Greater(t, memberID, float64(0), "未找到 member1")

	// 添加成员
	addBody := map[string]interface{}{
		"userId": uint(memberID),
		"role":   0,
	}
	w = makeRequest(r, "POST", "/api/v1/groups/"+itoa(groupID)+"/members", addBody, token)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestGroup_AddMember_AlreadyMember(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	groupBody := map[string]interface{}{"name": "重复成员测试组"}
	w := makeRequest(r, "POST", "/api/v1/groups", groupBody, token)
	require.Equal(t, http.StatusCreated, w.Code)
	group := parseJSON(t, w)
	groupID := uint(group["id"].(float64))

	getMemberToken(t, r, "member2", "password123")

	w = makeRequest(r, "GET", "/api/v1/users", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	users := parseJSONArray(t, w)

	var memberID float64
	for _, u := range users {
		um := u.(map[string]interface{})
		if um["username"] == "member2" {
			memberID = um["id"].(float64)
			break
		}
	}

	addBody := map[string]interface{}{"userId": uint(memberID), "role": 0}
	w = makeRequest(r, "POST", "/api/v1/groups/"+itoa(groupID)+"/members", addBody, token)
	assert.Equal(t, http.StatusOK, w.Code)

	// 再次添加应返回 409
	w = makeRequest(r, "POST", "/api/v1/groups/"+itoa(groupID)+"/members", addBody, token)
	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestGroup_Delete_Success(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	groupBody := map[string]interface{}{"name": "待删除组"}
	w := makeRequest(r, "POST", "/api/v1/groups", groupBody, token)
	require.Equal(t, http.StatusCreated, w.Code)
	group := parseJSON(t, w)
	groupID := uint(group["id"].(float64))

	w = makeRequest(r, "DELETE", "/api/v1/groups/"+itoa(groupID), nil, token)
	assert.Equal(t, http.StatusOK, w.Code)

	w = makeRequest(r, "GET", "/api/v1/groups/"+itoa(groupID), nil, token)
	assert.Equal(t, http.StatusNotFound, w.Code)
}
