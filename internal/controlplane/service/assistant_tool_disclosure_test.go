package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wcpe/JianManager/internal/controlplane/ai"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 渐进式工具披露（FR-544）。
//
// 这组用例守三件事：
//  1. 常驻上下文**不随工具总数增长**（这是本 FR 的全部意义）；
//  2. 三个元工具的描述能把模型引导到「先搜再取后调」；
//  3. 搜索足够宽松（宁多给几条，也不要因措辞不同返回空）。

func TestMetaToolSpecsAreOnlyThree(t *testing.T) {
	specs := assistantMetaToolSpecs()
	if len(specs) != 3 {
		t.Fatalf("常驻应只有 3 个元工具，实际 %d", len(specs))
	}
	names := map[string]bool{}
	for _, s := range specs {
		names[s.Name] = true
		if s.Description == "" {
			t.Fatalf("元工具 %s 缺描述（模型不知道该怎么用）", s.Name)
		}
		if len(s.Parameters) == 0 {
			t.Fatalf("元工具 %s 缺参数 schema", s.Name)
		}
	}
	for _, want := range assistantMetaToolNames() {
		if !names[want] {
			t.Fatalf("缺少元工具 %s", want)
		}
	}
}

// TestMetaToolConstantSize 常驻体积必须与工具总数无关。
//
// 断言方式是「元工具 schema 的字节数是一个小常数」：若将来有人把 108 个工具
// 又塞回 Available()，这条会红。
func TestMetaToolConstantSize(t *testing.T) {
	blob, err := json.Marshal(assistantMetaToolSpecs())
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	// 当前实测约 1.5 KB。上限取 4 KB：给措辞调整留余量，但远小于
	// 「全量 108 工具」的 45 KB，也小于原 13 工具的 4.3 KB。
	if len(blob) > 4096 {
		t.Fatalf("元工具 schema 过大（%d 字节），渐进式披露可能被退回成全量下发", len(blob))
	}
	t.Logf("元工具 schema = %d 字节（全量 108 工具为 45088 字节）", len(blob))
}

// TestSearchAssistantTools_LooseMatching 搜索要宽松：名字或用途命中即可。
func TestSearchAssistantTools_LooseMatching(t *testing.T) {
	catalog := []AssistantToolDescriptor{
		{Name: "instance_start", Summary: "启动实例", Category: "instance"},
		{Name: "instance_stop", Summary: "停止实例", Category: "instance"},
		{Name: "file_read_text", Summary: "读取实例内文本文件", Category: "file"},
		{Name: "bot_list", Summary: "列出 Bot", Category: "bot"},
	}

	t.Run("按名字命中", func(t *testing.T) {
		got := searchAssistantTools(catalog, "start", "")
		if len(got) != 1 || got[0].Name != "instance_start" {
			t.Fatalf("应只命中 instance_start，实际 %+v", got)
		}
	})
	t.Run("按用途命中", func(t *testing.T) {
		// 「文件」不出现在 file_read_text 的名字里，只能靠用途命中。
		got := searchAssistantTools(catalog, "文件", "")
		if len(got) != 1 || got[0].Name != "file_read_text" {
			t.Fatalf("应按用途命中 file_read_text，实际 %+v", got)
		}
	})
	t.Run("按分类过滤", func(t *testing.T) {
		got := searchAssistantTools(catalog, "", "instance")
		if len(got) != 2 {
			t.Fatalf("instance 分类应有 2 个，实际 %d", len(got))
		}
	})
	t.Run("空关键词返回全部并排序", func(t *testing.T) {
		got := searchAssistantTools(catalog, "", "")
		if len(got) != len(catalog) {
			t.Fatalf("空关键词应返回全部，实际 %d", len(got))
		}
		for i := 1; i < len(got); i++ {
			if got[i-1].Name > got[i].Name {
				t.Fatal("结果必须按名字升序（稳定输出便于模型与缓存）")
			}
		}
	})
}

