package router

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// FR-395：V1 无扩权 / V2 能力 / 节点 scope 继承实例 的 HTTP 门禁矩阵。

// TestAgentCapability_V1_NodeScopeDoesNotInheritInstances V1 Token 的节点 scope 不授予该节点实例权限。
func TestAgentCapability_V1_NodeScopeDoesNotInheritInstances(t *testing.T) {
	db, r, adminJWT, node, inst := setupAgentGate(t)
	sameNode := &model.Instance{
		NodeID: node.ID, Name: "v1-same-node", Type: model.InstanceTypeGeneric,
		ProcessType: model.ProcessTypeDirect, StartCommand: "x", Status: model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(sameNode).Error)

	// V1：只显式授权 inst，节点 scope 含 node
	_, plain := issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name":              "v1-legacy",
		"scopedInstanceIds": []uint{inst.ID},
		"scopedNodeIds":     []uint{node.ID},
	})

	// 显式实例可读
	w := makeRequest(r, "GET", "/api/v1/agent/instances/"+itoa(inst.ID), nil, plain)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// 同节点但未显式授权的实例仍 403（不继承）
	assertForbiddenAgent(t, r, "GET", "/api/v1/agent/instances/"+itoa(sameNode.ID), plain)
	assertForbiddenAgent(t, r, "POST", "/api/v1/agent/instances/"+itoa(sameNode.ID)+"/start", plain)

	// 列表也不含继承实例
	w = makeRequest(r, "GET", "/api/v1/agent/instances", nil, plain)
	require.Equal(t, http.StatusOK, w.Code)
	var list []model.Instance
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	ids := map[uint]bool{}
	for _, it := range list {
		ids[it.ID] = true
	}
	assert.True(t, ids[inst.ID])
	assert.False(t, ids[sameNode.ID], "V1 不得因节点 scope 出现继承实例")

	// whoami 显示 policyVersion=1
	w = makeRequest(r, "GET", "/api/v1/agent/whoami", nil, plain)
	require.Equal(t, http.StatusOK, w.Code)
	who := parseJSON(t, w)
	assert.Equal(t, float64(service.AgentPolicyVersionV1), who["policyVersion"])
}

// TestAgentCapability_V1_RejectsCapabilities V1 请求不得提交 capabilities。
func TestAgentCapability_V1_RejectsCapabilities(t *testing.T) {
	_, r, adminJWT, _, _ := setupAgentGate(t)
	w := makeRequest(r, "POST", "/api/v1/agent/tokens", map[string]any{
		"name":         "v1-with-caps",
		"capabilities": []string{service.AgentCapabilityInstanceRead},
	}, adminJWT)
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

// TestAgentCapability_V2_RequiresCapabilitiesAndRejectsWriteAllowlist V2 字段互斥与必填。
func TestAgentCapability_V2_RequiresCapabilitiesAndRejectsWriteAllowlist(t *testing.T) {
	_, r, adminJWT, _, _ := setupAgentGate(t)

	// 缺 capabilities
	w := makeRequest(r, "POST", "/api/v1/agent/tokens", map[string]any{
		"name":          "v2-missing",
		"policyVersion": 2,
	}, adminJWT)
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	// 与 writeAllowlist 混用
	w = makeRequest(r, "POST", "/api/v1/agent/tokens", map[string]any{
		"name":           "v2-mixed",
		"policyVersion":  2,
		"capabilities":   []string{service.AgentCapabilityInstanceRead},
		"writeAllowlist": []string{service.AgentWriteInstanceLife},
	}, adminJWT)
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	// 未知 capability
	w = makeRequest(r, "POST", "/api/v1/agent/tokens", map[string]any{
		"name":          "v2-unknown",
		"policyVersion": 2,
		"capabilities":  []string{"instance.superuser"},
	}, adminJWT)
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

// TestAgentCapability_V2_EmptyCapabilities 空能力 V2 Token 除 whoami 外无业务权限。
func TestAgentCapability_V2_EmptyCapabilities(t *testing.T) {
	_, r, adminJWT, node, inst := setupAgentGate(t)
	_, plain := issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name":              "v2-empty",
		"policyVersion":     2,
		"capabilities":      []string{},
		"scopedInstanceIds": []uint{inst.ID},
		"scopedNodeIds":     []uint{node.ID},
	})

	w := makeRequest(r, "GET", "/api/v1/agent/whoami", nil, plain)
	require.Equal(t, http.StatusOK, w.Code)
	who := parseJSON(t, w)
	assert.Equal(t, float64(service.AgentPolicyVersionV2), who["policyVersion"])

	assertForbiddenAgent(t, r, "GET", "/api/v1/agent/instances", plain)
	assertForbiddenAgent(t, r, "GET", "/api/v1/agent/nodes", plain)
	assertForbiddenAgent(t, r, "GET", "/api/v1/agent/instances/"+itoa(inst.ID), plain)
	assertForbiddenAgent(t, r, "POST", "/api/v1/agent/instances/"+itoa(inst.ID)+"/start", plain)
	assertForbiddenAgent(t, r, "POST", "/api/v1/agent/nodes/"+itoa(node.ID)+"/maintenance/enter", plain)
}

