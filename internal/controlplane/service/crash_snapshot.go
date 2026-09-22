package service

import (
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/crashdiag"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// CrashSnapshotService 实例崩溃快照查询 + 趋势/聚合/重分类服务（FR-313 增强 FR-470）。
// 写入在 gRPC 层（Worker 上报 ReportCrashSnapshot，含滚动修剪 + 同步分类 + 统计 upsert）；
// 此处承担 REST 读侧（列表 / 趋势 / 总览）与管理员重分类批处理。
type CrashSnapshotService struct {
	db *gorm.DB
	// correlation 关联证据（FR-470 §2.3）；nil 时列表不返回关联段。
	correlation *CrashCorrelationService
	// settings 读 crash.stat_retention_days（日粒度统计的清理窗口）。
	settings SettingsReader

	mu      sync.Mutex
	running bool
	stopCh  chan struct{}
}

// NewCrashSnapshotService 创建崩溃快照查询服务。
func NewCrashSnapshotService(db *gorm.DB) *CrashSnapshotService {
	return &CrashSnapshotService{db: db, stopCh: make(chan struct{})}
}

// SetSettingsReader 注入设置读取器（crash.stat_retention_days）。
func (s *CrashSnapshotService) SetSettingsReader(r SettingsReader) { s.settings = r }

// SetCorrelation 注入资源关联服务（FR-470，main 接线）。
func (s *CrashSnapshotService) SetCorrelation(c *CrashCorrelationService) { s.correlation = c }

// CorrelationEnabled 报告是否已装配资源关联服务（读侧据此决定是否附关联段）。
func (s *CrashSnapshotService) CorrelationEnabled() bool { return s.correlation != nil }

// Correlate 计算实例在某时刻的崩溃关联证据（未装配关联服务时返回空）。
func (s *CrashSnapshotService) Correlate(instanceID uint, occurredAt time.Time) (CrashCorrelation, error) {
	if s.correlation == nil {
		return CrashCorrelation{}, nil
	}
	return s.correlation.CorrelateInstance(instanceID, occurredAt)
}

// CorrelateBatch 一次算出多条崩溃时刻的关联证据，key = occurredAt.UnixNano()（R14）。
//
// 读侧列表用它替代逐条 Correlate：未装配关联服务时返回空映射（调用方据此不附关联段）。
func (s *CrashSnapshotService) CorrelateBatch(instanceID uint, occurredAts []time.Time) (map[int64]CrashCorrelation, error) {
	if s.correlation == nil {
		return map[int64]CrashCorrelation{}, nil
	}
	return s.correlation.CorrelateInstanceBatch(instanceID, occurredAts)
}

// ListByInstance 返回实例的崩溃快照，按发生时间倒序（最新在前，至多 K=5 条，由写侧修剪保证）。
// 实例不存在返回 ErrInstanceNotFound（平台管理员绕过组访问校验，存在性须由此兜底成 404）。
func (s *CrashSnapshotService) ListByInstance(instanceID uint) ([]model.InstanceCrashSnapshot, error) {
	var inst model.Instance
	if err := s.db.Select("id").First(&inst, instanceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInstanceNotFound
		}
		return nil, err
	}
	var out []model.InstanceCrashSnapshot
	err := s.db.Where("instance_id = ?", instanceID).
		Order("occurred_at DESC, id DESC").
		Find(&out).Error
	return out, err
}

// CrashCauseCount 按根因聚合的计数（FR-470 §2.4）。
type CrashCauseCount struct {
	RootCause string `json:"rootCause"`
	Count     int    `json:"count"`
}

// CrashSignatureCount 同类聚合（按指纹）计数。
type CrashSignatureCount struct {
	Signature string `json:"signature"`
	RootCause string `json:"rootCause"`
	Count     int    `json:"count"`
}

// CrashTrendPoint 趋势序列上的一个点（天 × 根因）。
type CrashTrendPoint struct {
	Day       string `json:"day"`
	RootCause string `json:"rootCause"`
	Count     int    `json:"count"`
}

// CrashTrend 实例崩溃趋势（FR-470 §2.4）：来自 InstanceCrashStat，**不受 K=5 快照裁剪影响**。
type CrashTrend struct {
	InstanceID    uint                  `json:"instanceId"`
	Days          int                   `json:"days"`
	Total         int                   `json:"total"`
	Points        []CrashTrendPoint     `json:"points"`
	ByRootCause   []CrashCauseCount     `json:"byRootCause"`
	TopSignatures []CrashSignatureCount `json:"topSignatures"`
}

