package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 每实例最新托管状态 + 对账矩阵读模型。
//
// ## 连接状态为什么必须合成
// 探针自报的 `connection` 走的是**同一条插件桥**。桥断开时 config_state 根本发不出去，
// 库里留着的就是断连前最后一次的值——一台已经掉线半小时的机器会显示 `connected`。
// 所以 CP 侧必须以**桥连接事件**（connected / disconnected）为主证据合成，
// 探针自报值单独暴露为 `reportedConnection` 供排障（它能区分 degraded / unauthorized，
// 后者表示控制面拒了凭据、探针不会降级到文件配置，属需要告警的异常）。
//
// ## 筛选必须作用在合成值上
// 一致性判定与连接状态都在**写时**算好落列（effective_connection / consistency 不落列，
// 见下），筛选走 SQL 而不是 Go 侧过滤：面向 1000+ 子服时把全表读进内存再筛会拖垮 CP。
// 一致性因为有「期望版本」这个跨表值，无法直接落列，故用查询条件表达（见 Matrix 的说明）。

// ManagedInstanceStateService 维护「每实例最新托管状态」。
type ManagedInstanceStateService struct {
	db *gorm.DB
}

// NewManagedInstanceStateService 创建状态服务。
func NewManagedInstanceStateService(db *gorm.DB) *ManagedInstanceStateService {
	return &ManagedInstanceStateService{db: db}
}

// UpsertFromReport 用一条 config_state 上报覆盖式更新该实例的最新状态。
//
// 桥连接证据**不由本方法改写**：上报能到达就证明桥是通的，但那属于「观测事实」，
// 连接状态的真源是桥的 connected/disconnected 事件。这里只把 BridgeConnected 置 true
// （能收到帧说明连接确实存在），断开由 MarkBridgeDisconnected 负责。
//
// `cacheAgeSeconds` 原样搬运（含 null）：null = 从未成功应用过，与 0（刚刚生效过）语义相反。
func (s *ManagedInstanceStateService) UpsertFromReport(inst model.Instance, report ManagedConfigState) error {
	now := time.Now()
	effective := model.SynthesizeManagedConnection(true, report.Connection)

	var effectiveJSON string
	if len(report.Effective) > 0 {
		if raw, err := json.Marshal(report.Effective); err == nil {
			effectiveJSON = string(raw)
		}
	}
	var capsJSON string
	if len(report.Capabilities) > 0 {
		if raw, err := json.Marshal(report.Capabilities); err == nil {
			capsJSON = string(raw)
		}
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		var existing model.ManagedInstanceConfigState
		err := tx.Where("instance_id = ?", inst.ID).First(&existing).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			row := model.ManagedInstanceConfigState{
				InstanceID:          inst.ID,
				InstanceUUID:        inst.UUID,
				ConfigVersion:       report.ConfigVersion,
				EffectiveJSON:       effectiveJSON,
				CacheAgeSeconds:     report.CacheAgeSecond, // 指针原样搬运，保留 null
				Mode:                report.Mode,
				ReportedConnection:  report.Connection,
				BridgeConnected:     true,
				EffectiveConnection: effective,
				CapabilitiesJSON:    capsJSON,
				LastError:           truncateManagedText(report.LastError, 1000),
				ReportedAt:          &now,
				BridgeUpdatedAt:     &now,
			}
			if cerr := tx.Create(&row).Error; cerr != nil {
				return fmt.Errorf("创建托管状态失败: %w", cerr)
			}
			return nil
		case err != nil:
			return fmt.Errorf("查询托管状态失败: %w", err)
		}
		// 覆盖式更新：这一行表达「最近一次」，不是历史。
		// bridge_connected / bridge_updated_at 不在此处改写：连接证据只由桥的
		// connected/disconnected 事件维护。事件与本上报在同一条流上但由并发 goroutine
		// 落库，乱序是常态——若上报也把 bridge_connected 置真，一条迟到的 config_state
		// 会把刚刚标记的断开重新盖成连接，矩阵显示永不掉线。
		// 同理 effective_connection 按库里的连接证据合成，不能硬写 true。
		return tx.Model(&model.ManagedInstanceConfigState{}).Where("id = ?", existing.ID).
			Updates(map[string]any{
				"instance_uuid":        inst.UUID,
				"config_version":       report.ConfigVersion,
				"effective_json":       effectiveJSON,
				"cache_age_seconds":    report.CacheAgeSecond,
				"mode":                 report.Mode,
				"reported_connection":  report.Connection,
				"effective_connection": model.SynthesizeManagedConnection(existing.BridgeConnected, report.Connection),
				"capabilities_json":    capsJSON,
				"last_error":           truncateManagedText(report.LastError, 1000),
				"reported_at":          now,
			}).Error
	})
}

