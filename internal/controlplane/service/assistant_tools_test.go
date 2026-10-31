package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/controlplane/ai"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

/* *
 *工具层的关键语义。
 *
 * 这些断言守的是 spec 里几条**最容易被实现成「看起来对」的规则**：
 *  - 写必确认：即使目录里 RequiresConfirm=false 也强制确认；
 *  - 平台治理/文件写入/助手自身配置永久不在目录里；
 *  - 参数规范化后哈希稳定（键序不同不得改变哈希，否则等同于「换个写法就能换票」）；
 *  - 票据决策只接受 approve/reject。
 */

func TestAssistantToolRegistry_WriteAlwaysRequiresConfirm(t *testing.T) {
	// spec §4.1 第 5 条：write/destructive **强制** RequiresConfirm=true，
	// 即使目录里标记为 false 也不放行。这条是「写必确认」不变量。
	// 目录来源已改为 MCP：断言的是「MCP 目录派生出的元数据」，
	// 因此这条现在覆盖 108 个工具，而不只是原来手写的 13 个。
	injectTestMCPCatalog(t)
	for _, tool := range testRegistryTools() {
		if tool.Operation == "read" {
			if tool.RequiresConfirm {
				t.Fatalf("只读工具不该要求确认: %s", tool.Name)
			}
			continue
		}
		if !tool.RequiresConfirm {
			t.Fatalf("写/破坏性工具必须要求确认: %s (op=%s)", tool.Name, tool.Operation)
		}
	}
}

func TestAssistantToolRegistry_ForbiddenActionsExcluded(t *testing.T) {
	// spec §3「明确不做」的禁区由 **MCP 目录**保证（永久禁区永不进入 allToolSpecs）。
	// 后助手侧的常驻目录只剩三个元工具，因此这里改为断言：
	// 元工具名本身不得长得像任何禁区动作（防止有人把元工具做成万能通道）。
	names := assistantToolsDir()
	forbiddenSubstrings := []string{
		"file_write", "config_write", "config_rollback",
		"assistant_config", "rbac", "system_settings", "system_update",
	}
	for _, name := range names {
		for _, bad := range forbiddenSubstrings {
			if strings.Contains(name, bad) {
				t.Fatalf("禁止的动作 %q 出现在工具目录里（命中 %q）", name, bad)
			}
		}
	}
}

func TestAssistantToolRegistry_OnlyMetaToolsResident(t *testing.T) {
	// 常驻目录只允许三个元工具。业务工具改由 search/describe 按需取。
	//
	// 这条守的是「渐进式披露不被悄悄退化」：一旦有人把业务工具加回常驻，
	// 固定开销会立刻回到全量量级，而这在功能上完全看不出来（工具照样能用）。
	names := assistantToolsDir()
	allowed := map[string]struct{}{}
	for _, n := range assistantMetaToolNames() {
		allowed[n] = struct{}{}
	}
	if len(names) != len(allowed) {
		t.Fatalf("常驻工具应恰好 %d 个元工具，实际 %d 个：%v", len(allowed), len(names), names)
	}
	for _, n := range names {
		if _, ok := allowed[n]; !ok {
			t.Fatalf("业务工具 %q 不应常驻（渐进式披露被退化）", n)
		}
	}
}

// TestAssistantToolRegistry_ExtraReadToolsReachableBySearch 三个受限只读工具
// 仍必须能被 search_tools 搜到。
//
// 它们此前靠「常驻目录里有」来保证可见；改成按需探索后，若 MCP 目录里没有它们，
// 模型就再也找不到——那是功能丢失，而不是架构调整。因此改断言「可被搜到」。
func TestAssistantToolRegistry_ExtraReadToolsReachableBySearch(t *testing.T) {
	catalog := assistantToolCatalog()
	// 未注入时跳过：本用例的断言对象是 MCP 目录（由 mcp 包注入）。
	if len(catalog) == 0 {
		t.Skip("MCP 目录未注入，跳过（该断言由 mcp 包的覆盖度用例保证）")
	}
	for _, want := range []string{"cluster_overview", "list_tasks", "list_alerts"} {
		found := false
		for _, d := range searchAssistantTools(catalog, want, "") {
			found = true
			_ = d
			break
		}
		if !found {
			t.Fatalf("受限只读能力 %q 无法被 search_tools 搜到", want)
		}
	}
}

func TestAssistantToolRegistry_StableOrder(t *testing.T) {
	// 顺序抖动会让模型行为不可复现，也让「同一轮重跑」的对比失去意义。
	a := assistantToolsDir()
	b := assistantToolsDir()
	if len(a) != len(b) {
		t.Fatal("两次构造长度不一致")
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("第 %d 项不稳定: %q vs %q", i, a[i], b[i])
		}
	}
}

