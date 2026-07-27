package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// fr397ReadTools 只读内容工具（instance.read 可见）。
var fr397ReadTools = []string{
	"file_list", "file_check_access", "file_read_text", "file_versions", "file_diff",
	"config_discover", "config_read", "config_cross_check", "config_versions", "config_diff",
	"plugin_list",
}

// fr397ContentTools 内容写工具（instance.content 可见）。
var fr397ContentTools = []string{
	"file_write_text", "file_rename", "file_chmod", "file_delete", "file_rollback",
	"file_issue_transfer_ticket",
	"plugin_deploy_from_asset", "plugin_toggle", "plugin_delete",
}

// fr397ConfigureTools 配置写工具（instance.configure 可见）。
var fr397ConfigureTools = []string{"config_write_text", "config_write_fields", "config_rollback"}

func toolNameSet(defs []ToolDef) map[string]bool {
	out := make(map[string]bool, len(defs))
	for _, d := range defs {
		out[d.Name] = true
	}
	return out
}

func TestFR397_ToolsRegisteredWithSchema(t *testing.T) {
	names := toolNameSet(RegisteredTools())
	all := append(append(append([]string{}, fr397ReadTools...), fr397ContentTools...), fr397ConfigureTools...)
	for _, n := range all {
		assert.True(t, names[n], "应注册工具 %s", n)
	}
	// 范围外能力不得注册
	for _, n := range []string{"file_search", "file_decompile", "file_archive_entries", "plugin_upload", "asset_upload"} {
		assert.False(t, names[n], "范围外工具不得注册: %s", n)
	}
	for _, d := range RegisteredTools() {
		assert.NotEmpty(t, d.Description, "工具 %s 缺描述", d.Name)
		assert.NotNil(t, d.InputSchema, "工具 %s 缺 InputSchema", d.Name)
	}
}

func TestFR397_ToolsForPrincipal_CapabilityMatrix(t *testing.T) {
	readOnly := &service.AgentPrincipal{
		PolicyVersion:     service.AgentPolicyVersionV2,
		ScopedInstanceIDs: []uint{1},
		Capabilities:      []string{service.AgentCapabilityInstanceRead},
	}
	names := toolNameSet(ToolsForPrincipal(readOnly))
	for _, n := range fr397ReadTools {
		assert.True(t, names[n], "instance.read 应可见 %s", n)
	}
	for _, n := range append(append([]string{}, fr397ContentTools...), fr397ConfigureTools...) {
		assert.False(t, names[n], "instance.read 不应可见写类 %s", n)
	}

	content := &service.AgentPrincipal{
		PolicyVersion:     service.AgentPolicyVersionV2,
		ScopedInstanceIDs: []uint{1},
		Capabilities:      []string{service.AgentCapabilityInstanceContent},
	}
	names = toolNameSet(ToolsForPrincipal(content))
	for _, n := range fr397ContentTools {
		assert.True(t, names[n], "instance.content 应可见 %s", n)
	}
	for _, n := range fr397ConfigureTools {
		assert.False(t, names[n], "instance.content 不应可见 %s", n)
	}

	configure := &service.AgentPrincipal{
		PolicyVersion:     service.AgentPolicyVersionV2,
		ScopedInstanceIDs: []uint{1},
		Capabilities:      []string{service.AgentCapabilityInstanceConfigure},
	}
	names = toolNameSet(ToolsForPrincipal(configure))
	for _, n := range fr397ConfigureTools {
		assert.True(t, names[n], "instance.configure 应可见 %s", n)
	}
	for _, n := range fr397ContentTools {
		assert.False(t, names[n], "instance.configure 不应可见 %s", n)
	}

	// V1 Token 永不可见内容域工具
	v1 := &service.AgentPrincipal{
		PolicyVersion:     service.AgentPolicyVersionV1,
		ScopedInstanceIDs: []uint{1},
		ScopedNodeIDs:     []uint{1},
		WriteAllowlist:    []string{service.AgentWriteInstanceLife, service.AgentWriteNodeMaintenance},
	}
	names = toolNameSet(ToolsForPrincipal(v1))
	for _, n := range append(append(append([]string{}, fr397ReadTools...), fr397ContentTools...), fr397ConfigureTools...) {
		assert.False(t, names[n], "V1 Token 不应可见 %s", n)
	}
}

// setupContentToolsDeps 建库 + 实例 + V2 Token，返回可授权的 deps 与 principal。
func setupContentToolsDeps(t *testing.T) (ToolDeps, *service.AgentPrincipal, uint) {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.AgentToken{}, &model.Instance{}, &model.Node{}))
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil && sqlDB != nil {
			_ = sqlDB.Close()
		}
	})

	node := &model.Node{Name: "n", Host: "127.0.0.1", GRPCPort: 9100, WSPort: 9101, Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{NodeID: node.ID, Name: "i", Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, StartCommand: "x", Status: model.InstanceStatusStopped}
	require.NoError(t, db.Create(inst).Error)

	agentSvc := service.NewAgentTokenService(db)
	_, plain, err := agentSvc.Issue(service.IssueAgentTokenRequest{
		Name:                 "content",
		ScopedInstanceIDs:    []uint{inst.ID},
		PolicyVersion:        service.AgentPolicyVersionV2,
		CapabilitiesProvided: true,
		Capabilities: []string{
			service.AgentCapabilityInstanceRead,
			service.AgentCapabilityInstanceContent,
			service.AgentCapabilityInstanceConfigure,
		},
		CreatedBy: 1,
	})
	require.NoError(t, err)
	p, err := agentSvc.Authenticate(plain)
	require.NoError(t, err)
	return ToolDeps{Agent: agentSvc}, p, inst.ID
}