// MarkBridgeConnected 记录桥（重）连事件。
//
// 只改连接证据，不动 config_version / cache_age_seconds：那些是探针的观测值，
// 桥连上不等于探针已上报过新值，抹掉它们会让对账矩阵在重连瞬间显示成「从未上报」。
//
// `reportedPrevious` 参数的意义：桥断开期间探针自报值已不可信，重连后要等它再报一次
// 才能采信；在此之前合成值按「桥已连、自报未知」处理（→ connected），
// 而不是沿用断连前的旧自报值（那可能仍是 unauthorized/degraded）。
func (s *ManagedInstanceStateService) MarkBridgeConnected(inst model.Instance) error {
	now := time.Now()
	return s.db.Transaction(func(tx *gorm.DB) error {
		var existing model.ManagedInstanceConfigState
		err := tx.Where("instance_id = ?", inst.ID).First(&existing).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			row := model.ManagedInstanceConfigState{
				InstanceID:          inst.ID,
				InstanceUUID:        inst.UUID,
				BridgeConnected:     true,
				EffectiveConnection: model.ManagedConnectionConnected,
				BridgeUpdatedAt:     &now,
			}
			if cerr := tx.Create(&row).Error; cerr != nil {
				return fmt.Errorf("创建托管状态失败: %w", cerr)
			}
			return nil
		case err != nil:
			return fmt.Errorf("查询托管状态失败: %w", err)
		}
		return tx.Model(&model.ManagedInstanceConfigState{}).Where("id = ?", existing.ID).
			Updates(map[string]any{
				"bridge_connected":     true,
				"effective_connection": model.SynthesizeManagedConnection(true, ""),
				"reported_connection":  "",
				"bridge_updated_at":    now,
			}).Error
	})
}

// MarkBridgeDisconnected 记录桥断开事件：合成值立刻置 disconnected。
//
// **必须保留** config_version 与 cache_age_seconds 的旧值——它们是「最后已知观测」，
// 抹掉会让对账矩阵丢掉唯一能判断「断开前同步到哪」的依据。
// 但 reported_connection 要清空：断连后它已不可信，留着会被误读为当前状态。
func (s *ManagedInstanceStateService) MarkBridgeDisconnected(instanceID uint) error {
	now := time.Now()
	return s.db.Model(&model.ManagedInstanceConfigState{}).
		Where("instance_id = ?", instanceID).
		Updates(map[string]any{
			"bridge_connected":     false,
			"effective_connection": model.ManagedConnectionDisconnected,
			"reported_connection":  "",
			"bridge_updated_at":    now,
		}).Error
}

// Get 读某实例的最新状态（不存在返回 ok=false）。
func (s *ManagedInstanceStateService) Get(instanceID uint) (model.ManagedInstanceConfigState, bool, error) {
	var row model.ManagedInstanceConfigState
	err := s.db.Where("instance_id = ?", instanceID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.ManagedInstanceConfigState{}, false, nil
	}
	if err != nil {
		return model.ManagedInstanceConfigState{}, false, fmt.Errorf("查询托管状态失败: %w", err)
	}
	return row, true, nil
}

// ---------------------------------------------------------------------------
// 对账矩阵
// ---------------------------------------------------------------------------

