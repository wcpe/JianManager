//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// 假探针（fakeProbe）走 Worker 真实的 /ws/plugin-bridge 端点，模拟 ServerProbe 的上行帧。
//
// ## 为什么需要一个假探针
//
// 只验 CP 编排不验 ACK，等于没验「下发是否真的被应用」——而那正是整个托管模式的核心承诺。
// 假探针让整条链路都是真的：真 Worker 的会话管理、真 gRPC StreamPluginEvents、
// 真 CP 的事件分流与落库、真矩阵读模型。唯一被替身的是「谁在 WS 另一端」。
//
// ## 帧格式以探针源码为准
//
// 结构化字段一律放在 `payload` 下（ServerProbe BridgeFrames）：`config_applied` 与
// `config_state` 都是 `{"type":"event","event":"<名>","instance":"<uuid>","payload":{...}}`。
// 这一点必须被测试覆盖——CP 曾把整帧当载荷解析，导致所有回执被判成 failed。

// e2eWSSecret 是 setupCluster 下发给 CP 的插件桥密钥，假探针用它签发桥 token。
const e2eWSSecret = "test-secret-e2e-ws"

// fakeProbe 是一条已建立的假探针会话。
type fakeProbe struct {
	t        *testing.T
	conn     *websocket.Conn
	instance string

	writeMu sync.Mutex

	// pushCh 收到下行 config_push 时投递其载荷（缓冲避免测试不读时阻塞读循环）。
	pushCh chan map[string]interface{}
	// done 关闭表示读循环已退出。
	done chan struct{}
}

// dialFakeProbe 以插件桥 token 连入 Worker 的 /ws/plugin-bridge 端点。
//
// wsURL 形如 ws://127.0.0.1:<wsPort>/ws/plugin-bridge，token 是 scope=plugin-bridge 的 JWT。
func dialFakeProbe(t *testing.T, wsURL, instanceUUID string) *fakeProbe {
	t.Helper()
	token := issueBridgeToken(t, instanceUUID, e2eWSSecret)
	url := fmt.Sprintf("%s?token=%s&instance=%s", wsURL, token, instanceUUID)
	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if resp != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		// 握手失败时把状态码与响应体带出来：Worker 用 401/400 区分密钥错、scope 错与实例不匹配，
		// 只报 "bad handshake" 会让排障绕远路。
		detail := ""
		if resp != nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			detail = fmt.Sprintf(" status=%d body=%s", resp.StatusCode, string(body))
		}
		require.NoError(t, err, "假探针连接插件桥失败: %s%s", wsURL, detail)
	}

	p := &fakeProbe{
		t:        t,
		conn:     conn,
		instance: instanceUUID,
		pushCh:   make(chan map[string]interface{}, 8),
		done:     make(chan struct{}),
	}
	go p.readLoop()
	t.Cleanup(func() { p.Close() })
	return p
}

// issueBridgeToken 本地签发插件桥 JWT（与 CP 的 PluginBridgeService 同 claims）。
//
// 直接签发而不是调 HTTP：CP 没有暴露「签发桥 token」的 HTTP 端点（它是建服时写进
// config.yml 的），而测试要的正是**探针那一侧**的凭据形态。
func issueBridgeToken(t *testing.T, instanceUUID, secret string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"instanceId": instanceUUID,
		"scope":      "plugin-bridge",
		"exp":        time.Now().Add(time.Hour).Unix(),
		"iat":        time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	require.NoError(t, err, "签发插件桥 token 失败")
	return signed
}

// readLoop 持续读取下行帧：config 帧投递给测试，其余忽略（pong 等）。
func (p *fakeProbe) readLoop() {
	defer close(p.done)
	for {
		_, raw, err := p.conn.ReadMessage()
		if err != nil {
			return
		}
		var frame map[string]interface{}
		if err := json.Unmarshal(raw, &frame); err != nil {
			continue
		}
		// 下行 config_push：CP 经 Worker SendPluginCommand 送到这里。
		// 载荷可能是嵌套对象（payloadJson 解析后）或字符串，两种都认。
		if action, _ := frame["action"].(string); action == "config_push" {
			payload := extractPushPayload(frame)
			if payload != nil {
				select {
				case p.pushCh <- payload:
				default: // 测试没读：丢弃而不是阻塞读循环
				}
			}
		}
	}
}

// extractPushPayload 从下行 command 帧里取出 config_push 载荷。
func extractPushPayload(frame map[string]interface{}) map[string]interface{} {
	if nested, ok := frame["payloadJson"].(map[string]interface{}); ok {
		return nested
	}
	if raw, ok := frame["payloadJson"].(string); ok && raw != "" {
		var out map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &out); err == nil {
			return out
		}
	}
	// 部分路径把载荷平铺在 args 下。
	if nested, ok := frame["args"].(map[string]interface{}); ok {
		return nested
	}
	return nil
}

// Send 写一帧 JSON。
func (p *fakeProbe) Send(frame map[string]interface{}) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return p.conn.WriteJSON(frame)
}

// Hello 发 hello 帧，声明托管模式与三项托管能力位（与探针真实行为一致）。
func (p *fakeProbe) Hello() error {
	return p.Send(map[string]interface{}{
		"type":         "hello",
		"instance":     p.instance,
		"platform":     "bukkit",
		"version":      "0.8.0-e2e",
		"managed":      true,
		"capabilities": []string{"metric_push", "config_push", "config_state"},
	})
}

// WaitPush 等待一条下行 config_push，超时失败。
func (p *fakeProbe) WaitPush(timeout time.Duration) map[string]interface{} {
	p.t.Helper()
	select {
	case push := <-p.pushCh:
		return push
	case <-time.After(timeout):
		p.t.Fatalf("假探针未在 %s 内收到 config_push", timeout)
		return nil
	}
}

// ReportApplied 回 config_applied 帧：与探针 BridgeFrames.configApplied 结构逐字一致。
//
// 载荷放在 `payload` 下（不是 `data`），这正是 CP 早期解析错位的地方。
func (p *fakeProbe) ReportApplied(configVersion int64, result string, effective map[string]interface{}) error {
	payload := map[string]interface{}{
		"configVersion": configVersion,
		"result":        result,
		"effective":     effective,
		"appliedAt":     time.Now().UnixMilli(),
	}
	if result != "applied" {
		payload["error"] = "e2e 模拟失败"
	}
	return p.Send(map[string]interface{}{
		"type":      "event",
		"event":     "config_applied",
		"instance":  p.instance,
		"timestamp": time.Now().UnixMilli(),
		"dedupKey":  fmt.Sprintf("cfg:%d:%s", configVersion, result),
		"payload":   payload,
	})
}

// ReportState 回 config_state 帧：与探针 BridgeFrames.configState 结构一致。
//
// cacheAgeSeconds 传 nil 表示从未成功应用过——这与 0 语义不同，CP 必须把它落成 null。
func (p *fakeProbe) ReportState(configVersion int64, effective map[string]interface{}, cacheAgeSeconds *int64, connection string) error {
	return p.Send(map[string]interface{}{
		"type":      "event",
		"event":     "config_state",
		"instance":  p.instance,
		"timestamp": time.Now().UnixMilli(),
		"payload": map[string]interface{}{
			"configVersion":   configVersion,
			"effective":       effective,
			"cacheAgeSeconds": cacheAgeSeconds,
			"mode":            "managed",
			"connection":      connection,
			"capabilities":    []string{"metric_push", "config_push", "config_state"},
		},
	})
}

// Close 关闭会话。
func (p *fakeProbe) Close() {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_ = p.conn.Close()
}
