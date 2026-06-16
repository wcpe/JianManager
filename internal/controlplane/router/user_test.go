package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