// ManagedMatrixFilter 是矩阵的筛选条件（全部下推 SQL）。
//
// 注意 `Configuration` 与 `Connection` 两维的语义：
//   - Configuration（配置来源）：file = 探针按本地 config.yml 跑（观测版本 0 / 未托管），
//     cp = 已由 CP 下发过（有期望版本）；drift 也可以算作「来源不一致」的单列看待，故单独一档。
//   - Connection（连接状态）：**合成后**的值，与展示严格同源。
type ManagedMatrixFilter struct {
	// Consistency 一致性筛选（inSync / drift / neverReported / failed）。
	Consistency string
	// Connection 合成后的连接状态筛选。
	Connection string
	// Configuration 配置来源筛选（file / cp）。
	Configuration string
	// Keyword 实例名/UUID 模糊搜索。
	Keyword string
	// NodeID 限定节点（0 = 不限）。
	NodeID uint
	// AccessibleInstanceIDs 实例可见性收敛（nil = 不限）。
	//
	// 与 NodeID 等筛选条件的关系是**与**：它表达「这个用户本来能看见哪些实例」，
	// 不是用户主动选的筛选项，故不参与 summary 的可选筛选集合——否则用户会看到一个
	// 被自己权限裁剪过的计数却没有任何提示。
	AccessibleInstanceIDs []uint
	// Page/PageSize 分页。
	Page     int
	PageSize int
}

// ManagedMatrixRow 是对账矩阵的一行（每实例一行）。
type ManagedMatrixRow struct {
	InstanceID   uint   `json:"instanceId"`
	InstanceUUID string `json:"instanceUuid"`
	InstanceName string `json:"instanceName"`
	NodeID       uint   `json:"nodeId"`
	NodeName     string `json:"nodeName,omitempty"`

	// DesiredVersion CP 侧最近一次下发的目标版本；**从未下发用 null，不是 0**。
	//
	// 用 0 表示「没下发过」会与「下发过且版本恰好是 0」混淆——而 0 正是探针首次托管前的
	// 版本号，两者混在一列会让「从未下发」的机器看起来像「下发了初始版本」。
	DesiredVersion *int64 `json:"desiredVersion"`
	// ObservedVersion 探针上报的版本；从未上报为 null。
	ObservedVersion *int64 `json:"observedVersion"`
	// Mode 探针运行模式（自报，原样保留）。
	Mode string `json:"mode,omitempty"`
	// Connection 合成后的连接状态。
	Connection string `json:"connection"`
	// ReportedConnection 探针自报的连接态（**可能因断连而过期**，仅供排障）。
	ReportedConnection string `json:"reportedConnection,omitempty"`
	// CacheAgeSeconds 距上次成功应用的秒数；null 与 0 语义不同。
	CacheAgeSeconds *int64 `json:"cacheAgeSeconds"`
	// LastError 探针自报的最近错误。
	LastError string `json:"lastError,omitempty"`
	// ReportedAt 最近一次状态上报时刻。
	ReportedAt *time.Time `json:"reportedAt,omitempty"`
	// Consistency 一致性判定（inSync / drift / neverReported / failed）。
	Consistency string `json:"consistency"`
	// LastTargetState 最近一次下发目标的终端状态（CP 侧经历）。
	LastTargetState string `json:"lastTargetState,omitempty"`
	// LastPushError 最近一次下发的错误文案（CP 侧）。
	LastPushError string `json:"lastPushError,omitempty"`
	// Configuration 配置来源（file / cp）：CP 有没有下发过期望版本。
	Configuration string `json:"configuration"`
}

// ManagedMatrixSummary 是矩阵的按维汇总（前端画两维热力/计数用）。
type ManagedMatrixSummary struct {
	// Total 总行数（受筛选前的全量）。
	Total int64 `json:"total"`
	// ByConsistency 一致性分布。
	ByConsistency map[string]int64 `json:"byConsistency"`
	// ByConnection 合成连接状态分布。
	ByConnection map[string]int64 `json:"byConnection"`
	// ByConfiguration 配置来源分布。
	ByConfiguration map[string]int64 `json:"byConfiguration"`
	// CrossMatrix 配置来源 × 连接状态 的交叉计数（两维展示的数据源）。
	CrossMatrix []ManagedMatrixCell `json:"crossMatrix"`
}

// ManagedMatrixCell 是交叉矩阵的一格。
type ManagedMatrixCell struct {
	Configuration string `json:"configuration"`
	Connection    string `json:"connection"`
	Count         int64  `json:"count"`
}

// ManagedMatrixService 对账矩阵读模型。
type ManagedMatrixService struct {
	db     *gorm.DB
	state  *ManagedInstanceStateService
	pushes *ManagedPushService
}

