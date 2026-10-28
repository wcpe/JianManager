package service

import (
	"testing"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/ai"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

/**
 * FR-516 工具覆盖度的守门用例。
 *
 * 这组断言守的是**「登记了就要能执行」**：此前目录里登记了契约内 action，
 * 但执行层只有 3 个受限只读实现，其余一律在运行期报「尚未接入」。
 * 那种缺口不会被发现——模型只会换个工具再试，用户看到的是「助手不会用这些工具」。
 *
 * 因此这里断言的是**目录与实现的一一对应**，而不是逐个工具的行为。
 */

func TestResolveToolTarget_NoTargetTypesAccepted(t *testing.T) {
	ex := &assistantDelegatedExecutor{svc: &AssistantChatService{}}
	for _, rt := range []string{"", "platform", AgentResourceNone} {
		tt, tid, err := ex.resolveToolTarget(nil, &AssistantToolAction{ResourceType: rt}, nil)
		if err != nil {
			t.Fatalf("资源类型 %q 应被接受，却报错: %v", rt, err)
		}
		if tt != "" || tid != "" {
			t.Fatalf("资源类型 %q 不该产出目标指纹，得到 %q/%q", rt, tt, tid)
		}
	}
	// 反向校验：未知类型仍必须被拒绝（不能把上面的放宽变成「什么都放行」）。
	if _, _, err := ex.resolveToolTarget(nil, &AssistantToolAction{ResourceType: "spaceship"}, nil); err == nil {
		t.Fatal("未知资源类型应被拒绝")
	}
}

// TestAssistantTools_RegisteredToolsImplemented 已删除（FR-544）。
//
// 它断言「本地注册表里每个工具在 invokeReadTool/invokeWriteTool 里都有实现分支」。
// 现在助手不再自己执行业务工具：`call_tool` 直接把调用转给 MCP 的 CallTool，
// 那条路径的「登记即实现」由 mcp 包自己的覆盖度用例守着（TestRegistry*）。
// 保留一条助手侧的等价断言反而会锁死已经删掉的实现路径。

// TestAssistantTools_TargetArgsMatchSchema 目标参数字段名在 schema 与解析器之间必须一致。
//
// 这是本轮修掉的真实缺陷：schema 曾教模型把参数放进 `params` 子对象，
// 而执行器从顶层读 `instanceId`/`nodeId`。模型照 schema 传参 → 执行器拿到空参 →
// 目标校验退化成「无特定目标」，写操作还可能对错目标出票。
func TestAssistantTools_TargetArgsMatchSchema(t *testing.T) {
	// 数据源改为 MCP 目录（经注入）：助手侧已无本地工具定义。
	//
	// 这条守的仍是那个真实缺陷：schema 教模型传 `params` 子对象、而执行器从顶层读
	// `instanceId`/`nodeId`，模型照 schema 传参就会拿到空参，写操作可能对错目标出票。
	// 现在 schema 来自 MCP，所以要断言的是「MCP 目录里的目标参数与解析器一致」。
	injectTestMCPCatalog(t)
	checked := 0
	for _, tool := range testRegistryTools() {
		detail, ok := assistantToolDetail(tool.Name)
		if !ok {
			t.Fatalf("工具 %s 在目录里却没有详情", tool.Name)
		}
		arg := testTargetArgOf(tool.ResourceType)
		if arg == "" {
			continue
		}
		checked++
		props, _ := detail.Parameters["properties"].(map[string]any)
		if _, ok := props[arg]; !ok {
			t.Fatalf("工具 %s 的 schema 缺少目标参数 %q（属性为 %v）", tool.Name, arg, props)
		}
		req, _ := detail.Parameters["required"].([]string)
		found := false
		for _, r := range req {
			if r == arg {
				found = true
			}
		}
		if !found {
			t.Fatalf("工具 %s 的目标参数 %q 未标为必填（required=%v）", tool.Name, arg, req)
		}
	}
	// 反向校验：不能因为过滤条件过宽而一条都没检查。
	if checked == 0 {
		t.Fatal("没有任何工具被检查，断言失去意义")
	}
}

// TestAssistantTools_NoWrappedParamsSchema 内置工具的 schema 不得再出现 {action, params} 包裹。
//
// 保留这条是为了让「参数平铺」成为显式契约：一旦有人照着旧写法加回包裹结构，
// 上面那条一致性断言未必能抓到（包裹里也可能塞一个同名属性），这条能。
func TestAssistantTools_NoWrappedParamsSchema(t *testing.T) {
	injectTestMCPCatalog(t)
	for _, tool := range testRegistryTools() {
		if isAssistantExternalTool(tool.Name) {
			continue
		}
		props, _ := tool.InputSchema["properties"].(map[string]any)
		if _, bad := props["params"]; bad {
			t.Fatalf("工具 %s 的 schema 又出现了 params 包裹结构", tool.Name)
		}
		if _, bad := props["action"]; bad {
			t.Fatalf("工具 %s 的 schema 又要求模型自己填 action（工具名已确定动作）", tool.Name)
		}
	}
}

func setupAssistantTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newAssistantChatTestDB(t)
	if err := db.AutoMigrate(&model.Node{}, &model.Instance{}, &model.GroupInstance{}); err != nil {
		t.Fatalf("迁移实体表失败: %v", err)
	}
	return db
}

