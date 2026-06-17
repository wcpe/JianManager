package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestQueryInstanceMetrics_Unreachable 验证 RCON 不可达时优雅降级返回 -1 标记值。
func TestQueryInstanceMetrics_Unreachable(t *testing.T) {
	// 使用一个几乎不可能有服务监听的高位端口
	tps, players, err := QueryInstanceMetrics("127.0.0.1", 19999, "test")

	assert.NoError(t, err, "优雅降级不应返回 error")
	assert.Equal(t, float32(-1), tps, "RCON 不可达时 TPS 应为 -1")
	assert.Equal(t, int32(-1), players, "RCON 不可达时在线玩家应为 -1")
}

// TestQueryInstanceMetrics_InvalidHost 验证无效主机地址同样优雅降级。
func TestQueryInstanceMetrics_InvalidHost(t *testing.T) {
	tps, players, err := QueryInstanceMetrics("192.0.2.1", 25575, "test")

	assert.NoError(t, err, "优雅降级不应返回 error")
	assert.Equal(t, float32(-1), tps)
	assert.Equal(t, int32(-1), players)
}
