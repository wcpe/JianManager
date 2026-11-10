package service

// FR-544 渐进式披露验收：量化常驻体积，并验证「与工具总数无关」的真实边界。
//
// 已有 assistant_tool_disclosure_test.go 守了「三个元工具」「≤4KB」与搜索行为。
// 本文件补三件它没盖的：
//  1. **实测**常驻 schema 字节数与占比（不是看上界，而是看真实值）
//  2. 「与工具总数无关」的**正面证明**：改目录规模不影响元工具体积
//  3. **边界如实登记**：外部 MCP 工具是**全量下发**的，不受渐进披露约束

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/wcpe/JianManager/internal/controlplane/ai"
)

// TestMetaToolSize_MeasuredAgainstFullCatalog 实测常驻体积与全量目录的比值。
//
// 这是 FR-544 声称的「1350 字节 vs 45088 字节 = 3%」的直接验证。
// 数字不写死：目录规模会变，比值才是意义所在。
func TestMetaToolSize_MeasuredAgainstFullCatalog(t *testing.T) {
	metaBlob, err := json.Marshal(assistantMetaToolSpecs())
	if err != nil {
		t.Fatalf("序列化元工具失败: %v", err)
	}

	// 构造一个接近真实规模的目录：用 108 个工具（与 spec 记的口径一致）
	const toolCount = 108
	full := make([]ai.ToolSpec, 0, toolCount)
	for i := 0; i < toolCount; i++ {
		full = append(full, ai.ToolSpec{
			Name:        fmt.Sprintf("mock_tool_%03d", i),
			Description: "这是一个用于测量全量目录体积的占位工具描述，长度接近真实工具",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"instanceId":{"type":"integer"},"reason":{"type":"string"}},"required":["instanceId"]}`),
		})
	}
	fullBlob, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("序列化全量目录失败: %v", err)
	}

	ratio := float64(len(metaBlob)) / float64(len(fullBlob)) * 100
	t.Logf("常驻 %d 字节 / 全量 %d 工具 %d 字节 = %.1f%%",
		len(metaBlob), toolCount, len(fullBlob), ratio)

	if ratio > 10 {
		t.Fatalf("常驻占比 %.1f%% 过高（应远小于 10%%）——渐进式披露可能被退回成全量下发", ratio)
	}
}

// TestMetaToolSize_IndependentOfCatalog 常驻体积与目录规模无关。
//
// 元工具是**固定三件套**，不读任何目录；改目录规模不应影响它的字节数。
// 这是「与工具总数无关」这个承诺的正面证明。
func TestMetaToolSize_IndependentOfCatalog(t *testing.T) {
	measure := func() int {
		blob, err := json.Marshal(assistantMetaToolSpecs())
		if err != nil {
			t.Fatalf("序列化失败: %v", err)
		}
		return len(blob)
	}

	// 目录为空时
	SetAssistantToolCatalog(func() []AssistantToolDescriptor { return nil })
	empty := measure()

	// 目录很大时（300 个工具）
	big := make([]AssistantToolDescriptor, 0, 300)
	for i := 0; i < 300; i++ {
		big = append(big, AssistantToolDescriptor{
			Name: fmt.Sprintf("tool_%03d", i), Summary: "占位用途", Category: "mock",
		})
	}
	SetAssistantToolCatalog(func() []AssistantToolDescriptor { return big })
	large := measure()
	SetAssistantToolCatalog(nil)

	if empty != large {
		t.Fatalf("常驻体积应恒定：空目录 %d 字节，300 工具目录 %d 字节", empty, large)
	}
	t.Logf("目录 0 个与 300 个工具时，常驻体积均为 %d 字节", empty)
}

// TestExternalTools_AreNotProgressive 如实登记边界：外部 MCP 工具全量下发。
//
// 这是渐进式披露的**适用范围边界**，不是缺陷，但必须写清楚——
// 否则「常驻体积与工具总数无关」会被误读为对所有工具成立。
//
// 为何不用用例钉住：externalToolSpecs 依赖 DB（enabledToolRows），
// 在单元层无法构造真实路径；自己造一个 50 元素切片再断言长度 == 50
// 属于自证，没有验证价值。因此这里只留注释说明，靠代码审阅守。
//
// 事实（已核）：assistant_external_tools.go:170-212 的 externalToolSpecs
// 遍历全部 enabled 工具行逐个转换，**无数量上限**。
