package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// realProbeMetrics 取自真机：真 Paper 1.21 + ServerProbe 0.1.0 的 /metrics 实际输出片段
// （真机复验时 curl http://127.0.0.1:9940/metrics 抓取）。
const realProbeMetrics = `# TYPE serverprobe_heap_used_bytes gauge
serverprobe_heap_used_bytes{serverId="8e95e963-5eaa-4378-8ff7-67edaad2f0a1",platform="BUKKIT"} 391380736
serverprobe_heap_max_bytes{serverId="8e95e963-5eaa-4378-8ff7-67edaad2f0a1",platform="BUKKIT"} 2147483648
# TYPE serverprobe_gc_count_total counter
serverprobe_gc_count_total{serverId="8e95e963-5eaa-4378-8ff7-67edaad2f0a1",platform="BUKKIT",gc="G1 Young Generation"} 16
serverprobe_threads{serverId="8e95e963-5eaa-4378-8ff7-67edaad2f0a1",platform="BUKKIT"} 59
serverprobe_threads_daemon{serverId="8e95e963-5eaa-4378-8ff7-67edaad2f0a1",platform="BUKKIT"} 34
serverprobe_system_cpu_load{serverId="8e95e963-5eaa-4378-8ff7-67edaad2f0a1",platform="BUKKIT"} 0.0785627748256803
serverprobe_uptime_seconds{serverId="8e95e963-5eaa-4378-8ff7-67edaad2f0a1",platform="BUKKIT"} 112.706
serverprobe_tps{serverId="8e95e963-5eaa-4378-8ff7-67edaad2f0a1",platform="BUKKIT",window="1m"} 20.03641193799071
serverprobe_tps{serverId="8e95e963-5eaa-4378-8ff7-67edaad2f0a1",platform="BUKKIT",window="5m"} 20.00727179639017
serverprobe_tps{serverId="8e95e963-5eaa-4378-8ff7-67edaad2f0a1",platform="BUKKIT",window="15m"} 20.00242334472774
serverprobe_mspt_seconds{serverId="8e95e963-5eaa-4378-8ff7-67edaad2f0a1",platform="BUKKIT",quantile="avg"} 6.011021099999999E-4
serverprobe_mspt_seconds{serverId="8e95e963-5eaa-4378-8ff7-67edaad2f0a1",platform="BUKKIT",quantile="p99"} 0.0011893000000000001
serverprobe_players_online{serverId="8e95e963-5eaa-4378-8ff7-67edaad2f0a1",platform="BUKKIT"} 7
serverprobe_world_loaded_chunks{serverId="8e95e963-5eaa-4378-8ff7-67edaad2f0a1",platform="BUKKIT",world="world"} 49
serverprobe_world_entities{serverId="8e95e963-5eaa-4378-8ff7-67edaad2f0a1",platform="BUKKIT",world="world"} 84
serverprobe_world_tile_entities{serverId="8e95e963-5eaa-4378-8ff7-67edaad2f0a1",platform="BUKKIT",world="world"} 2
serverprobe_world_loaded_chunks{serverId="8e95e963-5eaa-4378-8ff7-67edaad2f0a1",platform="BUKKIT",world="world_nether"} 12
`

// TestParseServerProbeMetrics_RealSample 用真机 /metrics 样本验证解析：TPS 取 1m 窗口、MSPT 取 avg 转毫秒、
// 在线人数、堆内存、线程、CPU、按世界负载等均正确提取。
func TestParseServerProbeMetrics_RealSample(t *testing.T) {
	snap := parseServerProbeMetrics(realProbeMetrics)
	require.NotNil(t, snap)

	assert.InDelta(t, 20.0364, snap.TPS, 0.001, "TPS 取 window=1m")
	assert.InDelta(t, 0.6011, snap.MSPTAvgMillis, 0.001, "MSPT avg 由秒转毫秒")
	assert.Equal(t, int32(7), snap.PlayersOnline)
	assert.Equal(t, int64(391380736), snap.HeapUsedBytes)
	assert.Equal(t, int64(2147483648), snap.HeapMaxBytes)
	assert.Equal(t, int32(59), snap.Threads)
	assert.InDelta(t, 0.07856, snap.SystemCPULoad, 0.0001)
	assert.InDelta(t, 112.706, snap.UptimeSeconds, 0.001)

	require.Contains(t, snap.Worlds, "world")
	assert.Equal(t, int64(49), snap.Worlds["world"].LoadedChunks)
	assert.Equal(t, int64(84), snap.Worlds["world"].Entities)
	assert.Equal(t, int64(2), snap.Worlds["world"].TileEntities)
	assert.Equal(t, int64(12), snap.Worlds["world_nether"].LoadedChunks)

	// GC（FR-465）：真机样本只有 serverprobe_gc_count_total{gc="G1 Young Generation"}=16。
	assert.Equal(t, int64(16), snap.GCCountTotal)
	assert.InDelta(t, 0, snap.GCTimeMillis, 0.0001)
}

