package lifecycle

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// migrateTargetCatalog 构造一个只装了给定分区的 Catalog。
func migrateTargetCatalog(t *testing.T, recs ...*catalog.Record) *catalog.Catalog {
	t.Helper()
	cat := catalog.New(catalog.NewMemJournal())
	for _, rec := range recs {
		require.NoError(t, cat.Put(rec))
	}
	return cat
}

// hotRecord 造一条该日仍在热层、未开始迁移的记录。
func hotRecord(namespace, day string) *catalog.Record {
	return catalog.NewStableRecord(catalog.PartitionKey{StorageNamespace: namespace, UTCDay: day}, catalog.OwnerHot, 1, "hot-"+day)
}

// TestResolveMigrateTargetNodeScopedProbeFallsBackToDay 是本次缺陷的正面用例。
//
// 缺陷形态（修复前实测）：节点级面板（NodeLogRuntimePanel）发 `node:<节点ID>`，而 Worker 只做
// (namespace, day) 精确查 Catalog。catalog 里 `node:<id>` 分区只在该节点配了 log_sources 节点自身
// 日志源时才存在（worker.yml 出厂示例整段注释掉），默认部署只有 `inst:<id>` ⇒ 精确键必然落空 ⇒
// 按钮稳定 409「is not in Catalog」，功能等于不可用。
//
// 转红方式（变异验证）：把 ResolveMigrateTarget 里「节点维度目标退化为日级探针」那一段删掉，
// 改为精确键落空即返回 notInCatalogErr —— 本用例会从 Start 变成 err「is not in Catalog」。
func TestResolveMigrateTargetNodeScopedProbeFallsBackToDay(t *testing.T) {
	const day = "2026-10-01"
	// 只有实例分区，没有 `node:1` 分区 —— 这正是默认部署的形态。
	cat := migrateTargetCatalog(t, hotRecord("inst:317", day), hotRecord("inst:318", day))

	action, err := ResolveMigrateTarget(cat, "node:1", day)
	require.NoError(t, err, "节点级目标必须能落到该日的分区上")
	require.Equal(t, MigrateActionStart, action)
}

// TestResolveMigrateTargetNodeScopedEmptyDayStillFails 守住错误面：该日整节点都没有分区时
// 仍必须报「is not in Catalog」（CP 侧据此稳定映射 409），不能因为放宽探针就把空日变成空成功。
func TestResolveMigrateTargetNodeScopedEmptyDayStillFails(t *testing.T) {
	const day = "2026-10-01"
	// 只有别的日期的分区：本日仍然无数据。
	cat := migrateTargetCatalog(t, hotRecord("inst:317", "2026-09-30"))

	_, err := ResolveMigrateTarget(cat, "node:1", day)
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not in Catalog", "空日必须保持原有错误面，不得静默成功")
}

// TestResolveMigrateTargetInstanceNamespaceIsNotWidened 守住「不放大」：认不出的实例命名空间
// 落空时不得退化成日级探针。否则拼错的键会静默变成「迁了一整天」，而调用方以为只动了一个实例。
func TestResolveMigrateTargetInstanceNamespaceIsNotWidened(t *testing.T) {
	const day = "2026-10-01"
	cat := migrateTargetCatalog(t, hotRecord("inst:317", day))

	_, err := ResolveMigrateTarget(cat, "inst:999", day)
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not in Catalog")
}

// TestResolveMigrateTargetUnknownFormRejected 覆盖既非 inst: 也非 node: 的形态。
func TestResolveMigrateTargetUnknownFormRejected(t *testing.T) {
	const day = "2026-10-01"
	cat := migrateTargetCatalog(t, hotRecord("inst:317", day))

	_, err := ResolveMigrateTarget(cat, "whatever-the-caller-wants", day)
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not in Catalog")
}

// TestResolveMigrateTargetExactKeyKeepsLegacyBranches 锁定「精确键命中时行为逐字不变」：
// 幂等成功 / 续跑 / 新起 三个分支必须与改造前的迁移闭包完全一致。
func TestResolveMigrateTargetExactKeyKeepsLegacyBranches(t *testing.T) {
	const day = "2026-10-01"
	key := catalog.PartitionKey{StorageNamespace: "inst:317", UTCDay: day}

	cold := catalog.NewStableRecord(key, catalog.OwnerCold, 2, "cold")
	inFlight := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot")
	inFlight.MigrationState = catalog.StateDraining
	inFlight.TargetOwner = catalog.OwnerCold
	inFlight.TargetDirID = "cold"

	tests := []struct {
		name string
		rec  *catalog.Record
		want MigrateAction
	}{
		{"已冷", cold, MigrateActionSkip},
		{"在途", inFlight, MigrateActionResume},
		{"待迁", hotRecord("inst:317", day), MigrateActionStart},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cat := migrateTargetCatalog(t, tt.rec)
			action, err := ResolveMigrateTarget(cat, "inst:317", day)
			require.NoError(t, err)
			require.Equal(t, tt.want, action)
		})
	}
}

// TestResolveMigrateTargetExactNodePartitionPreferred 覆盖真配了节点自身日志源的部署：
// catalog 里确实存在 `node:<id>` 分区时，精确键优先，不得走到日级退化分支。
func TestResolveMigrateTargetExactNodePartitionPreferred(t *testing.T) {
	const day = "2026-10-01"
	// node:1 自身已是冷层，同日的实例分区仍是热层。精确键优先 ⇒ 幂等成功，而不是又迁一整天。
	self := catalog.NewStableRecord(catalog.PartitionKey{StorageNamespace: "node:1", UTCDay: day}, catalog.OwnerCold, 2, "cold")
	cat := migrateTargetCatalog(t, self, hotRecord("inst:317", day))

	action, err := ResolveMigrateTarget(cat, "node:1", day)
	require.NoError(t, err)
	require.Equal(t, MigrateActionSkip, action)
}

// TestResolveMigrateTargetNilCatalog 覆盖未接线 Catalog：必须报错而不是乐观放行。
func TestResolveMigrateTargetNilCatalog(t *testing.T) {
	_, err := ResolveMigrateTarget(nil, "node:1", "2026-10-01")
	require.Error(t, err)
}
