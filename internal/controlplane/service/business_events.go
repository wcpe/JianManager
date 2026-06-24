package service

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// economyDomain 是经济业务域名（与探针侧 EconomyEventEnvelope.DOMAIN / 业务命令 domain 一致）。
const economyDomain = "economy"

// BusinessEventService 汇聚 JBIS 业务域事件（FR-116 底座 / FR-122 经济，见 ADR-027/028）。
//
// 它是业务事件的**上行入口**（探针经反向 WS 桥 → Worker → gRPC StreamPluginEvents → CP），与 BusinessService
// 的**下行**命令派发（business.go）刻意分离：二者方向、存储、幂等维度均不同（FR-121 在改 business.go 写路径，
// 本服务不碰之）。
//
// 职责：
//  1. 通用 envelope 去重落库（按 (domain, dedupKey) insert-or-ignore，应对桥的至少一次投递与跨节点重试）；
//  2. 经济域结构化镜像维护（按 node→zone 维度，seq 单调推进，跨区同名玩家不串味/不重复计数）；
//  3. 经济变更审计 append（按 ledgerId 去重，业务数据不降采样不丢，ADR-028）；
//  4. 只读聚合查询（业务事件流 / 经济镜像 / 跨区聚合）。
//
// 数据所有权（架构不变量）：仅 CP 读写 DB；JM 存的是汇聚镜像 + 审计，余额真源仍在各服 mce 库。
type BusinessEventService struct {
	db *gorm.DB
}

// NewBusinessEventService 创建业务事件汇聚服务。
func NewBusinessEventService(db *gorm.DB) *BusinessEventService {
	return &BusinessEventService{db: db}
}

// economyEventData 是经济业务事件帧 data 字段的载荷（与探针侧 EconomyEventEnvelope 的字段键名逐字一致）。
// 探针折算时已全部字符串化（金额禁浮点）；CP 解析后按需转回数值。
type economyEventData struct {
	PlayerName   string `json:"playerName"`
	CurrencyID   string `json:"currencyId"` // mce Int 主键（字符串）
	Currency     string `json:"currency"`   // 全局稳定 identifier
	ZoneID       string `json:"zoneId"`
	EntryType    string `json:"entryType"`
	SignedAmount string `json:"signedAmount"`
	BalanceAfter string `json:"balanceAfter"`
	LedgerID     string `json:"ledgerId"`
	Seq          string `json:"seq"`
	OccurredAt   string `json:"occurredAt"`
}

// bridgeFrame 是探针 event 帧的最小解析结构（仅取业务事件汇聚需要的字段）。
// data 保留为 RawMessage，按 domain 交对应解析器（当前仅 economy）。
type bridgeFrame struct {
	Data json.RawMessage `json:"data"`
}

// Ingest 汇聚一条业务域事件（domain 非空时由 PlayerEventService 的事件流回调路由进来）。
//
// 先按 (domain, dedupKey) 落通用 envelope（insert-or-ignore 去重）；economy 域再解析 data 维护结构化镜像
// 与审计。任何环节失败仅 WARN 降级、绝不 panic（汇聚是 best-effort，单条坏事件不得拖垮事件流）。
//
// nodeUUID 为事件来源节点（聚合 node→zone 维度起点），由调用方从所属 Worker 流上下文带入。
func (s *BusinessEventService) Ingest(nodeUUID string, evt *workerpb.PluginEvent) {
	if s == nil || s.db == nil || evt == nil {
		return
	}
	domain := strings.TrimSpace(evt.Domain)
	if domain == "" {
		return // 非业务事件（监控/治理），不归本服务
	}

	inserted, err := s.recordEnvelope(nodeUUID, domain, evt)
	if err != nil {
		slog.Warn("业务事件 envelope 落库失败", "domain", domain, "dedupKey", evt.DedupKey, "err", err)
		return
	}
	// 重复投递（已存在同 (domain,dedupKey)）：envelope 不重复计数；结构化镜像/审计同样跳过，避免重放。
	if !inserted {
		return
	}

	if domain == economyDomain {
		if err := s.applyEconomy(nodeUUID, evt); err != nil {
			slog.Warn("经济事件结构化落库失败", "dedupKey", evt.DedupKey, "err", err)
		}
	}
}

