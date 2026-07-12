package jdk

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// symlinkSupported 报告当前环境能否创建符号链接（Windows 非特权/非开发者模式会失败）。
func symlinkSupported(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "t"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return os.Symlink("t", filepath.Join(dir, "l")) == nil
}

// writeTarGz 生成含 目录/普通文件/符号链接 的 tar.gz（模拟 Node 官方归档布局：
// bin/corepack -> ../lib/node_modules/corepack/dist/corepack.js）。
func writeTarGz(t *testing.T, path string, entries []tar.Header, contents map[string][]byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	for i := range entries {
		h := entries[i]
		body := contents[h.Name]
		h.Size = int64(len(body))
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if len(body) > 0 {
			if _, err := tw.Write(body); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestUntarGz_PreservesSymlinks Node 官方 tar.gz 的 bin/npm、bin/corepack 是相对符号链接，
// 解压必须保留（FR-299 缺陷真机复现：symlink 条目被静默丢弃，bin/ 只剩 node，
// corepack/npm 不可用，FR-306 包管理器无从激活）。
func TestUntarGz_PreservesSymlinks(t *testing.T) {
	if !symlinkSupported(t) {
		t.Skip("环境不支持创建符号链接（Windows 非开发者模式），跳过")
	}
	dir := t.TempDir()
	archive := filepath.Join(dir, "node.tar.gz")
	writeTarGz(t, archive, []tar.Header{
		{Name: "top/bin/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "top/lib/node_modules/corepack/dist/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "top/lib/node_modules/corepack/dist/corepack.js", Typeflag: tar.TypeReg, Mode: 0o755},
		{Name: "top/bin/node", Typeflag: tar.TypeReg, Mode: 0o755},
		{Name: "top/bin/corepack", Typeflag: tar.TypeSymlink, Linkname: "../lib/node_modules/corepack/dist/corepack.js", Mode: 0o755},
	}, map[string][]byte{
		"top/lib/node_modules/corepack/dist/corepack.js": []byte("#!/usr/bin/env node\n"),
		"top/bin/node": []byte("#node"),
	})

	dest := filepath.Join(dir, "out")
	if err := untarGz(archive, dest); err != nil {
		t.Fatalf("解压失败: %v", err)
	}
	link := filepath.Join(dest, "top", "bin", "corepack")
	st, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("bin/corepack 符号链接丢失（FR-299 缺陷）: %v", err)
	}
	if st.Mode()&os.ModeSymlink == 0 {
		t.Fatal("bin/corepack 应为符号链接")
	}
	// 链接可解析到真实文件
	if _, err := os.Stat(link); err != nil {
		t.Fatalf("corepack 链接目标不可达: %v", err)
	}
}

// TestUntarGz_RejectsEscapingSymlink 符号链接目标逃出解压根必须拒绝（防 symlink 逃逸攻击）。
// 拒绝发生在创建链接之前，无需环境支持 symlink（Windows 也可跑）。
func TestUntarGz_RejectsEscapingSymlink(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "evil.tar.gz")
	writeTarGz(t, archive, []tar.Header{
		{Name: "top/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "top/evil", Typeflag: tar.TypeSymlink, Linkname: "../../../../etc/passwd", Mode: 0o755},
	}, nil)

	dest := filepath.Join(dir, "out")
	if err := untarGz(archive, dest); err == nil {
		t.Fatal("逃逸 symlink 应被拒绝")
	}
}
