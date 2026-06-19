//go:build e2e

// 全链路 e2e（FR-043）：节点在线 → 创建并启动 MC 实例 → 终端交互 → Bot 真正进服 → 运维闭环。
//
// 受限于 e2e 主机仅有 Java 8，真实 Paper 1.21 无法启动，故实例进程使用确定性的
// 假 MC 服务器（bot-worker/test/fake-mc-server.mjs，基于 mineflayer 自带的
// minecraft-protocol，离线 1.8.9）。它对真实 bot-worker spawn 的真实 mineflayer Bot
// 完成登录握手直至 spawn —— 平台侧每个环节（CP/Worker/gRPC/进程管理/终端代理/Bot
// spawn/IPC/状态回传）都是真链路，唯一被替身的是「Paper 实现」本身。
//
// 运行：go test -tags=e2e -run TestE2E ./internal/e2e/ -v   （或 make e2e）
package e2e

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// setupCluster 在给定端口基址上启动 CP+Worker，建管理员、等节点注册，返回已认证 client 与节点 ID。
// 端口分配：HTTP=base，CP gRPC=base+20，Worker gRPC=base+21，Worker WS=base+22。
func setupCluster(t *testing.T, base int) (*e2eClient, uint, string) {
	t.Helper()
	projectRoot := findProjectRoot(t)

	httpPort := strconv.Itoa(base)
	cpGRPCPort := strconv.Itoa(base + 20)
	workerGRPCPort := strconv.Itoa(base + 21)
	workerWSPort := strconv.Itoa(base + 22)
	addr := "http://127.0.0.1:" + httpPort

	dbDSN := "file:e2e-" + strconv.FormatInt(time.Now().UnixNano(), 10) + "?mode=memory&cache=shared"

	cpCmd := exec.Command("go", "run", "./cmd/control-plane")
	cpCmd.Dir = projectRoot
	cpCmd.Env = append(os.Environ(),
		"JIANMANAGER_SERVER_PORT="+httpPort,
		"JIANMANAGER_DATABASE_DRIVER=sqlite",
		"JIANMANAGER_DATABASE_DSN="+dbDSN,
		"JIANMANAGER_GRPC_PORT="+cpGRPCPort,
		"JIANMANAGER_LOG_LEVEL=info",
		"JIANMANAGER_LOG_FORMAT=text",
		// 终端 token 由 CP 签发、Worker WS 校验，二者 JWT secret 必须一致。
		"JIANMANAGER_JWT_SECRET=e2e-test-secret",
	)
	cpCmd.Stdout = os.Stdout
	cpCmd.Stderr = os.Stderr
	cpCmd.WaitDelay = 10 * time.Second
	require.NoError(t, cpCmd.Start(), "启动 Control Plane 失败")
	t.Cleanup(func() { stopProcessTree(cpCmd) })
	require.NoError(t, waitForReady(addr, 40*time.Second), "Control Plane 未就绪")

	workerCmd := exec.Command("go", "run", "./cmd/worker")
	workerCmd.Dir = projectRoot
	workerCmd.Env = append(os.Environ(),
		"JIANMANAGER_CONTROL_PLANE_GRPC=127.0.0.1:"+cpGRPCPort,
		"JIANMANAGER_GRPC_PORT="+workerGRPCPort,
		"JIANMANAGER_WS_PORT="+workerWSPort,
		"JIANMANAGER_HOST=127.0.0.1", // 注册为回环，使 CP 终端代理稳定回拨 Worker WS
		"JIANMANAGER_NODE_NAME=e2e-worker",
		"JIANMANAGER_WORK_DIR="+filepath.Join(projectRoot, "e2e-data", "servers"),
		"JIANMANAGER_JWT_SECRET=e2e-test-secret",
		// 替身实例进程不响应优雅 stop，缩短超时使停止快速回退强杀（验收 5 需实例尽快停、Bot 随之断开）。
		"JIANMANAGER_GRACEFUL_STOP_TIMEOUT=2s",
		// Bot 能力：指向已构建的 bot-worker 入口，使 Worker 能 spawn Node 子进程。
		"JIANMANAGER_BOT_WORKER_PATH="+filepath.Join(projectRoot, "bot-worker", "dist", "index.js"),
	)
	workerCmd.Stdout = os.Stdout
	workerCmd.Stderr = os.Stderr
	workerCmd.WaitDelay = 10 * time.Second
	require.NoError(t, workerCmd.Start(), "启动 Worker 失败")
	t.Cleanup(func() { stopProcessTree(workerCmd) })

	client := &e2eClient{baseURL: addr, client: &http.Client{Timeout: 10 * time.Second}}
	resp, data, err := client.request("POST", "/api/v1/setup", map[string]string{
		"username": "admin",
		"password": "e2e-password-123",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "setup 失败: %s", string(data))
	client.token = parseJSON(t, data)["accessToken"].(string)
	require.NotEmpty(t, client.token, "accessToken 为空")

	var nodeID uint
	for i := 0; i < 30; i++ {
		_, data, err := client.request("GET", "/api/v1/nodes", nil)
		if err == nil {
			nodes := parseJSONArray(t, data)
			if len(nodes) > 0 {
				nodeID = uint(nodes[0].(map[string]interface{})["id"].(float64))
				break
			}
		}
		time.Sleep(1 * time.Second)
	}
	require.NotZero(t, nodeID, "Worker 未在 30s 内注册到 Control Plane")
	t.Logf("集群就绪：节点 ID=%d，HTTP=%s", nodeID, addr)
	return client, nodeID, projectRoot
}

// TestE2E_FullChainTerminalBot 逐条验证 FR-043 的 1~5 项验收标准。
func TestE2E_FullChainTerminalBot(t *testing.T) {
	client, nodeID, projectRoot := setupCluster(t, 18200)

	// 每次取一个空闲端口，避免上一轮失败遗留的实例进程占用导致 Bot 连到错误的服务器。
	mcPort := freePort(t)
	fakeServer := filepath.Join(projectRoot, "bot-worker", "test", "fake-mc-server.mjs")
	startCmd := fmt.Sprintf("node %s --port=%d --version=1.8.9", fakeServer, mcPort)
	t.Logf("假 MC 服务器端口=%d", mcPort)

	// 验收 2(上)：经平台创建一个 MC 实例（进程=假 MC 服务器）。
	resp, data, err := client.request("POST", "/api/v1/instances", map[string]interface{}{
		"nodeId":       nodeID,
		"name":         "e2e-fullchain-mc",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": startCmd,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "创建实例失败: %s", string(data))
	instance := parseJSON(t, data)
	instanceID := uint(instance["id"].(float64))
	t.Logf("实例已创建 id=%d", instanceID)
	// 优雅清理：由 Worker 自身 kill 实例进程，避免遗留占端口的 node 子进程。
	t.Cleanup(func() {
		client.request("POST", fmt.Sprintf("/api/v1/instances/%d/kill", instanceID), nil)
		client.request("DELETE", fmt.Sprintf("/api/v1/instances/%d", instanceID), nil)
	})

	// 验收 3：先连终端（实例未起），随后启动以捕获实时启动日志。
	token, wsURL := issueTerminalToken(t, client, instanceID)
	conn := dialTerminal(t, wsURL, token)
	defer conn.Close()
	term := newTermReader(conn) // 持续 drain，避免代理因测试侧不读而背压关连接

	// 验收 2(下)：启动实例 → RUNNING。
	resp, data, err = client.request("POST", fmt.Sprintf("/api/v1/instances/%d/start", instanceID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "启动实例失败: %s", string(data))

	// 验收 2：终端可见服务端启动日志（"Done!"）。
	out, ok := term.waitFor("Done!", 30*time.Second)
	require.True(t, ok, "终端未见服务端启动日志，已收到:\n%s", out)
	t.Log("验收2 通过：实例启动，终端可见启动日志")
	requireInstanceState(t, client, instanceID, "RUNNING", 15*time.Second)

	// 验收 3：终端执行 list 并看到输出（此时无玩家）。
	termSend(t, conn, "list")
	out, ok = term.waitFor("players online", 15*time.Second)
	require.True(t, ok, "终端 list 无响应，已收到:\n%s", out)
	t.Log("验收3 通过：终端交互（list）有响应")

	// 验收 4：创建指向该运行实例的 Bot，Bot 实际进服。
	botCfg := fmt.Sprintf(`{"server":"127.0.0.1","port":%d,"version":"1.8.9","username":"E2EJoinBot"}`, mcPort)
	resp, data, err = client.request("POST", "/api/v1/bots", map[string]interface{}{
		"instanceId": instanceID,
		"name":       "e2e-join-bot",
		"config":     botCfg,
		"behavior":   "idle",
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "创建 Bot 失败: %s", string(data))
	botID := uint(parseJSON(t, data)["id"].(float64))
	t.Logf("Bot 已创建 id=%d，等待进服…", botID)

	// 验收 4(a)：前端可见 Bot 状态变 connected（经 CP API 拉取 Worker 实时状态）。
	requireBotStatus(t, client, botID, "connected", 45*time.Second)
	t.Log("验收4(a) 通过：Bot 状态 connected")

	// 验收 4(b)：服务端 list 可见该 Bot（list 专属输出格式，区别于 join 日志）。
	termSend(t, conn, "list")
	out, ok = term.waitFor("online: E2EJoinBot", 15*time.Second)
	require.True(t, ok, "服务端 list 未见该 Bot，已收到:\n%s", out)
	t.Log("验收4(b) 通过：服务端 list 可见 Bot")

	// 验收 5：终端对 Bot 下发可见交互（服务端 say）。
	termSend(t, conn, "say hello-from-e2e")
	out, ok = term.waitFor("[Server] hello-from-e2e", 15*time.Second)
	require.True(t, ok, "终端 say 无回显，已收到:\n%s", out)
	t.Log("验收5(a) 通过：终端可见交互（say）")

	// 验收 5：停止实例后，Bot 随之断开、状态回落 disconnected。
	resp, data, err = client.request("POST", fmt.Sprintf("/api/v1/instances/%d/stop", instanceID), nil)
	require.NoError(t, err)
	require.Contains(t, []int{http.StatusOK, http.StatusAccepted}, resp.StatusCode, "停止实例失败: %s", string(data))
	requireBotStatus(t, client, botID, "disconnected", 30*time.Second)
	t.Log("验收5(b) 通过：实例停止后 Bot 回落 disconnected")

	t.Log("FR-043 全链路 e2e（验收 1~5）通过")
}

// freePort 取一个当前空闲的 TCP 端口。
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// --- 终端 WS 辅助 ---

// termMsg 镜像 Worker 终端 WS 的消息结构（ws.TerminalMessage 的 JSON 子集）。
type termMsg struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
}

func issueTerminalToken(t *testing.T, client *e2eClient, instanceID uint) (token, wsURL string) {
	t.Helper()
	resp, data, err := client.request("GET", fmt.Sprintf("/api/v1/instances/%d/terminal-token", instanceID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "签发终端 token 失败: %s", string(data))
	m := parseJSON(t, data)
	token, _ = m["token"].(string)
	wsURL, _ = m["wsUrl"].(string)
	require.NotEmpty(t, token, "终端 token 为空")
	require.NotEmpty(t, wsURL, "终端 wsUrl 为空")
	return token, wsURL
}

func dialTerminal(t *testing.T, wsURL, token string) *websocket.Conn {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL+"?token="+token, nil)
	if resp != nil {
		defer resp.Body.Close()
	}
	require.NoError(t, err, "终端 WS 连接失败: %s", wsURL)
	return conn
}

func termSend(t *testing.T, conn *websocket.Conn, data string) {
	t.Helper()
	require.NoError(t, conn.WriteJSON(termMsg{Type: "stdin", Data: data}), "终端发送失败")
}

// termReader 在后台持续读取终端 stdout/stderr 并累积到缓冲区。
// 持续 drain 避免 CP 代理因测试侧长时间不读而背压/写超时关闭连接（验收 4(a) 轮询期间）。
type termReader struct {
	mu  sync.Mutex
	buf strings.Builder
}

func newTermReader(conn *websocket.Conn) *termReader {
	r := &termReader{}
	go func() {
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m termMsg
			if json.Unmarshal(raw, &m) == nil && m.Data != "" {
				r.mu.Lock()
				r.buf.WriteString(m.Data)
				r.mu.Unlock()
			}
		}
	}()
	return r
}

// waitFor 轮询累积缓冲区，直到出现 substr 或超时。返回当前累积内容与是否命中。
func (r *termReader) waitFor(substr string, timeout time.Duration) (string, bool) {
	deadline := time.Now().Add(timeout)
	for {
		r.mu.Lock()
		s := r.buf.String()
		r.mu.Unlock()
		if strings.Contains(s, substr) {
			return s, true
		}
		if time.Now().After(deadline) {
			return s, false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// --- 状态轮询辅助 ---

func requireInstanceState(t *testing.T, client *e2eClient, instanceID uint, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		_, data, err := client.request("GET", fmt.Sprintf("/api/v1/instances/%d", instanceID), nil)
		if err == nil {
			last = parseJSON(t, data)["status"].(string)
			if last == want {
				return
			}
			if last == "CRASHED" && want != "CRASHED" {
				t.Fatalf("实例意外 CRASHED（期望 %s）", want)
			}
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatalf("实例未在 %s 内进入 %s（最后状态 %s）", timeout, want, last)
}

func requireBotStatus(t *testing.T, client *e2eClient, botID uint, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		_, data, err := client.request("GET", fmt.Sprintf("/api/v1/bots/%d", botID), nil)
		if err == nil {
			if s, ok := parseJSON(t, data)["status"].(string); ok {
				last = s
				if last == want {
					return
				}
			}
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatalf("Bot 未在 %s 内变为 %s（最后状态 %s）", timeout, want, last)
}
