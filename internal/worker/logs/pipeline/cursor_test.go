package pipeline

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 本文件锤住 pipeline.Options.StartAtEOF / ResumeCursor 的**接线**（2026-10-30 裁定 D）。
//
// 为什么要单独钉接线：这两个字段在 New 里只是两个 if；写错位置（比如 Resume 在 Anchor 之前）
// 或写错方向，都会让「重启续读」变成「重读全部历史」——而那正是 P1 之后最贵的错。
// acquire 包自己的用例只能证明 AnchorAtEOF / ResumeFrom 各自正确，
// 证明不了**组装处把它们接对了**（顺序、互斥）。

func cursorOptions(t *testing.T, path string) Options {
	t.Helper()
	return Options{
		Mode:             ModeFilePrimary,
		LogSourceID:      "src-pipeline-cursor",
		SourceGeneration: "g1",
		Path:             path,
		Stream:           "stdout",
		Location:         time.UTC,
		BaseTime:         time.Date(2026, 10, 30, 12, 0, 1, 0, time.UTC),
		Delivery: FuncHook(func(events []logtypes.Event, replay bool) (DeliveryResult, error) {
			return DeliveryResult{HTTPStatus: 200}, nil
		}),
	}
}

// TestPipelineStartAtEOFDoesNotReadExistingHistory 无游标（首次部署/换绑定）⇒ 从文件末尾起读。
// 变异（把 Options 的 StartAtEOF 分支删掉）⇒ 本用例转红（会把历史全读出来）。
func TestPipelineStartAtEOFDoesNotReadExistingHistory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	history := "old-1\nold-2\nold-3\n"
	require.NoError(t, os.WriteFile(path, []byte(history), 0o644))

	opts := cursorOptions(t, path)
	opts.StartAtEOF = true
	p, err := New(opts)
	require.NoError(t, err)

	events, err := p.Poll()
	require.NoError(t, err)
	require.Empty(t, events, "StartAtEOF ⇒ 文件里已存在的历史不得被采集")

	c, ok := p.Cursor()
	require.True(t, ok, "FILE_PRIMARY 必须有文件游标")
	require.EqualValues(t, len(history), c.Cursor, "游标必须钉在文件末尾")
	require.Equal(t, path, c.Path)
}

// TestPipelineResumeCursorWinsOverStartAtEOF 两者同时给时，ResumeCursor 必须赢。
//
// 这正是生产路径的形状：Register 一律 `StartAtEOF: true`，同时按 `resumeCursorFor` 给游标。
// 如果顺序接反（先 Resume 再 Anchor），游标会被 EOF 覆盖 ⇒ 停机期间写入的字节全部丢掉，
// 而且没有任何报错——是本变更里最隐蔽的一种错。
func TestPipelineResumeCursorWinsOverStartAtEOF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	first := "line-1\nline-2\n"
	require.NoError(t, os.WriteFile(path, []byte(first), 0o644))

	// 第一进程：从文件头读完两行，拿到游标（模拟上一进程刷盘的游标表条目）。
	opts := cursorOptions(t, path)
	p1, err := New(opts)
	require.NoError(t, err)
	ev1, err := p1.Poll()
	require.NoError(t, err)
	require.Len(t, ev1, 2)
	saved, ok := p1.Cursor()
	require.True(t, ok)
	require.EqualValues(t, len(first), saved.Cursor)

	// 停机期间写入的字节：重启后必须**正好**读到它们，不多不少。
	second := "line-3\nline-4\n"
	af, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = af.WriteString(second)
	require.NoError(t, err)
	require.NoError(t, af.Close())

	// 第二进程：生产形状（StartAtEOF 与 ResumeCursor 同时给）。
	opts2 := cursorOptions(t, path)
	opts2.StartAtEOF = true
	opts2.ResumeCursor = &saved
	p2, err := New(opts2)
	require.NoError(t, err)

	ev2, err := p2.Poll()
	require.NoError(t, err)
	require.Len(t, ev2, 2, "只读停机期间新增的两行：既不得重读历史，也不得漏掉新内容")
	require.Equal(t, "line-3", ev2[0].Message)
	require.Equal(t, "line-4", ev2[1].Message)
	require.EqualValues(t, len(first), ev2[0].Record.Start, "新事件的逻辑位置接着游标，不与历史重叠")
}

// TestPipelineResumeCursorFallsBackToEOFWhenStale 不可用的游标（文件比它短）不得把整体搞坏：
// 退回 StartAtEOF 语义（这里文件已是新内容，仍从末尾起读）。
func TestPipelineResumeCursorFallsBackToEOFWhenStale(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	content := "only-line\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	stale := acquire.SourceCursor{Path: path, Cursor: int64(len(content)) + 100, LogicalPos: 100}
	opts := cursorOptions(t, path)
	opts.StartAtEOF = true // Anchor 先落 EOF，随后 ResumeFrom 因游标越界放弃 ⇒ 停在 EOF
	opts.ResumeCursor = &stale
	p, err := New(opts)
	require.NoError(t, err, "过期的游标不得让管道构造失败")

	events, err := p.Poll()
	require.NoError(t, err)
	require.Empty(t, events, "游标过期 ⇒ 退回 EOF 起点，不读已有内容")
}

// TestPipelineCursorWithoutTailerReturnsFalse STDIO_PRIMARY 且无 path 的源没有文件游标。
// ingest 的游标表靠这个 false 跳过它（否则会写出一条 path 为空的假游标）。
func TestPipelineCursorWithoutTailerReturnsFalse(t *testing.T) {
	p, err := New(Options{
		Mode:             ModeStdioPrimary,
		LogSourceID:      "src-pipeline-stdio",
		SourceGeneration: "g1",
		Stream:           "stdout",
		Reader:           nil,
	})
	require.Error(t, err, "STDIO_PRIMARY 无 path 又无 Reader ⇒ 构造必须失败")
	require.Nil(t, p)

	var nilPipe *Pipeline
	_, ok := nilPipe.Cursor()
	require.False(t, ok, "nil pipeline 不得 panic")
}
