package acquire

// 纯截断重写（非原子截断：logrotate copytruncate / 应用自行 truncate）——红绿用例。
//
// 形态：**同一 inode ∧ 无 rename ∧ 无任何轮转目标 ∧ 尺寸回退到游标之前 ∧ 残留前缀与
// 已投递内容逐字节相同**（截断只剪掉了尾巴，或应用截断后又从头部写了同一批字节）。
//
// 缺陷（本次修复的）：该形态此前与「rename 轮转」共用一条分支 ⇒ detectRotationLocked：
//   - cursor 归零、segmentStart=logicalPos ⇒ 下一轮从**文件头**重读残留前缀；
//   - 物理内容照旧、逻辑位置却更高 ⇒ 同一批内容以**不同 event_id** 二次投递；
//   - 全链路零缺口 ⇒ 查询侧看不出重复、账本也认为一致；
//   - 还登记了一条指向**根本不存在的** `latest.log.1` 的轮转关联。
//
// 本用例守修复后的不变量：
//   ① 不返回轮转标记、不计 RotationCount（纯截断不是轮转，调用方不得走轮转交接）；
//   ② 残留前缀**绝不**重读重投（事件总数与逻辑位置都不重复累计）；
//   ③ 截断后的新内容照常续读；
//   ④ 有未确认落库的尾部 ⇒ 恰好一条可见缺口 FILE_REWRITTEN（绝不静默 ✗）；
//      全部已确认落库 ⇒ 不记缺口（无丢失却记缺口会把源永久阻塞，见 CHANGELOG 教训）。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// truncateFixture 造一个「已读满 30 行」的现场，返回 tailer 与 30 行文本。
func truncateFixture(t *testing.T, key ledger.SourceKey, commit bool) (*FileTailer, []string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	lines := make([]string, 0, 30)
	for i := 1; i <= 30; i++ {
		// 固定长度行长，使「保留前 5 行」的字节边界与行边界一致。
		lines = append(lines, fmt.Sprintf("[00:00:%02d] line-%02d padding padding padding padding", i, i))
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))

	led := ledger.New()
	wal := NewWAL(led, key)
	tailer := NewFileTailer(led, key, path, wal, NewLineHook())
	events, err := tailer.Poll()
	require.NoError(t, err)
	require.Len(t, events, 30, "首轮应读到 30 行")
	require.NoError(t, wal.Append(events...))
	if commit {
		// 已确认落库：Durable 追上已消费位置，截断不会毁掉任何「可能没落库」的内容。
		require.NoError(t, wal.Commit())
	}
	require.Zero(t, tailer.RotationCount())
	return tailer, lines
}

