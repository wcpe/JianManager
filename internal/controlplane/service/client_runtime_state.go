package service

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// ClientRuntimeStateService 管理客户端运行态心跳与聚合查询（FR-265）。
type ClientRuntimeStateService struct {
	db *gorm.DB
}

// NewClientRuntimeStateService 创建客户端运行态服务。
func NewClientRuntimeStateService(db *gorm.DB) *ClientRuntimeStateService {
	return &ClientRuntimeStateService{db: db}
}

// ClientRuntimeHeartbeatInput 客户端启动心跳输入。
type ClientRuntimeHeartbeatInput struct {
	ChannelID    string
	MachineID    string
	IP           string
	Platform     string
	JavaVersion  string
	Launcher     string
	CoreVersion  string
	LocalVersion int
}

// ClientRuntimeQuery 运行态查询参数。
type ClientRuntimeQuery struct {
	ChannelID string
	Range     string
	Now       time.Time
}

// RuntimeSummary 客户端 Tab KPI。
type RuntimeSummary struct {
	RecentStarted     int64   `json:"recentStarted"`
	TodayStarted      int64   `json:"todayStarted"`
	RecentStarts      int64   `json:"recentStarts"`
	TodayStarts       int64   `json:"todayStarts"`
	UpdateSuccessRate float64 `json:"updateSuccessRate"`
	UpdateFailureRate float64 `json:"updateFailureRate"`
}

// RuntimeVersionCount 运行版本分布项。
type RuntimeVersionCount struct {
	Version int   `json:"version"`
	Count   int64 `json:"count"`
}

// RuntimeStringCount 字符串维度分布项。
type RuntimeStringCount struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// RuntimeLagCount 版本滞后分布项。
type RuntimeLagCount struct {
	Lag   int   `json:"lag"`
	Count int64 `json:"count"`
}

// RuntimeUpdateSeriesPoint 更新结果趋势点。
type RuntimeUpdateSeriesPoint struct {
	TS         time.Time `json:"ts"`
	Success    int64     `json:"success"`
	FailStatic int64     `json:"failStatic"`
	RolledBack int64     `json:"rolledBack"`
	Error      int64     `json:"error"`
}

// ClientRuntimeOverview 客户端运行态聚合响应。
type ClientRuntimeOverview struct {
	ChannelID          string                     `json:"channelId"`
	From               time.Time                  `json:"from"`
	To                 time.Time                  `json:"to"`
	Summary            RuntimeSummary             `json:"summary"`
	Items              []model.ClientRuntimeState `json:"items"`
	RuntimeVersionDist []RuntimeVersionCount      `json:"runtimeVersionDist"`
	CoreVersionDist    []RuntimeStringCount       `json:"coreVersionDist"`
	PlatformDist       []RuntimeStringCount       `json:"platformDist"`
	LauncherDist       []RuntimeStringCount       `json:"launcherDist"`
	LagDist            []RuntimeLagCount          `json:"lagDist"`
	UpdateResultSeries []RuntimeUpdateSeriesPoint `json:"updateResultSeries"`
}

// RecordHeartbeat upsert 一条启动心跳。心跳只更新运行态表，不写 client_telemetry。
func (s *ClientRuntimeStateService) RecordHeartbeat(in ClientRuntimeHeartbeatInput) error {
	if s == nil {
		return nil
	}
	if strings.TrimSpace(in.ChannelID) == "" || strings.TrimSpace(in.MachineID) == "" {
		return nil
	}
	now := time.Now().UTC()
	row := &model.ClientRuntimeState{
		ChannelID:       trunc(in.ChannelID, 64),
		MachineID:       trunc(in.MachineID, machineIDMaxLen),
		IP:              trunc(in.IP, 64),
		Platform:        normalizeRuntimeString(in.Platform, 32),
		JavaVersion:     trunc(in.JavaVersion, 32),
		Launcher:        normalizeRuntimeString(in.Launcher, 32),
		CoreVersion:     normalizeRuntimeString(in.CoreVersion, 64),
		LocalVersion:    in.LocalVersion,
		FirstSeenAt:     now,
		LastHeartbeatAt: now,
	}
	return s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "channel_id"}, {Name: "machine_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"ip":                row.IP,
			"platform":          row.Platform,
			"java_version":      row.JavaVersion,
			"launcher":          row.Launcher,
			"core_version":      row.CoreVersion,
			"local_version":     row.LocalVersion,
			"last_heartbeat_at": row.LastHeartbeatAt,
			"updated_at":        now,
		}),
	}).Create(row).Error
}

