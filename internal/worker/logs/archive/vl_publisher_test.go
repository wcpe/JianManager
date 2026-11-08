package archive

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// rehydratePublisherFixture 构造一条 OwnerArchive 记录 + 可回放的 VL 假服务。
//
// priorConflict 非 0 时模拟「HOT 发布时已累计冲突计数、archive 迁移由
// lifecycle.migratedProjection 原样继承」的既有记录——这是 ConflictCount 唯一
// 可能以非 0 形态出现在 archive 记录上的来源（本包无权重算它）。
func rehydratePublisherFixture(t *testing.T, priorConflict uint64) (*VLRehydratePublisher, PartitionKey, catalog.PartitionKey, []ArchivedEvent) {
	t.Helper()
	key := PartitionKey{StorageNamespace: "node:1", UTCDay: "2026-09-22", Generation: 1}
	catKey := catalog.PartitionKey{StorageNamespace: key.StorageNamespace, UTCDay: key.UTCDay}
	rec := catalog.NewStableRecord(catKey, catalog.OwnerArchive, key.Generation, "deep-g1")
	if priorConflict > 0 {
		rec.PublishedProjection = &catalog.PublishedProjection{
			ManifestVersion:    "manifest-hot-prior",
			ConflictCount:      priorConflict,
			QueryLocationDirID: "deep-g1",
			QueryGeneration:    key.Generation,
		}
	}
	cat := catalog.New(catalog.NewMemJournal())
	require.NoError(t, cat.Put(rec))

	var writes [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/insert/jsonline":
			payload := vlInsertBody(r)
			writes = append(writes, payload)
			w.WriteHeader(http.StatusOK)
		case "/select/logsql/query":
			// 把已写入的行原样回放，模拟「物理行确实可见」。
			for _, data := range writes {
				_, _ = w.Write(data)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)

	events := []ArchivedEvent{archivedTestEvent("2026-09-22", "archived")}
	return &VLRehydratePublisher{Catalog: cat, VL: vl}, key, catKey, events
}

// TestRehydratePublisherInheritsConflictCount 钉住「rehydrate 不得把既有冲突计数清零」。
//
// 判据取自唯一真实消费方：planner 读 ref.Projection.ConflictCount>0 才报 DupConflict。
// 若此处把计数抹成 0，rehydrate 成功后该 archive 分区重新变为可查询，就会对一个
// 实际存在身份冲突的分区误报 exact（前端不再提示、导出质量门禁被绕过）。
func TestRehydratePublisherInheritsConflictCount(t *testing.T) {
	const prior uint64 = 7
	publisher, key, catKey, events := rehydratePublisherFixture(t, prior)

	require.NoError(t, publisher.Publish(context.Background(), key, events, "task-1"))

	rec, ok := publisher.Catalog.Get(catKey)
	require.True(t, ok)
	require.NotNil(t, rec.PublishedProjection)
	require.Equal(t, prior, rec.PublishedProjection.ConflictCount,
		"rehydrate 必须继承既有冲突计数：本包无法重算，清零等于伪造「无冲突」")
	require.Equal(t, "rehydrate-task-1", rec.PublishedProjection.ProjectionGeneration,
		"夹具前提：本次发布确实走了 rehydrate 发布路径")
}

// TestRehydratePublisherKeepsZeroConflictCountWhenAbsent 反向约束：既有记录本就没有
// 冲突时不得凭空造出非 0 计数（继承而非「复制上一次的猜测」）。
func TestRehydratePublisherKeepsZeroConflictCountWhenAbsent(t *testing.T) {
	publisher, key, catKey, events := rehydratePublisherFixture(t, 0)

	require.NoError(t, publisher.Publish(context.Background(), key, events, "task-2"))

	rec, ok := publisher.Catalog.Get(catKey)
	require.True(t, ok)
	require.NotNil(t, rec.PublishedProjection)
	require.Zero(t, rec.PublishedProjection.ConflictCount)
}

// TestRehydratePublisherClosedVisibleSeqUsesCatalogPartitionKey 钉住第三键的语义：
// 必须写「分区键」namespace/day，与读取侧（planner 的 targetID、ingest 的
// ClosedVisibleSeq[key.String()]）以及 HOT 发布同形；**不得**写带 generation 的
// archive.PartitionKey.String()。
func TestRehydratePublisherClosedVisibleSeqUsesCatalogPartitionKey(t *testing.T) {
	publisher, key, catKey, events := rehydratePublisherFixture(t, 0)

	require.NoError(t, publisher.Publish(context.Background(), key, events, "task-3"))

	rec, ok := publisher.Catalog.Get(catKey)
	require.True(t, ok)
	require.NotNil(t, rec.PublishedProjection)
	seq := rec.PublishedProjection.ClosedVisibleSeq

	// 夹具前提：事件区间为 1..8，封闭水位应等于 max(record_end)。
	const wantSeq = uint64(8)
	require.Equal(t, wantSeq, seq[catKey.String()],
		"第三键必须是 namespace/day（catalog 分区键），读取侧按它索引")
	require.Equal(t, wantSeq, seq["default"], "default 兜底键必须存在")
	require.Equal(t, wantSeq, seq["node:1/g1"], "per-source 键必须存在（HOT 侧同形）")

	_, hasSourceKey := seq[key.String()]
	require.False(t, hasSourceKey,
		"第三键不得是 archive.PartitionKey.String()（含 /g{generation}）：读取侧从不按含代数串索引，写入它等于制造无人消费的槽位")
	require.Len(t, seq, 3, "向量形状应为 per-source + 分区键 + default 三项")
}

// TestRehydratePublisherRejectsNonArchiveAuthority 回归：发布前置校验仍要求
// OwnerArchive + 代数对齐（继承 ConflictCount 的改动不得放松权威校验）。
func TestRehydratePublisherRejectsNonArchiveAuthority(t *testing.T) {
	publisher, key, _, events := rehydratePublisherFixture(t, 0)
	hotKey := catalog.PartitionKey{StorageNamespace: key.StorageNamespace, UTCDay: key.UTCDay}
	require.NoError(t, publisher.Catalog.Put(catalog.NewStableRecord(hotKey, catalog.OwnerHot, key.Generation, "hot-g1")))

	err := publisher.Publish(context.Background(), key, events, "task-4")
	require.ErrorContains(t, err, "no matching DEEP Catalog authority")
}