// NewManagedMatrixService 创建矩阵读模型服务。
func NewManagedMatrixService(db *gorm.DB, state *ManagedInstanceStateService, pushes *ManagedPushService) *ManagedMatrixService {
	return &ManagedMatrixService{db: db, state: state, pushes: pushes}
}

// Matrix 查询对账矩阵（分页 + 全下推 SQL）。
//
// ## 为什么要用 SQL 表达一致性而不是 Go 侧过滤
// 一致性依赖「期望版本」（来自目标表的最近一条）与「观测版本」（来自状态表）两个来源。
// 把两表 JOIN 出候选行后，一致性判定可以完全用 SQL 条件表达：
//
//	failed        ← 最近目标状态 ∈ {failed, rejected, timeout}
//	neverReported ← 状态行不存在 或 config_version IS NULL
//	inSync        ← COALESCE(期望,0) = 观测版本
//	drift         ← 其余（有观测且不等）
//
// 这样分页与总数都由数据库算，1000+ 实例时不会把整表读进内存。
func (s *ManagedMatrixService) Matrix(filter ManagedMatrixFilter) ([]ManagedMatrixRow, int64, error) {
	page, pageSize := normalizeManagedPage(filter.Page, filter.PageSize)

	// 第一个 JOIN：每实例最近一条目标（用 MAX(id) 定位，id 单调所以等价于「最近」）。
	// 第二个 JOIN：每实例的最新状态（唯一索引保证最多一行）。
	//
	// 用 LEFT JOIN 而非 INNER：从未下发过、也从未上报过的实例同样要出现在矩阵里
	//（「这台机器到底托管了没有」正是矩阵要回答的问题之一）。
	base := s.db.Table("instances AS i").
		Joins("LEFT JOIN nodes AS n ON n.id = i.node_id").
		Joins(`LEFT JOIN managed_push_targets AS t ON t.id = (
			SELECT MAX(t2.id) FROM managed_push_targets AS t2
			WHERE t2.instance_id = i.id AND t2.desired_version > 0
		)`).
		Joins("LEFT JOIN managed_instance_config_states AS s ON s.instance_id = i.id").
		Where("i.deleted_at IS NULL")

	if filter.NodeID > 0 {
		base = base.Where("i.node_id = ?", filter.NodeID)
	}
	base = restrictMatrixInstances(base, filter)
	if keyword := strings.TrimSpace(filter.Keyword); keyword != "" {
		like := "%" + keyword + "%"
		base = base.Where("(i.name LIKE ? OR i.uuid LIKE ?)", like, like)
	}

	// 一致性筛选：作用在合成后的判定上（与展示同源）。
	switch filter.Consistency {
	case model.ManagedConsistencyFailed:
		base = base.Where("t.state IN ?", []string{
			string(model.ManagedPushTargetFailed),
			string(model.ManagedPushTargetRejected),
			string(model.ManagedPushTargetTimeout),
		})
	case model.ManagedConsistencyNeverReported:
		base = base.Where(
			"(s.id IS NULL OR s.reported_at IS NULL) AND (t.state IS NULL OR t.state NOT IN ?)",
			[]string{
				string(model.ManagedPushTargetFailed),
				string(model.ManagedPushTargetRejected),
				string(model.ManagedPushTargetTimeout),
			})
	case model.ManagedConsistencyInSync:
		base = base.Where(managedInSyncCondition()).
			Where("t.state IS NULL OR t.state NOT IN ?", []string{
				string(model.ManagedPushTargetFailed),
				string(model.ManagedPushTargetRejected),
				string(model.ManagedPushTargetTimeout),
			})
	case model.ManagedConsistencyDrift:
		base = base.Where(managedDriftCondition()).
			Where("t.state IS NULL OR t.state NOT IN ?", []string{
				string(model.ManagedPushTargetFailed),
				string(model.ManagedPushTargetRejected),
				string(model.ManagedPushTargetTimeout),
			})
	}

	// 连接状态筛选：**合成值**列，与展示同源。
	if conn := strings.TrimSpace(filter.Connection); conn != "" {
		if filter.Connection == model.ManagedConnectionDisconnected {
			// 从未上报过的实例在库里没有状态行，合成语义同样是 disconnected（桥没连）。
			base = base.Where("(s.effective_connection = ? OR s.id IS NULL)", conn)
		} else {
			base = base.Where("s.effective_connection = ?", conn)
		}
	}

	// 配置来源筛选：CP 有没有下发过期望版本。
	switch filter.Configuration {
	case "file":
		base = base.Where("t.desired_version IS NULL")
	case "cp":
		base = base.Where("t.desired_version IS NOT NULL")
	}

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("统计对账矩阵失败: %w", err)
	}

	var rows []managedMatrixRawRow
	err := base.Select(`i.id AS instance_id, i.uuid AS instance_uuid, i.name AS instance_name,
		i.node_id AS node_id, COALESCE(n.name, '') AS node_name,
		t.desired_version AS desired_version, t.state AS last_target_state, t.error AS last_push_error,
		s.config_version AS observed_version, s.mode AS mode,
		s.effective_connection AS effective_connection, s.reported_connection AS reported_connection,
		s.cache_age_seconds AS cache_age_seconds, s.last_error AS last_error, s.reported_at AS reported_at`).
		Order("i.id ASC").
		Offset((page - 1) * pageSize).Limit(pageSize).
		Scan(&rows).Error
	if err != nil {
		return nil, 0, fmt.Errorf("查询对账矩阵失败: %w", err)
	}

	out := make([]ManagedMatrixRow, 0, len(rows))
	for _, raw := range rows {
		out = append(out, raw.toRow())
	}
	return out, total, nil
}

