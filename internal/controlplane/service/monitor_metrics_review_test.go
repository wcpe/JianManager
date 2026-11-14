package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

func findByKey(t *testing.T, samples []Sample, key string) *Sample {
	t.Helper()
	for i := range samples {
		if samples[i].MetricKey == key {
			return &samples[i]
		}
	}
	return nil
}

// 注册容量应直接进入生产心跳采样，不能由舍入后的 used/rate 反推。
func TestMetric_IngestHeartbeatUsesRegisteredCapacity(t *testing.T) {
	for name, usage := range map[string]float32{"missing usage": 0, "rounded usage": 0.3} {
		t.Run(name, func(t *testing.T) {
			svc := newMetricSvc(t)
			require.NoError(t, svc.db.AutoMigrate(&model.Node{}))
			require.NoError(t, svc.db.Create(&model.Node{UUID: "node-1", Name: "node-1", MemoryMB: 8192, DiskTotalMB: 200000}).Error)
			require.NoError(t, svc.IngestHeartbeat(&workerpb.HeartbeatRequest{
				NodeUuid: "node-1", MemoryUsedMb: 4096, MemoryUsage: usage,
				DiskUsedMb: 50000, DiskUsage: usage,
			}))
			for key, want := range map[string]float64{model.MetricNodeMemTotal: 8192 * 1024 * 1024, model.MetricNodeDiskTotal: 200000 * 1024 * 1024} {
				var series model.MetricSeries
				require.NoError(t, svc.db.Where("metric_key = ?", key).First(&series).Error)
				var sample model.MetricSampleRaw
				require.NoError(t, svc.db.Where("series_id = ?", series.ID).First(&sample).Error)
				require.NotNil(t, sample.Value)
				require.Equal(t, want, *sample.Value)
			}
		})
	}
}

func TestBuildNodeSamplesAt_DoesNotInferCapacity(t *testing.T) {
	samples := buildNodeSamplesAt(&workerpb.HeartbeatRequest{
		NodeUuid: "node-1", MemoryUsedMb: 4096, MemoryUsage: 0.3,
		DiskUsedMb: 50000, DiskUsage: 0.3,
	}, metricBase())
	require.Nil(t, findByKey(t, samples, model.MetricNodeMemTotal))
	require.Nil(t, findByKey(t, samples, model.MetricNodeDiskTotal))
}

func TestMetric_IngestHeartbeatWithoutRegisteredCapacity(t *testing.T) {
	svc := newMetricSvc(t)
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}))
	require.NoError(t, svc.db.Create(&model.Node{UUID: "unknown-capacity", Name: "unknown-capacity"}).Error)
	for _, uuid := range []string{"unknown-capacity", "unregistered"} {
		require.NoError(t, svc.IngestHeartbeat(&workerpb.HeartbeatRequest{
			NodeUuid: uuid, MemoryUsedMb: 1024, MemoryUsage: 0.5,
			DiskUsedMb: 2048, DiskUsage: 0.5,
		}))
	}
	var count int64
	require.NoError(t, svc.db.Model(&model.MetricSeries{}).Where("metric_key IN ?", []string{model.MetricNodeMemTotal, model.MetricNodeDiskTotal}).Count(&count).Error)
	require.Zero(t, count, "未注册有效容量时不得反推")
}

func TestBuildNodeSamplesAt_CPUWindowPreservesZeroAndMissing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		req   *workerpb.HeartbeatRequest
		valid bool
	}{
		{name: "missing", req: &workerpb.HeartbeatRequest{}},
		{name: "idle", req: &workerpb.HeartbeatRequest{CpuIdle: 1}, valid: true},
		{name: "busy without iowait", req: &workerpb.HeartbeatRequest{CpuUsage: 0.5, CpuUser: 0.5, CpuIdle: 0.5}, valid: true},
		{name: "legacy", req: &workerpb.HeartbeatRequest{CpuUsage: 0.5}, valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			samples := buildNodeSamplesAt(tc.req, metricBase())
			for key, want := range map[string]float64{model.MetricNodeCPUPct: float64(tc.req.CpuUsage) * 100, model.MetricNodeIOWait: float64(tc.req.Iowait) * 100} {
				sample := findByKey(t, samples, key)
				require.NotNil(t, sample, "缺测也必须留下 NULL 断点")
				if tc.valid {
					require.NotNil(t, sample.Value)
					require.InDelta(t, want, *sample.Value, 1e-6)
				} else {
					require.Nil(t, sample.Value)
				}
			}
		})
	}
}

