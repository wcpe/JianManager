package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHasPrefixFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "io", "izzel", "taboolib", "library.jar")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("jar"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !hasPrefixFile(root, "io/izzel/taboolib/") {
		t.Fatal("存在前缀目录中的文件时应返回 true")
	}
	if hasPrefixFile(root, "missing/") {
		t.Fatal("前缀目录不存在时应返回 false")
	}
}
