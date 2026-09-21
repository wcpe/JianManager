package mcp

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// FR-440 实例标签 MCP 工具测试。
//
// 覆盖三层：① 工具注册与能力面（含 V1 不扩权）；② 三态语义（缺省/空数组/非空）；
// ③ 严格入参解析（错类型显式失败而非静默丢弃）。

func newTagsToolDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Instance{}))
	return db
}

// tagsV2Principal 持 instance.write 且有节点 scope（CanDiscover 需要潜在可达范围）。
func tagsV2Principal() *service.AgentPrincipal {
	return &service.AgentPrincipal{
		PolicyVersion: service.AgentPolicyVersionV2,
		ScopedNodeIDs: []uint{1},
		Capabilities:  []string{service.AgentCapabilityInstanceWrite, service.AgentCapabilityInstanceRead},
	}
}

// tagsReadOnlyPrincipal 只持 instance.read。
func tagsReadOnlyPrincipal() *service.AgentPrincipal {
	return &service.AgentPrincipal{
		PolicyVersion: service.AgentPolicyVersionV2,
		ScopedNodeIDs: []uint{1},
		Capabilities:  []string{service.AgentCapabilityInstanceRead},
	}
}

func seedTagsInstance(t *testing.T, db *gorm.DB, id uint, tags string) {
	t.Helper()
	require.NoError(t, db.Create(&model.Instance{
		ID: id, NodeID: 1, Name: "tags-test", Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDaemon,
		Status: model.InstanceStatusStopped, StartCommand: "./dummy", Tags: tags,
	}).Error)
}

// TestInstanceTags_RegisteredWithExpectedCapabilities 工具已注册、能力面正确、V1 不因扩容扩权。
func TestInstanceTags_RegisteredWithExpectedCapabilities(t *testing.T) {
	byName := map[string]toolSpec{}
	for _, spec := range allToolSpecs {
		byName[spec.Def.Name] = spec
	}

	spec, ok := byName["instance_update_tags"]
	require.True(t, ok, "应注册 instance_update_tags")
	require.Equal(t, service.AgentActionInstanceUpdateTags, spec.Action)

	d, ok := service.DescribeAgentAction(service.AgentActionInstanceUpdateTags)
	require.True(t, ok, "动作应进 catalog")
	require.Equal(t, service.AgentCapabilityInstanceWrite, d.V2Capability, "写走 instance.write（比 configure 更轻）")
	require.Equal(t, service.AgentResourceInstance, d.ResourceType)
	require.Equal(t, service.AgentOperationWrite, d.Operation)
	// 新增的 MCP 专用动作不得顺带扩 V1 或进 HTTP 契约。
	require.False(t, d.V1Allowed, "V1Allowed 应为 false")
	require.False(t, d.HTTPInContract, "不进 HTTP 契约投影")
}

// TestInstanceTags_VisibleByCapability 按能力裁剪：写能力可见、只读不可见。
func TestInstanceTags_VisibleByCapability(t *testing.T) {
	names := func(p *service.AgentPrincipal) map[string]bool {
		out := map[string]bool{}
		for _, def := range ToolsForPrincipal(p) {
			out[def.Name] = true
		}
		return out
	}
	require.True(t, names(tagsV2Principal())["instance_update_tags"], "instance.write 持有者应可见")
	require.False(t, names(tagsReadOnlyPrincipal())["instance_update_tags"], "只读主体不应可见写工具")
}

