package heartbeat

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/proto/workerpb"
)

func TestSanitizeCommandSummary_RedactsAndTruncates(t *testing.T) {
	got := sanitizeCommandSummary([]string{
		"java",
		"-Ddb.password=hunter2",
		"--token",
		"abcd",
		"-jar",
		"server.jar",
	})

	require.NotContains(t, got, "hunter2")
	require.NotContains(t, got, "abcd")
	require.Contains(t, got, "-Ddb.password=***")
	require.Contains(t, got, "--token ***")
}

func TestTopProcessSamples_SortsByCPUThenMemory(t *testing.T) {
	samples := []*workerpb.ProcessMetricSample{
		{Pid: 1, CpuPercent: 10, RssBytes: 100},
		{Pid: 2, CpuPercent: 30, RssBytes: 20},
		{Pid: 3, CpuPercent: 30, RssBytes: 200},
	}

	got := topProcessSamples(samples, 2)

	require.Len(t, got, 2)
	require.Equal(t, int32(3), got[0].Pid)
	require.Equal(t, int32(2), got[1].Pid)
}

func TestProcessIOBytesPerSec_ComputesDeltaRate(t *testing.T) {
	readBps, writeBps := processIOBytesPerSec(
		processIOState{readBytes: 1000, writeBytes: 2000, sampledAt: 1_000},
		processIOState{readBytes: 2500, writeBytes: 2600, sampledAt: 2_500},
	)

	require.Equal(t, uint64(1000), readBps)
	require.Equal(t, uint64(400), writeBps)
}

func TestProcessIOBytesPerSec_HandlesCounterReset(t *testing.T) {
	readBps, writeBps := processIOBytesPerSec(
		processIOState{readBytes: 1000, writeBytes: 2000, sampledAt: 1_000},
		processIOState{readBytes: 900, writeBytes: 1900, sampledAt: 2_000},
	)

	require.Zero(t, readBps)
	require.Zero(t, writeBps)
}
