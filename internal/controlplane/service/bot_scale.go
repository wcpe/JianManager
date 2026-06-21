package service

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// Bot 规模化查询/聚合/批量操作（FR-038）。
// 万级 Bot 下不全量序列化：列表分页、摘要走 DB 聚合，批量经 gRPC 按节点分片并发委托。

const (
	// defaultBotPageSize 列表默认每页条数。
	defaultBotPageSize = 20
	// maxBotPageSize 列表每页条数上限。
	maxBotPageSize = 100
	// maxBatchTargets 单次批量操作目标数上限，避免单请求过载。
	maxBatchTargets = 5000
	// batchConcurrency 批量委托的有界并发度。
	batchConcurrency = 16
	// maxBatchErrors 批量结果回传的失败明细上限。
	maxBatchErrors = 100
)

// BotFilter Bot 查询筛选条件，列表/摘要/批量目标解析共用。
type BotFilter struct {
	InstanceID *uint
	NodeID     *uint
	Status     *model.BotStatus
	Behavior   *string
	Keyword    string // 匹配 name 或 uuid
}

// BotFilterIn 批量请求体中的筛选 DTO（JSON 可绑定），经 ToFilter 转为内部 BotFilter。
type BotFilterIn struct {
	InstanceID *uint   `json:"instanceId"`
	NodeID     *uint   `json:"nodeId"`
	Status     *string `json:"status"`
	Behavior   *string `json:"behavior"`
	Keyword    string  `json:"q"`
}

// ToFilter 将请求 DTO 转为内部筛选条件。
func (in BotFilterIn) ToFilter() BotFilter {
	f := BotFilter{
		InstanceID: in.InstanceID,
		NodeID:     in.NodeID,
		Behavior:   in.Behavior,
		Keyword:    in.Keyword,
	}
	if in.Status != nil {
		s := model.BotStatus(*in.Status)
		f.Status = &s
	}
	return f
}

// BotListQuery 分页列表查询参数。
type BotListQuery struct {
	Filter   BotFilter
	Page     int
	PageSize int
}

// BotListResult 分页列表结果。
type BotListResult struct {
	Items    []model.Bot `json:"items"`
	Total    int64       `json:"total"`
	Page     int         `json:"page"`
	PageSize int         `json:"pageSize"`
}

// BotSummaryGroup 分组聚合的单组计数。
type BotSummaryGroup struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Total  int64  `json:"total"`
	Online int64  `json:"online"`
}

// BotSummary Bot 计数聚合结果，不含逐条 Bot。
type BotSummary struct {
	Total    int64             `json:"total"`
	ByStatus map[string]int64  `json:"byStatus"`
	GroupBy  string            `json:"groupBy,omitempty"`
	Groups   []BotSummaryGroup `json:"groups,omitempty"`
}

// BotBatchAction 批量操作动作。
type BotBatchAction string

const (
	BotBatchSetBehavior BotBatchAction = "set-behavior"
	BotBatchStart       BotBatchAction = "start"
	BotBatchStop        BotBatchAction = "stop"
	BotBatchDelete      BotBatchAction = "delete"
)

// BotBatchRequest 批量操作请求。目标由 IDs 或 Filter 二选一指定。
type BotBatchRequest struct {
	Action   BotBatchAction
	IDs      []uint
	Filter   *BotFilter
	Behavior string
	Target   string
}

// BotBatchError 批量操作单条失败明细。
type BotBatchError struct {
	BotID uint   `json:"botId"`
	Error string `json:"error"`
}

// BotBatchResult 批量操作结果计数。
type BotBatchResult struct {
	Action    string          `json:"action"`
	Requested int             `json:"requested"`
	Succeeded int             `json:"succeeded"`
	Failed    int             `json:"failed"`
	Skipped   int             `json:"skipped"`
	Errors    []BotBatchError `json:"errors"`
}

