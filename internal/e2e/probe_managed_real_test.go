//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 探针托管模式的真机联调：真 Paper + 真 ServerProbe jar + 真 CP/Worker。
//
// 与 probe_managed_e2e_test.go 的分工：
//   - 那个测试用假探针验 CP 的编排状态机与矩阵读模型（不跑 MC，秒级）；
//   - 本测试用**真探针 jar**验端到端契约：真探针解析 config_push、真应用到 config.yml、
//     真回 config_applied / config_state，CP 据此把矩阵判成 inSync。
//
// 为什么两者都要：假探针证明 CP 按协议说话，真探针证明协议本身被正确实现对上了。
// 只有假探针会漏掉「探针侧字段名/结构漂移」，只有真探针则跑得慢且依赖外部下载。
//
// 需要的外部条件（缺一即 skip，不伪装通过）：
//   - MCV_E2E_PROBE_JAR：已构建的 ServerProbe jar 路径
//   - 能访问 fill.papermc.io（JM 建服时下载 Paper 核心）
//
// 运行：MCV_E2E_PROBE_JAR=<path> go test -tags e2e ./internal/e2e/ -run TestE2E_ProbeManagedRealProbe -v

const probeManagedRealE2EBasePort = 18950

// TestE2E_ProbeManagedRealProbe 用真探针 jar 走完整托管链路。
func TestE2E_ProbeManagedRealProbe(t *testing.T) {
	probeJar := os.Getenv("MCV_E2E_PROBE_JAR")
	if probeJar == "" {
		t.Skip("未设置 MCV_E2E_PROBE_JAR，跳过真机探针联调")
	}
	absJar, err := filepath.Abs(probeJar)
	require.NoError(t, err)
	info, err := os.Stat(absJar)
	require.NoError(t, err, "探针 jar 不存在: %s", absJar)
	require.NotZero(t, info.Size(), "探针 jar 为空: %s", absJar)

	client, nodeID, _ := setupCluster(t, probeManagedRealE2EBasePort)

	// ---- 1. 上传探针 jar，登记为可选版本 ----
	version := "0.8.0-e2e"
	versionID := uploadLocalProbeJar(t, client, absJar, version)
	t.Logf("探针 jar 已上传：version=%s id=%d size=%d", version, versionID, info.Size())

	// 必须设为全局默认版本：建服时 ResolveInstanceProbeVersion 依次看「实例覆盖 → 节点覆盖 →
	// 全局默认」，三者皆空即 ErrArtifactVersionNotCached，**探针部署被静默跳过**（只记告警、
	// 不阻断建服）。漏了这一步，Paper 会正常起来但根本没有探针插件加载。
	setGlobalProbeDefault(t, client, versionID)

	// ---- 2. 经 JM 一键搭建 Paper 服（含探针部署与身份文件写入）----
	resp, data, err := client.request("POST", "/api/v1/instances/provision/server", map[string]interface{}{
		"nodeId":    nodeID,
		"name":      "probe-managed-real",
		"coreType":  "paper",
		"mcVersion": "1.20.1",
		"memoryMb":  1024,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "搭建 Paper 服失败: %s", string(data))
	provisioned := parseJSON(t, data)
	instance, _ := provisioned["instance"].(map[string]interface{})
	require.NotNil(t, instance, "搭建响应缺少 instance: %s", string(data))
	instanceID := uint(instance["id"].(float64))
	instanceUUID := instance["uuid"].(string)
	taskID, _ := provisioned["taskId"].(string)
	require.NotEmpty(t, taskID, "搭建响应缺少 taskId: %s", string(data))
	t.Cleanup(func() {
		client.request("POST", fmt.Sprintf("/api/v1/instances/%d/kill", instanceID), nil)
		client.request("DELETE", fmt.Sprintf("/api/v1/instances/%d", instanceID), nil)
	})
	t.Logf("Paper 实例已创建 id=%d uuid=%s taskId=%s", instanceID, instanceUUID, taskID)

	// ---- 3. 等搭建任务完成（Paper 下载 + 探针部署 + 配置写入）----
	waitProvisionTask(t, client, taskID, 10*time.Minute)

	// ---- 4. 启动实例，等真探针连上插件桥 ----
	resp, data, err = client.request("POST", fmt.Sprintf("/api/v1/instances/%d/start", instanceID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "启动实例失败: %s", string(data))

	// Paper 首启要生成世界，几十秒到几分钟；矩阵 connected 是探针真的连上来的证据。
	if !waitFor(6*time.Minute, 3*time.Second, func() bool {
		return matrixConnection(t, client, instanceID) == "connected"
	}) {
		// 失败时把实例状态与 Paper 日志尾部一起抛出来：只报「未连上」无法区分
		// 「Paper 没起来」「探针没装」「探针装了但桥连不上」三种完全不同的原因。
		t.Fatalf("真探针未在 6 分钟内连入插件桥\n实例状态: %s\nPaper 日志尾部:\n%s",
			instanceStatus(t, client, instanceID), tailInstanceLog(t, client, instanceID, "logs/latest.log", 8000))
	}
	t.Log("真探针已连入插件桥（CP 侧观察到 connected）")

	// ---- 5. 写托管配置并下发 ----
	resp, data, err = client.request("PUT", "/api/v1/managed-configs", map[string]interface{}{
		"scope":           "instance",
		"scopeRefId":      instanceID,
		"config":          `{"collect-period-ticks": 40, "history-capacity": 360}`,
		"expectedVersion": 0,
		"remark":          "真机托管联调",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "写实例层配置失败: %s", string(data))

	resp, data, err = client.request("POST", "/api/v1/managed-pushes", map[string]interface{}{
		"scope":      "instance",
		"scopeRefId": instanceID,
		"batchSize":  1,
		"reason":     "真机托管联调下发",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "建下发会话失败: %s", string(data))
	session := parseJSON(t, data)
	sessionID := uint(session["id"].(float64))
	configVersion := int64(session["configVersion"].(float64))

	resp, data, err = client.request("POST", fmt.Sprintf("/api/v1/managed-pushes/%d/start", sessionID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "启动下发失败: %s", string(data))
	t.Logf("已下发 configVersion=%d", configVersion)

	// ---- 6. 真探针回回执，目标必须 applied ----
	targetState := waitTargetState(t, client, sessionID, instanceID, 3*time.Minute)
	if targetState != "applied" {
		row := matrixRow(t, client, instanceID)
		t.Fatalf("真探针未应用成功：目标状态=%s 目标错误=%q 矩阵行=%v Paper 日志尾部:\n%s",
			targetState, targetError(t, client, sessionID, instanceID), row,
			tailInstanceLog(t, client, instanceID, "logs/latest.log", 6000))
	}
	t.Log("真探针已回 applied")

	// ---- 7. 真探针周期上报 config_state，矩阵判 inSync ----
	var row map[string]interface{}
	require.Eventually(t, func() bool {
		row = matrixRow(t, client, instanceID)
		if row == nil {
			return false
		}
		return row["consistency"] == "inSync"
	}, 3*time.Minute, 3*time.Second, "矩阵未在 3 分钟内判定 inSync")

	require.Equal(t, float64(configVersion), row["observedVersion"],
		"observedVersion 应来自真探针上报: %v", row)
	require.Equal(t, "managed", row["mode"], "真探针应自报托管模式: %v", row)
	require.NotNil(t, row["cacheAgeSeconds"], "应用成功后 cacheAgeSeconds 不应为 null: %v", row)

	// 探针自报的生效值必须是下发的那一份——这是「配置真的落到探针」的直接证据。
	// 生效值在详情接口（列表行只带版本与判定，不带整份配置）。
	resp, data, err = client.request("GET", fmt.Sprintf("/api/v1/managed-matrix/instances/%d", instanceID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "读矩阵详情失败: %s", string(data))
	detail := parseJSON(t, data)
	observed, _ := detail["observedEffective"].(map[string]interface{})
	require.NotEmpty(t, observed, "真探针应回报生效值: %s", string(data))
	require.Equal(t, float64(40), observed["collect-period-ticks"],
		"探针生效值应与下发的 40 一致: %v", observed)
	t.Logf("真机矩阵行：期望=%v 观测=%v 一致性=%v 模式=%v 缓存年龄=%v",
		row["desiredVersion"], row["observedVersion"], row["consistency"], row["mode"], row["cacheAgeSeconds"])

	// ---- 8. 零监听承诺：托管模式下探针不应再开本地 metrics 端口 ----
	// 这是托管模式的核心收益（不额外暴露面），也是与独立模式最显著的差异。
	t.Log("提示：零监听端口的最终判定见 ServerProbe 侧 e2e（探针自报未绑定监听 socket）")
}

// uploadLocalProbeJar 把本地探针 jar 上传为可选版本。
//
// 端点的字段契约很严：只认 `version` 与 `file` 两个字段，且 **version 必须在 file 之前**
// （CP 边流边判，为了不在拿到版本号前就把 jar 写进 CAS）。多发一个字段即 400。
func uploadLocalProbeJar(t *testing.T, client *e2eClient, jarPath, version string) uint {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("version", version))

	part, err := writer.CreateFormFile("file", filepath.Base(jarPath))
	require.NoError(t, err)
	f, err := os.Open(jarPath)
	require.NoError(t, err, "打开探针 jar 失败")
	_, err = io.Copy(part, f)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.NoError(t, writer.Close())

	req, err := http.NewRequest("POST", client.baseURL+"/api/v1/artifact-packages/serverprobe/versions/upload", &body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+client.token)

	resp, err := client.client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "上传探针 jar 失败: %s", string(raw))
	entry := parseJSON(t, raw)
	id, ok := entry["id"].(float64)
	require.True(t, ok, "上传响应缺少版本 id: %s", string(raw))
	return uint(id)
}

// setGlobalProbeDefault 把某探针版本设为全局默认（建服时的兜底选择）。
func setGlobalProbeDefault(t *testing.T, client *e2eClient, versionID uint) {
	t.Helper()
	resp, data, err := client.request("PUT", "/api/v1/artifact-packages/serverprobe/default-version",
		map[string]interface{}{"versionId": versionID})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "设为全局默认版本失败: %s", string(data))
}

// waitProvisionTask 等搭建任务进入终态。
//
// 响应是 `{task: {...}, logs: [...]}`，状态取值见 model.TaskState（pending/running/succeeded/failed/canceled）。
func waitProvisionTask(t *testing.T, client *e2eClient, taskID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastStatus string
	for time.Now().Before(deadline) {
		resp, data, err := client.request("GET", "/api/v1/tasks/"+taskID, nil)
		if err == nil && resp.StatusCode == http.StatusOK {
			body := parseJSON(t, data)
			task, _ := body["task"].(map[string]interface{})
			if task != nil {
				lastStatus, _ = task["state"].(string)
				switch lastStatus {
				case "succeeded":
					t.Logf("搭建任务已完成（进度 100%%）")
					return
				case "failed", "canceled":
					detail, _ := task["error"].(string)
					t.Fatalf("搭建任务终止（%s）: %s", lastStatus, detail)
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("搭建任务 %s 未在 %s 内完成，最后状态=%q", taskID, timeout, lastStatus)
}

// waitFor 轮询等待条件成立；超时返回 false（不 Fatal，让调用方附带诊断信息再失败）。
func waitFor(timeout, interval time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(interval)
	}
	return cond()
}

// instanceStatus 读实例当前状态；读失败返回 "?"。
func instanceStatus(t *testing.T, client *e2eClient, instanceID uint) string {
	t.Helper()
	resp, data, err := client.request("GET", fmt.Sprintf("/api/v1/instances/%d", instanceID), nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		return "?"
	}
	status, _ := parseJSON(t, data)["status"].(string)
	return status
}

// tailInstanceLog 读实例工作目录里某文件的尾部，供失败诊断。
//
// 走既有文件读取端点而不是去猜 Worker 的磁盘路径：测试跑在不同工作目录时后者不可靠。
//
// 这里**不能**用 parseJSON：它失败会 Fatal，而在诊断路径上 Fatal 会把真正要报的
// 失败原因盖掉（实测报成 "invalid character ':'"，看起来像日志格式问题）。
// 故先试 JSON，解不开就原样返回文本。
func tailInstanceLog(t *testing.T, client *e2eClient, instanceID uint, path string, tailBytes int) string {
	t.Helper()
	url := fmt.Sprintf("/api/v1/instances/%d/files/read?path=%s&tailBytes=%d", instanceID, path, tailBytes)
	resp, data, err := client.request("GET", url, nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("(读取 %s 失败: status=%v body=%s)", path, respStatus(resp), string(data))
	}
	var body map[string]interface{}
	if err := json.Unmarshal(data, &body); err == nil {
		if content, ok := body["content"].(string); ok {
			return content
		}
	}
	return string(data)
}

// respStatus 安全取状态码（resp 可能为 nil）。
func respStatus(resp *http.Response) interface{} {
	if resp == nil {
		return "nil"
	}
	return resp.StatusCode
}

// targetError 读下发目标上的错误文案（失败诊断用）。
//
// error 字段既可能是 CP 侧判错（未连接/超时），也可能是探针回执原样透传
// （如 rejected 的 conflict 说明），两种都落在这一处。
func targetError(t *testing.T, client *e2eClient, sessionID, instanceID uint) string {
	t.Helper()
	resp, data, err := client.request("POST", fmt.Sprintf("/api/v1/managed-pushes/%d/targets", sessionID), nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		return ""
	}
	page := parseJSON(t, data)
	items, _ := page["items"].([]interface{})
	for _, item := range items {
		target, _ := item.(map[string]interface{})
		if id, ok := target["instanceId"].(float64); ok && uint(id) == instanceID {
			msg, _ := target["error"].(string)
			kind, _ := target["errorKind"].(string)
			return kind + " | " + msg
		}
	}
	return ""
}
