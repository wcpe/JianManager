//go:build botloadacceptance

// FR-370 Bot Load acceptance harness — 固定真机验收测试。
//
// 运行：
//
//	JM_BOT_LOAD_ACCEPTANCE=1 \
//	JM_BOT_LOAD_ENV=.tmp/bot-load-acceptance/environment.json \
//	go test -tags=botloadacceptance ./internal/e2e -run '^TestBotLoadAcceptance$' -count=1 -timeout=4h
//
// 环境配置 JSON 示例见 docs/specs/bot-load-runner/spec.md §4 第 201 行。
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// acceptanceEnv 是 JM_BOT_LOAD_ENV 指向的 JSON 配置文件结构。
type acceptanceEnv struct {
	CpURL           string `json:"cpUrl"`
	Username        string `json:"username"`
	Password        string `json:"password"`
	NodeID          uint   `json:"nodeId"`
	InstancePath    string `json:"instancePath"`
	JarPath         string `json:"jarPath"`
	JDKID           uint   `json:"jdkId"`
	BotCount        int    `json:"botCount"`
	DurationMinutes int    `json:"durationMinutes"`
}

// acceptanceClient 封装验收测试的 HTTP 客户端，不依赖项目内部包。
type acceptanceClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func (c *acceptanceClient) do(method, path string, body interface{}) (*http.Response, []byte, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, fmt.Errorf("序列化请求体失败: %w", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		return nil, nil, fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("发送请求失败: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, nil, fmt.Errorf("读取响应失败: %w", err)
	}
	return resp, data, nil
}

// acceptanceEvidence 是最终输出到 stdout 的机器可读证据。
type acceptanceEvidence struct {
	Verdict            string                   `json:"verdict"` // passed | failed | blocked
	RunID              uint                     `json:"runId"`
	RunUUID            string                   `json:"runUuid"`
	TemplateID         uint                     `json:"templateId"`
	BotCount           int                      `json:"botCount"`
	DurationMinutes    int                      `json:"durationMinutes"`
	Checks             []acceptanceCheckResult  `json:"checks"`
	MetricsSnapshots   []map[string]interface{} `json:"metricsSnapshots,omitempty"`
	Report             map[string]interface{}   `json:"report,omitempty"`
	StartedAt          string                   `json:"startedAt"`
	FinishedAt         string                   `json:"finishedAt"`
	FailureReason      string                   `json:"failureReason,omitempty"`
}

// acceptanceCheckResult 单项验收检查结果。
type acceptanceCheckResult struct {
	Name    string  `json:"name"`
	Passed  bool    `json:"passed"`
	Value   float64 `json:"value"`
	Target  float64 `json:"target"`
	Detail  string  `json:"detail,omitempty"`
}

// 验收阈值常量（spec §38 默认严格验收）。
const (
	thresholdOnlineRate       = 0.99
	thresholdCommandSentRate  = 0.99
	thresholdScheduleComplete = 0.99
	thresholdWorkerHealth     = 0.99
	thresholdScheduleLagP95MS = 1000.0
	thresholdMaxCrashes       = 0.0
)

// TestBotLoadAcceptance 执行 FR-370 真机固定验收测试。
//
// 流程：登录 → 创建模板 → 从模板创建 run → preflight → start →
// 轮询连接 → 持续监控指标 → stop → 验证报告 → 输出 JSON 证据。
func TestBotLoadAcceptance(t *testing.T) {
	// 1. 读环境变量，未设置则跳过。
	if os.Getenv("JM_BOT_LOAD_ACCEPTANCE") != "1" {
		t.Skip("需要 JM_BOT_LOAD_ACCEPTANCE=1")
	}
	envPath := os.Getenv("JM_BOT_LOAD_ENV")
	if envPath == "" {
		t.Skip("需要 JM_BOT_LOAD_ENV 指向环境配置 JSON")
	}

	// 2. 从 JSON 读配置。
	raw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("读取环境配置失败 (%s): %v", envPath, err)
	}
	var env acceptanceEnv
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("解析环境配置 JSON 失败: %v", err)
	}
	if env.BotCount == 0 {
		env.BotCount = 500
	}
	if env.DurationMinutes == 0 {
		env.DurationMinutes = 60
	}
	t.Logf("验收配置: cpUrl=%s botCount=%d durationMinutes=%d", env.CpURL, env.BotCount, env.DurationMinutes)

	client := &acceptanceClient{
		baseURL: env.CpURL,
		http:    &http.Client{Timeout: 30 * time.Second},
	}

	ev := &acceptanceEvidence{
		Verdict:         "blocked",
		BotCount:        env.BotCount,
		DurationMinutes: env.DurationMinutes,
		StartedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	defer func() {
		ev.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		out, _ := json.MarshalIndent(ev, "", "  ")
		fmt.Printf("\n=== FR-370 Bot Load Acceptance Evidence ===\n%s\n", string(out))
	}()

	// 3. 登录获取 token。
	if err := acceptanceLogin(t, client, env.Username, env.Password); err != nil {
		ev.FailureReason = fmt.Sprintf("登录失败: %v", err)
		t.Fatalf("登录失败: %v", err)
	}
	t.Log("登录成功")

	// 4. 创建模板（commandSchedule + stable loadProfile）。
	tplID, err := acceptanceCreateTemplate(t, client, env)
	if err != nil {
		ev.FailureReason = fmt.Sprintf("创建模板失败: %v", err)
		t.Fatalf("创建模板失败: %v", err)
	}
	ev.TemplateID = tplID
	t.Logf("模板已创建 id=%d", tplID)

	// 5. 从模板创建 run。
	runID, runUUID, err := acceptanceCreateRun(t, client, tplID, env)
	if err != nil {
		ev.FailureReason = fmt.Sprintf("创建运行失败: %v", err)
		t.Fatalf("创建运行失败: %v", err)
	}
	ev.RunID = runID
	ev.RunUUID = runUUID
	t.Logf("运行已创建 id=%d uuid=%s", runID, runUUID)

	// 6. preflight。
	planToken, err := acceptancePreflight(t, client, runID)
	if err != nil {
		ev.FailureReason = fmt.Sprintf("预检失败: %v", err)
		// 即使预检失败也尝试停止并收集证据。
		acceptanceTryStop(t, client, runID)
		t.Fatalf("预检失败: %v", err)
	}
	t.Logf("预检通过 planToken=%s...", truncateToken(planToken))

	// 7. start。
	if err := acceptanceStart(t, client, runID, planToken); err != nil {
		ev.FailureReason = fmt.Sprintf("启动失败: %v", err)
		acceptanceTryStop(t, client, runID)
		t.Fatalf("启动失败: %v", err)
	}
	t.Log("运行已启动")

	// 确保测试结束或失败时停止运行。
	t.Cleanup(func() {
		acceptanceTryStop(t, client, runID)
	})

	// 8. 轮询直到 connected >= target 或超时。
	connectTimeout := time.Duration(env.BotCount/5+120) * time.Second // 估算：5/s + 120s 余量
	if connectTimeout < 5*time.Minute {
		connectTimeout = 5 * time.Minute
	}
	if err := acceptanceWaitForConnections(t, client, int(runID), env.BotCount, connectTimeout); err != nil {
		ev.FailureReason = fmt.Sprintf("等待连接超时: %v", err)
		ev.Verdict = "failed"
		acceptanceTryStop(t, client, runID)
		t.Fatalf("等待 Bot 连接失败: %v", err)
	}
	t.Logf("全部 %d Bot 已连接", env.BotCount)

	// 9. 持续监控指标。
	duration := time.Duration(env.DurationMinutes) * time.Minute
	ev.MetricsSnapshots = acceptanceMonitorMetrics(t, client, int(runID), env.BotCount, duration)

	// 10. stop。
	if err := acceptanceStop(t, client, runID); err != nil {
		t.Logf("停止运行失败（继续验证报告）: %v", err)
	}

	// 等待运行进入终态。
	if err := acceptanceWaitTerminal(t, client, runID, 5*time.Minute); err != nil {
		t.Logf("等待终态超时: %v", err)
	}

	// 11. 验证报告生成。
	report, err := acceptanceFetchReport(t, client, runID)
	if err != nil {
		ev.FailureReason = fmt.Sprintf("获取报告失败: %v", err)
		ev.Verdict = "failed"
		t.Errorf("获取报告失败: %v", err)
		return
	}
	ev.Report = report
	t.Logf("报告已生成 verdict=%v", report["verdict"])

	// 12. 执行阈值检查。
	ev.Checks = acceptanceEvaluateChecks(t, client, int(runID), env.BotCount, ev.MetricsSnapshots)

	allPassed := true
	for _, c := range ev.Checks {
		if !c.Passed {
			allPassed = false
			t.Errorf("检查未通过: %s (值=%.4f 目标=%.4f) %s", c.Name, c.Value, c.Target, c.Detail)
		}
	}
	if allPassed {
		ev.Verdict = "passed"
		t.Log("所有验收检查通过")
	} else {
		ev.Verdict = "failed"
	}
}

