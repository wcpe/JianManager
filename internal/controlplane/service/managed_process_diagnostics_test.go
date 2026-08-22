package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestManagedProcessDiagnosticsSkipsRSSGrowthWithZeroBaseline(t *testing.T) {
	now := time.Now()
	rows := []model.ProcessMetricSnapshot{
		{RSSBytes: 0, SampledAt: now.Add(-2 * time.Minute)},
		{RSSBytes: 0, SampledAt: now.Add(-1 * time.Minute)},
		{RSSBytes: 512 * 1024 * 1024, SampledAt: now},
	}
	history := summarizeManagedProcessHistory(rows)

	require.NotPanics(t, func() {
		diagnostics := managedProcessDiagnostics(history, rows)
		require.Empty(t, diagnostics)
	})
}

func TestManagedProcessDiagnostics_InsufficientSamples(t *testing.T) {
	now := time.Now()
	rows := []model.ProcessMetricSnapshot{
		{RSSBytes: 100, CPUPercent: 10, SampledAt: now},
		{RSSBytes: 200, CPUPercent: 20, SampledAt: now.Add(-10 * time.Second)},
	}
	history := summarizeManagedProcessHistory(rows)
	diagnostics := managedProcessDiagnostics(history, rows)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "insufficient_samples", diagnostics[0].Code)
	assert.Equal(t, "info", string(diagnostics[0].Severity))
}

func TestManagedProcessDiagnostics_StaleSamples(t *testing.T) {
	// 最新样本超过 90 秒 → stale_samples。
	old := time.Now().Add(-120 * time.Second)
	rows := []model.ProcessMetricSnapshot{
		{CPUPercent: 10, SampledAt: old.Add(-2 * time.Minute)},
		{CPUPercent: 10, SampledAt: old.Add(-1 * time.Minute)},
		{CPUPercent: 10, SampledAt: old},
	}
	history := summarizeManagedProcessHistory(rows)
	diagnostics := managedProcessDiagnostics(history, rows)
	codes := diagnosticCodes(diagnostics)
	assert.Contains(t, codes, "stale_samples")
}

func TestManagedProcessDiagnostics_HighCPU(t *testing.T) {
	now := time.Now()
	rows := []model.ProcessMetricSnapshot{
		{CPUPercent: 95, SampledAt: now.Add(-2 * time.Minute)},
		{CPUPercent: 90, SampledAt: now.Add(-1 * time.Minute)},
		{CPUPercent: 88, SampledAt: now},
	}
	history := summarizeManagedProcessHistory(rows)
	diagnostics := managedProcessDiagnostics(history, rows)
	codes := diagnosticCodes(diagnostics)
	assert.Contains(t, codes, "high_cpu")
}

func TestManagedProcessDiagnostics_HighWriteIO(t *testing.T) {
	now := time.Now()
	rows := []model.ProcessMetricSnapshot{
		{WriteBytesPerSec: 20 * 1024 * 1024, SampledAt: now.Add(-2 * time.Minute)},
		{WriteBytesPerSec: 15 * 1024 * 1024, SampledAt: now.Add(-1 * time.Minute)},
		{WriteBytesPerSec: 18 * 1024 * 1024, SampledAt: now},
	}
	history := summarizeManagedProcessHistory(rows)
	diagnostics := managedProcessDiagnostics(history, rows)
	codes := diagnosticCodes(diagnostics)
	assert.Contains(t, codes, "high_write_io")
}

func TestManagedProcessDiagnostics_RSSGrowth(t *testing.T) {
	now := time.Now()
	// 后半窗口最小 RSS 明显高于前半窗口最大 RSS（>20% 且 >256MiB）。
	rows := []model.ProcessMetricSnapshot{
		{RSSBytes: 1024 * 1024 * 1024, SampledAt: now.Add(-3 * time.Minute)},
		{RSSBytes: 1100 * 1024 * 1024, SampledAt: now.Add(-2 * time.Minute)},
		{RSSBytes: 1500 * 1024 * 1024, SampledAt: now.Add(-1 * time.Minute)},
		{RSSBytes: 1600 * 1024 * 1024, SampledAt: now},
	}
	history := summarizeManagedProcessHistory(rows)
	diagnostics := managedProcessDiagnostics(history, rows)
	codes := diagnosticCodes(diagnostics)
	assert.Contains(t, codes, "rss_growth")
}

func TestManagedProcessDiagnostics_NoGrowthBelowThreshold(t *testing.T) {
	now := time.Now()
	// 后半最小 1080MiB <= 前半最大 1100MiB → 无增长诊断。
	rows := []model.ProcessMetricSnapshot{
		{RSSBytes: 1000 * 1024 * 1024, SampledAt: now.Add(-3 * time.Minute)},
		{RSSBytes: 1100 * 1024 * 1024, SampledAt: now.Add(-2 * time.Minute)},
		{RSSBytes: 1080 * 1024 * 1024, SampledAt: now.Add(-1 * time.Minute)},
		{RSSBytes: 1100 * 1024 * 1024, SampledAt: now},
	}
	history := summarizeManagedProcessHistory(rows)
	diagnostics := managedProcessDiagnostics(history, rows)
	codes := diagnosticCodes(diagnostics)
	assert.NotContains(t, codes, "rss_growth")
}

func diagnosticCodes(diagnostics []ManagedProcessDiagnostic) []string {
	out := make([]string, 0, len(diagnostics))
	for _, d := range diagnostics {
		out = append(out, d.Code)
	}
	return out
}
