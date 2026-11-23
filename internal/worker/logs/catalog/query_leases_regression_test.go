package catalog

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func leasedFixture(t *testing.T) *Catalog {
	t.Helper()
	c := New(nil)
	key := PartitionKey{StorageNamespace: "ns", UTCDay: "2026-09-20"}
	r := NewStableRecord(key, OwnerHot, 1, "hot")
	r.PublishedProjection = &PublishedProjection{ManifestVersion: "m1", QueryLocationDirID: "hot", QueryGeneration: 1, CoverageComplete: true, SourceProjections: []SourceProjection{{SourceGenerationRef: SourceGenerationRef{LogSourceID: "source", SourceGeneration: "g1"}, ProjectionGenerations: []string{"p1"}, ClosedVisibleSeq: 10}}}
	require.NoError(t, c.Put(r))
	return c
}

func TestQueryLeaseDeltaDoesNotInvalidateAuthorityCAS(t *testing.T) {
	c := leasedFixture(t)
	viewID, err := c.NewQueryViewID(time.Now().UTC())
	require.NoError(t, err)
	key := c.Keys()[0]
	expected, _ := c.Get(key)
	next := expected.Clone()
	next.PublishedProjection.ManifestVersion = "m2"
	next.PublishedProjection.SourceProjections[0].ClosedVisibleSeq = 20
	snaps, err := c.SnapshotAndLease(QuerySnapshotRequest{ViewID: viewID, Keys: []PartitionKey{key}, TTL: time.Minute})
	require.NoError(t, err)
	entries, loadErr := c.Journal().(QueryLeaseJournal).LeaseDeltaSnapshot()
	require.NoError(t, loadErr)
	require.Empty(t, c.Journal().Snapshot(), "读租约不能写入永久权威历史")
	require.Nil(t, entries[len(entries)-1].Snapshots[0].Projection, "租约日志只存读取位置与期限，不按查询复制整份历史manifest")
	snaps[0].Projection.SourceProjections[0].ProjectionGenerations[0] = "mutated"
	require.NoError(t, c.PublishProjection(expected, next), "租约登记不能使已验证投影 CAS 失效")
	current, _ := c.Get(key)
	require.Len(t, current.QueryLeases, 1)
	require.Equal(t, "m2", current.PublishedProjection.ManifestVersion)
	same, err := c.SnapshotAndLease(QuerySnapshotRequest{ViewID: viewID, Keys: []PartitionKey{key}, TTL: time.Minute})
	require.NoError(t, err)
	require.Equal(t, "p1", same[0].Projection.SourceProjections[0].ProjectionGenerations[0])
	require.EqualValues(t, 10, same[0].Projection.SourceProjections[0].ClosedVisibleSeq)
	release, err := c.PinLeasedRead(viewID, time.Now())
	require.NoError(t, err)
	release()
	require.NoError(t, c.ReleaseQueryView(viewID))
	require.Empty(t, c.views, "无pin失效view必须释放完整快照")
	_, err = c.SnapshotAndLease(QuerySnapshotRequest{ViewID: viewID, Keys: []PartitionKey{key}, TTL: time.Minute})
	require.ErrorIs(t, err, ErrQueryViewStale, "一次性票据消费后不能重开旧view")
}

func TestQueryLeaseReleaseRevokesPendingCreationTicket(t *testing.T) {
	c := leasedFixture(t)
	key := c.Keys()[0]
	now := time.Now().UTC()
	viewID, err := c.NewQueryViewID(now)
	require.NoError(t, err)
	before, err := c.Journal().(QueryLeaseJournal).LeaseDeltaSnapshot()
	require.NoError(t, err)

	require.NoError(t, c.ReleaseQueryView(viewID))
	require.NoError(t, c.ReleaseQueryView(viewID), "取消未消费票据必须幂等")
	_, err = c.SnapshotAndLease(QuerySnapshotRequest{ViewID: viewID, Keys: []PartitionKey{key}, Now: now, TTL: time.Minute})
	require.ErrorIs(t, err, ErrQueryViewStale, "取消后的票据不能创建有完整投影的旧view")
	require.Empty(t, c.pendingViews)
	require.Empty(t, c.views)
	after, err := c.Journal().(QueryLeaseJournal).LeaseDeltaSnapshot()
	require.NoError(t, err)
	require.Equal(t, before, after, "没有物理引用的票据取消无需制造持久租约历史")

	nextID, err := c.NewQueryViewID(now)
	require.NoError(t, err)
	_, err = c.SnapshotAndLease(QuerySnapshotRequest{ViewID: nextID, Keys: []PartitionKey{key}, Now: now, TTL: time.Minute})
	require.NoError(t, err, "释放容量后仍应准入新的真实票据")
}

