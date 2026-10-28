package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/wcpe/JianManager/internal/controlplane/ai"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 渐进式工具披露（FR-544）。
//
// ## 为什么不再把工具全量塞进上下文
//
// MCP 目录现有 108 个工具，全量 schema 约 45 KB（≈11k tokens）。这个成本与
// 「本轮要做什么」无关——用户只是打招呼，也要为 108 个工具的 JSON Schema 付费。
// 而且工具越多，模型在长清单里挑错的概率越高（挑错参数比不调用更糟）。
//
// ## 三段式：搜索 → 取 schema → 调用
//
// 常驻上下文只放三个**固定的元工具**（体积与工具总数无关）：
//
//   - `search_tools`   按关键词/分类查有哪些工具（回名字 + 一句话用途）
//   - `describe_tool`  取某个工具的完整参数 schema
//   - `call_tool`      真正调用（内部走 MCP 执行路径）
//
// 这样「工具总数从 13 涨到 108」不再改变常驻开销，也让模型按需付费：
// 只为自己真正要用的那个工具读 schema。
//
// ## 为什么不复用原生 function calling 的「工具名直调」
//
// 若把 108 个工具都注册成 function，模型可以直接调用、无需 call_tool 这一层。
// 但那正是要避免的全量下发。三维元工具是**刻意的间接层**：用一次额外往返
// 换掉每轮的固定开销。

// AssistantToolDescriptor 工具目录条目（不含参数 schema——那正是要按需取的部分）。
type AssistantToolDescriptor struct {
	Name string `json:"name"`
	// Summary 一句话用途，来自 MCP 工具描述。
	Summary string `json:"summary"`
	// Operation read / write / destructive。
	Operation string `json:"operation"`
	// RequiresConfirm 是否必须经用户逐次确认。
	RequiresConfirm bool `json:"requiresConfirm"`
	// Category 从工具名前缀派生（instance / node / file / config / bot / ...），
	// 用于 search_tools 的粗筛，省掉模型瞎猜关键词。
	Category string `json:"category"`
}

// AssistantToolDetail 工具的完整定义（按需取）。
type AssistantToolDetail struct {
	AssistantToolDescriptor
	// Parameters MCP 工具原始 JSON Schema（原样透传，不二次加工）。
	Parameters map[string]any `json:"parameters"`
}

// 注入缝：由 mcp 包在 init 中填充（mcp 已 import service，反向 import 会成环）。
var (
	assistantToolCatalogProvider func() []AssistantToolDescriptor
	assistantToolDetailProvider  func(name string) (AssistantToolDetail, bool)
	assistantToolExecProvider    func(ctx context.Context, owner uint, name string, args map[string]any) (string, error)
	// assistantToolMetaProvider 取工具的「执行元数据」（action / 能力 / 读写 / 目标类型）。
	//
	// 这是**确认票据**与**权限重验**所依赖的那份信息。它同样来自 MCP 目录，
	// 因此平台不再维护第二套工具定义：单一真源 = mcp.allToolSpecs。
	assistantToolMetaProvider func(name string) (*AssistantToolAction, bool)

	assistantToolCatalogMu sync.RWMutex
)

// SetAssistantToolMeta 由 mcp 包注入「按名取执行元数据」。
func SetAssistantToolMeta(fn func(name string) (*AssistantToolAction, bool)) {
	assistantToolCatalogMu.Lock()
	assistantToolMetaProvider = fn
	assistantToolCatalogMu.Unlock()
}

// assistantToolMeta 按名取执行元数据（未注入或不存在时返回 nil,false）。
func assistantToolMeta(name string) (*AssistantToolAction, bool) {
	assistantToolCatalogMu.RLock()
	fn := assistantToolMetaProvider
	assistantToolCatalogMu.RUnlock()
	if fn == nil {
		return nil, false
	}
	return fn(name)
}

// SetAssistantToolCatalog 由 mcp 包注入工具目录枚举器。
func SetAssistantToolCatalog(fn func() []AssistantToolDescriptor) {
	assistantToolCatalogMu.Lock()
	assistantToolCatalogProvider = fn
	assistantToolCatalogMu.Unlock()
}

