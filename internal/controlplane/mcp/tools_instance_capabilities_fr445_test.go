package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// FR-445 回归：MCP `agent_get_instance` 必须与 HTTP GET /instances/:id 同口径，
// 在返回实例详情时携带 `capabilities` 能力画像。
//
// 第一轮把 AttachCapabilities 移出 ResolveInstanceTarget 后，只有 HTTP 路径补回了画像，
// MCP 路径直接 toolOK(inst)，导致 agent_get_instance 不再下发 capabilities。
func TestAgentGetInstance_AttachesCapabilities_FR445(t *testing.T) {
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
	// 用后端子服（minecraft_java/backend）：其画像与 generic 回退明显不同，
	// 便于断言这里是按 (type, role) 现算，而非通用兜底。
	inst := &model.Instance{
		NodeID: node.ID, Name: "backend", Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDirect,
		StartCommand: "x", Status: model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(inst).Error)

	agentSvc := service.NewAgentTokenService(db)
	_, plain, err := agentSvc.Issue(service.IssueAgentTokenRequest{
		Name:                 "cap",
		ScopedInstanceIDs:    []uint{inst.ID},
		PolicyVersion:        service.AgentPolicyVersionV2,
		CapabilitiesProvided: true,
		Capabilities:         []string{service.AgentCapabilityInstanceRead},
		CreatedBy:            1,
	})
	require.NoError(t, err)
	p, err := agentSvc.Authenticate(plain)
	require.NoError(t, err)

	res := CallTool(context.Background(), ToolDeps{Agent: agentSvc}, p, "agent_get_instance", map[string]any{"id": float64(inst.ID)})
	require.False(t, res.IsError, res.Content[0].Text)

	var payload struct {
		ID           uint `json:"id"`
		Type         string
		Role         string
		Capabilities *model.InstanceCapabilityProfile `json:"capabilities"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].Text), &payload))
	require.NotNil(t, payload.Capabilities, "MCP agent_get_instance 响应必须携带 capabilities 画像")
	assert.Equal(t, string(model.InstanceRoleBackend), payload.Capabilities.Role)
	assert.Equal(t, string(model.InstanceTypeMinecraftJava), payload.Capabilities.Type)
	// backend 画像的专有能力：metrics 只在 MC 世界语义实例上出现，证明是现算画像而非通用回退。
	assert.Contains(t, payload.Capabilities.Capabilities, model.CapMetrics)
	assert.True(t, payload.Capabilities.MCSemantics)
}
