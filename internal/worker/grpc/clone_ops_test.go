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

	files, bytesCopied, skipped, err := copyDirFiltered(src, dst, nil, []string{"session.lock", "logs", "*.pid", "usercache.json"})
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

// cloneIncluded（FR-231）：include 空=全包含；非空按首段匹配（精确 / basename glob / 去 /** 后缀）。
func TestCloneIncluded(t *testing.T) {
	require.True(t, cloneIncluded("anything", nil), "include 空=全包含")
	inc := []string{"*.jar", "plugins", "server.properties", "*.yml"}
	require.True(t, cloneIncluded("paper.jar", inc))
	require.True(t, cloneIncluded("plugins", inc))
	require.True(t, cloneIncluded(filepath.Join("plugins", "Essentials.jar"), inc), "插件内文件随顶层 plugins 包含")
	require.True(t, cloneIncluded("server.properties", inc))
	require.True(t, cloneIncluded("bukkit.yml", inc))
	require.False(t, cloneIncluded(filepath.Join("world", "level.dat"), inc), "world 不在 include")
	require.False(t, cloneIncluded("eula.txt", inc))
	require.True(t, cloneIncluded(filepath.Join("plugins", "x"), []string{"plugins/**"}), "/** 后缀归一")
}

// 快速复制（FR-231）：include=核心+插件+根配置 → 只复制这些，world/logs 不复制。
func TestCopyDirFiltered_QuickInclude(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	writeCloneTmp(t, filepath.Join(src, "paper.jar"), "JAR")
	writeCloneTmp(t, filepath.Join(src, "server.properties"), "server-port=25565\n")
	writeCloneTmp(t, filepath.Join(src, "bukkit.yml"), "x")
	writeCloneTmp(t, filepath.Join(src, "plugins", "Essentials.jar"), "P")
	writeCloneTmp(t, filepath.Join(src, "world", "level.dat"), "W")
	writeCloneTmp(t, filepath.Join(src, "logs", "latest.log"), "L")

	include := []string{"*.jar", "plugins", "server.properties", "*.yml", "*.yaml", "*.properties"}
	_, _, _, err := copyDirFiltered(src, dst, include, []string{"session.lock", "*.pid", "logs", "cache"})
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(dst, "paper.jar"))
	require.FileExists(t, filepath.Join(dst, "server.properties"))
	require.FileExists(t, filepath.Join(dst, "bukkit.yml"))
	require.FileExists(t, filepath.Join(dst, "plugins", "Essentials.jar"))
	require.NoDirExists(t, filepath.Join(dst, "world"), "world 不在 include，不复制")
	require.NoDirExists(t, filepath.Join(dst, "logs"))
}

func writeCloneTmp(t *testing.T, p, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}
