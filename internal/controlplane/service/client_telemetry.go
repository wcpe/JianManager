package service

import (
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// ClientTelemetryService 客户端遥测落库与治理（FR-094，见 ADR-023）。
// 明细短保留 + 后台滚动清理；按 result 写时增量日聚合长保留。Record best-effort 弱一致、失败不阻塞客户端（202）。
type ClientTelemetryService struct {
	db            *gorm.DB
	retentionDays int
	cleanupEvery  time.Duration
	stop          chan struct{}
}

// NewClientTelemetryService 创建遥测服务（明细默认保留 14 天，每 6h 清理）。
func NewClientTelemetryService(db *gorm.DB) *ClientTelemetryService {
	return &ClientTelemetryService{db: db, retentionDays: 14, cleanupEvery: 6 * time.Hour, stop: make(chan struct{})}
}

// ClientTelemetryInput 一条遥测上报。
type ClientTelemetryInput struct {
	ChannelID   string
	MachineID   string
	IP          string
	Result      string
	FromVersion int
	ToVersion   int
	OS          string
	JavaVersion string
	Launcher    string
	DurationMs  int64
	BootSuccess bool
	Error       string
}

// validTelemetryResult 合法 result 取值（契约 §4.3）；其它归一为 error 防脏数据。
func validTelemetryResult(r string) string {
	switch r {
	case "success", "fail-static", "rolled-back", "error":
		return r
	}
	return "error"
}

// Record 落一条遥测明细 + 按 result 日聚合 upsert。best-effort（失败返回错误供调用方忽略）。
func (s *ClientTelemetryService) Record(e ClientTelemetryInput) error {
	if s == nil {
		return nil
	}
	if len(e.MachineID) > machineIDMaxLen {
		e.MachineID = e.MachineID[:machineIDMaxLen]
	}
	result := validTelemetryResult(e.Result)
	now := time.Now()
	row := &model.ClientTelemetry{
		ChannelID: e.ChannelID, MachineID: e.MachineID, IP: e.IP, Result: result,
		FromVersion: e.FromVersion, ToVersion: e.ToVersion, OS: trunc(e.OS, 32),
		JavaVersion: trunc(e.JavaVersion, 32), Launcher: trunc(e.Launcher, 32),
		DurationMs: e.DurationMs, BootSuccess: e.BootSuccess, Error: trunc(e.Error, 512), CreatedAt: now,
	}
	if err := s.db.Create(row).Error; err != nil {
		return fmt.Errorf("写遥测明细失败: %w", err)
	}
	day := now.UTC().Format("2006-01-02")
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "day"}, {Name: "channel_id"}, {Name: "result"}},
		DoUpdates: clause.Assignments(map[string]any{"count": gorm.Expr("count + 1")}),
	}).Create(&model.ClientTelemetryDaily{Day: day, ChannelID: e.ChannelID, Result: result, Count: 1}).Error
}

// Cleanup 删除早于保留期的遥测明细（聚合长留）。
func (s *ClientTelemetryService) Cleanup() (int64, error) {
	cutoff := time.Now().Add(-time.Duration(s.retentionDays) * 24 * time.Hour)
	res := s.db.Where("created_at < ?", cutoff).Delete(&model.ClientTelemetry{})
	return res.RowsAffected, res.Error
}

// Start 启动后台滚动清理循环。
func (s *ClientTelemetryService) Start() {
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
func (s *ClientTelemetryService) Stop() { close(s.stop) }

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