// CrashInstanceCount 平台维度 Top 实例。
type CrashInstanceCount struct {
	InstanceID   uint   `json:"instanceId"`
	InstanceName string `json:"instanceName,omitempty"`
	Count        int    `json:"count"`
}

// CrashOverview 平台崩溃总览（FR-470 §2.4）。
type CrashOverview struct {
	Days          int                   `json:"days"`
	Total         int                   `json:"total"`
	Trend         []CrashTrendPoint     `json:"trend"`
	TopRootCauses []CrashCauseCount     `json:"topRootCauses"`
	TopInstances  []CrashInstanceCount  `json:"topInstances"`
	TopSignatures []CrashSignatureCount `json:"topSignatures"`
}

// emptyCrashOverview 返回各切片已初始化的空总览（保证 JSON 恒为 `[]` 而非 `null`）。
func emptyCrashOverview(days int) *CrashOverview {
	return &CrashOverview{
		Days: days, Trend: []CrashTrendPoint{}, TopRootCauses: []CrashCauseCount{},
		TopInstances: []CrashInstanceCount{}, TopSignatures: []CrashSignatureCount{},
	}
}

// trendAggregator 把统计行按 (天 × 根因) 聚合成趋势序列。
//
// **为什么必须聚合**：趋势行的数据源 `InstanceCrashStat` 唯一键是
// (实例, 天, 根因, **指纹**)，同一「天 × 根因」天然会有多行（每个指纹一行）。
// 趋势序列的语义是「每天各根因崩了几次」（`CrashTrendPoint.Count` 是 (天, 根因)
// 维度的**合计计数**，不是「一行的计数」），故必须先按 (天 × 根因) 求和再输出。
// 逐行 append 会让点数等于统计行数：同一天同一根因出现 N 个点、每点 count 各为
// 1/2/…，与同一次响应里正确的 `TopRootCauses` 合计自相矛盾（前端也按天自行求和，
// 说明聚合才是预期语义）。
//
// 统计行按 (bucket_day, root_cause) 升序读入时输出同样有序；map 索引使聚合不依赖
// 调用方排序（顺序变化也不会退化成逐行输出或产生重复点）。
type trendAggregator struct {
	points []CrashTrendPoint
	index  map[string]int
}

// add 并入一条统计行：同 (天, 根因) 累加计数，否则追加一个新点。
func (a *trendAggregator) add(day, rootCause string, count int) {
	if a.index == nil {
		a.index = make(map[string]int)
	}
	key := day + "\x00" + rootCause
	if i, ok := a.index[key]; ok {
		a.points[i].Count += count
		return
	}
	a.index[key] = len(a.points)
	a.points = append(a.points, CrashTrendPoint{Day: day, RootCause: rootCause, Count: count})
}

// result 返回聚合后的趋势序列（无点时为空切片，保证 JSON 恒为 `[]` 而非 `null`）。
func (a *trendAggregator) result() []CrashTrendPoint {
	if a.points == nil {
		return []CrashTrendPoint{}
	}
	return a.points
}

// 崩溃统计窗口的默认值与上限（edge m-5/m-6）。
//
// 导出为常量：MCP 工具描述与 router 的 `?days=` 解析都要引用同一口径，
// 原先 365 在两处硬编码（mcp/tools_instance.go 的工具描述、本文件的归一化），
// 改一处漏一处就会出现「Schema 声明与实现校验点不一致」。
const (
	// CrashTrendDefaultDays 趋势窗口默认天数（未指定/非法时）。
	CrashTrendDefaultDays = 30
	// CrashTrendMaxDays 趋势窗口上限（超出按上限收敛，保证查询有界）。
	CrashTrendMaxDays = 365
)

// normalizeCrashDays 归一化趋势窗口天数（默认 30，上限 365，下限 1）。
func normalizeCrashDays(days int) int {
	if days <= 0 {
		return CrashTrendDefaultDays
	}
	if days > CrashTrendMaxDays {
		return CrashTrendMaxDays
	}
	return days
}