// SetAssistantToolDetail 由 mcp 包注入「按名取完整定义」。
func SetAssistantToolDetail(fn func(name string) (AssistantToolDetail, bool)) {
	assistantToolCatalogMu.Lock()
	assistantToolDetailProvider = fn
	assistantToolCatalogMu.Unlock()
}

// SetAssistantToolExecutor 由 mcp 包注入「进程内执行一个 MCP 工具」。
func SetAssistantToolExecutor(fn func(ctx context.Context, owner uint, name string, args map[string]any) (string, error)) {
	assistantToolCatalogMu.Lock()
	assistantToolExecProvider = fn
	assistantToolCatalogMu.Unlock()
}

func assistantToolCatalog() []AssistantToolDescriptor {
	assistantToolCatalogMu.RLock()
	fn := assistantToolCatalogProvider
	assistantToolCatalogMu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn()
}

// assistantToolExecutor 取注入的执行入口（未注入时返回 nil）。
func assistantToolExecutor() func(ctx context.Context, owner uint, name string, args map[string]any) (string, error) {
	assistantToolCatalogMu.RLock()
	fn := assistantToolExecProvider
	assistantToolCatalogMu.RUnlock()
	return fn
}

func assistantToolDetail(name string) (AssistantToolDetail, bool) {
	assistantToolCatalogMu.RLock()
	fn := assistantToolDetailProvider
	assistantToolCatalogMu.RUnlock()
	if fn == nil {
		return AssistantToolDetail{}, false
	}
	return fn(name)
}

// 三个元工具的对外名。与既有 `system_*` 并存：元工具本身也是内置工具，
// 沿用同一命名空间前缀，避免与外部 MCP 连接的工具名冲突。
const (
	assistantMetaToolSearch   = "system_search_tools"
	assistantMetaToolDescribe = "system_describe_tool"
	assistantMetaToolCall     = "system_call_tool"
)

// assistantMetaToolNames 三个元工具名集合（供测试与提示词构建复用）。
func assistantMetaToolNames() []string {
	return []string{assistantMetaToolSearch, assistantMetaToolDescribe, assistantMetaToolCall}
}

// AssistantToolCategoryOf 导出给 mcp 包复用（分类规则只维护一处）。
func AssistantToolCategoryOf(name string) string { return assistantToolCategory(name) }

// assistantToolCategory 从工具名派生分类（`instance_send_command` → `instance`）。
//
// 为什么按名字前缀而不是让 mcp 侧维护一份分类表：108 个工具手写分类必然漂移，
// 而工具名本身已经带域前缀（instance_/node_/file_/config_/bot_/...）。
// 前缀取不到时归 `other`，不猜。
func assistantToolCategory(name string) string {
	prefix, _, ok := strings.Cut(strings.TrimPrefix(name, "system_"), "_")
	if !ok || prefix == "" {
		return "other"
	}
	switch prefix {
	case "instance", "node", "file", "config", "plugin", "bot", "beacon",
		"network", "registration", "loadtest", "agent", "snapshot", "artifact":
		return prefix
	default:
		return "other"
	}
}

