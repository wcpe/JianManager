package service

import (
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// ClientDistTrackingService 客户端分发拉取/下载追踪（FR-093，见 ADR-023）。
//
// 数据量治理：明细 `client_dist_event` 短保留 + 后台滚动清理；写时增量 upsert 聚合 `client_dist_daily` 长保留。
// 写入弱一致 best-effort——失败仅返回错误供调用方忽略，**绝不阻断玩家拉取**。
type ClientDistTrackingService struct {
	db            *gorm.DB
	retentionDays int
	cleanupEvery  time.Duration
	stop          chan struct{}
}

// NewClientDistTrackingService 创建追踪服务（明细默认保留 14 天，每 6h 清理）。
func NewClientDistTrackingService(db *gorm.DB) *ClientDistTrackingService {
	return &ClientDistTrackingService{
		db:            db,
		retentionDays: 14,
		cleanupEvery:  6 * time.Hour,
		stop:          make(chan struct{}),
	}
}

// ClientDistEventInput 一次拉取/下载事件输入。
type ClientDistEventInput struct {
	ChannelID   string
	MachineID   string
	IP          string
	Kind        string // manifest | artifact
	Version     int
	ArtifactSHA string
	Bytes       int64
	Status      int
	DurationMs  int64
}

// Record 记录一次拉取/下载事件：写明细 + 写时增量 upsert 当日聚合。best-effort（失败不阻断）。
func (s *ClientDistTrackingService) Record(e ClientDistEventInput) error {
	// 制品端点跨频道共享、路径无频道段，故 ChannelID 可空（按 kind 全局聚合）；仅 kind 空才跳过。
	if s == nil || e.Kind == "" {
		return nil
	}
	if len(e.MachineID) > machineIDMaxLen {
		e.MachineID = e.MachineID[:machineIDMaxLen]
	}
	now := time.Now()
	ev := &model.ClientDistEvent{
		ChannelID:   e.ChannelID,
		MachineID:   e.MachineID,
		IP:          e.IP,
		Kind:        e.Kind,
		Version:     e.Version,
		ArtifactSHA: e.ArtifactSHA,
		Bytes:       e.Bytes,
		Status:      e.Status,
		DurationMs:  e.DurationMs,
		CreatedAt:   now,
	}
	if err := s.db.Create(ev).Error; err != nil {
		return fmt.Errorf("写分发明细失败: %w", err)
	}
	day := now.UTC().Format("2006-01-02")
	return s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "day"}, {Name: "channel_id"}, {Name: "version"}, {Name: "kind"}},
		DoUpdates: clause.Assignments(map[string]any{
			"requests": gorm.Expr("requests + 1"),
			"bytes":    gorm.Expr("bytes + ?", e.Bytes),
		}),
	}).Create(&model.ClientDistDaily{
		Day: day, ChannelID: e.ChannelID, Version: e.Version, Kind: e.Kind, Requests: 1, Bytes: e.Bytes,
	}).Error
}

// Cleanup 删除早于保留期的明细行（聚合长留）；返回删除行数。
func (s *ClientDistTrackingService) Cleanup() (int64, error) {
	cutoff := time.Now().Add(-time.Duration(s.retentionDays) * 24 * time.Hour)
	res := s.db.Where("created_at < ?", cutoff).Delete(&model.ClientDistEvent{})
	return res.RowsAffected, res.Error
}

// Start 启动后台滚动清理循环（FR-093 数据量治理，仿 FR-060）。
func (s *ClientDistTrackingService) Start() {
	go func() {
		t := time.NewTicker(s.cleanupEvery)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				_, _ = s.Cleanup()
			}
		}
	}()
}

// Stop 停止后台清理循环。
func (s *ClientDistTrackingService) Stop() {
	close(s.stop)
}

// ClientDistEventFilter 明细检索过滤条件（FR-093 检索）。空字段不约束。
type ClientDistEventFilter struct {
	ChannelID string
	MachineID string
	IP        string
	Kind      string
	Version   *int
	Since     *time.Time
	Until     *time.Time
	Limit     int
}

// QueryEvents 按条件检索明细（created_at DESC）。供管理面追溯（IP/机器码/频道/版本/时间）。
func (s *ClientDistTrackingService) QueryEvents(f ClientDistEventFilter) ([]model.ClientDistEvent, error) {
	q := s.db.Model(&model.ClientDistEvent{})
	if f.ChannelID != "" {
		q = q.Where("channel_id = ?", f.ChannelID)
	}
	if f.MachineID != "" {
		q = q.Where("machine_id = ?", f.MachineID)
	}
	if f.IP != "" {
		q = q.Where("ip = ?", f.IP)
	}
	if f.Kind != "" {
		q = q.Where("kind = ?", f.Kind)
	}
	if f.Version != nil {
		q = q.Where("version = ?", *f.Version)
	}
	if f.Since != nil {
		q = q.Where("created_at >= ?", *f.Since)
	}
	if f.Until != nil {
		q = q.Where("created_at <= ?", *f.Until)
	}
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	var events []model.ClientDistEvent
	if err := q.Order("created_at DESC").Limit(limit).Find(&events).Error; err != nil {
		return nil, fmt.Errorf("检索分发明细失败: %w", err)
	}
	return events, nil
}