// TestParseServerProbeMetrics_GC 覆盖 GC 跨收集器累加与耗时秒→毫秒（FR-465）。
func TestParseServerProbeMetrics_GC(t *testing.T) {
	snap := parseServerProbeMetrics(realProbeMetrics + `# TYPE serverprobe_gc_count_total counter
serverprobe_gc_count_total{gc="G1 Old Generation"} 3
serverprobe_gc_count_total{gc="G1 Young Generation"} 17
serverprobe_gc_time_seconds_total{gc="G1 Young Generation"} 1.5
serverprobe_gc_time_seconds_total{gc="G1 Old Generation"} 0.25
`)
	// 累加：Young(16+17) + Old(3) = 36
	assert.Equal(t, int64(36), snap.GCCountTotal)
	// 秒→毫秒：(1.5 + 0.25) * 1000 = 1750
	assert.InDelta(t, 1750, snap.GCTimeMillis, 0.001)
}

// TestParseServerProbeMetrics_ProxyFallback 代理端无 players_online，用 proxy_players_online 回退。
func TestParseServerProbeMetrics_ProxyFallback(t *testing.T) {
	snap := parseServerProbeMetrics(`serverprobe_proxy_players_online{serverId="p",platform="BUNGEE"} 42` + "\n")
	assert.Equal(t, int32(42), snap.PlayersOnline)
}

// TestParsePromLine 覆盖带 label 与无 label 两种行，以及非法行。
func TestParsePromLine(t *testing.T) {
	s, ok := parsePromLine(`serverprobe_tps{window="1m"} 20.5`)
	require.True(t, ok)
	assert.Equal(t, "serverprobe_tps", s.name)
	assert.Equal(t, "1m", s.labels["window"])
	assert.InDelta(t, 20.5, s.value, 0.0001)

	s2, ok2 := parsePromLine(`serverprobe_uptime_seconds 99`)
	require.True(t, ok2)
	assert.Equal(t, "serverprobe_uptime_seconds", s2.name)
	assert.InDelta(t, 99, s2.value, 0.0001)

	_, ok3 := parsePromLine(`# HELP something`)
	assert.False(t, ok3)
}

// TestParseServerProbeMetrics_GCNoDoubleCount （S2）同名变体多收集器继续累加，
// 但不同命名变体并存时只认首个命中的（避免同一份耗时重复累加、成倍放大 GC 占用）。
func TestParseServerProbeMetrics_GCNoDoubleCount(t *testing.T) {
	// ① 同变体多收集器：仍应累加。
	same := parseServerProbeMetrics(`serverprobe_gc_time_seconds_total{gc="G1 Young Generation"} 1.5
serverprobe_gc_time_seconds_total{gc="G1 Old Generation"} 0.25
`)
	assert.InDelta(t, 1750, same.GCTimeMillis, 0.001, "同变体多收集器需继续累加")

	// ② 秒变体在前、毫秒变体在后：只认秒变体（首条命中）。
	mixed := parseServerProbeMetrics(`serverprobe_gc_time_seconds_total{gc="A"} 2
serverprobe_gc_time_millis_total{gc="A"} 2000
`)
	assert.InDelta(t, 2000, mixed.GCTimeMillis, 0.001, "异变体不得重复累加")

	// ③ 毫秒变体在前、秒变体在后：只认毫秒变体。
	mixed2 := parseServerProbeMetrics(`serverprobe_gc_time_ms_total{gc="A"} 2000
serverprobe_gc_time_seconds_total{gc="A"} 2
`)
	assert.InDelta(t, 2000, mixed2.GCTimeMillis, 0.001, "异变体不得重复累加")
}
