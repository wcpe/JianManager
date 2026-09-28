package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// FR-475 Windows 就绪：CRLF 文件经完整 pipeline（FileTailer → NormalizeBoundary）
// 后，事件正文不得含行终止符，且 record 位置仍与文件**字节偏移**精确对齐。
//
// 位置断言是本次修复的关键不变量：行终止符只在归一化收口（normalize.FeedAt）剥除，
// 采集侧 absPos/recordEnd 仍按原始字节计算。若误在采集层裁掉 '\r' 而不修正偏移，
// 游标会与文件失步——此时下面的位置断言与追加断言会转红。
func TestPipeline_CRLFMatchesLFAndKeepsByteExactPositions(t *testing.T) {
	// 同一逻辑内容分别以 LF / CRLF 落盘。
	lf := "[12:00:01] [Server thread/INFO]: started\n" +
		"[12:00:02] [Server thread/ERROR]: boom\n" +
		"\tat com.example.Foo.bar(Foo.java:1)\n"
	crlf := strings.ReplaceAll(lf, "\n", "\r\n")

	drainFile := func(t *testing.T, content, name string) []logtypes.Event {
		t.Helper()
		path := filepath.Join(t.TempDir(), name)
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
		var got []logtypes.Event
		p := newFilePipeline(t, path, "src-crlf", "g1", okDelivery(&got))
		events, err := p.Drain()
		require.NoError(t, err)
		require.Len(t, got, len(events), "投递钩子应收到同一批事件")
		return events
	}

	lfEvents := drainFile(t, lf, "lf.log")
	crlfEvents := drainFile(t, crlf, "crlf.log")

	require.Len(t, lfEvents, 2, "前置：单行事件 + 多行堆栈事件")
	require.Len(t, crlfEvents, 2, "CRLF 不应改变事件切分")

	for i := range crlfEvents {
		require.NotContains(t, crlfEvents[i].Message, "\r", "行终止符不得进入正文")
		require.Equal(t, lfEvents[i].Message, crlfEvents[i].Message,
			"同一逻辑内容的正文应与平台行尾无关")
		require.Equal(t, lfEvents[i].CanonicalHash, crlfEvents[i].CanonicalHash,
			"同一逻辑内容的 canonical 应与平台行尾无关")
		require.Equal(t, lfEvents[i].Level, crlfEvents[i].Level)
	}

	// 位置按真实字节推进：CRLF 每行多 1 字节，末事件覆盖到文件尾。
	require.EqualValues(t, 0, crlfEvents[0].Record.Start)
	require.Equal(t, lfEvents[1].Record.Start+1, crlfEvents[1].Record.Start,
		"CRLF 第 2 事件起点应比 LF 多 1 字节（首行的额外 \\r）")
	require.Equal(t, uint64(len(lf)), lfEvents[1].Record.End)
	require.Equal(t, uint64(len(crlf)), crlfEvents[1].Record.End,
		"末事件应覆盖到文件尾（字节精确）")
}

// 追加断言：CRLF 文件在读取后再追加一行，新事件必须从「追加前的文件长度」开始，
// 既不重读已耐久前缀，也不因行尾多出的字节而错位。
func TestPipeline_CRLFAppendKeepsCursorAligned(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	first := "[12:00:01] [Server thread/INFO]: a\r\n" +
		"[12:00:02] [Server thread/INFO]: b\r\n"
	require.NoError(t, os.WriteFile(path, []byte(first), 0o644))

	var got []logtypes.Event
	p := newFilePipeline(t, path, "src-crlf-append", "g1", okDelivery(&got))

	events, err := p.Drain()
	require.NoError(t, err)
	require.Len(t, events, 2)

	before := uint64(len(first))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString("[12:00:03] [Server thread/INFO]: c\r\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	more, err := p.Drain()
	require.NoError(t, err)
	require.Len(t, more, 1, "追加一行应只产出 1 个新事件")
	require.Equal(t, before, more[0].Record.Start, "新事件必须从追加前的文件长度开始")
	require.NotContains(t, more[0].Message, "\r")
	require.Contains(t, more[0].Message, "c")
}
