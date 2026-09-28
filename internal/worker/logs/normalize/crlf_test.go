package normalize

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// FR-475 Windows 就绪：行终止符归一——CRLF 的行尾 '\r' 属于终止符而非正文。
//
// 背景：Windows 上 Java 经 log4j2 `%n` 写出 CRLF；采集侧按 '\n' 拆行、只剥 '\n'
// （FileTailer / 归档读取），故行尾会残留 '\r'。修复前它进入事件正文，并使同一逻辑
// 内容在 CRLF 与 LF 下产出不同的 canonical，直接破坏跨平台可复算性。
func TestFeedCRLFStripsLineTerminator(t *testing.T) {
	const line = "[12:00:00] [Server thread/INFO]: hello"

	lf := ProcessLines([]string{line}, testOpts("stdout"))
	crlf := ProcessLines([]string{line + "\r"}, testOpts("stdout"))

	require.Len(t, lf.Events, 1)
	require.Len(t, crlf.Events, 1)
	require.Equal(t, line, lf.Events[0].Message)
	require.Equal(t, line, crlf.Events[0].Message, "CRLF 的行终止符不得进入事件正文")
	require.Equal(t, lf.Events[0].CanonicalHash, crlf.Events[0].CanonicalHash,
		"同一逻辑内容必须产出同一 canonical，与平台行尾无关")
	require.Equal(t, lf.Events[0].Level, crlf.Events[0].Level)
	require.Equal(t, lf.Events[0].EventTimeUTC, crlf.Events[0].EventTimeUTC)
}

// 多行堆栈：CRLF 下每行尾的 '\r' 同样不得进入正文，归并结果与 LF 完全一致。
func TestFeedCRLFMultilineJoinsLikeLF(t *testing.T) {
	lfLines := []string{
		"[12:00:00] [Server thread/ERROR]: boom",
		"\tat com.example.Foo.bar(Foo.java:1)",
		"Caused by: java.lang.IllegalStateException: nested",
	}
	crlfLines := make([]string, len(lfLines))
	for i, l := range lfLines {
		crlfLines[i] = l + "\r"
	}

	lf := ProcessLines(lfLines, testOpts("stdout"))
	crlf := ProcessLines(crlfLines, testOpts("stdout"))

	require.Len(t, lf.Events, 1, "前置：该输入应归并为单事件")
	require.Len(t, crlf.Events, 1, "CRLF 不应改变多行归并")
	require.Equal(t, lf.Events[0].Message, crlf.Events[0].Message)
	require.Equal(t, lf.Events[0].CanonicalHash, crlf.Events[0].CanonicalHash)
	require.NotContains(t, crlf.Events[0].Message, "\r")
	require.Equal(t, strings.Count(lf.Events[0].Message, "\n"), strings.Count(crlf.Events[0].Message, "\n"),
		"堆栈内部换行符应与 LF 输入一致")
}

// 混行尾（同一文件内 LF 与 CRLF 并存）同样按终止符处理：既不多剥也不漏剥。
func TestFeedCRLFMixedLineEndings(t *testing.T) {
	lines := []string{
		"[12:00:00] [Server thread/INFO]: first",    // LF
		"[12:00:01] [Server thread/INFO]: second\r", // CRLF
	}

	res := ProcessLines(lines, testOpts("stdout"))

	require.Len(t, res.Events, 2)
	require.Equal(t, "[12:00:00] [Server thread/INFO]: first", res.Events[0].Message)
	require.Equal(t, "[12:00:01] [Server thread/INFO]: second", res.Events[1].Message)
	require.Equal(t, 2, res.Stats.LineCount, "行数口径不受行尾影响")
}
