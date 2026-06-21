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