// managedMatrixRawRow 是 SQL 扫描的中间结构（列可为 NULL，故全部用指针）。
type managedMatrixRawRow struct {
	InstanceID      uint       `gorm:"column:instance_id"`
	InstanceUUID    string     `gorm:"column:instance_uuid"`
	InstanceName    string     `gorm:"column:instance_name"`
	NodeID          uint       `gorm:"column:node_id"`
	NodeName        string     `gorm:"column:node_name"`
	DesiredVersion  *int64     `gorm:"column:desired_version"`
	LastTargetState *string    `gorm:"column:last_target_state"`
	LastPushError   *string    `gorm:"column:last_push_error"`
	ObservedVersion *int64     `gorm:"column:observed_version"`
	Mode            *string    `gorm:"column:mode"`
	EffectiveConn   *string    `gorm:"column:effective_connection"`
	ReportedConn    *string    `gorm:"column:reported_connection"`
	CacheAgeSeconds *int64     `gorm:"column:cache_age_seconds"`
	LastError       *string    `gorm:"column:last_error"`
	ReportedAt      *time.Time `gorm:"column:reported_at"`
}

// toRow 把扫描结果转成 API 行（含一致性判定与配置来源）。
func (r managedMatrixRawRow) toRow() ManagedMatrixRow {
	row := ManagedMatrixRow{
		InstanceID:      r.InstanceID,
		InstanceUUID:    r.InstanceUUID,
		InstanceName:    r.InstanceName,
		NodeID:          r.NodeID,
		NodeName:        r.NodeName,
		DesiredVersion:  r.DesiredVersion,
		ObservedVersion: r.ObservedVersion,
		CacheAgeSeconds: r.CacheAgeSeconds, // 原样搬运：null 与 0 不可混
		ReportedAt:      r.ReportedAt,
	}
	if r.Mode != nil {
		row.Mode = *r.Mode
	}
	if r.LastError != nil {
		row.LastError = *r.LastError
	}
	if r.LastPushError != nil {
		row.LastPushError = *r.LastPushError
	}
	if r.LastTargetState != nil {
		row.LastTargetState = *r.LastTargetState
	}

	// 连接状态：库里存的是**写时合成好**的值；没有状态行（从未上报）按桥未连处理。
	if r.EffectiveConn != nil {
		row.Connection = *r.EffectiveConn
	} else {
		row.Connection = model.SynthesizeManagedConnection(false, "")
	}
	if r.ReportedConn != nil {
		row.ReportedConnection = *r.ReportedConn
	}

	// 观测侧：**没有上报时刻就等于没有观测**。
	//
	// `config_version` 是 NOT NULL DEFAULT 0 的列，所以「行存在」不代表「探针报过」——
	// MarkBridgeConnected 会为刚连上的实例建一行，只填连接证据，config_version 留默认 0。
	// 若直接按列值扫出 0，这类行会被判成「观测到第 0 版」，与 SQL 筛选条件
	// （要求 reported_at IS NOT NULL）互相矛盾：列表显示 inSync，用 inSync 筛选却筛不出来。
	// 故此处以 reported_at 作为「是否真的收到过 config_state」的判据，与 SQL 同口径。
	observed := r.ObservedVersion
	if r.ReportedAt == nil {
		observed = nil
	}
	row.ObservedVersion = observed

	// 一致性：与 SQL 筛选条件同口径（JudgeManagedConsistency 是唯一真源）。
	row.Consistency = model.JudgeManagedConsistency(row.LastTargetState, r.DesiredVersion, observed)

	// 配置来源：CP 有没有下发过期望版本。
	if r.DesiredVersion != nil {
		row.Configuration = "cp"
	} else {
		row.Configuration = "file"
	}
	return row
}

