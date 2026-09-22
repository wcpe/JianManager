package service

import (
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// rankingSupportedMetrics 跨实例排行支持的指标键（FR-469）。
var rankingSupportedMetrics = map[string]bool{
	model.MetricInstTPS:           true,
	model.MetricInstMSPT:          true,
	model.MetricInstCPUPct:        true,
	model.MetricInstHeapUsed:      true,
	model.MetricInstPlayersOnline: true,
}

// rankingDefaultWindow 排行代表值的默认聚合窗口。
const rankingDefaultWindow = 5 * time.Minute

// rankingMaxLimit 排行返回条数上限。
const rankingMaxLimit = 100

// rankingMaxRawWindow raw 档可覆盖的最大窗口（与 metricRawRetention 同口径）。
const rankingMaxRawWindow = 6 * time.Hour

// rankingMax5mWindow 5m 档可覆盖的最大窗口（与 metric5mRetention 同口径）。
//
// M5 修复：30d 原走 5m 档 → 500 实例 × 2016 桶 = 100.8 万行参与聚合（实测约 10s），
// 而前端 30s 轮询，慢查询会持续堆积。30d 及以上改走 1h 档（168 桶/实例，行数降 12 倍）；
// 代表值是窗口均值，1h 档的均值与 5m 档的加权均值在本场景等价（同一指标、同一窗口）。
// 与 spec §2.1「单条聚合 SQL 避免 N+1」不冲突——仍是单条聚合，只是档位更粗。
const rankingMax5mWindow = 7 * 24 * time.Hour

// RankingQuery 跨实例排行查询（FR-469）。代表值取窗口内均值（比最新一拍稳健）。
type RankingQuery struct {
	MetricKey string
	Order     string // asc | desc；空则按指标语义定方向
	Window    time.Duration
	Limit     int
	NodeUUID  string
	// Scoped=true 时仅统计 AllowedIDs 内实例（非管理员收敛）；AllowedIDs 为空即无可见实例。
	AllowedIDs []uint
	Scoped     bool
}

// RankingItem 排行中一个实例的代表值与名次。
type RankingItem struct {
	InstanceID   uint      `json:"instanceId"`
	InstanceUUID string    `json:"instanceUuid"`
	Name         string    `json:"name"`
	NodeUUID     string    `json:"nodeUuid"`
	Value        float64   `json:"value"`
	Rank         int       `json:"rank"`
	SampledAt    time.Time `json:"sampledAt"`
}

// RankingResult 排行结果。SkippedNoData 为有序列但窗口内无样本、因而未进榜的实例数。
type RankingResult struct {
	MetricKey     string        `json:"metricKey"`
	Order         string        `json:"order"`
	WindowSeconds int           `json:"windowSeconds"`
	Scoped        bool          `json:"scoped"`
	SkippedNoData int           `json:"skippedNoData"`
	Items         []RankingItem `json:"items"`
}

// RankingSupportedMetric 判断指标键是否可用于排行（供路由校验）。
func RankingSupportedMetric(key string) bool { return rankingSupportedMetrics[key] }

// rankingOrder 解析排序方向：显式 asc/desc 优先，否则按指标语义（TPS 越低越差 → asc；其余 desc）。
func rankingOrder(metricKey, requested string) string {
	switch requested {
	case "asc":
		return "asc"
	case "desc":
		return "desc"
	}
	if metricKey == model.MetricInstTPS {
		return "asc"
	}
	return "desc"
}

// InstanceRanking 返回窗口内各实例某指标代表值的跨节点排行（FR-469）。
// 单条聚合 SQL：以样本表为驱动 JOIN metric_series 按 **instance_id** 分组求均值，
// 再 JOIN instances 取名，最后排序 LIMIT；窗口内无样本的实例不进榜（不伪造 0）。
//
// M-6 修复：分组键由 `(instance_id, node_uuid)` 收敛为 `instance_id`——**一行 = 一个实例**。
//
// 为什么这是 bug 而不是「一行 = 一条序列」的刻意设计：spec §1.3 明写
// 「排行首版**不跨节点聚合同一实例的多序列**——**一个实例一行**，取该实例的实例级序列值」。
// 序列身份含 `node_uuid`（`MetricSeries` 唯一索引 = node_uuid+instance_id+scope+metric_key+world），
// 故实例换节点后会**残留新旧两条序列**；按 (instance_id, node_uuid) 分组时同一实例产出两行，
// rank 各占一名（审查员实测：同一 UUID 同时占 rank 1 与 rank 2，仅 nodeUuid 不同）。
// 连带两个副作用：① `skippedNoData`（按 DISTINCT instance_id 计）与 `items`
// （按 (instance_id,node_uuid) 计）口径不一致，`items` 数可超过 limit/实例总数；
// ② 代表值被拆成两条各自的部分窗口均值，比「同实例单条代表值」失真。
//
// 幂等性与 node_uuid 取值：分组收敛后 `s.node_uuid` 成为**裸列**（既不在 GROUP BY 也不在聚合里），
// 其值取自「驱动行的最后一拍」——SQLite 保证「单个 max() 聚合时所有裸列取自该 max 所在行」，
// 故此处刻意让 `MAX(ts)` 成为查询里**唯一**的 max() 聚合，裸列 `s.node_uuid`
// 便确定地落在「窗口内最新样本所属的序列」上——这正是实例迁移后该显示的**当前节点**
// （该行为由 `TestMetric_InstanceRanking_OneSeatPerInstance` 的 `nodeUuid` 断言锁定：
// 旧序列在前、新序列在后时取到新节点）。`AVG` 不参与该规则（它不是 min/max），
// 故 value 仍是**跨序列全体样本**的窗口均值——与「取该实例的实例级序列值」同口径
// （同一实例的多条序列是同一测量的先后两段，不是两个可加的分区，故求均值而非求和，
// 与 `forecastPoints` 的「按 identity 择一」一致：都拒绝「哪条序列存活取决于查询顺序」）。
//
// 代价与取舍：实例换节点当口，若新旧序列**都**有窗口内样本，均值会把两段混算
// （旧节点的历史与当前值各占权重）。这是「一个实例一个代表值」的必然结果，
// 且比原先「同一实例占两席、各报一个部分均值」更贴近 spec 语义；若将来需要「只取当前序列」，
// 应显式按 `s.node_uuid = i.node_uuid` 过滤，而不是恢复 (instance_id, node_uuid) 分组。
func (s *MetricService) InstanceRanking(q RankingQuery) (RankingResult, error) {
	out := RankingResult{
		MetricKey: q.MetricKey,
		Order:     rankingOrder(q.MetricKey, q.Order),
		Scoped:    q.Scoped,
		Items:     []RankingItem{},
	}
	if !rankingSupportedMetrics[q.MetricKey] {
		return out, fmt.Errorf("不支持的排行指标: %s", q.MetricKey)
	}
	window := q.Window
	if window <= 0 {
		window = rankingDefaultWindow
	}
	out.WindowSeconds = int(window.Seconds())
	limit := q.Limit
	if limit <= 0 || limit > rankingMaxLimit {
		limit = 20
	}
	// 非管理员但无任何可访问实例 → 空榜（不整拒，与 SeriesBatch 的越权剔除一致）。
	if q.Scoped && len(q.AllowedIDs) == 0 {
		return out, nil
	}

	now := time.Now().UTC()
	from, to := now.Add(-window), now

	// 按窗口跨度选样本档：raw 留存 ~48h，5m 留 ~30d，1h 留更久。
	valueTable, valueCol, tsCol, rollup := rankingSampleSource(s.db.NamingStrategy, window)

	// M5：主聚合与「未进榜计数」共用同一套过滤条件，只在是否限定时间窗上不同。
	//
	// N-1 修复：JOIN instances 时显式带 `i.deleted_at IS NULL`。实例删除是 GORM 软删
	// （`InstanceService.Delete` 走 `tx.Delete`），行仍在表里；不带该条件会把软删实例
	// （其序列残留、且不再产生新样本）的历史值继续列进跨实例排行榜。
	// 与 instance_group.go / beacon_sync.go JOINS instances 的既有范式一致。
	base := func() *gorm.DB {
		query := s.db.Table(valueTable+" AS v").
			Joins("JOIN metric_series AS s ON s.id = v.series_id").
			Joins("JOIN instances AS i ON i.uuid = s.instance_id AND i.deleted_at IS NULL").
			Where("s.scope = ? AND s.metric_key = ? AND s.world = ''", model.MetricScopeInstance, q.MetricKey)
		if q.NodeUUID != "" {
			query = query.Where("s.node_uuid = ?", q.NodeUUID)
		}
		if q.Scoped {
			query = query.Where("i.id IN ?", q.AllowedIDs)
		}
		return query
	}

	// M-6：`s.node_uuid` 是刻意的**裸列**——不在 GROUP BY 内、不受聚合包裹。
	// SQLite 保证「查询含单个 max() 聚合时，所有裸列取自该 max 所在的那一行」，
	// 故 MAX(v.<tsCol>) 是这里唯一的 max()，node_uuid 便确定地取自「窗口内最新样本所属序列」，
	// 即实例迁移后的当前节点（由 TestMetric_InstanceRanking_OneSeatPerInstance 断言锁定）。
	// AVG 不属于该规则（非 min/max），故 value 是跨序列全体样本的均值，不受裸列语义影响。
	sel := fmt.Sprintf(
		"s.instance_id AS instance_uuid, i.id AS instance_id, i.name AS name, s.node_uuid AS node_uuid, "+
			"AVG(v.%s) AS value, CAST(strftime('%%s', MAX(v.%s)) AS INTEGER) AS sampled_unix", valueCol, tsCol,
	)
	// M-6：分组键**只**含实例身份（s.instance_id, i.id, i.name）。若把 s.node_uuid 放回
	// GROUP BY，同一实例的多序列会重新裂成多行、各占一名（正是本修复要消除的缺陷）。
	query := base().Select(sel).
		Where(fmt.Sprintf("v.%s >= ? AND v.%s <= ?", tsCol, tsCol), from, to).
		Group("s.instance_id, i.id, i.name")
	if rollup {
		// rollup 档无 NULL 语义：桶存在即代表该桶有数据。
		query = query.Having("COUNT(*) > 0")
	} else {
		// raw 档 value 可为 NULL（缺测）：窗口内必须至少一条非空样本才进榜。
		query = query.Having("COUNT(v.value) > 0")
	}

	var rows []rankingRow
	order := "value DESC, i.name ASC"
	if out.Order == "asc" {
		order = "value ASC, i.name ASC"
	}
	if err := query.Order(order).Limit(limit).Scan(&rows).Error; err != nil {
		return out, err
	}
	for i, r := range rows {
		out.Items = append(out.Items, RankingItem{
			InstanceID:   r.InstanceID,
			InstanceUUID: r.InstanceUUID,
			Name:         r.Name,
			NodeUUID:     r.NodeUUID,
			Value:        r.Value,
			Rank:         i + 1,
			SampledAt:    time.Unix(r.SampledUnix, 0).UTC(),
		})
	}
	out.SkippedNoData = s.rankingSkippedNoData(base, tsCol, from, to)
	return out, nil
}

// rankingRow 排行聚合查询的扫描行（sampled_at 用 unix 秒规避 sqlite 聚合列时间扫描限制）。
type rankingRow struct {
	InstanceUUID string  `gorm:"column:instance_uuid"`
	InstanceID   uint    `gorm:"column:instance_id"`
	Name         string  `gorm:"column:name"`
	NodeUUID     string  `gorm:"column:node_uuid"`
	Value        float64 `gorm:"column:value"`
	SampledUnix  int64   `gorm:"column:sampled_unix"`
}

// rankingSampleSource 据窗口跨度选择样本表与列名。返回 (表名, 值列, 时间列, 是否 rollup 档)。
func rankingSampleSource(namer interface{ TableName(string) string }, window time.Duration) (string, string, string, bool) {
	switch {
	case window <= rankingMaxRawWindow:
		return namer.TableName("MetricSampleRaw"), "value", "ts", false
	case window <= rankingMax5mWindow:
		return namer.TableName("MetricRollup5m"), "avg", "bucket_ts", true
	default:
		return namer.TableName("MetricRollup1h"), "avg", "bucket_ts", true
	}
}

// rankingSkippedNoData 统计有序列（该指标、同 scope/收敛条件）但窗口内无样本的实例数，即未进榜数。
//
// M5 修复：原实现跑两条 `COUNT(DISTINCT s.instance_id)`（一条不限定时间窗计「有序列实例数」，
// 一条限定窗口计「有样本实例数」），在 500 实例 × 2016 桶下两次全表聚合共约 0.9s。
// 现合并为**一条**查询：用条件聚合一次算出同窗口内的两个计数
// （`COUNT(DISTINCT CASE WHEN <在窗口内> THEN s.instance_id END)`），减半聚合开销。
// 仍是单条 SQL，不引入 N+1；查询失败退化为 0，不影响主结果。
//
// m3：退化值必须有日志。查询故障时前端会读到 `skippedNoData: 0`，与「确实没有跳过」
// 不可区分——静默吞错会让观测面出现「看起来正常」的假象，故按 Warn 记录（与 metric.go
// 其它「不影响主结果」的吞错点同级别）。
func (s *MetricService) rankingSkippedNoData(base func() *gorm.DB, tsCol string, from, to time.Time) int {
	var row struct {
		WithSeries  int64 `gorm:"column:with_series"`
		WithSamples int64 `gorm:"column:with_samples"`
	}
	// 注意：必须用 CASE 把「有序列」与「有样本」分开数——直接 COUNT(DISTINCT) 加了时间条件后
	// 两个计数会退化成同一个数（skipped 恒 0）。
	sel := fmt.Sprintf(
		"COUNT(DISTINCT s.instance_id) AS with_series, "+
			"COUNT(DISTINCT CASE WHEN v.%s >= ? AND v.%s <= ? THEN s.instance_id END) AS with_samples",
		tsCol, tsCol,
	)
	err := base().Select(sel, from, to).Scan(&row).Error
	if err != nil {
		slog.Warn("排行：未进榜计数查询失败，按 0 返回（不影响主榜）", "error", err)
		return 0
	}
	skipped := int(row.WithSeries - row.WithSamples)
	if skipped < 0 {
		return 0
	}
	return skipped
}

// PlayerTrendQuery 玩家在线趋势查询（FR-469）。
type PlayerTrendQuery struct {
	From, To   time.Time
	Resolution string
	Location   *time.Location // 时段分布时区；nil → 服务器本地时区
}

// PlayerTrendResult 全网玩家在线趋势与时段分布（FR-469）。
type PlayerTrendResult struct {
	Resolution string        `json:"resolution"`
	Timezone   string        `json:"timezone"`
	Trend      []SeriesPoint `json:"trend"`      // 全网在线合计曲线（跨实例 sum）
	HourlyDist []float64     `json:"hourlyDist"` // 24 个时段（0~23 时，本地时区）平均在线
	PeakValue  float64       `json:"peakValue"`
	PeakAt     *time.Time    `json:"peakAt"`
	DailyAvg   float64       `json:"dailyAvg"`
}

// PlayerTrend 返回窗口内全网在线人数趋势曲线 + 24 时段分布 + 峰值/日均（FR-469）。
// 时段分桶按查询时区（默认服务器本地时区）的小时归并求均值。
func (s *MetricService) PlayerTrend(q PlayerTrendQuery) (PlayerTrendResult, error) {
	loc := q.Location
	if loc == nil {
		loc = time.Local
	}
	res := selectResolution(q.To.Sub(q.From), q.Resolution)
	tr, err := s.aggregateTrend(model.MetricScopeInstance, model.MetricInstPlayersOnline, "count",
		q.From.UTC(), q.To.UTC(), res, true)
	if err != nil {
		return PlayerTrendResult{}, err
	}

	hourSum := make([]float64, 24)
	hourN := make([]int, 24)
	var sum float64
	var n int
	var peak float64
	var peakAt *time.Time
	for _, p := range tr.Points {
		if p.Avg == nil {
			continue
		}
		v := *p.Avg
		sum += v
		n++
		h := p.TS.In(loc).Hour()
		hourSum[h] += v
		hourN[h]++
		if peakAt == nil || v > peak {
			vv := v
			ts := p.TS.UTC()
			peak, peakAt = vv, &ts
		}
	}
	dist := make([]float64, 24)
	for h := 0; h < 24; h++ {
		if hourN[h] > 0 {
			dist[h] = hourSum[h] / float64(hourN[h])
		}
	}
	out := PlayerTrendResult{
		Resolution: res,
		Timezone:   loc.String(),
		Trend:      tr.Points,
		HourlyDist: dist,
		PeakValue:  peak,
		PeakAt:     peakAt,
	}
	if n > 0 {
		out.DailyAvg = sum / float64(n)
	}
	return out, nil
}