func TestQueryReadPinSurvivesViewReleaseAndExpiry(t *testing.T) {
	c := leasedFixture(t)
	key := c.Keys()[0]
	now := time.Now().UTC()
	viewID, err := c.NewQueryViewID(now)
	require.NoError(t, err)
	_, err = c.SnapshotAndLease(QuerySnapshotRequest{ViewID: viewID, Keys: []PartitionKey{key}, Now: now, TTL: time.Second})
	require.NoError(t, err)
	release, err := c.PinLeasedRead(viewID, now)
	require.NoError(t, err)
	_, err = c.BeginDayMigration(key.UTCDay, OwnerCold, "cold")
	require.NoError(t, err)
	for _, state := range []MigrationState{StateDraining, StateSnapshotting, StateStagingVerify} {
		_, err = c.AdvanceDay(key.UTCDay, state)
		require.NoError(t, err)
	}
	require.NoError(t, c.RecordDayVerification(key.UTCDay, "proof"))
	_, err = c.AdvanceDay(key.UTCDay, StateAttachedStaging)
	require.NoError(t, err)
	current, _ := c.Get(key)
	projection := current.Clone().PublishedProjection
	projection.QueryGeneration = 2
	projection.QueryLocationDirID = "cold"
	_, err = c.SwitchDayOwners(key.UTCDay, OwnerCold, "cold", map[PartitionKey]*PublishedProjection{key: projection})
	require.NoError(t, err)
	post, err := c.PinLeasedRead(viewID, now)
	require.NoError(t, err, "纯迁移不得改读旧HOT引用")
	post()
	newID, err := c.NewQueryViewID(now)
	require.NoError(t, err)
	newer, err := c.SnapshotAndLease(QuerySnapshotRequest{ViewID: newID, Keys: []PartitionKey{key}, Now: now, TTL: time.Second})
	require.NoError(t, err)
	require.Equal(t, OwnerCold, newer[0].Owner)
	require.NoError(t, c.PruneQueryLeases(now.Add(2*time.Second)))
	_, err = c.PinLeasedRead(viewID, now.Add(2*time.Second))
	require.ErrorIs(t, err, ErrQueryViewStale)
	require.NoError(t, c.ReleaseQueryView(viewID))
	ok, _ := c.CanRetirePhysicalDay(key.UTCDay)
	require.False(t, ok, "TTL和释放view不能提前释放正在读取的goroutine")
	release()
	release()
	ok, reason := c.CanRetirePhysicalDay(key.UTCDay)
	require.True(t, ok, reason)
}

func TestQueryLeaseEmptyAndRestartedViewsDoNotRevive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.jsonl")
	open := func() *Catalog {
		store, err := NewJSONLFileStore(path)
		require.NoError(t, err)
		j, err := NewJournalWithStore(store)
		require.NoError(t, err)
		return New(j)
	}
	c := open()
	now := time.Now().UTC()
	viewID, err := c.NewQueryViewID(now)
	require.NoError(t, err)
	_, err = c.SnapshotAndLease(QuerySnapshotRequest{ViewID: viewID, Now: now, TTL: time.Second})
	require.NoError(t, err)
	_, err = c.PinLeasedRead(viewID, now.Add(2*time.Second))
	require.ErrorIs(t, err, ErrQueryViewStale)
	for round := 0; round < 3; round++ {
		c = open()
		_, err = c.SnapshotAndLease(QuerySnapshotRequest{ViewID: viewID, Now: now, TTL: time.Second})
		require.ErrorIs(t, err, ErrQueryViewStale, "重启孤儿view不得复活")
	}
}

