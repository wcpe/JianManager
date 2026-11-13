package grpc

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeLines 写 n 行内容到 path，每行固定以 ip 前缀保证可区分。
func writeLines(t *testing.T, path string, n int) {
	t.Helper()
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("line-")
		b.WriteString(strings.Repeat("x", 40))
		b.WriteString("-tail")
		b.WriteByte('\n')
	}
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o644))
}

// TestReadFileTail_ReturnsOnlyLastBytesAlignedToLines 尾部窗口只返回末尾部分，且丢弃首个残行。
func TestReadFileTail_ReturnsOnlyLastBytesAlignedToLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	writeLines(t, path, 200) // 每行约 50 字节，总计约 10KB

	info, err := os.Stat(path)
	require.NoError(t, err)

	// 窗口 200 字节：远小于文件，必须走尾部路径。
	res, err := readFileTail(path, 200)
	require.NoError(t, err)

	require.True(t, res.Truncated, "从中间读起时必须标记 truncated")
	require.Equal(t, info.Size(), res.TotalSize, "total_size 必须是文件总字节数")
	require.LessOrEqual(t, int64(len(res.Content)), int64(200)+64, "内容不应超出窗口太多")

	// 关键：不得以半行开头。按行对齐后首行必须是完整的 line-xxx-tail。
	text := string(res.Content)
	require.True(t, strings.HasPrefix(text, "line-"), "首行必须是完整行，实际开头: %q", text[:min(20, len(text))])
	require.True(t, strings.HasSuffix(text, "\n"), "尾部行集应以换行结尾")
}

// TestReadFileTail_SmallFileReturnsWholeContentWithoutTruncation 文件小于窗口时返回完整内容且不标记截断。
func TestReadFileTail_SmallFileReturnsWholeContentWithoutTruncation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "small.log")
	writeLines(t, path, 3)

	full, err := os.ReadFile(path)
	require.NoError(t, err)

	res, err := readFileTail(path, 64*1024)
	require.NoError(t, err)
	require.False(t, res.Truncated, "文件小于窗口时不得谎报截断")
	require.Equal(t, full, res.Content)
	require.Equal(t, int64(len(full)), res.TotalSize)
}

// TestReadFileTail_ClampsToServerHardCap 调用方放大窗口不得越过服务端硬上限。
func TestReadFileTail_ClampsToServerHardCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.log")
	// 写 2MiB：大于服务端硬上限 512KiB。
	line := []byte(strings.Repeat("y", 99) + "\n")
	var b bytes.Buffer
	for b.Len() < 2*1024*1024 {
		b.Write(line)
	}
	require.NoError(t, os.WriteFile(path, b.Bytes(), 0o644))

	res, err := readFileTail(path, 64*1024*1024) // 故意传超大窗口
	require.NoError(t, err)
	require.LessOrEqual(t, int64(len(res.Content)), int64(readFileTailHardMaxBytes), "必须被服务端硬上限限住")
	require.Equal(t, int64(b.Len()), res.TotalSize)
}

// TestReadFileTail_LineAlignedOffsetKeepsFirstLineWhenOffsetStartsAtLineBoundary
// 当窗口起点正好位于行首时，不能无条件丢掉第一行。
func TestReadFileTail_LineAlignedOffsetKeepsFirstLineWhenOffsetStartsAtLineBoundary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lines.log")
	content := []byte("a\nb\nc\n")
	require.NoError(t, os.WriteFile(path, content, 0o644))

	// total=6, tail=4 -> offset=2，正好落在 b 的行首；应返回 b\nc\n。
	res, err := readFileTail(path, 4)
	require.NoError(t, err)
	require.True(t, res.Truncated)
	require.Equal(t, []byte("b\nc\n"), res.Content)
}

// shortReaderAt 模拟 Stat 后文件被 copytruncate：ReadAt 只能返回实际剩余字节并报 EOF。
type shortReaderAt struct {
	data []byte
}

func (r shortReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// TestReadFileTail_ShortReadDoesNotReturnNulPadding
// 并发截短时不能把预分配缓冲区中未读的 NUL 字节返回给调用方。
func TestReadFileTail_ShortReadDoesNotReturnNulPadding(t *testing.T) {
	res, err := readFileTailWindow(shortReaderAt{data: []byte("a\nc\n")}, 6, 4)
	require.NoError(t, err)
	require.True(t, res.Truncated)
	require.Equal(t, []byte("c\n"), res.Content)
	require.NotContains(t, string(res.Content), "\x00")
}

// TestReadFileTail_DirectoryRejected 目录不能作为普通文件读取。
func TestReadFileTail_DirectoryRejected(t *testing.T) {
	dir := t.TempDir()
	_, err := readFileTail(dir, 1024)
	require.Error(t, err)
}

// TestReadFileTail_NotAlignedWhenFileHasSingleLine 单行无换行时不能把内容全丢掉。
func TestReadFileTail_NotAlignedWhenFileHasSingleLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "oneline.log")
	require.NoError(t, os.WriteFile(path, []byte("no-newline-tail"), 0o644))

	res, err := readFileTail(path, 4)
	require.NoError(t, err)
	// 文件小于窗口，返回完整内容即可（此处重点是不能 panic、不能空内容）。
	require.NotEmpty(t, res.Content)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