func TestQueryProcessTop_HidesStaleSnapshots(t *testing.T) {
	svc := newMetricSvc(t)
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}, &model.Instance{}))
	now := time.Now().UTC()
	node := model.Node{UUID: "node-1", Name: "node-1", Status: model.NodeStatusOnline, LastHeartbeat: &now}
	require.NoError(t, svc.db.Create(&node).Error)
	require.NoError(t, svc.db.Create(&model.Instance{
		UUID: "inst-stale", Name: "stale", NodeID: node.ID, Status: model.InstanceStatusRunning, Type: model.InstanceTypeGeneric,
		ProcessType: model.ProcessTypeDirect, StartCommand: "sleep",
	}).Error)
	// 快照虽旧，实例和节点仍在线；年龄过期也必须过滤。
	require.NoError(t, svc.db.Create(&model.ProcessMetricSnapshot{
		NodeUUID: "node-1", InstanceUUID: "inst-stale", PID: 4242, Name: "java",
		CPUPercent: 90, SampledAt: now.Add(-10 * time.Minute),
	}).Error)

	got, err := svc.QueryProcessTop(ProcessTopQuery{InstanceUUID: "inst-stale", Sort: "cpu", Limit: 10})
	require.NoError(t, err)
	require.Empty(t, got, "过期快照不得当作当前进程")

	// 新一拍到达后正常返回
	require.NoError(t, svc.db.Create(&model.ProcessMetricSnapshot{
		NodeUUID: "node-1", InstanceUUID: "inst-stale", PID: 4243, Name: "java",
		CPUPercent: 50, SampledAt: now,
	}).Error)
	got, err = svc.QueryProcessTop(ProcessTopQuery{InstanceUUID: "inst-stale", Sort: "cpu", Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, int32(4243), got[0].PID)
	got, err = svc.QueryProcessTop(ProcessTopQuery{InstanceUUID: "inst-stale", At: now.Add(-9 * time.Minute), Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, int32(4242), got[0].PID, "At 不应用当前年龄过滤")
}

// TestRollup5m_LateRawRefreshesExistingBucket 封桶后才提交的 raw 必须能收敛回聚合桶。
func TestRollup5m_LateRawRefreshesExistingBucket(t *testing.T) {
	svc := newMetricSvc(t)
	bucket := metricBase().Add(55 * time.Minute)

	// 桶边界内先来一拍，卷积把它封存。
	require.NoError(t, svc.Ingest([]Sample{{
		NodeUUID: "node-r", Scope: model.MetricScopeNode, MetricKey: model.MetricNodeCPUPct,
		Unit: "pct", TS: bucket.Add(10 * time.Second), Value: fp(10),
	}}))
	require.NoError(t, svc.rollup5m(bucket.Add(metricBucket5m)))
	require.NoError(t, svc.rollup1h(bucket.Add(metricBucket5m)))

	var rows []model.MetricRollup5m
	require.NoError(t, svc.db.Find(&rows).Error)
	require.Len(t, rows, 1)
	require.InDelta(t, 10.0, rows[0].Avg, 1e-9)

	// 迟到样本仍落在同一个已封桶里（写闸门排队导致）。
	require.NoError(t, svc.Ingest([]Sample{{
		NodeUUID: "node-r", Scope: model.MetricScopeNode, MetricKey: model.MetricNodeCPUPct,
		Unit: "pct", TS: bucket.Add(20 * time.Second), Value: fp(30),
	}}))
	// 下一次正常 tick 必须同时修正已封的 5m 桶与上一小时桶。
	require.NoError(t, svc.rollup5m(bucket.Add(2*metricBucket5m)))
	require.NoError(t, svc.rollup1h(bucket.Add(2*metricBucket5m)))

	rows = nil
	require.NoError(t, svc.db.Find(&rows).Error)
	require.Len(t, rows, 1, "仍应只有一个桶（不得重复插入）")
	require.InDelta(t, 20.0, rows[0].Avg, 1e-9, "迟到样本必须被纳入重算")
	require.Equal(t, 2, rows[0].Count)
	var hours []model.MetricRollup1h
	require.NoError(t, svc.db.Find(&hours).Error)
	require.Len(t, hours, 1)
	require.Equal(t, 2, hours[0].Count)
	require.Equal(t, 20.0, hours[0].Avg)
	require.Equal(t, 30.0, hours[0].Last)
}

func TestQueryProcessTop_CurrentOwnershipBeforeLimit(t *testing.T) {
	for _, state := range []string{"stopped", "offline", "stale heartbeat", "no heartbeat", "deleted instance", "deleted node", "migrated"} {
		t.Run(state, func(t *testing.T) {
			svc := newMetricSvc(t)
			require.NoError(t, svc.db.AutoMigrate(&model.Node{}, &model.Instance{}))
			now := time.Now().UTC()
			node := model.Node{UUID: "live", Name: "live", Status: model.NodeStatusOnline, LastHeartbeat: &now}
			otherBeat := now
			other := model.Node{UUID: "other", Name: "other", Status: model.NodeStatusOnline, LastHeartbeat: &otherBeat}
			require.NoError(t, svc.db.Create(&node).Error)
			require.NoError(t, svc.db.Create(&other).Error)
			good := model.Instance{UUID: "good", NodeID: node.ID, Name: "good", Status: model.InstanceStatusRunning}
			bad := model.Instance{UUID: "bad", NodeID: other.ID, Name: "bad", Status: model.InstanceStatusRunning}
			require.NoError(t, svc.db.Create(&good).Error)
			require.NoError(t, svc.db.Create(&bad).Error)
			snapshotNode := other.UUID
			switch state {
			case "stopped":
				require.NoError(t, svc.db.Model(&bad).Update("status", model.InstanceStatusStopped).Error)
			case "offline":
				require.NoError(t, svc.db.Model(&other).Update("status", model.NodeStatusOffline).Error)
			case "stale heartbeat":
				require.NoError(t, svc.db.Model(&other).Update("last_heartbeat", now.Add(-3*time.Minute)).Error)
			case "no heartbeat":
				require.NoError(t, svc.db.Model(&other).Update("last_heartbeat", nil).Error)
			case "deleted instance":
				require.NoError(t, svc.db.Delete(&bad).Error)
			case "deleted node":
				require.NoError(t, svc.db.Delete(&other).Error)
			case "migrated":
				snapshotNode = node.UUID
			}
			require.NoError(t, svc.db.Create(&[]model.ProcessMetricSnapshot{
				{NodeUUID: node.UUID, InstanceUUID: good.UUID, PID: 1, CPUPercent: 1, SampledAt: now},
				{NodeUUID: snapshotNode, InstanceUUID: bad.UUID, PID: 2, CPUPercent: 99, SampledAt: now},
			}).Error)
			got, err := svc.QueryProcessTop(ProcessTopQuery{Sort: "cpu", Limit: 1})
			require.NoError(t, err)
			require.Len(t, got, 1, "应先过滤再 LIMIT")
			require.Equal(t, good.UUID, got[0].InstanceUUID)
			got, err = svc.QueryProcessTop(ProcessTopQuery{InstanceUUID: bad.UUID, At: now.Add(time.Second), Limit: 1})
			require.NoError(t, err)
			require.Len(t, got, 1, "历史不依赖当前归属和状态")
			require.Equal(t, int32(2), got[0].PID)
		})
	}
}

func TestMetric_RollupRefreshDoesNotReduceRetainedCounts(t *testing.T) {
	svc := newMetricSvc(t)
	now := metricBase().Add(48*time.Hour + 2*time.Minute)
	bucket := metricBase()
	// raw 保留边界切过桶中部，剩余 raw 不能覆盖已完整聚合的老桶。
	require.NoError(t, svc.db.Create(&model.MetricSampleRaw{SeriesID: 1, TS: bucket.Add(3 * time.Minute), Value: fp(99)}).Error)
	require.NoError(t, svc.db.Create(&model.MetricRollup5m{SeriesID: 1, BucketTS: bucket, Avg: 20, Min: 10, Max: 30, Last: 30, Count: 2}).Error)
	require.NoError(t, svc.rollup5m(now))
	rows := readRollup5m(t, svc.db)
	require.Len(t, rows, 1)
	require.Equal(t, 2, rows[0].Count)
	require.Equal(t, 20.0, rows[0].Avg)
	// 1h count 同样不能因不完整输入而降低。
	require.NoError(t, svc.db.Create(&model.MetricRollup1h{SeriesID: 1, BucketTS: bucket, Avg: 15, Min: 0, Max: 30, Last: 30, Count: 4}).Error)
	require.NoError(t, svc.rollup1h(now))
	hours := readRollup1h(t, svc.db)
	require.Len(t, hours, 1)
	require.Equal(t, 4, hours[0].Count)
	require.Equal(t, 15.0, hours[0].Avg)
}

func TestMetric_RollupUnchangedBucketsAvoidWrites(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	seedRawFixture(t, svc.db, base, []uint{1}, 400)
	now := base.Add(401 * metricBucket5m)
	require.NoError(t, svc.rollup5m(now))
	require.NoError(t, svc.rollup1h(now))
	db, counter := withCountingLogger(t, svc.db)
	svc = NewMetricService(db)
	require.NoError(t, svc.rollup5m(now.Add(metricBucket5m)))
	require.NoError(t, svc.rollup1h(now.Add(metricBucket5m)))
	// 两档扫描 + 分块预取；已存在且不变的桶不能每桶额外 UPDATE。
	require.LessOrEqual(t, counter.count, 12)
}

func TestMetric_RollupRefreshProtectsPartialRetentionBuckets(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	require.NoError(t, svc.db.Create(&[]model.MetricSampleRaw{
		{SeriesID: 1, TS: base.Add(3 * time.Minute), Value: fp(90)},
		{SeriesID: 1, TS: base.Add(4 * time.Minute), Value: fp(99)},
	}).Error)
	require.NoError(t, svc.db.Create(&model.MetricRollup5m{SeriesID: 1, BucketTS: base, Avg: 20, Min: 20, Max: 20, Last: 20, Count: 1}).Error)
	require.NoError(t, svc.rollup5m(base.Add(metricRawRetention+2*time.Minute)))
	five := readRollup5m(t, svc.db)
	require.Len(t, five, 1)
	require.Equal(t, 1, five[0].Count, "部分 raw 即使 count 更大也不能重写完整旧桶")
	require.Equal(t, 20.0, five[0].Avg)

	require.NoError(t, svc.db.Create(&model.MetricRollup5m{SeriesID: 2, BucketTS: base.Add(5 * time.Minute), Avg: 99, Min: 99, Max: 99, Last: 99, Count: 2}).Error)
	require.NoError(t, svc.db.Create(&model.MetricRollup1h{SeriesID: 2, BucketTS: base, Avg: 20, Min: 20, Max: 20, Last: 20, Count: 1}).Error)
	require.NoError(t, svc.rollup1h(base.Add(metric5mRetention+2*time.Minute)))
	var hour model.MetricRollup1h
	require.NoError(t, svc.db.Where("series_id = ?", 2).First(&hour).Error)
	require.Equal(t, 1, hour.Count)
	require.Equal(t, 20.0, hour.Avg)
}

func TestMetric_CPUWindowMissingWritesNullAfterValidSample(t *testing.T) {
	svc := newMetricSvc(t)
	base := metricBase()
	for i, req := range []*workerpb.HeartbeatRequest{
		{NodeUuid: "node-cpu", CpuUsage: 0.5, CpuUser: 0.5, CpuIdle: 0.5},
		{NodeUuid: "node-cpu", CpuIdle: 1},
		{NodeUuid: "node-cpu"},
	} {
		require.NoError(t, svc.ingestHeartbeatAt(req, base.Add(time.Duration(i)*30*time.Second)))
	}
	_, series, err := svc.QuerySeries(SeriesQuery{NodeUUID: "node-cpu", Scope: model.MetricScopeNode, From: base.Add(-time.Second), To: base.Add(time.Minute), Resolution: "raw"})
	require.NoError(t, err)
	for _, key := range []string{model.MetricNodeCPUPct, model.MetricNodeIOWait} {
		found := false
		for _, row := range series {
			if row.MetricKey != key {
				continue
			}
			found = true
			require.Len(t, row.Points, 3)
			require.NotNil(t, row.Points[0].Avg)
			require.NotNil(t, row.Points[1].Avg)
			require.Zero(t, *row.Points[1].Avg)
			require.Nil(t, row.Points[2].Avg, "缺测点不能跳过而沿用旧有效值")
		}
		require.True(t, found, key)
	}
}

// After migration, worker clocks/report ordering can put the previous owner's
// last snapshot after the current owner's. Ownership must constrain MAX, not
// merely discard the winner after it has masked the current valid snapshot.
func TestQueryProcessTop_CurrentOwnerSelectedBeforeLatest(t *testing.T) {
	svc := newMetricSvc(t)
	require.NoError(t, svc.db.AutoMigrate(&model.Node{}, &model.Instance{}))
	now := time.Now().UTC()
	oldNode := model.Node{UUID: "previous-owner", Name: "previous", Status: model.NodeStatusOnline, LastHeartbeat: &now}
	newNode := model.Node{UUID: "current-owner", Name: "current", Status: model.NodeStatusOnline, LastHeartbeat: &now}
	require.NoError(t, svc.db.Create(&oldNode).Error)
	require.NoError(t, svc.db.Create(&newNode).Error)
	inst := model.Instance{UUID: "migrated", Name: "migrated", NodeID: newNode.ID, Status: model.InstanceStatusRunning}
	require.NoError(t, svc.db.Create(&inst).Error)
	require.NoError(t, svc.db.Create(&[]model.ProcessMetricSnapshot{
		{NodeUUID: newNode.UUID, InstanceUUID: inst.UUID, PID: 200, CPUPercent: 20, SampledAt: now.Add(-30 * time.Second)},
		{NodeUUID: oldNode.UUID, InstanceUUID: inst.UUID, PID: 100, CPUPercent: 90, SampledAt: now.Add(-10 * time.Second)},
	}).Error)
	for _, tc := range []struct {
		name string
		q    ProcessTopQuery
		pid  int32
	}{
		{name: "platform current", q: ProcessTopQuery{Limit: 1}, pid: 200},
		{name: "instance current", q: ProcessTopQuery{InstanceUUID: inst.UUID, Limit: 1}, pid: 200},
		{name: "node current", q: ProcessTopQuery{NodeUUID: newNode.UUID, Limit: 1}, pid: 200},
		{name: "explicit history preserves original selection", q: ProcessTopQuery{InstanceUUID: inst.UUID, At: now, Limit: 1}, pid: 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := svc.QueryProcessTop(tc.q)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, tc.pid, rows[0].PID)
		})
	}
}