// Overview 聚合客户端运行态和更新结果趋势。
func (s *ClientRuntimeStateService) Overview(q ClientRuntimeQuery) (*ClientRuntimeOverview, error) {
	if s == nil {
		return nil, fmt.Errorf("客户端运行态服务未初始化")
	}
	now := q.Now
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	dur := runtimeRangeDuration(q.Range)
	from := now.Add(-dur)
	out := &ClientRuntimeOverview{ChannelID: q.ChannelID, From: from, To: now}

	base := s.db.Model(&model.ClientRuntimeState{})
	if q.ChannelID != "" {
		base = base.Where("channel_id = ?", q.ChannelID)
	}
	if err := countRuntime(base, "last_heartbeat_at >= ?", now.Add(-5*time.Minute), &out.Summary.RecentStarted); err != nil {
		return nil, err
	}
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if err := countRuntime(base, "last_heartbeat_at >= ?", dayStart, &out.Summary.TodayStarted); err != nil {
		return nil, err
	}
	out.Summary.RecentStarts = out.Summary.RecentStarted
	out.Summary.TodayStarts = out.Summary.TodayStarted

	var states []model.ClientRuntimeState
	if err := base.Order("last_heartbeat_at DESC").Find(&states).Error; err != nil {
		return nil, err
	}
	out.Items = states
	out.RuntimeVersionDist = runtimeVersionDist(states)
	out.CoreVersionDist = runtimeStringDist(states, func(s model.ClientRuntimeState) string { return s.CoreVersion })
	out.PlatformDist = runtimeStringDist(states, func(s model.ClientRuntimeState) string { return s.Platform })
	out.LauncherDist = runtimeStringDist(states, func(s model.ClientRuntimeState) string { return s.Launcher })
	out.LagDist = runtimeLagDist(states, latestByChannel(states))

	series, successRate, failureRate, err := s.updateSeries(q.ChannelID, from, now)
	if err != nil {
		return nil, err
	}
	out.UpdateResultSeries = series
	out.Summary.UpdateSuccessRate = successRate
	out.Summary.UpdateFailureRate = failureRate
	return out, nil
}

func countRuntime(db *gorm.DB, cond string, arg any, out *int64) error {
	return db.Session(&gorm.Session{}).Where(cond, arg).Count(out).Error
}

func runtimeRangeDuration(r string) time.Duration {
	switch r {
	case "24h":
		return 24 * time.Hour
	case "30d":
		return 30 * 24 * time.Hour
	case "90d":
		return 90 * 24 * time.Hour
	case "7d", "":
		return 7 * 24 * time.Hour
	default:
		return 7 * 24 * time.Hour
	}
}

func normalizeRuntimeString(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		s = "unknown"
	}
	return trunc(s, max)
}

func runtimeVersionDist(states []model.ClientRuntimeState) []RuntimeVersionCount {
	m := map[int]int64{}
	for _, st := range states {
		m[st.LocalVersion]++
	}
	out := make([]RuntimeVersionCount, 0, len(m))
	for k, v := range m {
		out = append(out, RuntimeVersionCount{Version: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Version > out[j].Version
	})
	return out
}

func runtimeStringDist(states []model.ClientRuntimeState, pick func(model.ClientRuntimeState) string) []RuntimeStringCount {
	m := map[string]int64{}
	for _, st := range states {
		key := pick(st)
		if key == "" {
			key = "unknown"
		}
		m[key]++
	}
	out := make([]RuntimeStringCount, 0, len(m))
	for k, v := range m {
		out = append(out, RuntimeStringCount{Value: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Value < out[j].Value
	})
	return out
}

func latestByChannel(states []model.ClientRuntimeState) map[string]int {
	latest := map[string]int{}
	for _, st := range states {
		if st.LocalVersion > latest[st.ChannelID] {
			latest[st.ChannelID] = st.LocalVersion
		}
	}
	return latest
}

func runtimeLagDist(states []model.ClientRuntimeState, latest map[string]int) []RuntimeLagCount {
	m := map[int]int64{}
	for _, st := range states {
		lag := latest[st.ChannelID] - st.LocalVersion
		if lag < 0 {
			lag = 0
		}
		m[lag]++
	}
	out := make([]RuntimeLagCount, 0, len(m))
	for k, v := range m {
		out = append(out, RuntimeLagCount{Lag: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Lag < out[j].Lag })
	return out
}

func (s *ClientRuntimeStateService) updateSeries(channelID string, from, to time.Time) ([]RuntimeUpdateSeriesPoint, float64, float64, error) {
	q := s.db.Where("created_at >= ? AND created_at <= ?", from, to.Add(time.Second))
	if channelID != "" {
		q = q.Where("channel_id = ?", channelID)
	}
	var rows []model.ClientTelemetry
	if err := q.Find(&rows).Error; err != nil {
		return nil, 0, 0, err
	}
	byDay := map[string]*RuntimeUpdateSeriesPoint{}
	var order []string
	var total, success, failure int64
	for _, r := range rows {
		dayTs := time.Date(r.CreatedAt.UTC().Year(), r.CreatedAt.UTC().Month(), r.CreatedAt.UTC().Day(), 0, 0, 0, 0, time.UTC)
		key := dayTs.Format(time.RFC3339)
		p := byDay[key]
		if p == nil {
			p = &RuntimeUpdateSeriesPoint{TS: dayTs}
			byDay[key] = p
			order = append(order, key)
		}
		total++
		switch r.Result {
		case "success":
			p.Success++
			success++
		case "fail-static":
			p.FailStatic++
			failure++
		case "rolled-back":
			p.RolledBack++
		case "error":
			p.Error++
			failure++
		}
	}
	sort.Strings(order)
	series := make([]RuntimeUpdateSeriesPoint, 0, len(order))
	for _, day := range order {
		series = append(series, *byDay[day])
	}
	if total == 0 {
		return series, 0, 0, nil
	}
	return series, float64(success) / float64(total), float64(failure) / float64(total), nil
}

func parseRuntimeInt(s string) *int {
	if s == "" {
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return nil
	}
	return &n
}
