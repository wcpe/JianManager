package service

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

var (
	// ErrBotRuntimeTargetNotFound 表示关联的节点、实例或压测会话不存在。
	ErrBotRuntimeTargetNotFound = errors.New("Bot Worker 运行时关联对象不存在")
)

// BotRuntimeQuery 是共享 Bot Worker 历史观测的关联和时间范围。
type BotRuntimeQuery struct {
	NodeID, InstanceID, SessionID uint
	From, To                      time.Time
	Resolution                    string
}

// BotRuntimeNode 是一个关联节点的共享运行时序列。
type BotRuntimeNode struct {
	NodeID   uint     `json:"nodeId"`
	NodeName string   `json:"nodeName"`
	Series   []Series `json:"series"`
}

// BotRuntimeUnavailable 说明当前快照为何不能提供完整共享运行时数据。
type BotRuntimeUnavailable struct {
	NodeID uint   `json:"nodeId"`
	Reason string `json:"reason"`
}

// BotRuntimeResult 是共享 Bot Worker 历史观测响应，资源永不归属到单个 Bot 或会话。
type BotRuntimeResult struct {
	Resolution    string                  `json:"resolution"`
	From          time.Time               `json:"from"`
	To            time.Time               `json:"to"`
	SharedRuntime bool                    `json:"sharedRuntime"`
	Notice        string                  `json:"notice"`
	Nodes         []BotRuntimeNode        `json:"nodes"`
	Unavailable   []BotRuntimeUnavailable `json:"unavailable"`
}

const botRuntimeSharedNotice = "Bot Worker 资源为共享进程观察值，不代表任一 Bot 或会话的独占资源。"

var botRuntimeMetricKeys = []string{
	model.MetricWorkerProcessRSSBytes,
	model.MetricWorkerProcessCPUPct,
	model.MetricBotWorkerRSSBytes,
	model.MetricBotWorkerCPUPct,
	model.MetricBotActiveCount,
	model.MetricBotConnectingCount,
	model.MetricBotCapacityMax,
	model.MetricBotEventLoopP95MS,
}

// QueryBotRuntime 按节点、目标实例、压测会话或全平台关联查询共享运行时序列。
func (s *MetricService) QueryBotRuntime(query BotRuntimeQuery) (BotRuntimeResult, error) {
	return s.QueryBotRuntimeAt(time.Now().UTC(), query)
}

// QueryBotRuntimeAt 注入当前时刻，便于验证当前快照可用性元数据。
func (s *MetricService) QueryBotRuntimeAt(now time.Time, query BotRuntimeQuery) (BotRuntimeResult, error) {
	if botRuntimeSelectorCount(query) > 1 || !query.To.After(query.From) {
		return BotRuntimeResult{}, fmt.Errorf("Bot Worker 运行时查询参数非法")
	}
	nodes, err := s.botRuntimeNodes(query)
	if err != nil {
		return BotRuntimeResult{}, err
	}
	resolution := selectResolution(query.To.Sub(query.From), query.Resolution)
	result := BotRuntimeResult{
		Resolution: resolution, From: query.From.UTC(), To: query.To.UTC(),
		SharedRuntime: true, Notice: botRuntimeSharedNotice,
		Nodes: make([]BotRuntimeNode, 0, len(nodes)), Unavailable: []BotRuntimeUnavailable{},
	}
	for _, node := range nodes {
		_, series, queryErr := s.QuerySeries(SeriesQuery{
			Scope: model.MetricScopeNode, NodeUUID: node.UUID, MetricKeys: botRuntimeMetricKeys,
			From: query.From, To: query.To, Resolution: resolution,
		})
		if queryErr != nil {
			return BotRuntimeResult{}, queryErr
		}
		result.Nodes = append(result.Nodes, BotRuntimeNode{NodeID: node.ID, NodeName: node.Name, Series: series})
		if reason := botRuntimeUnavailableReason(node, now); reason != "" {
			result.Unavailable = append(result.Unavailable, BotRuntimeUnavailable{NodeID: node.ID, Reason: reason})
		}
	}
	return result, nil
}