// recordEnvelope 按 (domain, dedupKey) 去重落通用 envelope；返回是否为本次新插入（false=重复投递已存在）。
func (s *BusinessEventService) recordEnvelope(nodeUUID, domain string, evt *workerpb.PluginEvent) (bool, error) {
	row := &model.BusinessEvent{
		Domain:       domain,
		DedupKey:     evt.DedupKey,
		Action:       evt.Type,
		NodeUUID:     nodeUUID,
		InstanceUUID: evt.InstanceUuid,
		PayloadJSON:  evt.RawJson,
		OccurredAt:   evt.Timestamp,
	}
	// dedupKey 为空时退化为不去重（仍落库，但无唯一约束保护）：由上层保证业务事件携非空 dedupKey。
	res := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "domain"}, {Name: "dedup_key"}},
		DoNothing: true,
	}).Create(row)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// applyEconomy 解析经济事件 data，维护结构化镜像（seq 单调）+ append 审计（按 ledgerId 去重）。
func (s *BusinessEventService) applyEconomy(nodeUUID string, evt *workerpb.PluginEvent) error {
	d, ok := parseEconomyData(evt.RawJson)
	if !ok {
		return errors.New("经济事件 data 解析失败或缺字段")
	}
	ledgerID, _ := strconv.ParseInt(strings.TrimSpace(d.LedgerID), 10, 64)
	seq, _ := strconv.ParseInt(strings.TrimSpace(d.Seq), 10, 64)
	occurredAt, _ := strconv.ParseInt(strings.TrimSpace(d.OccurredAt), 10, 64)
	currencyID, _ := strconv.Atoi(strings.TrimSpace(d.CurrencyID))

	// 审计 append（按 ledgerId 去重，与 envelope 同源；业务数据不降采样不丢，ADR-028）。
	if err := s.appendLedger(nodeUUID, evt.InstanceUuid, d, ledgerID, seq, currencyID, occurredAt); err != nil {
		return err
	}
	// 结构化镜像 upsert（node→zone 维度，seq 单调推进，乱序/旧事件不回退余额）。
	return s.upsertMirror(nodeUUID, d, ledgerID, seq, currencyID, occurredAt)
}

// appendLedger 追加经济变更审计行；按 ledgerId 去重（重发不重复留痕）。
func (s *BusinessEventService) appendLedger(
	nodeUUID, instanceUUID string, d economyEventData, ledgerID, seq int64, currencyID int, occurredAt int64,
) error {
	row := &model.EconomyLedgerEntry{
		LedgerID:     ledgerID,
		NodeUUID:     nodeUUID,
		InstanceUUID: instanceUUID,
		ZoneID:       d.ZoneID,
		PlayerName:   d.PlayerName,
		Currency:     d.Currency,
		CurrencyID:   currencyID,
		EntryType:    d.EntryType,
		SignedAmount: d.SignedAmount,
		BalanceAfter: d.BalanceAfter,
		Seq:          seq,
		OccurredAt:   occurredAt,
	}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "ledger_id"}},
		DoNothing: true,
	}).Create(row).Error
}

// upsertMirror 维护经济镜像最新余额：仅当来量 seq ≥ 已存 seq 时覆盖（防乱序/重发回退余额）。
//
// 聚合维度 (NodeUUID, ZoneID, PlayerName, Currency) 唯一——node→zone 确保跨区同名玩家独立镜像，不串味/不重复计数。
// 用 OnConflict + WHERE 守卫保证单调性；首见组合直接插入。
func (s *BusinessEventService) upsertMirror(
	nodeUUID string, d economyEventData, ledgerID, seq int64, currencyID int, occurredAt int64,
) error {
	row := &model.EconomyBalanceMirror{
		NodeUUID:      nodeUUID,
		ZoneID:        d.ZoneID,
		PlayerName:    d.PlayerName,
		Currency:      d.Currency,
		CurrencyID:    currencyID,
		Balance:       d.BalanceAfter,
		LastSeq:       seq,
		LastLedgerID:  ledgerID,
		LastEntryType: d.EntryType,
		OccurredAt:    occurredAt,
	}
	// 冲突即同一 (node,zone,player,currency)：仅当新 seq ≥ 旧 seq 才覆盖（WHERE 守卫保证单调，挡乱序回退）。
	return s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "node_uuid"}, {Name: "zone_id"}, {Name: "player_name"}, {Name: "currency"},
		},
		Where: clause.Where{Exprs: []clause.Expression{
			gorm.Expr("economy_balance_mirrors.last_seq <= ?", seq),
		}},
		DoUpdates: clause.AssignmentColumns([]string{
			"balance", "last_seq", "last_ledger_id", "last_entry_type", "currency_id", "occurred_at", "updated_at",
		}),
	}).Create(row).Error
}

