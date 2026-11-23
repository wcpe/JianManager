package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bot-worker 分片配置（见 BotWorkerConfig 注释：单 Node 进程承载 bot 有硬上限，
// 272 bot 时主线程 99.9% CPU、心跳停发，故支持按片拆分）。

// TestBotWorkerConfig_NormalizeDefaults 验证零值归一：片数默认 1（单进程）、容量默认 50。
func TestBotWorkerConfig_NormalizeDefaults(t *testing.T) {
	var c BotWorkerConfig // 零值
	n := c.Normalize()
	assert.Equal(t, 1, n.Shards, "Shards<=0 应归一为 1（与旧版单进程行为一致）")
	assert.Equal(t, 50, n.MaxBots, "MaxBots<=0 应回落内置默认 50")

	neg := BotWorkerConfig{Shards: -3, MaxBots: -1}
	n2 := neg.Normalize()
	assert.Equal(t, 1, n2.Shards)
	assert.Equal(t, 50, n2.MaxBots)
}

// TestBotWorkerConfig_PerShardMaxBots 验证每片容量按片数向上取整均分。
func TestBotWorkerConfig_PerShardMaxBots(t *testing.T) {
	cases := []struct {
		maxBots, shards, want int
	}{
		{500, 3, 167}, // 向上取整：3 片共 501，多一格无害
		{500, 1, 500}, // 单进程拿全部容量
		{300, 2, 150},
		{100, 4, 25},
		{10, 3, 4}, // 10/3 => 4
		{0, 0, 50}, // 零值归一后：单进程 50
	}
	for _, tc := range cases {
		got := BotWorkerConfig{MaxBots: tc.maxBots, Shards: tc.shards}.PerShardMaxBots()
		assert.Equal(t, tc.want, got, "maxBots=%d shards=%d", tc.maxBots, tc.shards)
	}
}

// TestBotWorkerConfig_ApplyEnvForShard 验证环境变量按片下发单片容量。
//
// 关键：多分片时每个子进程只应知道自己那一片的上限——若各片都按总容量准入，
// 合起来会超出节点可承载量。
func TestBotWorkerConfig_ApplyEnvForShard(t *testing.T) {
	c := BotWorkerConfig{MaxBots: 500, Shards: 3}

	env := c.ApplyEnvForShard(167)
	require.Len(t, env, 1)
	assert.Equal(t, "JM_BOT_WORKER_MAX_BOTS=167", env[0], "应下发单片容量而非总容量")

	// perShardMax<=0 时回退到总容量（保持旧调用点行为）。
	fallback := c.ApplyEnvForShard(0)
	require.Len(t, fallback, 1)
	assert.Equal(t, "JM_BOT_WORKER_MAX_BOTS=500", fallback[0])
}

// TestBotWorkerConfig_ApplyEnvUnchanged 回归保护：单进程路径（ApplyEnv）行为不变。
func TestBotWorkerConfig_ApplyEnvUnchanged(t *testing.T) {
	assert.Equal(t, []string{"JM_BOT_WORKER_MAX_BOTS=500"}, BotWorkerConfig{MaxBots: 500}.ApplyEnv())
	assert.Nil(t, BotWorkerConfig{MaxBots: 0}.ApplyEnv(), "零值不注入 env（沿用 bot-worker 内置默认）")
}
