package service

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

// 入库通道与批量参数。日志是高写入路径，故异步缓冲 + 定量/定时批量 Create，
// 避免逐条 INSERT 拖慢采集；通道满则丢弃（与终端事件扇出一致：宁可丢日志不可阻塞实例流）。
const (
	logIngestBuffer = 4096
	logBatchSize    = 256
	logFlushEvery   = 2 * time.Second
	// singleEntryBytes 估算单条日志占用字节（正文 + 列开销），用于把「总量上限 MB」换算为行数阈值，
	// 避免每轮归档都对全表做 LENGTH(message) 求和（SQLite 下昂贵）。
	singleEntryBytes = 512
)

// IngestEntry 是一条待入库日志的最小载荷，由采集侧（实例事件流 / 平台 slog handler）构造。
type IngestEntry struct {
	Source       model.LogSource
	Level        model.LogLevel
	InstanceID   uint
	InstanceUUID string
	NodeID       uint
	Stream       string
	Message      string
	Time         time.Time
}

// LogService 负责日志的采集入库、检索、导出、归档与保留（FR-049）。
//
// 设计要点（守 ADR-005 单二进制不引 ELK、ADR-010 数据根布局）：
//   - 写入：异步缓冲通道 + 批量 Create，采集侧非阻塞；
//   - 检索：过滤维度全部下沉为 DB 谓词（source/level/instance/node/keyword/time），分页 LIMIT/OFFSET，不全量序列化（FR-050 复用）；
//   - 归档：超保留天数或总量上限的旧条目，先按 NDJSON 滚动落盘到数据根 var/log，再从表中批量删除；
//   - 便携：归档路径恒由数据根派生（var/log），整体拷走自洽。
type LogService struct {
	db   *gorm.DB
	root *dataroot.Root
	cfg  config.LogStoreConfig

	ingest chan IngestEntry
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// cache 缓存采集侧 UUID→ID 解析，避免每条实例日志反查数据库。
	cache *resolveCache
}

// NewLogService 创建日志服务。root 用于解析归档目录 var/log，可为 nil（此时归档落盘跳过，仅做表内保留删除）。
func NewLogService(db *gorm.DB, root *dataroot.Root, cfg config.LogStoreConfig) *LogService {
	ctx, cancel := context.WithCancel(context.Background())
	return &LogService{
		db:     db,
		root:   root,
		cfg:    cfg,
		ingest: make(chan IngestEntry, logIngestBuffer),
		ctx:    ctx,
		cancel: cancel,
		cache:  newResolveCache(),
	}
}

// Enabled 报告日志入库是否启用。
func (s *LogService) Enabled() bool { return s.cfg.Enabled }

