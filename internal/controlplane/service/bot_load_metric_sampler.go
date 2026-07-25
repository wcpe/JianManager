package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

const botLoadMetricSampleInterval = 5 * time.Second

// BotLoadMetricSampler 每 5s 为活跃压测会话写入一条聚合样本（FR-370）。
// 首版只聚合 Bot 状态计数与命令 checkpoint 终态计数；延迟百分位/探针 targetLegacy 后续迭代。
type BotLoadMetricSampler struct {
	db    *gorm.DB
	clock BotLoadClock

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewBotLoadMetricSampler 创建采样器。
func NewBotLoadMetricSampler(db *gorm.DB, clock BotLoadClock) *BotLoadMetricSampler {
	return &BotLoadMetricSampler{db: db, clock: normalizeBotLoadClock(clock)}
}

// Start 启动后台 5s 循环；重复调用幂等。
func (s *BotLoadMetricSampler) Start() {
	if s == nil || s.db == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(1)
	go s.loop(ctx)
}

// Stop 停止后台循环。
func (s *BotLoadMetricSampler) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

func (s *BotLoadMetricSampler) loop(ctx context.Context) {
	defer s.wg.Done()
	// 启动后立即采一轮，避免首窗空窗过长。
	s.sampleActive(ctx)
	ticker := time.NewTicker(botLoadMetricSampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sampleActive(ctx)
		}
	}
}

func (s *BotLoadMetricSampler) sampleActive(ctx context.Context) {
	ids, err := s.listActiveSessionIDs(ctx)
	if err != nil {
		slog.Warn("压测指标采样：列举活跃会话失败", "error", err)
		return
	}
	for _, id := range ids {
		if err := s.SampleSession(ctx, id); err != nil {
			slog.Debug("压测指标采样失败", "sessionId", id, "error", err)
		}
	}
}

func (s *BotLoadMetricSampler) listActiveSessionIDs(ctx context.Context) ([]uint, error) {
	var ids []uint
	// V1 status=running 或 V2 run_state in (running,degraded,starting,stopping)
	err := s.db.WithContext(ctx).Model(&model.BotStressSession{}).
		Where("status = ? OR run_state IN ?", model.BotStressSessionRunning,
			[]model.BotLoadRunState{
				model.BotLoadRunRunning, model.BotLoadRunDegraded,
				model.BotLoadRunStarting, model.BotLoadRunStopping,
			}).
		Pluck("id", &ids).Error
	return ids, err
}

// SampleSession 对单会话写一条 5s 对齐样本（同秒幂等 upsert）。
func (s *BotLoadMetricSampler) SampleSession(ctx context.Context, sessionID uint) error {
	if s == nil || s.db == nil {
		return errors.New("指标采样器未初始化")
	}
	var sess model.BotStressSession
	if err := s.db.WithContext(ctx).First(&sess, sessionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrBotStressSessionNotFound
		}
		return fmt.Errorf("查询压测会话失败: %w", err)
	}
	now := s.clock.Now().UTC().Truncate(botLoadMetricSampleInterval)
	stage := 0
	if sess.CurrentStage != nil {
		stage = *sess.CurrentStage
	}

	counts, err := s.aggregateBotCounts(ctx, sessionID, sess.BotCount)
	if err != nil {
		return err
	}
	command, err := s.aggregateCommandCounts(ctx, sessionID)
	if err != nil {
		return err
	}
	countsJSON, err := json.Marshal(counts)
	if err != nil {
		return err
	}
	commandJSON, err := json.Marshal(command)
	if err != nil {
		return err
	}
	// 首版空壳：屏障/执行节点/延迟/错误在后续接真实源。
	emptyObj := []byte(`{}`)
	emptyArr := []byte(`[]`)
	latency := map[string]any{
		"connectP50Ms": nil, "connectP95Ms": nil, "connectP99Ms": nil,
		"scheduleLagP50Ms": nil, "scheduleLagP95Ms": nil, "scheduleLagP99Ms": nil,
		"barrierReleaseLagP50Ms": nil, "barrierReleaseLagP95Ms": nil, "barrierReleaseLagP99Ms": nil,
	}
	latencyJSON, _ := json.Marshal(latency)

	row := model.BotLoadMetricSample{
		StressSessionID: sessionID,
		SampledAt:       now,
		StageIndex:      stage,
		CountsJSON:      string(countsJSON),
		CommandJSON:     string(commandJSON),
		BarrierJSON:     string(emptyObj),
		ExecutorJSON:    string(emptyArr),
		LatencyJSON:     string(latencyJSON),
		ErrorsJSON:      string(emptyObj),
	}
	// unique(session, sampled_at) 冲突时覆盖 JSON 字段（同窗重采幂等）。
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "stress_session_id"}, {Name: "sampled_at"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"stage_index", "counts_json", "command_json", "barrier_json",
			"executor_json", "latency_json", "errors_json", "target_legacy_json",
		}),
	}).Create(&row).Error
}

