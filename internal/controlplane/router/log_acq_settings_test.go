package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// FR-503 日志采集配置端点：平台默认（读/写）+ 节点级覆盖（读/写/清除）+ 权限与校验。

// newLogAcqTestRouter 装配日志采集端点与一个已登记节点，返回引擎与两个服务。
// isAdmin=false 时用于验证写门禁（无任何权限节点 ⇒ 403）。
func newLogAcqTestRouter(t *testing.T, isAdmin bool) (*gin.Engine, *service.SettingsService, uint) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	settings := service.NewSettingsService(db, &config.Config{})
	nodeSvc := service.NewNodeService(db)

	node := model.Node{
		UUID: "uuid-log-acq-node", Name: "n1", Host: "127.0.0.1", GRPCPort: 19102, WSPort: 19103, Secret: "s",
	}
	require.NoError(t, db.Create(&node).Error)

	handler := NewLogAcqSettingsHandler(settings, nodeSvc, nil)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		access := &service.UserAccess{UserID: 1}
		if isAdmin {
			access.IsPlatformAdmin = true
		} else {
			access.Nodes = map[string]struct{}{}
		}
		c.Set(middleware.CtxAccess, access)
		c.Next()
	})
	handler.RegisterPlatformRoutes(r.Group("/api/v1"))
	handler.RegisterNodeRoutes(r.Group("/api/v1"))
	return r, settings, node.ID
}

// doJSON 发一次请求并返回响应与解析后的视图。
func doJSON(t *testing.T, r *gin.Engine, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	parsed := map[string]any{}
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &parsed)
	}
	return w, parsed
}

// logAcqItem 从视图响应里取一项。
func logAcqItem(t *testing.T, parsed map[string]any, key string) map[string]any {
	t.Helper()
	items, ok := parsed["items"].([]any)
	require.True(t, ok, "响应应含 items 数组")
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if ok && item["key"] == key {
			return item
		}
	}
	t.Fatalf("视图缺少配置项 %s", key)
	return nil
}

// TestLogAcqPlatformEndpoints 验证平台默认视图与写入。
func TestLogAcqPlatformEndpoints(t *testing.T) {
	r, _, _ := newLogAcqTestRouter(t, true)

	// 读：5 项，默认值与设计一致（规则开、实时开、gz 关、扫描空、覆盖空）。
	w, parsed := doJSON(t, r, http.MethodGet, "/api/v1/settings/log-acquisition", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, parsed["items"], 5)
	require.Equal(t, "true", logAcqItem(t, parsed, "log.acq.rules_enabled")["default"])
	require.Equal(t, "true", logAcqItem(t, parsed, "log.acq.realtime_tail")["effective"])
	require.Equal(t, "true", logAcqItem(t, parsed, "log.acq.gz_import_enabled")["effective"])
	require.Equal(t, "", logAcqItem(t, parsed, "log.acq.gz_scan_interval")["effective"])

	// 写：平台默认改为关规则。
	w, parsed = doJSON(t, r, http.MethodPut, "/api/v1/settings/log-acquisition",
		`{"values":{"log.acq.rules_enabled":"false"}}`)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "false", logAcqItem(t, parsed, "log.acq.rules_enabled")["platform"])
	require.Equal(t, "false", logAcqItem(t, parsed, "log.acq.rules_enabled")["effective"])

	// 写非法值/非法键 → 422（整体拒绝）。
	w, _ = doJSON(t, r, http.MethodPut, "/api/v1/settings/log-acquisition",
		`{"values":{"log.acq.rules_enabled":"maybe"}}`)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, "非法值应回 422")

	w, _ = doJSON(t, r, http.MethodPut, "/api/v1/settings/log-acquisition",
		`{"values":{"log.level":"debug"}}`)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, "非日志采集键应被本端点拒绝")
}

