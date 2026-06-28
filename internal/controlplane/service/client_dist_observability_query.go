package service

import (
	"sort"
	"strconv"
	"time"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// ObsSeriesPoint 观测时序的一个小时桶点（FR-217）。
type ObsSeriesPoint struct {
	TS               time.Time `json:"ts"`
	ManifestPulls    int64     `json:"manifestPulls"`
	ArtifactPulls    int64     `json:"artifactPulls"`
	DownloadBytes    int64     `json:"downloadBytes"`
	CASHit           int64     `json:"casHit"`
	CASMiss          int64     `json:"casMiss"`
	ActiveMachines   int64     `json:"activeMachines"`
	UpdateTotal      int64     `json:"updateTotal"`
	UpdateSuccess    int64     `json:"updateSuccess"`
	UpdateFailStatic int64     `json:"updateFailStatic"`
	UpdateRolledBack int64     `json:"updateRolledBack"`
	UpdateError      int64     `json:"updateError"`
}

// ObsSummary 区间内跨桶汇总标量 + 派生率（FR-217）。
type ObsSummary struct {
	ManifestPulls    int64   `json:"manifestPulls"`
	ArtifactPulls    int64   `json:"artifactPulls"`
	DownloadBytes    int64   `json:"downloadBytes"`
	CASHit           int64   `json:"casHit"`
	CASMiss          int64   `json:"casMiss"`
	UpdateTotal      int64   `json:"updateTotal"`
	UpdateSuccess    int64   `json:"updateSuccess"`
	UpdateFailStatic int64   `json:"updateFailStatic"`
	UpdateRolledBack int64   `json:"updateRolledBack"`
	UpdateError      int64   `json:"updateError"`
	SuccessRate      float64 `json:"successRate"`
	FailStaticRate   float64 `json:"failStaticRate"`
	RollbackRate     float64 `json:"rollbackRate"`
	CASHitRate       float64 `json:"casHitRate"`
	// ActiveMachines 区间「活跃客户端」；ActiveMachinesExact 标明是否为精确去重独立数（ADR-049 §4）。
	ActiveMachines      int64 `json:"activeMachines"`
	ActiveMachinesExact bool  `json:"activeMachinesExact"`
}

// ObsVersionCount 版本分布项。
type ObsVersionCount struct {
	Version int   `json:"version"`
	Count   int64 `json:"count"`
}

// ObsPlatformCount 平台分布项。
type ObsPlatformCount struct {
	OS    string `json:"os"`
	Count int64  `json:"count"`
}

// ObsLagCount 版本滞后分布项。
type ObsLagCount struct {
	Lag   int   `json:"lag"`
	Count int64 `json:"count"`
}

// ObservabilityResult 观测查询复合结果（FR-217）。
type ObservabilityResult struct {
	ChannelID    string             `json:"channelId"`
	From         time.Time          `json:"from"`
	To           time.Time          `json:"to"`
	Series       []ObsSeriesPoint   `json:"series"`
	Summary      ObsSummary         `json:"summary"`
	VersionDist  []ObsVersionCount  `json:"versionDist"`
	PlatformDist []ObsPlatformCount `json:"platformDist"`
	LagDist      []ObsLagCount      `json:"lagDist"`
}

// ObservabilityQuery 观测查询参数。
type ObservabilityQuery struct {
	ChannelID string // 空=总（跨频道合并）
	From, To  time.Time
}

// Query 据目标频道与区间返回观测时序 + 区间分布聚合 + 汇总标量（FR-217）。
func (s *ClientDistObservabilityService) Query(q ObservabilityQuery) (*ObservabilityResult, error) {
	return s.queryAt(time.Now().UTC(), q)
}

// queryAt 是 Query 的可测内核，now 注入便于断言「活跃客户端精确性」依明细保留窗的判定。
func (s *ClientDistObservabilityService) queryAt(now time.Time, q ObservabilityQuery) (*ObservabilityResult, error) {
	from, to := q.From.UTC(), q.To.UTC()
	out := &ObservabilityResult{
		ChannelID: q.ChannelID, From: from, To: to,
		Series: []ObsSeriesPoint{}, VersionDist: []ObsVersionCount{},
		PlatformDist: []ObsPlatformCount{}, LagDist: []ObsLagCount{},
	}

	db := s.db.Model(&model.ClientDistSnapshot{}).Where("bucket_ts >= ? AND bucket_ts < ?", from, to)
	if q.ChannelID != "" {
		db = db.Where("channel_id = ?", q.ChannelID)
	}
	var rows []model.ClientDistSnapshot
	if err := db.Order("bucket_ts").Find(&rows).Error; err != nil {
		return nil, err
	}

	// 跨频道（总）时同小时跨频道桶合并求和；单频道时每桶即一点。按桶起点聚合。
	byBucket := map[int64]*ObsSeriesPoint{}
	var order []int64
	verDist := map[string]int64{}
	platDist := map[string]int64{}
	lagDist := map[string]int64{}
	for _, r := range rows {
		k := r.BucketTS.UTC().UnixNano()
		p := byBucket[k]
		if p == nil {
			p = &ObsSeriesPoint{TS: r.BucketTS.UTC()}
			byBucket[k] = p
			order = append(order, k)
		}
		p.ManifestPulls += r.ManifestPulls
		p.ArtifactPulls += r.ArtifactPulls
		p.DownloadBytes += r.DownloadBytes
		p.CASHit += r.CASHit
		p.CASMiss += r.CASMiss
		p.ActiveMachines += r.ActiveMachines
		p.UpdateTotal += r.UpdateTotal
		p.UpdateSuccess += r.UpdateSuccess
		p.UpdateFailStatic += r.UpdateFailStatic
		p.UpdateRolledBack += r.UpdateRolledBack
		p.UpdateError += r.UpdateError
		mergeDist(verDist, unmarshalDist(r.VersionDist))
		mergeDist(platDist, unmarshalDist(r.PlatformDist))
		mergeDist(lagDist, unmarshalDist(r.LagDist))
	}

	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	var sum ObsSummary
	var activeSum int64
	for _, k := range order {
		p := byBucket[k]
		out.Series = append(out.Series, *p)
		sum.ManifestPulls += p.ManifestPulls
		sum.ArtifactPulls += p.ArtifactPulls
		sum.DownloadBytes += p.DownloadBytes
		sum.CASHit += p.CASHit
		sum.CASMiss += p.CASMiss
		sum.UpdateTotal += p.UpdateTotal
		sum.UpdateSuccess += p.UpdateSuccess
		sum.UpdateFailStatic += p.UpdateFailStatic
		sum.UpdateRolledBack += p.UpdateRolledBack
		sum.UpdateError += p.UpdateError
		activeSum += p.ActiveMachines
	}
	if sum.UpdateTotal > 0 {
		sum.SuccessRate = float64(sum.UpdateSuccess) / float64(sum.UpdateTotal)
		sum.FailStaticRate = float64(sum.UpdateFailStatic) / float64(sum.UpdateTotal)
		sum.RollbackRate = float64(sum.UpdateRolledBack) / float64(sum.UpdateTotal)
	}
	if casTotal := sum.CASHit + sum.CASMiss; casTotal > 0 {
		sum.CASHitRate = float64(sum.CASHit) / float64(casTotal)
	}

	// 活跃客户端独立数（ADR-049 §4）：区间完全落在明细保留窗内 → 回查明细做区间级精确去重；
	// 否则只能给各桶 active_machines 求和（人次近似上界）。
	exactActive, exact, err := s.activeMachinesExact(now, q.ChannelID, from, to)
	if err != nil {
		return nil, err
	}
	if exact {
		sum.ActiveMachines = exactActive
		sum.ActiveMachinesExact = true
	} else {
		sum.ActiveMachines = activeSum
		sum.ActiveMachinesExact = false
	}
	out.Summary = sum

	out.VersionDist = sortVersionDist(verDist)
	out.PlatformDist = sortPlatformDist(platDist)
	out.LagDist = sortLagDist(lagDist)
	return out, nil
}

// activeMachinesExact 判定区间能否对明细做精确去重并在可时返回精确独立数。
// 区间下界须 ≥ (now - 明细保留窗) 才回查明细（窗外明细已被滚动清理，回查会少算 → 不精确）。
func (s *ClientDistObservabilityService) activeMachinesExact(now time.Time, channelID string, from, to time.Time) (int64, bool, error) {
	if from.Before(now.Add(-obsEventDetailRetention)) {
		return 0, false, nil
	}
	db := s.db.Model(&model.ClientDistEvent{}).
		Where("created_at >= ? AND created_at < ? AND machine_id != ''", from, to)
	if channelID != "" {
		db = db.Where("channel_id = ?", channelID)
	}
	var n int64
	if err := db.Distinct("machine_id").Count(&n).Error; err != nil {
		return 0, false, err
	}
	return n, true, nil
}

// mergeDist 把 src 的计数累加进 dst。
func mergeDist(dst, src map[string]int64) {
	for k, v := range src {
		dst[k] += v
	}
}

// sortVersionDist 把版本分布 map 转为按 count 降序的数组（非数字键跳过）。
func sortVersionDist(m map[string]int64) []ObsVersionCount {
	out := make([]ObsVersionCount, 0, len(m))
	for k, v := range m {
		ver, err := strconv.Atoi(k)
		if err != nil {
			continue
		}
		out = append(out, ObsVersionCount{Version: ver, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Version > out[j].Version
	})
	return out
}

// sortPlatformDist 把平台分布 map 转为按 count 降序的数组。
func sortPlatformDist(m map[string]int64) []ObsPlatformCount {
	out := make([]ObsPlatformCount, 0, len(m))
	for k, v := range m {
		out = append(out, ObsPlatformCount{OS: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].OS < out[j].OS
	})
	return out
}

// sortLagDist 把滞后分布 map 转为按 lag 升序的数组（非数字键跳过）。
func sortLagDist(m map[string]int64) []ObsLagCount {
	out := make([]ObsLagCount, 0, len(m))
	for k, v := range m {
		lag, err := strconv.Atoi(k)
		if err != nil {
			continue
		}
		out = append(out, ObsLagCount{Lag: lag, Count: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Lag < out[j].Lag })
	return out
}
