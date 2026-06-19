package grpc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCloneExcluded(t *testing.T) {
	patterns := []string{"session.lock", "logs", "cache", "usercache.json", "*.pid", "libraries/.cache"}
	require.True(t, cloneExcluded("session.lock", patterns))
	require.True(t, cloneExcluded(filepath.Join("logs", "latest.log"), patterns))
	require.True(t, cloneExcluded("logs", patterns))
	require.True(t, cloneExcluded(filepath.Join("cache", "x"), patterns))
	require.True(t, cloneExcluded("usercache.json", patterns))
	require.True(t, cloneExcluded("server.pid", patterns))
	require.True(t, cloneExcluded(filepath.Join("libraries", ".cache", "y"), patterns))
	require.False(t, cloneExcluded("server.properties", patterns))
	require.False(t, cloneExcluded(filepath.Join("world", "level.dat"), patterns))
	require.False(t, cloneExcluded("paper.yml", patterns))
}

func TestCopyDirExcluding(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	writeCloneTmp(t, filepath.Join(src, "server.properties"), "server-port=25565\n")
	writeCloneTmp(t, filepath.Join(src, "session.lock"), "lock")
	writeCloneTmp(t, filepath.Join(src, "world", "level.dat"), "world")
	writeCloneTmp(t, filepath.Join(src, "logs", "latest.log"), "log")
	writeCloneTmp(t, filepath.Join(src, "server.pid"), "123")
	writeCloneTmp(t, filepath.Join(src, "usercache.json"), "[]")

	files, bytesCopied, skipped, err := copyDirExcluding(src, dst, []string{"session.lock", "logs", "*.pid", "usercache.json"})
	require.NoError(t, err)
	require.Greater(t, files, int64(0))
	require.Greater(t, bytesCopied, int64(0))

	// 复制保留的文件
	require.FileExists(t, filepath.Join(dst, "server.properties"))
	require.FileExists(t, filepath.Join(dst, "world", "level.dat"))
	// 排除的运行态文件
	require.NoFileExists(t, filepath.Join(dst, "session.lock"))
	require.NoDirExists(t, filepath.Join(dst, "logs"))
	require.NoFileExists(t, filepath.Join(dst, "server.pid"))
	require.NoFileExists(t, filepath.Join(dst, "usercache.json"))

	require.Contains(t, skipped, "logs")
	require.Contains(t, skipped, "session.lock")
}

func writeCloneTmp(t *testing.T, p, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}
