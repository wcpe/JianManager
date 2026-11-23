package grpc_test

import (
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/proto/workerpb"
)

type ownershipOrphanObserver struct{ uuids []string }

func (o *ownershipOrphanObserver) ObserveHeartbeat(_ string, states []*workerpb.InstanceState) {
	for _, state := range states {
		o.uuids = append(o.uuids, state.InstanceUuid)
	}
}

// Exercise the authenticated handler and real storage/aggregation together. A fake
// MetricIngester would miss unknown UUIDs contributing players to the platform.
func TestHeartbeat_FormalMetricsRequireRegisteredOwner(t *testing.T) {
	for _, includeStates := range []bool{true, false} {
		name := "without_instance_states"
		if includeStates {
			name = "with_orphan_instance_state"
		}
		t.Run(name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ownership.db")), &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			require.NoError(t, db.AutoMigrate(
				&model.Node{}, &model.Instance{}, &model.MetricSeries{}, &model.MetricSampleRaw{},
				&model.MetricRollup5m{}, &model.MetricRollup1h{}, &model.ProcessMetricSnapshot{},
			))
			now := time.Now().UTC()
			nodeA := model.Node{UUID: "owner-a", Name: "A", Secret: "a-secret", Status: model.NodeStatusOnline, LastHeartbeat: &now}
			nodeB := model.Node{UUID: "owner-b", Name: "B", Secret: "b-secret", Status: model.NodeStatusOnline, LastHeartbeat: &now}
			require.NoError(t, db.Create(&nodeA).Error)
			require.NoError(t, db.Create(&nodeB).Error)
			owned := model.Instance{UUID: "owned-a", Name: "A instance", NodeID: nodeA.ID, Status: model.InstanceStatusStopped}
			foreign := model.Instance{UUID: "foreign-b", Name: "B instance", NodeID: nodeB.ID, Status: model.InstanceStatusStopped, WorkDirBytes: 55}
			require.NoError(t, db.Create(&owned).Error)
			require.NoError(t, db.Create(&foreign).Error)
			metrics := service.NewMetricService(db)
			legitimatePlayers := 11.0
			require.NoError(t, metrics.Ingest([]service.Sample{{
				NodeUUID: nodeB.UUID, InstanceID: foreign.UUID, Scope: model.MetricScopeInstance,
				MetricKey: model.MetricInstPlayersOnline, Unit: "count", TS: now.Add(-time.Second), Value: &legitimatePlayers,
			}}))
			h := cpgrpc.NewControlPlaneHandler(db, cpgrpc.NewClientPool())
			h.SetMetricIngester(metrics)
			orphans := &ownershipOrphanObserver{}
			h.SetOrphanRuntimeIngester(orphans)
			req := &workerpb.HeartbeatRequest{NodeUuid: nodeA.UUID, CpuUsage: 0.25}
			if includeStates {
				req.Instances = []*workerpb.InstanceState{
					{InstanceUuid: owned.UUID, State: "RUNNING", Health: "healthy"},
					{InstanceUuid: foreign.UUID, State: "RUNNING", StatusReason: "untrusted"},
					{InstanceUuid: "unknown-runtime", State: "RUNNING", Pid: 303},
				}
			}
			for i, uuid := range []string{owned.UUID, foreign.UUID, "unknown-runtime"} {
				players := int32(1000)
				if uuid == owned.UUID {
					players = 7
				}
				req.InstanceMetrics = append(req.InstanceMetrics, &workerpb.InstanceMetricSample{
					InstanceUuid: uuid, ProbeAvailable: true, Tps: 20, PlayersOnline: players,
					PlayersOnlineAvailable: true,
					Worlds:                 []*workerpb.WorldMetric{{Name: "world", LoadedChunks: 10}},
				})
				req.ProcessMetrics = append(req.ProcessMetrics, &workerpb.ProcessMetricSample{
					InstanceUuid: uuid, Pid: int32(101 + i), CpuPercent: 10, SampledAtUnixMs: now.UnixMilli(),
				})
				req.WorkDirUsages = append(req.WorkDirUsages, &workerpb.InstanceWorkDirUsage{
					InstanceUuid: uuid, Available: true, WorkDirBytes: 123,
				})
			}
			stream := newHeartbeatTestStream(ctxWithHeartbeatSecret(nodeA.Secret), req)
			require.True(t, errors.Is(h.Heartbeat(stream), io.EOF))
			require.Len(t, stream.sent, 1, "legitimate node heartbeat still receives its response")

			// The actual homepage aggregate must include A's 7 and B's legitimate 11,
			// never either of A's forged 1000-player readings.
			overview, err := metrics.Overview(now.Add(-time.Minute), time.Now().UTC().Add(time.Second), "raw")
			require.NoError(t, err)
			require.Equal(t, int64(18), overview.Totals.OnlinePlayers, "unknown/foreign metrics must not inflate platform players")
			var forbiddenSeries int64
			require.NoError(t, db.Model(&model.MetricSeries{}).
				Where("node_uuid = ? AND instance_id IN ?", nodeA.UUID, []string{foreign.UUID, "unknown-runtime"}).Count(&forbiddenSeries).Error)
			require.Zero(t, forbiddenSeries, "neither instance nor world series may be created for unknown/foreign targets")
			var processRows []model.ProcessMetricSnapshot
			require.NoError(t, db.Find(&processRows).Error)
			require.Len(t, processRows, 1)
			require.Equal(t, owned.UUID, processRows[0].InstanceUUID)
			require.Len(t, req.InstanceMetrics, 1)
			require.Equal(t, owned.UUID, req.InstanceMetrics[0].InstanceUuid)
			require.Len(t, req.WorkDirUsages, 1)
			require.Equal(t, owned.UUID, req.WorkDirUsages[0].InstanceUuid)

			require.NoError(t, db.First(&foreign, foreign.ID).Error)
			require.Equal(t, model.InstanceStatusStopped, foreign.Status)
			require.Empty(t, foreign.StatusReason)
			require.Equal(t, int64(55), foreign.WorkDirBytes)
			require.NoError(t, db.First(&owned, owned.ID).Error)
			require.Equal(t, int64(123), owned.WorkDirBytes, "owned workdir observation must still update")
			if includeStates {
				require.ElementsMatch(t, []string{owned.UUID, "unknown-runtime"}, orphans.uuids,
					"unknown state remains available to orphan reconciliation, foreign state does not")
				require.Equal(t, model.InstanceStatusRunning, owned.Status)
			}
			var unknownInstances int64
			require.NoError(t, db.Model(&model.Instance{}).Where("uuid = ?", "unknown-runtime").Count(&unknownInstances).Error)
			require.Zero(t, unknownInstances, "orphan observation does not register a formal instance")
		})
	}
}

