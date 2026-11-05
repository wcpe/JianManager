package ingest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// fakeHits 是可控的 hits 数据源。
type fakeHits struct {
	facts []SourceDayFact
	err   error
	calls int
	start time.Time
	end   time.Time
}

func (f *fakeHits) HitsBySourceDay(_ context.Context, start, end time.Time) ([]SourceDayFact, error) {
	f.calls++
	f.start, f.end = start, end
	return f.facts, f.err
}

// newHitsFixture 造一个只有 Catalog 与已登记源的 Manager（不启动采集）。
func newHitsFixture(t *testing.T, sources []SourceConfig) (*Manager, *catalog.Catalog) {
	t.Helper()
	cat := catalog.New(catalog.NewMemJournal())
	m := &Manager{cat: cat, state: persistedState{
		Sources:       map[string]persistedSource{},
		SourceConfigs: map[string]SourceConfig{},
		Instances:     map[string]InstanceBinding{},
	}}
	for _, cfg := range sources {
		// 键形态与真实登记一致：LogSourceID/SourceGeneration。
		m.state.SourceConfigs[cfg.LogSourceID+"/"+cfg.SourceGeneration] = cfg
	}
	return m, cat
}

// 回归（2026-11-05 架构审计 D1）：hits 给出的 (源,天) 事实必须变成可查的分区记录。
//
// 这是「历史天查不到」的正面用例：改造前分区目录只靠投递增量维护，
// 进程重启后没有任何一天的记录 ⇒ 11-01/11-02 的数据在 VL 里却查不到。
func TestRebuildCatalogPartitionsFromHitsCreatesRecords(t *testing.T) {
	src := SourceConfig{
		LogSourceID: "inst:318/file", SourceGeneration: "g1",
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:318", UTCDay: "2026-11-04",
	}
	m, cat := newHitsFixture(t, []SourceConfig{src})
	m.SetHitsSource(&fakeHits{facts: []SourceDayFact{
		{LogSourceID: "inst:318/file", UTCDay: "2026-11-01", Weight: 2880},
		{LogSourceID: "inst:318/file", UTCDay: "2026-11-02", Weight: 1746},
	}})

	stats, err := m.RebuildCatalogPartitionsFromHits(context.Background(), CatalogBackfillOptions{
		Days: 30, Now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.Equal(t, 2, stats.Created, "两天都应建立分区，实际: %s", stats.String())

	for _, day := range []string{"2026-11-01", "2026-11-02"} {
		rec, ok := cat.Get(catalog.PartitionKey{StorageNamespace: "inst:318", UTCDay: day})
		require.True(t, ok, "必须为 %s 建立 Catalog 分区记录", day)
		// 权威仍由 WriteAdmission 决定：owner 与 dirID 必须非空，否则查询侧判不可查。
		require.NotEmpty(t, rec.Owner)
		require.NotEmpty(t, rec.OwnerDirID)
		// 简化投递路径的形状：不填逐源投影（否则查询会按不存在的代次过滤）。
		require.Empty(t, rec.PublishedProjection.SourceProjections)
	}
	// 不该影响未在事实里的天。
	_, exists := cat.Get(catalog.PartitionKey{StorageNamespace: "inst:318", UTCDay: "2026-11-03"})
	require.False(t, exists, "11-03 无数据，不得建立分区")
}

// 未登记的源（实测有 11 个 `probe:*` 探针源）不得进入分区目录：
// 否则查询侧会为不存在的实例建立「可查」覆盖，用户点进去得到的是一个必然为空的 range。
func TestRebuildCatalogPartitionsFromHitsSkipsUnregisteredSources(t *testing.T) {
	src := SourceConfig{
		LogSourceID: "inst:318/file", SourceGeneration: "g1",
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:318", UTCDay: "2026-11-04",
	}
	m, cat := newHitsFixture(t, []SourceConfig{src})
	m.SetHitsSource(&fakeHits{facts: []SourceDayFact{
		{LogSourceID: "probe:strict", UTCDay: "2026-11-04", Weight: 2},
		{LogSourceID: "inst:999/file", UTCDay: "2026-11-04", Weight: 5},
		{LogSourceID: "inst:318/file", UTCDay: "2026-11-04", Weight: 890},
	}})

	stats, err := m.RebuildCatalogPartitionsFromHits(context.Background(), CatalogBackfillOptions{
		Days: 30, Now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.Equal(t, 1, stats.Created)
	require.Equal(t, 2, stats.SkippedExisting, "两个未登记源都应跳过")

	for _, ns := range []string{"probe:strict", "inst:999"} {
		_, ok := cat.Get(catalog.PartitionKey{StorageNamespace: ns, UTCDay: "2026-11-04"})
		require.False(t, ok, "未登记源 %s 不得进入分区目录", ns)
	}
}

// 已有记录一律不得覆盖：把迁移到 COLD 的分区强行改回 HOT 比「查不到」更严重（数据面破坏）。
func TestRebuildCatalogPartitionsFromHitsNeverOverwritesExisting(t *testing.T) {
	src := SourceConfig{
		LogSourceID: "inst:318/file", SourceGeneration: "g1",
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:318", UTCDay: "2026-11-04",
	}
	m, cat := newHitsFixture(t, []SourceConfig{src})
	// 预置一条「已迁移到 COLD」的记录。
	cold := catalog.NewStableRecord(
		catalog.PartitionKey{StorageNamespace: "inst:318", UTCDay: "2026-11-04"},
		catalog.OwnerCold, 7, "cold-dir-1")
	cold.PublishedProjection = &catalog.PublishedProjection{QueryLocationDirID: "cold-dir-1", QueryGeneration: 7}
	require.NoError(t, cat.Put(cold))

	m.SetHitsSource(&fakeHits{facts: []SourceDayFact{
		{LogSourceID: "inst:318/file", UTCDay: "2026-11-04", Weight: 890},
	}})
	stats, err := m.RebuildCatalogPartitionsFromHits(context.Background(), CatalogBackfillOptions{
		Days: 30, Now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.Equal(t, 0, stats.Created)
	require.Equal(t, 1, stats.SkippedExisting)

	rec, ok := cat.Get(catalog.PartitionKey{StorageNamespace: "inst:318", UTCDay: "2026-11-04"})
	require.True(t, ok)
	require.Equal(t, catalog.OwnerCold, rec.Owner, "已有权威（COLD）不得被重建改回 HOT")
	require.Equal(t, "cold-dir-1", rec.OwnerDirID)
}

// 没有接到 hits 数据源时不得报错、也不得伪造分区：分区目录保持为空，
// 查询侧会如实报「无分区记录」而不是粉饰成空成功。
func TestRebuildCatalogPartitionsFromHitsWithoutSourceIsNoOp(t *testing.T) {
	src := SourceConfig{
		LogSourceID: "inst:318/file", SourceGeneration: "g1",
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:318", UTCDay: "2026-11-04",
	}
	m, cat := newHitsFixture(t, []SourceConfig{src})

	stats, err := m.RebuildCatalogPartitionsFromHits(context.Background(), CatalogBackfillOptions{Days: 30})
	require.NoError(t, err)
	require.Equal(t, 0, stats.Created)
	require.Empty(t, cat.Keys(), "未接数据源时不得凭空造分区")
}

// hits 查询失败必须把错误返回给调用方（由启动路径打 ERROR），
// 不得静默当成「VL 里没有数据」——那会把分区目录清空。
func TestRebuildCatalogPartitionsFromHitsPropagatesQueryError(t *testing.T) {
	src := SourceConfig{
		LogSourceID: "inst:318/file", SourceGeneration: "g1",
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:318", UTCDay: "2026-11-04",
	}
	m, cat := newHitsFixture(t, []SourceConfig{src})
	m.SetHitsSource(&fakeHits{err: errors.New("vl unavailable")})

	_, err := m.RebuildCatalogPartitionsFromHits(context.Background(), CatalogBackfillOptions{Days: 30})
	require.Error(t, err)
	require.Empty(t, cat.Keys())
}

// 天界口径：窗口必须落在 UTC 日边界上，且天数语义与 backfillDaysWindow 一致。
// 若窗口算错（例如带上本地时区偏移），hits 会漏掉边界那天。
func TestRebuildCatalogPartitionsFromHitsUsesUTCDayWindow(t *testing.T) {
	src := SourceConfig{
		LogSourceID: "inst:318/file", SourceGeneration: "g1",
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:318", UTCDay: "2026-11-04",
	}
	m, _ := newHitsFixture(t, []SourceConfig{src})
	fake := &fakeHits{}
	m.SetHitsSource(fake)

	// 11-05 12:00 UTC，回搜 3 天 ⇒ [11-03T00:00Z, 11-06T00:00Z)
	_, err := m.RebuildCatalogPartitionsFromHits(context.Background(), CatalogBackfillOptions{
		Days: 3, Now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.Equal(t, 1, fake.calls)
	require.Equal(t, time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC), fake.start.UTC())
	require.Equal(t, time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC), fake.end.UTC())
}
