package router

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 配置分层覆盖路由端到端测试（§7）。
//
// 这里走 HTTP 层，验证：路由可达、门禁生效、错误码映射正确、有效值预览返回 traced。

// TestConfigFiles_CRUDAndLayers 覆盖§7.1 / §7.2 的完整链路。
func TestConfigFiles_CRUDAndLayers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)

	// 创建配置文件。
	w := makeRequest(r, http.MethodPost, "/api/v1/config-files", map[string]any{
		"name":        "server.properties",
		"format":      "properties",
		"description": "主服配置",
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	created := parseJSON(t, w)
	fileID := uint(created["id"].(float64))
	require.NotZero(t, fileID)

	// 列表。
	w = makeRequest(r, http.MethodGet, "/api/v1/config-files", nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	list := parseJSON(t, w)
	files, ok := list["files"].([]interface{})
	require.True(t, ok)
	require.Len(t, files, 1)

	// 详情。
	w = makeRequest(r, http.MethodGet, "/api/v1/config-files/"+itoa(fileID), nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// 重复创建 → 409。
	w = makeRequest(r, http.MethodPost, "/api/v1/config-files", map[string]any{
		"name": "server.properties", "format": "properties",
	}, adminTok)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	require.Equal(t, "ALREADY_EXISTS", parseJSON(t, w)["error"])

	// 改 format → 400（创建后不可改）。
	w = makeRequest(r, http.MethodPut, "/api/v1/config-files/"+itoa(fileID), map[string]any{
		"name": "server.properties", "format": "yaml",
	}, adminTok)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Equal(t, "INVALID_REQUEST", parseJSON(t, w)["error"])

	// 保存实例层版本需要真实实例：造一个。
	node := createTestNode(t, db)
	inst := model.Instance{Name: "cfg-srv", UUID: "uuid-cfg-srv", NodeID: node.ID, WorkDir: t.TempDir()}
	require.NoError(t, db.Create(&inst).Error)

	w = makeRequest(r, http.MethodPost, "/api/v1/config-files/"+itoa(fileID)+"/layers", map[string]any{
		"scopeType": "instance", "scopeId": inst.ID, "content": "server-port=25565\nmotd=hello\n",
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	ver := parseJSON(t, w)
	require.Equal(t, float64(1), ver["versionNo"])

	// 层列表。
	w = makeRequest(r, http.MethodGet, "/api/v1/config-files/"+itoa(fileID)+"/layers", nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	layers := parseJSON(t, w)["layers"].([]interface{})
	require.Len(t, layers, 1)

	// 历史。
	w = makeRequest(r, http.MethodGet,
		"/api/v1/config-files/"+itoa(fileID)+"/layers/instance/"+itoa(inst.ID)+"/history", nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	versions := parseJSON(t, w)["versions"].([]interface{})
	require.Len(t, versions, 1)

	// 有效值预览。
	w = makeRequest(r, http.MethodGet,
		"/api/v1/config-files/"+itoa(fileID)+"/effective?instanceId="+itoa(inst.ID), nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	preview := parseJSON(t, w)
	require.Contains(t, preview["content"], "server-port=25565")
	require.NotEmpty(t, preview["effectiveHash"])
	traced, ok := preview["traced"].(map[string]interface{})
	require.True(t, ok)
	entry, ok := traced["server-port"].(map[string]interface{})
	require.True(t, ok, "traced 必须包含 server-port")
	source := entry["source"].(map[string]interface{})
	require.Equal(t, "instance", source["level"])
	require.Equal(t, float64(inst.ID), source["id"])

	// 删除层。
	w = makeRequest(r, http.MethodDelete,
		"/api/v1/config-files/"+itoa(fileID)+"/layers/instance/"+itoa(inst.ID), nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// 再删 → 404。
	w = makeRequest(r, http.MethodDelete,
		"/api/v1/config-files/"+itoa(fileID)+"/layers/instance/"+itoa(inst.ID), nil, adminTok)
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())

	// 软删配置文件。
	w = makeRequest(r, http.MethodDelete, "/api/v1/config-files/"+itoa(fileID), nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// 详情 → 404。
	w = makeRequest(r, http.MethodGet, "/api/v1/config-files/"+itoa(fileID), nil, adminTok)
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}

// TestConfigFiles_InvalidRequests 覆盖参数校验与错误码映射。
func TestConfigFiles_InvalidRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)

	// 非法格式。
	w := makeRequest(r, http.MethodPost, "/api/v1/config-files", map[string]any{
		"name": "a.toml", "format": "toml",
	}, adminTok)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())

	// 非法 id。
	w = makeRequest(r, http.MethodGet, "/api/v1/config-files/abc", nil, adminTok)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	// 不存在的配置文件。
	w = makeRequest(r, http.MethodGet, "/api/v1/config-files/9999", nil, adminTok)
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())

	// 有效值预览缺 instanceId。
	w = makeRequest(r, http.MethodPost, "/api/v1/config-files", map[string]any{
		"name": "b.yml", "format": "yaml",
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	fileID := uint(parseJSON(t, w)["id"].(float64))

	w = makeRequest(r, http.MethodGet, "/api/v1/config-files/"+itoa(fileID)+"/effective", nil, adminTok)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Equal(t, "INVALID_REQUEST", parseJSON(t, w)["error"])

	// 非法 scopeType。
	w = makeRequest(r, http.MethodGet,
		"/api/v1/config-files/"+itoa(fileID)+"/layers/network/1/history", nil, adminTok)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	// 悬空作用域引用（分组不存在）→ 404。
	w = makeRequest(r, http.MethodPost, "/api/v1/config-files/"+itoa(fileID)+"/layers", map[string]any{
		"scopeType": "group", "scopeId": 8888, "content": "a: 1\n",
	}, adminTok)
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}

// TestConfigFiles_SchemaViolationRejected 覆盖 §6：保存层时按 schema 校验该层原文。
func TestConfigFiles_SchemaViolationRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)

	node := createTestNode(t, db)
	inst := model.Instance{Name: "schema-srv", UUID: "uuid-schema-srv", NodeID: node.ID, WorkDir: t.TempDir()}
	require.NoError(t, db.Create(&inst).Error)

	w := makeRequest(r, http.MethodPost, "/api/v1/config-files", map[string]any{
		"name":       "app.json",
		"format":     "json",
		"schemaJson": `{"type":"object","required":["port"],"properties":{"port":{"type":"integer","minimum":25565,"maximum":25599}}}`,
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	fileID := uint(parseJSON(t, w)["id"].(float64))

	// 不合规：缺必填 + 类型错。
	w = makeRequest(r, http.MethodPost, "/api/v1/config-files/"+itoa(fileID)+"/layers", map[string]any{
		"scopeType": "instance", "scopeId": inst.ID, "content": `{"port":"abc"}`,
	}, adminTok)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	body := parseJSON(t, w)
	require.Equal(t, "CONFIG_INVALID", body["error"])
	require.NotEmpty(t, body["issues"])

	// 合规。
	w = makeRequest(r, http.MethodPost, "/api/v1/config-files/"+itoa(fileID)+"/layers", map[string]any{
		"scopeType": "instance", "scopeId": inst.ID, "content": `{"port":25570}`,
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

// TestConfigFiles_EffectiveScopeIsolation 验证非管理员不能预览其不可访问实例的有效值（越权隐藏为 404）。
func TestConfigFiles_EffectiveScopeIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)
	node := createTestNode(t, db)

	groupA := createGroupViaAPI(t, r, adminTok, "cfgA")
	instA := makeInstanceInGroup(t, db, node.ID, groupA, "cfg-inst-a", model.InstanceStatusRunning)

	// 管理员创建配置文件并保存实例层版本。
	w := makeRequest(r, http.MethodPost, "/api/v1/config-files", map[string]any{
		"name": "iso.properties", "format": "properties",
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	fileID := uint(parseJSON(t, w)["id"].(float64))

	w = makeRequest(r, http.MethodPost, "/api/v1/config-files/"+itoa(fileID)+"/layers", map[string]any{
		"scopeType": "instance", "scopeId": instA, "content": "a=1\n",
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// 非管理员（不属于 groupA）预览 → 404 隐藏存在性。
	memberTok := getMemberToken(t, r, "bob", "password123")
	w = makeRequest(r, http.MethodGet,
		"/api/v1/config-files/"+itoa(fileID)+"/effective?instanceId="+itoa(instA), nil, memberTok)
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())

	// 管理员放行。
	w = makeRequest(r, http.MethodGet,
		"/api/v1/config-files/"+itoa(fileID)+"/effective?instanceId="+itoa(instA), nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

// TestConfigFiles_UpdateCanClearFields 通过 HTTP 请求体显式传空串，必须真的清空字段。
//
// 回归背景（前端接入时发现）：旧实现把「空串」当作「没传」，于是用户在前端清空描述、
// 接口返回 200，但值没变，表现为「清不空」。修复后语义是
// 「字段缺席 = 不改」「显式空串 = 清空」。
func TestConfigFiles_UpdateCanClearFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)

	w := makeRequest(r, http.MethodPost, "/api/v1/config-files", map[string]any{
		"name":        "clearable.yml",
		"format":      "yaml",
		"description": "初始描述",
		"schemaJson":  `{"type":"object"}`,
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	fileID := uint(parseJSON(t, w)["id"].(float64))

	// 显式传空串 → 清空。
	w = makeRequest(r, http.MethodPut, "/api/v1/config-files/"+itoa(fileID), map[string]any{
		"description": "",
		"schemaJson":  "",
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	afterClear := parseJSON(t, w)
	require.Equal(t, "", afterClear["description"], "描述应被清空")
	// SchemaJSON 带 `json:"schemaJson,omitempty"`：清空后字段被省略（前端读到 undefined），
	// 这是既有契约，不是缺陷。断言「不存在或为空串」两种合法形态。
	if v, exists := afterClear["schemaJson"]; exists {
		require.Equal(t, "", v, "schema 若出现则必须为空串")
	}

	// 两个字段都不传（缺席）→ 保持空，不被"恢复"。
	w = makeRequest(r, http.MethodPut, "/api/v1/config-files/"+itoa(fileID), map[string]any{}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	afterNoop := parseJSON(t, w)
	require.Equal(t, "", afterNoop["description"], "字段缺席应保持原值（空）")
	if v, exists := afterNoop["schemaJson"]; exists {
		require.Equal(t, "", v, "字段缺席时应保持空")
	}

	// 再传非空 → 正常写入。
	w = makeRequest(r, http.MethodPut, "/api/v1/config-files/"+itoa(fileID), map[string]any{
		"description": "新描述",
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "新描述", parseJSON(t, w)["description"])
}
