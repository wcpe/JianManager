package service

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

// 留存与档位常量（ADR-013：分级降采样）。
const (
	metricRawRetention = 48 * time.Hour
	metric5mRetention  = 30 * 24 * time.Hour
	metric1hRetention  = 400 * 24 * time.Hour
	metricBucket5m     = 5 * time.Minute
	metricBucket1h     = time.Hour
	metricRollupTick   = 5 * time.Minute
)

// Sample 一条待入库的指标样本。Value 为 nil 表示缺测（采集源不可达）。
type Sample struct {
	NodeUUID   string
	InstanceID string
	Scope      model.MetricScope
	MetricKey  string
	World      string
	Unit       string
	TS         time.Time
	Value      *float64
}

// SeriesQuery 历史曲线查询参数。Scope 为目标维度（node|instance）；
// instance 目标会同时返回其 instance 级与 world 级序列。
type SeriesQuery struct {
	Scope      model.MetricScope
	NodeUUID   string
	InstanceID string
	MetricKeys []string // 空 = 该目标下全部
	From, To   time.Time
	Resolution string // auto/raw/5m/1h
}

// SeriesPoint 曲线上一个点。raw 档 Avg=Min=Max=样本值；缺测为 nil（断点）。
type SeriesPoint struct {
	TS  time.Time `json:"ts"`
	Avg *float64  `json:"avg"`
	Min *float64  `json:"min"`
	Max *float64  `json:"max"`
}

// Series 一条返回曲线。
type Series struct {
	MetricKey string        `json:"metricKey"`
	Unit      string        `json:"unit"`
	World     string        `json:"world"`
	Points    []SeriesPoint `json:"points"`
}

// MetricService 时序指标存储与查询（ADR-013：CP 端分级降采样）。
type MetricService struct {
	db *gorm.DB

	mu      sync.Mutex
	running bool
	stopCh  chan struct{}
}

// NewMetricService 创建时序指标服务。
func NewMetricService(db *gorm.DB) *MetricService {
	return &MetricService{db: db}
}

