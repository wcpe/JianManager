package service

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/proto/workerpb"
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

	// lastNet 记录每节点上次心跳的累计网络字节与时刻，用于据相邻差推导速率。
	netMu   sync.Mutex
	lastNet map[string]netCounters
}

// netCounters 某节点上次心跳的累计网络字节快照。
type netCounters struct {
	sent, recv int64
	ts         time.Time
}

// NewMetricService 创建时序指标服务。
func NewMetricService(db *gorm.DB) *MetricService {
	return &MetricService{db: db, lastNet: map[string]netCounters{}}
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

// IngestHeartbeat 把一次心跳负载落库为时序样本：节点 CPU/内存/磁盘/网络速率 +
// 每实例 ServerProbe 富指标（含分世界）。网络速率据相邻心跳累计字节差推导（首拍与计数器
// 回绕时不出速率样本）。探针不可用的实例写 NULL 断点，缺测不补假值（ADR-013）。
// 实现 grpc.MetricIngester，由 CP 心跳处理器调用。
func (s *MetricService) IngestHeartbeat(req *workerpb.HeartbeatRequest) error {
	return s.ingestHeartbeatAt(req, time.Now().UTC())
}

// ingestHeartbeatAt 是 IngestHeartbeat 的可测内核，now 注入便于断言时间相关逻辑（网络速率）。
func (s *MetricService) ingestHeartbeatAt(req *workerpb.HeartbeatRequest, now time.Time) error {
	if req == nil || req.NodeUuid == "" {
		return nil
	}
	now = now.UTC()
	ptr := func(v float64) *float64 { return &v }
	samples := make([]Sample, 0, 8+len(req.InstanceMetrics)*10)

	node := req.NodeUuid
	nodeSample := func(key, unit string, v *float64) Sample {
		return Sample{NodeUUID: node, Scope: model.MetricScopeNode, MetricKey: key, Unit: unit, TS: now, Value: v}
	}
	samples = append(samples,
		nodeSample(model.MetricNodeCPUPct, "pct", ptr(float64(req.CpuUsage)*100)),
		nodeSample(model.MetricNodeMemUsed, "bytes", ptr(float64(req.MemoryUsedMb)*1024*1024)),
		nodeSample(model.MetricNodeDiskUsed, "bytes", ptr(float64(req.DiskUsedMb)*1024*1024)),
	)

	// 网络速率：相邻心跳累计字节差 / 间隔秒。差为负（节点重启计数器回绕）时跳过该拍。
	s.netMu.Lock()
	if prev, ok := s.lastNet[node]; ok {
		if dt := now.Sub(prev.ts).Seconds(); dt > 0 {
			if d := req.NetworkBytesSent - prev.sent; d >= 0 {
				samples = append(samples, nodeSample(model.MetricNodeNetTxRate, "bytes_per_sec", ptr(float64(d)/dt)))
			}
			if d := req.NetworkBytesRecv - prev.recv; d >= 0 {
				samples = append(samples, nodeSample(model.MetricNodeNetRxRate, "bytes_per_sec", ptr(float64(d)/dt)))
			}
		}
	}
	s.lastNet[node] = netCounters{sent: req.NetworkBytesSent, recv: req.NetworkBytesRecv, ts: now}
	s.netMu.Unlock()

	// 每实例 ServerProbe 快照（instance 维度 + 分世界 world 维度）。
	for _, im := range req.InstanceMetrics {
		iid := im.InstanceUuid
		if iid == "" {
			continue
		}
		instSample := func(key, unit string, v *float64) Sample {
			return Sample{NodeUUID: node, InstanceID: iid, Scope: model.MetricScopeInstance, MetricKey: key, Unit: unit, TS: now, Value: v}
		}
		if !im.ProbeAvailable {
			// 探针不可用：写 NULL 断点（曲线断开，不补假值）。
			samples = append(samples, instSample(model.MetricInstTPS, "tps", nil))
			continue
		}
		samples = append(samples,
			instSample(model.MetricInstTPS, "tps", ptr(im.Tps)),
			instSample(model.MetricInstMSPT, "ms", ptr(im.MsptMillis)),
			instSample(model.MetricInstPlayersOnline, "count", ptr(float64(im.PlayersOnline))),
			instSample(model.MetricInstHeapUsed, "bytes", ptr(float64(im.HeapUsedBytes))),
			instSample(model.MetricInstHeapMax, "bytes", ptr(float64(im.HeapMaxBytes))),
			instSample(model.MetricInstThreads, "count", ptr(float64(im.Threads))),
			instSample(model.MetricInstUptime, "seconds", ptr(im.UptimeSeconds)),
		)
		// 系统 CPU 0~1，<0 表示探针未取到 → 跳过该指标。
		if im.CpuLoad >= 0 {
			samples = append(samples, instSample(model.MetricInstCPUPct, "pct", ptr(im.CpuLoad*100)))
		}
		for _, w := range im.Worlds {
			if w.Name == "" {
				continue
			}
			worldSample := func(key, unit string, v *float64) Sample {
				return Sample{NodeUUID: node, InstanceID: iid, Scope: model.MetricScopeWorld, World: w.Name, MetricKey: key, Unit: unit, TS: now, Value: v}
			}
			samples = append(samples,
				worldSample(model.MetricWorldLoadedChunks, "count", ptr(float64(w.LoadedChunks))),
				worldSample(model.MetricWorldEntities, "count", ptr(float64(w.Entities))),
				worldSample(model.MetricWorldTileEntities, "count", ptr(float64(w.TileEntities))),
			)
		}
	}

	return s.Ingest(samples)
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

// OverviewTotals 总览页的跨节点当前总量快照。
type OverviewTotals struct {
	NodeCount        int     `json:"nodeCount"`
	OnlineNodeCount  int     `json:"onlineNodeCount"`
	RunningInstances int     `json:"runningInstances"`
	CPUPct           float64 `json:"cpuPct"`        // 在线节点 CPU 使用率均值
	MemUsedBytes     int64   `json:"memUsedBytes"`  // 在线节点已用内存合计
	MemTotalBytes    int64   `json:"memTotalBytes"` // 在线节点内存容量合计
	OnlinePlayers    int64   `json:"onlinePlayers"` // 各实例最近在线人数合计
}

// OverviewTrend 总览页的一条跨维度聚合历史曲线。
type OverviewTrend struct {
	MetricKey string        `json:"metricKey"`
	Unit      string        `json:"unit"`
	Points    []SeriesPoint `json:"points"`
}

// OverviewResult 总览页聚合结果：当前总量 + 聚合曲线（总 CPU/内存/在线玩家）。
type OverviewResult struct {
	Totals     OverviewTotals  `json:"totals"`
	Resolution string          `json:"resolution"`
	Trends     []OverviewTrend `json:"trends"`
}

// overviewRecentWindow 「当前在线人数」只计最近一窗内仍在上报的实例，避免已停实例的陈旧样本计入。
const overviewRecentWindow = 2 * time.Minute

// Overview 汇总跨节点当前总量 + 聚合历史曲线（总 CPU 均值 / 总内存合计 / 总在线玩家合计），
// 供总览页一屏概览（FR-060）。曲线按区间自动选档并按档位桶跨序列对齐聚合。
func (s *MetricService) Overview(from, to time.Time, resolution string) (OverviewResult, error) {
	return s.overviewAt(time.Now().UTC(), from, to, resolution)
}

// overviewAt 是 Overview 的可测内核，now 注入便于断言「最近在线人数」窗口逻辑。
func (s *MetricService) overviewAt(now, from, to time.Time, resolution string) (OverviewResult, error) {
	res := selectResolution(to.Sub(from), resolution)
	totals, err := s.overviewTotals(now)
	if err != nil {
		return OverviewResult{}, err
	}
	out := OverviewResult{Totals: totals, Resolution: res}

	specs := []struct {
		scope model.MetricScope
		key   string
		unit  string
		sum   bool // true=跨序列求和，false=求均值
	}{
		{model.MetricScopeNode, model.MetricNodeCPUPct, "pct", false},
		{model.MetricScopeNode, model.MetricNodeMemUsed, "bytes", true},
		{model.MetricScopeInstance, model.MetricInstPlayersOnline, "count", true},
	}
	for _, sp := range specs {
		tr, err := s.aggregateTrend(sp.scope, sp.key, sp.unit, from.UTC(), to.UTC(), res, sp.sum)
		if err != nil {
			return OverviewResult{}, err
		}
		out.Trends = append(out.Trends, tr)
	}
	return out, nil
}

// overviewTotals 据 Node/Instance 当前值与最近指标样本汇总跨节点总量。
func (s *MetricService) overviewTotals(now time.Time) (OverviewTotals, error) {
	var t OverviewTotals
	var nodes []model.Node
	if err := s.db.Find(&nodes).Error; err != nil {
		return t, err
	}
	t.NodeCount = len(nodes)
	var cpuSum float64
	for _, n := range nodes {
		if n.Status != model.NodeStatusOnline {
			continue
		}
		t.OnlineNodeCount++
		cpuSum += float64(n.CPUUsage) * 100
		t.MemUsedBytes += n.MemoryUsedMB * 1024 * 1024
		t.MemTotalBytes += n.MemoryMB * 1024 * 1024
	}
	if t.OnlineNodeCount > 0 {
		t.CPUPct = cpuSum / float64(t.OnlineNodeCount)
	}

	var running int64
	if err := s.db.Model(&model.Instance{}).Where("status = ?", model.InstanceStatusRunning).Count(&running).Error; err != nil {
		return t, err
	}
	t.RunningInstances = int(running)

	players, err := s.latestSum(model.MetricScopeInstance, model.MetricInstPlayersOnline, now.Add(-overviewRecentWindow))
	if err != nil {
		return t, err
	}
	t.OnlinePlayers = int64(players)
	return t, nil
}

// latestSum 取某 scope+metric_key 下每条序列在 since 之后的最新非空样本并求和（如总在线人数）。
func (s *MetricService) latestSum(scope model.MetricScope, metricKey string, since time.Time) (float64, error) {
	var series []model.MetricSeries
	if err := s.db.Where("scope = ? AND metric_key = ?", scope, metricKey).Find(&series).Error; err != nil {
		return 0, err
	}
	var sum float64
	for _, se := range series {
		var row model.MetricSampleRaw
		err := s.db.Where("series_id = ? AND value IS NOT NULL AND ts >= ?", se.ID, since).
			Order("ts DESC").First(&row).Error
		if err == nil && row.Value != nil {
			sum += *row.Value
		} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, err
		}
	}
	return sum, nil
}

// overviewBucket 据查询档位选择跨序列对齐的桶大小（raw 用 1m 把 30s 样本并桶）。
func overviewBucket(res string) time.Duration {
	switch res {
	case "1h":
		return time.Hour
	case "5m":
		return metricBucket5m
	default: // raw
		return time.Minute
	}
}

// aggregateTrend 把某 scope+metric_key 下的所有序列按档位桶对齐后跨序列聚合成一条曲线。
// 每条序列在同一桶内先取其样本均值作代表值，再按 sum/avg 跨序列合并。
func (s *MetricService) aggregateTrend(scope model.MetricScope, metricKey, unit string, from, to time.Time, res string, sum bool) (OverviewTrend, error) {
	bucket := overviewBucket(res)
	var series []model.MetricSeries
	if err := s.db.Where("scope = ? AND metric_key = ?", scope, metricKey).Find(&series).Error; err != nil {
		return OverviewTrend{}, err
	}

	type acc struct {
		sum float64
		n   int
	}
	combined := map[int64]*acc{}
	for _, se := range series {
		pts, err := s.queryPoints(se.ID, res, from, to)
		if err != nil {
			return OverviewTrend{}, err
		}
		perSeries := map[int64]*acc{}
		for _, p := range pts {
			if p.Avg == nil {
				continue
			}
			b := p.TS.Truncate(bucket).UnixNano()
			a := perSeries[b]
			if a == nil {
				a = &acc{}
				perSeries[b] = a
			}
			a.sum += *p.Avg
			a.n++
		}
		for b, a := range perSeries {
			rep := a.sum / float64(a.n)
			g := combined[b]
			if g == nil {
				g = &acc{}
				combined[b] = g
			}
			g.sum += rep
			g.n++
		}
	}

	keys := make([]int64, 0, len(combined))
	for b := range combined {
		keys = append(keys, b)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	points := make([]SeriesPoint, 0, len(keys))
	for _, b := range keys {
		g := combined[b]
		v := g.sum
		if !sum && g.n > 0 {
			v = g.sum / float64(g.n)
		}
		val := v
		ts := time.Unix(0, b).UTC()
		points = append(points, SeriesPoint{TS: ts, Avg: &val, Min: &val, Max: &val})
	}
	return OverviewTrend{MetricKey: metricKey, Unit: unit, Points: points}, nil
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
