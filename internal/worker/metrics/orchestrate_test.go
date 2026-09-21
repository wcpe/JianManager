package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func testProbe() *ProbeSnapshot {
	return &ProbeSnapshot{
		TPS:           19.5,
		MSPTAvgMillis: 12.0,
		PlayersOnline: 7,
		HeapUsedBytes: 512,
		HeapMaxBytes:  1024,
		Threads:       42,
		SystemCPULoad: 0.25,
		UptimeSeconds: 100,
		Worlds:        map[string]WorldStat{"world": {LoadedChunks: 100}},
	}
}

func testSLP() *SLPSnapshot {
	return &SLPSnapshot{
		Motd:          "SLP MOTD",
		Version:       "1.20.4",
		PlayersOnline: 5,
		PlayersMax:    20,
		PlayerSample:  []string{"Steve"},
		Favicon:       "data:image/png;base64,SLP",
	}
}

func testQuery() *QuerySnapshot {
	return &QuerySnapshot{
		Motd:                   "Query MOTD",
		Version:                "1.20.4",
		Plugins:                []string{"WorldEdit"},
		Map:                    "world",
		PlayersOnline:          6,
		PlayersOnlineAvailable: true,
		PlayersMax:             30,
		PlayerNames:            []string{"RealPlayer"},
	}
}

// TestOrchestrateAllUnavailable 三档皆无 → 所有可用位 false（显式「不可用」，无伪值）。
func TestOrchestrateAllUnavailable(t *testing.T) {
	tel := Orchestrate(nil, nil, nil)
	assert.False(t, tel.ProbeAvailable)
	assert.False(t, tel.SLPAvailable)
	assert.False(t, tel.QueryAvailable)
	assert.Equal(t, SourceNone, tel.Sources)
	assert.False(t, tel.PlayersOnlineAvailable)
	assert.False(t, tel.PlayersMaxAvailable)
	assert.False(t, tel.MotdAvailable)
	assert.False(t, tel.VersionAvailable)
	assert.False(t, tel.PlayerNamesAvailable)
	assert.False(t, tel.PluginsAvailable)
	assert.False(t, tel.MapAvailable)
	assert.False(t, tel.FaviconAvailable)
	assert.Empty(t, tel.PlayerNames)
}

// TestOrchestrateProbeOnly 仅探针：深度指标 + 在线人数可用；MOTD/版本/最大人数/名单不可用。
func TestOrchestrateProbeOnly(t *testing.T) {
	tel := Orchestrate(testProbe(), nil, nil)
	assert.True(t, tel.ProbeAvailable)
	assert.Equal(t, SourceProbe, tel.Sources)
	assert.Equal(t, 19.5, tel.TPS)
	assert.True(t, tel.PlayersOnlineAvailable)
	assert.Equal(t, int32(7), tel.PlayersOnline)
	assert.False(t, tel.PlayersMaxAvailable, "探针无最大人数")
	assert.False(t, tel.MotdAvailable, "探针无 MOTD")
	assert.False(t, tel.PlayerNamesAvailable)
}

// TestOrchestrateSLPOnly 仅 SLP：MOTD/版本/在线/最大人数可用；名单为弱信息（Partial）。
func TestOrchestrateSLPOnly(t *testing.T) {
	tel := Orchestrate(nil, testSLP(), nil)
	assert.True(t, tel.SLPAvailable)
	assert.Equal(t, SourceSLP, tel.Sources)
	assert.Equal(t, "SLP MOTD", tel.Motd)
	assert.True(t, tel.MotdAvailable)
	assert.True(t, tel.VersionAvailable)
	assert.True(t, tel.PlayersOnlineAvailable)
	assert.Equal(t, int32(5), tel.PlayersOnline)
	assert.True(t, tel.PlayersMaxAvailable)
	assert.Equal(t, int32(20), tel.PlayersMax)
	assert.True(t, tel.PlayerNamesAvailable)
	assert.True(t, tel.PlayerNamesPartial, "SLP 名单应标注不完整")
	assert.Equal(t, []string{"Steve"}, tel.PlayerNames)
	// SLP 不提供 TPS/插件/地图。
	assert.Zero(t, tel.TPS)
	assert.False(t, tel.PluginsAvailable)
	assert.False(t, tel.MapAvailable)
}