// TestAgentCapability_V2_NodeScopeInheritsInstances V2 节点 scope 覆盖当前与新建实例；移出即失权。
func TestAgentCapability_V2_NodeScopeInheritsInstances(t *testing.T) {
	db, r, adminJWT, node, inst := setupAgentGate(t)
	otherNode := createTestNodeWithSuffix(t, db, "v2-other-node")

	// V2：只授权节点 scope，不列任何实例 ID
	_, plain := issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name":          "v2-node-scope",
		"policyVersion": 2,
		"capabilities": []string{
			service.AgentCapabilityInstanceRead,
			service.AgentCapabilityInstanceLife,
		},
		"scopedNodeIds": []uint{node.ID},
	})

	// 该节点已有实例可读
	w := makeRequest(r, "GET", "/api/v1/agent/instances/"+itoa(inst.ID), nil, plain)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// 签发后新建到授权节点的实例自动可见
	future := &model.Instance{
		NodeID: node.ID, Name: "v2-future", Type: model.InstanceTypeGeneric,
		ProcessType: model.ProcessTypeDirect, StartCommand: "x", Status: model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(future).Error)
	w = makeRequest(r, "GET", "/api/v1/agent/instances/"+itoa(future.ID), nil, plain)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	w = makeRequest(r, "GET", "/api/v1/agent/instances", nil, plain)
	require.Equal(t, http.StatusOK, w.Code)
	var list []model.Instance
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	ids := map[uint]bool{}
	for _, it := range list {
		ids[it.ID] = true
	}
	assert.True(t, ids[inst.ID])
	assert.True(t, ids[future.ID], "新建实例应自动纳入节点 scope")

	// 移出授权节点 → 立即失权
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", future.ID).
		Update("node_id", otherNode.ID).Error)
	assertForbiddenAgent(t, r, "GET", "/api/v1/agent/instances/"+itoa(future.ID), plain)

	// 移回 → 恢复
	require.NoError(t, db.Model(&model.Instance{}).Where("id = ?", future.ID).
		Update("node_id", node.ID).Error)
	w = makeRequest(r, "GET", "/api/v1/agent/instances/"+itoa(future.ID), nil, plain)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

// TestAgentCapability_V2_InstanceScopeDoesNotGrantNode 实例 scope 不反向授权节点。
func TestAgentCapability_V2_InstanceScopeDoesNotGrantNode(t *testing.T) {
	_, r, adminJWT, node, inst := setupAgentGate(t)
	_, plain := issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name":          "v2-inst-only",
		"policyVersion": 2,
		"capabilities": []string{
			service.AgentCapabilityInstanceRead,
			service.AgentCapabilityNodeRead,
			service.AgentCapabilityNodeOperate,
		},
		"scopedInstanceIds": []uint{inst.ID},
	})

	// 实例可读
	w := makeRequest(r, "GET", "/api/v1/agent/instances/"+itoa(inst.ID), nil, plain)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// 有 node.read/node.operate 能力但无节点 scope → 403
	assertForbiddenAgent(t, r, "GET", "/api/v1/agent/nodes", plain)
	assertForbiddenAgent(t, r, "POST", "/api/v1/agent/nodes/"+itoa(node.ID)+"/maintenance/enter", plain)
}

// TestAgentCapability_V2_CapabilityWithoutScopeDenied 有能力无 scope 一律拒绝。
func TestAgentCapability_V2_CapabilityWithoutScopeDenied(t *testing.T) {
	_, r, adminJWT, node, inst := setupAgentGate(t)
	_, plain := issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name":          "v2-no-scope",
		"policyVersion": 2,
		"capabilities": []string{
			service.AgentCapabilityInstanceRead,
			service.AgentCapabilityInstanceLife,
			service.AgentCapabilityNodeRead,
		},
	})
	assertForbiddenAgent(t, r, "GET", "/api/v1/agent/instances", plain)
	assertForbiddenAgent(t, r, "GET", "/api/v1/agent/instances/"+itoa(inst.ID), plain)
	assertForbiddenAgent(t, r, "POST", "/api/v1/agent/instances/"+itoa(inst.ID)+"/start", plain)
	assertForbiddenAgent(t, r, "GET", "/api/v1/agent/nodes", plain)
	_ = node
}

// TestAgentCapability_V2_ObservabilityCapabilityRequired 指标需要 observability.read。
func TestAgentCapability_V2_ObservabilityCapabilityRequired(t *testing.T) {
	_, r, adminJWT, _, inst := setupAgentGate(t)

	// 仅 instance.read：详情可读，指标 403
	_, readOnly := issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name":              "v2-read-only",
		"policyVersion":     2,
		"capabilities":      []string{service.AgentCapabilityInstanceRead},
		"scopedInstanceIds": []uint{inst.ID},
	})
	w := makeRequest(r, "GET", "/api/v1/agent/instances/"+itoa(inst.ID), nil, readOnly)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assertForbiddenAgent(t, r, "GET", "/api/v1/agent/instances/"+itoa(inst.ID)+"/metrics", readOnly)
}