// acceptanceLogin 使用用户名密码登录 CP，获取 accessToken。
func acceptanceLogin(t *testing.T, c *acceptanceClient, username, password string) error {
	t.Helper()
	resp, data, err := c.do("POST", "/api/v1/auth/login", map[string]string{
		"username": username,
		"password": password,
	})
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("登录返回 %d: %s", resp.StatusCode, string(data))
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("解析登录响应失败: %w", err)
	}
	token, ok := m["accessToken"].(string)
	if !ok || token == "" {
		return fmt.Errorf("登录响应缺少 accessToken")
	}
	c.token = token
	return nil
}

// acceptanceCreateTemplate 创建 command_schedule + stable loadProfile 模板。
func acceptanceCreateTemplate(t *testing.T, c *acceptanceClient, env acceptanceEnv) (uint, error) {
	t.Helper()
	// 命令计划：一条每 30s 重复的 say 命令，持续整个运行周期。
	commandSchedule := map[string]interface{}{
		"commands": []map[string]interface{}{
			{
				"id":      "cmd-say-hi",
				"atMs":    0,
				"command": "/say {{botName}} acceptance-probe",
				"repeat": map[string]interface{}{
					"intervalMs": 30000,
					"count":      env.DurationMinutes * 2, // 每 30s 一次
				},
			},
		},
		"durationMs": env.DurationMinutes * 60 * 1000,
		"jitterMs":   0,
	}
	// 稳定负载：固定 botCount，短爬升，指定时长。
	loadProfile := map[string]interface{}{
		"type":            "stable",
		"targetBots":      env.BotCount,
		"rampUpSeconds":   60,
		"durationSeconds": env.DurationMinutes * 60,
	}
	// 默认严格阈值。
	thresholds := map[string]interface{}{
		"minOnlineRate":             thresholdOnlineRate,
		"minCommandSentRate":        thresholdCommandSentRate,
		"minScheduleCompletionRate": thresholdScheduleComplete,
		"minWorkerHealthRate":       thresholdWorkerHealth,
		"minBarrierArrivalRate":     thresholdOnlineRate,
		"maxScheduleLagP95Ms":       int(thresholdScheduleLagP95MS),
		"maxProcessCrashes":         int(thresholdMaxCrashes),
	}
	body := map[string]interface{}{
		"name":            fmt.Sprintf("acceptance-%d", time.Now().Unix()),
		"description":     "FR-370 自动验收模板",
		"commandSchedule": commandSchedule,
		"loadProfile":     loadProfile,
		"thresholds":      thresholds,
		"tags":            []string{"acceptance", "fr-370"},
	}
	resp, data, err := c.do("POST", "/api/v1/bots/load-templates", body)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusCreated {
		return 0, fmt.Errorf("创建模板返回 %d: %s", resp.StatusCode, string(data))
	}
	m := parseJSONAcceptance(data)
	id, ok := m["id"].(float64)
	if !ok {
		return 0, fmt.Errorf("模板响应缺少 id: %s", string(data))
	}
	return uint(id), nil
}

