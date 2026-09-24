package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// LogQuerySourceTag 标记查询结果来自哪条路径，供 UI 显式打标（FR-480 / FR-472）。
// 不得把 Federated 与 Legacy 的行/事件统计拼成无标记的精确总量。
type LogQuerySourceTag string

const (
	// LogQuerySourceFederated 新路径（Worker/VL 固定视图事件集合）。
	LogQuerySourceFederated LogQuerySourceTag = "federated"
	// LogQuerySourceLegacy 切换前仍留在 CP logs 表中的存量行（按行，非 Multiline 事件）。
	LogQuerySourceLegacy LogQuerySourceTag = "legacy"
)

// ErrFederatedNotReady 新联邦查询路径尚未就绪（依赖 FR-478/440）。
var ErrFederatedNotReady = errors.New("federated log query path not ready")

// LegacyCoverage 描述 Legacy 可查集合的覆盖边界。
// 既有 NDJSON 归档明确不纳入本批集合。
type LegacyCoverage struct {
	// SourceTag 恒为 legacy，供 UI 打标。
	SourceTag LogQuerySourceTag `json:"sourceTag"`
	// Complete 本批 Legacy 集合是否覆盖全部历史。恒 false：已转 NDJSON 的历史不在集合内。
	Complete bool `json:"complete"`
	// FromTime / ToTime 当前 CP logs 表内 Legacy 行的时间覆盖窗。
	FromTime *time.Time `json:"fromTime,omitempty"`
	ToTime   *time.Time `json:"toTime,omitempty"`
	// NDJSONInScope 是否纳入已有 NDJSON 归档。本批恒 false。
	NDJSONInScope bool `json:"ndjsonInScope"`
	// WorkerCutoffs 逐 Worker 切换水位（已应用的）。
	WorkerCutoffs map[string]time.Time `json:"workerCutoffs,omitempty"`
	// Notes 面向 UI/调用方的覆盖说明。
	Notes []string `json:"notes,omitempty"`
}

// LegacyPage 是 LegacyLogReader 的分页结果。
type LegacyPage struct {
	// SourceTag 恒为 legacy。
	SourceTag LogQuerySourceTag `json:"sourceTag"`
	Items     []model.LogEntry  `json:"items"`
	Total     int64             `json:"total"`
	Page      int               `json:"page"`
	PageSize  int               `json:"pageSize"`
	Coverage  LegacyCoverage    `json:"coverage"`
}

// LegacyLogReader 只读访问切换水位之前仍留在 CP logs 表中的实例/Worker/Node 存量（FR-480）。
//
// 保留预算独立于 platform LogStoreConfig：未到期 Legacy 不受 platform 容量淘汰挤出。
// 已转 NDJSON 的历史明确不纳入本批可查集合，只通过 Coverage 返回覆盖边界。
type LegacyLogReader struct {
	svc     *LogService
	db      *gorm.DB
	cutover *LogCutover

	// RetentionDays Legacy 独立时间保留预算（天）。<=0 表示不按时间清理。
	// 与 platform cfg.RetentionDays 无关。
	RetentionDays int
	// MaxTotalMB Legacy 独立总量预算（MB）。<=0 表示不按总量清理。
	// 与 platform cfg.MaxTotalMB 无关。
	MaxTotalMB int
}

// legacyDefaultRetentionDays 是 Legacy 独立时间预算的默认值。
// 刻意长于 platform 默认 14 天：切换后存量是可解释历史，不应被平台短周期巡检挤出。
const legacyDefaultRetentionDays = 30

// NewLegacyLogReader 创建 Legacy 只读访问器。svc 提供 DB 与过滤谓词；retention 预算独立。
func NewLegacyLogReader(svc *LogService, retentionDays, maxTotalMB int) *LegacyLogReader {
	if retentionDays == 0 && maxTotalMB == 0 {
		// 两字段都为零时给时间预算一个显式默认，避免「忘了设」变成永不清且无文档语义。
		// 调用方可通过负值或 MaxTotalMB>0 且 RetentionDays<=0 表达「只按总量」。
		retentionDays = legacyDefaultRetentionDays
	}
	r := &LegacyLogReader{
		svc:           svc,
		db:            svc.db,
		cutover:       svc.cutover,
		RetentionDays: retentionDays,
		MaxTotalMB:    maxTotalMB,
	}
	return r
}

