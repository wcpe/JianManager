package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// runAPI 实现 `jm api <METHOD> <PATH>`（FR-517）。
//
// 与 gh api 同构：一条命令覆盖 CP 全部 HTTP 路由，不做子命令映射。
// 输出保持原始 JSON，便于 jq / 脚本消费。
func runAPI(cfg Config, args []string) error {
	if err := requireURL(cfg); err != nil {
		return err
	}
	if err := requireLogin(cfg); err != nil {
		return err
	}

	var method, path string
	var dataArg string
	query := url.Values{}
	hasQuery := false

	// 位置参数：METHOD PATH；也允许只给 PATH（默认 GET）。
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--data" || a == "-d":
			if i+1 >= len(args) {
				return fmt.Errorf("--data 需要参数（@文件 / - 表示 stdin）")
			}
			i++
			dataArg = args[i]
		case strings.HasPrefix(a, "--data="):
			dataArg = strings.TrimPrefix(a, "--data=")
		case a == "--query" || a == "-q":
			if i+1 >= len(args) {
				return fmt.Errorf("--query 需要 key=value")
			}
			i++
			if err := addQuery(query, args[i]); err != nil {
				return err
			}
			hasQuery = true
		case strings.HasPrefix(a, "--query="):
			if err := addQuery(query, strings.TrimPrefix(a, "--query=")); err != nil {
				return err
			}
			hasQuery = true
		default:
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("未知选项: %s", a)
			}
			positional = append(positional, a)
		}
	}

	switch len(positional) {
	case 1:
		method = http.MethodGet
		path = positional[0]
	case 2:
		method = strings.ToUpper(positional[0])
		path = positional[1]
	default:
		return fmt.Errorf("用法: jm api <METHOD> <PATH> [--data @file|-] [--query k=v]...\n" +
			"       jm api <PATH>            （默认 GET）")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if !validMethod(method) {
		return fmt.Errorf("不支持的 HTTP 方法: %s", method)
	}

	var body []byte
	if dataArg != "" {
		b, err := readDataArg(dataArg)
		if err != nil {
			return err
		}
		body = b
	}

	client := newClient(cfg)
	var q url.Values
	if hasQuery {
		q = query
	}
	respBody, err := client.doRequest(method, path, q, body)
	if err != nil {
		return err
	}

	// 无 body（204 等）时不输出多余换行。
	if len(respBody) == 0 {
		return nil
	}
	// 原始输出 + 保证以换行结尾，便于 shell 管道使用。
	out := strings.TrimRight(string(respBody), "\n")
	fmt.Fprintln(os.Stdout, out)
	return nil
}

// addQuery 解析 key=value 并累加（允许同名多值）。
func addQuery(q url.Values, pair string) error {
	idx := strings.Index(pair, "=")
	if idx <= 0 {
		return fmt.Errorf("--query 需要 key=value 形式，收到 %q", pair)
	}
	q.Add(pair[:idx], pair[idx+1:])
	return nil
}

// readDataArg 读取 --data 的值：- 表示 stdin，@file 表示文件，否则当作字面量。
func readDataArg(arg string) ([]byte, error) {
	switch {
	case arg == "-":
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("读取 stdin 失败: %w", err)
		}
		return b, nil
	case strings.HasPrefix(arg, "@"):
		path := strings.TrimPrefix(arg, "@")
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读取 %s 失败: %w", path, err)
		}
		return b, nil
	default:
		return []byte(arg), nil
	}
}

// validMethod 白名单校验 HTTP 方法，避免拼出奇怪请求。
func validMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// printJSON 以缩进 JSON 输出（text 模式下的可读形式）。
func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化输出失败: %w", err)
	}
	fmt.Fprintln(os.Stdout, string(b))
	return nil
}