// acceptanceCreateRun 从模板创建 schemaVersion=2 压测运行。
func acceptanceCreateRun(t *testing.T, c *acceptanceClient, tplID uint, env acceptanceEnv) (uint, string, error) {
	t.Helper()
	// 离线 Bot 配置：指向目标实例。
	config := map[string]interface{}{
		"server": extractHostFromURL(env.CpURL),
		"port":   25565,
		"auth":   "offline",
	}
	body := map[string]interface{}{
		"instanceId": env.NodeID, // 目标实例所在节点
		"name":       fmt.Sprintf("acceptance-run-%d", time.Now().Unix()),
		"namePrefix": "acpt",
		"config":     config,
	}
	resp, data, err := c.do("POST", fmt.Sprintf("/api/v1/bots/load-templates/%d/runs", tplID), body)
	if err != nil {
		return 0, "", err
	}
	if resp.StatusCode != http.StatusCreated {
		return 0, "", fmt.Errorf("创建运行返回 %d: %s", resp.StatusCode, string(data))
	}
	m := parseJSONAcceptance(data)
	id, _ := m["id"].(float64)
	uuid, _ := m["uuid"].(string)
	if id == 0 {
		return 0, "", fmt.Errorf("运行响应缺少 id: %s", string(data))
	}
	return uint(id), uuid, nil
}

