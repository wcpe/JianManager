package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 缺陷现场（真机）：实例处于 STOPPED 时 POST /api/v1/instances/:id/restart 返回
// 422 {"error":"INVALID_TRANSITION","message":"重启-停止: 当前状态 STOPPED 无法转换到 STOPPING: 无效的状态转换"}。
// 本用例钉死 HTTP 层语义：重启一个没有活进程的实例等价于启动，应答 200「重启中」，状态进 STARTING。
func TestInstance_Restart_StoppedInstanceSucceeds(t *testing.T) {
	for _, status := range []model.InstanceStatus{model.InstanceStatusStopped, model.InstanceStatusCrashed} {
		t.Run(string(status), func(t *testing.T) {
			db := setupTestDB(t)
			pool := cpgrpc.NewClientPool()
			r := setupTestRouterWithPool(db, pool)
			token := getAdminToken(t, r)
			node := createTestNode(t, db)
			pool.SetWorkerClientForTest(node.UUID, &fakeLifecycleWorker{})

			body := map[string]interface{}{
				"nodeId":       node.ID,
				"name":         "重启无进程实例-" + string(status),
				"type":         "minecraft_java",
				"processType":  "direct",
				"startCommand": "java -jar server.jar",
			}
			w := makeRequest(r, "POST", "/api/v1/instances", body, token)
			require.Equal(t, http.StatusCreated, w.Code)
			id := uint(parseJSON(t, w)["id"].(float64))
			// createInstanceViaAPI 只能建 STOPPED：CRASHED 需直接落库（模拟进程已崩）。
			require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", id).
				Update("status", status).Error)

			w = makeRequest(r, "POST", "/api/v1/instances/"+itoa(id)+"/restart", nil, token)
			require.Equal(t, http.StatusOK, w.Code,
				"重启已停止/已崩溃实例应 200，不得 422 INVALID_TRANSITION: %s", w.Body.String())
			require.Equal(t, "重启中", parseJSON(t, w)["message"])

			// 与启动同栈：进入 STARTING（测试环境委托为旁路，故停在此态）。
			w = makeRequest(r, "GET", "/api/v1/instances/"+itoa(id), nil, token)
			require.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, string(model.InstanceStatusStarting), parseJSON(t, w)["status"])
		})
	}
}

// 运行中实例仍按「先停后启」进 STOPPING（确认新分支未把正常重启路径也改成启动）。
func TestInstance_Restart_RunningInstanceStillStopping(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupTestRouterWithPool(db, pool)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	pool.SetWorkerClientForTest(node.UUID, &fakeLifecycleWorker{})

	body := map[string]interface{}{
		"nodeId":       node.ID,
		"name":         "重启运行中实例",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
	}
	w := makeRequest(r, "POST", "/api/v1/instances", body, token)
	require.Equal(t, http.StatusCreated, w.Code)
	id := uint(parseJSON(t, w)["id"].(float64))
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", id).
		Update("status", model.InstanceStatusRunning).Error)

	w = makeRequest(r, "POST", "/api/v1/instances/"+itoa(id)+"/restart", nil, token)
	require.Equal(t, http.StatusOK, w.Code)

	w = makeRequest(r, "GET", "/api/v1/instances/"+itoa(id), nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, string(model.InstanceStatusStopping), parseJSON(t, w)["status"],
		"运行中实例的重启仍以「重启-停止」开始")
}