// NodeExists 判断节点 UUID 是否存在（查询目标存在性校验用）。
func (s *MetricService) NodeExists(uuid string) (bool, error) {
	var n int64
	if err := s.db.Model(&model.Node{}).Where("uuid = ?", uuid).Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

// ResolveInstanceID 由实例 UUID 取数值 ID（供 RBAC 校验）；不存在返回 found=false。
func (s *MetricService) ResolveInstanceID(uuid string) (uint, bool, error) {
	var inst model.Instance
	err := s.db.Select("id").Where("uuid = ?", uuid).First(&inst).Error
	if err == nil {
		return inst.ID, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, false, nil
	}
	return 0, false, err
}

// selectResolution 据查询跨度自动选档；显式 resolution 优先。
func selectResolution(span time.Duration, requested string) string {
	switch requested {
	case "raw", "5m", "1h":
		return requested
	}
	switch {
	case span <= 6*time.Hour:
		return "raw"
	case span <= 30*24*time.Hour:
		return "5m"
	default:
		return "1h"
	}
}

// bucketStart 把时刻向下对齐到档位桶起点（UTC）。
func bucketStart(t time.Time, d time.Duration) time.Time {
	return t.UTC().Truncate(d)
}

// Ingest 批量写入样本：按身份 upsert 序列、追加原始样本。
func (s *MetricService) Ingest(samples []Sample) error {
	if len(samples) == 0 {
		return nil
	}
	now := time.Now().UTC()
	return s.db.Transaction(func(tx *gorm.DB) error {
		for _, sm := range samples {
			seriesID, err := s.ensureSeries(tx, sm, now)
			if err != nil {
				return err
			}
			row := model.MetricSampleRaw{SeriesID: seriesID, TS: sm.TS.UTC(), Value: sm.Value}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ensureSeries 按 (node,instance,scope,metric_key,world) 身份找到或创建序列，返回 ID。
// 用显式条件而非结构体 Where，避免 GORM 忽略 instance_id/world 的空字符串零值。
func (s *MetricService) ensureSeries(tx *gorm.DB, sm Sample, now time.Time) (uint, error) {
	var se model.MetricSeries
	err := tx.Where(
		"node_uuid = ? AND instance_id = ? AND scope = ? AND metric_key = ? AND world = ?",
		sm.NodeUUID, sm.InstanceID, sm.Scope, sm.MetricKey, sm.World,
	).First(&se).Error
	if err == nil {
		if se.LastSeenAt.Before(now) {
			tx.Model(&model.MetricSeries{}).Where("id = ?", se.ID).Update("last_seen_at", now)
		}
		return se.ID, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, err
	}
	se = model.MetricSeries{
		NodeUUID: sm.NodeUUID, InstanceID: sm.InstanceID, Scope: sm.Scope,
		MetricKey: sm.MetricKey, World: sm.World, Unit: sm.Unit,
		CreatedAt: now, LastSeenAt: now,
	}
	if err := tx.Create(&se).Error; err != nil {
		return 0, err
	}
	return se.ID, nil
}

// QuerySeries 据目标与区间返回各序列的历史曲线，自动选档。
func (s *MetricService) QuerySeries(q SeriesQuery) (string, []Series, error) {
	res := selectResolution(q.To.Sub(q.From), q.Resolution)

	sq := s.db.Model(&model.MetricSeries{})
	if q.Scope == model.MetricScopeNode {
		sq = sq.Where("scope = ? AND node_uuid = ?", model.MetricScopeNode, q.NodeUUID)
	} else {
		// instance 目标：instance 级 + world 级序列均挂在 instance_id 上
		sq = sq.Where("instance_id = ?", q.InstanceID)
	}
	if len(q.MetricKeys) > 0 {
		sq = sq.Where("metric_key IN ?", q.MetricKeys)
	}
	var rows []model.MetricSeries
	if err := sq.Order("metric_key, world").Find(&rows).Error; err != nil {
		return res, nil, err
	}

	out := make([]Series, 0, len(rows))
	for _, sr := range rows {
		pts, err := s.queryPoints(sr.ID, res, q.From.UTC(), q.To.UTC())
		if err != nil {
			return res, nil, err
		}
		out = append(out, Series{MetricKey: sr.MetricKey, Unit: sr.Unit, World: sr.World, Points: pts})
	}
	return res, out, nil
}

func (s *MetricService) queryPoints(seriesID uint, res string, from, to time.Time) ([]SeriesPoint, error) {
	switch res {
	case "raw":
		var rows []model.MetricSampleRaw
		if err := s.db.Where("series_id = ? AND ts >= ? AND ts <= ?", seriesID, from, to).
			Order("ts").Find(&rows).Error; err != nil {
			return nil, err
		}
		pts := make([]SeriesPoint, len(rows))
		for i, r := range rows {
			pts[i] = SeriesPoint{TS: r.TS.UTC(), Avg: r.Value, Min: r.Value, Max: r.Value}
		}
		return pts, nil
	case "1h":
		var rows []model.MetricRollup1h
		if err := s.db.Where("series_id = ? AND bucket_ts >= ? AND bucket_ts <= ?", seriesID, from, to).
			Order("bucket_ts").Find(&rows).Error; err != nil {
			return nil, err
		}
		pts := make([]SeriesPoint, len(rows))
		for i, r := range rows {
			avg, mn, mx := r.Avg, r.Min, r.Max
			pts[i] = SeriesPoint{TS: r.BucketTS.UTC(), Avg: &avg, Min: &mn, Max: &mx}
		}
		return pts, nil
	default: // "5m"
		var rows []model.MetricRollup5m
		if err := s.db.Where("series_id = ? AND bucket_ts >= ? AND bucket_ts <= ?", seriesID, from, to).
			Order("bucket_ts").Find(&rows).Error; err != nil {
			return nil, err
		}
		pts := make([]SeriesPoint, len(rows))
		for i, r := range rows {
			avg, mn, mx := r.Avg, r.Min, r.Max
			pts[i] = SeriesPoint{TS: r.BucketTS.UTC(), Avg: &avg, Min: &mn, Max: &mx}
		}
		return pts, nil
	}
}

// RollupAndPurge 卷积已完结的桶（raw→5m、5m→1h）并按 TTL 清理过期数据。幂等。
func (s *MetricService) RollupAndPurge(now time.Time) error {
	now = now.UTC()
	if err := s.rollup5m(now); err != nil {
		return err
	}
	if err := s.rollup1h(now); err != nil {
		return err
	}
	return s.purge(now)
}

type rawAcc struct {
	seriesID uint
	bucket   time.Time
	sum      float64
	min, max float64
	last     float64
	count    int
}

func (s *MetricService) rollup5m(now time.Time) error {
	cutoff := bucketStart(now, metricBucket5m) // 只卷已完结的桶
	var raws []model.MetricSampleRaw
	if err := s.db.Where("ts < ?", cutoff).Order("ts").Find(&raws).Error; err != nil {
		return err
	}
	accs := map[string]*rawAcc{}
	var order []string
	for _, r := range raws {
		if r.Value == nil {
			continue // 缺测不计入聚合
		}
		v := *r.Value
		b := bucketStart(r.TS, metricBucket5m)
		key := fmt.Sprintf("%d|%d", r.SeriesID, b.UnixNano())
		a := accs[key]
		if a == nil {
			a = &rawAcc{seriesID: r.SeriesID, bucket: b, min: v, max: v}
			accs[key] = a
			order = append(order, key)
		}
		a.sum += v
		a.count++
		if v < a.min {
			a.min = v
		}
		if v > a.max {
			a.max = v
		}
		a.last = v // raws 按 ts 升序，桶内最后一条即最新
	}
	for _, key := range order {
		a := accs[key]
		var exists int64
		if err := s.db.Model(&model.MetricRollup5m{}).
			Where("series_id = ? AND bucket_ts = ?", a.seriesID, a.bucket).Count(&exists).Error; err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		row := model.MetricRollup5m{
			SeriesID: a.seriesID, BucketTS: a.bucket,
			Avg: a.sum / float64(a.count), Min: a.min, Max: a.max, Last: a.last, Count: a.count,
		}
		if err := s.db.Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

type fiveAcc struct {
	seriesID    uint
	bucket      time.Time
	weightedSum float64
	min, max    float64
	last        float64
	count       int
}

func (s *MetricService) rollup1h(now time.Time) error {
	cutoff := bucketStart(now, metricBucket1h)
	var fives []model.MetricRollup5m
	if err := s.db.Where("bucket_ts < ?", cutoff).Order("bucket_ts").Find(&fives).Error; err != nil {
		return err
	}
	accs := map[string]*fiveAcc{}
	var order []string
	for _, f := range fives {
		b := bucketStart(f.BucketTS, metricBucket1h)
		key := fmt.Sprintf("%d|%d", f.SeriesID, b.UnixNano())
		a := accs[key]
		if a == nil {
			a = &fiveAcc{seriesID: f.SeriesID, bucket: b, min: f.Min, max: f.Max}
			accs[key] = a
			order = append(order, key)
		}
		a.weightedSum += f.Avg * float64(f.Count)
		a.count += f.Count
		if f.Min < a.min {
			a.min = f.Min
		}
		if f.Max > a.max {
			a.max = f.Max
		}
		a.last = f.Last
	}
	for _, key := range order {
		a := accs[key]
		if a.count == 0 {
			continue
		}
		var exists int64
		if err := s.db.Model(&model.MetricRollup1h{}).
			Where("series_id = ? AND bucket_ts = ?", a.seriesID, a.bucket).Count(&exists).Error; err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		row := model.MetricRollup1h{
			SeriesID: a.seriesID, BucketTS: a.bucket,
			Avg: a.weightedSum / float64(a.count), Min: a.min, Max: a.max, Last: a.last, Count: a.count,
		}
		if err := s.db.Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *MetricService) purge(now time.Time) error {
	if err := s.db.Where("ts < ?", now.Add(-metricRawRetention)).
		Delete(&model.MetricSampleRaw{}).Error; err != nil {
		return err
	}
	if err := s.db.Where("bucket_ts < ?", now.Add(-metric5mRetention)).
		Delete(&model.MetricRollup5m{}).Error; err != nil {
		return err
	}
	if err := s.db.Where("bucket_ts < ?", now.Add(-metric1hRetention)).
		Delete(&model.MetricRollup1h{}).Error; err != nil {
		return err
	}
	return nil
}

// Start 启动后台卷积/清理循环（每 5min，复用 scheduler 式后台 goroutine）。
func (s *MetricService) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.stopCh = make(chan struct{})
	s.mu.Unlock()

	go func() {
		ticker := time.NewTicker(metricRollupTick)
		defer ticker.Stop()
		for {
			select {
			case <-s.stopCh:
				return
			case now := <-ticker.C:
				if err := s.RollupAndPurge(now); err != nil {
					slog.Error("时序指标卷积/清理失败", "error", err)
				}
			}
		}
	}()
	slog.Info("时序指标卷积器已启动")
}

// Stop 停止后台循环。
func (s *MetricService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	close(s.stopCh)
	s.running = false
	slog.Info("时序指标卷积器已停止")
}
