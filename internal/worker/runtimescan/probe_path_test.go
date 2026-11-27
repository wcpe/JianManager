package runtimescan

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNodeExecutableCandidates_DoesNotIncludeArbitraryFile(t *testing.T) {
	dir := t.TempDir()
	evil := filepath.Join(dir, "evil.sh")
	cands := nodeExecutableCandidates(evil)
	for _, c := range cands {
		require.NotEqual(t, evil, c, "非 node 基名路径不得作为直接执行候选")
	}

	nodeBin := filepath.Join(dir, "node")
	require.Equal(t, []string{nodeBin}, nodeExecutableCandidates(nodeBin))
}

func TestProbePathContext_RejectsRelativePath(t *testing.T) {
	called := false
	sc := &Scanner{
		probeNodeCtx: func(_ context.Context, exe string) (Candidate, bool) {
			called = true
			return Candidate{Type: TypeNodeJS, Path: exe}, true
		},
	}
	_, ok := sc.ProbePathContext(context.Background(), TypeNodeJS, "node")
	require.False(t, ok, "相对路径必须被拒绝，不得走 PATH 查找")
	require.False(t, called)
}

func TestProbePathContext_DoesNotExecuteArbitraryBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script test")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "executed")
	evil := filepath.Join(dir, "not-node")
	script := "#!/bin/sh\necho pwned > " + marker + "\necho v22.14.0\n"
	require.NoError(t, os.WriteFile(evil, []byte(script), 0o755))

	sc := New(nil)
	_, ok := sc.ProbePathContext(context.Background(), TypeNodeJS, evil)
	require.False(t, ok)
	_, statErr := os.Stat(marker)
	require.True(t, os.IsNotExist(statErr), "任意非 node 可执行文件绝不应被执行")
}

func TestProbePathContext_RespectsContextCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script test")
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	fakeNode := filepath.Join(binDir, "node")
	require.NoError(t, os.WriteFile(fakeNode, []byte("#!/bin/sh\nsleep 30\n"), 0o755))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	sc := New(nil)
	_, ok := sc.ProbePathContext(ctx, TypeNodeJS, dir)
	require.False(t, ok)
	require.Less(t, time.Since(start), 3*time.Second, "挂起的探测进程必须在 ctx 取消时及时中断")
}

func TestProbePathContext_ProbesDirectoryLayout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script test")
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	fakeNode := filepath.Join(binDir, "node")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo v22.14.0; else echo x64; fi\n"
	require.NoError(t, os.WriteFile(fakeNode, []byte(script), 0o755))

	sc := New(nil)
	cand, ok := sc.ProbePathContext(context.Background(), TypeNodeJS, dir)
	require.True(t, ok)
	require.Equal(t, TypeNodeJS, cand.Type)
	require.Equal(t, "22.14.0", cand.Version)
	require.Equal(t, 22, cand.Major)
	require.Equal(t, "x64", cand.Arch)
	require.Equal(t, fakeNode, cand.Path)
}
