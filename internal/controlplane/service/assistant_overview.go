package service

import (
	"fmt"
	"sort"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 助手概览类只读（FR-544 补回）。
//
// ## 为什么单独成服务
//
// 这三个能力（集群概览 / 任务 / 告警）原本实现在助手内部，随「工具真源统一到 MCP」
// 一起被删掉了——但 MCP 的 108 个工具里**没有等价项**：`instance_search` 是逐台明细，
// 而这三个是**聚合/摘要**且按调用者权限收敛。删掉等于助手失去「看整体状况」的能力。
//
// 现在它们以 MCP 工具暴露（见 mcp/tools_overview.go），实现留在 service 层：
// MCP 包不该直接写 SQL，也不该知道 RBAC 细节。
//
// ## 收敛口径不变
//
// 全部按**用户 RBAC** 收敛，而不是 Agent Token 的 scope——助手代表的是一个人。
// 这里保留原实现的全部取舍（见各方法注释），因为那些是踩过的坑。

// AssistantOverviewService 概览类只读查询。
type AssistantOverviewService struct {
	db    *gorm.DB
	authz *AuthzService
}

// NewAssistantOverviewService 创建服务。
func NewAssistantOverviewService(db *gorm.DB, authz *AuthzService) *AssistantOverviewService {
	return &AssistantOverviewService{db: db, authz: authz}
}

// assistantClusterOverview 集群概览（只聚合，不含逐台明细）。
type assistantClusterOverview struct {
	NodeCount            int `json:"nodeCount"`
	OnlineNodeCount      int `json:"onlineNodeCount"`
	InstanceCount        int `json:"instanceCount"`
	RunningInstanceCount int `json:"runningInstanceCount"`
	CrashedInstanceCount int `json:"crashedInstanceCount"`
	StoppedInstanceCount int `json:"stoppedInstanceCount"`
	// Scoped 是否按权限收敛过（非管理员为 true）。
	// **必须回给模型**：否则它会把「你只看得到 3 台」误当成「集群只有 3 台」。
	Scoped bool `json:"scoped"`
}

// ClusterOverview 按用户可见范围统计集群概览。
func (s *AssistantOverviewService) ClusterOverview(owner uint) (*assistantClusterOverview, error) {
	access, err := s.loadAccess(owner)
	if err != nil {
		return nil, err
	}

	if access.IsPlatformAdmin {
		var nodeCount, onlineNodeCount int64
		if err := s.db.Model(&model.Node{}).Count(&nodeCount).Error; err != nil {
			return nil, err
		}
		if err := s.db.Model(&model.Node{}).Where("status = ?", model.NodeStatusOnline).Count(&onlineNodeCount).Error; err != nil {
			return nil, err
		}
		var total, running, crashed, stopped int64
		if err := s.db.Model(&model.Instance{}).Count(&total).Error; err != nil {
			return nil, err
		}
		for _, c := range []struct {
			status model.InstanceStatus
			dst    *int64
		}{
			{model.InstanceStatusRunning, &running},
			{model.InstanceStatusCrashed, &crashed},
			{model.InstanceStatusStopped, &stopped},
		} {
			if err := s.db.Model(&model.Instance{}).Where("status = ?", c.status).Count(c.dst).Error; err != nil {
				return nil, err
			}
		}
		return &assistantClusterOverview{
			NodeCount:            int(nodeCount),
			OnlineNodeCount:      int(onlineNodeCount),
			InstanceCount:        int(total),
			RunningInstanceCount: int(running),
			CrashedInstanceCount: int(crashed),
			StoppedInstanceCount: int(stopped),
			Scoped:               false,
		}, nil
	}

	// 非管理员：SQL 谓词级收敛（AccessibleInstanceIDs 返回可访问实例 id 集合）。
	//
	// 注意第二个返回值**不是**「全量可见」标记：平台管理员得到 (nil, false)，
	// 非管理员且无任何组得到 ([], true)。本分支只可能是后者语义，
	// 因此不能据它做任何「放行全部」的判断——那会反过来把零可见当成全量。
	ids, _, err := s.authz.AccessibleInstanceIDs(access)
	if err != nil {
		return nil, err
	}
	out := &assistantClusterOverview{Scoped: true}
	if len(ids) == 0 {
		return out, nil
	}

	var total, running, crashed, stopped int64
	if err := s.db.Model(&model.Instance{}).Where("id IN ?", ids).Count(&total).Error; err != nil {
		return nil, err
	}
	for _, c := range []struct {
		status model.InstanceStatus
		dst    *int64
	}{
		{model.InstanceStatusRunning, &running},
		{model.InstanceStatusCrashed, &crashed},
		{model.InstanceStatusStopped, &stopped},
	} {
		if err := s.db.Model(&model.Instance{}).Where("id IN ?", ids).
			Where("status = ?", c.status).Count(c.dst).Error; err != nil {
			return nil, err
		}
	}
	// 节点数按「可访问实例所在的节点」去重统计，而不是给全部节点数。
	var nodeIDs []uint
	if err := s.db.Model(&model.Instance{}).Where("id IN ?", ids).
		Distinct().Pluck("node_id", &nodeIDs).Error; err != nil {
		return nil, err
	}
	var onlineNodeCount int64
	if len(nodeIDs) > 0 {
		if err := s.db.Model(&model.Node{}).Where("id IN ?", nodeIDs).
			Where("status = ?", model.NodeStatusOnline).Count(&onlineNodeCount).Error; err != nil {
			return nil, err
		}
	}
	out.NodeCount = len(nodeIDs)
	out.OnlineNodeCount = int(onlineNodeCount)
	out.InstanceCount = int(total)
	out.RunningInstanceCount = int(running)
	out.CrashedInstanceCount = int(crashed)
	out.StoppedInstanceCount = int(stopped)
	return out, nil
}

// AssistantTaskBrief 任务摘要（不回详情正文）。
type AssistantTaskBrief struct {
	ID       uint   `json:"id"`
	Kind     string `json:"kind"`
	State    string `json:"state"`
	Title    string `json:"title"`
	Progress int    `json:"progress"`
}

// ListTasks 受限任务列表。
//
// 复用 `TaskService.List` 的收敛口径（非管理员只看自己创建的），而不是自己写
// `Where("created_by = ?")`——那样两处口径迟早分叉，而「谁能看到哪个任务」
// 是权限语义，只能有一个真源。
func (s *AssistantOverviewService) ListTasks(owner uint, limit int, state string) ([]AssistantTaskBrief, error) {
	access, err := s.loadAccess(owner)
	if err != nil {
		return nil, err
	}
	filter := TaskListFilter{Limit: clampBriefLimit(limit)}
	if state != "" {
		filter.State = state
	}
	tasks, _, err := NewTaskService(s.db).List(access, filter)
	if err != nil {
		return nil, err
	}
	out := make([]AssistantTaskBrief, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, AssistantTaskBrief{
			ID: t.ID, Kind: t.Kind, State: string(t.State), Title: t.Title, Progress: t.Progress,
		})
	}
	return out, nil
}

