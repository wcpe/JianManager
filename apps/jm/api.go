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

// runAPI 实现 `jm api <METHOD> <PATH>`。
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
	// 完整 URL 直接使用（gh api 也支持）：这是绕开 shell 路径改写最彻底的方式，
	// 因为它本就不以 `/` 开头，不会被当成文件系统路径。
	var absURL string
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		absURL = path
		path = ""
	}
	if path != "" {
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		// 折叠开头的重复斜杠：Git Bash 下用户常写 `//api/v1/x` 来绕开路径改写，
		// 而 `baseURL + "//api/..."` 会得到 `host//api/...`，路由据此匹配失败。
		// 与 gh 一致地把它当作单个前导斜杠。
		for strings.HasPrefix(path, "//") {
			path = path[1:]
		}
	}
	if !validMethod(method) {
		return fmt.Errorf("不支持的 HTTP 方法: %s", method)
	}

	// 拦截「shell 把 URL 路径当成文件系统路径改写」的情形。
	//
	// 最典型的是 Git Bash / MSYS2：它会把 `/api/v1/user/tokens` 改写成
	// `C:/Program Files/Git/api/v1/user/tokens`，于是请求打到 SPA 兜底路由，
	// 拿回一坨 HTML —— 用户看到的是「接口没数据」，完全联想不到是 shell 干的。
	// 与 `gh api` 一致的处理：明确点出原因，并给出「省略前导斜杠」这条解法。
	if looksLikeRewrittenPath(path) {
		return fmt.Errorf("请求路径被 shell 改写成了 %q\n"+
			"这通常是 Git Bash / MSYS2 把 URL 路径当文件路径转换所致。\n"+
			"改为省略前导斜杠即可：%s api %s %s\n"+
			"（也可用双斜杠或完整 URL）", path, progName(), method, strings.TrimPrefix(rewrittenSuffix(path), "/"))
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
	var respBody []byte
	var err error
	if absURL != "" {
		respBody, err = client.doRequestAbs(method, absURL, q, body)
	} else {
		respBody, err = client.doRequest(method, path, q, body)
	}
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

// looksLikeRewrittenPath 判定路径是否被 shell 改写成了文件系统路径。
//
// 只认得形态明显的两种，避免误伤正常路由：
//   - `C:/Program Files/Git/api/...`：Git Bash 把 `/api/...` 拼到其安装根
//   - `/c/Program Files/...`：少数 shell 的另一种盘符写法
//
// 判定依据是「路径里出现了盘符或 Program Files 这类明显非 API 片段的目录名」。
func looksLikeRewrittenPath(p string) bool {
	low := strings.ToLower(p)
	// Windows 盘符形态：字母 + ":"
	if len(p) >= 2 && p[1] == ':' && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')) {
		return true
	}
	if strings.Contains(low, "/program files") || strings.Contains(low, "\\program files") {
		return true
	}
	// 形如 /c/Program Files/... 的盘符挂载写法
	if len(p) > 3 && p[0] == '/' && p[2] == '/' && strings.Contains(low, "/git/") {
		return true
	}
	return false
}

// rewrittenSuffix 从被改写的路径里取出原本的 API 路径部分。
//
// 找不到可识别的标记时原样返回，让错误信息至少还带着用户输入过的内容。
func rewrittenSuffix(p string) string {
	for _, marker := range []string{"/api/", "/oauth/"} {
		if i := strings.Index(p, marker); i >= 0 {
			return p[i:]
		}
	}
	return p
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