// acceptancePreflight 执行预检，返回 planToken。
func acceptancePreflight(t *testing.T, c *acceptanceClient, runID uint) (string, error) {
	t.Helper()
	resp, data, err := c.do("POST", fmt.Sprintf("/api/v1/bots/stress-sessions/%d/preflight", runID), map[string]interface{}{})
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("预检返回 %d: %s", resp.StatusCode, string(data))
	}
	m := parseJSONAcceptance(data)
	ready, _ := m["ready"].(bool)
	if !ready {
		blockers, _ := json.Marshal(m["blockers"])
		return "", fmt.Errorf("预检未就绪 blockers=%s", string(blockers))
	}
	token, _ := m["planToken"].(string)
	if token == "" {
		return "", fmt.Errorf("预检响应缺少 planToken: %s", string(data))
	}
	return token, nil
}

// acceptanceStart 使用 planToken 启动运行。
func acceptanceStart(t *testing.T, c *acceptanceClient, runID uint, planToken string) error {
	t.Helper()
	resp, data, err := c.do("POST", fmt.Sprintf("/api/v1/bots/stress-sessions/%d/start", runID), map[string]interface{}{
		"planToken": planToken,
	})
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("启动返回 %d: %s", resp.StatusCode, string(data))
	}
	return nil
}

// acceptanceWaitForConnections 轮询直到 connected >= target 或超时。
func acceptanceWaitForConnections(t *testing.T, c *acceptanceClient, runID, target int, timeout time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastConnected int
	for time.Now().Before(deadline) {
		_, data, err := c.do("GET", fmt.Sprintf("/api/v1/bots/stress-sessions/%d", runID), nil)
		if err == nil {
			m := parseJSONAcceptance(data)
			counts, _ := m["counts"].(map[string]interface{})
			byStatus, _ := counts["byStatus"].(map[string]interface{})
			connected, _ := byStatus["connected"].(float64)
			lastConnected = int(connected)
			if lastConnected >= target {
				return nil
			}
			// 检查是否已进入终态但未达目标。
			if status, _ := m["status"].(string); status == "stopped" || status == "error" {
				return fmt.Errorf("运行已进入终态 %s，连接数 %d/%d", status, lastConnected, target)
			}
		}
		time.Sleep(5 * time.Second)
		t.Logf("等待连接… connected=%d/%d", lastConnected, target)
	}
	return fmt.Errorf("连接超时：最后 connected=%d/%d", lastConnected, target)
}

// acceptanceMonitorMetrics 持续监控指标，每 5s 采样一次。
func acceptanceMonitorMetrics(t *testing.T, c *acceptanceClient, runID, targetBots int, duration time.Duration) []map[string]interface{} {
	t.Helper()
	var snapshots []map[string]interface{}
	deadline := time.Now().Add(duration)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-tick.C:
			_, data, err := c.do("GET", fmt.Sprintf("/api/v1/bots/stress-sessions/%d/metrics?resolution=raw", runID), nil)
			if err != nil {
				t.Logf("获取指标失败: %v", err)
				continue
			}
			m := parseJSONAcceptance(data)
			items, _ := m["items"].([]interface{})
			if len(items) > 0 {
				last, _ := items[len(items)-1].(map[string]interface{})
				snap := acceptanceExtractMetricSummary(last, targetBots)
				snap["timestamp"] = last["timestamp"]
				snap["stageIndex"] = last["stageIndex"]
				snapshots = append(snapshots, snap)

				// 检查 crash。
				if crashes, _ := snap["crashes"].(float64); crashes > 0 {
					t.Errorf("检测到非预期 crash=%v，立即停止", crashes)
					return snapshots
				}
				// 打印简要摘要。
				t.Logf("指标: connected=%.0f commandSent=%.4f scheduleComplete=%.4f lagP95Ms=%.0f",
					snap["connected"], snap["commandSentRate"], snap["scheduleCompletionRate"], snap["scheduleLagP95Ms"])
			}

			if time.Now().After(deadline) {
				t.Logf("监控时长 %v 已到，停止采样", duration)
				return snapshots
			}
		case <-time.After(time.Until(deadline)):
			return snapshots
		}
	}
}

