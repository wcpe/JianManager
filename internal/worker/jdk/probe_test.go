package jdk

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// normalizeJDKHome（FR-228）：java 可执行文件 → 上两级 home；目录 → 原样；空白裁剪。
func TestNormalizeJDKHome(t *testing.T) {
	exe := filepath.Join("opt", "jdk-21", "bin", "java")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	home := filepath.Clean(filepath.Join("opt", "jdk-21"))
	require.Equal(t, home, normalizeJDKHome(exe), "bin/java 应归一为上两级 home")
	require.Equal(t, home, normalizeJDKHome(filepath.Join("opt", "jdk-21")), "目录应原样")
	require.Equal(t, home, normalizeJDKHome("  "+filepath.Join("opt", "jdk-21")+"  "), "应裁剪空白")
}

// Probe 非 JDK 目录 / 空路径 → 报错（无 bin/java 或无法取版本）。有效 JDK 探测需真 java，由真机验收覆盖。
func TestManager_Probe_NotJDK(t *testing.T) {
	m := &Manager{}
	_, err := m.Probe(t.TempDir()) // 空目录，无 bin/java
	require.Error(t, err)
	_, err = m.Probe("   ")
	require.Error(t, err)
}
