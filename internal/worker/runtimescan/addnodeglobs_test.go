package runtimescan

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAddNodeGlobs_ManagedRootDiscovered 托管安装根经 AddNodeGlobs 追加后，
// 一键安装布局（nodejs-<major>/bin/node）的 Node 能被扫描发现，且因位于
// 托管根下自动标 AlreadyRegistered（FR-299×FR-300 胶合）。
func TestAddNodeGlobs_ManagedRootDiscovered(t *testing.T) {
	root := t.TempDir()
	runtimesRoot := filepath.Join(root, "opt", "runtimes")
	nodeBin := filepath.Join(runtimesRoot, "nodejs-22", "bin", "node")
	if err := os.MkdirAll(filepath.Dir(nodeBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nodeBin, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}

	s := New([]string{runtimesRoot})
	s.nodeGlobs = nil // 隔离默认系统路径，只验追加的托管根 glob
	s.probeNode = func(exe string) (Candidate, bool) {
		return Candidate{Type: TypeNodeJS, Version: "22.17.0", Major: 22, Arch: "x64", Path: exe}, true
	}
	s.AddNodeGlobs(filepath.Join(runtimesRoot, "nodejs-*", "bin", "node"))

	out := s.Scan([]string{TypeNodeJS})
	if len(out) != 1 {
		t.Fatalf("托管根下的 Node 应被发现，got %d 候选", len(out))
	}
	if out[0].Path != nodeBin {
		t.Fatalf("候选路径应为托管 node，got %s", out[0].Path)
	}
	if !out[0].AlreadyRegistered {
		t.Fatal("托管根下候选应标 AlreadyRegistered")
	}
}
