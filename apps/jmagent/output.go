package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// writeOutput 按 cfg.Output 写出 body：json 原样美化；text 走 formatText。
// formatText 收到已解析的任意 JSON 值（map/slice/primitive）。
func writeOutput(cfg Config, body []byte, formatText func(w io.Writer, v any) error) error {
	if cfg.Output == "json" {
		return writeJSON(os.Stdout, body)
	}
	var v any
	if len(body) == 0 || string(body) == "null" {
		v = nil
	} else if err := json.Unmarshal(body, &v); err != nil {
		// 非 JSON 时 text 模式直接原文输出
		_, err2 := os.Stdout.Write(body)
		if err2 != nil {
			return err2
		}
		if !strings.HasSuffix(string(body), "\n") {
			fmt.Fprintln(os.Stdout)
		}
		return nil
	}
	if formatText == nil {
		return writePrettyAny(os.Stdout, v)
	}
	return formatText(os.Stdout, v)
}

// writeJSON 将 body 美化为缩进 JSON 写到 w。
func writeJSON(w io.Writer, body []byte) error {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		// 无法解析则原样输出
		_, err2 := w.Write(body)
		if err2 != nil {
			return err2
		}
		if len(body) > 0 && body[len(body)-1] != '\n' {
			_, err2 = io.WriteString(w, "\n")
		}
		return err2
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// writePrettyAny 通用 text 回退：键值对或列表的简易打印。
func writePrettyAny(w io.Writer, v any) error {
	switch t := v.(type) {
	case nil:
		fmt.Fprintln(w, "(空)")
	case map[string]any:
		// 稳定顺序：优先常见键
		keys := preferredKeys(t)
		for _, k := range keys {
			fmt.Fprintf(w, "%s: %v\n", k, formatScalar(t[k]))
		}
	case []any:
		if len(t) == 0 {
			fmt.Fprintln(w, "(无数据)")
			return nil
		}
		for i, item := range t {
			fmt.Fprintf(w, "--- [%d] ---\n", i+1)
			if m, ok := item.(map[string]any); ok {
				for _, k := range preferredKeys(m) {
					fmt.Fprintf(w, "  %s: %v\n", k, formatScalar(m[k]))
				}
			} else {
				fmt.Fprintf(w, "  %v\n", item)
			}
		}
	default:
		fmt.Fprintf(w, "%v\n", t)
	}
	return nil
}

// preferredKeys 返回 map 的键：优先 id/name/status 等，其余按字典序。
func preferredKeys(m map[string]any) []string {
	priority := []string{
		"id", "uuid", "name", "kind", "tokenId", "status", "nodeId",
		"host", "os", "arch", "maintenance", "role", "type", "processType",
		"scopedInstanceIds", "scopedNodeIds", "writeAllowlist", "ok",
	}
	seen := map[string]bool{}
	var out []string
	for _, k := range priority {
		if _, ok := m[k]; ok {
			out = append(out, k)
			seen[k] = true
		}
	}
	// 其余键字典序
	rest := make([]string, 0, len(m))
	for k := range m {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// formatScalar 把 JSON 值格式成单行可读文本。
func formatScalar(v any) string {
	switch t := v.(type) {
	case nil:
		return "-"
	case []any:
		parts := make([]string, len(t))
		for i, x := range t {
			parts[i] = fmt.Sprint(x)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		b, _ := json.Marshal(t)
		return string(b)
	case float64:
		// JSON 数字默认 float64；整数则去小数
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%v", t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(t)
	}
}

// formatWhoami text 输出 whoami。
func formatWhoami(w io.Writer, v any) error {
	m, ok := v.(map[string]any)
	if !ok {
		return writePrettyAny(w, v)
	}
	fmt.Fprintf(w, "kind:              %v\n", formatScalar(m["kind"]))
	fmt.Fprintf(w, "name:              %v\n", formatScalar(m["name"]))
	fmt.Fprintf(w, "tokenId:           %v\n", formatScalar(m["tokenId"]))
	fmt.Fprintf(w, "scopedInstanceIds: %v\n", formatScalar(m["scopedInstanceIds"]))
	fmt.Fprintf(w, "scopedNodeIds:     %v\n", formatScalar(m["scopedNodeIds"]))
	fmt.Fprintf(w, "writeAllowlist:    %v\n", formatScalar(m["writeAllowlist"]))
	return nil
}

// formatNodeList text 输出节点列表。
func formatNodeList(w io.Writer, v any) error {
	list, ok := v.([]any)
	if !ok {
		// 可能是 null
		if v == nil {
			fmt.Fprintln(w, "(无节点)")
			return nil
		}
		return writePrettyAny(w, v)
	}
	if len(list) == 0 {
		fmt.Fprintln(w, "(无节点)")
		return nil
	}
	fmt.Fprintf(w, "%-6s  %-20s  %-10s  %-8s  %s\n", "ID", "NAME", "STATUS", "MAINT", "HOST")
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		status := nodeStatusText(m["status"])
		maint := "否"
		if b, ok := m["maintenance"].(bool); ok && b {
			maint = "是"
		}
		fmt.Fprintf(w, "%-6s  %-20s  %-10s  %-8s  %s\n",
			formatScalar(m["id"]),
			truncate(formatScalar(m["name"]), 20),
			status,
			maint,
			formatScalar(m["host"]),
		)
	}
	return nil
}

// nodeStatusText 将 Node.Status 数值转为可读标签。
func nodeStatusText(v any) string {
	switch n := v.(type) {
	case float64:
		switch int(n) {
		case 0:
			return "offline"
		case 1:
			return "online"
		case 2:
			return "starting"
		}
	}
	return formatScalar(v)
}

// formatInstanceList text 输出实例列表。
func formatInstanceList(w io.Writer, v any) error {
	list, ok := v.([]any)
	if !ok {
		if v == nil {
			fmt.Fprintln(w, "(无实例)")
			return nil
		}
		return writePrettyAny(w, v)
	}
	if len(list) == 0 {
		fmt.Fprintln(w, "(无实例)")
		return nil
	}
	fmt.Fprintf(w, "%-6s  %-20s  %-10s  %-8s  %s\n", "ID", "NAME", "STATUS", "NODE", "ROLE")
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		fmt.Fprintf(w, "%-6s  %-20s  %-10s  %-8s  %s\n",
			formatScalar(m["id"]),
			truncate(formatScalar(m["name"]), 20),
			formatScalar(m["status"]),
			formatScalar(m["nodeId"]),
			formatScalar(m["role"]),
		)
	}
	return nil
}

// formatInstanceDetail text 输出单实例状态。
func formatInstanceDetail(w io.Writer, v any) error {
	m, ok := v.(map[string]any)
	if !ok {
		return writePrettyAny(w, v)
	}
	keys := []string{"id", "uuid", "name", "status", "statusReason", "nodeId", "role", "type", "processType"}
	for _, k := range keys {
		if _, exists := m[k]; exists {
			fmt.Fprintf(w, "%s: %v\n", k, formatScalar(m[k]))
		}
	}
	return nil
}

// formatOK text 输出写操作成功。
func formatOK(w io.Writer, v any) error {
	if m, ok := v.(map[string]any); ok {
		if okv, exists := m["ok"]; exists {
			fmt.Fprintf(w, "ok: %v\n", formatScalar(okv))
			return nil
		}
	}
	return writePrettyAny(w, v)
}

// truncate 截断过长字符串并加省略号。
func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	return s[:max-1] + "…"
}
