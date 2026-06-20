package service

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// fieldUpdate 表示表单模式下对单个配置字段的修改：键（properties 平铺键 / yaml 点路径 / toml 顶层键）、
// 目标值（文本）、按 schema 推断的类型（用于 yaml/toml 的标量格式化）。
type fieldUpdate struct {
	Key   string
	Value string
	Type  string
}

// patchConfig 把表单字段修改补丁回原始配置文本，保留注释、键顺序与无关行。
// 这是 FR-031「表单与原始文本双模式切换，保存后注释不丢失」的核心：表单保存走字段级补丁，
// 而非整文件重新序列化（后者会丢注释）。properties 行级补丁、yaml AST 补丁、toml 顶层行级补丁。
func patchConfig(format, content string, updates []fieldUpdate) (string, error) {
	switch format {
	case "properties":
		kv := make(map[string]string, len(updates))
		for _, u := range updates {
			kv[u.Key] = u.Value
		}
		return patchProperties(content, kv), nil
	case "yaml":
		return patchYAML(content, updates)
	case "toml":
		return patchTOML(content, updates)
	default:
		return "", fmt.Errorf("格式 %s 不支持表单补丁", format)
	}
}

// patchYAML 通过 yaml.v3 Node AST 设置点路径标量值，保留注释、键顺序与其它节点。
// 路径中缺失的父级会按需创建；缺失的叶子键追加到对应映射末尾。
func patchYAML(content string, updates []fieldUpdate) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		return "", fmt.Errorf("解析 yaml 失败: %w", err)
	}
	root := yamlRoot(&doc)
	for _, u := range updates {
		path := strings.Split(u.Key, ".")
		yamlSetPath(root, path, u.Value, u.Type)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return "", fmt.Errorf("序列化 yaml 失败: %w", err)
	}
	_ = enc.Close()
	return buf.String(), nil
}

// yamlRoot 返回可写的根映射节点；空文档/非映射根会被初始化为映射。
func yamlRoot(doc *yaml.Node) *yaml.Node {
	// Unmarshal 进 *yaml.Node 得到 DocumentNode，其 Content[0] 为根。
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
			root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			doc.Content = []*yaml.Node{root}
			return root
		}
		return doc.Content[0]
	}
	// 空内容：doc 为零值，构造为 DocumentNode 包裹空映射。
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	doc.Kind = yaml.DocumentNode
	doc.Content = []*yaml.Node{root}
	return root
}

// yamlSetPath 在映射节点 m 下按 path 设置标量；逐级查找/创建，末级设置值与类型标签。
func yamlSetPath(m *yaml.Node, path []string, value, typ string) {
	if m.Kind != yaml.MappingNode || len(path) == 0 {
		return
	}
	key := path[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			val := m.Content[i+1]
			if len(path) == 1 {
				applyScalar(val, value, typ)
				return
			}
			if val.Kind != yaml.MappingNode {
				// 原值非映射，替换为映射以容纳子键。
				val.Kind = yaml.MappingNode
				val.Tag = "!!map"
				val.Value = ""
				val.Content = nil
			}
			yamlSetPath(val, path[1:], value, typ)
			return
		}
	}
	// 未找到：追加新键。
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	if len(path) == 1 {
		valNode := &yaml.Node{Kind: yaml.ScalarNode}
		applyScalar(valNode, value, typ)
		m.Content = append(m.Content, keyNode, valNode)
		return
	}
	child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	m.Content = append(m.Content, keyNode, child)
	yamlSetPath(child, path[1:], value, typ)
}

// applyScalar 就地把节点设为标量，按类型设置 yaml 标签；保留节点上的注释。
func applyScalar(n *yaml.Node, value, typ string) {
	n.Kind = yaml.ScalarNode
	n.Content = nil
	switch typ {
	case "bool":
		n.Tag = "!!bool"
		n.Value = normalizeBool(value)
	case "int":
		n.Tag = "!!int"
		n.Value = value
	default:
		n.Tag = "!!str"
		n.Value = value
	}
}

// patchTOML 以行级方式替换顶层键的值，保留注释、表头与其它行；缺失键追加到末尾。
// 字符串值加引号，bool/int 裸写。仅覆盖顶层键（velocity.toml schema 字段均为顶层）。
func patchTOML(content string, updates []fieldUpdate) (string, error) {
	lines := strings.Split(content, "\n")
	want := make(map[string]fieldUpdate, len(updates))
	for _, u := range updates {
		want[u.Key] = u
	}
	seen := map[string]bool{}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "[") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		if u, ok := want[key]; ok {
			lines[i] = key + " = " + tomlFormatValue(u.Value, u.Type)
			seen[key] = true
		}
	}
	missing := make([]string, 0)
	for k := range want {
		if !seen[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	for _, k := range missing {
		lines = append(lines, k+" = "+tomlFormatValue(want[k].Value, want[k].Type))
	}
	return strings.Join(lines, "\n"), nil
}

// tomlFormatValue 按类型格式化 toml 标量：string 加引号并转义，bool/int 裸写。
func tomlFormatValue(value, typ string) string {
	switch typ {
	case "bool":
		return normalizeBool(value)
	case "int":
		return value
	default:
		return strconv.Quote(value)
	}
}

// normalizeBool 把多种真值/假值文本归一为 "true"/"false"；无法识别时原样返回。
func normalizeBool(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "1", "on":
		return "true"
	case "false", "no", "0", "off":
		return "false"
	}
	return value
}