// TestAgentCapability_ScopeOutAndMissingConverge scope 外与不存在实例返回同一收敛语义。
func TestAgentCapability_ScopeOutAndMissingConverge(t *testing.T) {
	db, r, adminJWT, node, inst := setupAgentGate(t)
	other := &model.Instance{
		NodeID: createTestNodeWithSuffix(t, db, "converge-node").ID,
		Name:   "converge-out", Type: model.InstanceTypeGeneric,
		ProcessType: model.ProcessTypeDirect, StartCommand: "x", Status: model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(other).Error)

	_, plain := issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name":          "converge",
		"policyVersion": 2,
		"capabilities":  []string{service.AgentCapabilityInstanceRead},
		"scopedNodeIds": []uint{node.ID},
	})
	// 授权节点内可读
	w := makeRequest(r, "GET", "/api/v1/agent/instances/"+itoa(inst.ID), nil, plain)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// scope 外与不存在都为 403，不泄露存在性
	scopeOut := makeRequest(r, "GET", "/api/v1/agent/instances/"+itoa(other.ID), nil, plain)
	missing := makeRequest(r, "GET", "/api/v1/agent/instances/999999", nil, plain)
	assert.Equal(t, http.StatusForbidden, scopeOut.Code, scopeOut.Body.String())
	assert.Equal(t, http.StatusForbidden, missing.Code, missing.Body.String())
	assert.Equal(t, scopeOut.Body.String(), missing.Body.String(), "scope 外与不存在须同一响应")
}

// TestAgentCapability_CallLogRecordsCapability 调用流水记录 capability（V1 legacy / V2 能力）。
func TestAgentCapability_CallLogRecordsCapability(t *testing.T) {
	db, r, adminJWT, node, inst := setupAgentGate(t)

	_, v1 := issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name":              "cap-v1",
		"scopedInstanceIds": []uint{inst.ID},
		"scopedNodeIds":     []uint{node.ID},
	})
	require.Equal(t, http.StatusOK, makeRequest(r, "GET", "/api/v1/agent/instances/"+itoa(inst.ID), nil, v1).Code)

	_, v2 := issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name":          "cap-v2",
		"policyVersion": 2,
		"capabilities":  []string{service.AgentCapabilityInstanceRead},
		"scopedNodeIds": []uint{node.ID},
	})
	require.Equal(t, http.StatusOK, makeRequest(r, "GET", "/api/v1/agent/instances/"+itoa(inst.ID), nil, v2).Code)

	var legacyN, v2N int64
	require.NoError(t, db.Model(&model.AgentCallLog{}).
		Where("capability = ?", service.AgentLegacyCapabilityRead).Count(&legacyN).Error)
	require.NoError(t, db.Model(&model.AgentCallLog{}).
		Where("capability = ?", service.AgentCapabilityInstanceRead).Count(&v2N).Error)
	assert.GreaterOrEqual(t, legacyN, int64(1), "V1 读应记 legacy.read")
	assert.GreaterOrEqual(t, v2N, int64(1), "V2 读应记 instance.read")
}

// TestAgentCapability_TokenListExposesPolicyFields Token 列表投影新增字段且保留旧字段。
func TestAgentCapability_TokenListExposesPolicyFields(t *testing.T) {
	_, r, adminJWT, node, inst := setupAgentGate(t)
	issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name":              "proj-v1",
		"scopedInstanceIds": []uint{inst.ID},
		"scopedNodeIds":     []uint{node.ID},
	})
	issueAgentPlaintext(t, r, adminJWT, map[string]any{
		"name":          "proj-v2",
		"policyVersion": 2,
		"capabilities":  []string{service.AgentCapabilityInstanceRead},
		"scopedNodeIds": []uint{node.ID},
	})

	w := makeRequest(r, "GET", "/api/v1/agent/tokens", nil, adminJWT)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var list []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	require.NotEmpty(t, list)

	byName := map[string]map[string]any{}
	for _, row := range list {
		name, _ := row["name"].(string)
		byName[name] = row
	}
	v1row := byName["proj-v1"]
	require.NotNil(t, v1row)
	assert.Equal(t, float64(service.AgentPolicyVersionV1), v1row["policyVersion"])
	assert.NotNil(t, v1row["writeAllowlist"], "旧字段须保留")
	caps, ok := v1row["capabilities"].([]any)
	require.True(t, ok, "capabilities 须为数组形态")
	assert.Empty(t, caps)

	v2row := byName["proj-v2"]
	require.NotNil(t, v2row)
	assert.Equal(t, float64(service.AgentPolicyVersionV2), v2row["policyVersion"])
	v2caps, ok := v2row["capabilities"].([]any)
	require.True(t, ok)
	assert.Contains(t, v2caps, service.AgentCapabilityInstanceRead)
}