// seedAssistantNode 建一个节点。
func seedAssistantNode(t *testing.T, db *gorm.DB, uuid string) *model.Node {
	t.Helper()
	n := &model.Node{UUID: uuid, Name: "node-" + uuid, Host: "10.0.0.1",
		GRPCPort: 9000, WSPort: 9001, Secret: "s", Status: model.NodeStatusOnline}
	if err := db.Create(n).Error; err != nil {
		t.Fatalf("建节点失败: %v", err)
	}
	return n
}

// seedAssistantInstance 建一个实例。
func seedAssistantInstance(t *testing.T, db *gorm.DB, nodeUUID string, status model.InstanceStatus) *model.Instance {
	t.Helper()
	var node model.Node
	if err := db.Where("uuid = ?", nodeUUID).First(&node).Error; err != nil {
		t.Fatalf("节点不存在: %v", err)
	}
	in := &model.Instance{
		UUID: "inst-" + nodeUUID, Name: "inst-" + nodeUUID, NodeID: node.ID,
		Status: status, Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleBackend,
		StartCommand: "java -jar server.jar",
	}
	if err := db.Create(in).Error; err != nil {
		t.Fatalf("建实例失败: %v", err)
	}
	return in
}

func mustArgs(t *testing.T, raw string) map[string]any {
	t.Helper()
	m, err := normalizeToolArgs(raw)
	if err != nil {
		t.Fatalf("参数解析失败: %v", err)
	}
	return m
}

// contains 朴素子串判断（避免为一条断言引入 strings 之外的工具）。
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// 占位：确保 ai 包被引用（工具调用类型在断言里用到）。
var _ = ai.ToolCall{}

// injectTestMCPCatalog 注入一份最小的「MCP 目录」供 service 层测试用（FR-544）。
//
// 为什么不直接构造 13 个工具的本地注册表：那份注册表已经删除，
// 工具定义的单一真源是 MCP 目录。service 测试若还要断言目录属性，
// 就必须像生产一样经注入缝拿到数据——否则测的是另一套东西。
func injectTestMCPCatalog(t *testing.T) {
	t.Helper()
	// 两份名字都给：既有用例按 MCP 名（instance_start）查，也有用例按助手侧的
	// 旧命名（system_agent_instance_start）查——后者是历史票据/测试的写法，
	// 迁到 MCP 真源后仍需能查到，否则确认票据会失去元数据。
	catalog := []AssistantToolDescriptor{
		{Name: "instance_start", Summary: "启动实例", Operation: AgentOperationWrite, RequiresConfirm: true, Category: "instance"},
		{Name: assistantToolName(AgentActionInstanceStart), Summary: "启动实例", Operation: AgentOperationWrite, RequiresConfirm: true, Category: "instance"},
		{Name: "node_maintenance_enter", Summary: "节点进入维护", Operation: AgentOperationWrite, RequiresConfirm: true, Category: "node"},
		{Name: assistantToolName(AgentActionNodeMaintenanceEnter), Summary: "节点进入维护", Operation: AgentOperationWrite, RequiresConfirm: true, Category: "node"},
		{Name: "agent_get_instance", Summary: "实例详情", Operation: AgentOperationRead, Category: "instance"},
	}
	SetAssistantToolCatalog(func() []AssistantToolDescriptor { return catalog })
	SetAssistantToolMeta(func(name string) (*AssistantToolAction, bool) {
		for _, d := range catalog {
			if d.Name != name {
				continue
			}
			return &AssistantToolAction{
				Name: d.Name, Action: "agent." + d.Name, Capability: AgentCapabilityInstanceRead,
				ResourceType: d.Category, Operation: d.Operation, RequiresConfirm: d.RequiresConfirm,
			}, true
		}
		return nil, false
	})
	SetAssistantToolDetail(func(name string) (AssistantToolDetail, bool) {
		for _, d := range catalog {
			if d.Name != name {
				continue
			}
			arg := testTargetArgOf(d.Category)
			props := map[string]any{}
			req := []string{}
			if arg != "" {
				props[arg] = map[string]any{"type": "number"}
				req = append(req, arg)
			}
			return AssistantToolDetail{
				AssistantToolDescriptor: d,
				Parameters: map[string]any{
					"type":       "object",
					"properties": props,
					"required":   req,
				},
			}, true
		}
		return AssistantToolDetail{}, false
	})
	t.Cleanup(func() {
		SetAssistantToolCatalog(nil)
		SetAssistantToolMeta(nil)
		SetAssistantToolDetail(nil)
	})
}

// testTargetArgOf 目标类型 → 该类型的目标参数名（与执行器读取的键一致）。
//
// 原实现 assistantToolTargetArg 依赖被删除的本地注册表；这里按资源类型直接给出，
// 保持断言语义不变：schema 里的必填目标参数必须与执行器读的键同名。
func testTargetArgOf(resourceType string) string {
	switch resourceType {
	case "instance":
		return "instanceId"
	case "node":
		return "nodeId"
	case "bot":
		return "botId"
	default:
		return ""
	}
}

// testRegistryTools 把注入的目录展开成 AssistantToolAction（供属性断言用）。
func testRegistryTools() []AssistantToolAction {
	out := make([]AssistantToolAction, 0, 8)
	for _, d := range assistantToolCatalog() {
		if meta, ok := assistantToolMeta(d.Name); ok {
			out = append(out, *meta)
		}
	}
	return out
}