// managedInSyncCondition 返回「期望 = 观测」的 SQL 条件。
//
// 期望为空按 0 计（从未下发 → CP 没有期望）；观测必须存在。
//
// **判据是 `s.id IS NOT NULL AND s.reported_at IS NOT NULL`，不是 `config_version IS NOT NULL`**：
// config_version 是 NOT NULL DEFAULT 0 的列，永远非空，写它等于没写；而「行存在但没上报时刻」
// 是真实存在的形态（MarkBridgeConnected 为刚连上的实例建行时不填 reported_at）。
// 这两条必须与 toRow 里 observed = (reported_at == nil ? nil : config_version) 严格对应，
// 否则「列表显示的结果」与「按该档位筛出来的结果」会互相矛盾。
func managedInSyncCondition() string {
	return "(s.id IS NOT NULL AND s.reported_at IS NOT NULL AND s.config_version = COALESCE(t.desired_version, 0))"
}

// managedDriftCondition 返回「期望 ≠ 观测」的 SQL 条件（与 inSync 同源的补集）。
func managedDriftCondition() string {
	return "(s.id IS NOT NULL AND s.reported_at IS NOT NULL AND s.config_version <> COALESCE(t.desired_version, 0))"
}

// Summary 计算矩阵的按维汇总与两维交叉计数。
//
// 与 Matrix 分开而不是一次返回：汇总要不带分页（全量口径），而列表要分页。
// 两者共用同一套筛选条件（buildMatrixBase），保证「筛出来的行数」与「汇总的计数」一致。
func (s *ManagedMatrixService) Summary(filter ManagedMatrixFilter) (*ManagedMatrixSummary, error) {
	out := &ManagedMatrixSummary{
		ByConsistency:   map[string]int64{},
		ByConnection:    map[string]int64{},
		ByConfiguration: map[string]int64{},
		CrossMatrix:     []ManagedMatrixCell{},
	}
	// 汇总不带分页，也不受一致性/连接筛选影响（它是「这一屏能筛出什么」的引导）。
	//
	// 但**必须**受节点/关键字约束：否则运营盯着某个节点，汇总却在报全平台数字。
	// 汇总也必须带实例可见性范围：只传 NodeID/Keyword 会让受限用户看到
	// 全局连接与一致性计数，等于泄露其他组的实例状态统计。
	scopeFilter := ManagedMatrixFilter{NodeID: filter.NodeID, Keyword: filter.Keyword, AccessibleInstanceIDs: filter.AccessibleInstanceIDs}
	base := s.buildMatrixBase(scopeFilter)
	if err := base.Count(&out.Total).Error; err != nil {
		return nil, fmt.Errorf("统计对账矩阵失败: %w", err)
	}

	var rawRows []managedMatrixRawRow
	err := base.Select(`i.id AS instance_id,
		t.desired_version AS desired_version, t.state AS last_target_state,
		s.config_version AS observed_version,
		s.effective_connection AS effective_connection, s.reported_at AS reported_at`).
		Scan(&rawRows).Error
	if err != nil {
		return nil, fmt.Errorf("统计对账矩阵分布失败: %w", err)
	}

	cross := map[string]int64{}
	for _, raw := range rawRows {
		row := raw.toRow()
		out.ByConsistency[row.Consistency]++
		out.ByConnection[row.Connection]++
		out.ByConfiguration[row.Configuration]++
		cross[row.Configuration+"|"+row.Connection]++
	}
	for key, count := range cross {
		parts := strings.SplitN(key, "|", 2)
		out.CrossMatrix = append(out.CrossMatrix, ManagedMatrixCell{
			Configuration: parts[0], Connection: parts[1], Count: count,
		})
	}
	// 稳定输出：先按配置来源、再按连接状态排序（前端画格子的顺序要可预期）。
	sortMatrixCells(out.CrossMatrix)
	return out, nil
}

