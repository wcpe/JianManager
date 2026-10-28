package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 配置分层合并（FR-529 §5）。
//
// 纯函数实现，不碰 DB 与 gRPC：作用域链由调用方解析后传入，这里只负责
// 「按 format 合并 + 确定性序列化 + 逐键溯源」。这样合并逻辑可被表驱动单测穷举，
// 也保证「同一组合并两次 hash 相同」这一确定性要求可被直接断言。

// LayerSource 是 traced 中记录的来源信息：某个键最终值来自哪一层。
type LayerSource struct {
	// Level group / instance。
	Level string `json:"level"`
	// ID 分组 ID 或实例 ID。
	ID uint `json:"id"`
	// Name 分组名/实例名，便于运维不查 ID 也能认出来源。
	Name string `json:"name,omitempty"`
	// VersionNo 该层命中的版本号。
	VersionNo int `json:"versionNo"`
}

// TracedValue 是单个键的溯源条目：值 + 来源 + 是否被删除。
type TracedValue struct {
	Value   any         `json:"value"`
	Source  LayerSource `json:"source"`
	Deleted bool        `json:"deleted,omitempty"`
}

// MergeWarning 合并告警。
type MergeWarning struct {
	// Level error 表示该层整体失效（语法错误）；warning 表示仅提示（类型冲突）。
	Level string `json:"level"`
	// Code 稳定标识，便于前端做定向文案。
	Code string `json:"code"`
	// Key 出问题的键路径（点分）；无具体键时为空。
	Key     string      `json:"key,omitempty"`
	Message string      `json:"message"`
	Source  LayerSource `json:"source"`
}

// 合并告警码。
const (
	// WarnLayerParseFailed 该层原文解析失败（语法错误），该层被整体跳过并给出层与行号。
	WarnLayerParseFailed = "LAYER_PARSE_FAILED"
	// WarnTypeConflict 中间层类型冲突（对象 vs 标量/数组），高层覆盖低层。
	WarnTypeConflict = "TYPE_CONFLICT"
	// WarnRootNotObject 该层根节点不是对象，整层忽略。
	WarnRootNotObject = "ROOT_NOT_OBJECT"
)

// MergeLayer 是参与合并的一层输入（已从 DB 读出）。
type MergeLayer struct {
	Level      model.ConfigScopeLevel
	ScopeRefID uint
	// Name 分组名/实例名，仅影响 traced 可读性，不参与合并。
	Name      string
	VersionNo int
	Content   string
}

// source 构造本层的 LayerSource。
func (l MergeLayer) source() LayerSource {
	return LayerSource{Level: string(l.Level), ID: l.ScopeRefID, Name: l.Name, VersionNo: l.VersionNo}
}

// MergeResult 是一次合并的产物。
type MergeResult struct {
	// Content 合并后的完整文本（确定性输出，可直接下发落盘）。
	Content string
	// EffectiveHash sha256(Content) 小写 hex。
	EffectiveHash string
	// Traced 每个键的来源。被 null 删除的键也在这里，带 deleted=true。
	Traced map[string]TracedValue
	// Warnings 合并过程中的告警（解析失败 / 类型冲突）。
	Warnings []MergeWarning
	// Valid 为 false 表示存在使结果不可信的告警（当前为层解析失败）。
	Valid bool
}

// MergeConfigLayers 按作用域链（低→高优先级）合并各层，输出确定性文本与逐键溯源。
//
// layers 顺序即优先级：越靠后优先级越高。调用方需保证链已从根到叶排好
// （[group:root, ..., group:parent, instance:self]）。
func MergeConfigLayers(format model.ConfigFileFormat, layers []MergeLayer) (*MergeResult, error) {
	switch format {
	case model.ConfigFormatYAML, model.ConfigFormatJSON:
		return mergeStructured(format, layers)
	case model.ConfigFormatProperties:
		return mergePropertiesLayers(layers)
	default:
		return nil, fmt.Errorf("不支持的配置格式: %s", format)
	}
}

// ---------------------------------------------------------------------------
// yaml / json：深合并 + null 删键
// ---------------------------------------------------------------------------