// TestOrchestrateQueryOnly 仅 Query：实名名单 + 插件 + 地图可用，Partial=false。
func TestOrchestrateQueryOnly(t *testing.T) {
	tel := Orchestrate(nil, nil, testQuery())
	assert.True(t, tel.QueryAvailable)
	assert.Equal(t, SourceQuery, tel.Sources)
	assert.Equal(t, "Query MOTD", tel.Motd)
	assert.True(t, tel.PlayersOnlineAvailable)
	assert.Equal(t, int32(6), tel.PlayersOnline)
	assert.True(t, tel.PlayerNamesAvailable)
	assert.False(t, tel.PlayerNamesPartial, "Query 是实名来源")
	assert.Equal(t, []string{"RealPlayer"}, tel.PlayerNames)
	assert.True(t, tel.PluginsAvailable)
	assert.Equal(t, []string{"WorldEdit"}, tel.Plugins)
	assert.True(t, tel.MapAvailable)
	assert.Equal(t, "world", tel.Map)
	assert.True(t, tel.PlayersMaxAvailable)
	assert.Equal(t, int32(30), tel.PlayersMax)
}

// TestOrchestrateQueryMissingNumplayersKeepsPlayersUnavailable Query 可用但缺 numplayers →
// 在线人数保持「不可用」，绝不落成 0（FR-447 不伪造 0）。
func TestOrchestrateQueryMissingNumplayersKeepsPlayersUnavailable(t *testing.T) {
	q := testQuery()
	q.PlayersOnlineAvailable = false
	q.PlayersOnline = 0
	tel := Orchestrate(nil, nil, q)
	assert.True(t, tel.QueryAvailable, "Query 本身仍算可用（其它指标照常回填）")
	assert.False(t, tel.PlayersOnlineAvailable, "缺 numplayers 时不得声称在线人数可用")
	assert.True(t, tel.PlayerNamesAvailable, "实名名单不受 numplayers 缺失影响")
	// SLP 存在时仍由 SLP 回填在线人数（SLP 的 players.online 为协议必带字段）。
	slp := testSLP()
	tel2 := Orchestrate(nil, slp, q)
	assert.True(t, tel2.PlayersOnlineAvailable)
	assert.Equal(t, int32(5), tel2.PlayersOnline)
}

// TestOrchestrateProbeWinsSameMetric 同指标多源以探针为准（在线人数取探针值）。
func TestOrchestrateProbeWinsSameMetric(t *testing.T) {
	tel := Orchestrate(testProbe(), testSLP(), testQuery())
	assert.Equal(t, SourceProbe|SourceSLP|SourceQuery, tel.Sources)
	// 在线人数：探针 7 胜出 SLP 5 / Query 6。
	assert.True(t, tel.PlayersOnlineAvailable)
	assert.Equal(t, int32(7), tel.PlayersOnline)
	// 最大人数：探针无 → SLP 20 胜出 Query 30（SLP 优先于 Query）。
	assert.Equal(t, int32(20), tel.PlayersMax)
	// MOTD：探针无 → SLP 优先于 Query。
	assert.Equal(t, "SLP MOTD", tel.Motd)
	// 玩家名单：Query 实名优先于 SLP sample。
	assert.Equal(t, []string{"RealPlayer"}, tel.PlayerNames)
	assert.False(t, tel.PlayerNamesPartial)
	// 插件/地图仅 Query 有。
	assert.Equal(t, []string{"WorldEdit"}, tel.Plugins)
	assert.Equal(t, "world", tel.Map)
	// 深度指标仍来自探针。
	assert.Equal(t, 19.5, tel.TPS)
}

// TestOrchestratePlayerNamesFromSLPWhenQueryEmpty Query 无名单时回退 SLP sample 并标注不完整。
func TestOrchestratePlayerNamesFromSLPWhenQueryEmpty(t *testing.T) {
	q := testQuery()
	q.PlayerNames = nil
	tel := Orchestrate(nil, testSLP(), q)
	assert.True(t, tel.PlayerNamesAvailable)
	assert.True(t, tel.PlayerNamesPartial)
	assert.Equal(t, []string{"Steve"}, tel.PlayerNames)
}

// TestOrchestrateFaviconFromSLP favicon 由 SLP 提供。
func TestOrchestrateFaviconFromSLP(t *testing.T) {
	tel := Orchestrate(nil, testSLP(), nil)
	assert.True(t, tel.FaviconAvailable)
	assert.Equal(t, "data:image/png;base64,SLP", tel.Favicon)
}

// TestCollectInstanceTelemetryNoPorts 无任何端口 → 全不可用，且不 panic（编排链终点）。
func TestCollectInstanceTelemetryNoPorts(t *testing.T) {
	tel := CollectInstanceTelemetry(CollectConfig{Host: "127.0.0.1"})
	assert.False(t, tel.ProbeAvailable)
	assert.False(t, tel.SLPAvailable)
	assert.False(t, tel.QueryAvailable)
	assert.False(t, tel.PlayersOnlineAvailable)
}