// An ownership lookup error must not turn the report into an authoritative empty
// instance list. The real handler must keep running state and only ingest node data.
func TestHeartbeat_OwnershipLookupErrorSkipsInstanceSideEffects(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "lookup-error.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(
		&model.Node{}, &model.Instance{}, &model.MetricSeries{}, &model.MetricSampleRaw{},
		&model.MetricRollup5m{}, &model.MetricRollup1h{}, &model.ProcessMetricSnapshot{},
	))
	now := time.Now().UTC()
	node := model.Node{UUID: "lookup-owner", Name: "owner", Secret: "node-secret", LastHeartbeat: &now}
	require.NoError(t, db.Create(&node).Error)
	inst := model.Instance{UUID: "lookup-instance", Name: "instance", NodeID: node.ID, Status: model.InstanceStatusRunning, WorkDirBytes: 55}
	require.NoError(t, db.Create(&inst).Error)
	lookupFailed := false
	require.NoError(t, db.Callback().Row().Before("gorm:row").Register("test:ownership-lookup-error", func(tx *gorm.DB) {
		if tx.Statement.Table == "instances" && len(tx.Statement.Selects) == 2 && tx.Statement.Selects[1] == "node_id" {
			lookupFailed = true
			tx.AddError(errors.New("test ownership lookup failure"))
		}
	}))
	metrics := service.NewMetricService(db)
	h := cpgrpc.NewControlPlaneHandler(db, cpgrpc.NewClientPool())
	h.SetMetricIngester(metrics)
	orphans := &ownershipOrphanObserver{}
	h.SetOrphanRuntimeIngester(orphans)
	req := &workerpb.HeartbeatRequest{
		NodeUuid: node.UUID, CpuUsage: 0.25,
		Instances:       []*workerpb.InstanceState{{InstanceUuid: inst.UUID, State: "STOPPED", StatusReason: "untrusted"}},
		InstanceMetrics: []*workerpb.InstanceMetricSample{{InstanceUuid: inst.UUID, PlayersOnline: 1000, PlayersOnlineAvailable: true}},
		ProcessMetrics:  []*workerpb.ProcessMetricSample{{InstanceUuid: inst.UUID, Pid: 101}},
		WorkDirUsages:   []*workerpb.InstanceWorkDirUsage{{InstanceUuid: inst.UUID, Available: true, WorkDirBytes: 999}},
	}
	stream := newHeartbeatTestStream(ctxWithHeartbeatSecret(node.Secret), req)
	require.True(t, errors.Is(h.Heartbeat(stream), io.EOF))
	require.True(t, lookupFailed, "the test must actually exercise an ownership query failure")
	require.Len(t, stream.sent, 1)
	require.Empty(t, orphans.uuids, "failed lookup must skip orphan reconciliation too")
	require.NoError(t, db.First(&inst, inst.ID).Error)
	require.Equal(t, model.InstanceStatusRunning, inst.Status)
	require.Empty(t, inst.StatusReason)
	require.Equal(t, int64(55), inst.WorkDirBytes)
	var instanceSeries, nodeSeries, processRows int64
	require.NoError(t, db.Model(&model.MetricSeries{}).Where("instance_id <> ''").Count(&instanceSeries).Error)
	require.NoError(t, db.Model(&model.MetricSeries{}).Where("scope = ?", model.MetricScopeNode).Count(&nodeSeries).Error)
	require.NoError(t, db.Model(&model.ProcessMetricSnapshot{}).Count(&processRows).Error)
	require.Zero(t, instanceSeries)
	require.Zero(t, processRows)
	require.Positive(t, nodeSeries, "node metrics and heartbeat still work when instance ownership cannot be checked")
}
