package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fr397ReadActions 只读内容域 action（capability=instance.read）。
var fr397ReadActions = []string{
	AgentActionFileList,
	AgentActionFileCheckAccess,
	AgentActionFileReadText,
	AgentActionFileVersions,
	AgentActionFileDiff,
	AgentActionConfigDiscover,
	AgentActionConfigRead,
	AgentActionConfigCrossCheck,
	AgentActionConfigVersions,
	AgentActionConfigDiff,
	AgentActionPluginList,
}

// fr397ContentActions 内容写/破坏性 action（capability=instance.content）。
var fr397ContentActions = []string{
	AgentActionFileWriteText,
	AgentActionFileRename,
	AgentActionFileChmod,
	AgentActionFileDelete,
	AgentActionFileRollback,
	AgentActionFileIssueTransferTicket,
	AgentActionPluginDeployFromAsset,
	AgentActionPluginToggle,
	AgentActionPluginDelete,
}

// fr397ConfigureActions 配置写 action（capability=instance.configure）。
var fr397ConfigureActions = []string{
	AgentActionConfigWriteText,
	AgentActionConfigWriteFields,
	AgentActionConfigRollback,
}

func fr397AllActions() []string {
	out := make([]string, 0, len(fr397ReadActions)+len(fr397ContentActions)+len(fr397ConfigureActions))
	out = append(out, fr397ReadActions...)
	out = append(out, fr397ContentActions...)
	out = append(out, fr397ConfigureActions...)
	return out
}

func TestFR397_ActionCatalogDescriptors(t *testing.T) {
	cases := []struct {
		actions    []string
		capability string
	}{
		{fr397ReadActions, AgentCapabilityInstanceRead},
		{fr397ContentActions, AgentCapabilityInstanceContent},
		{fr397ConfigureActions, AgentCapabilityInstanceConfigure},
	}
	for _, tc := range cases {
		for _, action := range tc.actions {
			d, ok := DescribeAgentAction(action)
			require.True(t, ok, "action 应已登记: %s", action)
			assert.Equal(t, tc.capability, d.V2Capability, "action %s 能力不符", action)
			assert.Equal(t, AgentResourceInstance, d.ResourceType, "action %s 资源类型应为 instance", action)
			assert.False(t, d.V1Allowed, "action %s 不得对 V1 Token 开放", action)
			assert.Empty(t, d.V1WriteAllow, "action %s 不应有 V1 写白名单", action)
			assert.False(t, d.HTTPInContract, "action %s 不进 HTTP 契约投影", action)
		}
	}
	// 破坏性操作单列
	for _, action := range []string{AgentActionFileDelete, AgentActionPluginDelete} {
		d, _ := DescribeAgentAction(action)
		assert.Equal(t, AgentOperationDestructive, d.Operation, "action %s 应为 destructive", action)
	}
}

func TestFR397_V1TokenDeniedAll(t *testing.T) {
	p := &AgentPrincipal{
		PolicyVersion:     AgentPolicyVersionV1,
		ScopedInstanceIDs: []uint{1},
		ScopedNodeIDs:     []uint{1},
		WriteAllowlist:    []string{AgentWriteInstanceLife, AgentWriteNodeMaintenance},
	}
	for _, action := range fr397AllActions() {
		_, err := CanDiscover(p, action)
		assert.ErrorIs(t, err, ErrAgentForbidden, "V1 Token 不得发现 %s", action)
		_, err = Authorize(p, action, AgentTrustedTarget{ResourceType: AgentResourceInstance, InstanceID: 1})
		assert.ErrorIs(t, err, ErrAgentForbidden, "V1 Token 不得调用 %s", action)
	}
}

func TestFR397_V2CapabilityMatrix(t *testing.T) {
	readOnly := &AgentPrincipal{
		PolicyVersion:     AgentPolicyVersionV2,
		ScopedInstanceIDs: []uint{1},
		Capabilities:      []string{AgentCapabilityInstanceRead},
	}
	for _, action := range fr397ReadActions {
		_, err := CanDiscover(readOnly, action)
		assert.NoError(t, err, "instance.read 应可发现 %s", action)
	}
	for _, action := range append(append([]string{}, fr397ContentActions...), fr397ConfigureActions...) {
		_, err := CanDiscover(readOnly, action)
		assert.ErrorIs(t, err, ErrAgentForbidden, "instance.read 不得发现写类 %s", action)
	}

	content := &AgentPrincipal{
		PolicyVersion:     AgentPolicyVersionV2,
		ScopedInstanceIDs: []uint{1},
		Capabilities:      []string{AgentCapabilityInstanceContent},
	}
	for _, action := range fr397ContentActions {
		_, err := CanDiscover(content, action)
		assert.NoError(t, err, "instance.content 应可发现 %s", action)
	}
	for _, action := range fr397ConfigureActions {
		_, err := CanDiscover(content, action)
		assert.ErrorIs(t, err, ErrAgentForbidden, "instance.content 不得发现配置写 %s", action)
	}

	configure := &AgentPrincipal{
		PolicyVersion:     AgentPolicyVersionV2,
		ScopedInstanceIDs: []uint{1},
		Capabilities:      []string{AgentCapabilityInstanceConfigure},
	}
	for _, action := range fr397ConfigureActions {
		_, err := CanDiscover(configure, action)
		assert.NoError(t, err, "instance.configure 应可发现 %s", action)
	}
	for _, action := range fr397ContentActions {
		_, err := CanDiscover(configure, action)
		assert.ErrorIs(t, err, ErrAgentForbidden, "instance.configure 不得发现内容写 %s", action)
	}
}

func TestFR397_ScopeOutsideDenied(t *testing.T) {
	p := &AgentPrincipal{
		PolicyVersion:     AgentPolicyVersionV2,
		ScopedInstanceIDs: []uint{1},
		Capabilities: []string{
			AgentCapabilityInstanceRead, AgentCapabilityInstanceContent, AgentCapabilityInstanceConfigure,
		},
	}
	for _, action := range fr397AllActions() {
		_, err := Authorize(p, action, AgentTrustedTarget{ResourceType: AgentResourceInstance, InstanceID: 99})
		assert.ErrorIs(t, err, ErrAgentForbidden, "scope 外应拒绝 %s", action)
	}
}

func TestFR397_TransferConsumeNotDiscoverable(t *testing.T) {
	// 消费只是流水标签，不得成为可授权/可发现的 action。
	_, ok := DescribeAgentAction(AgentActionFileTransferConsume)
	assert.False(t, ok, "agent.file_transfer_consume 不得进 action 目录")
}