// mergeStructured 合并 yaml/json 层。两者内部统一用 any 表示
// （map[string]any / []any / rawScalar / nil），仅解析与序列化方式不同。
//
// 深合并规则（§5.2）：对象递归合并、数组整体替换、显式 null 删除该键。
func mergeStructured(format model.ConfigFileFormat, layers []MergeLayer) (*MergeResult, error) {
	res := &MergeResult{Traced: map[string]TracedValue{}, Warnings: []MergeWarning{}, Valid: true}
	merged := map[string]any{}
	for _, layer := range layers {
		// 空内容视为空对象，不影响合并（§5.4）。
		if strings.TrimSpace(layer.Content) == "" {
			continue
		}
		doc, err := parseStructured(format, layer.Content)
		if err != nil {
			// 语法解析失败：该层标记失效，不参与合并，并给出层与行号（§5.4）。
			res.Valid = false
			res.Warnings = append(res.Warnings, MergeWarning{
				Level: "error", Code: WarnLayerParseFailed, Message: err.Error(), Source: layer.source(),
			})
			continue
		}
		deepMergeInto(merged, doc, "", layer.source(), res)
	}
	content, err := serializeStructured(format, merged)
	if err != nil {
		return nil, err
	}
	res.Content = content
	res.EffectiveHash = hashContent(content)
	return res, nil
}

// rawScalar 记住标量的原始字面量与标签，使数值在合并后不被重新格式化（§5.3）。
// 这是「同一组合并两次 hash 相同」之外的另一条确定性要求：数值原格式必须保留，
// 否则 1.50 会变成 1.5、0x10 会变成 16，灰度和变更检测都会误报。
type rawScalar struct {
	Tag   string
	Value string
}

// parseStructured 按格式解析为通用 any 结构。
func parseStructured(format model.ConfigFileFormat, content string) (any, error) {
	switch format {
	case model.ConfigFormatJSON:
		dec := json.NewDecoder(strings.NewReader(content))
		// 用 Number 保留数字字面量原文（float64 会丢精度并改格式）。
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("JSON 解析失败: %s", jsonErrorWithLine(content, err))
		}
		// 拒绝尾随内容（`{} {}` 这类输入应报错而不是静默取第一段）。
		if _, err := dec.Token(); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("JSON 解析失败: 文档结束后存在多余内容")
		}
		return v, nil
	case model.ConfigFormatYAML:
		return parseYAMLToAny(content)
	default:
		return nil, fmt.Errorf("格式 %s 不走结构化解析", format)
	}
}

// jsonErrorWithLine 给 encoding/json 的错误补上行号。
//
// encoding/json 的 SyntaxError/UnmarshalTypeError 只给字节 Offset 不给行号，
// 而 spec §5.4 要求「语法错误能被定位到层与行号」——运维拿到 "invalid character 'b'"
// 无从下手，必须告诉他是第几行。yaml.v3 自带行号，故只有 JSON 需要这一步。
func jsonErrorWithLine(content string, err error) string {
	offset := -1
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntaxErr):
		offset = int(syntaxErr.Offset)
	case errors.As(err, &typeErr):
		offset = int(typeErr.Offset)
	}
	if offset < 0 {
		return err.Error()
	}
	// Offset 是 1-based 字节位置；换算成 1-based 行号。
	line := 1 + strings.Count(content[:min(offset, len(content))], "\n")
	col := offset - strings.LastIndex(content[:min(offset, len(content))], "\n")
	return fmt.Sprintf("第 %d 行第 %d 列: %v", line, col, err)
}

// parseYAMLToAny 用 yaml.Node 解析并转成通用结构，保留标量原始字面量。
func parseYAMLToAny(content string) (any, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		return nil, fmt.Errorf("YAML 解析失败: %w", err)
	}
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 {
			return map[string]any{}, nil
		}
		return yamlNodeToAny(doc.Content[0])
	}
	return yamlNodeToAny(&doc)
}

// yamlNodeToAny 递归把 yaml.Node 转为 map[string]any / []any / rawScalar / nil。
func yamlNodeToAny(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return map[string]any{}, nil
		}
		return yamlNodeToAny(n.Content[0])
	case yaml.MappingNode:
		out := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, err := yamlNodeToAny(n.Content[i])
			if err != nil {
				return nil, err
			}
			key := scalarText(k)
			v, err := yamlNodeToAny(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			out[key] = v
		}
		return out, nil
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := yamlNodeToAny(c)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case yaml.ScalarNode:
		if n.Tag == "!!null" {
			return nil, nil
		}
		return rawScalar{Tag: n.Tag, Value: n.Value}, nil
	case yaml.AliasNode:
		if n.Alias == nil {
			return nil, nil
		}
		return yamlNodeToAny(n.Alias)
	default:
		return nil, fmt.Errorf("YAML 解析失败: 不支持的节点类型 %v（第 %d 行）", n.Kind, n.Line)
	}
}