// applyFilter 将筛选条件作用到查询，nodeID 经 instances 联表。
// scopeIDs 非 nil 时附加可访问实例集合谓词（跨组隔离下沉为 SQL）。
func applyFilter(q *gorm.DB, f BotFilter, scopeIDs []uint, scope bool) *gorm.DB {
	if scope {
		if len(scopeIDs) == 0 {
			// 无任何可见实例：强制空结果
			return q.Where("1 = 0")
		}
		q = q.Where("bots.instance_id IN ?", scopeIDs)
	}
	if f.InstanceID != nil {
		q = q.Where("bots.instance_id = ?", *f.InstanceID)
	}
	if f.NodeID != nil {
		// Bot 无 node_id 列，经实例联表收敛到该节点下的实例
		q = q.Where("bots.instance_id IN (?)",
			q.Session(&gorm.Session{NewDB: true}).
				Model(&model.Instance{}).
				Select("id").
				Where("node_id = ?", *f.NodeID))
	}
	if f.Status != nil {
		q = q.Where("bots.status = ?", *f.Status)
	}
	if f.Behavior != nil {
		q = q.Where("bots.behavior = ?", *f.Behavior)
	}
	if f.Keyword != "" {
		like := "%" + f.Keyword + "%"
		q = q.Where("bots.name LIKE ? OR bots.uuid LIKE ?", like, like)
	}
	return q
}

// ListPaged 分页查询 Bot，scopeIDs/scope 用于跨组隔离收敛。
func (s *BotService) ListPaged(query BotListQuery, scopeIDs []uint, scope bool) (*BotListResult, error) {
	page := query.Page
	if page < 1 {
		page = 1
	}
	size := query.PageSize
	if size < 1 {
		size = defaultBotPageSize
	}
	if size > maxBotPageSize {
		size = maxBotPageSize
	}

	base := applyFilter(s.db.Model(&model.Bot{}), query.Filter, scopeIDs, scope)

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, fmt.Errorf("统计 Bot 总数失败: %w", err)
	}

	var items []model.Bot
	if total > 0 {
		if err := base.
			Preload("Instance.Node").
			Order("bots.id ASC").
			Offset((page - 1) * size).
			Limit(size).
			Find(&items).Error; err != nil {
			return nil, fmt.Errorf("查询 Bot 列表失败: %w", err)
		}
	}
	if items == nil {
		items = []model.Bot{}
	}

	// 列表也回填实时状态：否则重连/连接的 Bot 在聚合页一直显示 connecting。
	s.refreshStatuses(items)

	return &BotListResult{Items: items, Total: total, Page: page, PageSize: size}, nil
}

