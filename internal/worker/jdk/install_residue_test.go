package jdk

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstall_ResidueDirAutoCleared 安装失败/取消遗留的残骸目录（无 bin/java 完成标记）
// 不再堵死重装：安装前检测到残骸即自动清除并继续（FR-291）。
// 真机复现：卡死任务遗留 4KB 半截 temurin-21/，此后同版本重装永远撞「目标目录已存在」。
// 用不支持的 vendor 使流程在「解析下载源」步失败——只要错误不再是「目标目录已存在」
// 且残骸已被移除，即证明检查已越过并清理，无需真实下载。
func TestInstall_ResidueDirAutoCleared(t *testing.T) {
	root := t.TempDir()
	m := NewManager(filepath.Join(root, "jdks"), nil)
	residue := filepath.Join(root, "jdks", "temurin-21")
	if err := os.MkdirAll(residue, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := m.InstallWithProgress(context.Background(), "nosuchvendor", 21, "", "x64", residue, "", nil)
	if err == nil {
		t.Fatal("不支持的 vendor 应失败")
	}
	if strings.Contains(err.Error(), "目标目录已存在") {
		t.Fatalf("残骸目录不应再堵死安装，实际: %v", err)
	}
	if _, statErr := os.Stat(residue); !os.IsNotExist(statErr) {
		t.Fatalf("残骸目录应被自动清除，仍存在: %s", residue)
	}
}

// TestInstall_CompleteDirStillRejected 完好已装目录（有 bin/java 完成标记）仍拒绝覆盖，
// 语义不变（FR-291）。
func TestInstall_CompleteDirStillRejected(t *testing.T) {
	root := t.TempDir()
	m := NewManager(filepath.Join(root, "jdks"), nil)
	dir := filepath.Join(root, "jdks", "temurin-21")
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	javaBin := filepath.Join(dir, "bin", "java")
	if err := os.WriteFile(javaBin, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := m.InstallWithProgress(context.Background(), "temurin", 21, "", "x64", dir, "", nil)
	if err == nil || !strings.Contains(err.Error(), "目标目录已存在") {
		t.Fatalf("完好目录应仍拒绝覆盖，实际: %v", err)
	}
	if _, statErr := os.Stat(javaBin); statErr != nil {
		t.Fatalf("完好目录内容不得被动: %v", statErr)
	}
}

// TestInstall_NestedCompleteDirStillRejected 归档外层多包一层目录的完好安装
// （bin/java 在一级子目录下）同样识别为完好、拒绝覆盖（FR-291）。
func TestInstall_NestedCompleteDirStillRejected(t *testing.T) {
	root := t.TempDir()
	m := NewManager(filepath.Join(root, "jdks"), nil)
	dir := filepath.Join(root, "jdks", "temurin-21")
	nested := filepath.Join(dir, "jdk-21.0.11+10", "bin")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "java"), []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := m.InstallWithProgress(context.Background(), "temurin", 21, "", "x64", dir, "", nil)
	if err == nil || !strings.Contains(err.Error(), "目标目录已存在") {
		t.Fatalf("嵌套完好目录应仍拒绝覆盖，实际: %v", err)
	}
}