// scalarText 把解析出的标量取成字符串，用于映射键。
func scalarText(v any) string {
	switch t := v.(type) {
	case rawScalar:
		return t.Value
	case string:
		return t
	case json.Number:
		return t.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

// deepMergeInto 把 src 深合并进 dst；null 值删除对应键并记入 traced（deleted=true）。
// prefix 是当前递归位置的键路径前缀（点分）。
func deepMergeInto(dst map[string]any, src any, prefix string, srcLayer LayerSource, res *MergeResult) {
	srcMap, ok := src.(map[string]any)
	if !ok {
		if prefix == "" {
			// 根节点不是对象（如顶层是列表）：整层忽略并告警，绝不静默。
			res.Warnings = append(res.Warnings, MergeWarning{
				Level: "warning", Code: WarnRootNotObject, Key: "",
				Message: "该层根节点不是对象，整层忽略（配置文件根必须是映射/对象）", Source: srcLayer,
			})
		}
		return
	}
	// 按键字典序遍历，保证告警与溯源顺序稳定。
	keys := make([]string, 0, len(srcMap))
	for k := range srcMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := srcMap[k]
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		if v == nil {
			// null 删键：删掉低层遗留值，但仍在 traced 里留痕（§5.2 / 验收 6）。
			delete(dst, k)
			removeSubtree(res.Traced, path)
			res.Traced[path] = TracedValue{Value: nil, Source: srcLayer, Deleted: true}
			continue
		}
		if childMap, isMap := v.(map[string]any); isMap {
			existing, exists := dst[k]
			existingMap, existingIsMap := existing.(map[string]any)
			if exists && !existingIsMap {
				// 中间层类型冲突：低层是标量/数组、高层是对象 → 高层覆盖并告警（§5.4）。
				res.Warnings = append(res.Warnings, MergeWarning{
					Level: "warning", Code: WarnTypeConflict, Key: path,
					Message: fmt.Sprintf("低层键 %q 不是对象，被高层对象覆盖", path), Source: srcLayer,
				})
				removeSubtree(res.Traced, path)
			}
			if !existingIsMap {
				existingMap = map[string]any{}
				dst[k] = existingMap
			}
			deepMergeInto(existingMap, childMap, path, srcLayer, res)
			continue
		}
		// 标量 / 数组：整体替换（数组不元素合并）。
		if existing, exists := dst[k]; exists {
			if _, wasMap := existing.(map[string]any); wasMap {
				// 低层是对象、高层是标量/数组 → 高层覆盖低层，同样告警。
				res.Warnings = append(res.Warnings, MergeWarning{
					Level: "warning", Code: WarnTypeConflict, Key: path,
					Message: fmt.Sprintf("低层键 %q 是对象，被高层标量/数组覆盖", path), Source: srcLayer,
				})
			}
		}
		dst[k] = v
		removeSubtree(res.Traced, path)
		res.Traced[path] = TracedValue{Value: nativeValue(v), Source: srcLayer}
	}
}

// removeSubtree 删除 path 及其全部子路径的溯源记录（覆盖或删除时子树失效）。
func removeSubtree(traced map[string]TracedValue, path string) {
	delete(traced, path)
	prefix := path + "."
	for p := range traced {
		if strings.HasPrefix(p, prefix) {
			delete(traced, p)
		}
	}
}

// nativeValue 把内部表示转成便于 JSON 输出的原生值（rawScalar → string/int64/float64/bool）。
func nativeValue(v any) any {
	switch t := v.(type) {
	case rawScalar:
		return scalarNative(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = nativeValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = nativeValue(val)
		}
		return out
	case json.Number:
		return numberNative(t)
	default:
		return v
	}
}

// scalarNative 按 yaml 标签把 rawScalar 还原为原生类型（用于 traced 的 JSON 呈现）。
func scalarNative(s rawScalar) any {
	switch s.Tag {
	case "!!int":
		if n, err := strconv.ParseInt(s.Value, 0, 64); err == nil {
			return n
		}
		return s.Value
	case "!!float":
		if f, err := strconv.ParseFloat(s.Value, 64); err == nil {
			return f
		}
		return s.Value
	case "!!bool":
		return s.Value == "true" || s.Value == "True" || s.Value == "yes"
	case "!!null":
		return nil
	default:
		return s.Value
	}
}

// numberNative 把 json.Number 还原为 int64/float64，失败时返回原文字符串。
func numberNative(n json.Number) any {
	if i, err := n.Int64(); err == nil {
		return i
	}
	if f, err := n.Float64(); err == nil {
		return f
	}
	return n.String()
}

// serializeStructured 确定性序列化：对象键字典序、数组保序、数值保持原格式（§5.3）。
func serializeStructured(format model.ConfigFileFormat, merged map[string]any) (string, error) {
	switch format {
	case model.ConfigFormatJSON:
		var buf strings.Builder
		writeJSON(&buf, merged, 0)
		buf.WriteString("\n")
		return buf.String(), nil
	case model.ConfigFormatYAML:
		var buf strings.Builder
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(toYAMLNode(merged)); err != nil {
			return "", fmt.Errorf("序列化 YAML 失败: %w", err)
		}
		_ = enc.Close()
		return buf.String(), nil
	default:
		return "", fmt.Errorf("格式 %s 不做结构化序列化", format)
	}
}

// writeJSON 手写 JSON 序列化以保证键字典序、缩进与数值原格式。
func writeJSON(buf *strings.Builder, v any, depth int) {
	indent := func(n int) { buf.WriteString(strings.Repeat("  ", n)) }
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			buf.WriteString("{}")
			return
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteString("{\n")
		for i, k := range keys {
			indent(depth + 1)
			writeJSONString(buf, k)
			buf.WriteString(": ")
			writeJSON(buf, t[k], depth+1)
			if i < len(keys)-1 {
				buf.WriteString(",")
			}
			buf.WriteString("\n")
		}
		indent(depth)
		buf.WriteString("}")
	case []any:
		if len(t) == 0 {
			buf.WriteString("[]")
			return
		}
		buf.WriteString("[\n")
		for i, item := range t {
			indent(depth + 1)
			writeJSON(buf, item, depth+1)
			if i < len(t)-1 {
				buf.WriteString(",")
			}
			buf.WriteString("\n")
		}
		indent(depth)
		buf.WriteString("]")
	case rawScalar:
		writeJSONRawScalar(buf, t)
	case json.Number:
		// 保持原始字面量，不重新格式化（§5.3 数值保持原格式）。
		buf.WriteString(t.String())
	case string:
		writeJSONString(buf, t)
	case bool:
		buf.WriteString(strconv.FormatBool(t))
	case nil:
		buf.WriteString("null")
	default:
		// 兜底：交给 encoding/json（非常规类型不应出现在这里）。
		if b, err := json.Marshal(t); err == nil {
			buf.Write(b)
			return
		}
		buf.WriteString("null")
	}
}