// Ingest 投递一条日志到异步入库通道。通道满时丢弃并返回 false（不阻塞采集侧）。
func (s *LogService) Ingest(e IngestEntry) bool {
	if !s.cfg.Enabled {
		return false
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	if e.Message == "" {
		return false
	}
	select {
	case s.ingest <- e:
		return true
	default:
		return false
	}
}

// Start 启动后台入库 worker 与归档/保留巡检。幂等性由调用方保证（main 仅调用一次）。
func (s *LogService) Start() {
	if !s.cfg.Enabled {
		return
	}
	s.wg.Add(1)
	go s.runIngestLoop()

	interval := time.Duration(s.cfg.ArchiveIntervalMinutes) * time.Minute
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	s.wg.Add(1)
	go s.runArchiveLoop(interval)
}

// Stop 停止后台循环并刷出剩余缓冲。
func (s *LogService) Stop() {
	s.cancel()
	s.wg.Wait()
}

// runIngestLoop 消费入库通道，按批量大小/时间窗口批量落库。
func (s *LogService) runIngestLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(logFlushEvery)
	defer ticker.Stop()

	batch := make([]model.LogEntry, 0, logBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := s.db.Create(&batch).Error; err != nil {
			// 入库失败不重试（避免循环放大故障）；服务自身错误经 slog 暴露但不再落库（防递归）。
			slog.Error("日志入库失败", "err", err, "count", len(batch), SkipPersist())
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-s.ctx.Done():
			// 退出前尽量排空通道，减少丢失。
			for {
				select {
				case e := <-s.ingest:
					batch = append(batch, toModel(e))
					if len(batch) >= logBatchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case e := <-s.ingest:
			batch = append(batch, toModel(e))
			if len(batch) >= logBatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// toModel 把入库载荷转为持久化模型。
func toModel(e IngestEntry) model.LogEntry {
	return model.LogEntry{
		Source:       e.Source,
		Level:        e.Level,
		InstanceID:   e.InstanceID,
		InstanceUUID: e.InstanceUUID,
		NodeID:       e.NodeID,
		Stream:       e.Stream,
		Message:      e.Message,
		Time:         e.Time,
	}
}

// LogFilter 日志检索过滤条件。所有字段下沉为 DB 谓词，零值表示不过滤。
type LogFilter struct {
	Source      *model.LogSource
	Sources     []model.LogSource
	Level       *model.LogLevel
	InstanceID  *uint
	NodeID      *uint
	Keyword     string // 在 message 上做 LIKE %keyword%
	From        *time.Time
	To          *time.Time
	InstanceIDs []uint // 资源级隔离：非平台管理员收敛到可访问实例集（平台与 Worker 日志由 router 层隔离）
	Page        int
	PageSize    int
	// Cursor 游标分页起点（FR-419）：nil 表示从最新一条开始。仅 QueryCursor 读取。
	Cursor *LogCursor
	// Limit 游标分页单页行数（FR-419）：<=0 取默认，上限与 PageSize 同为 500。仅 QueryCursor 读取。
	Limit int
}

// LogPage 分页查询结果。
type LogPage struct {
	Items    []model.LogEntry `json:"items"`
	Total    int64            `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"pageSize"`
}

// Query 按过滤条件分页检索日志（按时间倒序）。过滤与分页全部在 DB 完成，不全量序列化。
func (s *LogService) Query(filter LogFilter) (*LogPage, error) {
	q := s.applyFilter(s.db.Model(&model.LogEntry{}), filter)

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, fmt.Errorf("统计日志总数失败: %w", err)
	}

	page := filter.Page
	if page <= 0 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 500 {
		pageSize = 500
	}

	var items []model.LogEntry
	if err := q.Order("time DESC").Order("id DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&items).Error; err != nil {
		return nil, fmt.Errorf("查询日志失败: %w", err)
	}

	return &LogPage{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

// 游标分页参数（FR-419）。上限与页码分页的 pageSize 保持同一个 500，避免出现两套「单次最多拿多少」。
const (
	logCursorDefaultLimit = 200
	logCursorMaxLimit     = 500
	// logCursorSep 分隔游标的时间与自增主键。RFC3339Nano 与十进制 ID 都不含下划线，故不会歧义。
	logCursorSep = "_"
)

// LogCursor 是「(时间, 自增主键)」复合游标（FR-419）。
//
// 为什么不用 OFFSET：`/logs` 按 `time DESC, id DESC` 排序，OFFSET 的语义是「跳过前 N 行」，
// 而控制台向上回溯期间新日志正在持续涌入表头——同一个 offset 在两次请求间指向不同的行，
// 于是翻页结果既可能重复也可能丢行。游标把「翻到哪了」表达为数据本身的位置而非序号，
// 表头插入多少行都不影响 `(time, id) < (cursorTime, cursorId)` 的判定。
//
// ID 是必需的第二维：同一毫秒内批量入库的日志时间完全相同（log_ingest 批量 INSERT），
// 只按 time 比较会在时间相等处反复返回同一批或整批跳过。
type LogCursor struct {
	Time time.Time
	ID   uint
}

// String 编码为 `<RFC3339Nano>_<id>`。用 RFC3339Nano 而非 Unix 时间戳，
// 与既有 from/to 参数的时间表达保持一致，且出问题时肉眼可读。
func (c LogCursor) String() string {
	return c.Time.Format(time.RFC3339Nano) + logCursorSep + strconv.FormatUint(uint64(c.ID), 10)
}

// ParseLogCursor 解析游标字符串。非法格式返回错误（由 router 转 400，不静默降级为「从最新开始」
// ——静默降级会让客户端在翻页中途悄悄跳回表头，表现为无限重复加载同一页）。
func ParseLogCursor(s string) (LogCursor, error) {
	idx := strings.LastIndex(s, logCursorSep)
	if idx <= 0 || idx == len(s)-1 {
		return LogCursor{}, fmt.Errorf("游标格式非法: %q", s)
	}
	ts, err := time.Parse(time.RFC3339Nano, s[:idx])
	if err != nil {
		return LogCursor{}, fmt.Errorf("游标时间非法: %w", err)
	}
	id, err := strconv.ParseUint(s[idx+1:], 10, 64)
	if err != nil {
		return LogCursor{}, fmt.Errorf("游标主键非法: %w", err)
	}
	return LogCursor{Time: ts, ID: uint(id)}, nil
}

// LogCursorPage 游标分页结果（FR-419）。
//
// 刻意不带 total：游标分页用于「一直往更早翻」，每页再做一次全表 COUNT 纯属浪费
// （SQLite 下尤其明显），而调用方需要的「还有没有更早的」由 NextCursor 是否为 nil 表达。
type LogCursorPage struct {
	Items []model.LogEntry `json:"items"`
	// NextCursor 下一页（更早）的起点；nil 表示已到最早，没有更早日志。
	NextCursor *string `json:"nextCursor"`
	Limit      int     `json:"limit"`
}

// QueryCursor 按 (time, id) 游标向更早方向分页检索日志（FR-419）。
//
// 与 Query 并存而非取代：日志中心的页码分页需要 total 与任意跳页，控制台回溯需要的是
// 「表头插入不影响的稳定顺序流」，两种语义各用各的入口（spec §4.2）。
//
// 排序与 Query 完全一致（`time DESC, id DESC`），故复合索引 idx_logs_instance_time
// 等既有索引照样吃得上；游标条件写成 `time < ? OR (time = ? AND id < ?)` 的展开形式
// 而非行值比较 `(time,id) < (?,?)`，因为展开形式在 SQLite 与 MySQL 上的索引利用都可靠。
func (s *LogService) QueryCursor(filter LogFilter) (*LogCursorPage, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = logCursorDefaultLimit
	}
	if limit > logCursorMaxLimit {
		limit = logCursorMaxLimit
	}

	q := s.applyFilter(s.db.Model(&model.LogEntry{}), filter)
	if filter.Cursor != nil {
		q = q.Where("time < ? OR (time = ? AND id < ?)", filter.Cursor.Time, filter.Cursor.Time, filter.Cursor.ID)
	}

	// 多取一行探测「是否还有更早的」：拿满 limit+1 才说明后面还有，
	// 否则本页即末页。这样末页不需要额外一次返回空数组的往返。
	var rows []model.LogEntry
	if err := q.Order("time DESC").Order("id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("游标查询日志失败: %w", err)
	}

	page := &LogCursorPage{Limit: limit}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next := LogCursor{Time: last.Time, ID: last.ID}.String()
		page.NextCursor = &next
	}
	// items 恒为非 nil 切片：JSON 里 `null` 与 `[]` 对前端是两种类型，别让调用方分情况处理。
	if rows == nil {
		rows = []model.LogEntry{}
	}
	page.Items = rows
	return page, nil
}

// Export 按过滤条件导出日志（按时间正序，便于阅读），上限 maxRows 防止一次拉取过大。
func (s *LogService) Export(filter LogFilter, maxRows int) ([]model.LogEntry, error) {
	if maxRows <= 0 || maxRows > 50000 {
		maxRows = 50000
	}
	q := s.applyFilter(s.db.Model(&model.LogEntry{}), filter)
	var items []model.LogEntry
	if err := q.Order("time ASC").Order("id ASC").Limit(maxRows).Find(&items).Error; err != nil {
		return nil, fmt.Errorf("导出日志失败: %w", err)
	}
	return items, nil
}

// applyFilter 把过滤条件转为 GORM 链式谓词。
func (s *LogService) applyFilter(q *gorm.DB, filter LogFilter) *gorm.DB {
	if filter.Source != nil {
		q = q.Where("source = ?", *filter.Source)
	}
	if filter.Sources != nil {
		if len(filter.Sources) == 0 {
			q = q.Where("1 = 0")
		} else {
			q = q.Where("source IN ?", filter.Sources)
		}
	}
	if filter.Level != nil {
		q = q.Where("level = ?", *filter.Level)
	}
	if filter.InstanceID != nil {
		q = q.Where("instance_id = ?", *filter.InstanceID)
	}
	if filter.NodeID != nil {
		q = q.Where("node_id = ?", *filter.NodeID)
	}
	if kw := strings.TrimSpace(filter.Keyword); kw != "" {
		q = q.Where("message LIKE ?", "%"+kw+"%")
	}
	if filter.From != nil {
		q = q.Where("time >= ?", *filter.From)
	}
	if filter.To != nil {
		q = q.Where("time <= ?", *filter.To)
	}
	if filter.InstanceIDs != nil {
		// 资源级隔离：仅返回属于可访问实例的日志。
		// 平台日志（instance_id=0）的可见性由 router 层决定是否额外放行。
		if len(filter.InstanceIDs) == 0 {
			q = q.Where("1 = 0")
		} else {
			q = q.Where("instance_id IN ?", filter.InstanceIDs)
		}
	}
	return q
}
