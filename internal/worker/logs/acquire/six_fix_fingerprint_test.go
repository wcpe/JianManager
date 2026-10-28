package acquire

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func TestSixFixFingerprintDoesNotSampleReplacementAtOldWatermark(t *testing.T) {
	live := filepath.Join(t.TempDir(), "latest.log")
	rotated := live + ".1"
	old := strings.Repeat("old-old-old\n", 70)
	require.NoError(t, os.WriteFile(live, []byte(old), 0600))
	key, led := testKey("six-fingerprint-rotation-race", "g1"), ledger.New()
	wal := NewWAL(led, key)
	tailer := NewFileTailer(led, key, live, wal, nil)
	tailer.SetRotateTarget(rotated)
	events, err := tailer.Poll()
	require.NoError(t, err)
	require.NoError(t, wal.Append(events...))
	require.NoError(t, wal.Commit())
	// 确认线刚前进但尚未采样，物理替换先于下一次 Poll 发生。
	require.NoError(t, led.RecordDelivery(key, 0, uint64(len(old)), logtypes.DeliveryRequestDone))
	require.NoError(t, os.Rename(live, rotated))
	require.NoError(t, os.WriteFile(live, []byte(strings.Repeat("new-new-new\n", 140)), 0600))
	_, err = tailer.Poll()
	require.NoError(t, err)
	if fingerprint, ok := led.ConfirmedFingerprint(key); ok && fingerprint.SegmentStartPos == 0 {
		expected, err := prefixSHA256At(rotated, fingerprint.PhysicalOffset-fingerprint.WindowBytes, fingerprint.WindowBytes)
		require.NoError(t, err)
		require.Equal(t, expected, fingerprint.SHA256, "旧段绑定只能来自旧物理内容，不能先读新文件再给旧段贴凭据")
	}
}

func TestSixFixNonzeroSegmentRewriteHasExactLogicalGap(t *testing.T) {
	dir := t.TempDir()
	live, rotated := filepath.Join(dir, "latest.log"), filepath.Join(dir, "latest.log.1")
	old := strings.Repeat("old-old-old\n", 70)
	current := strings.Repeat("new-new-new\n", 140)
	require.NoError(t, os.WriteFile(live, []byte(old), 0600))
	key, led := testKey("six-logical-gap", "g1"), ledger.New()
	wal := NewWAL(led, key)
	tailer := NewFileTailer(led, key, live, wal, nil)
	tailer.SetRotateTarget(rotated)
	events, err := tailer.Poll()
	require.NoError(t, err)
	require.NoError(t, wal.Append(events...))
	require.NoError(t, wal.Commit())
	require.NoError(t, led.RecordDelivery(key, 0, uint64(len(old)), logtypes.DeliveryRequestDone))
	_, err = tailer.Poll()
	require.NoError(t, err)
	require.NoError(t, os.Rename(live, rotated))
	require.NoError(t, os.WriteFile(live, []byte(current), 0600))
	changed, err := tailer.PrepareRotation()
	require.NoError(t, err)
	require.True(t, changed)
	events, err = tailer.Poll()
	require.NoError(t, err)
	require.NoError(t, wal.Append(events...))
	require.NoError(t, wal.Commit())
	confirmed := uint64(len(old) + len(current))
	require.NoError(t, led.RecordDelivery(key, uint64(len(old)), confirmed, logtypes.DeliveryRequestDone))
	_, err = tailer.Poll()
	require.NoError(t, err)
	fp, ok := led.ConfirmedFingerprint(key)
	require.True(t, ok)
	require.Equal(t, confirmed, fp.LogicalPos)
	require.Equal(t, int64(len(current)), fp.PhysicalOffset)
	require.Equal(t, uint64(len(old)), fp.SegmentStartPos)

	led2 := ledger.New()
	require.NoError(t, led2.Restore(led.Snapshot()))
	wal2 := NewWAL(led2, key)
	require.NoError(t, wal2.seedEntries(wal.Snapshot()))
	restarted := NewFileTailer(led2, key, live, wal2, nil)
	// 保留身份前缀，真实改写确认尾窗；不能为修复旧段假阳性而关闭此保护。
	rewritten := []byte(current)
	rewritten[len(rewritten)-100] = 'X'
	tail := "untrusted-new-tail\n"
	rewritten = append(rewritten, []byte(tail)...)
	require.NoError(t, os.WriteFile(live, rewritten, 0600))
	events, err = restarted.Poll()
	require.NoError(t, err)
	require.Empty(t, events)
	entry := led2.Get(key)
	require.Len(t, entry.Gaps, 1)
	require.Equal(t, confirmed, entry.Gaps[0].StartPos)
	require.Equal(t, confirmed+uint64(len(tail)), entry.Gaps[0].EndPos)
	require.Equal(t, ledger.GapReasonFileRewritten, entry.Gaps[0].Reason)
	appendBytes(t, live, []byte("trusted-after-anchor\n"))
	events, err = restarted.Poll()
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, entry.Gaps[0].EndPos, events[0].Record.Start, "新事件不能覆盖刚登记的被跳过逻辑区间")
}

