package acquire

// 原地重写（保头吞尾）检测——红绿用例（2026-10-06 复跑 #4 的 IDENTITY_CONFLICT 现场）。
//
// 现场：实例自身日志栈会原地重写/裁剪 latest.log ✗（活体实验：追加后立即检出=1，50s 后文件内=0、
// 事件段=0，而 inode 不变、size 仍在增长）⇒ 字节偏移 ↔ 内容映射漂移 ⇒ 同一 event_id 指向不同内容
// ⇒ 投递侧 IDENTITY_CONFLICT ⇒ 投递/回收水位钉死（delivery_pos ≡ reclaim_pos）。
//
// 本用例守 tailer 侧的不变量：**同 inode ∧ 尺寸未倒退 ∧ 游标前 512B 窗口指纹变化** ⇒
//   ① 记一条**可见**缺口 FILE_REWRITTEN（绝不静默 ✗）
//   ② 跳过歧义区、锚到 EOF（**不清零 cursor 重读**：那会把已读前缀当新事件重灌 ✗）
//   ③ 从新锚续读，后续行不丢
//
// 变异（人工验证过）：撤掉「尾窗指纹比对」这一判据 ⇒ 漂移内容被当作新事件继续读出、
// 且**零缺口** ⇒ 本用例红；原样装回 ⇒ 绿。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

func TestInPlaceRewriteRecordsVisibleGapAndReanchors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	key := testKey("src-rewrite", "g1")

	// ① 写 30 行并全部读入（游标应停在 EOF，尾窗指纹就位）。
	lines := make([]string, 0, 30)
	for i := 1; i <= 30; i++ {
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
	require.NoError(t, wal.Commit())
	require.Zero(t, led.UnresolvedGapCount(key), "正常读入不得产生缺口")
	require.Zero(t, tailer.RotationCount(), "同 inode 原地重写不是轮转（不得计 Rotation）")

	fi, err := os.Stat(path)
	require.NoError(t, err)
	cursor := fi.Size()
	require.Equal(t, uint64(cursor), tailer.LogicalPos(), "游标应落在 EOF（= 逻辑位置）")

	// ② 保头 + 原地重写游标前 512B 窗口（同长度、不同内容 ⇒ 尺寸不倒退）。
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	require.NoError(t, err)
	payload := strings.Repeat("X", 512)
	_, err = f.WriteAt([]byte(payload), int64(cursor-512))
	require.NoError(t, err)
	require.NoError(t, f.Close())

	// 再追加 5 行：尺寸增长 ⇒ 重锚点 > 旧游标，缺口区间可观测。
	more := "[00:01:00] after-rewrite-1\n[00:01:01] after-rewrite-2\n[00:01:02] after-rewrite-3\n[00:01:03] after-rewrite-4\n[00:01:04] after-rewrite-5\n"
	appendBytes(t, path, []byte(more))

	// ③ 本轮必须：不读歧义区（0 事件）＋ 记 1 条可见缺口 ＋ 锚到当前 EOF。
	events, err = tailer.Poll()
	require.NoError(t, err)
	require.Empty(t, events, "歧义区必须跳过：本轮不得把漂移内容当新事件读出 ✗")
	require.Equal(t, 1, led.UnresolvedGapCount(key), "必须留下恰好 1 条可见缺口（FILE_REWRITTEN 族）✗")
	require.Equal(t, tailer.RotationCount(), 0, "重锚不得走轮转路径（不得发 Rotation）")

	// 注：LogicalPos 是"已消费内容"的逻辑累计（按设计**不跟随文件字节偏移** ✗），
	// 故此处不断言它等于 EOF；重锚的正确性由 ④「从新锚续读且不丢」与缺口计数共同守。

	// ④ 续读：再追加 3 行 ⇒ 应从新锚读到 3 行，且不新增缺口。
	appendBytes(t, path, []byte("[00:02:00] resume-1\n[00:02:01] resume-2\n[00:02:02] resume-3\n"))
	events, err = tailer.Poll()
	require.NoError(t, err)
	require.Len(t, events, 3, "重锚后必须正常续读（不丢后续行）")
	for _, ev := range events {
		require.Contains(t, ev.Message, "resume-", "续读的必须是新锚之后的行")
	}
	require.Equal(t, 1, led.UnresolvedGapCount(key), "续读不得新增缺口")
}

// appendBytes 以追加方式写文件（保持 inode 不变，模拟实例自身在同一文件上继续写）。
func appendBytes(t *testing.T, path string, payload []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	require.NoError(t, err)
	_, err = f.Write(payload)
	require.NoError(t, err)
	require.NoError(t, f.Close())
}
