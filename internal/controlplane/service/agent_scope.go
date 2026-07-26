package service

import (
	"fmt"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// ListAccessibleInstances 返回 principal 可访问的实例集合。
// V1：仅显式实例 ID；V2：显式实例 ∪ 授权节点上的当前实例。
// optionalNodeID 非空时再叠加 node_id 过滤（结果过滤，不授予额外权限）。
func (s *AgentTokenService) ListAccessibleInstances(p *AgentPrincipal, optionalNodeID *uint) ([]model.Instance, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("agent token service 未初始化")
	}
	if p == nil {
		return nil, ErrAgentForbidden
	}
	if _, err := CanDiscover(p, AgentActionListInstances); err != nil {
		return nil, err
	}

	q := s.db.Model(&model.Instance{})
	switch {
	case p.PolicyVersion == AgentPolicyVersionV2:
		hasInst := len(p.ScopedInstanceIDs) > 0
		hasNode := len(p.ScopedNodeIDs) > 0
		switch {
		case hasInst && hasNode:
			q = q.Where("id IN ? OR node_id IN ?", p.ScopedInstanceIDs, p.ScopedNodeIDs)
		case hasInst:
			q = q.Where("id IN ?", p.ScopedInstanceIDs)
		case hasNode:
			q = q.Where("node_id IN ?", p.ScopedNodeIDs)
		default:
			return []model.Instance{}, nil
		}
	default:
		if len(p.ScopedInstanceIDs) == 0 {
			return []model.Instance{}, nil
		}
		q = q.Where("id IN ?", p.ScopedInstanceIDs)
	}
	if optionalNodeID != nil {
		q = q.Where("node_id = ?", *optionalNodeID)
	}
	var out []model.Instance
	if err := q.Order("id ASC").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("查询可访问实例失败: %w", err)
	}
	return out, nil
}

// ResolveInstanceTarget 从 CP 可信数据解析实例目标；不存在返回 ErrInstanceNotFound。
func (s *AgentTokenService) ResolveInstanceTarget(instanceID uint) (AgentTrustedTarget, *model.Instance, error) {
	if s == nil || s.db == nil {
		return AgentTrustedTarget{}, nil, fmt.Errorf("agent token service 未初始化")
	}
	var inst model.Instance
	if err := s.db.First(&inst, instanceID).Error; err != nil {
		return AgentTrustedTarget{}, nil, ErrInstanceNotFound
	}
	return AgentTrustedTarget{
		ResourceType: AgentResourceInstance,
		InstanceID:   inst.ID,
		NodeID:       inst.NodeID,
	}, &inst, nil
}

// AuthorizeInstanceAction 解析实例并执行最终授权；scope 外与不存在均收敛为 ErrAgentForbidden。
func (s *AgentTokenService) AuthorizeInstanceAction(p *AgentPrincipal, action string, instanceID uint) (AgentAuthorization, *model.Instance, error) {
	target, inst, err := s.ResolveInstanceTarget(instanceID)
	if err != nil {
		// 不向 Agent 泄露存在性。
		return AgentAuthorization{}, nil, ErrAgentForbidden
	}
	auth, err := Authorize(p, action, target)
	if err != nil {
		return AgentAuthorization{}, nil, err
	}
	return auth, inst, nil
}

// AuthorizeNodeAction 对显式节点目标执行最终授权。
func (s *AgentTokenService) AuthorizeNodeAction(p *AgentPrincipal, action string, nodeID uint) (AgentAuthorization, error) {
	return Authorize(p, action, AgentTrustedTarget{
		ResourceType: AgentResourceNode,
		NodeID:       nodeID,
	})
}