func TestSixFixTruncatedAnchorAndAppendedIDsSurviveJSONThreeStarts(t *testing.T) {
	key := testKey("six-json-truncate", "g1")
	tailer, lines := truncateFixture(t, key, true)
	prefix := strings.Join(lines[:5], "\n") + "\n"
	require.NoError(t, os.Truncate(tailer.path, int64(len(prefix))))
	_, err := tailer.PrepareRotation()
	require.NoError(t, err)
	_, err = tailer.Poll()
	require.NoError(t, err)
	base := tailer.LogicalPos()
	appendBytes(t, tailer.path, []byte("new-1\n"))
	events, err := tailer.Poll()
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, base, events[0].Record.Start)
	require.NoError(t, tailer.wal.Append(events...))
	require.NoError(t, tailer.wal.Commit())
	firstID := events[0].EventID
	for round := 2; round <= 3; round++ {
		encoded, err := json.Marshal(tailer.led.Snapshot())
		require.NoError(t, err)
		var entries []ledger.Entry
		require.NoError(t, json.Unmarshal(encoded, &entries))
		led := ledger.New()
		require.NoError(t, led.Restore(entries))
		encoded, err = json.Marshal(tailer.wal.Snapshot())
		require.NoError(t, err)
		var pending []WALEntry
		require.NoError(t, json.Unmarshal(encoded, &pending))
		wal := NewWAL(led, key)
		require.NoError(t, wal.seedEntries(pending))
		restarted := NewFileTailer(led, key, tailer.path, wal, nil)
		events, err = restarted.Poll()
		require.NoError(t, err)
		require.Empty(t, events)
		require.Equal(t, int64(len(prefix)+6), restarted.cursor)
		require.Equal(t, base+6, restarted.LogicalPos())
		require.Equal(t, firstID, wal.Snapshot()[len(wal.Snapshot())-1].Event.EventID)
		tailer = restarted
	}
}

func TestSixFixArchiveTailUsesReanchoredPhysicalMapping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.log.1")
	body := "surviving-prefix\nnew-tail\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0600))
	key, led := testKey("six-reanchor-archive", "g1"), ledger.New()
	imp := NewArchiveImporter(led, key)
	idBytes, idSHA, err := ContentIdentity(path)
	require.NoError(t, err)
	const base = uint64(1530)
	physicalStart := uint64(len("surviving-prefix\n"))
	require.NoError(t, led.AdvanceRead(key, base))
	require.NoError(t, led.AdvanceDurable(key, base))
	require.NoError(t, led.RegisterSegment(key, ledger.Segment{Path: path, Kind: ledger.SegmentRotated, StartPos: base, EndPos: base, PhysicalStart: physicalStart, PhysicalMappingKnown: true, IdentityBytes: idBytes, IdentitySHA256: idSHA}))
	result, err := imp.ImportText(path)
	require.NoError(t, err)
	require.Len(t, result.Events, 1)
	require.Equal(t, "new-tail", result.Events[0].Message)
	require.Equal(t, base, result.Events[0].Record.Start)
	require.Equal(t, base+uint64(len("new-tail\n")), result.Events[0].Record.End)
	require.NoError(t, imp.Close())
}
