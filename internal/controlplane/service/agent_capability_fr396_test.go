package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FR-396：新 action 一律 V1Allowed=false，V2 按能力发现。
func TestFR396_ActionCatalog_V1NeverSeesNewActions(t *testing.T) {
	for _, d := range fr396DomainActions {
		assert.False(t, d.V1Allowed, "FR-396 action 不得对 V1 开放: "+d.Action)
		got, ok := DescribeAgentAction(d.Action)
		require.True(t, ok, "action 必须登记: "+d.Action)
		assert.Equal(t, d.Action, got.Action)
		assert.Equal(t, d.RequiresConfirm, got.RequiresConfirm)
	}
}

func TestFR396_CanDiscover_V2CapabilityMatrix(t *testing.T) {
	p := &AgentPrincipal{
		PolicyVersion:     AgentPolicyVersionV2,
		ScopedInstanceIDs: []uint{1},
		ScopedNodeIDs:     []uint{1},
		Capabilities:      []string{AgentCapabilityInstanceDestructive},
	}
	_, err := CanDiscover(p, AgentActionInstanceKill)
	assert.NoError(t, err)
	_, err = CanDiscover(p, AgentActionInstanceStart)
	assert.ErrorIs(t, err, ErrAgentForbidden, "无 instance.life 不得发现 start")
	_, err = CanDiscover(p, AgentActionNodePurgeArchived)
	assert.ErrorIs(t, err, ErrAgentForbidden, "无 node.destructive 不得发现 purge")
}

func TestFR396_CanDiscover_V1Blocked(t *testing.T) {
	p := &AgentPrincipal{
		PolicyVersion:     AgentPolicyVersionV1,
		ScopedInstanceIDs: []uint{1},
		ScopedNodeIDs:     []uint{1},
		WriteAllowlist:    []string{AgentWriteInstanceLife, AgentWriteNodeMaintenance},
	}
	for _, d := range fr396DomainActions {
		_, err := CanDiscover(p, d.Action)
		assert.ErrorIs(t, err, ErrAgentForbidden, "V1 不得发现 "+d.Action)
	}
}

func TestFR396_DestructiveRequiresConfirmFlag(t *testing.T) {
	for _, action := range []string{
		AgentActionInstanceKill, AgentActionInstanceDelete, AgentActionNodePurgeArchived,
	} {
		d, ok := DescribeAgentAction(action)
		require.True(t, ok)
		assert.True(t, d.RequiresConfirm, action+" 须 RequiresConfirm")
		assert.Equal(t, AgentOperationDestructive, d.Operation)
	}
}
