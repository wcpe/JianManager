package service

import (
	"fmt"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// FR-398：Bot 与压测运行的可信目标解析。
// 归属一律从 CP 数据库读取，不接受客户端传入的 instanceId/nodeId 作为授权依据。

// AuthorizeBotAction 解析 Bot 所属实例后执行最终授权。
// Bot 不存在与 scope 外均收敛为 ErrAgentForbidden，不泄露存在性。
func (s *AgentTokenService) AuthorizeBotAction(p *AgentPrincipal, action string, botID uint) (AgentAuthorization, *model.Bot, error) {
	if s == nil || s.db == nil {
		return AgentAuthorization{}, nil, fmt.Errorf("agent token service 未初始化")
	}
	var bot model.Bot
	if err := s.db.First(&bot, botID).Error; err != nil {
		return AgentAuthorization{}, nil, ErrAgentForbidden
	}
	target, err := s.trustedInstanceTarget(AgentResourceBot, bot.InstanceID)
	if err != nil {
		return AgentAuthorization{}, nil, err
	}
	auth, err := Authorize(p, action, target)
	if err != nil {
		return AgentAuthorization{}, nil, err
	}
	return auth, &bot, nil
}

// AuthorizeBotRunAction 对压测运行执行双重 scope 校验：目标实例 + 运行已持久化的 executor 节点集合。
// 返回的 outOfScopeNodes 是越界执行节点，供停止方向在返回中告知；启动方向须自行拒绝非空结果。
func (s *AgentTokenService) AuthorizeBotRunAction(p *AgentPrincipal, action string, sessionID uint) (AgentAuthorization, *model.BotStressSession, []uint, error) {
	if s == nil || s.db == nil {
		return AgentAuthorization{}, nil, nil, fmt.Errorf("agent token service 未初始化")
	}
	var session model.BotStressSession
	if err := s.db.First(&session, sessionID).Error; err != nil {
		return AgentAuthorization{}, nil, nil, ErrAgentForbidden
	}
	target, err := s.trustedInstanceTarget(AgentResourceBotRun, session.InstanceID)
	if err != nil {
		return AgentAuthorization{}, nil, nil, err
	}
	auth, err := Authorize(p, action, target)
	if err != nil {
		return AgentAuthorization{}, nil, nil, err
	}
	nodeIDs, err := s.runExecutorNodeIDs(sessionID)
	if err != nil {
		return AgentAuthorization{}, nil, nil, err
	}
	outOfScope := make([]uint, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		if !principalCanAccessNode(p, nodeID) {
			outOfScope = append(outOfScope, nodeID)
		}
	}
	return auth, &session, outOfScope, nil
}

// AuthorizeBotRunExecutorNodes 校验启动方向显式请求的 executor 节点集合。
// 任一越界即整体拒绝，绝不静默缩减节点集合降级执行。
func (s *AgentTokenService) AuthorizeBotRunExecutorNodes(p *AgentPrincipal, nodeIDs []uint) error {
	for _, nodeID := range nodeIDs {
		if !principalCanAccessNode(p, nodeID) {
			return ErrAgentForbidden
		}
	}
	return nil
}

// trustedInstanceTarget 由实例 ID 解析出含归属节点的可信目标。
func (s *AgentTokenService) trustedInstanceTarget(resourceType string, instanceID uint) (AgentTrustedTarget, error) {
	var inst model.Instance
	if err := s.db.Select("id", "node_id").First(&inst, instanceID).Error; err != nil {
		return AgentTrustedTarget{}, ErrAgentForbidden
	}
	return AgentTrustedTarget{
		ResourceType: resourceType,
		InstanceID:   inst.ID,
		NodeID:       inst.NodeID,
	}, nil
}

// runExecutorNodeIDs 查询运行已持久化的 executor 节点集合（去重升序）。
func (s *AgentTokenService) runExecutorNodeIDs(sessionID uint) ([]uint, error) {
	var nodeIDs []uint
	err := s.db.Model(&model.BotLoadBatch{}).
		Distinct("executor_node_id").
		Where("stress_session_id = ?", sessionID).
		Order("executor_node_id ASC").
		Pluck("executor_node_id", &nodeIDs).Error
	if err != nil {
		return nil, fmt.Errorf("查询运行执行节点集合失败: %w", err)
	}
	return nodeIDs, nil
}
