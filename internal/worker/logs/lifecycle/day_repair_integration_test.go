package lifecycle

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

func catalogFreePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	return port
}
func catalogWaitHealthy(t *testing.T, sup *vlsup.Supervisor, ns vlsup.Namespace) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for {
		if err := sup.Health(ctx, ns); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func TestRealVLExistingColdDayResidualPinnedAcrossOwnerSwitch(t *testing.T) {
	binary := os.Getenv("JM_VL_BIN")
	if binary == "" {
		t.Skip("未提供本地批准版本二进制")
	}
	require.NoError(t, vlsup.VerifyAsset(binary, vlsup.CurrentApprovedExeSHA256()))
	root := t.TempDir()
	if base := os.Getenv("JM_CATALOG_REAL_ROOT"); base != "" {
		var err error
		root, err = os.MkdirTemp(base, "real-")
		require.NoError(t, err)
	}
	t.Logf("批准资产=%s 数据根=%s", vlsup.CurrentApprovedExeSHA256(), root)
	hotPort, coldPort := catalogFreePort(t), catalogFreePort(t)
	sup, err := vlsup.New(vlsup.Options{BinaryPath: binary, AssetSHA256: vlsup.CurrentApprovedExeSHA256(), DataRoot: root, RetentionPeriod: "730d", AuthUsername: "catalog-local", AuthPassword: "isolated-only", Ports: map[vlsup.Namespace]int{vlsup.NamespaceHot: hotPort, vlsup.NamespaceCold: coldPort}, MemoryAllowedBytes: map[vlsup.Namespace]int64{vlsup.NamespaceHot: 64 << 20, vlsup.NamespaceCold: 64 << 20}})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for _, ns := range []vlsup.Namespace{vlsup.NamespaceHot, vlsup.NamespaceCold} {
		require.NoError(t, sup.Start(ctx, ns))
		catalogWaitHealthy(t, sup, ns)
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		sup.StopAll(stop)
	})
	hot, err := sup.ClientFor(vlsup.NamespaceHot)
	require.NoError(t, err)
	cold, err := sup.ClientFor(vlsup.NamespaceCold)
	require.NoError(t, err)
	dayStart := time.Now().UTC().Truncate(24 * time.Hour).Add(-48 * time.Hour)
	day := dayStart.Format("2006-01-02")
	dir := dayStart.Format("20060102")
	build := func(source string, index uint64, msg string) logtypes.Event {
		event := logtypes.BuildEvent(logtypes.SourceIdentity{LogSourceID: source, SourceGeneration: "g1", ParserVersion: "parser/v1"}, logtypes.RecordRange{Start: index * 10, End: (index + 1) * 10}, dayStart.Add(12*time.Hour+time.Duration(index)*time.Millisecond).Format(time.RFC3339Nano), dayStart.Add(13*time.Hour).Format(time.RFC3339Nano), "INFO", "stdout", msg)
		event.Fields = map[string]string{"app": "catalog-isolated", "ordinal": strconv.FormatUint(index, 10)}
		return event
	}
	old := []logtypes.Event{build("settled-source", 0, "不得覆盖的冷层原文")}
	residual := []logtypes.Event{build("residual-source", 0, "热层残留一"), build("residual-source", 1, "热层残留二")}
	insert := func(client *vlsup.Client, events []logtypes.Event, generation string) {
		var b strings.Builder
		for _, event := range events {
			data, err := json.Marshal(repairPayload(event, generation))
			require.NoError(t, err)
			b.Write(data)
			b.WriteByte('\n')
		}
		_, err := client.InsertJSONLines(ctx, []byte(b.String()))
		require.NoError(t, err)
		_, err = client.Post(ctx, "/internal/force_flush", nil)
		require.NoError(t, err)
	}
	insert(cold, old, "cold-original")
	insert(hot, residual, "hot-original")
	// 模拟旧版本已卸载整个日分区、但仍遗留 HOT 责任记录的脏环境。
	_, err = hot.Post(ctx, "/internal/partition/detach", url.Values{"name": {dir}})
	require.NoError(t, err)
	path := filepath.Join(root, "catalog.jsonl")
	open := func() *catalog.Catalog {
		store, err := catalog.NewJSONLFileStore(path)
		require.NoError(t, err)
		journal, err := catalog.NewJournalWithStore(store)
		require.NoError(t, err)
		c := catalog.New(journal)
		c.StartingRecover()
		return c
	}
	cat := open()
	oldKey := catalog.PartitionKey{StorageNamespace: "settled", UTCDay: day}
	hotKey := catalog.PartitionKey{StorageNamespace: "residual", UTCDay: day}
	record := func(key catalog.PartitionKey, owner catalog.Owner, events []logtypes.Event, generation, dir string) *catalog.Record {
		r := catalog.NewStableRecord(key, owner, 1, dir)
		r.PublishedProjection = &catalog.PublishedProjection{ManifestVersion: generation, QueryLocationDirID: dir, QueryGeneration: 1, ProjectionGeneration: generation, ProjectionGenerations: []string{generation}, CoverageComplete: true, SourceProjections: []catalog.SourceProjection{{SourceGenerationRef: catalog.SourceGenerationRef{LogSourceID: events[0].Source.LogSourceID, SourceGeneration: "g1"}, ProjectionGenerations: []string{generation}, ClosedVisibleSeq: events[len(events)-1].Record.End}}}
		return r
	}
	settledRecord := record(oldKey, catalog.OwnerCold, old, "cold-original", dir)
	require.NoError(t, cat.AppendJournal(catalog.JournalEntry{Key: oldKey, Record: settledRecord}))
	require.NoError(t, cat.AppendJournal(catalog.JournalEntry{Key: hotKey, Record: record(hotKey, catalog.OwnerHot, residual, "hot-original", "hot-dir")}))
	physical, err := NewVLPartitionOps(hot, cold, filepath.Join(root, "hot", "data"), filepath.Join(root, "cold", "data"), cat)
	require.NoError(t, err)
	budget, err := vlsup.NewBudgetController(vlsup.RuntimeBudget{DiskBytes: 1 << 30, RSSBytes: 1 << 30, Reservations: map[vlsup.ReservationKind]int64{vlsup.ReservationStaging: 1 << 28}})
	require.NoError(t, err)
	budget.Update(sup.SampleResources(os.Getpid()))
	physical.ResourceBudget = budget
	manager := NewDayManager(cat, physical.Ops(), physical)
	oldViewID, err := cat.NewQueryViewID(time.Now().UTC())
	require.NoError(t, err)
	_, err = cat.SnapshotAndLease(catalog.QuerySnapshotRequest{ViewID: oldViewID, Keys: []catalog.PartitionKey{hotKey}, TTL: time.Minute})
	require.NoError(t, err)
	require.Error(t, manager.Start(day, catalog.OwnerCold, dir), "COLD同日已存在但缺canonical输入时必须保留原物并明确失败")
	require.DirExists(t, filepath.Join(root, "hot", "data", "partitions", dir))
	unchanged, _ := cat.Get(oldKey)
	require.Equal(t, settledRecord.PublishedProjection, unchanged.PublishedProjection)
	physical.CanonicalDayInput = func(member *catalog.Record) ([]logtypes.Event, error) {
		require.Equal(t, hotKey, member.Key)
		return residual, nil
	}
	entered, finish := make(chan struct{}), make(chan struct{})
	var finishOnce sync.Once
	finishRead := func() { finishOnce.Do(func() { close(finish) }) }
	t.Cleanup(finishRead)
	queryResult := make(chan error, 1)
	go func() {
		release, err := cat.PinLeasedRead(oldViewID, time.Now().UTC())
		if err != nil {
			queryResult <- err
			return
		}
		err = hot.Stream(ctx, "/select/logsql/query", url.Values{"query": {"projection_generation:=\"hot-original\""}, "start": {dayStart.Format(time.RFC3339Nano)}, "end": {dayStart.Add(24 * time.Hour).Format(time.RFC3339Nano)}}, func(body io.Reader) error {
			close(entered)
			select {
			case <-finish:
			case <-ctx.Done():
				return ctx.Err()
			}
			data, err := io.ReadAll(body)
			if err == nil && !strings.Contains(string(data), residual[0].EventID) {
				return io.ErrUnexpectedEOF
			}
			return err
		})
		release()
		queryResult <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.ErrorIs(t, manager.Resume(day), catalog.ErrPhysicalDayNotRetirable)
	switched, _ := cat.Get(hotKey)
	require.Equal(t, catalog.OwnerCold, switched.Owner)
	require.Equal(t, catalog.StateQueryLeaseDraining, switched.MigrationState)
	newViewID, err := cat.NewQueryViewID(time.Now().UTC())
	require.NoError(t, err)
	snapshots, err := cat.SnapshotAndLease(catalog.QuerySnapshotRequest{ViewID: newViewID, Keys: []catalog.PartitionKey{hotKey}, TTL: time.Minute})
	require.NoError(t, err)
	require.Equal(t, catalog.OwnerCold, snapshots[0].Owner)
	require.NoError(t, cat.ReleaseQueryView(oldViewID))
	require.NoError(t, cat.PruneQueryLeases(time.Now().Add(2*time.Minute)))
	ok, _ := cat.CanRetirePhysicalDay(day)
	require.False(t, ok, "真实HTTP读取未结束时release/TTL不能拆HOT")
	require.DirExists(t, filepath.Join(root, "hot", "data", "partitions", dir))
	checkOld := func() {
		scope := settledRecord.PublishedProjection.SourceProjections[0]
		seen, err := physical.readRepairRows(ctx, cold, day, repairSelector(scope, scope.ProjectionGenerations, true), "cold-original", old)
		require.NoError(t, err)
		require.Len(t, seen, 1)
	}
	checkOld()
	finishRead()
	require.NoError(t, <-queryResult)
	require.NoError(t, manager.Resume(day))
	require.NoDirExists(t, filepath.Join(root, "hot", "data", "partitions", dir))
	checkOld()
	proof, _ := cat.PhysicalDay(day)
	require.Len(t, proof.Batches, 1)
	require.True(t, proof.Batches[0].LogicalRepair)
	require.True(t, proof.Batches[0].Reattached, "仅固定Catalog残留批次可受控重新挂载")
	require.Len(t, proof.Batches[0].RepairSources, 1)
	require.True(t, proof.Batches[0].RepairSources[0].Verified)
	generation := proof.Batches[0].RepairSources[0].ProjectionGeneration
	t.Logf("物理日=%s 残留成员=%s 固定代次=%s 逐字段验证事件=%d", day, hotKey, generation, len(residual))
	for round := 1; round <= 3; round++ {
		for _, ns := range []vlsup.Namespace{vlsup.NamespaceHot, vlsup.NamespaceCold} {
			require.NoError(t, sup.Stop(ctx, ns))
		}
		cat = open()
		physical.Catalog = cat
		for _, ns := range []vlsup.Namespace{vlsup.NamespaceHot, vlsup.NamespaceCold} {
			require.NoError(t, sup.Start(ctx, ns))
			catalogWaitHealthy(t, sup, ns)
		}
		require.NoError(t, NewDayManager(cat, physical.Ops(), physical).Resume(day))
		restored, _ := cat.Get(hotKey)
		require.Equal(t, catalog.OwnerCold, restored.Owner)
		require.Equal(t, catalog.StateCleaned, restored.MigrationState)
		require.True(t, restored.JournalComplete)
		require.Equal(t, switched.PublishedProjection, restored.PublishedProjection)
		unchanged, _ := cat.Get(oldKey)
		require.Equal(t, settledRecord.PublishedProjection, unchanged.PublishedProjection)
		checkOld()
		source := restored.PublishedProjection.SourceProjections[0]
		seen, err := physical.readRepairRows(ctx, cold, day, repairSelector(source, source.ProjectionGenerations, true), generation, residual)
		require.NoError(t, err)
		require.Len(t, seen, len(residual))
		_, err = cat.PinLeasedRead(oldViewID, time.Now().UTC())
		require.ErrorIs(t, err, catalog.ErrQueryViewStale)
		t.Logf("第%d次完整停启：owner=%s generation=%d manifest=%s sources=%v", round, restored.Owner, restored.Generation, restored.PublishedProjection.ManifestVersion, restored.PublishedProjection.SourceProjections)
	}
}
