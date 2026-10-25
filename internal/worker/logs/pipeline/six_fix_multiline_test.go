package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/normalize"
)

func TestSixFixProductionMultilineDefaultsBoundContinuousInput(t *testing.T) {
	for _, tc := range []struct {
		name    string
		lines   int
		payload string
	}{
		{"字节界", 100, "\tat example.Frame." + strings.Repeat("x", 128<<10)},
		{"行数界", 1200, "\tat example.Frame.next()"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := []string{"[12:00:00] [Server thread/ERROR]: exception"}
			for n := 0; n < tc.lines; n++ {
				lines = append(lines, tc.payload)
			}
			body := strings.Join(lines, "\n") + "\n"
			path := filepath.Join(t.TempDir(), "latest.log")
			require.NoError(t, os.WriteFile(path, []byte(body), 0600))
			p, err := New(Options{LogSourceID: "six-multiline-default", SourceGeneration: "g1", Path: path, Charset: "utf-8"})
			require.NoError(t, err)
			var all []logtypes.Event
			for round := 0; round < 20 && p.FileTailer().LogicalPos() < uint64(len(body)); round++ {
				events, err := p.Poll()
				require.NoError(t, err)
				all = append(all, events...)
				pendingLines := p.bound.n.Result().Stats.PendingLines
				if pendingLines > 0 {
					pendingBytes := p.FileTailer().LogicalPos() - p.bound.PendingStart()
					t.Logf("round=%d pendingBytes=%d pendingLines=%d emitted=%d", round, pendingBytes, pendingLines, len(all))
					require.LessOrEqual(t, pendingBytes, uint64(1<<20), "持续合法续行不能只依赖闲置超时限制缓存")
					require.LessOrEqual(t, pendingLines, 512)
				}
			}
			last, err := p.Flush()
			require.NoError(t, err)
			all = append(all, last...)
			require.Greater(t, len(all), 1)
			var messages []string
			position := uint64(0)
			truncated := 0
			for _, event := range all {
				require.Equal(t, position, event.Record.Start, "截断输出必须完整承接精确相邻区间")
				require.Greater(t, event.Record.End, event.Record.Start)
				position = event.Record.End
				require.LessOrEqual(t, len(event.Message), 1<<20)
				status := event.Fields[normalize.FieldParseStatus]
				require.Contains(t, []string{string(normalize.StatusTruncated), string(normalize.StatusPartial)}, status)
				if status == string(normalize.StatusTruncated) {
					truncated++
				}
				messages = append(messages, event.Message)
				want := logtypes.BuildEvent(event.Source, event.Record, event.EventTimeUTC, event.IngestTimeUTC, event.Level, event.Stream, event.Message)
				require.Equal(t, want.EventID, event.EventID, "只用正式源区间推导身份，不发明截断ID")
			}
			require.Positive(t, truncated)
			require.Equal(t, uint64(len(body)), position)
			require.Equal(t, body, strings.Join(messages, "\n")+"\n", "默认有限缓存不得丢弃任何原文行")
		})
	}
}

func TestSixFixProductionDefaultsPreserveExplicitNormalizerLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "latest.log")
	require.NoError(t, os.WriteFile(path, []byte(""), 0600))
	p, err := New(Options{LogSourceID: "six-explicit-limits", SourceGeneration: "g1", Path: path, Limits: normalize.Limits{MaxBytes: 12345, MaxLines: 17}})
	require.NoError(t, err)
	require.Equal(t, 12345, p.normalizeOpts.Limits.MaxBytes)
	require.Equal(t, 17, p.normalizeOpts.Limits.MaxLines)
	// 独立 normalizer 仍保留零值不限制的库契约；只在生产组合根应用有限默认。
	n := normalize.New(normalize.Options{})
	for i := 0; i < 600; i++ {
		require.Empty(t, n.Feed("\tat example.Frame.next()"))
	}
	require.Equal(t, 600, n.Result().Stats.PendingLines)
}