// searchAssistantTools 按关键词与分类过滤工具目录。
//
// 匹配规则刻意宽松：名字子串 **或** 用途子串 **或** 分类相等，任一命中即返回。
// 宁可多给几条让模型挑，也不要因为措辞不同就返回空——空结果会让模型
// 直接放弃使用工具，转而凭记忆答题（那是更糟的失败模式）。
func searchAssistantTools(catalog []AssistantToolDescriptor, keyword, category string) []AssistantToolDescriptor {
	kw := strings.ToLower(strings.TrimSpace(keyword))
	cat := strings.ToLower(strings.TrimSpace(category))
	out := make([]AssistantToolDescriptor, 0, 16)
	for _, d := range catalog {
		if cat != "" && d.Category != cat {
			continue
		}
		if kw == "" {
			out = append(out, d)
			continue
		}
		if strings.Contains(strings.ToLower(d.Name), kw) ||
			strings.Contains(strings.ToLower(d.Summary), kw) {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// assistantMetaToolSpecs 三个元工具的定义（常驻上下文的那部分）。
//
// 参数描述要写清「为什么」：模型只有读到「先用 search 再 describe」的说明，
// 才会按三段式走；否则它可能直接 call_tool 猜参数。
func assistantMetaToolSpecs() []ai.ToolSpec {
	return []ai.ToolSpec{
		{
			Name: assistantMetaToolSearch,
			Description: "搜索可用的平台工具（按关键词或分类）。" +
				"平台工具很多，不要凭记忆猜工具名：先用本工具看到有哪些，再用 " +
				assistantMetaToolDescribe + " 取参数定义，最后用 " + assistantMetaToolCall + " 调用。",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "keyword": {"type": "string", "description": "关键词，会匹配工具名与用途，例如「日志」「启动」「文件」"},
    "category": {"type": "string", "description": "可选分类过滤：instance/node/file/config/plugin/bot/beacon/network/registration/loadtest/agent/snapshot/artifact"}
  }
}`),
		},
		{
			Name: assistantMetaToolDescribe,
			Description: "取某个平台工具的完整参数定义（JSON Schema）。" +
				"调用任何工具前先取它的定义，避免参数名或结构猜错。",
			Parameters: json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "工具名，来自 %s 的返回"}
  },
  "required": ["name"]
}`, assistantMetaToolSearch)),
		},
		{
			Name: assistantMetaToolCall,
			Description: "调用一个平台工具。写操作会先经用户逐次确认，不会立即执行。" +
				"参数必须符合该工具的 schema（先用 " + assistantMetaToolDescribe + " 取）。",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "工具名"},
    "arguments": {"type": "object", "description": "该工具的参数对象"}
  },
  "required": ["name"]
}`),
		},
	}
}

// assistantPageRef 页面引用标记（只带标视，不带内容）。
//
// ## 为什么只带 id 不带名字与状态
//
// 名字、状态、指标都是**会变的、且可能带权限语义**的内容。把它们塞进上下文，
// 既占体积，又可能过期（用户页面停留 10 分钟后状态早就变了），
// 还可能绕开工具的权限回填（shared-contracts §6 明确名称由工具按权限回填）。
// 只给「实体类型 + 数字 id」，模型需要什么就自己去查——那份数据永远是最新的、
// 且一定过了权限判定。
type assistantPageRef struct {
	PageKey  string   `json:"page"`
	Entity   string   `json:"entity,omitempty"`
	EntityID string   `json:"entityId,omitempty"`
	Filters  []string `json:"filters,omitempty"`
	Tab      string   `json:"tab,omitempty"`
}

// pageRefSuffix 返回要追加到系统提示词尾部的引用标记（无上下文时为空）。
func pageRefSuffix(run *model.AssistantRun) string {
	ref := formatPageRef(run)
	if ref == "" {
		return ""
	}
	return "\n\n" + ref
}

// formatPageRef 把页面上下文压成一行引用标记。
//
// 空上下文返回空串：不产生「当前页面：（空）」这种无信息量的行——它只会
// 让模型以为页面信息缺失而反复确认。
func formatPageRef(run *model.AssistantRun) string {
	if run == nil || run.ContextSnapshot == nil {
		return ""
	}
	raw := strings.TrimSpace(*run.ContextSnapshot)
	if raw == "" {
		return ""
	}
	var ref assistantPageRef
	if err := json.Unmarshal([]byte(raw), &ref); err != nil {
		return ""
	}
	var parts []string
	if ref.PageKey != "" {
		parts = append(parts, "页面="+ref.PageKey)
	}
	if ref.Entity != "" && ref.EntityID != "" {
		// 只给类型与 id：名称/状态由工具查询（更准，且路径上过权限）。
		parts = append(parts, fmt.Sprintf("当前实体=%s#%s", ref.Entity, ref.EntityID))
	}
	if ref.Tab != "" {
		parts = append(parts, "标签页="+ref.Tab)
	}
	if len(ref.Filters) > 0 {
		parts = append(parts, "筛选="+strings.Join(ref.Filters, " "))
	}
	if len(parts) == 0 {
		return ""
	}
	return "【参考】" + strings.Join(parts, "，") +
		"（只是引用，不含内容；需要名称、状态或指标时请调用工具查询）"
}
