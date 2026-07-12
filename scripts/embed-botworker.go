//go:build ignore

// 打包 bot-worker dist 为 CP 内嵌归档（FR-308/ADR-070）：把 bot-worker/dist 打成
// 确定性 tar.gz（路径排序、固定 mtime/属主），写出 bot-worker.tar.gz + manifest.json
// （version/sha256/size）。由 `make embed-botworker` 调用；纯 Go 实现不依赖外部 tar。
//
// 用法: go run ./scripts/embed-botworker.go --src bot-worker/dist --out internal/controlplane/embed/botworker --version 0.16.0-dev
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type manifest struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

func main() {
	src := flag.String("src", "bot-worker/dist", "bot-worker 构建产物目录")
	pkgJSON := flag.String("pkgjson", "bot-worker/package.json", "随归档携带的 package.json（ESM 需 type:module 与 dist 同级）")
	out := flag.String("out", "internal/controlplane/embed/botworker", "嵌入目录")
	version := flag.String("version", "", "构建版本（与 CP version.Version 同源）")
	flag.Parse()
	if *version == "" {
		fatal("必须指定 --version")
	}
	if st, err := os.Stat(*src); err != nil || !st.IsDir() {
		fatal("源目录不可用: %s（先 cd bot-worker && npm run build）", *src)
	}

	var files []string
	err := filepath.Walk(*src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		fatal("扫描源目录失败: %v", err)
	}
	if len(files) == 0 {
		fatal("源目录为空: %s", *src)
	}
	sort.Strings(files)

	// package.json 以归档内名 "package.json" 附在 dist 平级：dist 为 ESM（type:module），
	// 解压后 .js 的模块语义靠它；顺带让 Worker 侧可读 name/version 取证。
	type extraFile struct{ archiveName, srcPath string }
	var extras []extraFile
	if *pkgJSON != "" {
		if _, err := os.Stat(*pkgJSON); err != nil {
			fatal("package.json 不可读: %s", *pkgJSON)
		}
		extras = append(extras, extraFile{"package.json", *pkgJSON})
	}

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatal("创建嵌入目录失败: %v", err)
	}
	archivePath := filepath.Join(*out, "bot-worker.tar.gz")
	f, err := os.Create(archivePath)
	if err != nil {
		fatal("创建归档失败: %v", err)
	}
	h := sha256.New()
	gz, _ := gzip.NewWriterLevel(io.MultiWriter(f, h), gzip.BestCompression)
	tw := tar.NewWriter(gz)
	// 固定 mtime/属主：同一 dist 内容多次打包产出逐字节一致，指纹稳定。
	fixed := time.Unix(0, 0)
	writeEntry := func(name string, data []byte) {
		hdr := &tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(data)),
			ModTime: fixed, Format: tar.FormatPAX,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			fatal("写 tar 头失败: %v", err)
		}
		if _, err := tw.Write(data); err != nil {
			fatal("写 tar 数据失败: %v", err)
		}
	}
	for _, e := range extras {
		data, err := os.ReadFile(e.srcPath)
		if err != nil {
			fatal("读文件失败 %s: %v", e.srcPath, err)
		}
		writeEntry(e.archiveName, data)
	}
	for _, p := range files {
		rel, err := filepath.Rel(*src, p)
		if err != nil {
			fatal("求相对路径失败: %v", err)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			fatal("读文件失败 %s: %v", p, err)
		}
		writeEntry(filepath.ToSlash(rel), data)
	}
	if err := tw.Close(); err != nil {
		fatal("收尾 tar 失败: %v", err)
	}
	if err := gz.Close(); err != nil {
		fatal("收尾 gzip 失败: %v", err)
	}
	if err := f.Close(); err != nil {
		fatal("关闭归档失败: %v", err)
	}
	st, err := os.Stat(archivePath)
	if err != nil {
		fatal("stat 归档失败: %v", err)
	}

	m := manifest{Version: *version, SHA256: hex.EncodeToString(h.Sum(nil)), Size: st.Size()}
	raw, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(*out, "manifest.json"), append(raw, '\n'), 0o644); err != nil {
		fatal("写 manifest 失败: %v", err)
	}
	fmt.Printf("embed-botworker: %d files → %s (%d bytes, sha256=%s…)\n",
		len(files), archivePath, m.Size, m.SHA256[:12])
}

func fatal(format string, args ...any) {
	fmt.Fprintln(os.Stderr, "embed-botworker: "+strings.TrimSpace(fmt.Sprintf(format, args...)))
	os.Exit(1)
}
