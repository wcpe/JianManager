package schema

import (
	"bufio"
	"strconv"
	"strings"

	"github.com/wcpe/JianManager/proto/workerpb"
)

func parseProperties(content string) []*workerpb.ConfigField {
	fields := []*workerpb.ConfigField{}
	for i, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "!") {
			continue
		}
		sep := strings.IndexAny(line, "=:")
		if sep < 0 {
			continue
		}
		key := strings.TrimSpace(line[:sep])
		value := strings.TrimSpace(line[sep+1:])
		fields = append(fields, &workerpb.ConfigField{Key: key, Value: value, Type: inferScalarType(value), Line: int32(i + 1)})
	}
	return fields
}

// parseFlatYAML 解析 properties-style 的简单 yaml 块（key=value / 缩进 key: value）。
// 嵌套结构（如 paper-global.yml 的 proxies.velocity.enabled）会保留点号路径。
func parseFlatYAML(content string) []*workerpb.ConfigField {
	fields := []*workerpb.ConfigField{}
	stack := []indentEntry{}
	scanner := bufio.NewScanner(strings.NewReader(content))
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := scanner.Text()
		if strings.TrimSpace(raw) == "" || strings.HasPrefix(strings.TrimSpace(raw), "#") {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		// 弹出更浅层
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		key, value, ok := parseYAMLLine(raw)
		if !ok {
			continue
		}
		path := key
		for _, e := range stack {
			path = e.key + "." + path
		}
		if value == "" {
			stack = append(stack, indentEntry{indent: indent, key: key})
			continue
		}
		fields = append(fields, &workerpb.ConfigField{Key: path, Value: value, Type: inferScalarType(value), Line: int32(lineNo)})
	}
	return fields
}

type indentEntry struct {
	indent int
	key    string
}

func parseYAMLLine(raw string) (key, value string, ok bool) {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "- ") {
		// 简化：列表项以 "- key: value" 处理
		trimmed = strings.TrimPrefix(trimmed, "- ")
	}
	idx := strings.IndexAny(trimmed, ":")
	if idx < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(trimmed[:idx])
	value = strings.TrimSpace(trimmed[idx+1:])
	value = strings.TrimSuffix(value, "#")
	value = strings.TrimSpace(value)
	if value == "" {
		return key, "", true
	}
	if strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"") && len(value) >= 2 {
		value = value[1 : len(value)-1]
	}
	return key, value, true
}

// parseFlatTOML 简化 TOML：仅处理顶层 key = value / key = "..."。
func parseFlatTOML(content string) []*workerpb.ConfigField {
	fields := []*workerpb.ConfigField{}
	inSection := ""
	for i, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			inSection = strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]")
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		raw := strings.TrimSpace(line[eq+1:])
		raw = strings.TrimSuffix(raw, "#")
		raw = strings.TrimSpace(raw)
		raw = strings.TrimSuffix(raw, ",")
		raw = strings.TrimSpace(raw)
		if strings.HasPrefix(raw, "\"") && strings.HasSuffix(raw, "\"") && len(raw) >= 2 {
			raw = raw[1 : len(raw)-1]
		}
		fullKey := key
		if inSection != "" {
			fullKey = inSection + "." + key
		}
		fields = append(fields, &workerpb.ConfigField{Key: fullKey, Value: raw, Type: inferScalarType(raw), Line: int32(i + 1)})
	}
	return fields
}

func inferScalarType(value string) string {
	lower := strings.ToLower(value)
	if lower == "true" || lower == "false" {
		return "bool"
	}
	if _, err := strconv.Atoi(value); err == nil {
		return "int"
	}
	return "string"
}
