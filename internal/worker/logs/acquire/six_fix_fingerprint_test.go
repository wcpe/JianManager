package acquire

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

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

	// P2.1：不再有「重启后按旧偏移续读」这条路，也就不再需要「已确认水位指纹」去拦它。
	// 本 fixture 真正要验的东西保留：**非零段基址下的原地重写必须报出精确的逻辑缺口**，
	// 否则歧义区会被静默跨过（那才是真丢数据）。
	tail := "untrusted-new-tail\n"
	rewritten := append([]byte(current), []byte(tail)...)
	// 保留身份前缀，只改确认尾窗 ⇒ 尺寸/身份/轮转判据全看不见，必须靠进程内尾窗指纹发现。
	rewritten[len(current)-100] = 'X'
	require.NoError(t, os.WriteFile(live, rewritten, 0600))

	events, err = tailer.Poll()
	require.NoError(t, err)
	require.Empty(t, events)
	entry := led.Get(key)
	require.Len(t, entry.Gaps, 1)
	require.Equal(t, uint64(len(old)+len(current)), entry.Gaps[0].StartPos)
	require.Equal(t, uint64(len(old)+len(current)+len(tail)), entry.Gaps[0].EndPos)
	require.Equal(t, ledger.GapReasonFileRewritten, entry.Gaps[0].Reason)
	appendBytes(t, live, []byte("trusted-after-anchor\n"))
	events, err = tailer.Poll()
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, entry.Gaps[0].EndPos, events[0].Record.Start, "新事件不能覆盖刚登记的被跳过逻辑区间")
}

// TestSixFixTruncatedAnchorKeepsAppendedIDsStable 验：纯截断重锚后，后续追加的事件 ID 稳定，
// 且不会把已存活前缀重新赋新身份。
//
// P2.1：原来这里跑三轮「快照 → 重启 → 续读」，那条路（跨进程按账本水位恢复 cursor）
// 已被撤销（索引库改 :memory: 后账本本来就不跨重启）。保留本用例真正要验的部分：
// **截断重锚后继续追写的字节，逻辑位置接着锚点、身份不复用旧 ID。**
func TestSixFixTruncatedAnchorKeepsAppendedIDsStable(t *testing.T) {
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

	// 继续追写：新事件必须接着锚点、且不得复用上一轮的 event id。
	appendBytes(t, tailer.path, []byte("new-2\n"))
	more, err := tailer.Poll()
	require.NoError(t, err)
	require.Len(t, more, 1)
	require.Equal(t, base+uint64(len("new-1\n")), more[0].Record.Start)
	require.NotEqual(t, firstID, more[0].EventID, "不同位置的字节不得复用同一 event id")
}