// TrendByInstance 查询实例崩溃趋势（FR-470）。days<=0 默认 30。
func (s *CrashSnapshotService) TrendByInstance(instanceID uint, days int) (*CrashTrend, error) {
	var inst model.Instance
	if err := s.db.Select("id").First(&inst, instanceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInstanceNotFound
		}
		return nil, err
	}
	days = normalizeCrashDays(days)
	from := crashDayFloor(days)

	var stats []model.InstanceCrashStat
	if err := s.db.Where("instance_id = ? AND bucket_day >= ?", instanceID, from).
		Order("bucket_day ASC, root_cause ASC").
		Find(&stats).Error; err != nil {
		return nil, err
	}

	trend := &CrashTrend{InstanceID: instanceID, Days: days, Points: []CrashTrendPoint{}}
	agg := &trendAggregator{}
	causeTotal := map[string]int{}
	sigTotal := map[string]*CrashSignatureCount{}
	for _, st := range stats {
		trend.Total += st.Count
		// 按 (天 × 根因) 求和：同一行源会有多个指纹各成一行，逐行 append 会输出重复点。
		agg.add(st.BucketDay, st.RootCause, st.Count)
		causeTotal[st.RootCause] += st.Count
		key := st.RootCause + "\x00" + st.Signature
		if sigTotal[key] == nil {
			sigTotal[key] = &CrashSignatureCount{Signature: st.Signature, RootCause: st.RootCause}
		}
		sigTotal[key].Count += st.Count
	}
	trend.Points = agg.result()
	trend.ByRootCause = sortedCauseCounts(causeTotal)
	trend.TopSignatures = topSignatureCounts(sigTotal, 10)
	return trend, nil
}

// crashOverviewMaxRows 单次总览查询的硬行数上限（R15）。
//
// 缺陷现场：`Overview` 原先一次 `Find` 出整个窗口的全部统计行（行数 = 实例数 × 天数 ×
// 根因 × 指纹）。默认 90 天窗口下，500 实例 × 90 天 × 3 根因 × 20 指纹 ≈ 270 万行
// 全量加载进内存——行数上界由 `crash.stat_retention_days` 决定，没有硬上限，
// 内存与响应时间随规模线性增长。
//
// 修复：读侧只取**聚合所需的最小列集**，并把行数交给数据库侧限定（本常量）。
// 超限时在日志里明确记录截断事实——总览是「近期态势」视图，宁可给出明确标注的
// 近似值也不让请求打爆内存，但绝不静默截断（否则趋势与 Top 会被误读为全量事实）。
//
// 取 200000：即便每行 64 字节，也只占 ~13MB，远大于真实平台规模（64 服）而远小于
// 危险量级；超过它说明该部署的统计粒度需要另行治理（降窗口或收敛指纹基数）。
//
// 为什么不把聚合下推到 SQL：总览同时要 Total/Trend/ByRootCause/TopInstances/
// TopSignatures 五种输出，下推需要 5 条 SQL（其中 trend 还要 (day, root_cause)
// 二级分组），而单条读取的成本已被本上限约束住。未来若平台规模显著超过本上限，
// 应按需下推并各自加 LIMIT。
const crashOverviewMaxRows = 200000

// overviewStatsQuery 构造总览统计行的读取查询（R15）。
//
// 抽成独立函数是为了让「上限真的进了语句」可被测试断言（见 loadOverviewStats），
// 而不必真的造出 20 万行数据。
func (s *CrashSnapshotService) overviewStatsQuery(days int, scope []uint) (*gorm.DB, bool) {
	query := s.db.Where("bucket_day >= ?", crashDayFloor(days))
	if scope == nil {
		return query, true
	}
	if len(scope) == 0 {
		// 无可访问实例：返回空总览（不能退化成「不加过滤」——那正是越权读）。
		return nil, false
	}
	return query.Where("instance_id IN ?", scope), true
}

// loadOverviewStats 读取总览所需的统计行（R15）：只取聚合用得到的最小列集，
// 并按 limit 在**数据库侧**截断行数。
//
// limit < 0 表示不限制（GORM 的「无 LIMIT」语义——传 0 会生成 `LIMIT 0` 而读回零行，
// 生产路径一律传 crashOverviewMaxRows）。返回的 second 值表示 scope 判定为
// 「无可访问实例」，调用方据此直接返回空总览而不查库。
func (s *CrashSnapshotService) loadOverviewStats(days int, scope []uint, limit int) ([]model.InstanceCrashStat, bool, error) {
	query, ok := s.overviewStatsQuery(days, scope)
	if !ok {
		return nil, false, nil
	}
	var stats []model.InstanceCrashStat
	q := query.
		Select("instance_id", "bucket_day", "root_cause", "signature", "count").
		Order("bucket_day ASC, root_cause ASC")
	// limit == 0 时 GORM 会生成 `LIMIT 0`（读回零行），与「不限」的调用者意图相反，
	// 故只有 limit > 0 才施加限制。
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&stats).Error; err != nil {
		return nil, true, err
	}
	return stats, true, nil
}

