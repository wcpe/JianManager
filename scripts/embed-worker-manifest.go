//go:build ignore

// 生成 CP 内嵌 Worker 资产清单（FR-278/ADR-062）：扫描嵌入目录内 worker-<os>-<arch>[.exe]
// 二进制，计算 sha256/size，写出 manifest.json。由 `make embed-worker` 在交叉编译后调用。
//
// 用法: go run ./scripts/embed-worker-manifest.go --dir internal/controlplane/embed/worker --version 0.14.0
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type entry struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type manifest struct {
	Version string  `json:"version"`
	Assets  []entry `json:"assets"`
}

var namePattern = regexp.MustCompile(`^worker-([a-z0-9]+)-([a-z0-9]+?)(\.exe)?$`)

func main() {
	dir := flag.String("dir", "", "嵌入目录（含 worker-<os>-<arch> 二进制）")
	version := flag.String("version", "", "Worker 构建版本（与 CP version.Version 同源）")
	flag.Parse()
	if *dir == "" || *version == "" {
		fmt.Fprintln(os.Stderr, "用法: --dir <嵌入目录> --version <版本>")
		os.Exit(2)
	}

	files, err := os.ReadDir(*dir)
	if err != nil {
		fatal("读嵌入目录失败: %v", err)
	}
	var assets []entry
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		m := namePattern.FindStringSubmatch(f.Name())
		if m == nil {
			continue
		}
		path := filepath.Join(*dir, f.Name())
		sum, size, err := fileSHA256(path)
		if err != nil {
			fatal("计算 %s sha256 失败: %v", f.Name(), err)
		}
		assets = append(assets, entry{OS: m[1], Arch: m[2], File: f.Name(), SHA256: sum, Size: size})
	}
	if len(assets) == 0 {
		fatal("嵌入目录 %s 未发现 worker-<os>-<arch> 二进制", *dir)
	}

	raw, err := json.MarshalIndent(manifest{Version: strings.TrimSpace(*version), Assets: assets}, "", "  ")
	if err != nil {
		fatal("序列化 manifest 失败: %v", err)
	}
	out := filepath.Join(*dir, "manifest.json")
	if err := os.WriteFile(out, append(raw, '\n'), 0o644); err != nil {
		fatal("写 %s 失败: %v", out, err)
	}
	fmt.Printf("[embed-worker] 写出 %s（version=%s, %d 平台）\n", out, *version, len(assets))
}

func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[embed-worker] "+format+"\n", args...)
	os.Exit(1)
}
