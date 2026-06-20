package grpc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestWriteBackupArchive_ExcludesRuntimeFiles 回归测试：备份打包必须排除运行态/锁定文件
// （session.lock、logs/、cache/、usercache.json、*.pid）。此前未排除，导致对运行中的实例
// 打包时因 world/session.lock 被独占锁而整次失败（真机复验 FR-056 发现）。
func TestWriteBackupArchive_ExcludesRuntimeFiles(t *testing.T) {
	work := t.TempDir()
	mk := func(rel, content string) {
		p := filepath.Join(work, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	mk("server.properties", "server-port=25565")
	mk("world/region/r.0.0.mca", "regiondata")
	mk("world/session.lock", "lock")
	mk("logs/latest.log", "log")
	mk("cache/x.bin", "cache")
	mk("usercache.json", "[]")
	mk("server.pid", "123")

	arch := filepath.Join(t.TempDir(), "b.tar.gz")
	manifest, packed, _, err := writeBackupArchive(arch, work, nil, false)
	require.NoError(t, err)

	got := map[string]bool{}
	for _, e := range manifest {
		got[e.Path] = true
	}
	require.True(t, got["server.properties"], "常规文件应入备份")
	require.True(t, got["world/region/r.0.0.mca"], "世界数据应入备份")
	require.False(t, got["world/session.lock"], "session.lock 必须排除")
	require.False(t, got["logs/latest.log"], "logs 必须排除")
	require.False(t, got["cache/x.bin"], "cache 必须排除")
	require.False(t, got["usercache.json"], "usercache.json 必须排除")
	require.False(t, got["server.pid"], "*.pid 必须排除")
	require.Equal(t, int64(2), packed, "仅 server.properties 与世界数据两个常规文件入包")
}
