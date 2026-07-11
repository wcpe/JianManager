package grpc

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// collectDownload 跑 streamFileDownload 并收集全部分片，返回拼接内容、首帧 totalSize 与帧数。
func collectDownload(t *testing.T, workDir, rel string) (content []byte, firstTotal int64, frames int) {
	t.Helper()
	var buf bytes.Buffer
	err := streamFileDownload(workDir, rel, func(chunk []byte, totalSize int64) error {
		if frames == 0 {
			firstTotal = totalSize
		} else {
			require.Zero(t, totalSize, "totalSize 只应在首帧携带")
		}
		frames++
		buf.Write(chunk)
		return nil
	})
	require.NoError(t, err)
	return buf.Bytes(), firstTotal, frames
}

// TestStreamFileDownload_LargeFileNotTruncated 超过编辑器护栏（10MiB）的文件必须完整逐字节返回。
// 缺陷背景：单文件下载曾复用 ReadFile 的 10MiB 上限，120MB 文件被静默截断为恰好 10485760 字节。
func TestStreamFileDownload_LargeFileNotTruncated(t *testing.T) {
	workDir := t.TempDir()
	const size = 10*1024*1024 + 3 // 恰好越过 10MiB 护栏
	want := make([]byte, size)
	for i := range want {
		want[i] = byte(i % 251)
	}
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "server.jar"), want, 0o644))

	got, firstTotal, frames := collectDownload(t, workDir, "server.jar")
	require.Equal(t, int64(size), firstTotal, "首帧应携带文件总大小")
	require.Equal(t, size, len(got), "内容被截断")
	require.True(t, bytes.Equal(want, got), "内容与源文件字节不一致")
	require.Greater(t, frames, 1, "大文件应分多帧发送")
}

// TestStreamFileDownload_EmptyFile 空文件也要发一帧（首帧 totalSize=0），CP 依赖首帧先行判错再写响应头。
func TestStreamFileDownload_EmptyFile(t *testing.T) {
	workDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "empty.txt"), nil, 0o644))

	got, firstTotal, frames := collectDownload(t, workDir, "empty.txt")
	require.Zero(t, firstTotal)
	require.Empty(t, got)
	require.Equal(t, 1, frames, "空文件应恰好发一帧")
}

// TestStreamFileDownload_SmallFile 单帧小文件内容原样返回。
func TestStreamFileDownload_SmallFile(t *testing.T) {
	workDir := t.TempDir()
	want := []byte("hello world")
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "hello.txt"), want, 0o644))

	got, firstTotal, frames := collectDownload(t, workDir, "hello.txt")
	require.Equal(t, int64(len(want)), firstTotal)
	require.Equal(t, want, got)
	require.Equal(t, 1, frames)
}

// TestStreamFileDownload_RejectsTraversal 越界路径被拒，且不发出任何分片。
func TestStreamFileDownload_RejectsTraversal(t *testing.T) {
	workDir := t.TempDir()
	sent := 0
	err := streamFileDownload(workDir, "../escape.txt", func([]byte, int64) error {
		sent++
		return nil
	})
	require.Error(t, err)
	require.Zero(t, sent)
}

// TestStreamFileDownload_RejectsDirectory 目录不可经单文件下载，应引导打包下载。
func TestStreamFileDownload_RejectsDirectory(t *testing.T) {
	workDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "plugins"), 0o755))
	err := streamFileDownload(workDir, "plugins", func([]byte, int64) error { return nil })
	require.Error(t, err)
	require.Contains(t, err.Error(), "目录")
}

// TestStreamFileDownload_MissingFile 文件不存在返回明确错误。
func TestStreamFileDownload_MissingFile(t *testing.T) {
	workDir := t.TempDir()
	err := streamFileDownload(workDir, "nope.jar", func([]byte, int64) error { return nil })
	require.Error(t, err)
}
