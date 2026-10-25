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

// TestResolveMigrateTargetExactNodePartitionPreferred 保留精确键准入，但动作仍以完整物理日为准。
func TestResolveMigrateTargetExactNodePartitionPreferred(t *testing.T) {
	const day = "2026-10-01"
	// node:1 已在冷层不能掩盖同一物理日的实例 HOT 残留。
	self := catalog.NewStableRecord(catalog.PartitionKey{StorageNamespace: "node:1", UTCDay: day}, catalog.OwnerCold, 2, "cold")
	cat := migrateTargetCatalog(t, self, hotRecord("inst:317", day))

	action, err := ResolveMigrateTarget(cat, "node:1", day)
	require.NoError(t, err)
	require.Equal(t, MigrateActionStart, action)
}

// TestResolveMigrateTargetNilCatalog 覆盖未接线 Catalog：必须报错而不是乐观放行。
func TestResolveMigrateTargetNilCatalog(t *testing.T) {
	_, err := ResolveMigrateTarget(nil, "node:1", "2026-10-01")
	require.Error(t, err)
}

// coldRecord 造一条该日已下沉到冷层的记录（Owner=cold + CLEANED，即迁移完成后的稳态）。
func coldRecord(namespace, day string) *catalog.Record {
	return catalog.NewStableRecord(catalog.PartitionKey{StorageNamespace: namespace, UTCDay: day}, catalog.OwnerCold, 2, "cold-"+day)
}

// inFlightRecord 造一条该日在途迁移的记录（已在途 + 目标冷层有效）。
func inFlightRecord(namespace, day string) *catalog.Record {
	rec := hotRecord(namespace, day)
	rec.MigrationState = catalog.StateDraining
	rec.TargetOwner = catalog.OwnerCold
	rec.TargetDirID = "cold-" + day
	rec.MigrationFromDirID = "hot-" + day
	return rec
}

// TestResolveMigrateTargetNodeScopedChecksEveryRecord 是本次缺陷的核心用例：节点维度探针
// 必须逐条检查该日全部记录，不能取 records[0] 当代表。
//
// 缺陷形态（修复前实测）：探针把该日所有分区坍缩成 records[0] 一条。
//   - 「部分已下沉（首条已冷）」用例中 records[0] 恰好是已 COLD 的记录 ⇒ 旧实现返回 Skip ⇒
//     整个迁移请求被当成「已完成」成功返回，未迁走的分区永远不会被搬运，热层数据到期后
//     被 retention 直接删除（真丢数据）。
//   - 「部分已下沉（首条仍热）」用例则相反，已全部下沉的日会被判成还要再迁（无谓重活）。
//
// 转红方式（变异验证）：把 ResolveMigrateTarget 的日级探针改回 `migrateActionFor(records[0])`，
// 「部分已下沉（首条已冷）」会从 Start 变成 Skip 而失败。
func TestResolveMigrateTargetNodeScopedChecksEveryRecord(t *testing.T) {
	const day = "2026-10-01"

	tests := []struct {
		name string
		recs []*catalog.Record
		want MigrateAction
	}{
		{
			// 关键用例：records 按 key 升序，inst:100 / inst:200 已冷 ⇒ 旧实现的代表记录是 Skip。
			name: "部分已下沉_首条已冷_必须仍要迁移",
			recs: []*catalog.Record{
				coldRecord("inst:100", day),
				coldRecord("inst:200", day),
				hotRecord("inst:300", day),
			},
			want: MigrateActionStart,
		},
		{
			// 反向排列：首条仍热 ⇒ 旧实现恰好也能返回 Start，这里守住「不能因为排序而漂移」。
			name: "部分已下沉_首条仍热_必须仍要迁移",
			recs: []*catalog.Record{
				hotRecord("inst:100", day),
				coldRecord("inst:200", day),
				coldRecord("inst:300", day),
			},
			want: MigrateActionStart,
		},
		{
			name: "全部已下沉_幂等成功",
			recs: []*catalog.Record{
				coldRecord("inst:100", day),
				coldRecord("inst:200", day),
				coldRecord("inst:300", day),
			},
			want: MigrateActionSkip,
		},
		{
			name: "全部未迁_必须发起迁移",
			recs: []*catalog.Record{
				hotRecord("inst:100", day),
				hotRecord("inst:200", day),
				hotRecord("inst:300", day),
			},
			want: MigrateActionStart,
		},
		{
			// 全在途：重试必须续跑，不能重新 Start（会与状态机冲突）。
			name: "全部在途_续跑",
			recs: []*catalog.Record{
				inFlightRecord("inst:100", day),
				inFlightRecord("inst:200", day),
			},
			want: MigrateActionResume,
		},
		{
			// 冷热混合里夹着在途记录：同样不得 Skip（Skip 会吞掉未完成的下沉）。
			name: "部分已下沉_含在途_不得跳过",
			recs: []*catalog.Record{
				coldRecord("inst:100", day),
				inFlightRecord("inst:200", day),
			},
			want: MigrateActionResume,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cat := migrateTargetCatalog(t, tt.recs...)
			// 节点级面板发的就是 `node:<节点ID>`：该键本身不在 Catalog 里，必然走日级探针。
			action, err := ResolveMigrateTarget(cat, "node:1", day)
			require.NoError(t, err)
			require.Equal(t, tt.want, action)
		})
	}
}

// TestResolveMigrateTargetNodeScopedEmptyDayReturnsNotInCatalog 守住空日错误面：
// 该日一条记录都没有时仍必须是 notInCatalogErr（CP 侧稳定映射 409），不能退化成 Skip。
func TestResolveMigrateTargetNodeScopedEmptyDayReturnsNotInCatalog(t *testing.T) {
	const day = "2026-10-01"
	// 只有别的日期的分区。
	cat := migrateTargetCatalog(t, coldRecord("inst:317", "2026-09-30"))

	action, err := ResolveMigrateTarget(cat, "node:1", day)
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not in Catalog")
	require.Equal(t, notInCatalogErr(catalog.PartitionKey{StorageNamespace: "node:1", UTCDay: day}).Error(), err.Error(),
		"空日必须保持 notInCatalogErr 的原文案")
	// 出错时动作值不参与语义，这里只固定既有实现的返回值，避免无意漂移。
	require.Equal(t, MigrateActionStart, action)
}
