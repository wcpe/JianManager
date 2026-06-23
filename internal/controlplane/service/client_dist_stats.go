package service

import (
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// ClientDistStatsService 分发统计后台只读聚合（FR-095，见 ADR-023）。
// 复用 FR-093/094 聚合表 + 明细 + FR-092 机器码登记，**只读 GROUP BY、不引入新表**。
type ClientDistStatsService struct {
	db *gorm.DB
}

// NewClientDistStatsService 创建统计服务。
func NewClientDistStatsService(db *gorm.DB) *ClientDistStatsService {
	return &ClientDistStatsService{db: db}
}

// StatsDayPoint 下载量按日点。
type StatsDayPoint struct {
	Day      string `json:"day"`
	Requests int64  `json:"requests"`
	Bytes    int64  `json:"bytes"`
}

// StatsVersion 版本分布项。
type StatsVersion struct {
	Version  int   `json:"version"`
	Requests int64 `json:"requests"`
}

// StatsResult 更新结果分布项。
type StatsResult struct {
	Result string `json:"result"`
	Count  int64  `json:"count"`
}

// StatsIP 来源 IP 分布项。
type StatsIP struct {
	IP    string `json:"ip"`
	Count int64  `json:"count"`
}

// ClientDistStats 分发统计复合视图（FR-095）。
type ClientDistStats struct {
	ChannelID      string          `json:"channelId"`
	Days           int             `json:"days"`
	Downloads      []StatsDayPoint `json:"downloads"`
	Versions       []StatsVersion  `json:"versions"`
	Results        []StatsResult   `json:"results"`
	SuccessRate    float64         `json:"successRate"`
	RollbackRate   float64         `json:"rollbackRate"`
	ActiveMachines int64           `json:"activeMachines"`
	TopIPs         []StatsIP       `json:"topIps"`
}

// Overview 聚合指定频道近 days 天的分发统计（默认 30，上限 365）。
func (s *ClientDistStatsService) Overview(channelID string, days int) (*ClientDistStats, error) {
	if days <= 0 || days > 365 {
		days = 30
	}
	sinceTime := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	sinceDay := sinceTime.UTC().Format("2006-01-02")
	out := &ClientDistStats{
		ChannelID: channelID, Days: days,
		Downloads: []StatsDayPoint{}, Versions: []StatsVersion{}, Results: []StatsResult{}, TopIPs: []StatsIP{},
	}

	// 下载量趋势（按日，所有 kind 汇总）。
	if err := s.db.Model(&model.ClientDistDaily{}).
		Select("day, SUM(requests) AS requests, SUM(bytes) AS bytes").
		Where("channel_id = ? AND day >= ?", channelID, sinceDay).
		Group("day").Order("day").Scan(&out.Downloads).Error; err != nil {
		return nil, err
	}
	// 版本分布（manifest 拉取）。
	if err := s.db.Model(&model.ClientDistDaily{}).
		Select("version, SUM(requests) AS requests").
		Where("channel_id = ? AND kind = ? AND day >= ?", channelID, "manifest", sinceDay).
		Group("version").Order("version").Scan(&out.Versions).Error; err != nil {
		return nil, err
	}
	// 更新结果分布（遥测）。
	if err := s.db.Model(&model.ClientTelemetryDaily{}).
		Select("result, SUM(count) AS count").
		Where("channel_id = ? AND day >= ?", channelID, sinceDay).
		Group("result").Scan(&out.Results).Error; err != nil {
		return nil, err
	}
	var total, success, rollback int64
	for _, r := range out.Results {
		total += r.Count
		switch r.Result {
		case "success":
			success = r.Count
		case "rolled-back":
			rollback = r.Count
		}
	}
	if total > 0 {
		out.SuccessRate = float64(success) / float64(total)
		out.RollbackRate = float64(rollback) / float64(total)
	}
	// 活跃机器码数（近窗去重）。
	if err := s.db.Model(&model.ClientDistEvent{}).
		Where("channel_id = ? AND created_at >= ? AND machine_id != ''", channelID, sinceTime).
		Distinct("machine_id").Count(&out.ActiveMachines).Error; err != nil {
		return nil, err
	}
	// 来源 IP 分布 Top 10（近窗）。
	if err := s.db.Model(&model.ClientDistEvent{}).
		Select("ip, COUNT(*) AS count").
		Where("channel_id = ? AND created_at >= ? AND ip != ''", channelID, sinceTime).
		Group("ip").Order("count DESC").Limit(10).Scan(&out.TopIPs).Error; err != nil {
		return nil, err
	}
	return out, nil
}