// ======================== 只读查询（FR-122 读端点 / FR-123 读契约起点） ========================

// BusinessEventQuery 业务事件流查询条件（读端点）。
type BusinessEventQuery struct {
	Domain   string // 必填：按业务域过滤（economy…）
	NodeUUID string // 可选：按来源节点过滤
	Limit    int    // 取最近 N 条（默认/上限收敛见实现）
}

// ListBusinessEvents 按域倒序取最近业务事件（通用 envelope 视图，插件无关）。
func (s *BusinessEventService) ListBusinessEvents(q BusinessEventQuery) ([]model.BusinessEvent, error) {
	tx := s.db.Model(&model.BusinessEvent{})
	if q.Domain != "" {
		tx = tx.Where("domain = ?", q.Domain)
	}
	if q.NodeUUID != "" {
		tx = tx.Where("node_uuid = ?", q.NodeUUID)
	}
	var out []model.BusinessEvent
	if err := tx.Order("id DESC").Limit(clampLimit(q.Limit)).Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// EconomyMirrorQuery 经济镜像查询条件。
type EconomyMirrorQuery struct {
	PlayerName string // 可选：按玩家名过滤（跨区会返回该玩家在各 node→zone 的多行）
	Currency   string // 可选：按货币 identifier 过滤
	NodeUUID   string // 可选：按节点过滤
	ZoneID     string // 可选：按区过滤
	Limit      int
}

// ListEconomyMirror 查经济镜像最新余额（按 node→zone 维度逐行，跨区同名玩家分行不混）。
func (s *BusinessEventService) ListEconomyMirror(q EconomyMirrorQuery) ([]model.EconomyBalanceMirror, error) {
	tx := s.db.Model(&model.EconomyBalanceMirror{})
	if q.PlayerName != "" {
		tx = tx.Where("player_name = ?", q.PlayerName)
	}
	if q.Currency != "" {
		tx = tx.Where("currency = ?", q.Currency)
	}
	if q.NodeUUID != "" {
		tx = tx.Where("node_uuid = ?", q.NodeUUID)
	}
	if q.ZoneID != "" {
		tx = tx.Where("zone_id = ?", q.ZoneID)
	}
	var out []model.EconomyBalanceMirror
	if err := tx.Order("player_name ASC, currency ASC, node_uuid ASC, zone_id ASC").
		Limit(clampLimit(q.Limit)).Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// EconomyAggregateRow 跨区聚合一行：某玩家某货币按 (node, zone) 维度的余额（聚合视图基元）。
//
// 聚合刻意**保留 node→zone 维度**而非简单求和：mce 账户按 zoneId 隔离、同名玩家跨区独立，跨区余额语义上
// 是不同账户，不能盲目相加（会重复计数/串味）。前端可据本视图选择"按区展开"或"显式跨区求和"（须用户知情）。
type EconomyAggregateRow struct {
	PlayerName string `json:"playerName"`
	Currency   string `json:"currency"`
	NodeUUID   string `json:"nodeUuid"`
	ZoneID     string `json:"zoneId"`
	Balance    string `json:"balance"`
}

// AggregateEconomyByZone 取某玩家（某货币，可选）在各 node→zone 的余额明细，供跨区聚合展示。
//
// 返回逐 (node, zone) 行而非合计：跨区是否相加由调用方/前端按业务语义决定（见 [EconomyAggregateRow]）。
func (s *BusinessEventService) AggregateEconomyByZone(playerName, currency string) ([]EconomyAggregateRow, error) {
	if strings.TrimSpace(playerName) == "" {
		return nil, errors.New("playerName 必填")
	}
	tx := s.db.Model(&model.EconomyBalanceMirror{}).Where("player_name = ?", playerName)
	if currency != "" {
		tx = tx.Where("currency = ?", currency)
	}
	var out []EconomyAggregateRow
	if err := tx.Select("player_name", "currency", "node_uuid", "zone_id", "balance").
		Order("currency ASC, node_uuid ASC, zone_id ASC").Scan(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// EconomyLeaderboardQuery 经济排行查询条件（FR-123，旁路实现：mce 无 leaderboard API）。
type EconomyLeaderboardQuery struct {
	Currency string // 必填：排行锚定单一货币（跨货币余额不可比）
	ZoneID   string // 可选：限定某区
	NodeUUID string // 可选：限定某节点
	Limit    int    // Top-N，复用 clampLimit（默认 100，上限 500）
}

// EconomyLeaderboardRow 排行一行：某 (node, zone) 内某玩家某货币的余额 + 名次。
type EconomyLeaderboardRow struct {
	Rank       int    `json:"rank"`
	PlayerName string `json:"playerName"`
	Currency   string `json:"currency"`
	NodeUUID   string `json:"nodeUuid"`
	ZoneID     string `json:"zoneId"`
	Balance    string `json:"balance"`
}

// LeaderboardEconomy 取某货币余额倒序的 Top-N（FR-123 旁路排行：从 JM 自有镜像表派生，不穿透探针）。
//
// 排序难点：[model.EconomyBalanceMirror.Balance] 是字符串承载的 BigDecimal（FR-122 禁浮点防精度失真），
// 字符串字典序对数值排序错误（"99.9" 会排在 "1000" 前）。故按数据库方言选数值 CAST 做 ORDER BY：
// MySQL 用 DECIMAL(65,18) 精确十进制序、SQLite 用 REAL 数值化排序（dev 足够）。
//
// 逐 (node, zone) 行返回（与 mirror/aggregate 同口径）：同名玩家跨区是不同账户、各占一行参与排行，
// 不合并不串味（mce 账户按 zoneId 隔离）。Currency 必填——跨货币不可比。
func (s *BusinessEventService) LeaderboardEconomy(q EconomyLeaderboardQuery) ([]EconomyLeaderboardRow, error) {
	if strings.TrimSpace(q.Currency) == "" {
		return nil, errors.New("currency 必填")
	}
	tx := s.db.Model(&model.EconomyBalanceMirror{}).Where("currency = ?", q.Currency)
	if q.ZoneID != "" {
		tx = tx.Where("zone_id = ?", q.ZoneID)
	}
	if q.NodeUUID != "" {
		tx = tx.Where("node_uuid = ?", q.NodeUUID)
	}
	var rows []EconomyLeaderboardRow
	if err := tx.Select("player_name", "currency", "node_uuid", "zone_id", "balance").
		Order(balanceNumericDescExpr(s.db) + ", player_name ASC").
		Limit(clampLimit(q.Limit)).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i].Rank = i + 1
	}
	return rows, nil
}

// balanceNumericDescExpr 按数据库方言返回「余额数值倒序」的 ORDER BY 片段。
// 余额列是字符串十进制，须数值化排序：MySQL 用 DECIMAL(65,18)（精确无浮点损失）、其它（sqlite）用 REAL。
func balanceNumericDescExpr(db *gorm.DB) string {
	if db.Dialector != nil && db.Dialector.Name() == "mysql" {
		return "CAST(balance AS DECIMAL(65,18)) DESC"
	}
	return "CAST(balance AS REAL) DESC"
}

// clampLimit 收敛查询条数到 [1, businessEventMaxLimit]，缺省取 businessEventDefaultLimit。
func clampLimit(limit int) int {
	switch {
	case limit <= 0:
		return businessEventDefaultLimit
	case limit > businessEventMaxLimit:
		return businessEventMaxLimit
	default:
		return limit
	}
}

// 业务事件读端点条数收敛常量。
const (
	businessEventDefaultLimit = 100
	businessEventMaxLimit     = 500
)

// parseEconomyData 从 event 帧原文 RawJson 提取并校验经济 data 载荷。
// data 缺失/非法或关键字段（playerName/currency/ledgerId）为空时返回 ok=false（不落结构化，envelope 已留原文）。
func parseEconomyData(rawJSON string) (economyEventData, bool) {
	var d economyEventData
	if rawJSON == "" {
		return d, false
	}
	var frame bridgeFrame
	if err := json.Unmarshal([]byte(rawJSON), &frame); err != nil || len(frame.Data) == 0 {
		return d, false
	}
	if err := json.Unmarshal(frame.Data, &d); err != nil {
		return d, false
	}
	if d.PlayerName == "" || d.Currency == "" || strings.TrimSpace(d.LedgerID) == "" {
		return d, false
	}
	return d, true
}