// acceptanceExtractMetricSummary 从原始 metric sample 提取关键指标。
func acceptanceExtractMetricSummary(sample map[string]interface{}, targetBots int) map[string]interface{} {
	snap := make(map[string]interface{})

	// counts
	if counts, ok := sample["counts"].(map[string]interface{}); ok {
		connected, _ := counts["connected"].(float64)
		snap["connected"] = connected
		snap["onlineRate"] = connected / float64(targetBots)
	}

	// command
	if cmd, ok := sample["command"].(map[string]interface{}); ok {
		sent, _ := cmd["sent"].(float64)
		total, _ := cmd["total"].(float64)
		if total > 0 {
			snap["commandSentRate"] = sent / total
		} else {
			snap["commandSentRate"] = 1.0
		}
		completed, _ := cmd["completed"].(float64)
		if total > 0 {
			snap["scheduleCompletionRate"] = completed / total
		} else {
			snap["scheduleCompletionRate"] = 1.0
		}
	}

	// latency
	if latency, ok := sample["latency"].(map[string]interface{}); ok {
		if cmdLag, ok := latency["commandScheduleLag"].(map[string]interface{}); ok {
			p95, _ := cmdLag["p95Ms"].(float64)
			snap["scheduleLagP95Ms"] = p95
		}
	}

	// executor health
	if exec, ok := sample["executor"].(map[string]interface{}); ok {
		healthy, _ := exec["healthy"].(float64)
		total, _ := exec["total"].(float64)
		if total > 0 {
			snap["workerHealthRate"] = healthy / total
		} else {
			snap["workerHealthRate"] = 1.0
		}
	}

	// errors
	if errs, ok := sample["errors"].(map[string]interface{}); ok {
		crashes, _ := errs["crashes"].(float64)
		snap["crashes"] = crashes
	}

	return snap
}

// acceptanceStop 停止运行。
func acceptanceStop(t *testing.T, c *acceptanceClient, runID uint) error {
	t.Helper()
	resp, data, err := c.do("POST", fmt.Sprintf("/api/v1/bots/stress-sessions/%d/stop", runID), map[string]interface{}{
		"reason": "FR-370 验收测试结束",
	})
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("停止返回 %d: %s", resp.StatusCode, string(data))
	}
	return nil
}

// acceptanceTryStop 尽力停止运行，忽略错误。
func acceptanceTryStop(t *testing.T, c *acceptanceClient, runID uint) {
	t.Helper()
	resp, _, err := c.do("POST", fmt.Sprintf("/api/v1/bots/stress-sessions/%d/stop", runID), map[string]interface{}{
		"reason": "FR-370 验收测试异常清理",
	})
	if err != nil {
		t.Logf("清理停止失败: %v", err)
		return
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Logf("清理停止返回 %d（可能已终态）", resp.StatusCode)
	}
}

