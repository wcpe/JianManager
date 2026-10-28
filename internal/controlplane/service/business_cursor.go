package service

import (
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 业务事件位点账本的查询与保留（ADR-107 §3 / §5）。
//
// 边界（一眼可见，改代码前先读）：
//   - 本服务**只读位点**，从不返回 payload——表里没有 payload 列，这里也不制造它；
//   - 不计算「缺失区间」：JM 不知道业务台收到了什么，缺口只能由业务台拿自己的已收位点做差集
//     （ADR-107 §3 的理由段落，不要"优化"成 JM 单方算差集）；
//   - 裁剪是**定期巡检**，不是「每次查询顺手删」——后者会把读请求变成写放大。
const (
	// BusinessCursorQueryDefaultLimit 缺口查询默认条数（ADR-107 §3）。
	BusinessCursorQueryDefaultLimit = 1000
	// BusinessCursorQueryMaxLimit 缺口查询条数上限（ADR-107 §3）。
	BusinessCursorQueryMaxLimit = 5000
	// BusinessCursorRetentionDays 位点账本默认保留天数（ADR-107 §5）。
	BusinessCursorRetentionDays = 7
	// businessCursorRetentionTick 裁剪巡检周期。位点是低频运维数据，
	// 每小时巡检一次即可；保留期以「天」计，没有更细的必要。
	businessCursorRetentionTick = time.Hour
	// businessCursorPruneChunk 单轮裁剪每批删除的行数。
	//
	// 为什么要分批：7 天 × 100 实例 × 20 事件/秒可达上亿行，一条 DELETE 会长时间持锁
	// （SQLite 直接锁库、MySQL 产生大事务与主从延迟）。分批 + 批间让出，把长事务切成短事务。
	businessCursorPruneChunk = 5_000
	// businessCursorPruneMaxRounds 单次巡检最多删多少批，防止一波清空把 CP 拖住。
	// 剩余行下一拍（一小时后再来）继续清，长期上仍然收敛。
	businessCursorPruneMaxRounds = 200
)

// ErrBusinessCursorInvalidQuery 缺口查询参数非法。
var ErrBusinessCursorInvalidQuery = errors.New("缺口查询参数非法")

// BusinessCursorItem 是一条位点的对外表示（**只有位点，无 payload**）。
type BusinessCursorItem struct {
	// StreamSeq CP 全局位点（= SSE `id`），跨实例可比。
	StreamSeq int64 `json:"streamSeq"`
	// Seq 探针分配的 per-domain 序号；探针未上报时为 0。
	Seq int64 `json:"seq"`
	// DedupKey 插件提供的幂等锚点。
	DedupKey string `json:"dedupKey,omitempty"`
	// OccurredAt 事件发生时间（epoch 毫秒）。
	OccurredAt int64 `json:"occurredAt"`
}

// BusinessCursorQuery 缺口查询条件（对应 GET /business/cursor 的查询串）。
type BusinessCursorQuery struct {
	// InstanceID 必填：位点账本按实例分区，缺实例无从定位。
	InstanceID uint
	// Domain 必填：探针 seq 只在同一 domain 内单调，跨域比较无意义。
	Domain string
	// FromSeq 起始探针位点（含）；0 表示不限下界。
	FromSeq int64
	// ToSeq 结束探针位点（含）；0 表示不限上界。
	ToSeq int64
	// Limit 返回条数上限；<=0 取默认，超出上限钳制到上限。
	Limit int
}

// BusinessCursorService 位点账本的读侧与保留期裁剪（ADR-107）。
type BusinessCursorService struct {
	db *gorm.DB
	// retentionDays 保留天数（来自 CP 配置 business_cursor.retention_days）；<=0 走默认 7 天。
	retentionDays int

	mu      sync.Mutex
	running bool
	stopCh  chan struct{}
}

// NewBusinessCursorService 创建位点账本查询/裁剪服务。retentionDays<=0 取默认 7 天。
func NewBusinessCursorService(db *gorm.DB, retentionDays int) *BusinessCursorService {
	return &BusinessCursorService{db: db, retentionDays: retentionDays, stopCh: make(chan struct{})}
}

// NormalizeLimit 把请求条数归一到 [1, 上限]；<=0 用默认值。
func NormalizeBusinessCursorLimit(limit int) int {
	if limit <= 0 {
		return BusinessCursorQueryDefaultLimit
	}
	if limit > BusinessCursorQueryMaxLimit {
		return BusinessCursorQueryMaxLimit
	}
	return limit
}

// Query 返回该 (实例, 域) 区间内**存在过的位点**，按探针 seq 升序。
//
// 无数据返回**空切片**而非错误：ADR-107 §3 明确「无数据返回空数组（不是 404）」，
// 因为「区间内没有位点」本身就是一条有效答案（说明这段时间该实例没产生该域事件，
// 或位点已被保留期裁掉——两种都让业务台更保守地对账，不该被当成故障）。
func (s *BusinessCursorService) Query(q BusinessCursorQuery) ([]BusinessCursorItem, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("位点账本未启用")
	}
	domain := strings.TrimSpace(q.Domain)
	if q.InstanceID == 0 || domain == "" {
		return nil, ErrBusinessCursorInvalidQuery
	}
	dbq := s.db.Model(&model.BusinessEventCursor{}).
		Where("instance_id = ? AND domain = ?", q.InstanceID, domain)
	if q.FromSeq > 0 {
		dbq = dbq.Where("seq >= ?", q.FromSeq)
	}
	if q.ToSeq > 0 {
		dbq = dbq.Where("seq <= ?", q.ToSeq)
	}
	var rows []model.BusinessEventCursor
	if err := dbq.
		Select("stream_seq", "seq", "dedup_key", "occurred_at").
		Order("seq ASC, stream_seq ASC").
		Limit(NormalizeBusinessCursorLimit(q.Limit)).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	// 显式构造空切片：nil 切片会被 json 序列化成 null，而契约要求 []。
	out := make([]BusinessCursorItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, BusinessCursorItem{
			StreamSeq:  r.StreamSeq,
			Seq:        r.Seq,
			DedupKey:   r.DedupKey,
			OccurredAt: r.OccurredAt,
		})
	}
	return out, nil
}

