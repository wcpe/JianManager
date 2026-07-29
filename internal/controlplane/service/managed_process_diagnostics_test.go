package service

import (
	"testing"
	"time"

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