// TestLogAcqNodeOverrideLifecycle 验证节点级覆盖的写 → 读 → 清除闭环。
//
// 这是本项的核心用户路径：单节点改配置、对照查看、恢复默认。
func TestLogAcqNodeOverrideLifecycle(t *testing.T) {
	r, _, nodeID := newLogAcqTestRouter(t, true)
	nodePath := "/api/v1/nodes/" + itoa(nodeID) + "/settings/log-acquisition"

	// 平台默认先关掉实时采集（节点应继承它）。
	w, _ := doJSON(t, r, http.MethodPut, "/api/v1/settings/log-acquisition",
		`{"values":{"log.acq.realtime_tail":"false"}}`)
	require.Equal(t, http.StatusOK, w.Code)

	// 初始：节点无覆盖 → 生效值 = 平台默认，覆盖列空。
	w, parsed := doJSON(t, r, http.MethodGet, nodePath, "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "uuid-log-acq-node", parsed["nodeUuid"])
	item := logAcqItem(t, parsed, "log.acq.realtime_tail")
	require.Equal(t, "false", item["effective"], "未覆盖时生效值应继承平台默认")
	require.Equal(t, false, item["overridden"])

	// 写节点覆盖：本节点打开实时采集。
	w, parsed = doJSON(t, r, http.MethodPut, nodePath, `{"values":{"log.acq.realtime_tail":"true"}}`)
	require.Equal(t, http.StatusOK, w.Code)
	item = logAcqItem(t, parsed, "log.acq.realtime_tail")
	require.Equal(t, "true", item["override"])
	require.Equal(t, true, item["overridden"])
	require.Equal(t, "true", item["effective"], "生效值应为 Worker 覆盖值")
	require.Equal(t, "false", item["platform"], "平台默认不应被节点覆盖改写")

	// 清除覆盖 → 回到继承平台默认。
	w, parsed = doJSON(t, r, http.MethodDelete, nodePath, "")
	require.Equal(t, http.StatusOK, w.Code)
	item = logAcqItem(t, parsed, "log.acq.realtime_tail")
	require.Equal(t, false, item["overridden"])
	require.Equal(t, "false", item["effective"], "清除覆盖后应恢复继承平台默认")
}

// TestLogAcqNodeDeleteSingleKey 验证按 key 清除单项覆盖（其余覆盖保留）。
func TestLogAcqNodeDeleteSingleKey(t *testing.T) {
	r, _, nodeID := newLogAcqTestRouter(t, true)
	nodePath := "/api/v1/nodes/" + itoa(nodeID) + "/settings/log-acquisition"

	w, _ := doJSON(t, r, http.MethodPut, nodePath,
		`{"values":{"log.acq.realtime_tail":"false","log.acq.gz_import_enabled":"true"}}`)
	require.Equal(t, http.StatusOK, w.Code)

	w, parsed := doJSON(t, r, http.MethodDelete, nodePath+"?key=log.acq.realtime_tail", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, false, logAcqItem(t, parsed, "log.acq.realtime_tail")["overridden"])
	require.Equal(t, true, logAcqItem(t, parsed, "log.acq.gz_import_enabled")["overridden"],
		"只清指定项时其余覆盖必须保留")

	// 非法 key → 422。
	w, _ = doJSON(t, r, http.MethodDelete, nodePath+"?key=log.level", "")
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
}

// TestLogAcqNodeEndpointsErrors 验证节点不存在与非采集键的拒绝。
func TestLogAcqNodeEndpointsErrors(t *testing.T) {
	r, _, _ := newLogAcqTestRouter(t, true)

	w, _ := doJSON(t, r, http.MethodGet, "/api/v1/nodes/99999/settings/log-acquisition", "")
	require.Equal(t, http.StatusNotFound, w.Code, "节点不存在应回 404")

	r2, _, nodeID2 := newLogAcqTestRouter(t, true)
	nodePath := "/api/v1/nodes/" + itoa(nodeID2) + "/settings/log-acquisition"
	w, _ = doJSON(t, r2, http.MethodPut, nodePath, `{"values":{"log.acq.realtime_tail":"nope"}}`)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, "非法值应回 422")

	w, _ = doJSON(t, r2, http.MethodPut, nodePath, `{"values":{"jdk.mirror.temurin":"https://x"}}`)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, "非日志采集键不得写入节点级覆盖")
}

// TestLogAcqNodeWriteRequiresPlatformAdmin 验证节点写门禁：非平台管理员一律 403。
func TestLogAcqNodeWriteRequiresPlatformAdmin(t *testing.T) {
	r, _, nodeID := newLogAcqTestRouter(t, false)
	nodePath := "/api/v1/nodes/" + itoa(nodeID) + "/settings/log-acquisition"

	w, _ := doJSON(t, r, http.MethodPut, nodePath, `{"values":{"log.acq.rules_enabled":"false"}}`)
	require.Equal(t, http.StatusForbidden, w.Code)

	w, _ = doJSON(t, r, http.MethodDelete, nodePath, "")
	require.Equal(t, http.StatusForbidden, w.Code)

	w, _ = doJSON(t, r, http.MethodGet, nodePath, "")
	require.Equal(t, http.StatusForbidden, w.Code, "无节点读权限时读也应拒绝")
}