// Legacy 报告日志服务上的 Legacy 只读访问器。可能为 nil（测试未接线时）。
func (s *LogService) Legacy() *LegacyLogReader {
	if s == nil {
		return nil
	}
	return s.legacy
}

// Query 只读检索仍留在 CP logs 表中的 Legacy 行。
//
// 强制将结果收敛到 instance/worker 来源；NDJSON 归档不读取。
// Coverage 恒标注 SourceTag=legacy 且 Complete=false。
func (r *LegacyLogReader) Query(filter LogFilter) (*LegacyPage, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("legacy reader 未初始化")
	}

	pageNo := filter.Page
	if pageNo <= 0 {
		pageNo = 1
	}
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 500 {
		pageSize = 500
	}

	q := r.baseQuery(filter)

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, fmt.Errorf("统计 Legacy 日志失败: %w", err)
	}

	var items []model.LogEntry
	if err := q.Order("time DESC").Order("id DESC").
		Limit(pageSize).Offset((pageNo - 1) * pageSize).
		Find(&items).Error; err != nil {
		return nil, fmt.Errorf("查询 Legacy 日志失败: %w", err)
	}

	cov, err := r.coverage()
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []model.LogEntry{}
	}

	return &LegacyPage{
		SourceTag: LogQuerySourceLegacy,
		Items:     items,
		Total:     total,
		Page:      pageNo,
		PageSize:  pageSize,
		Coverage:  cov,
	}, nil
}

// baseQuery 组装 Legacy 查询：platform 过滤谓词 + 强制 legacy 来源。
func (r *LegacyLogReader) baseQuery(filter LogFilter) *gorm.DB {
	q := r.db.Model(&model.LogEntry{})
	if r.svc != nil {
		q = r.svc.applyFilter(q, filter)
	}
	// Legacy 集合只含 instance/worker 存量；调用方若指定其他 source，交集为空。
	q = q.Where("source IN ?", legacyLogSources)
	return q
}