// Start 启动保留期裁剪巡检（幂等）。
func (s *BusinessCursorService) Start() {
	if s == nil || s.db == nil {
		return
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	stop := restartableStop(&s.stopCh)
	s.mu.Unlock()

	go func() {
		ticker := time.NewTicker(businessCursorRetentionTick)
		defer ticker.Stop()
		s.PruneOnce()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				s.PruneOnce()
			}
		}
	}()
	slog.Info("业务事件位点账本裁剪巡检已启动", "retentionDays", s.RetentionDays())
}

// Stop 停止裁剪巡检。
func (s *BusinessCursorService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	close(s.stopCh)
	s.running = false
}

// RetentionDays 取保留天数（CP 配置 business_cursor.retention_days，默认 7）。
func (s *BusinessCursorService) RetentionDays() int {
	if s == nil || s.retentionDays < 1 {
		return BusinessCursorRetentionDays
	}
	return s.retentionDays
}

// PruneOnce 删除早于保留窗口的位点行，返回本轮删除条数（便于测试断言）。
//
// 只删过期行：谓词恒为 `created_at < cutoff`，不碰任何未过期记录。
// 用 created_at（CP 接收时钟）而不是 occurred_at（探针时钟）：探针时钟偏斜会让
// 慢钟实例的行永不删除、快钟实例的行刚写就被删。
//
// **为什么先查 id 再按 id 批删，而不是 `DELETE ... LIMIT`**：GORM 在 SQLite 上会静默忽略
// Delete 的 Limit（实测生成的是无 LIMIT 的 DELETE），一条语句删掉整段过期数据就是长事务——
// SQLite 直接锁库、MySQL 产生大事务与主从延迟。故这里显式取一批主键再按主键删，
// 保证每批语句边界清楚，且在两种数据库上行为一致。
func (s *BusinessCursorService) PruneOnce() int {
	if s == nil || s.db == nil {
		return 0
	}
	cutoff := time.Now().AddDate(0, 0, -s.RetentionDays())
	total := 0
	for round := 0; round < businessCursorPruneMaxRounds; round++ {
		var ids []uint
		if err := s.db.Model(&model.BusinessEventCursor{}).
			Where("created_at < ?", cutoff).
			Order("id ASC").
			Limit(businessCursorPruneChunk).
			Pluck("id", &ids).Error; err != nil {
			slog.Warn("查询待裁剪业务事件位点失败", "error", err, "deleted", total)
			return total
		}
		if len(ids) == 0 {
			break
		}
		res := s.db.Where("id IN ?", ids).Delete(&model.BusinessEventCursor{})
		if res.Error != nil {
			slog.Warn("裁剪业务事件位点失败", "error", res.Error, "deleted", total)
			return total
		}
		total += int(res.RowsAffected)
		if len(ids) < businessCursorPruneChunk {
			// 该批已不足一整批：过期区间取完。
			break
		}
	}
	if total > 0 {
		slog.Info("已裁剪过期业务事件位点", "deleted", total, "cutoff", cutoff)
	}
	return total
}