// refreshStatuses 按节点聚合批量拉取各 Bot 的实时状态并回填 DB（每个 Worker 仅一次 ListBots）。
// 需 items 预加载 Instance.Node。Worker 离线或 Bot 不在列表中时保留上次状态。
func (s *BotService) refreshStatuses(bots []model.Bot) {
	byNode := map[string][]int{}
	for i := range bots {
		if u := bots[i].Instance.Node.UUID; u != "" {
			byNode[u] = append(byNode[u], i)
		}
	}
	for nodeUUID, idxs := range byNode {
		client, ok := s.pool.Get(nodeUUID)
		if !ok {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		resp, err := client.Worker.ListBots(ctx, &workerpb.ListBotsRequest{})
		cancel()
		if err != nil {
			continue
		}
		live := make(map[string]string, len(resp.Bots))
		for _, bi := range resp.Bots {
			live[bi.BotUuid] = bi.Status
		}
		for _, i := range idxs {
			st, ok := live[bots[i].UUID]
			if !ok {
				continue
			}
			mapped := mapWorkerBotStatus(st)
			if mapped != "" && mapped != bots[i].Status {
				bots[i].Status = mapped
				_ = s.db.Model(&model.Bot{}).Where("id = ?", bots[i].ID).Update("status", mapped).Error
			}
		}
	}
}

// Summary 计算 Bot 计数聚合，仅 DB 聚合不序列化 Bot 行。
// groupBy 为空只返回 total + byStatus；否则附加分组计数。
func (s *BotService) Summary(f BotFilter, groupBy string, scopeIDs []uint, scope bool) (*BotSummary, error) {
	summary := &BotSummary{ByStatus: map[string]int64{}}

	base := applyFilter(s.db.Model(&model.Bot{}), f, scopeIDs, scope)
	if err := base.Count(&summary.Total).Error; err != nil {
		return nil, fmt.Errorf("统计 Bot 总数失败: %w", err)
	}

	// byStatus 始终返回，供概览卡片使用
	type statusRow struct {
		Status string
		Cnt    int64
	}
	var statusRows []statusRow
	if err := applyFilter(s.db.Model(&model.Bot{}), f, scopeIDs, scope).
		Select("bots.status AS status, COUNT(*) AS cnt").
		Group("bots.status").
		Scan(&statusRows).Error; err != nil {
		return nil, fmt.Errorf("统计 Bot 状态分布失败: %w", err)
	}
	for _, r := range statusRows {
		summary.ByStatus[r.Status] = r.Cnt
	}

	if groupBy == "" {
		return summary, nil
	}
	summary.GroupBy = groupBy

	groups, err := s.summaryGroups(f, groupBy, scopeIDs, scope)
	if err != nil {
		return nil, err
	}
	summary.Groups = groups
	return summary, nil
}

// summaryGroups 按指定维度做 GROUP BY 聚合（含 online=connected 计数）。
func (s *BotService) summaryGroups(f BotFilter, groupBy string, scopeIDs []uint, scope bool) ([]BotSummaryGroup, error) {
	type groupRow struct {
		Key    string
		Total  int64
		Online int64
	}

	// online 计数：connected 状态在分组内的条数（CASE WHEN 兼容 SQLite/MySQL）
	onlineExpr := fmt.Sprintf("SUM(CASE WHEN bots.status = '%s' THEN 1 ELSE 0 END) AS online", model.BotStatusConnected)

	var keyCol, groupCol string
	switch groupBy {
	case "instance":
		keyCol = "bots.instance_id"
		groupCol = "bots.instance_id"
	case "node":
		// 经实例联表取节点
		keyCol = "instances.node_id"
		groupCol = "instances.node_id"
	case "status":
		keyCol = "bots.status"
		groupCol = "bots.status"
	case "behavior":
		keyCol = "bots.behavior"
		groupCol = "bots.behavior"
	default:
		return nil, fmt.Errorf("不支持的分组维度: %s", groupBy)
	}

	q := applyFilter(s.db.Model(&model.Bot{}), f, scopeIDs, scope)
	if groupBy == "node" {
		q = q.Joins("JOIN instances ON instances.id = bots.instance_id")
	}

	var rows []groupRow
	if err := q.
		Select(fmt.Sprintf("%s AS key, COUNT(*) AS total, %s", keyCol, onlineExpr)).
		Group(groupCol).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("分组统计 Bot 失败: %w", err)
	}

	groups := make([]BotSummaryGroup, 0, len(rows))
	for _, r := range rows {
		groups = append(groups, BotSummaryGroup{
			Key:    r.Key,
			Label:  r.Key,
			Total:  r.Total,
			Online: r.Online,
		})
	}

	// instance/node 维度补充可读名（label）
	switch groupBy {
	case "instance":
		s.labelInstances(groups)
	case "node":
		s.labelNodes(groups)
	}
	return groups, nil
}

// labelInstances 用实例名填充 instance 分组的 label。
func (s *BotService) labelInstances(groups []BotSummaryGroup) {
	if len(groups) == 0 {
		return
	}
	ids := make([]string, 0, len(groups))
	for _, g := range groups {
		ids = append(ids, g.Key)
	}
	var rows []struct {
		ID   uint
		Name string
	}
	if err := s.db.Model(&model.Instance{}).Select("id, name").Where("id IN ?", ids).Scan(&rows).Error; err != nil {
		slog.Warn("查询实例名失败，分组 label 退化为 ID", "error", err)
		return
	}
	nameByID := map[string]string{}
	for _, r := range rows {
		nameByID[fmt.Sprintf("%d", r.ID)] = r.Name
	}
	for i := range groups {
		if name, ok := nameByID[groups[i].Key]; ok {
			groups[i].Label = name
		}
	}
}