// TestInstanceTags_ThreeStateSemantics 三态：非空覆盖 / 空数组清空。
// 缺省与非数组入参属拒绝路径，见 TestInstanceTags_RejectsBadArgs。
func TestInstanceTags_ThreeStateSemantics(t *testing.T) {
	db := newTagsToolDB(t)
	svc := service.NewInstanceService(db, nil, nil)
	deps := ToolDeps{Instance: svc, Agent: service.NewAgentTokenService(db)}
	p := tagsV2Principal()
	ctx := context.Background()

	seedTagsInstance(t, db, 1, `["env:prod"]`)

	// 非空数组 → 整体覆盖。
	res := execInstanceUpdateTags(ctx, deps, p, service.AgentActionInstanceUpdateTags, map[string]any{
		"id": 1, "tags": []any{"region:r1", "zone:z1"},
	})
	require.False(t, res.IsError, "非空数组应成功: %v", res.Content)
	var got model.Instance
	require.NoError(t, db.First(&got, 1).Error)
	require.JSONEq(t, `["region:r1","zone:z1"]`, got.Tags, "应整体覆盖而非追加")

	// 空数组 → 清空。
	res = execInstanceUpdateTags(ctx, deps, p, service.AgentActionInstanceUpdateTags, map[string]any{
		"id": 1, "tags": []any{},
	})
	require.False(t, res.IsError, "空数组应成功（清空）: %v", res.Content)
	require.NoError(t, db.First(&got, 1).Error)
	require.Contains(t, []string{"", "[]", "null"}, got.Tags, "空数组应清空标签")
}

// TestInstanceTags_RejectsBadArgs 拒绝路径：缺 tags / tags=null / 元素非字符串 / 非数组。
func TestInstanceTags_RejectsBadArgs(t *testing.T) {
	db := newTagsToolDB(t)
	svc := service.NewInstanceService(db, nil, nil)
	deps := ToolDeps{Instance: svc, Agent: service.NewAgentTokenService(db)}
	p := tagsV2Principal()
	ctx := context.Background()
	seedTagsInstance(t, db, 1, `["env:prod"]`)

	cases := []struct {
		name string
		args map[string]any
	}{
		{"缺 tags", map[string]any{"id": 1}},
		{"tags 为 null", map[string]any{"id": 1, "tags": nil}},
		{"tags 为单个字符串", map[string]any{"id": 1, "tags": "region:r1"}},
		{"tags 数组含非字符串", map[string]any{"id": 1, "tags": []any{"a", 123}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := execInstanceUpdateTags(ctx, deps, p, service.AgentActionInstanceUpdateTags, tc.args)
			require.True(t, res.IsError, "应拒绝: %s", tc.name)
		})
	}

	// 拒绝路径不得改动标签（严格解析在写库之前）。
	var got model.Instance
	require.NoError(t, db.First(&got, 1).Error)
	require.JSONEq(t, `["env:prod"]`, got.Tags, "拒绝路径应保持原标签不变")
}

// TestInstanceTags_CrossScopeRejected 越 scope 目标被拒。
func TestInstanceTags_CrossScopeRejected(t *testing.T) {
	db := newTagsToolDB(t)
	svc := service.NewInstanceService(db, nil, nil)
	deps := ToolDeps{Instance: svc, Agent: service.NewAgentTokenService(db)}
	ctx := context.Background()

	// 实例挂在 node 2，而 principal 的 scope 只有 node 1。
	require.NoError(t, db.Create(&model.Instance{
		ID: 9, NodeID: 2, Name: "other-node", Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDaemon,
		Status: model.InstanceStatusStopped, StartCommand: "./dummy",
	}).Error)

	res := execInstanceUpdateTags(ctx, deps, tagsV2Principal(), service.AgentActionInstanceUpdateTags,
		map[string]any{"id": 9, "tags": []any{"a:b"}})
	require.True(t, res.IsError, "越 scope 应拒绝")
}

// TestToStringSliceStrict 严格解析：合规输入通过，非法输入显式报错（不静默丢弃）。
func TestToStringSliceStrict(t *testing.T) {
	got, err := toStringSliceStrict([]any{"a", "b"})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, got)

	got, err = toStringSliceStrict([]string{"x"})
	require.NoError(t, err)
	require.Equal(t, []string{"x"}, got)

	_, err = toStringSliceStrict("not-an-array")
	require.Error(t, err)
	_, err = toStringSliceStrict([]any{1})
	require.Error(t, err)
	_, err = toStringSliceStrict(nil)
	require.Error(t, err)

	// 空数组是合法输入（表示清空），不是错误。
	got, err = toStringSliceStrict([]any{})
	require.NoError(t, err)
	require.Empty(t, got)
}