func botRuntimeSelectorCount(query BotRuntimeQuery) int {
	count := 0
	for _, id := range []uint{query.NodeID, query.InstanceID, query.SessionID} {
		if id != 0 {
			count++
		}
	}
	return count
}

func (s *MetricService) botRuntimeNodes(query BotRuntimeQuery) ([]model.Node, error) {
	ids, err := s.botRuntimeNodeIDs(query)
	if err != nil {
		return nil, err
	}
	var nodes []model.Node
	dbQuery := s.db.Order("id ASC")
	if len(ids) > 0 {
		dbQuery = dbQuery.Where("id IN ?", ids)
	}
	if err := dbQuery.Find(&nodes).Error; err != nil {
		return nil, err
	}
	if len(ids) > 0 && len(nodes) != len(ids) {
		return nil, ErrBotRuntimeTargetNotFound
	}
	return nodes, nil
}

func (s *MetricService) botRuntimeNodeIDs(query BotRuntimeQuery) ([]uint, error) {
	switch {
	case query.NodeID != 0:
		return []uint{query.NodeID}, nil
	case query.InstanceID != 0:
		var instance model.Instance
		if err := s.db.Select("id", "node_id").First(&instance, query.InstanceID).Error; err != nil {
			return nil, botRuntimeNotFound(err)
		}
		return []uint{instance.NodeID}, nil
	case query.SessionID != 0:
		return s.botRuntimeSessionNodeIDs(query.SessionID)
	default:
		return nil, nil
	}
}

func (s *MetricService) botRuntimeSessionNodeIDs(sessionID uint) ([]uint, error) {
	var session model.BotStressSession
	if err := s.db.Select("id", "instance_id").First(&session, sessionID).Error; err != nil {
		return nil, botRuntimeNotFound(err)
	}
	var instance model.Instance
	if err := s.db.Select("node_id").First(&instance, session.InstanceID).Error; err != nil {
		return nil, botRuntimeNotFound(err)
	}
	ids := []uint{instance.NodeID}
	var batches []model.BotLoadBatch
	if err := s.db.Select("executor_node_id").Where("stress_session_id = ?", session.ID).
		Order("executor_node_id ASC").Find(&batches).Error; err != nil {
		return nil, err
	}
	for _, batch := range batches {
		ids = append(ids, batch.ExecutorNodeID)
	}
	return uniqueBotRuntimeNodeIDs(ids), nil
}

// ResolveBotRuntimeSessionInstanceID 返回压测会话目标实例，供路由在查询前执行实例访问校验。
func (s *MetricService) ResolveBotRuntimeSessionInstanceID(sessionID uint) (uint, bool, error) {
	var session model.BotStressSession
	err := s.db.Select("instance_id").First(&session, sessionID).Error
	if err == nil {
		return session.InstanceID, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, false, nil
	}
	return 0, false, err
}

func botRuntimeNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrBotRuntimeTargetNotFound
	}
	return err
}

func uniqueBotRuntimeNodeIDs(ids []uint) []uint {
	seen := make(map[uint]struct{}, len(ids))
	result := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func botRuntimeUnavailableReason(node model.Node, now time.Time) string {
	if node.Status != model.NodeStatusOnline {
		return "节点离线"
	}
	if node.LastHeartbeat == nil || node.LastHeartbeat.Before(now.Add(-botRuntimeMetricFreshWindow)) {
		return "节点心跳已过期"
	}
	if node.ManagedRuntimeObservedAt == nil || node.ManagedRuntimeObservedAt.Before(now.Add(-botRuntimeMetricFreshWindow)) {
		return "受管运行时快照已过期"
	}
	if !node.BotAvailable {
		if node.BotUnavailableReason != "" {
			return node.BotUnavailableReason
		}
		return "Bot Worker 不可用"
	}
	return ""
}
