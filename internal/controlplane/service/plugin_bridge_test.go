package service

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testBridgeSecret = "cp-bridge-secret"

// TestPluginBridgeService_IssueToken 验证签发的 token 可被同 secret 验签，且 claims 满足 Worker 握手要求：
// scope=plugin-bridge、instanceId 为目标实例、HS256、未过期、有 iat/exp。
// 这是 CP 签发 ↔ Worker 校验契约的单侧固化（Worker 侧 validateBridgeToken 做同样断言）。
func TestPluginBridgeService_IssueToken(t *testing.T) {
	svc := NewPluginBridgeService(testBridgeSecret)

	tokenStr, err := svc.IssueToken("inst-uuid-1")
	require.NoError(t, err)
	require.NotEmpty(t, tokenStr)

	claims := jwt.MapClaims{}
	tok, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		// 仅接受 HS256，拒绝 alg 混淆。
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, assert.AnError
		}
		return []byte(testBridgeSecret), nil
	})
	require.NoError(t, err)
	require.True(t, tok.Valid)

	assert.Equal(t, "inst-uuid-1", claims["instanceId"])
	assert.Equal(t, PluginBridgeScope, claims["scope"])
	assert.Equal(t, "plugin-bridge", claims["scope"], "scope 必须是 plugin-bridge，Worker 据此与终端 token 区分")

	// 含 exp/iat，且 exp 在未来。
	exp, ok := claims["exp"].(float64)
	require.True(t, ok)
	assert.Greater(t, int64(exp), time.Now().Unix())
	_, ok = claims["iat"].(float64)
	assert.True(t, ok)
}

// TestPluginBridgeService_IssueToken_WrongSecretRejected 验证错误 secret 验签失败（防伪造）。
func TestPluginBridgeService_IssueToken_WrongSecretRejected(t *testing.T) {
	svc := NewPluginBridgeService(testBridgeSecret)
	tokenStr, err := svc.IssueToken("inst-uuid-1")
	require.NoError(t, err)

	claims := jwt.MapClaims{}
	_, err = jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		return []byte("wrong-secret"), nil
	})
	assert.Error(t, err)
}

// TestPluginBridgeService_BuildBridgeConfigBlock 验证生成的 bridge 段含 worker WS 地址、实例、token，
// 且 token+url 齐备时 enabled=true。
func TestPluginBridgeService_BuildBridgeConfigBlock(t *testing.T) {
	svc := NewPluginBridgeService(testBridgeSecret)
	block := svc.BuildBridgeConfigBlock("ws://127.0.0.1:9102/ws/plugin-bridge", "inst-uuid-1", "tok-abc")

	assert.Contains(t, block, "bridge:")
	assert.Contains(t, block, "enabled: true")
	assert.Contains(t, block, "ws://127.0.0.1:9102/ws/plugin-bridge")
	assert.Contains(t, block, "inst-uuid-1")
	assert.Contains(t, block, "tok-abc")
}

// TestPluginBridgeService_BuildBridgeConfigBlock_EmptyTokenDisabled 验证 token 为空时 enabled=false
// （签发失败的降级路径：探针不连反向 WS，/metrics 仍工作）。
func TestPluginBridgeService_BuildBridgeConfigBlock_EmptyTokenDisabled(t *testing.T) {
	svc := NewPluginBridgeService(testBridgeSecret)
	block := svc.BuildBridgeConfigBlock("ws://127.0.0.1:9102/ws/plugin-bridge", "inst-uuid-1", "")
	assert.Contains(t, block, "enabled: false")
}

// TestPluginBridgeWSURL 验证探针连入地址走本机回环（探针与 Worker 同机）。
func TestPluginBridgeWSURL(t *testing.T) {
	url := pluginBridgeWSURL(9102)
	assert.Equal(t, "ws://127.0.0.1:9102/ws/plugin-bridge", url)
	assert.True(t, strings.HasPrefix(url, "ws://127.0.0.1:"))
}

// TestBuildServerProbeConfig_AppendsBridgeBlock 验证 buildServerProbeConfig 在 metrics 段后追加 bridge 段，
// 且 bridge 段为空时不污染 config（向后兼容仅监控的实例）。
func TestBuildServerProbeConfig_AppendsBridgeBlock(t *testing.T) {
	withBridge := buildServerProbeConfig(29940, "bridge:\n  enabled: true\n")
	assert.Contains(t, withBridge, "metrics:")
	assert.Contains(t, withBridge, "port: 29940")
	assert.Contains(t, withBridge, "bridge:")

	noBridge := buildServerProbeConfig(29940, "")
	assert.Contains(t, noBridge, "metrics:")
	assert.NotContains(t, noBridge, "bridge:")
}

// TestBuildServerProbeConfig_AlwaysEnablesMCP 下发的探针配置**必须**带 `mcp.enabled: true`。
//
// 守的是一个真机踩到的静默断链：探针侧 `DEFAULT_MCP_ENABLED = false`，
// 而 JM 的 `probe_tools` / `probe_diagnose` 全靠探针注册 `core.mcp` 桥动作。
// 配置里不写 mcp 段时：
//   - 桥仍然连着（conn=true、probeVersion=2）
//   - /metrics 仍正常
//   - 但调任何诊断工具都回「本平台未实现治理执行」
//
// 这种“一半正常一半全废”的形态极难定位，所以用回归测试钉死。
func TestBuildServerProbeConfig_AlwaysEnablesMCP(t *testing.T) {
	for _, c := range []struct {
		name   string
		bridge string
	}{
		{"带 bridge 段（普通 daemon 部署）", "bridge:\n  enabled: true\n"},
		{"无 bridge 段（托管模式）", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := buildServerProbeConfig(29940, c.bridge)
			assert.Contains(t, cfg, "mcp:", "必须写出 mcp 段")
			assert.Contains(t, cfg, "enabled: true", "mcp 必须显式启用")
			// 托管模式下不应开出本地端口：port 必须为 0。
			assert.Contains(t, cfg, "port: 0", "托管传输走插件桥，不得监听本地端口")
		})
	}
}