// Overview 平台崩溃总览：Top 根因 / Top 实例 / Top 同类 / 时间窗趋势（全部实例）。
//
// scope（m-3）：nil 表示不限范围（平台管理员）；非 nil 时只统计 scope 内的实例，
// 避免「组只读角色经 /crash-overview 看到其它组的实例名与崩溃量」——总览会回填
// InstanceName，跨组可见即等于跨组泄露实例清单。
func (s *CrashSnapshotService) Overview(days int, scope []uint) (*CrashOverview, error) {
	days = normalizeCrashDays(days)

	stats, scoped, err := s.loadOverviewStats(days, scope, crashOverviewMaxRows)
	if err != nil {
		return nil, err
	}
	if !scoped {
		return emptyCrashOverview(days), nil
	}
	if len(stats) >= crashOverviewMaxRows {
		// 明确留痕：总览是近似值，不能静默截断（否则趋势与 Top 会被误读为全量事实）。
		slog.Warn("崩溃总览统计行达到上限，本次结果按最近窗口截断",
			"rows", len(stats), "limit", crashOverviewMaxRows, "days", days)
	}

	ov := emptyCrashOverview(days)
	causeTotal := map[string]int{}
	instTotal := map[uint]int{}
	sigTotal := map[string]*CrashSignatureCount{}
	agg := &trendAggregator{}
	for _, st := range stats {
		ov.Total += st.Count
		// 同 TrendByInstance：趋势点按 (天 × 根因) 聚合，否则每个指纹各出一个点。
		agg.add(st.BucketDay, st.RootCause, st.Count)
		causeTotal[st.RootCause] += st.Count
		instTotal[st.InstanceID] += st.Count
		key := st.RootCause + "\x00" + st.Signature
		if sigTotal[key] == nil {
			sigTotal[key] = &CrashSignatureCount{Signature: st.Signature, RootCause: st.RootCause}
		}
		sigTotal[key].Count += st.Count
	}
	ov.Trend = agg.result()
	ov.TopRootCauses = sortedCauseCounts(causeTotal)
	ov.TopSignatures = topSignatureCounts(sigTotal, 10)

	topInsts := make([]CrashInstanceCount, 0, len(instTotal))
	for id, c := range instTotal {
		topInsts = append(topInsts, CrashInstanceCount{InstanceID: id, Count: c})
	}
	sort.SliceStable(topInsts, func(i, j int) bool {
		if topInsts[i].Count != topInsts[j].Count {
			return topInsts[i].Count > topInsts[j].Count
		}
		return topInsts[i].InstanceID < topInsts[j].InstanceID
	})
	if len(topInsts) > 10 {
		topInsts = topInsts[:10]
	}
	// 回填实例名（Top 实例展示用）。
	if len(topInsts) > 0 {
		ids := make([]uint, 0, len(topInsts))
		for _, ti := range topInsts {
			ids = append(ids, ti.InstanceID)
		}
		var insts []model.Instance
		s.db.Select("id", "name").Where("id IN ?", ids).Find(&insts)
		nameByID := make(map[uint]string, len(insts))
		for _, in := range insts {
			nameByID[in.ID] = in.Name
		}
		for i := range topInsts {
			topInsts[i].InstanceName = nameByID[topInsts[i].InstanceID]
		}
	}
	ov.TopInstances = topInsts
	return ov, nil
}

// ReclassifyResult 重分类批处理结果。
type ReclassifyResult struct {
	Scanned    int `json:"scanned"`
	Updated    int `json:"updated"`
	Backfilled int `json:"backfilled"`
}

// ReclassifyAll 对历史快照按当前规则重跑分类，回填分类列并修正统计（FR-470 §2.5）。
//
// 统计修正采用「差量搬移」：旧分类与新分类不同则从旧桶减 1、新桶加 1；旧快照无分类
// （FR-470 之前入库）按回填处理（只加不减）。因此重复执行幂等——第二次运行时旧分类已等于新分类，不再变动。
func (s *CrashSnapshotService) ReclassifyAll() (*ReclassifyResult, error) {
	res := &ReclassifyResult{}
	const batch = 200
	var lastID uint
	for {
		var snaps []model.InstanceCrashSnapshot
		if err := s.db.Where("id > ?", lastID).Order("id ASC").Limit(batch).Find(&snaps).Error; err != nil {
			return nil, err
		}
		if len(snaps) == 0 {
			break
		}
		for i := range snaps {
			snap := &snaps[i]
			lastID = snap.ID
			res.Scanned++
			class := crashdiag.ClassifyCrashSnapshot(snap.ExitCode, snap.Signal, snap.TailOutput)
			oldCause, oldSig := snap.RootCause, snap.Signature
			hadOld := oldCause != ""
			changed := oldCause != class.RootCause || oldSig != class.Signature
			if !changed {
				continue
			}
			err := s.db.Transaction(func(tx *gorm.DB) error {
				if err := crashdiag.ReapplyCrashStat(tx, snap.InstanceID, snap.OccurredAt,
					oldCause, oldSig, class.RootCause, class.Signature, hadOld); err != nil {
					return err
				}
				return tx.Model(&model.InstanceCrashSnapshot{}).Where("id = ?", snap.ID).
					Updates(map[string]any{
						"root_cause": class.RootCause,
						"signature":  class.Signature,
						"evidence":   crashdiag.EncodeCrashEvidence(class.Evidence),
						"confidence": class.Confidence,
					}).Error
			})
			if err != nil {
				return nil, err
			}
			res.Updated++
			if !hadOld {
				res.Backfilled++
			}
		}
	}
	return res, nil
}

