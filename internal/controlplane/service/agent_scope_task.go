package service

import (
	"fmt"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// GetTaskForAgent 按 taskId 读取任务；归属经关联实例或节点 scope 重验（FR-396）。
// Agent Token 不是平台用户，不走 CreatedBy 用户所有权；scope 外与不存在均收敛为 ErrAgentForbidden。
func (s *AgentTokenService) GetTaskForAgent(p *AgentPrincipal, taskID string) (*model.Task, []model.TaskLog, error) {
	if s == nil || s.db == nil {
		return nil, nil, fmt.Errorf("agent token service 未初始化")
	}
	if _, err := CanDiscover(p, AgentActionTaskGet); err != nil {
		return nil, nil, err
	}
	var t model.Task
	if err := s.db.Where("task_id = ?", taskID).First(&t).Error; err != nil {
		return nil, nil, ErrAgentForbidden
	}
	// 优先按关联实例授权；无实例时退回节点 scope（如节点级任务）。
	switch {
	case t.InstanceID != 0:
		if _, _, err := s.authorizeInstanceForTask(p, t.InstanceID); err != nil {
			return nil, nil, err
		}
	case t.NodeID != 0:
		if err := s.authorizeNodeForTask(p, t.NodeID); err != nil {
			return nil, nil, err
		}
	default:
		// 无关联目标：仅平台级任务，Agent 一律不可见。
		return nil, nil, ErrAgentForbidden
	}
	var logs []model.TaskLog
	if err := s.db.Where("task_id = ?", taskID).Order("seq ASC").Find(&logs).Error; err != nil {
		return nil, nil, fmt.Errorf("查询任务日志失败: %w", err)
	}
	return &t, logs, nil
}

// authorizeInstanceForTask 用 instance.read 能力校验任务关联实例是否在 scope 内。
// task_get 的 ResourceType 为 none，故不能直接走 AuthorizeInstanceAction（它会要求 instance 目标类型）。
func (s *AgentTokenService) authorizeInstanceForTask(p *AgentPrincipal, instanceID uint) (AgentAuthorization, *model.Instance, error) {
	if s == nil || s.db == nil {
		return AgentAuthorization{}, nil, fmt.Errorf("agent token service 未初始化")
	}
	var inst model.Instance
	if err := s.db.Select("id", "node_id", "name", "status", "uuid").First(&inst, instanceID).Error; err != nil {
		return AgentAuthorization{}, nil, ErrAgentForbidden
	}
	if !principalCanAccessInstance(p, inst.ID, inst.NodeID) {
		return AgentAuthorization{}, nil, ErrAgentForbidden
	}
	// 能力已在 CanDiscover 校验过 instance.read；此处只补 scope。
	return AgentAuthorization{Capability: AgentCapabilityInstanceRead}, &inst, nil
}

// authorizeNodeForTask 用节点 scope 校验任务关联节点。
func (s *AgentTokenService) authorizeNodeForTask(p *AgentPrincipal, nodeID uint) error {
	if !principalCanAccessNode(p, nodeID) {
		return ErrAgentForbidden
	}
	return nil
}