// TestPureTruncationDoesNotRedeliverConsumedPrefix 钉住缺陷本体：
// 同 inode、前缀相同、size < cursor 时，残留前缀不得以更高逻辑位置二次投递。
//
// 变异（实测）：把 isInPlaceTruncationLocked 的调用撤掉（回到 detectRotationLocked）⇒
// PrepareRotation 返回 true、RotationCount=1，且下一轮把前 5 行再投一次 ⇒ 本用例立刻转红。
func TestPureTruncationDoesNotRedeliverConsumedPrefix(t *testing.T) {
	key := testKey("src-truncate-redeliver", "g1")
	tailer, lines := truncateFixture(t, key, true)
	path := tailer.path

	delivered := tailer.EventsEmitted()
	posBefore := tailer.LogicalPos()
	require.Equal(t, 30, delivered)
	require.Greater(t, posBefore, uint64(0))

	// 纯截断：同 inode 原地截短到「前 5 行」（前缀与已投递内容逐字节相同）。
	// 刻意**不**设置 rotateTo：没有任何轮转目标能接管被剪掉的尾巴，这是纯截断的本质。
	keptPrefix := strings.Join(lines[:5], "\n") + "\n"
	require.NoError(t, os.Truncate(path, int64(len(keptPrefix))))

	rotated, err := tailer.PrepareRotation()
	require.NoError(t, err)
	require.False(t, rotated, "纯截断不是轮转：不得返回轮转标记（否则调用方会去找不存在的轮转段）")
	require.Zero(t, tailer.RotationCount(), "纯截断不得计入轮转")

	// ② 残留前缀绝不重投：第一轮必须 0 事件，事件总数不得变化。
	events, err := tailer.Poll()
	require.NoError(t, err)
	require.Empty(t, events, "残留前缀是已投递内容：绝不能从文件头重读、以新逻辑位置二次投递 ✗")
	require.Equal(t, delivered, tailer.EventsEmitted(), "同一批内容不得以不同 event_id 再投一次 ✗")

	// ③ 截断后的新内容照常续读，且逻辑位置继续向前（不回到旧区间）。
	appendBytes(t, path, []byte("[00:09:00] fresh-1\n[00:09:01] fresh-2\n"))
	events, err = tailer.Poll()
	require.NoError(t, err)
	require.Len(t, events, 2, "截断后追加的新行必须照常读出")
	for _, ev := range events {
		require.Contains(t, ev.Message, "fresh-")
		require.GreaterOrEqual(t, ev.Record.Start, posBefore, "新事件的逻辑位置必须单调向前（不得复用旧区间）")
	}
	require.Equal(t, delivered+2, tailer.EventsEmitted(), "全程事件数 = 原 30 + 新 2：零重复投递")

	// ④ 全部已确认落库 ⇒ 记缺口是假缺口（会把源永久阻塞），这里必须为零。
	require.Zero(t, ledgerGapCount(t, tailer), "无内容丢失的截断不得登记缺口（否则源被永久阻塞 ✗）")
}

// TestPureTruncationRecordsVisibleGapForUnconfirmedTail 钉住「绝不静默」：
// 截断发生时若已消费的尾部尚未确认落库（Durable < 已消费位置），那段字节既读不回来、
// 也无从证明已落库 ⇒ 必须留下**可见**缺口，且区间是真实损失区间 [Durable, 已消费位置)。
func TestPureTruncationRecordsVisibleGapForUnconfirmedTail(t *testing.T) {
	key := testKey("src-truncate-gap", "g1")
	// 刻意不 Commit：Durable 停在 0，已读的 30 行全部处于「可能没落库」状态。
	tailer, _ := truncateFixture(t, key, false)
	path := tailer.path

	consumed := tailer.LogicalPos()
	require.Greater(t, consumed, uint64(0))
	ent := led0(t, tailer)
	require.Equal(t, uint64(0), ent.Positions.Durable, "夹具前提：尾部尚未确认落库")

	// 截断到 0：全部残留内容消失（最严酷的纯截断形态）。
	require.NoError(t, os.Truncate(path, 0))

	rotated, err := tailer.PrepareRotation()
	require.NoError(t, err)
	require.False(t, rotated, "纯截断不是轮转")
	require.Zero(t, tailer.RotationCount())

	require.Equal(t, 1, ledgerGapCount(t, tailer), "未确认的尾部被截掉 ⇒ 必须留下恰好一条可见缺口 ✗")
	gap := led0(t, tailer).Gaps[0]
	require.Equal(t, ledger.GapReasonFileRewritten, gap.Reason, "缺口原因须属 FILE_REWRITTEN 族（跨过的内容不再回读）")
	require.Equal(t, ent.Positions.Durable, gap.StartPos, "缺口起点应是已确认水位")
	require.Equal(t, consumed, gap.EndPos, "缺口终点应是截断前的已消费位置")
	require.False(t, gap.Resolved, "缺口必须保持未解决，交给对账裁定")

	// 本轮不得再把任何内容当新事件投递。
	events, err := tailer.Poll()
	require.NoError(t, err)
	require.Empty(t, events)
	require.Equal(t, 1, ledgerGapCount(t, tailer), "同一段歧义区不得重复记缺口")
}

func ledgerGapCount(t *testing.T, tailer *FileTailer) int {
	t.Helper()
	return tailer.led.UnresolvedGapCount(tailer.key)
}

func led0(t *testing.T, tailer *FileTailer) *ledger.Entry {
	t.Helper()
	ent := tailer.led.Get(tailer.key)
	require.NotNil(t, ent)
	return ent
}