// labelNodes 用节点名填充 node 分组的 label。
func (s *BotService) labelNodes(groups []BotSummaryGroup) {
	if len(groups) == 0 {
		return
	}
	ids := make([]string, 0, len(groups))
	for _, g := range groups {
		ids = append(ids, g.Key)
	}
	var rows []struct {
		ID   uint
		Name string
	}
	if err := s.db.Model(&model.Node{}).Select("id, name").Where("id IN ?", ids).Scan(&rows).Error; err != nil {
		slog.Warn("查询节点名失败，分组 label 退化为 ID", "error", err)
		return
	}
	nameByID := map[string]string{}
	for _, r := range rows {
		nameByID[fmt.Sprintf("%d", r.ID)] = r.Name
	}
	for i := range groups {
		if name, ok := nameByID[groups[i].Key]; ok {
			groups[i].Label = name
		}
	}
}

// resolveBatchTargets 解析批量目标 Bot（预加载实例+节点），并按可访问实例集合收敛。
// 返回 (目标 Bot 列表, skipped 数量)。skipped 为请求 IDs 中不存在或越权被剔除的数量（存在性隐藏）。
func (s *BotService) resolveBatchTargets(req BotBatchRequest, scopeIDs []uint, scope bool) ([]model.Bot, int, error) {
	var bots []model.Bot

	if len(req.IDs) > 0 {
		q := applyFilter(s.db.Model(&model.Bot{}).Preload("Instance.Node"), BotFilter{}, scopeIDs, scope)
		if err := q.Where("bots.id IN ?", req.IDs).Find(&bots).Error; err != nil {
			return nil, 0, fmt.Errorf("查询批量目标失败: %w", err)
		}
		// 请求了 N 个 id，鉴权/存在性过滤后剩 len(bots)，差额即 skipped
		skipped := len(req.IDs) - len(bots)
		if skipped < 0 {
			skipped = 0
		}
		return bots, skipped, nil
	}

	// filter 模式
	f := BotFilter{}
	if req.Filter != nil {
		f = *req.Filter
	}
	q := applyFilter(s.db.Model(&model.Bot{}).Preload("Instance.Node"), f, scopeIDs, scope)
	if err := q.Limit(maxBatchTargets + 1).Find(&bots).Error; err != nil {
		return nil, 0, fmt.Errorf("查询批量目标失败: %w", err)
	}
	return bots, 0, nil
}

// Batch 执行批量操作：解析目标 → 按节点分片 → 有界并发委托既有 per-bot RPC → 计数。
func (s *BotService) Batch(req BotBatchRequest, scopeIDs []uint, scope bool) (*BotBatchResult, error) {
	bots, skipped, err := s.resolveBatchTargets(req, scopeIDs, scope)
	if err != nil {
		return nil, err
	}
	if len(bots) > maxBatchTargets {
		return nil, fmt.Errorf("批量目标数 %d 超过上限 %d", len(bots), maxBatchTargets)
	}

	result := &BotBatchResult{
		Action:    string(req.Action),
		Requested: len(bots),
		Skipped:   skipped,
		Errors:    []BotBatchError{},
	}
	if len(bots) == 0 {
		return result, nil
	}

	// 先做 DB 侧状态变更（与既有单 Bot 操作语义一致：DB 变更不依赖 Worker 委托成功）
	s.applyBatchDBChange(req, bots)

	// 有界并发委托，按 Bot 各自所属节点路由
	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		sem    = make(chan struct{}, batchConcurrency)
	)
	for i := range bots {
		bot := bots[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			derr := s.delegateBatchOne(req, &bot)

			mu.Lock()
			defer mu.Unlock()
			if derr != nil {
				result.Failed++
				if len(result.Errors) < maxBatchErrors {
					result.Errors = append(result.Errors, BotBatchError{BotID: bot.ID, Error: derr.Error()})
				}
			} else {
				result.Succeeded++
			}
		}()
	}
	wg.Wait()

	return result, nil
}

