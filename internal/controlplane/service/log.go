package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/config"
	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/internal/platform/dataroot"
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
			// 入库失败不重试（避免循环放大故障）；日志服务本身的错误经 stderr 暴露即可。
			fmt.Printf("日志入库失败: %v\n", err)
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
	Source       *model.LogSource
	Level        *model.LogLevel
	InstanceID   *uint
	NodeID       *uint
	Keyword      string // 在 message 上做 LIKE %keyword%
	From         *time.Time
	To           *time.Time
	InstanceIDs  []uint // 资源级隔离：非平台管理员收敛到可访问实例集（含平台日志另行放行，见 router）
	Page         int
	PageSize     int
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
