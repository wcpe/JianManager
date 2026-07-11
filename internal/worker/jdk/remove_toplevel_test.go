package jdk

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRemove_NestedPathClearsTopLevelDir 删除走登记的嵌套探测路径时，应清理到
// 托管根下的顶层子目录，不留父壳空目录（FR-292）。
// 真机复现：NodeJDK.Path=…/temurin-11/jdk-11.0.31+11，删后 temurin-11/ 空壳仍在占位。
func TestRemove_NestedPathClearsTopLevelDir(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root, nil)
	nested := filepath.Join(root, "temurin-11", "jdk-11.0.31+11")
	if err := os.MkdirAll(filepath.Join(nested, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "bin", "java"), []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := m.Remove(nested); err != nil {
		t.Fatalf("删除嵌套路径应成功: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "temurin-11")); !os.IsNotExist(err) {
		t.Fatal("顶层目录 temurin-11 应被整体清除，不留父壳")
	}
}

// TestRemove_DirectChildUnchanged 直接指向顶层子目录的删除行为不变（FR-292 回归守护）。
func TestRemove_DirectChildUnchanged(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root, nil)
	dir := filepath.Join(root, "temurin-21")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(dir); err != nil {
		t.Fatalf("删除顶层子目录应成功: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("顶层子目录应被删除")
	}
}

// TestRemove_RootItselfRejected 托管根本身不得作为删除目标（FR-292：
// 原实现 rel="." 可穿过校验直达 RemoveAll(根)，会清空全部托管 JDK）。
func TestRemove_RootItselfRejected(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root, nil)
	keep := filepath.Join(root, "temurin-17")
	if err := os.MkdirAll(keep, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := m.Remove(root); err == nil {
		t.Fatal("删除托管根本身应被拒绝")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("托管根内容不得被动: %v", err)
	}
}
