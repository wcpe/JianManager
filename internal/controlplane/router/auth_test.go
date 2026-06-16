package router

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetup_CreateAdmin_Success(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)

	body := map[string]string{"username": "admin", "password": "password123"}
	w := makeRequest(r, "POST", "/api/v1/setup", body, "")

	assert.Equal(t, http.StatusCreated, w.Code)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp["accessToken"])
	assert.NotEmpty(t, resp["refreshToken"])
}

func TestSetup_CreateAdmin_AlreadyExists(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)

	body := map[string]string{"username": "admin", "password": "password123"}
	w := makeRequest(r, "POST", "/api/v1/setup", body, "")
	assert.Equal(t, http.StatusCreated, w.Code)

	w = makeRequest(r, "POST", "/api/v1/setup", body, "")
	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestSetup_Status(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)

	w := makeRequest(r, "GET", "/api/v1/setup/status", nil, "")
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["setupRequired"])

	_ = getAdminToken(t, r)

	w = makeRequest(r, "GET", "/api/v1/setup/status", nil, "")
	assert.Equal(t, http.StatusOK, w.Code)

	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["setupRequired"])
}

func TestAuth_Register_Success(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)

	body := map[string]string{"username": "testuser", "password": "password123"}
	w := makeRequest(r, "POST", "/api/v1/auth/register", body, "")

	assert.Equal(t, http.StatusCreated, w.Code)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "testuser", resp["username"])
	assert.NotEmpty(t, resp["id"])
}

func TestAuth_Register_DuplicateUsername(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)

	body := map[string]string{"username": "testuser", "password": "password123"}
	w := makeRequest(r, "POST", "/api/v1/auth/register", body, "")
	assert.Equal(t, http.StatusCreated, w.Code)

	w = makeRequest(r, "POST", "/api/v1/auth/register", body, "")
	assert.Equal(t, http.StatusConflict, w.Code)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "USER_EXISTS", resp["error"])
}

func TestAuth_Register_InvalidRequest(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)

	body := map[string]string{"username": "ab", "password": "12345"}
	w := makeRequest(r, "POST", "/api/v1/auth/register", body, "")
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestAuth_Login_Success(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)

	regBody := map[string]string{"username": "testuser", "password": "password123"}
	w := makeRequest(r, "POST", "/api/v1/auth/register", regBody, "")
	require.Equal(t, http.StatusCreated, w.Code)

	w = makeRequest(r, "POST", "/api/v1/auth/login", regBody, "")
	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp["accessToken"])
	assert.NotEmpty(t, resp["refreshToken"])
}

func TestAuth_Login_WrongPassword(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)

	regBody := map[string]string{"username": "testuser", "password": "password123"}
	w := makeRequest(r, "POST", "/api/v1/auth/register", regBody, "")
	require.Equal(t, http.StatusCreated, w.Code)

	loginBody := map[string]string{"username": "testuser", "password": "wrongpassword"}
	w = makeRequest(r, "POST", "/api/v1/auth/login", loginBody, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAuth_Login_UserNotFound(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)

	body := map[string]string{"username": "nonexistent", "password": "password123"}
	w := makeRequest(r, "POST", "/api/v1/auth/login", body, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAuth_Refresh_Success(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)

	body := map[string]string{"username": "testuser", "password": "password123"}
	w := makeRequest(r, "POST", "/api/v1/auth/register", body, "")
	require.Equal(t, http.StatusCreated, w.Code)

	w = makeRequest(r, "POST", "/api/v1/auth/login", body, "")
	require.Equal(t, http.StatusOK, w.Code)

	var loginResp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &loginResp))

	refreshBody := map[string]string{"refreshToken": loginResp["refreshToken"].(string)}
	w = makeRequest(r, "POST", "/api/v1/auth/refresh", refreshBody, "")
	assert.Equal(t, http.StatusOK, w.Code)

	var refreshResp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &refreshResp))
	assert.NotEmpty(t, refreshResp["accessToken"])
	assert.NotEmpty(t, refreshResp["refreshToken"])
}

func TestAuth_Refresh_InvalidToken(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)

	body := map[string]string{"refreshToken": "invalid-token"}
	w := makeRequest(r, "POST", "/api/v1/auth/refresh", body, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestProtectedEndpoint_NoToken(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)

	w := makeRequest(r, "GET", "/api/v1/nodes", nil, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestProtectedEndpoint_InvalidToken(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)

	w := makeRequest(r, "GET", "/api/v1/nodes", nil, "invalid-token")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAdminEndpoint_MemberForbidden(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)

	_ = getAdminToken(t, r)
	memberToken := getMemberToken(t, r, "member1", "password123")

	w := makeRequest(r, "GET", "/api/v1/users", nil, memberToken)
	assert.Equal(t, http.StatusForbidden, w.Code)
}
