package service

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 观测聚合常量（ADR-049：单档小时桶离线卷积 + TTL 清理）。
const (
	// obsBucket 快照桶粒度：整小时。
	obsBucket = time.Hour
	// obsAggregateEvery 后台卷积周期。
	obsAggregateEvery = 10 * time.Minute
	// obsReaggregateWindow 每次卷积重算的完结桶窗口（保证延迟到达的明细被纳入，且开销有界）。
	obsReaggregateWindow = 48 * time.Hour
	// obsSnapshotRetention 快照自身留存（小时桶 × 半年）。
	obsSnapshotRetention = 180 * 24 * time.Hour
	// obsEventDetailRetention 明细（client_dist_event）保留窗，须与 ClientDistTrackingService.retentionDays 一致；
	// 决定跨区间「活跃客户端」能否回查明细做精确去重（ADR-049 §4）。
	obsEventDetailRetention = 14 * 24 * time.Hour
)

// ClientDistObservabilityService 客户端分发观测数据底座（FR-217，见 ADR-049）。
//
// 离线把 client_dist_event（FR-093）+ client_telemetry（FR-094）卷积为按「频道×小时桶」的时序快照
// client_dist_snapshot，供观测·分发监控页（FR-218）与频道统计 Tab 扩维（FR-219）消费。聚合落 CP
// （架构不变量：Worker 不直连 DB），复用 scheduler 式后台 goroutine。幂等：按 (channel,bucket) upsert 覆盖。
type ClientDistObservabilityService struct {
	db   *gorm.DB
	stop chan struct{}
}

// NewClientDistObservabilityService 创建观测服务。
func NewClientDistObservabilityService(db *gorm.DB) *ClientDistObservabilityService {
	return &ClientDistObservabilityService{db: db, stop: make(chan struct{})}
}

// bucketAgg 单个 (channel,hour) 桶的卷积累加器。
type bucketAgg struct {
	channelID string
	bucket    time.Time

	manifestPulls int64
	artifactPulls int64
	downloadBytes int64
	casHit        int64
	casMiss       int64
	machines      map[string]struct{} // 桶内 machineId 去重集（卷积期临时，落库只存 size）
	versionDist   map[string]int64
	platformDist  map[string]int64

	updateTotal      int64
	updateSuccess    int64
	updateFailStatic int64
	updateRolledBack int64
	updateError      int64
	lagDist          map[string]int64
}

func newBucketAgg(channel string, bucket time.Time) *bucketAgg {
	return &bucketAgg{
		channelID:    channel,
		bucket:       bucket,
		machines:     map[string]struct{}{},
		versionDist:  map[string]int64{},
		platformDist: map[string]int64{},
		lagDist:      map[string]int64{},
	}
}

// obsBucketStart 把时刻向下对齐到整小时桶起点（UTC）。
func obsBucketStart(t time.Time) time.Time {
	return t.UTC().Truncate(obsBucket)
}

// AggregateAndPurge 卷积已完结的小时桶（重算近 obsReaggregateWindow）并按 TTL 清理过期快照。幂等可重跑。
func (s *ClientDistObservabilityService) AggregateAndPurge(now time.Time) error {
	if err := s.aggregate(now); err != nil {
		return err
	}
	return s.purge(now)
}