// acceptanceWaitTerminal 等待运行进入终态。
func acceptanceWaitTerminal(t *testing.T, c *acceptanceClient, runID uint, timeout time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(timeout)
	terminalStates := map[string]bool{
		"completed": true, "failed": true, "cancelled": true,
		"stopped": true, "error": true,
	}
	for time.Now().Before(deadline) {
		_, data, err := c.do("GET", fmt.Sprintf("/api/v1/bots/stress-sessions/%d", runID), nil)
		if err == nil {
			m := parseJSONAcceptance(data)
			// 优先检查 runState（V2），回退 status（V1 兼容）。
			if rs, ok := m["runState"].(string); ok && terminalStates[rs] {
				return nil
			}
			if status, ok := m["status"].(string); ok && terminalStates[status] {
				return nil
			}
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("等待终态超时")
}

// acceptanceFetchReport 获取终态报告。
func acceptanceFetchReport(t *testing.T, c *acceptanceClient, runID uint) (map[string]interface{}, error) {
	t.Helper()
	resp, data, err := c.do("GET", fmt.Sprintf("/api/v1/bots/stress-sessions/%d/report?format=json", runID), nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("报告返回 %d: %s", resp.StatusCode, string(data))
	}
	return parseJSONAcceptance(data), nil
}

// acceptanceEvaluateChecks 对采集的指标执行阈值检查。
func acceptanceEvaluateChecks(t *testing.T, c *acceptanceClient, runID, targetBots int, snapshots []map[string]interface{}) []acceptanceCheckResult {
	t.Helper()
	var checks []acceptanceCheckResult

	if len(snapshots) == 0 {
		checks = append(checks, acceptanceCheckResult{
			Name: "sample_coverage", Passed: false, Value: 0, Target: 1,
			Detail: "无指标样本",
		})
		return checks
	}

	// 计算各指标在所有样本中的最小值/最大值。
	var minOnlineRate, minCmdSentRate, minSchedComplete, minWorkerHealth = 1.0, 1.0, 1.0, 1.0
	var maxLagP95, maxCrashes = 0.0, 0.0

	for _, snap := range snapshots {
		if v, ok := snap["onlineRate"].(float64); ok && v < minOnlineRate {
			minOnlineRate = v
		}
		if v, ok := snap["commandSentRate"].(float64); ok && v < minCmdSentRate {
			minCmdSentRate = v
		}
		if v, ok := snap["scheduleCompletionRate"].(float64); ok && v < minSchedComplete {
			minSchedComplete = v
		}
		if v, ok := snap["workerHealthRate"].(float64); ok && v < minWorkerHealth {
			minWorkerHealth = v
		}
		if v, ok := snap["scheduleLagP95Ms"].(float64); ok && v > maxLagP95 {
			maxLagP95 = v
		}
		if v, ok := snap["crashes"].(float64); ok && v > maxCrashes {
			maxCrashes = v
		}
	}

	checks = append(checks,
		acceptanceCheckResult{
			Name: "online_rate", Passed: minOnlineRate >= thresholdOnlineRate,
			Value: minOnlineRate, Target: thresholdOnlineRate,
			Detail: "连接率（最小值）",
		},
		acceptanceCheckResult{
			Name: "command_sent_rate", Passed: minCmdSentRate >= thresholdCommandSentRate,
			Value: minCmdSentRate, Target: thresholdCommandSentRate,
			Detail: "命令发送率（最小值）",
		},
		acceptanceCheckResult{
			Name: "schedule_completion_rate", Passed: minSchedComplete >= thresholdScheduleComplete,
			Value: minSchedComplete, Target: thresholdScheduleComplete,
			Detail: "调度完成率（最小值）",
		},
		acceptanceCheckResult{
			Name: "worker_health_rate", Passed: minWorkerHealth >= thresholdWorkerHealth,
			Value: minWorkerHealth, Target: thresholdWorkerHealth,
			Detail: "Worker 健康率（最小值）",
		},
		acceptanceCheckResult{
			Name: "schedule_lag_p95_ms", Passed: maxLagP95 <= thresholdScheduleLagP95MS,
			Value: maxLagP95, Target: thresholdScheduleLagP95MS,
			Detail: "调度延迟 p95（最大值）",
		},
		acceptanceCheckResult{
			Name: "process_crashes", Passed: maxCrashes <= thresholdMaxCrashes,
			Value: maxCrashes, Target: thresholdMaxCrashes,
			Detail: "非预期 crash（最大值）",
		},
	)

	return checks
}

// parseJSONAcceptance 解析 JSON 到 map（不依赖 testify）。
func parseJSONAcceptance(data []byte) map[string]interface{} {
	var m map[string]interface{}
	_ = json.Unmarshal(data, &m)
	return m
}

// truncateToken 截断 token 用于日志显示。
func truncateToken(token string) string {
	if len(token) > 12 {
		return token[:12]
	}
	return token
}

// extractHostFromURL 从完整 URL 提取主机部分（去掉协议和端口）。
func extractHostFromURL(rawURL string) string {
	s := rawURL
	// 去掉协议前缀。
	for _, prefix := range []string{"https://", "http://"} {
		if len(s) > len(prefix) && s[:len(prefix)] == prefix {
			s = s[len(prefix):]
			break
		}
	}
	// 去掉端口和路径。
	for i := 0; i < len(s); i++ {
		if s[i] == ':' || s[i] == '/' {
			return s[:i]
		}
	}
	return s
}