func TestQueryLeaseFileJournalPublishReleaseCompactPreservesCASAndPinAcrossThreeStarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authority.jsonl")
	open := func() *Catalog {
		store, err := NewJSONLFileStore(path)
		require.NoError(t, err)
		journal, err := NewJournalWithStore(store)
		require.NoError(t, err)
		c := New(journal)
		c.StartingRecover()
		return c
	}
	fixture := leasedFixture(t)
	key := fixture.Keys()[0]
	record, _ := fixture.Get(key)
	c := open()
	require.NoError(t, c.AppendJournal(JournalEntry{Key: key, Record: record}))
	now := time.Now().UTC()
	anchorID, err := c.NewQueryViewID(now)
	require.NoError(t, err)
	_, err = c.SnapshotAndLease(QuerySnapshotRequest{ViewID: anchorID, Keys: []PartitionKey{key}, Now: now, TTL: time.Minute})
	require.NoError(t, err)
	unpin, err := c.PinLeasedRead(anchorID, now)
	require.NoError(t, err)
	defer unpin()

	const rounds = 260
	for i := 0; i < rounds; i++ {
		expected, _ := c.Get(key)
		beforeSeq := c.Journal().LastSeq()
		viewID, err := c.NewQueryViewID(now)
		require.NoError(t, err)
		fixed, err := c.SnapshotAndLease(QuerySnapshotRequest{ViewID: viewID, Keys: []PartitionKey{key}, Now: now, TTL: time.Minute})
		require.NoError(t, err)
		require.Equal(t, beforeSeq, c.Journal().LastSeq(), "租约准入不能推进权威序号")
		next := expected.Clone()
		next.PublishedProjection.ManifestVersion = fmt.Sprintf("append-%d", i)
		next.PublishedProjection.SourceProjections[0].ClosedVisibleSeq++
		require.NoError(t, c.PublishProjection(expected, next), "第%d轮真实发布CAS不应被租约阻断", i)
		require.Equal(t, beforeSeq+1, c.Journal().LastSeq())
		unchanged, err := c.SnapshotAndLease(QuerySnapshotRequest{ViewID: viewID, Keys: []PartitionKey{key}, Now: now, TTL: time.Minute})
		require.NoError(t, err)
		require.Equal(t, fixed, unchanged, "发布新前缀不能改写旧view完整投影")
		require.NoError(t, c.ReleaseQueryView(viewID))
		require.Len(t, c.views, 1, "每轮释放后只保留仍在读取的anchor")
		current, _ := c.Get(key)
		require.Len(t, current.QueryLeases, 1)
		require.Equal(t, anchorID, current.QueryLeases[0].ViewID)
	}
	leaseEntries, err := c.Journal().(QueryLeaseJournal).LeaseDeltaSnapshot()
	require.NoError(t, err)
	require.Less(t, len(leaseEntries), rounds, "自动compact不能永久积累已释放view")
	for _, entry := range leaseEntries {
		for _, snapshot := range entry.Snapshots {
			require.Nil(t, snapshot.Projection, "sidecar不复制完整PP")
		}
	}
	anchor, err := c.SnapshotAndLease(QuerySnapshotRequest{ViewID: anchorID, Keys: []PartitionKey{key}, Now: now, TTL: time.Minute})
	require.NoError(t, err)
	require.EqualValues(t, 10, anchor[0].Projection.SourceProjections[0].ClosedVisibleSeq)
	current, _ := c.Get(key)
	require.NoError(t, c.AppendJournal(JournalEntry{Key: key, Record: current, Records: []*Record{current}}), "外部追加也必须剥离派生租约")
	_, err = c.BeginDayMigration(key.UTCDay, OwnerCold, "cold")
	require.NoError(t, err)
	for _, state := range []MigrationState{StateDraining, StateSnapshotting, StateStagingVerify} {
		_, err = c.AdvanceDay(key.UTCDay, state)
		require.NoError(t, err)
	}
	require.NoError(t, c.RecordDayVerification(key.UTCDay, "verified-compact"))
	_, err = c.AdvanceDay(key.UTCDay, StateAttachedStaging)
	require.NoError(t, err)
	current, _ = c.Get(key)
	projection := current.Clone().PublishedProjection
	projection.QueryGeneration = 2
	projection.QueryLocationDirID = "cold"
	_, err = c.SwitchDayOwners(key.UTCDay, OwnerCold, "cold", map[PartitionKey]*PublishedProjection{key: projection})
	require.NoError(t, err)
	require.NoError(t, c.ReleaseQueryView(anchorID))
	canRetire, _ := c.CanRetirePhysicalDay(key.UTCDay)
	require.False(t, canRetire, "compact和权威批提交不能丢失仍在读取的旧pin")
	unpin()
	require.Empty(t, c.views)
	canRetire, reason := c.CanRetirePhysicalDay(key.UTCDay)
	require.True(t, canRetire, reason)
	require.NoError(t, c.CompletePhysicalDay(key.UTCDay))
	want, _ := c.Get(key)
	checkAuthority := func() {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.False(t, bytes.Contains(data, []byte(`"query_leases"`)), "Record/Records/固定日Members均不得永久嵌入派生租约")
		require.False(t, bytes.Contains(data, []byte(`"lease_delta"`)))
	}
	checkAuthority()
	for round := 1; round <= 3; round++ {
		c = open()
		restored, _ := c.Get(key)
		require.Equal(t, want, restored, "第%d启权威字段必须逐字段保持", round)
		require.Empty(t, restored.QueryLeases)
		_, err = c.SnapshotAndLease(QuerySnapshotRequest{ViewID: anchorID, Keys: []PartitionKey{key}, Now: now, TTL: time.Minute})
		require.ErrorIs(t, err, ErrQueryViewStale)
		checkAuthority()
	}
	t.Logf("正式发布=%d 自动compact后sidecar项=%d 权威LastSeq=%d FileJournal脏环境停启=3", rounds, len(leaseEntries), c.Journal().LastSeq())
}