func (s *BotLoadMetricSampler) aggregateBotCounts(ctx context.Context, sessionID uint, planned int) (map[string]int64, error) {
	out := map[string]int64{
		"planned": int64(planned),
		"total":   0,
	}
	type row struct {
		Status string
		Cnt    int64
	}
	var rows []row
	if err := s.db.WithContext(ctx).Model(&model.Bot{}).
		Select("status, COUNT(*) AS cnt").
		Where("stress_session_id = ?", sessionID).
		Group("status").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("聚合 Bot 状态失败: %w", err)
	}
	for _, r := range rows {
		out["total"] += r.Cnt
		out[r.Status] = r.Cnt
	}
	return out, nil
}

func (s *BotLoadMetricSampler) aggregateCommandCounts(ctx context.Context, sessionID uint) (map[string]int64, error) {
	out := map[string]int64{
		"planned": 0, "prepared": 0, "scheduled": 0,
		"sent": 0, "failed": 0, "timed_out": 0, "cancelled": 0,
	}
	type row struct {
		Status string
		Cnt    int64
	}
	var rows []row
	if err := s.db.WithContext(ctx).Model(&model.BotLoadCommandCheckpoint{}).
		Select("status, COUNT(*) AS cnt").
		Where("stress_session_id = ?", sessionID).
		Group("status").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("聚合命令 checkpoint 失败: %w", err)
	}
	for _, r := range rows {
		out["planned"] += r.Cnt
		out[r.Status] = r.Cnt
	}
	return out, nil
}

// ---- 查询面 ----

// BotLoadMetricPointView 与规格/前端 BotLoadMetricPoint 对齐的读模型。
type BotLoadMetricPointView struct {
	Timestamp    time.Time      `json:"timestamp"`
	StageIndex   int            `json:"stageIndex"`
	Counts       map[string]any `json:"counts"`
	Command      map[string]any `json:"command"`
	Barrier      map[string]any `json:"barrier"`
	Executor     []any          `json:"executor"`
	Latency      map[string]any `json:"latency"`
	Errors       map[string]any `json:"errors"`
	TargetLegacy map[string]any `json:"targetLegacy,omitempty"`
}

// BotLoadMetricListResult GET metrics 响应。
type BotLoadMetricListResult struct {
	Items      []BotLoadMetricPointView `json:"items"`
	From       time.Time                `json:"from"`
	To         time.Time                `json:"to"`
	Resolution string                   `json:"resolution"`
}