// coverage 计算当前表内 Legacy 时间覆盖窗与逐 Worker 水位。
func (r *LegacyLogReader) coverage() (LegacyCoverage, error) {
	cov := LegacyCoverage{
		SourceTag:     LogQuerySourceLegacy,
		Complete:      false,
		NDJSONInScope: false,
		Notes: []string{
			"legacy rows only; archived NDJSON history is out of scope",
			"row-based legacy stats are not equivalent to federated event stats",
		},
	}

	// 用首尾行取时间窗，避免 SQLite 下 MIN/MAX 扫描成 string 的驱动差异。
	var first, last model.LogEntry
	err := r.db.Where("source IN ?", legacyLogSources).
		Order("time ASC").Order("id ASC").First(&first).Error
	if err == nil {
		cov.FromTime = &first.Time
		errLast := r.db.Where("source IN ?", legacyLogSources).
			Order("time DESC").Order("id DESC").First(&last).Error
		if errLast == nil {
			cov.ToTime = &last.Time
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return cov, fmt.Errorf("统计 Legacy 覆盖窗失败: %w", err)
	}

	if r.cutover != nil {
		ws := r.cutover.Watermarks()
		if len(ws) > 0 {
			cov.WorkerCutoffs = make(map[string]time.Time, len(ws))
			for _, w := range ws {
				if w.CutoverApplied && !w.CutoffTime.IsZero() {
					cov.WorkerCutoffs[w.WorkerUUID] = w.CutoffTime
				}
			}
		}
	}
	return cov, nil
}

// RunRetention 按 Legacy 独立预算清理：先时间，再总量。
// 仅作用于 instance/worker 来源；与 platform 容量/保留巡检解耦。
// 返回删除（归档+表内删）条数。
func (r *LegacyLogReader) RunRetention() (int, error) {
	if r == nil || r.db == nil || r.svc == nil {
		return 0, nil
	}
	total := 0
	if n, err := r.archiveBeforeLegacyRetention(); err != nil {
		return total, err
	} else {
		total += n
	}
	if n, err := r.archiveOverLegacyCapacity(); err != nil {
		return total, err
	} else {
		total += n
	}
	return total, nil
}

// archiveBeforeLegacyRetention 清理超过 Legacy 独立时间预算的 instance/worker 行。
func (r *LegacyLogReader) archiveBeforeLegacyRetention() (int, error) {
	if r.RetentionDays <= 0 {
		return 0, nil
	}
	cutoff := time.Now().AddDate(0, 0, -r.RetentionDays)
	removed := 0
	for {
		var batch []model.LogEntry
		if err := r.db.Where("source IN ?", legacyLogSources).
			Where("time < ?", cutoff).
			Order("time ASC").Order("id ASC").
			Limit(archiveBatch).Find(&batch).Error; err != nil {
			return removed, err
		}
		if len(batch) == 0 {
			break
		}
		if err := r.svc.flushArchiveAndDelete(batch); err != nil {
			return removed, err
		}
		removed += len(batch)
		if len(batch) < archiveBatch {
			break
		}
	}
	return removed, nil
}

// archiveOverLegacyCapacity 按 Legacy 独立总量预算裁剪 instance/worker 行。
// 这是 Legacy 自己的容量预算，不是 platform MaxTotalMB。
func (r *LegacyLogReader) archiveOverLegacyCapacity() (int, error) {
	if r.MaxTotalMB <= 0 {
		return 0, nil
	}
	maxRows := int64(r.MaxTotalMB) * 1024 * 1024 / singleEntryBytes
	if maxRows <= 0 {
		return 0, nil
	}
	var count int64
	if err := r.db.Model(&model.LogEntry{}).
		Where("source IN ?", legacyLogSources).Count(&count).Error; err != nil {
		return 0, err
	}
	if count <= maxRows {
		return 0, nil
	}
	toRemove := count - maxRows
	removed := 0
	for toRemove > 0 {
		limit := archiveBatch
		if int64(limit) > toRemove {
			limit = int(toRemove)
		}
		var batch []model.LogEntry
		if err := r.db.Where("source IN ?", legacyLogSources).
			Order("time ASC").Order("id ASC").
			Limit(limit).Find(&batch).Error; err != nil {
			return removed, err
		}
		if len(batch) == 0 {
			break
		}
		if err := r.svc.flushArchiveAndDelete(batch); err != nil {
			return removed, err
		}
		removed += len(batch)
		toRemove -= int64(len(batch))
	}
	return removed, nil
}

// LogQueryResult 是双路径查询单侧结果，携带来源标签供 UI 打标。
type LogQueryResult struct {
	// SourceTag 结果来自 federated 或 legacy。
	SourceTag LogQuerySourceTag `json:"sourceTag"`
	Items     []model.LogEntry  `json:"items"`
	Total     int64             `json:"total"`
	Page      int               `json:"page"`
	PageSize  int               `json:"pageSize"`
	// StatsExact 本结果集合的计数是否可作为「精确统计」。
	// Legacy 仅行集合时为 true（表内完整）；与 Federated 合并解读时必须为 false。
	StatsExact bool            `json:"statsExact"`
	Coverage   *LegacyCoverage `json:"coverage,omitempty"`
	Notes      []string        `json:"notes,omitempty"`
}

// FederatedLogQuerier 新路径（Worker/VL）查询入口。
// FR-478/440 落地前由 UnimplementedFederatedQuerier 占位，不得把空结果当完整。
type FederatedLogQuerier interface {
	Query(ctx context.Context, filter LogFilter) (*LogQueryResult, error)
}

// UnimplementedFederatedQuerier 未就绪的新路径占位：返回结构化 not-ready，而非空成功。
type UnimplementedFederatedQuerier struct{}

// Query 实现 FederatedLogQuerier。
func (UnimplementedFederatedQuerier) Query(_ context.Context, _ LogFilter) (*LogQueryResult, error) {
	return &LogQueryResult{
		SourceTag:  LogQuerySourceFederated,
		Items:      []model.LogEntry{},
		StatsExact: false,
		Notes:      []string{ErrFederatedNotReady.Error()},
	}, ErrFederatedNotReady
}

// CombinedLogStats 双路径汇总：刻意不给出单一精确总量。
type CombinedLogStats struct {
	// FederatedEvents 新路径逻辑事件计数（路径就绪时）。
	FederatedEvents int64 `json:"federatedEvents"`
	// FederatedReady 新路径是否可用。
	FederatedReady bool `json:"federatedReady"`
	// LegacyRows Legacy 表内行计数。
	LegacyRows int64 `json:"legacyRows"`
	// StatsExact 恒 false：行集合与事件集合未建立等价映射前禁止当作精确趋势。
	StatsExact bool                `json:"statsExact"`
	SourceTags []LogQuerySourceTag `json:"sourceTags"`
	Notes      []string            `json:"notes"`
}

// LogDualPath 是 New Federated vs Legacy 的查询门面。
// 两侧结果始终带 SourceTag；合并统计恒 StatsExact=false。
type LogDualPath struct {
	legacy    *LegacyLogReader
	federated FederatedLogQuerier
}

// NewLogDualPath 构造双路径门面。federated 为 nil 时使用 Unimplemented 占位。
func NewLogDualPath(legacy *LegacyLogReader, federated FederatedLogQuerier) *LogDualPath {
	if federated == nil {
		federated = UnimplementedFederatedQuerier{}
	}
	return &LogDualPath{legacy: legacy, federated: federated}
}

// DualPath 报告日志服务上的双路径门面。
func (s *LogService) DualPath() *LogDualPath {
	if s == nil {
		return nil
	}
	return s.dual
}

// QueryLegacy 走 Legacy 只读路径，结果带 sourceTag=legacy。
func (d *LogDualPath) QueryLegacy(filter LogFilter) (*LogQueryResult, error) {
	if d == nil || d.legacy == nil {
		return nil, fmt.Errorf("legacy path 未初始化")
	}
	page, err := d.legacy.Query(filter)
	if err != nil {
		return nil, err
	}
	cov := page.Coverage
	return &LogQueryResult{
		SourceTag:  LogQuerySourceLegacy,
		Items:      page.Items,
		Total:      page.Total,
		Page:       page.Page,
		PageSize:   page.PageSize,
		StatsExact: true, // 表内 Legacy 行集合自身是可数的；不代表 NDJSON 或 federated
		Coverage:   &cov,
		Notes: []string{
			"legacy path: CP logs rows only",
			"do not merge with federated event counts as exact",
		},
	}, nil
}

// QueryFederated 走新路径，结果带 sourceTag=federated。
// 路径未就绪时返回 ErrFederatedNotReady，调用方不得把空 items 当作完整零结果。
func (d *LogDualPath) QueryFederated(ctx context.Context, filter LogFilter) (*LogQueryResult, error) {
	if d == nil {
		return nil, fmt.Errorf("dual-path 未初始化")
	}
	return d.federated.Query(ctx, filter)
}

// QueryBoth 分别返回两条路径的结果，不合并行。
// 任一路径失败时仍返回另一侧（若可用），并在 Notes 中标明失败原因。
func (d *LogDualPath) QueryBoth(ctx context.Context, filter LogFilter) (federated *LogQueryResult, legacy *LogQueryResult, err error) {
	var notes []string
	fed, fedErr := d.QueryFederated(ctx, filter)
	if fedErr != nil {
		notes = append(notes, "federated: "+fedErr.Error())
		if fed != nil {
			fed.Notes = append(fed.Notes, notes...)
		}
	}
	leg, legErr := d.QueryLegacy(filter)
	if legErr != nil {
		notes = append(notes, "legacy: "+legErr.Error())
	}
	if fedErr != nil && legErr != nil {
		return fed, leg, fmt.Errorf("dual-path 查询失败: federated=%v legacy=%v", fedErr, legErr)
	}
	// 单侧失败不视为整体成功合并——调用方须按 SourceTag 分别展示。
	return fed, leg, nil
}

// CombinedStats 汇总两侧计数。恒 StatsExact=false：旧日志按行、新日志按 Multiline 事件时，
// 未建立等价映射前禁止拼接精确趋势（spec §3.1）。
func (d *LogDualPath) CombinedStats(ctx context.Context, filter LogFilter) (*CombinedLogStats, error) {
	if d == nil {
		return nil, fmt.Errorf("dual-path 未初始化")
	}
	out := &CombinedLogStats{
		StatsExact: false,
		SourceTags: []LogQuerySourceTag{},
		Notes: []string{
			"row-based legacy and event-based federated stats are not exact when combined",
			"UI must label source tags; do not chart a single merged exact trend",
		},
	}

	if leg, err := d.QueryLegacy(filter); err == nil {
		out.LegacyRows = leg.Total
		out.SourceTags = append(out.SourceTags, LogQuerySourceLegacy)
	} else {
		out.Notes = append(out.Notes, "legacy stats unavailable: "+err.Error())
	}

	fed, fedErr := d.QueryFederated(ctx, filter)
	if fedErr == nil && fed != nil {
		out.FederatedReady = true
		out.FederatedEvents = fed.Total
		out.SourceTags = append(out.SourceTags, LogQuerySourceFederated)
	} else if fedErr != nil {
		out.FederatedReady = false
		out.Notes = append(out.Notes, "federated stats unavailable: "+fedErr.Error())
	}
	return out, nil
}