type unknownLeaseAppendJournal struct {
	*JournalWithStore
	unknownAppend bool
	failCompact   bool
	appendCalls   int
	compactCalls  int
}

func (j *unknownLeaseAppendJournal) AppendLeaseDelta(delta LeaseDelta) error {
	j.appendCalls++
	if err := j.JournalWithStore.AppendLeaseDelta(delta); err != nil {
		return err
	}
	if j.unknownAppend {
		return errors.New("真实租约已落盘但响应失败")
	}
	return nil
}

func (j *unknownLeaseAppendJournal) CompactLeaseDeltas(entries []LeaseDelta) error {
	j.compactCalls++
	if j.failCompact {
		return errors.New("租约压缩持续失败")
	}
	return j.JournalWithStore.CompactLeaseDeltas(entries)
}

func TestQueryLeaseUnknownAppendRequiresBoundedCompactionBeforeRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unknown.jsonl")
	store, err := NewJSONLFileStore(path)
	require.NoError(t, err)
	journal, err := NewJournalWithStore(store)
	require.NoError(t, err)
	faults := &unknownLeaseAppendJournal{JournalWithStore: journal}
	c := New(faults)
	fixture := leasedFixture(t)
	key := fixture.Keys()[0]
	record, _ := fixture.Get(key)
	require.NoError(t, c.AppendJournal(JournalEntry{Key: key, Record: record}))
	now := time.Now().UTC()
	anchorID, err := c.NewQueryViewID(now)
	require.NoError(t, err)
	_, err = c.SnapshotAndLease(QuerySnapshotRequest{ViewID: anchorID, Keys: []PartitionKey{key}, Now: now, TTL: time.Minute})
	require.NoError(t, err)
	unpin, err := c.PinLeasedRead(anchorID, now)
	require.NoError(t, err)
	defer unpin()
	beforeSeq := c.Journal().LastSeq()
	faults.unknownAppend = true
	const attempts = 32
	for i := 0; i < attempts; i++ {
		viewID, err := c.NewQueryViewID(now)
		require.NoError(t, err)
		_, err = c.SnapshotAndLease(QuerySnapshotRequest{ViewID: viewID, Keys: []PartitionKey{key}, Now: now, TTL: time.Minute})
		require.ErrorIs(t, err, ErrQueryAdmission)
		require.NoError(t, c.ReleaseQueryView(viewID), "未知写失败后取消票据不应留下active占位")
	}
	data, err := os.ReadFile(path + ".leases")
	require.NoError(t, err)
	require.LessOrEqual(t, bytes.Count(data, []byte{'\n'}), 3, "持续未知写响应也不能使sidecar绕过压缩界限")
	require.GreaterOrEqual(t, faults.compactCalls, attempts, "每个不确定append之后必须先压缩再尝试下一次写入")
	require.Equal(t, beforeSeq, c.Journal().LastSeq())
	require.Empty(t, c.pendingViews)
	require.Len(t, c.views, 1)
	require.EqualValues(t, 1, c.views[anchorID].pins)

	faults.failCompact = true
	appendCalls := faults.appendCalls
	for i := 0; i < 4; i++ {
		_, err = c.NewQueryViewID(now)
		require.ErrorIs(t, err, ErrQueryAdmission, "压缩不可用时不能继续append未知结果")
	}
	require.Equal(t, appendCalls, faults.appendCalls)
	after, err := os.ReadFile(path + ".leases")
	require.NoError(t, err)
	require.Equal(t, data, after)
	expected, _ := c.Get(key)
	next := expected.Clone()
	next.PublishedProjection.SourceProjections[0].ClosedVisibleSeq++
	require.NoError(t, c.PublishProjection(expected, next), "租约错误路径不能污染已验证权威CAS")
	require.Equal(t, beforeSeq+1, c.Journal().LastSeq())
	require.ErrorIs(t, c.ReleaseQueryView(anchorID), ErrQueryAdmission)
	require.Empty(t, c.views[anchorID].invalid, "持久释放失败必须保留旧pin责任")
	require.EqualValues(t, 10, c.views[anchorID].snapshots[0].Projection.SourceProjections[0].ClosedVisibleSeq)

	faults.unknownAppend = false
	faults.failCompact = false
	require.NoError(t, c.ReleaseQueryView(anchorID), "底层恢复后可以压缩并提交释放")
	require.Len(t, c.views, 1, "释放成功也必须等真实pin退出")
	unpin()
	require.Empty(t, c.views)
	require.Empty(t, c.leaseByPartition)
	t.Logf("真实FileJournal未知append=%d 压缩尝试=%d 压缩后行数=%d", attempts, faults.compactCalls, bytes.Count(data, []byte{'\n'}))
}