func TestFR397_TextSizeGuard(t *testing.T) {
	deps, p, instID := setupContentToolsDeps(t)
	oversize := strings.Repeat("a", mcpTextContentMaxBytes+1)

	res := CallTool(context.Background(), deps, p, "file_write_text", map[string]any{
		"id": float64(instID), "path": "server.properties", "content": oversize,
	})
	require.True(t, res.IsError, "超限文本必须拒绝")
	assert.Contains(t, res.Content[0].Text, "512KiB")
	assert.Contains(t, res.Content[0].Text, "file_issue_transfer_ticket")

	res = CallTool(context.Background(), deps, p, "config_write_text", map[string]any{
		"id": float64(instID), "path": "server.properties", "content": oversize,
	})
	require.True(t, res.IsError, "配置超限文本必须拒绝")
	assert.Contains(t, res.Content[0].Text, "512KiB")
}

func TestFR397_BinaryReadRejected(t *testing.T) {
	// 二进制内容不得经 MCP 返回，须引导票据下载。
	res := buildReadTextResult("plugins/foo.jar", []byte("PK\x03\x04\x00\x00binary"))
	require.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "二进制")
	assert.Contains(t, res.Content[0].Text, "file_issue_transfer_ticket")

	// 超限文本同样引导票据
	res = buildReadTextResult("big.txt", []byte(strings.Repeat("a", mcpTextContentMaxBytes+1)))
	require.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "512KiB")

	// 正常 UTF-8 文本放行
	res = buildReadTextResult("server.properties", []byte("motd=你好世界\n"))
	require.False(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "你好世界")
}

func TestFR397_DangerConfirm(t *testing.T) {
	deps, p, instID := setupContentToolsDeps(t)

	// 缺确认
	res := CallTool(context.Background(), deps, p, "file_delete", map[string]any{
		"id": float64(instID), "path": "world/level.dat",
	})
	require.True(t, res.IsError, "缺 confirmPath 必须拒绝")
	assert.Contains(t, res.Content[0].Text, "confirmPath")

	// 确认不匹配
	res = CallTool(context.Background(), deps, p, "file_delete", map[string]any{
		"id": float64(instID), "path": "world/level.dat", "confirmPath": "world/level.dat.bak",
	})
	require.True(t, res.IsError, "confirmPath 不匹配必须拒绝")
	assert.Contains(t, res.Content[0].Text, "confirmPath")

	res = CallTool(context.Background(), deps, p, "plugin_delete", map[string]any{
		"id": float64(instID), "name": "Essentials.jar",
	})
	require.True(t, res.IsError, "缺 confirmName 必须拒绝")
	assert.Contains(t, res.Content[0].Text, "confirmName")

	res = CallTool(context.Background(), deps, p, "plugin_delete", map[string]any{
		"id": float64(instID), "name": "Essentials.jar", "confirmName": "Other.jar",
	})
	require.True(t, res.IsError, "confirmName 不匹配必须拒绝")
	assert.Contains(t, res.Content[0].Text, "confirmName")
}

func TestFR397_PathTraversalRejected(t *testing.T) {
	deps, p, instID := setupContentToolsDeps(t)
	for _, name := range []string{"file_list", "file_read_text"} {
		res := CallTool(context.Background(), deps, p, name, map[string]any{
			"id": float64(instID), "path": "../../etc/passwd",
		})
		require.True(t, res.IsError, "%s 应拒绝路径穿越", name)
		assert.Contains(t, res.Content[0].Text, "..")
	}
}

func TestFR397_RequiredPathRejectsWorkDirRoot(t *testing.T) {
	deps, p, instID := setupContentToolsDeps(t)
	for _, path := range []string{"", ".", "./", ".\\"} {
		res := CallTool(context.Background(), deps, p, "file_read_text", map[string]any{
			"id": float64(instID), "path": path,
		})
		require.True(t, res.IsError, "路径 %q 必须拒绝", path)
		assert.Contains(t, res.Content[0].Text, "工作目录根")
	}
	res := CallTool(context.Background(), deps, p, "file_rename", map[string]any{
		"id": float64(instID), "oldPath": ".", "newPath": "next",
	})
	require.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "工作目录根")
}

func TestFR397_ConfigToolsRejectNonConfigPath(t *testing.T) {
	deps, p, instID := setupContentToolsDeps(t)
	res := CallTool(context.Background(), deps, p, "config_read", map[string]any{
		"id": float64(instID), "path": "plugins/unsafe.jar",
	})
	require.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "配置文件")
}

func TestFR397_ScopeOutsideConverges(t *testing.T) {
	deps, p, instID := setupContentToolsDeps(t)
	res := CallTool(context.Background(), deps, p, "file_list", map[string]any{
		"id": float64(instID + 999), "path": "",
	})
	require.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "拒绝")
}

func TestFR397_TicketToolRequiresTransferService(t *testing.T) {
	deps, p, instID := setupContentToolsDeps(t)
	res := CallTool(context.Background(), deps, p, "file_issue_transfer_ticket", map[string]any{
		"id": float64(instID), "direction": "upload", "path": "plugins/foo.jar",
	})
	require.True(t, res.IsError, "未装配票据服务时应返回中文不可用提示")
	assert.Contains(t, res.Content[0].Text, "票据")
}
