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

// ResolveBotTarget 从 CP 可信数据解析 Bot 目标（取其所属实例与节点）。
//
// 为什么必须有这个函数：Bot 类动作（`bot_send_command` 等）的 `id` 参数是**Bot 的 id**，
// 而授权判定需要的是**它所属实例的 id**。此前审批拦截层把 bot id 直接当实例 id 传进
// `ResolveInstanceTarget`，于是：
//   - bot id 与某个实例 id 恰好相同时「碰巧通过」——这掩盖了缺陷；
//   - 不同时解析失败，回退成 `{InstanceID: botID}`，再被判越权，返回笼统的
//     「能力/scope 不足」——即使调用者是平台管理员、bot 就在他完全可访问的实例上。
// 两种表现都不指向真实原因，排障成本极高。
//
// 存在性收敛为 ErrBotNotFound 由调用方决定如何对外表达（授权路径一律不泄露存在性）。
func (s *AgentTokenService) ResolveBotTarget(botID uint) (AgentTrustedTarget, *model.Bot, error) {
	if s == nil || s.db == nil {
		return AgentTrustedTarget{}, nil, fmt.Errorf("agent token service 未初始化")
	}
	var bot model.Bot
	if err := s.db.First(&bot, botID).Error; err != nil {
		return AgentTrustedTarget{}, nil, ErrBotNotFound
	}
	// 归属从 Bot 记录解析，而不是信任调用方传入的任何实例标识。
	target, _, err := s.ResolveInstanceTarget(bot.InstanceID)
	if err != nil {
		// Bot 指向的实例已不存在：数据不一致，按无权限处理（不让它变成可用目标）。
		return AgentTrustedTarget{}, nil, ErrAgentForbidden
	}
	target.ResourceType = AgentResourceBot
	return target, &bot, nil
}

// ResolveInstanceTarget 从 CP 可信数据解析实例目标；不存在返回 ErrInstanceNotFound。
// 仅解析目标 + 实例，不挂能力画像：画像只在真正会把它序列化出去的 get_instance 路径附加
// （见 router/agent.go GetInstance），避免所有实例级动作（启停/命令…）都带上无用字段。
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