// writeJSONRawScalar 按 yaml 标签输出 JSON 字面量。
func writeJSONRawScalar(buf *strings.Builder, s rawScalar) {
	switch s.Tag {
	case "!!int", "!!float", "!!bool":
		buf.WriteString(s.Value)
	case "!!null":
		buf.WriteString("null")
	default:
		writeJSONString(buf, s.Value)
	}
}

// writeJSONString 写 JSON 字符串字面量（复用 encoding/json 的转义规则）。
func writeJSONString(buf *strings.Builder, s string) {
	b, err := json.Marshal(s)
	if err != nil {
		buf.WriteString(`""`)
		return
	}
	buf.Write(b)
}

// toYAMLNode 把通用结构转成 yaml.Node 并按字典序排列键，保证确定性输出。
func toYAMLNode(v any) *yaml.Node {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, k := range keys {
			node.Content = append(node.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k},
				toYAMLNode(t[k]),
			)
		}
		return node
	case []any:
		node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, item := range t {
			node.Content = append(node.Content, toYAMLNode(item))
		}
		return node
	case rawScalar:
		if t.Tag == "!!null" {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: t.Tag, Value: t.Value}
	case json.Number:
		tag := "!!int"
		if strings.ContainsAny(t.String(), ".eE") {
			tag = "!!float"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: t.String()}
	case string:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: t}
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(t)}
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	default:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: fmt.Sprint(t)}
	}
}