// Detail 下钻：单个实例的托管配置详情（期望 / 观测 / 生效值 / 最近下发经历）。
type ManagedMatrixDetail struct {
	Row ManagedMatrixRow `json:"row"`
	// DesiredValues 最近一次下发冻结的 values（CP 期望的配置）。
	DesiredValues map[string]any `json:"desiredValues"`
	// ObservedEffective 探针回报的生效值。
	ObservedEffective map[string]any `json:"observedEffective"`
	// Traced 逐键来源（哪个 scope、哪一版）。
	Traced []ManagedTracedKey `json:"traced"`
	// RecentPushes 最近若干次下发经历。
	RecentPushes []model.ManagedPushTarget `json:"recentPushes"`
}

// Detail 读取某实例的托管配置对账详情。
func (s *ManagedMatrixService) Detail(instanceID uint) (*ManagedMatrixDetail, error) {
	var inst model.Instance
	if err := s.db.First(&inst, instanceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrManagedInstanceNotFound
		}
		return nil, fmt.Errorf("查询实例失败: %w", err)
	}
	out := &ManagedMatrixDetail{
		DesiredValues:     map[string]any{},
		ObservedEffective: map[string]any{},
		Traced:            []ManagedTracedKey{},
		RecentPushes:      []model.ManagedPushTarget{},
	}

	var node model.Node
	if err := s.db.Select("name").First(&node, inst.NodeID).Error; err == nil {
		out.Row.NodeName = node.Name
	}
	out.Row.InstanceID = inst.ID
	out.Row.InstanceUUID = inst.UUID
	out.Row.InstanceName = inst.Name
	out.Row.NodeID = inst.NodeID

	var stateRow model.ManagedInstanceConfigState
	if err := s.db.Where("instance_id = ?", inst.ID).First(&stateRow).Error; err == nil {
		// 与矩阵行同口径：reported_at 为 null 表示从未收到过 config_state，
		// config_version 的默认 0 不是「观测到第 0 版」，故观测版本置 null。
		if stateRow.ReportedAt != nil {
			out.Row.ObservedVersion = &stateRow.ConfigVersion
		}
		out.Row.Mode = stateRow.Mode
		out.Row.ReportedConnection = stateRow.ReportedConnection
		out.Row.CacheAgeSeconds = stateRow.CacheAgeSeconds
		out.Row.LastError = stateRow.LastError
		out.Row.ReportedAt = stateRow.ReportedAt
		out.Row.Connection = stateRow.EffectiveConnection
		if stateRow.EffectiveJSON != "" {
			_ = json.Unmarshal([]byte(stateRow.EffectiveJSON), &out.ObservedEffective)
		}
	} else {
		out.Row.Connection = model.SynthesizeManagedConnection(false, "")
	}

	// 最近一次下发（任意状态，取最新一条）。
	var lastTarget model.ManagedPushTarget
	if err := s.db.Where("instance_id = ?", inst.ID).Order("id DESC").First(&lastTarget).Error; err == nil {
		out.Row.DesiredVersion = &lastTarget.DesiredVersion
		out.Row.LastTargetState = string(lastTarget.State)
		out.Row.LastPushError = lastTarget.Error
		// 期望内容必须取**这台实例实际收到的那份**（PushedPayloadJSON）：
		// 会话级的 ValuesJSON/TracedJSON 只是首批第一台实例的代表值，不同实例的
		// node/instance 层合并结果不同——拿会话级值当本机期望值，既显示错，
		// 还把另一台实例的层值泄给了这台的查看者。
		if lastTarget.PushedPayloadJSON != "" {
			var pushed ManagedConfigPush
			if uerr := json.Unmarshal([]byte(lastTarget.PushedPayloadJSON), &pushed); uerr == nil {
				out.DesiredValues = pushed.Values
			}
		}
		// Traced 没有按目标留存；用本实例当前层的合并来源近似（只含本实例的值，不泄密）。
		if s.pushes != nil && s.pushes.config != nil {
			if merged, merr := s.pushes.config.MergeForInstance(inst); merr == nil && merged != nil {
				out.Traced = merged.Traced
			}
		}
	}
	if out.Row.DesiredVersion != nil {
		out.Row.Configuration = "cp"
	} else {
		out.Row.Configuration = "file"
	}
	out.Row.Consistency = model.JudgeManagedConsistency(
		out.Row.LastTargetState, out.Row.DesiredVersion, out.Row.ObservedVersion)

	var recent []model.ManagedPushTarget
	if err := s.db.Where("instance_id = ?", inst.ID).Order("id DESC").Limit(10).Find(&recent).Error; err == nil {
		out.RecentPushes = recent
	}
	return out, nil
}