// crashStatRetentionTick 统计保留裁剪巡检周期（日粒度数据，每小时查一次足够）。
const crashStatRetentionTick = time.Hour

// Start 启动统计保留裁剪巡检（幂等）。未注入设置读取器时用默认 90 天。
//
// 为什么需要它：InstanceCrashStat 是日粒度累积表（行数 = 实例数 × 天数 × 根因数），
// 没有保留窗口会随时间无限增长——spec §5「统计表膨胀」正是靠这个窗口兜底。
//
// stopCh 每次 Start 重建（见 restartableStop），与 SnapshotService.Start 同口径（R22/R30）。
func (s *CrashSnapshotService) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	stop := restartableStop(&s.stopCh)
	s.mu.Unlock()

	go func() {
		ticker := time.NewTicker(crashStatRetentionTick)
		defer ticker.Stop()
		s.pruneCrashStatsOnce()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				s.pruneCrashStatsOnce()
			}
		}
	}()
	slog.Info("崩溃统计保留裁剪巡检已启动", "retentionDays", s.crashStatRetentionDays())
}

// Stop 停止统计保留裁剪巡检。
func (s *CrashSnapshotService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	close(s.stopCh)
	s.running = false
}

// crashStatRetentionDays 取统计保留天数（平台设置 crash.stat_retention_days，默认 90）。
func (s *CrashSnapshotService) crashStatRetentionDays() int {
	if s.settings == nil {
		return 90
	}
	n, err := strconv.Atoi(strings.TrimSpace(s.settings.EffectiveValue(SettingKeyCrashStatRetentionDays)))
	if err != nil || n < 1 {
		return 90
	}
	return n
}

// pruneCrashStatsOnce 删除早于保留窗口的日粒度统计行，返回删除条数（便于测试断言）。
func (s *CrashSnapshotService) pruneCrashStatsOnce() int {
	days := s.crashStatRetentionDays()
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")
	res := s.db.Where("bucket_day < ?", cutoff).Delete(&model.InstanceCrashStat{})
	if res.Error != nil {
		slog.Warn("裁剪崩溃统计失败", "error", res.Error)
		return 0
	}
	if res.RowsAffected > 0 {
		slog.Info("按保留天数裁剪崩溃统计", "days", days, "deleted", res.RowsAffected)
	}
	return int(res.RowsAffected)
}

// crashDayFloor 返回最近 days 天的起始桶（含今天），UTC，YYYY-MM-DD。
func crashDayFloor(days int) string {
	return time.Now().UTC().AddDate(0, 0, -(days - 1)).Format("2006-01-02")
}

// sortedCauseCounts 把根因计数 map 转为按计数降序（同数按根因字典序）的切片。
//
// R18 读侧加固：过滤掉计数 <= 0 的条目。递减路径已改为「归零即删」（crashdiag.stat），
// 但存量库里可能已有改造前中断留下的 `count = 0` 行；不过滤会让运维在趋势页看到
// 「从未发生的根因」，与 Total（加 0 不受影响）自相矛盾。
func sortedCauseCounts(m map[string]int) []CrashCauseCount {
	out := make([]CrashCauseCount, 0, len(m))
	for cause, c := range m {
		if c <= 0 {
			continue
		}
		out = append(out, CrashCauseCount{RootCause: cause, Count: c})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].RootCause < out[j].RootCause
	})
	return out
}

// topSignatureCounts 取同类聚合 Top N（计数降序，同数按指纹字典序）。
//
// 同 sortedCauseCounts 过滤计数 <= 0 的残留行（R18）。
func topSignatureCounts(m map[string]*CrashSignatureCount, n int) []CrashSignatureCount {
	out := make([]CrashSignatureCount, 0, len(m))
	for _, v := range m {
		if v.Count <= 0 {
			continue
		}
		out = append(out, *v)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Signature < out[j].Signature
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}
