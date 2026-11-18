//go:build ignore

// 按精确标识符改名：读一份「旧名 → 新名」映射，扫 .go 文件的标识符 token 逐个替换。
//
// 为什么不用正则 / sed：标识符可能出现在字符串、注释、复合词（pr61Worker 里的 pr61）、
// 或不同包里同名但语义无关的位置。go/scanner 切出来的 IDENT token 才是「代码在叫这个名字」，
// 替换只落在这些 token 上，注释与字符串一个字不动，也不需要处理正则转义。
//
// 用法：
//
//	go run ./scripts/rename-identifiers.go <map-file> <dir-or-file>...
//
// map-file 每行 `旧名 新名`（空白分隔，# 开头为注释）。
package main

import (
	"bufio"
	"fmt"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// repl 是一次 token 级替换。
type repl struct {
	start, end int
	text       string
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "用法: go run ./scripts/rename-identifiers.go <map-file> <dir-or-file>...")
		os.Exit(2)
	}
	mapping, err := loadMapping(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "读映射失败: %v\n", err)
		os.Exit(2)
	}

	var files []string
	for _, target := range os.Args[2:] {
		info, err := os.Stat(target)
		if err != nil {
			fmt.Fprintf(os.Stderr, "跳过 %s: %v\n", target, err)
			continue
		}
		if info.IsDir() {
			err = filepath.Walk(target, func(p string, fi os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if !fi.IsDir() && strings.HasSuffix(p, ".go") {
					files = append(files, p)
				}
				return nil
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "遍历 %s 失败: %v\n", target, err)
			}
			continue
		}
		files = append(files, target)
	}
	sort.Strings(files)

	totalFiles, totalRepl := 0, 0
	for _, path := range files {
		n, err := renameFile(path, mapping)
		if err != nil {
			fmt.Fprintf(os.Stderr, "处理 %s 失败: %v\n", path, err)
			continue
		}
		if n > 0 {
			totalFiles++
			totalRepl += n
			fmt.Printf("  %s  (%d 处)\n", path, n)
		}
	}
	fmt.Printf("完成：%d 个文件，%d 处替换\n", totalFiles, totalRepl)
}

// loadMapping 读「旧名 新名」映射表。
func loadMapping(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m := map[string]string{}
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) != 2 {
			return nil, fmt.Errorf("第 %d 行格式应为「旧名 新名」: %q", line, text)
		}
		m[fields[0]] = fields[1]
	}
	return m, sc.Err()
}

// renameFile 对单个 Go 文件做 token 级替换，返回替换次数。
func renameFile(path string, mapping map[string]string) (int, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	fset := token.NewFileSet()
	file := fset.AddFile(path, fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, src, func(_ token.Position, msg string) {}, 0)

	var repls []repl
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok != token.IDENT {
			continue
		}
		newName, ok := mapping[lit]
		if !ok {
			continue
		}
		offset := file.Offset(pos)
		repls = append(repls, repl{start: offset, end: offset + len(lit), text: newName})
	}
	if len(repls) == 0 {
		return 0, nil
	}

	// 从后往前改，避免偏移错位。
	out := make([]byte, len(src))
	copy(out, src)
	for i := len(repls) - 1; i >= 0; i-- {
		r := repls[i]
		out = append(out[:r.start], append([]byte(r.text), out[r.end:]...)...)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return 0, err
	}
	return len(repls), nil
}
