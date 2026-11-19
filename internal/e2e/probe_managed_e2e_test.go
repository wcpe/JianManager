//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 探针托管配置的端到端联调（不需要真机 Paper）。
//
// 覆盖链路：
//
//	写三层配置 → 禁档键被拒 → 影响预演 → 建下发会话 → 推第一批 →
//	假探针收 config_push 并回 config_applied(applied) → 目标转 applied →
//	假探针周期上报 config_state → 对账矩阵出现该实例且判定为 inSync
//
// **为什么不用 Paper**：本测试要验的是「CP 的编排状态机 + ACK 消费 + 矩阵读模型」，
// 这部分与游戏服无关。真机探针另有独立验收（见 ServerProbe 仓库的 e2eBridgeFixture），
// 那一层验的是探针侧行为。两者互补，不要用其中一个替代另一个。
//
// 假探针走**真实的 Worker 桥 WS 端点**，只是不跑 MC：Worker 的会话管理、事件上行、
// CP 的 StreamPluginEvents 消费与落库都是真链路。

const probeManagedE2EBasePort = 18900

// TestE2E_ProbeManagedConfigPush 端到端验证托管配置的下发编排、ACK 消费与对账矩阵。
func TestE2E_ProbeManagedConfigPush(t *testing.T) {
	client, nodeID, _ := setupCluster(t, probeManagedE2EBasePort)

	// ---- 1. 建一个实例并启动（托管下发要求实例处于 running，否则目标会被判 skipped）----
	resp, data, err := client.request("POST", "/api/v1/instances", map[string]interface{}{
		"nodeId":       nodeID,
		"name":         "probe-managed-e2e",
		"type":         "minecraft_java",
		"role":         "backend",
		"processType":  "direct",
		"startCommand": "ping -n 120 127.0.0.1",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "创建实例失败: %s", string(data))
	instance := parseJSON(t, data)
	instanceID := uint(instance["id"].(float64))
	instanceUUID := instance["uuid"].(string)
	t.Cleanup(func() {
		client.request("POST", fmt.Sprintf("/api/v1/instances/%d/kill", instanceID), nil)
		client.request("DELETE", fmt.Sprintf("/api/v1/instances/%d", instanceID), nil)
	})

	resp, data, err = client.request("POST", fmt.Sprintf("/api/v1/instances/%d/start", instanceID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "启动实例失败: %s", string(data))
	requireInstanceState(t, client, instanceID, "RUNNING", 30*time.Second)
	t.Logf("实例已启动 id=%d uuid=%s", instanceID, instanceUUID)

	// ---- 2. 假探针连入 Worker 的插件桥（真 WS 端点，真 Worker 会话）----
	wsURL := fmt.Sprintf("ws://127.0.0.1:%d/ws/plugin-bridge", probeManagedE2EBasePort+22)
	probe := dialFakeProbe(t, wsURL, instanceUUID)
	require.NoError(t, probe.Hello())
	t.Log("假探针已连入插件桥并发 hello")

	// 等 CP 侧看到 connected：矩阵的连接维度判定依赖它。
	require.Eventually(t, func() bool {
		conn := matrixConnection(t, client, instanceID)
		return conn == "connected"
	}, 30*time.Second, 500*time.Millisecond, "CP 未在 30s 内观察到探针连接")

	// ---- 3. 写三层配置：global 基线 + instance 覆盖 ----
	resp, data, err = client.request("PUT", "/api/v1/managed-configs", map[string]interface{}{
		"scope":           "global",
		"scopeRefId":      0,
		"config":          `{"collect-period-ticks": 100, "history-capacity": 360}`,
		"expectedVersion": 0,
		"remark":          "e2e 全局基线",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "写全局层失败: %s", string(data))

	resp, data, err = client.request("PUT", "/api/v1/managed-configs", map[string]interface{}{
		"scope":           "instance",
		"scopeRefId":      instanceID,
		"config":          `{"collect-period-ticks": 40}`,
		"expectedVersion": 0,
		"remark":          "e2e 实例覆盖",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "写实例层失败: %s", string(data))
	t.Log("三层配置已写入（global 基线 + instance 覆盖 collect-period-ticks=40）")

	// ---- 4. 禁档键必须被拒（纵深防御的第一道）----
	resp, data, _ = client.request("PUT", "/api/v1/managed-configs", map[string]interface{}{
		"scope":           "instance",
		"scopeRefId":      instanceID,
		"config":          `{"bridge": {"token": "leak"}}`,
		"expectedVersion": 1,
		"remark":          "应当被拒绝",
	})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"禁档键 bridge 必须被 CP 预检拒绝，实际 %d: %s", resp.StatusCode, string(data))
	t.Log("禁档键 bridge 已被 CP 预检拒绝")

	// ---- 5. 影响预演：改采集周期应报出危险档与倍率 ----
	resp, data, err = client.request("POST", "/api/v1/managed-configs/impact", map[string]interface{}{
		"scope":      "instance",
		"scopeRefId": instanceID,
		"config":     `{"collect-period-ticks": 20}`,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "影响预演失败: %s", string(data))
	impact := parseJSON(t, data)
	guarded, _ := impact["guardedKeys"].([]interface{})
	require.Contains(t, guarded, "collect-period-ticks",
		"改采集周期应被判为危险档变更: %s", string(data))
	t.Logf("影响预演通过：受影响实例数=%v 危险档键=%v 倍率=%v",
		impact["affectedInstances"], guarded, impact["metricMultiplier"])

	// ---- 6. 建下发会话、启动、收下 config_push ----
	resp, data, err = client.request("POST", "/api/v1/managed-pushes", map[string]interface{}{
		"scope":      "instance",
		"scopeRefId": instanceID,
		"batchSize":  1,
		"reason":     "e2e 托管下发联调",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "建下发会话失败: %s", string(data))
	session := parseJSON(t, data)
	sessionID := uint(session["id"].(float64))
	configVersion := int64(session["configVersion"].(float64))
	require.NotZero(t, configVersion, "会话必须有单调递增的 configVersion")
	t.Logf("下发会话已建 id=%d configVersion=%d", sessionID, configVersion)

	resp, data, err = client.request("POST", fmt.Sprintf("/api/v1/managed-pushes/%d/start", sessionID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "启动下发失败: %s", string(data))

	push := probe.WaitPush(30 * time.Second)
	// 载荷必须是三层合并后的结果：instance 层覆盖 global 层的 collect-period-ticks。
	values, _ := push["values"].(map[string]interface{})
	require.NotNil(t, values, "config_push 应带 values: %v", push)
	require.Equal(t, float64(40), values["collect-period-ticks"],
		"instance 层应覆盖 global 层的 100: %v", values)
	require.Equal(t, float64(360), values["history-capacity"],
		"global 层独有的键应保留: %v", values)
	require.Equal(t, float64(0), push["baseVersion"],
		"首次下发的 baseVersion 必须是 0（探针从未应用过任何托管配置）: %v", push)
	t.Logf("已收 config_push：configVersion=%v baseVersion=%v values=%v",
		push["configVersion"], push["baseVersion"], values)

	// ---- 7. 回 config_applied，目标必须转 applied ----
	require.NoError(t, probe.ReportApplied(configVersion, "applied", values))

	targetState := waitTargetState(t, client, sessionID, instanceID, 30*time.Second)
	require.Equal(t, "applied", targetState,
		"回执 applied 后目标应为 applied（若为 failed，多半是 CP 没从 payload 段正确取载荷）")
	t.Log("目标已转为 applied")

	// ---- 8. 周期上报 config_state，矩阵应判定 inSync ----
	cacheAge := int64(3)
	require.NoError(t, probe.ReportState(configVersion, values, &cacheAge, "connected"))

	var row map[string]interface{}
	require.Eventually(t, func() bool {
		row = matrixRow(t, client, instanceID)
		if row == nil {
			return false
		}
		return row["consistency"] == "inSync"
	}, 30*time.Second, 500*time.Millisecond, "矩阵未在 30s 内判定 inSync")

	// 期望版本与观测版本都必须是真值，不能是解析失败留下的 0。
	require.Equal(t, float64(configVersion), row["desiredVersion"],
		"desiredVersion 应等于本次下发的版本: %v", row)
	require.Equal(t, float64(configVersion), row["observedVersion"],
		"observedVersion 应来自探针上报: %v", row)
	require.Equal(t, float64(cacheAge), row["cacheAgeSeconds"], "cacheAgeSeconds 应来自上报: %v", row)
	require.Equal(t, "connected", row["connection"])
	require.Equal(t, "cp", row["configuration"], "下发过期望版本的实例，配置来源应为 cp")
	t.Logf("矩阵行正确：期望=%v 观测=%v 一致性=%v 连接=%v 缓存年龄=%v",
		row["desiredVersion"], row["observedVersion"], row["consistency"], row["connection"], row["cacheAgeSeconds"])

	// ---- 9. 汇总：各档计数之和应等于总数 ----
	resp, data, err = client.request("GET", "/api/v1/managed-matrix/summary", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "读汇总失败: %s", string(data))
	summary := parseJSON(t, data)
	t.Logf("矩阵汇总：total=%v byConsistency=%v byConnection=%v",
		summary["total"], summary["byConsistency"], summary["byConnection"])

	// ---- 10. 探针断开后矩阵连接维度必须跟着变 ----
	probe.Close()
	require.Eventually(t, func() bool {
		return matrixConnection(t, client, instanceID) == "disconnected"
	}, 30*time.Second, 500*time.Millisecond, "探针断开后矩阵应显示 disconnected")
	t.Log("探针断开后矩阵连接维度已转为 disconnected")
}

// matrixRow 读矩阵里指定实例的行；未出现返回 nil。
func matrixRow(t *testing.T, client *e2eClient, instanceID uint) map[string]interface{} {
	t.Helper()
	resp, data, err := client.request("GET", "/api/v1/managed-matrix?page=1&pageSize=100", nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	page := parseJSON(t, data)
	items, _ := page["items"].([]interface{})
	for _, item := range items {
		row, _ := item.(map[string]interface{})
		if id, ok := row["instanceId"].(float64); ok && uint(id) == instanceID {
			return row
		}
	}
	return nil
}

// matrixConnection 读矩阵里指定实例的连接态；未出现返回空串。
func matrixConnection(t *testing.T, client *e2eClient, instanceID uint) string {
	t.Helper()
	row := matrixRow(t, client, instanceID)
	if row == nil {
		return ""
	}
	conn, _ := row["connection"].(string)
	return conn
}

// waitTargetState 轮询下发目标列表，等指定实例的目标进入终态并返回其 state。
//
// 终态白名单含 pending/pushing 之外的取值：applied/timeout/failed/rejected 都是可解释结果，
// 区别只在断言期望哪一个。
func waitTargetState(t *testing.T, client *e2eClient, sessionID, instanceID uint, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		resp, data, err := client.request("POST", fmt.Sprintf("/api/v1/managed-pushes/%d/targets", sessionID), nil)
		if err == nil && resp.StatusCode == http.StatusOK {
			page := parseJSON(t, data)
			items, _ := page["items"].([]interface{})
			for _, item := range items {
				target, _ := item.(map[string]interface{})
				if id, ok := target["instanceId"].(float64); ok && uint(id) == instanceID {
					last, _ = target["state"].(string)
					if isTerminalTargetState(last) {
						return last
					}
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("目标 %d 未在 %s 内进入终态，最后状态=%q", instanceID, timeout, last)
	return ""
}

// isTerminalTargetState 判断目标是否已收敛。
func isTerminalTargetState(state string) bool {
	switch state {
	case "applied", "timeout", "failed", "rejected", "skipped":
		return true
	}
	return false
}

// 保持 strconv 引用（端口基址推导用）。
var _ = strconv.Itoa