// aggregate 卷积 [windowStart, cutoff) 内完结小时桶的明细 + 遥测为快照并 upsert。
func (s *ClientDistObservabilityService) aggregate(now time.Time) error {
	cutoff := obsBucketStart(now)                       // 只卷已完结的桶（< 当前小时桶起点）
	windowStart := cutoff.Add(-obsReaggregateWindow)    // 重算窗口下界
	channelToCurrent, err := s.channelCurrentVersions() // 频道 latest 版本指针（算滞后用）
	if err != nil {
		return err
	}

	aggs := map[string]*bucketAgg{}
	keyOf := func(channel string, bucket time.Time) string {
		return channel + "|" + strconv.FormatInt(bucket.UnixNano(), 10)
	}
	getAgg := func(channel string, bucket time.Time) *bucketAgg {
		k := keyOf(channel, bucket)
		a := aggs[k]
		if a == nil {
			a = newBucketAgg(channel, bucket)
			aggs[k] = a
		}
		return a
	}

	// 拉取/下载明细 → 拉取侧维度。
	var events []model.ClientDistEvent
	if err := s.db.Where("created_at >= ? AND created_at < ?", windowStart, cutoff).
		Order("created_at").Find(&events).Error; err != nil {
		return err
	}
	for _, e := range events {
		a := getAgg(e.ChannelID, obsBucketStart(e.CreatedAt))
		a.downloadBytes += e.Bytes
		switch e.Kind {
		case "manifest":
			a.manifestPulls++
			if e.Version > 0 {
				a.versionDist[strconv.Itoa(e.Version)]++
			}
		case "artifact":
			a.artifactPulls++
			// CAS 命中 = 304（客户端已有该制品）；未命中 = 200/206（实际传输）。
			switch e.Status {
			case 304:
				a.casHit++
			case 200, 206:
				a.casMiss++
			}
		}
		if e.MachineID != "" {
			a.machines[e.MachineID] = struct{}{}
		}
	}

	// 更新结果遥测 → 更新侧维度 + 平台/滞后分布。
	var tels []model.ClientTelemetry
	if err := s.db.Where("created_at >= ? AND created_at < ?", windowStart, cutoff).
		Order("created_at").Find(&tels).Error; err != nil {
		return err
	}
	for _, t := range tels {
		a := getAgg(t.ChannelID, obsBucketStart(t.CreatedAt))
		a.updateTotal++
		switch t.Result {
		case "success":
			a.updateSuccess++
		case "fail-static":
			a.updateFailStatic++
		case "rolled-back":
			a.updateRolledBack++
		case "error":
			a.updateError++
		}
		if t.OS != "" {
			a.platformDist[t.OS]++
		}
		if t.ToVersion > 0 {
			lag := channelToCurrent[t.ChannelID] - t.ToVersion
			if lag < 0 {
				lag = 0
			}
			a.lagDist[strconv.Itoa(lag)]++
		}
	}

	// upsert 每个桶（幂等：OnConflict 覆盖全部聚合列）。
	now = now.UTC()
	for _, a := range aggs {
		row := model.ClientDistSnapshot{
			ChannelID:        a.channelID,
			BucketTS:         a.bucket,
			ManifestPulls:    a.manifestPulls,
			ArtifactPulls:    a.artifactPulls,
			DownloadBytes:    a.downloadBytes,
			CASHit:           a.casHit,
			CASMiss:          a.casMiss,
			ActiveMachines:   int64(len(a.machines)),
			VersionDist:      marshalDist(a.versionDist),
			PlatformDist:     marshalDist(a.platformDist),
			UpdateTotal:      a.updateTotal,
			UpdateSuccess:    a.updateSuccess,
			UpdateFailStatic: a.updateFailStatic,
			UpdateRolledBack: a.updateRolledBack,
			UpdateError:      a.updateError,
			LagDist:          marshalDist(a.lagDist),
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		if err := s.db.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "channel_id"}, {Name: "bucket_ts"}},
			DoUpdates: clause.Assignments(map[string]any{
				"manifest_pulls":     row.ManifestPulls,
				"artifact_pulls":     row.ArtifactPulls,
				"download_bytes":     row.DownloadBytes,
				"cas_hit":            row.CASHit,
				"cas_miss":           row.CASMiss,
				"active_machines":    row.ActiveMachines,
				"version_dist":       row.VersionDist,
				"platform_dist":      row.PlatformDist,
				"update_total":       row.UpdateTotal,
				"update_success":     row.UpdateSuccess,
				"update_fail_static": row.UpdateFailStatic,
				"update_rolled_back": row.UpdateRolledBack,
				"update_error":       row.UpdateError,
				"lag_dist":           row.LagDist,
				"updated_at":         now,
			}),
		}).Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

// channelCurrentVersions 取各频道当前 latest 版本指针（算版本滞后用）。
func (s *ClientDistObservabilityService) channelCurrentVersions() (map[string]int, error) {
	var chans []model.ClientChannel
	if err := s.db.Select("channel_id, current_version").Find(&chans).Error; err != nil {
		return nil, err
	}
	out := make(map[string]int, len(chans))
	for _, c := range chans {
		out[c.ChannelID] = c.CurrentVersion
	}
	return out, nil
}

// purge 删除超留存期的快照行。
func (s *ClientDistObservabilityService) purge(now time.Time) error {
	cutoff := now.UTC().Add(-obsSnapshotRetention)
	return s.db.Where("bucket_ts < ?", cutoff).Delete(&model.ClientDistSnapshot{}).Error
}

// marshalDist 序列化分布 map 为 JSON（空 map → 空串，省存储）。
func marshalDist(m map[string]int64) string {
	if len(m) == 0 {
		return ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

// unmarshalDist 反序列化分布 JSON（空串/非法 → 空 map）。
func unmarshalDist(s string) map[string]int64 {
	if s == "" {
		return map[string]int64{}
	}
	m := map[string]int64{}
	_ = json.Unmarshal([]byte(s), &m)
	return m
}

// Start 启动后台卷积/清理循环（每 obsAggregateEvery）。
func (s *ClientDistObservabilityService) Start() {
	go func() {
		t := time.NewTicker(obsAggregateEvery)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case now := <-t.C:
				if err := s.AggregateAndPurge(now); err != nil {
					slog.Error("客户端分发观测卷积/清理失败", "error", err)
				}
			}
		}
	}()
	slog.Info("客户端分发观测卷积器已启动")
}

// Stop 停止后台循环。
func (s *ClientDistObservabilityService) Stop() {
	close(s.stop)
	slog.Info("客户端分发观测卷积器已停止")
}