func TestHashToolArgs_KeyOrderIndependent(t *testing.T) {
	// 键序不同但内容相同的参数必须得到同一哈希：
	// 否则模型换个写法就能让「参数未变」的判定失效，等同绕过票据绑定。
	a := map[string]any{"b": float64(2), "a": float64(1), "c": "x"}
	b := map[string]any{"c": "x", "a": float64(1), "b": float64(2)}
	if hashToolArgs(a) != hashToolArgs(b) {
		t.Fatal("键序不同导致哈希不同——票据绑定会被绕过")
	}
	// 内容不同必须哈希不同。
	c := map[string]any{"a": float64(1), "b": float64(3), "c": "x"}
	if hashToolArgs(a) == hashToolArgs(c) {
		t.Fatal("内容不同却哈希相同")
	}
}

func TestNormalizeToolArgs_InvalidJSONRejected(t *testing.T) {
	if _, err := normalizeToolArgs("{not json"); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
	// 空参数视为空对象（无参数的工具很常见）。
	m, err := normalizeToolArgs("")
	if err != nil || len(m) != 0 {
		t.Fatalf("空参数应得到空 map，得到 %v / %v", m, err)
	}
}

func TestAssistantDelegatedExecutor_UnknownToolRejected(t *testing.T) {
	// 未登记的工具必须拒绝，而不是「找不到就当只读放行」。
	ex := &assistantDelegatedExecutor{svc: &AssistantChatService{}}
	out := ex.execute(context.Background(), 1, ai.ToolCall{
		CallID: "c1", ToolName: "system.not_a_real_tool", Args: "{}",
	})
	if out.Err == nil || !errors.Is(out.Err, ErrAssistantInvalid) {
		t.Fatalf("未登记工具应报 ErrAssistantInvalid，得到 %v", out.Err)
	}
}

func TestAssistantConfirmation_FrozenArgsRoundTrip(t *testing.T) {
	// 票据冻结的参数必须是「核对 UI 与执行都以它为权威」的原文快照。
	args := map[string]any{"instanceId": float64(7), "force": true}
	frozen, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	ticket := &model.AssistantConfirmation{
		ID:                1,
		ToolName:          "system.instance_delete",
		ArgsFrozen:        string(frozen),
		ArgsHash:          hashToolArgs(args),
		TargetFingerprint: "instance:7",
		ExpiresAt:         time.Now().Add(time.Minute),
	}
	view := confirmationViewOf(ticket, &AssistantToolAction{Operation: "destructive"})
	var back map[string]any
	if err := json.Unmarshal(view.Args, &back); err != nil {
		t.Fatalf("视图里的参数应可解析: %v", err)
	}
	if back["instanceId"] != float64(7) || back["force"] != true {
		t.Fatalf("冻结参数不一致: %v", back)
	}
	if view.TargetType != "instance" || view.TargetID != "7" {
		t.Fatalf("目标指纹拆分错误: %q / %q", view.TargetType, view.TargetID)
	}
	if view.Risk != "destructive" {
		t.Fatalf("破坏性操作的风险标记应为 destructive，得到 %q", view.Risk)
	}
}

func TestConfirmationTTL_IsTenMinutes(t *testing.T) {
	// spec §3 冻结：确认有效期 10 分钟（与平台审批的 24 小时区分）。
	if assistantConfirmTTL != 10*time.Minute {
		t.Fatalf("确认有效期应为 10 分钟，得到 %v", assistantConfirmTTL)
	}
}

// TestAssistantToolNames_SatisfyUpstreamCharset 工具名必须符合 OpenAI 工具名字符集。
//
// ## 为什么要有这条门禁（真机联调发现）
//
// 工具名原先用点号（`system.agent_whoami`）。真实上游对带工具的请求直接 400：
//
//	{"code":11152,"msg":"the tool name is invalid or duplicated"}
//
// 因为规范字符集是 `^[a-zA-Z0-9_-]{1,64}$`，点号不在其中。所有假上游都不校验字符集，
// 所以「只跑假上游」永远发现不了——这条用例把它变成 CI 可判定的失败，而不是上线后才炸。
func TestAssistantToolNames_SatisfyUpstreamCharset(t *testing.T) {
	// 与 OpenAI 规范同源：只允许字母、数字、下划线、连字符，长度 1..64。
	allowed := func(name string) bool {
		if name == "" || len(name) > 64 {
			return false
		}
		for _, r := range name {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			default:
				return false
			}
		}
		return true
	}

	for _, name := range assistantToolsDir() {
		if !allowed(name) {
			t.Fatalf("工具名 %q 含非法字符（上游会 400 the tool name is invalid）", name)
		}
	}
	// 分隔符本身也必须合规——它决定了所有内置工具名的形态。
	if !allowed(assistantToolSourceSystem + assistantToolNameSeparator + "x") {
		t.Fatalf("分隔符 %q 组合后不符合上游字符集", assistantToolNameSeparator)
	}
}

// assistantToolsDir 返回「模型可见的工具名列表」（起即三个元工具）。
//
// 原实现依赖被删除的本地注册表；现在直接读常驻 spec，语义不变：
// 断言对象是「本轮下发给模型的工具集合」。
func assistantToolsDir() []string {
	specs := assistantToolSpecs()
	out := make([]string, 0, len(specs))
	for _, sp := range specs {
		out = append(out, sp.Name)
	}
	return out
}
