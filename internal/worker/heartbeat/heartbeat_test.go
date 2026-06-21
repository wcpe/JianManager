package heartbeat

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/process"
)

// fakeProbeBody 是一份可被 metrics.parseServerProbeMetrics 解析的 ServerProbe /metrics 文本。
const fakeProbeBody = `# HELP serverprobe_tps 每秒 tick
serverprobe_tps{window="1m"} 19.5
serverprobe_mspt_seconds{quantile="avg"} 0.0123
serverprobe_players_online 7
serverprobe_heap_used_bytes 1073741824
serverprobe_heap_max_bytes 2147483648
serverprobe_threads 42
serverprobe_system_cpu_load 0.25
serverprobe_uptime_seconds 3600
serverprobe_world_loaded_chunks{world="world"} 100
serverprobe_world_entities{world="world"} 50
serverprobe_world_tile_entities{world="world"} 20
`

func portOf(t *testing.T, rawURL string) int {
	t.Helper()
	u, err := url.Parse(rawURL)
	require.NoError(t, err)
	_, portStr, err := net.SplitHostPort(u.Host)
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	return port
}

func TestCollectInstanceMetrics_ScrapesRunningWithProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fakeProbeBody))
	}))
	defer srv.Close()
	port := portOf(t, srv.URL)

	snaps := []process.InstanceSnapshot{
		{UUID: "running-with-probe", State: "RUNNING", ProbePort: port},
		{UUID: "running-no-probe", State: "RUNNING", ProbePort: 0},
		{UUID: "stopped-with-probe", State: "STOPPED", ProbePort: port},
	}
	out := collectInstanceMetrics(snaps)

	require.Len(t, out, 1, "仅采集 RUNNING 且 ProbePort>0 的实例")
	s := out[0]
	require.Equal(t, "running-with-probe", s.InstanceUuid)
	require.True(t, s.ProbeAvailable)
	require.Equal(t, 19.5, s.Tps)
	require.InDelta(t, 12.3, s.MsptMillis, 1e-9)
	require.Equal(t, int32(7), s.PlayersOnline)
	require.Equal(t, int64(1073741824), s.HeapUsedBytes)
	require.Equal(t, int32(42), s.Threads)
	require.InDelta(t, 0.25, s.CpuLoad, 1e-9)
	require.Len(t, s.Worlds, 1)
	require.Equal(t, "world", s.Worlds[0].Name)
	require.Equal(t, int64(100), s.Worlds[0].LoadedChunks)
	require.Equal(t, int64(50), s.Worlds[0].Entities)
}

func TestCollectInstanceMetrics_ProbeErrorMarksUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	port := portOf(t, srv.URL)

	out := collectInstanceMetrics([]process.InstanceSnapshot{
		{UUID: "inst-1", State: "RUNNING", ProbePort: port},
	})
	require.Len(t, out, 1, "抓取失败仍产出一条快照（标记探针不可用）")
	require.Equal(t, "inst-1", out[0].InstanceUuid)
	require.False(t, out[0].ProbeAvailable)
}

func TestCollectInstanceMetrics_NoTargets(t *testing.T) {
	require.Nil(t, collectInstanceMetrics(nil))
	require.Nil(t, collectInstanceMetrics([]process.InstanceSnapshot{
		{UUID: "s", State: "STOPPED", ProbePort: 9999},
		{UUID: "r", State: "RUNNING", ProbePort: 0},
	}), "无可采实例返回 nil")
}
