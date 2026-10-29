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
// 这是「表单与原始文本双模式切换，保存后注释不丢失」的核心：表单保存走字段级补丁，
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
//
// 遇到无法安全表达的写入时返回错误而**不是**强行覆盖——见 yamlSetPath 的说明。
func patchYAML(content string, updates []fieldUpdate) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		return "", fmt.Errorf("解析 yaml 失败: %w", err)
	}
	root := yamlRoot(&doc)
	for _, u := range updates {
		path := strings.Split(u.Key, ".")
		if err := yamlSetPath(root, path, u.Value, u.Type); err != nil {
			return "", err
		}
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
// yamlSetPath 在映射节点 m 上按点路径写入一个标量值。
//
// ## 为什么宁可报错也不覆盖容器
//
// 本函数服务于「字段级补丁」——调用方表达的是「把这个**字段**改成这个值」，而字段是一个标量。
// 若路径命中的是一个映射或序列（例如把标量写到 `servers` 上，而真实配置里 `servers` 是
// 「子服名 → 地址」的映射），把它整个换成标量就等于**删掉整棵子树**：两台后端、它们的
// address/motd/restricted 全部消失，用户只看到「保存成功」。
//
// 这正是此前实测到的破坏形态：`servers: {lobby: …, game1: …}` 打一个 `servers` 标量补丁后
// 变成 `servers: 'lobby: localhost:25566'`。`permissions.default`（真实是列表）同理会把
// 列表后面的条目丢掉。这类写入没有任何正当用途，必须在写盘前拦住并报错。
//
// ## 列表：只有一个元素时才敢进
//
// `listeners.host` 这类键在 YAML 里位于序列项内部（`listeners:` 是列表）。只有一个监听器时
// 语义无歧义，可以安全地进到那一项里改写 `host`；一旦有多个监听器，「改哪一个」就没有答案，
// 此时报错让用户改用文本模式，而不是替用户猜一个。
func yamlSetPath(m *yaml.Node, path []string, value, typ string) error {
	if m.Kind != yaml.MappingNode || len(path) == 0 {
		return fmt.Errorf("配置结构不符合预期：无法在非映射节点上写入 %q", strings.Join(path, "."))
	}
	key := path[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != key {
			continue
		}
		val := m.Content[i+1]
		if len(path) == 1 {
			// 叶子：目标必须是标量。序列/映射一律拒绝覆盖。
			if val.Kind != yaml.ScalarNode {
				return fmt.Errorf(
					"键 %q 在配置中是%s，不是单个值，拒绝用 %q 覆盖（会丢失其下的全部内容）；请在文本模式下修改",
					key, yamlKindName(val.Kind), value)
			}
			applyScalar(val, value, typ)
			return nil
		}
		child, err := yamlDescend(val, key)
		if err != nil {
			return err
		}
		return yamlSetPath(child, path[1:], value, typ)
	}
	// 未找到：追加新键。
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	if len(path) == 1 {
		valNode := &yaml.Node{Kind: yaml.ScalarNode}
		applyScalar(valNode, value, typ)
		m.Content = append(m.Content, keyNode, valNode)
		return nil
	}
	child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	m.Content = append(m.Content, keyNode, child)
	return yamlSetPath(child, path[1:], value, typ)
}

// yamlDescend 取出继续向下写入的映射节点。
//
// 映射直接进入；序列仅在**恰好一个**元素且该元素是映射时才进入（见 yamlSetPath 的说明）；
// 标量无法容纳子键，报错而不是把它悄悄变成映射（那会丢掉原值）。
func yamlDescend(n *yaml.Node, key string) (*yaml.Node, error) {
	switch n.Kind {
	case yaml.MappingNode:
		return n, nil
	case yaml.SequenceNode:
		if len(n.Content) != 1 {
			return nil, fmt.Errorf(
				"键 %q 在配置中是含 %d 个元素的列表，无法确定要修改哪一项；请在文本模式下修改",
				key, len(n.Content))
		}
		item := n.Content[0]
		if item.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("键 %q 的列表元素不是映射，无法继续按路径写入", key)
		}
		return item, nil
	default:
		return nil, fmt.Errorf(
			"键 %q 在配置中是%s，无法在其下写入子键；请在文本模式下修改", key, yamlKindName(n.Kind))
	}
}

// yamlKindName 把节点类型渲染成中文，用于面向运维的错误提示。
func yamlKindName(k yaml.Kind) string {
	switch k {
	case yaml.MappingNode:
		return "映射（键值表）"
	case yaml.SequenceNode:
		return "列表"
	case yaml.ScalarNode:
		return "单个值"
	case yaml.AliasNode:
		return "别名引用"
	default:
		return "复合结构"
	}
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