// ListMetrics 读取样本；resolution=raw|15s|1m|5m，默认 raw，最多 1200 点。
func (s *BotLoadMetricSampler) ListMetrics(ctx context.Context, sessionID uint, from, to *time.Time, resolution string) (*BotLoadMetricListResult, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("指标采样器未初始化")
	}
	var sess model.BotStressSession
	if err := s.db.WithContext(ctx).First(&sess, sessionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBotStressSessionNotFound
		}
		return nil, err
	}
	res := normalizeMetricResolution(resolution)
	q := s.db.WithContext(ctx).Model(&model.BotLoadMetricSample{}).
		Where("stress_session_id = ?", sessionID)
	if from != nil {
		q = q.Where("sampled_at >= ?", from.UTC())
	}
	if to != nil {
		q = q.Where("sampled_at <= ?", to.UTC())
	}
	var rows []model.BotLoadMetricSample
	if err := q.Order("sampled_at ASC").Limit(5000).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询指标样本失败: %w", err)
	}
	points := make([]BotLoadMetricPointView, 0, len(rows))
	for _, r := range rows {
		points = append(points, metricSampleToView(r))
	}
	points = downsampleMetricPoints(points, res, 1200)
	result := &BotLoadMetricListResult{Items: points, Resolution: res}
	if len(points) > 0 {
		result.From = points[0].Timestamp
		result.To = points[len(points)-1].Timestamp
	} else {
		now := s.clock.Now().UTC()
		result.From, result.To = now, now
		if from != nil {
			result.From = from.UTC()
		}
		if to != nil {
			result.To = to.UTC()
		}
	}
	return result, nil
}

func normalizeMetricResolution(raw string) string {
	switch raw {
	case "15s", "1m", "5m", "raw":
		return raw
	default:
		return "raw"
	}
}

func metricSampleToView(r model.BotLoadMetricSample) BotLoadMetricPointView {
	v := BotLoadMetricPointView{
		Timestamp:  r.SampledAt.UTC(),
		StageIndex: r.StageIndex,
		Counts:     decodeJSONMap(r.CountsJSON),
		Command:    decodeJSONMap(r.CommandJSON),
		Barrier:    decodeJSONMap(r.BarrierJSON),
		Latency:    decodeJSONMap(r.LatencyJSON),
		Errors:     decodeJSONMap(r.ErrorsJSON),
		Executor:   decodeJSONArray(r.ExecutorJSON),
	}
	if r.TargetLegacyJSON != nil && *r.TargetLegacyJSON != "" {
		v.TargetLegacy = decodeJSONMap(*r.TargetLegacyJSON)
	}
	return v
}

func decodeJSONMap(raw string) map[string]any {
	if raw == "" {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil || m == nil {
		return map[string]any{}
	}
	return m
}

func decodeJSONArray(raw string) []any {
	if raw == "" {
		return []any{}
	}
	var a []any
	if err := json.Unmarshal([]byte(raw), &a); err != nil || a == nil {
		return []any{}
	}
	return a
}

func downsampleMetricPoints(points []BotLoadMetricPointView, resolution string, maxPoints int) []BotLoadMetricPointView {
	if len(points) == 0 {
		return points
	}
	step := time.Duration(0)
	switch resolution {
	case "15s":
		step = 15 * time.Second
	case "1m":
		step = time.Minute
	case "5m":
		step = 5 * time.Minute
	}
	out := points
	if step > 0 {
		// 每桶取最后一点（raw 5s 已对齐）。
		var reduced []BotLoadMetricPointView
		var bucket time.Time
		var last *BotLoadMetricPointView
		for i := range out {
			p := out[i]
			b := p.Timestamp.UTC().Truncate(step)
			if last == nil {
				bucket = b
				cp := p
				last = &cp
				continue
			}
			if !b.Equal(bucket) {
				reduced = append(reduced, *last)
				bucket = b
				cp := p
				last = &cp
				continue
			}
			cp := p
			last = &cp
		}
		if last != nil {
			reduced = append(reduced, *last)
		}
		out = reduced
	}
	if maxPoints > 0 && len(out) > maxPoints {
		// 均匀抽稀：保留首尾。
		stride := (len(out) + maxPoints - 1) / maxPoints
		if stride < 1 {
			stride = 1
		}
		var slim []BotLoadMetricPointView
		for i := 0; i < len(out); i += stride {
			slim = append(slim, out[i])
		}
		if slim[len(slim)-1].Timestamp != out[len(out)-1].Timestamp {
			slim = append(slim, out[len(out)-1])
		}
		out = slim
	}
	return out
}
