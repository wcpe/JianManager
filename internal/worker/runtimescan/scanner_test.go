package runtimescan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// mkdir 建目录（含父级），失败即 fatal。
func mkdir(t *testing.T, path string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(path, 0o755))
	return path
}

// touch 建空文件（含父目录），失败即 fatal。
func touch(t *testing.T, path string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("stub"), 0o755))
	return path
}

// fakeJDKDetect 只认目录名含 "jdk-ok" 的目录为有效 JDK。
func fakeJDKDetect(dir string) (Candidate, bool) {
	if !strings.Contains(filepath.Base(dir), "jdk-ok") {
		return Candidate{}, false
	}
	return Candidate{Type: TypeJDK, Vendor: "Temurin", Version: "21.0.4+9", Major: 21, Arch: "x64", Path: dir}, true
}

// fakeNodeProbe 只认文件名含 node 的可执行为有效 Node.js。
func fakeNodeProbe(exe string) (Candidate, bool) {
	return Candidate{Type: TypeNodeJS, Vendor: "Node.js", Version: "22.17.0", Major: 22, Arch: "x64", Path: exe}, true
}

func TestScan_JDKCandidatesFromGlobs(t *testing.T) {
	tmp := t.TempDir()
	ok := mkdir(t, filepath.Join(tmp, "jvm", "jdk-ok"))
	mkdir(t, filepath.Join(tmp, "jvm", "not-a-jdk")) // 探测失败静默跳过

	s := &Scanner{
		jdkGlobs:  []string{filepath.Join(tmp, "jvm", "*")},
		detectJDK: fakeJDKDetect,
		probeNode: fakeNodeProbe,
	}
	got := s.Scan([]string{TypeJDK})
	require.Len(t, got, 1)
	require.Equal(t, TypeJDK, got[0].Type)
	require.Equal(t, "Temurin", got[0].Vendor)
	require.Equal(t, 21, got[0].Major)
	require.Equal(t, ok, got[0].Path)
	require.False(t, got[0].AlreadyRegistered)
}

func TestScan_NodeCandidatesFromGlobs(t *testing.T) {
	tmp := t.TempDir()
	exe := touch(t, filepath.Join(tmp, "usr", "local", "bin", "node"))
	touch(t, filepath.Join(tmp, "nvm", "v20.10.0", "node.exe"))

	s := &Scanner{
		nodeGlobs: []string{
			filepath.Join(tmp, "usr", "local", "bin", "node"),
			filepath.Join(tmp, "nvm", "v*", "node.exe"),
		},
		detectJDK: fakeJDKDetect,
		probeNode: fakeNodeProbe,
	}
	got := s.Scan([]string{TypeNodeJS})
	require.Len(t, got, 2)
	paths := make([]string, 0, 2)
	for _, c := range got {
		require.Equal(t, TypeNodeJS, c.Type)
		require.Equal(t, "Node.js", c.Vendor)
		paths = append(paths, c.Path)
	}
	require.Contains(t, paths, exe)
}

func TestScan_MissingPathsSilentlySkipped(t *testing.T) {
	tmp := t.TempDir()
	s := &Scanner{
		jdkGlobs:  []string{filepath.Join(tmp, "no-such-dir", "*")},
		nodeGlobs: []string{filepath.Join(tmp, "nothing", "node")},
		detectJDK: fakeJDKDetect,
		probeNode: fakeNodeProbe,
	}
	require.Empty(t, s.Scan(nil))
}

func TestScan_ProbeFailureSilentlySkipped(t *testing.T) {
	tmp := t.TempDir()
	touch(t, filepath.Join(tmp, "bin", "node"))
	s := &Scanner{
		nodeGlobs: []string{filepath.Join(tmp, "bin", "node")},
		detectJDK: fakeJDKDetect,
		probeNode: func(string) (Candidate, bool) { return Candidate{}, false },
	}
	require.Empty(t, s.Scan([]string{TypeNodeJS}))
}

func TestScan_ManagedRootMarksAlreadyRegistered(t *testing.T) {
	tmp := t.TempDir()
	managedRoot := filepath.Join(tmp, "opt", "jdks")
	inside := mkdir(t, filepath.Join(managedRoot, "jdk-ok"))
	outside := mkdir(t, filepath.Join(tmp, "external", "jdk-ok"))

	s := &Scanner{
		jdkGlobs:     []string{filepath.Join(managedRoot, "*"), filepath.Join(tmp, "external", "*")},
		managedRoots: []string{managedRoot},
		detectJDK:    fakeJDKDetect,
		probeNode:    fakeNodeProbe,
	}
	got := s.Scan([]string{TypeJDK})
	require.Len(t, got, 2)
	byPath := map[string]Candidate{}
	for _, c := range got {
		byPath[c.Path] = c
	}
	require.True(t, byPath[inside].AlreadyRegistered, "托管根下的候选应标 already_registered")
	require.False(t, byPath[outside].AlreadyRegistered)
}

func TestScan_TypeFilterAndUnknownTypeIgnored(t *testing.T) {
	tmp := t.TempDir()
	mkdir(t, filepath.Join(tmp, "jvm", "jdk-ok"))
	touch(t, filepath.Join(tmp, "bin", "node"))

	s := &Scanner{
		jdkGlobs:  []string{filepath.Join(tmp, "jvm", "*")},
		nodeGlobs: []string{filepath.Join(tmp, "bin", "node")},
		detectJDK: fakeJDKDetect,
		probeNode: fakeNodeProbe,
	}

	// 只要 nodejs：不出 jdk 候选。
	nodeOnly := s.Scan([]string{TypeNodeJS})
	require.Len(t, nodeOnly, 1)
	require.Equal(t, TypeNodeJS, nodeOnly[0].Type)

	// 未知类型静默忽略（CP 侧负责拒绝），已知类型照常。
	mixed := s.Scan([]string{"python", TypeJDK})
	require.Len(t, mixed, 1)
	require.Equal(t, TypeJDK, mixed[0].Type)

	// 空 = 全部支持类型。
	all := s.Scan(nil)
	require.Len(t, all, 2)
}

func TestScan_DedupeByPath(t *testing.T) {
	tmp := t.TempDir()
	mkdir(t, filepath.Join(tmp, "jvm", "jdk-ok"))
	s := &Scanner{
		// 两个 glob 命中同一目录：只出一条。
		jdkGlobs:  []string{filepath.Join(tmp, "jvm", "*"), filepath.Join(tmp, "jvm", "jdk-ok")},
		detectJDK: fakeJDKDetect,
		probeNode: fakeNodeProbe,
	}
	require.Len(t, s.Scan([]string{TypeJDK}), 1)
}

func TestParseNodeVersion(t *testing.T) {
	tests := []struct {
		in      string
		version string
		major   int
		ok      bool
	}{
		{"v22.17.0", "22.17.0", 22, true},
		{"v20.10.0\n", "20.10.0", 20, true},
		{"22.17.0", "22.17.0", 22, true},
		{"", "", 0, false},
		{"garbage", "", 0, false},
	}
	for _, tt := range tests {
		version, major, ok := parseNodeVersion(tt.in)
		require.Equal(t, tt.ok, ok, "input %q", tt.in)
		if tt.ok {
			require.Equal(t, tt.version, version, "input %q", tt.in)
			require.Equal(t, tt.major, major, "input %q", tt.in)
		}
	}
}
