package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestWriteEnvFile 物化自定义启动 env 到 <workDir>/.env（FR-344）：按 KEY=VALUE 行排序写入，含生成头注释。
func TestWriteEnvFile(t *testing.T) {
	dir := t.TempDir()
	writeEnvFile(WrapperConfig{WorkDir: dir, EnvVars: map[string]string{"FOO": "bar", "ABC": "123"}})

	data, err := os.ReadFile(filepath.Join(dir, ".env"))
	require.NoError(t, err)
	s := string(data)
	require.Contains(t, s, "ABC=123")
	require.Contains(t, s, "FOO=bar")
	require.True(t, strings.Index(s, "ABC=") < strings.Index(s, "FOO="), "键应按字典序排序")
	require.Contains(t, s, "# 由 JianManager 生成", "含生成头注释")
}

// TestWriteEnvFile_EmptyWorkDirNoop 空 WorkDir：不写、不 panic（best-effort）。
func TestWriteEnvFile_EmptyWorkDirNoop(t *testing.T) {
	writeEnvFile(WrapperConfig{EnvVars: map[string]string{"X": "y"}})
}

// TestWriteEnvFile_NoEnvVars 无自定义 env：仍写出仅含生成头的 .env（可见「无自定义 env」）。
func TestWriteEnvFile_NoEnvVars(t *testing.T) {
	dir := t.TempDir()
	writeEnvFile(WrapperConfig{WorkDir: dir})
	data, err := os.ReadFile(filepath.Join(dir, ".env"))
	require.NoError(t, err)
	require.Contains(t, string(data), "# 由 JianManager 生成")
}