func TestPhysicalDayEveryMigrationStateRejectsNewNamespaceBeforeWrite(t *testing.T) {
	c := leasedFixture(t)
	key := c.Keys()[0]
	_, err := c.BeginDayMigration(key.UTCDay, OwnerCold, "cold")
	require.NoError(t, err)
	for _, state := range []MigrationState{StateRoutingFrozen, StateDraining, StateSnapshotting, StateStagingVerify, StateAttachedStaging, StateOwnerSwitched, StateQueryLeaseDraining, StateDetached} {
		if state != StateRoutingFrozen {
			if state == StateOwnerSwitched {
				current, _ := c.Get(key)
				pp := current.Clone().PublishedProjection
				pp.QueryGeneration = 2
				pp.QueryLocationDirID = "cold"
				_, err = c.SwitchDayOwners(key.UTCDay, OwnerCold, "cold", map[PartitionKey]*PublishedProjection{key: pp})
			} else {
				_, err = c.AdvanceDay(key.UTCDay, state)
			}
			require.NoError(t, err)
		}
		if state == StateStagingVerify {
			require.NoError(t, c.RecordDayVerification(key.UTCDay, "proof"))
		}
		late := PartitionKey{StorageNamespace: "new-" + string(state), UTCDay: key.UTCDay}
		_, err = c.AcquireWritePermit(late)
		require.ErrorIs(t, err, ErrPhysicalDayFrozen)
		_, err = c.WriteAdmission(late)
		require.ErrorIs(t, err, ErrPhysicalDayFrozen)
		record := NewStableRecord(late, OwnerHot, 1, "unpermitted-hot")
		record.PublishedProjection = &PublishedProjection{QueryGeneration: 1, QueryLocationDirID: record.OwnerDirID}
		require.ErrorIs(t, c.PublishProjection(nil, record), ErrPhysicalDayFrozen, "新namespace不能先物理落库再借首次发布回到HOT")
		_, exists := c.Get(late)
		require.False(t, exists)
	}
}

func TestWritePermitFreezeDrainPublicationBeforeSnapshot(t *testing.T) {
	c := leasedFixture(t)
	key := c.Keys()[0]
	expected, _ := c.Get(key)
	permit, err := c.AcquireWritePermit(key)
	require.NoError(t, err)
	_, err = c.BeginDayMigration(key.UTCDay, OwnerCold, "cold")
	require.ErrorIs(t, err, ErrWritePermitsActive)
	frozen, _ := c.Get(key)
	require.Equal(t, expected.JournalLastSeq, frozen.JournalLastSeq)
	require.Equal(t, expected.WriteRoute, frozen.WriteRoute)
	_, err = c.AcquireWritePermit(PartitionKey{StorageNamespace: "new", UTCDay: key.UTCDay})
	require.ErrorIs(t, err, ErrPhysicalDayFrozen)
	next := expected.Clone()
	next.PublishedProjection.ManifestVersion = "late-before-drain"
	next.PublishedProjection.SourceProjections[0].ClosedVisibleSeq = 20
	require.NoError(t, c.PublishProjection(expected, next), "已获permit的writer必须能完成发布")
	c.ReleaseWritePermit(permit)
	c.ReleaseWritePermit(permit)
	_, err = c.BeginDayMigration(key.UTCDay, OwnerCold, "cold")
	require.NoError(t, err)
	day, _ := c.PhysicalDay(key.UTCDay)
	require.Equal(t, "late-before-drain", day.Batches[0].Members[0].PublishedProjection.ManifestVersion)
}