// ErrManagedInstanceNotFound 实例不存在。
var ErrManagedInstanceNotFound = errors.New("实例不存在")

// buildMatrixBase 构造矩阵的公共查询（供 Matrix 与 Summary 共用同一套筛选口径）。
func (s *ManagedMatrixService) buildMatrixBase(filter ManagedMatrixFilter) *gorm.DB {
	// 两个口径与 Matrix 保持一致：
	//  - 目标行只取实际推出去过的（desired_version > 0）：待推送/被跳过的行 desired_version=0，
	//    取它们当“最近一次下发”会把本来已同步的实例误判成期望=0；
	//  - 软删实例不进矩阵（raw SQL 不过 GORM 的软删作用域）。
	base := s.db.Table("instances AS i").
		Joins("LEFT JOIN nodes AS n ON n.id = i.node_id").
		Joins(`LEFT JOIN managed_push_targets AS t ON t.id = (
			SELECT MAX(t2.id) FROM managed_push_targets AS t2
			WHERE t2.instance_id = i.id AND t2.desired_version > 0
		)`).
		Joins("LEFT JOIN managed_instance_config_states AS s ON s.instance_id = i.id").
		Where("i.deleted_at IS NULL")
	if filter.NodeID > 0 {
		base = base.Where("i.node_id = ?", filter.NodeID)
	}
	base = restrictMatrixInstances(base, filter)
	if keyword := strings.TrimSpace(filter.Keyword); keyword != "" {
		like := "%" + keyword + "%"
		base = base.Where("(i.name LIKE ? OR i.uuid LIKE ?)", like, like)
	}
	return base
}

// restrictMatrixInstances 按调用方的实例可见性收敛查询。
//
// nil 表示不受限（平台管理员或未见 AccessibleInstanceIDs 的调用方）；**空但非 nil 的切片
// 表示「一个都不许看」**，必须落成恒假条件而不是跳过——否则「无可见实例的用户」会看到全表。
// 这个区分只能用 nil 与空切片表达，故设计为切片而非 `(ids, all bool)` 两个参数：
// 后者在调用方漏传 all 时默认成 true，是更危险的失败方向。
func restrictMatrixInstances(q *gorm.DB, filter ManagedMatrixFilter) *gorm.DB {
	if filter.AccessibleInstanceIDs == nil {
		return q
	}
	if len(filter.AccessibleInstanceIDs) == 0 {
		return q.Where("1 = 0")
	}
	return q.Where("i.id IN ?", filter.AccessibleInstanceIDs)
}

// sortMatrixCells 稳定排序交叉矩阵（配置来源优先，其次连接状态）。
func sortMatrixCells(cells []ManagedMatrixCell) {
	order := func(configuration string) int {
		switch configuration {
		case "cp":
			return 0
		case "file":
			return 1
		}
		return 2
	}
	for i := 1; i < len(cells); i++ {
		for j := i; j > 0; j-- {
			less := order(cells[j].Configuration) < order(cells[j-1].Configuration) ||
				(order(cells[j].Configuration) == order(cells[j-1].Configuration) &&
					cells[j].Connection < cells[j-1].Connection)
			if !less {
				break
			}
			cells[j], cells[j-1] = cells[j-1], cells[j]
		}
	}
}