// applyBatchDBChange 应用批量操作的 DB 侧变更（行为/状态/软删除）。
func (s *BotService) applyBatchDBChange(req BotBatchRequest, bots []model.Bot) {
	ids := make([]uint, 0, len(bots))
	for _, b := range bots {
		ids = append(ids, b.ID)
	}
	switch req.Action {
	case BotBatchSetBehavior:
		if err := s.db.Model(&model.Bot{}).Where("id IN ?", ids).Update("behavior", req.Behavior).Error; err != nil {
			slog.Warn("批量更新 Bot 行为失败", "error", err)
		}
	case BotBatchStop:
		if err := s.db.Model(&model.Bot{}).Where("id IN ?", ids).Update("status", model.BotStatusStopped).Error; err != nil {
			slog.Warn("批量更新 Bot 状态失败", "error", err)
		}
	case BotBatchDelete:
		if err := s.db.Where("id IN ?", ids).Delete(&model.Bot{}).Error; err != nil {
			slog.Warn("批量删除 Bot 失败", "error", err)
		}
	case BotBatchStart:
		// 状态由委托返回结果回写，见 delegateBatchOne
	}
}

// delegateBatchOne 将单个 Bot 的批量动作委托到其所属 Worker（复用既有 per-bot RPC）。
func (s *BotService) delegateBatchOne(req BotBatchRequest, bot *model.Bot) error {
	if bot.Instance.ID == 0 || bot.Instance.Node.UUID == "" {
		return fmt.Errorf("Bot %d 缺少关联实例或节点", bot.ID)
	}
	client, ok := s.pool.Get(bot.Instance.Node.UUID)
	if !ok {
		return fmt.Errorf("Worker %s 未连接", bot.Instance.Node.UUID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	switch req.Action {
	case BotBatchSetBehavior:
		resp, err := client.Worker.SetBotBehavior(ctx, &workerpb.SetBotBehaviorRequest{
			BotUuid:  bot.UUID,
			Behavior: req.Behavior,
			Target:   req.Target,
		})
		if err != nil {
			return fmt.Errorf("gRPC SetBotBehavior 失败: %w", err)
		}
		if !resp.Success {
			return fmt.Errorf("Worker SetBotBehavior 失败: %s", resp.Error)
		}
	case BotBatchStop, BotBatchDelete:
		resp, err := client.Worker.DeleteBot(ctx, &workerpb.DeleteBotRequest{BotUuid: bot.UUID})
		if err != nil {
			return fmt.Errorf("gRPC DeleteBot 失败: %w", err)
		}
		if !resp.Success {
			return fmt.Errorf("Worker DeleteBot 失败: %s", resp.Error)
		}
	case BotBatchStart:
		// 重连即重新上线：必须带连接目标（host/port/version），否则 Bot 连到默认端口连不上。
		host, port, conn := botConnTarget(bot, &bot.Instance)
		resp, err := client.Worker.CreateBot(ctx, &workerpb.CreateBotRequest{
			BotUuid:      bot.UUID,
			InstanceUuid: bot.Instance.UUID,
			Name:         bot.Name,
			Host:         host,
			Port:         int32(port),
			Username:     conn.Username,
			Version:      conn.Version,
			Auth:         conn.Auth,
			Behavior:     bot.Behavior,
		})
		if err != nil {
			return fmt.Errorf("gRPC CreateBot 失败: %w", err)
		}
		if !resp.Success {
			return fmt.Errorf("Worker CreateBot 失败: %s", resp.Error)
		}
		// 真实状态由读取时 refreshStatus 回填，这里先置 connecting，不再乐观置 connected。
		_ = s.db.Model(&model.Bot{}).Where("id = ?", bot.ID).Update("status", model.BotStatusConnecting).Error
	default:
		return fmt.Errorf("不支持的批量动作: %s", req.Action)
	}
	return nil
}