// ---------------------------------------------------------------------------
// properties：键级覆盖，不支持删键
// ---------------------------------------------------------------------------

// propertiesEntry 是 properties 的一行。
type propertiesEntry struct {
	// Key 键名；注释行与空行的 Key 为空。
	Key string
	// Value 值文本。
	Value string
	// Raw 原始行，便于将来需要原文时复用。
	Raw string
	// Comment 该行是注释、空行或无分隔符的无效行。
	Comment bool
}

// mergePropertiesLayers 合并 properties 层（§5.2）：键级覆盖，后者覆盖前者；
// **不支持删键**——properties 里 `key=` 是空字符串值，不是删除。
//
// 输出顺序：按首次出现顺序保留键（低层先出现者在前），新增键追加在后。
// 注释与空行整体丢弃：合并结果是要下发的有效值，多层各自的注释在合并后互相矛盾，
// 保留反而是错误信息。
func mergePropertiesLayers(layers []MergeLayer) (*MergeResult, error) {
	res := &MergeResult{Traced: map[string]TracedValue{}, Warnings: []MergeWarning{}, Valid: true}
	values := map[string]string{}
	order := make([]string, 0, 16)
	for _, layer := range layers {
		if strings.TrimSpace(layer.Content) == "" {
			continue
		}
		src := layer.source()
		for _, e := range parsePropertiesLines(layer.Content) {
			if e.Comment || e.Key == "" {
				continue
			}
			if _, seen := values[e.Key]; !seen {
				order = append(order, e.Key)
			}
			values[e.Key] = e.Value
			res.Traced[e.Key] = TracedValue{Value: e.Value, Source: src}
		}
	}
	var buf strings.Builder
	for _, k := range order {
		buf.WriteString(k)
		buf.WriteString("=")
		buf.WriteString(values[k])
		buf.WriteString("\n")
	}
	res.Content = buf.String()
	res.EffectiveHash = hashContent(res.Content)
	return res, nil
}

// parsePropertiesLines 解析 properties 文本为行列表。
// 分隔符取第一个 `=` 或 `:`（与既有 parseProperties 同口径），值不做引号剥离。
func parsePropertiesLines(content string) []propertiesEntry {
	lines := strings.Split(content, "\n")
	out := make([]propertiesEntry, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "!") {
			out = append(out, propertiesEntry{Raw: line, Comment: true})
			continue
		}
		sep := strings.IndexAny(line, "=:")
		if sep < 0 {
			out = append(out, propertiesEntry{Raw: line, Comment: true})
			continue
		}
		out = append(out, propertiesEntry{
			Key:   strings.TrimSpace(line[:sep]),
			Value: strings.TrimSpace(line[sep+1:]),
			Raw:   line,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// 通用工具
// ---------------------------------------------------------------------------

// ParseConfigContent 按格式解析内容，返回扁平化的键值对（供 schema 校验与敏感路径匹配）。
// properties 返回键名原样；yaml/json 返回点分路径 → 原生值（嵌套对象本身也记一条）。
func ParseConfigContent(format model.ConfigFileFormat, content string) (map[string]any, error) {
	switch format {
	case model.ConfigFormatProperties:
		out := map[string]any{}
		for _, e := range parsePropertiesLines(content) {
			if e.Comment || e.Key == "" {
				continue
			}
			out[e.Key] = e.Value
		}
		return out, nil
	case model.ConfigFormatYAML, model.ConfigFormatJSON:
		doc, err := parseStructured(format, content)
		if err != nil {
			return nil, err
		}
		out := map[string]any{}
		flattenStructured(doc, "", out)
		return out, nil
	default:
		return nil, fmt.Errorf("不支持的配置格式: %s", format)
	}
}

// flattenStructured 把嵌套结构展开为点分路径 → 原生值；容器本身也记一条。
func flattenStructured(v any, prefix string, out map[string]any) {
	m, ok := v.(map[string]any)
	if !ok {
		if prefix != "" {
			out[prefix] = nativeValue(v)
		}
		return
	}
	for k, val := range m {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		out[path] = nativeValue(val)
		if _, isMap := val.(map[string]any); isMap {
			flattenStructured(val, path, out)
		}
	}
}
