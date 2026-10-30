package acquire

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// TestResumeFromContinuesAtSavedCursor 锤住裁定 D 的核心：
// 有游标 ⇒ 从上次位置续读，**不重读已采集的字节**（重启不重复投递）。
func TestResumeFromContinuesAtSavedCursor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	history := "old-1\nold-2\nold-3\n"
	require.NoError(t, os.WriteFile(path, []byte(history), 0o644))

	key := testKey("src-resume-cursor", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)

	// 第一进程：从头读完全部历史。
	first := NewFileTailer(led, key, path, wal, NewLineHook())
	evs, err := first.Poll()
	require.NoError(t, err)
	require.Len(t, evs, 3)
	saved := first.Cursor()
	require.EqualValues(t, len(history), saved.Cursor)
	require.NotEmpty(t, saved.Path)

	// 文件又长了（进程停机期间写入的）：重启后应当把它们读出来。
	appended := "new-1\nnew-2\n"
	af, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = af.WriteString(appended)
	require.NoError(t, err)
	require.NoError(t, af.Close())

	// 第二进程：从游标续读。
	second := NewFileTailer(led, key, path, NewWAL(led, key), NewLineHook())
	require.NoError(t, second.ResumeFrom(saved))
	require.EqualValues(t, len(history), second.LogicalPos(), "逻辑位置必须接着上次")

	got, err := second.Poll()
	require.NoError(t, err)
	require.Len(t, got, 2, "只读到停机期间新增的两行，不重读历史")
	require.Equal(t, "new-1", got[0].Message)
	require.Equal(t, "new-2", got[1].Message)
	require.EqualValues(t, len(history), got[0].Record.Start, "新事件逻辑位置接着游标，不重叠")
}

// TestResumeFromRejectsInapplicableCursor 四种不可用游标都必须退回「从头读」，
// 而不是盲目跳到旧位置（那会丢掉文件里的真实内容）。
func TestResumeFromRejectsInapplicableCursor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	content := "aaaaaaaaaaaaaaaaaaaa\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	key := testKey("src-resume-reject", "g1")

	cases := []struct {
		name   string
		cursor SourceCursor
	}{
		{"路径变了", SourceCursor{Path: filepath.Join(dir, "other.log"), Cursor: 5, LogicalPos: 5}},
		{"inode 不匹配（已轮转）", SourceCursor{Path: path, DeviceID: 99999, Inode: 99999, Cursor: 5, LogicalPos: 5}},
		{"文件比游标短（被截断）", SourceCursor{Path: path, Cursor: int64(len(content)) + 100, LogicalPos: 100}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			led := ledger.New()
			tailer := NewFileTailer(led, key, path, NewWAL(led, key), NewLineHook())
			require.NoError(t, tailer.ResumeFrom(tc.cursor))
			require.Zero(t, tailer.cursor, "不可用的游标必须退回文件头")
			require.Zero(t, tailer.LogicalPos())
			evs, err := tailer.Poll()
			require.NoError(t, err)
			require.Len(t, evs, 1, "从头读才能拿到文件里的真实内容")
		})
	}
}
