package router

import (
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestConfigSurface_ReadAndUpdate 覆盖 FR-451：受管项清单读 + 内联写入启动命令并持久化。
func TestConfigSurface_ReadAndUpdate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, adminTok, "surfg")
	id := makeInstanceInGroup(t, db, node.ID, g, "surf1", model.InstanceStatusStopped)

	w := makeRequest(r, http.MethodGet, "/api/v1/instances/"+itoa(id)+"/configs/surface", nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	items, ok := body["items"].([]interface{})
	require.True(t, ok)
	require.NotEmpty(t, items)

	v := "java -Xmx1G -jar server.jar nogui"
	w = makeRequest(r, http.MethodPut, "/api/v1/instances/"+itoa(id)+"/configs/surface", map[string]any{
		"items": []map[string]any{
			{"itemKey": "startup.command", "source": "inline", "inlineValue": v},
		},
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var got model.Instance
	require.NoError(t, db.First(&got, id).Error)
	require.Equal(t, v, got.StartCommand, "内联写入应持久化到实例启动命令")
}

// TestInstanceRolling_CreateAndGet 覆盖 FR-457：滚动编排端点可达 + 会话可查。
func TestInstanceRolling_CreateAndGet(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)
	node := createTestNode(t, db)
	g := createGroupViaAPI(t, r, adminTok, "rollg")
	id := makeInstanceInGroup(t, db, node.ID, g, "roll1", model.InstanceStatusRunning)

	// 用 command 动作：无 Worker 时委托失败也不回写实例状态，避免测试收尾的异步写入。
	w := makeRequest(r, http.MethodPost, "/api/v1/instances/rolling", map[string]any{
		"action": "command", "ids": []uint{id}, "command": "say hi",
		"batchSize": 1, "batchIntervalSec": 0,
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	require.NotZero(t, body["id"])
	opID := uint(body["id"].(float64))

	w = makeRequest(r, http.MethodGet, "/api/v1/instances/rolling/"+itoa(opID), nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// 等编排到达终态，避免后台 goroutine 越过测试 DB 关闭时刻。
	require.Eventually(t, func() bool {
		gw := makeRequest(r, http.MethodGet, "/api/v1/instances/rolling/"+itoa(opID), nil, adminTok)
		if gw.Code != http.StatusOK {
			return false
		}
		state, _ := parseJSON(t, gw)["state"].(string)
		return state == "done" || state == "canceled"
	}, 2*time.Second, 20*time.Millisecond)
}

// TestConfigBaseline_Endpoints 覆盖 FR-458：基线创建 / 列出 / 漂移检测端点可达。
func TestConfigBaseline_Endpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)

	w := makeRequest(r, http.MethodPost, "/api/v1/config-baselines", map[string]any{
		"scopeKey": "all", "filePath": "server.properties", "content": "server-port=25565\n",
	}, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	require.NotZero(t, body["id"])
	blID := uint(body["id"].(float64))

	w = makeRequest(r, http.MethodGet, "/api/v1/config-baselines", nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	list := parseJSON(t, w)
	baselines, ok := list["baselines"].([]interface{})
	require.True(t, ok)
	require.Len(t, baselines, 1)

	w = makeRequest(r, http.MethodGet, "/api/v1/config-baselines/"+itoa(blID)+"/drift", nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}