// AssistantAlertBrief 告警摘要（不回通知通道凭证）。
type AssistantAlertBrief struct {
	ID    uint   `json:"id"`
	Level string `json:"level"`
	Rule  string `json:"rule"`
	// TargetType 目标类型：node / instance。**必须一并返回**——
	// 否则模型会把 TargetID 一概当实例 id（shared-contracts §2 第 5 条明确禁止）。
	TargetType string `json:"targetType"`
	TargetID   string `json:"targetId"`
	Message    string `json:"message"`
	CreatedAt  string `json:"createdAt"`
}

// ListAlerts 受限告警列表。
//
// **用户级收敛**：`AlertService.ListEvents` 没有 scope 收敛（它服务「能读告警的人
// 看全部告警」，在既有页面语境下是有意取舍）。助手是新入口，不能继承那个隐含假设，
// 因此这里按调用者可见实例/节点过滤。
func (s *AssistantOverviewService) ListAlerts(owner uint, limit int, level string) ([]AssistantAlertBrief, error) {
	access, err := s.loadAccess(owner)
	if err != nil {
		return nil, err
	}
	limit = clampBriefLimit(limit)

	// 需要规则维度来判断 TargetID 的语义（node 维度不是实例 id），故预加载 Rule。
	q := s.db.Model(&model.AlertEvent{}).Preload("Rule").Where("resolved = ?", false)
	if level != "" {
		q = q.Where("level = ?", level)
	}

	if !access.IsPlatformAdmin {
		ids, _, aerr := s.authz.AccessibleInstanceIDs(access)
		if aerr != nil {
			return nil, aerr
		}
		nodeUUIDs := make([]string, 0, len(access.Nodes))
		for uuid := range access.Nodes {
			nodeUUIDs = append(nodeUUIDs, uuid)
		}
		sort.Strings(nodeUUIDs)

		// 没有任何可见目标时直接回空：不加谓词的 `IN ()` 会匹配全部（灾难性）。
		if len(ids) == 0 && len(nodeUUIDs) == 0 {
			return []AssistantAlertBrief{}, nil
		}

		// node 维度的 TargetID 存的是什么，取决于规则写法；这里按「节点名/uuid/数字 id」
		// 三态都试，宁可多一层过滤也不漏判。实例维度直接用 id 集合。
		nodeKeys := append([]string{}, nodeUUIDs...)
		if len(nodeUUIDs) > 0 {
			var nodeIDs []uint
			if err := s.db.Model(&model.Node{}).Where("uuid IN ?", nodeUUIDs).Pluck("id", &nodeIDs).Error; err != nil {
				return nil, err
			}
			for _, id := range nodeIDs {
				nodeKeys = append(nodeKeys, fmt.Sprintf("%d", id))
			}
			var nodeNames []string
			if err := s.db.Model(&model.Node{}).Where("uuid IN ?", nodeUUIDs).Pluck("name", &nodeNames).Error; err != nil {
				return nil, err
			}
			nodeKeys = append(nodeKeys, nodeNames...)
		}

		cond := ""
		args := []any{}
		if len(ids) > 0 {
			cond = "(rules.target_type = ? AND alert_events.target_id IN ?)"
			args = append(args, "instance", ids)
		}
		if len(nodeKeys) > 0 {
			if cond != "" {
				cond += " OR "
			}
			// node 维度：target_id 是数字列，用 CAST 后比对，兼容「存 name / 存 id」两种规则写法。
			cond += "(rules.target_type = ? AND CAST(alert_events.target_id AS TEXT) IN ?)"
			args = append(args, "node", nodeKeys)
		}
		q = q.Joins("JOIN alert_rules AS rules ON rules.id = alert_events.rule_id").Where(cond, args...)
	}

	var events []model.AlertEvent
	if err := q.Order("alert_events.fired_at DESC").Limit(limit).Find(&events).Error; err != nil {
		return nil, err
	}
	out := make([]AssistantAlertBrief, 0, len(events))
	for _, e := range events {
		// 目标类型必须取自规则：事件自身不知道自己是 node 还是 instance 维度。
		targetType := ""
		ruleName := ""
		if e.Rule.ID != 0 {
			targetType = e.Rule.TargetType
			ruleName = e.Rule.Name
		}
		out = append(out, AssistantAlertBrief{
			ID:         e.ID,
			Level:      e.Level,
			Rule:       ruleName,
			TargetType: targetType,
			TargetID:   fmt.Sprintf("%d", e.TargetID),
			Message:    truncateRunes(e.Message, 200),
			CreatedAt:  e.FiredAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	return out, nil
}

// loadAccess 装载用户权限；装不出来就拒绝（绝不凭猜测放行）。
func (s *AssistantOverviewService) loadAccess(owner uint) (*UserAccess, error) {
	if owner == 0 {
		return nil, fmt.Errorf("%w: 无有效权限上下文", ErrAssistantForbidden)
	}
	access, err := s.authz.LoadUserAccess(owner)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAssistantForbidden, err)
	}
	if access == nil {
		return nil, fmt.Errorf("%w: 无有效权限上下文", ErrAssistantForbidden)
	}
	return access, nil
}

// clampBriefLimit 夹住摘要条数上限。
//
// 上限 50 而不是放开：模型很容易一次要 500 条，那既撑爆上下文也没人看。
func clampBriefLimit(limit int) int {
	if limit <= 0 {
		return 10
	}
	if limit > 50 {
		return 50
	}
	return limit
}