// TestSearchAssistantTools_NoMatchStillHelpful 无匹配时不能只回空。
//
// 模型收到纯空结果通常会放弃使用工具、转而凭记忆作答；给出「换个关键词」的
// 提示能显著提高它再试一次的概率。
func TestSearchAssistantTools_NoMatchStillHelpful(t *testing.T) {
	// 注入一个假目录：不注入时会走「目录不可用」分支，测不到搜索行为。
	SetAssistantToolCatalog(func() []AssistantToolDescriptor {
		return []AssistantToolDescriptor{{Name: "instance_start", Summary: "启动实例", Category: "instance"}}
	})
	t.Cleanup(func() { SetAssistantToolCatalog(nil) })

	exec := &assistantDelegatedExecutor{svc: &AssistantChatService{}}
	out := exec.metaSearchTools(`{"keyword":"绝对不存在的关键词zzz"}`)
	if !strings.Contains(out, "没有匹配") {
		t.Fatalf("应说明没有匹配，实际 %q", out)
	}
	if !strings.Contains(out, "换关键词") {
		t.Fatalf("应给出下一步建议，实际 %q", out)
	}
}

// TestFormatPageRef_OnlyReference 页面上下文只带引用，不带名称/状态。
func TestFormatPageRef_OnlyReference(t *testing.T) {
	snap := `{"page":"instances","entity":"instance","entityId":"42","tab":"console"}`
	run := &model.AssistantRun{ContextSnapshot: &snap}

	out := formatPageRef(run)
	if out == "" {
		t.Fatal("有上下文时应产出引用标记")
	}
	if !strings.Contains(out, "instance#42") {
		t.Fatalf("应包含实体引用，实际 %q", out)
	}
	if !strings.Contains(out, "只是引用") {
		t.Fatalf("必须写明这是引用、需用工具查详情，实际 %q", out)
	}
	// 关键：不得出现任何服务端内容的痕迹（这些只能通过工具查）。
	for _, forbidden := range []string{"running", "cpu", "nodeId", "STOPPED"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("引用标记不得携带内容字段 %q：%s", forbidden, out)
		}
	}
}

// TestFormatPageRef_EmptyWhenAbsent 无上下文时不得产出「页面：（空）」这种噪声。
func TestFormatPageRef_EmptyWhenAbsent(t *testing.T) {
	if got := formatPageRef(nil); got != "" {
		t.Fatalf("nil run 应返回空串，实际 %q", got)
	}
	empty := ""
	run := &model.AssistantRun{ContextSnapshot: &empty}
	if got := formatPageRef(run); got != "" {
		t.Fatalf("空快照应返回空串，实际 %q", got)
	}
}

// TestMetaCallTool_RejectsMetaRecursion 元工具不得通过 call_tool 递归调用自身。
func TestMetaCallTool_RejectsMetaRecursion(t *testing.T) {
	// 注入一个恒返回 nil 的执行器：本用例只验证「递归被拒」发生在调用之前，
	// 不依赖全局是否真有 MCP 装配（否则全包跑时会被其它用例的注入状态影响）。
	SetAssistantToolExecutor(func(context.Context, uint, string, map[string]any) (string, error) {
		t.Fatal("元工具递归必须在下发前被拒，不得调用执行器")
		return "", nil
	})
	t.Cleanup(func() { SetAssistantToolExecutor(nil) })

	exec := &assistantDelegatedExecutor{svc: &AssistantChatService{}}
	res := exec.metaCallTool(t.Context(), 1, ai.ToolCall{Args: `{"name":"system_call_tool","arguments":{}}`})
	if !strings.Contains(res.Result, "不能通过 call_tool 调用元工具") {
		t.Fatalf("应拒绝调用元工具自身，实际 %q", res.Result)
	}
}

// TestMetaCallTool_RequiresObjectArguments arguments 必须是对象。
func TestMetaCallTool_RequiresObjectArguments(t *testing.T) {
	SetAssistantToolExecutor(func(context.Context, uint, string, map[string]any) (string, error) {
		t.Fatal("arguments 非法时必须在下发前拒绝")
		return "", nil
	})
	t.Cleanup(func() { SetAssistantToolExecutor(nil) })

	exec := &assistantDelegatedExecutor{svc: &AssistantChatService{}}
	res := exec.metaCallTool(t.Context(), 1, ai.ToolCall{Args: `{"name":"instance_start","arguments":"不是对象"}`})
	if !strings.Contains(res.Result, "必须是对象") {
		t.Fatalf("应拒绝非对象 arguments，实际 %q", res.Result)
	}
}
