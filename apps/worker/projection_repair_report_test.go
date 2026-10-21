package main

// 本文件是缺陷 A4（🟡 可观测性）的回归：启动期投影代次自愈对「需人工」的形态完全无输出。
//
// 缺陷（只读复审确证）：`catalog.RepairProjectionQueryGeneration` 对三种「本该交人工」的形态
// 返回 `(false, nil)` —— ① `qgen > gen`（反向矛盾，成因未知）；②
// `QueryLocationDirID != OwnerDirID`（目录也已不一致）；③ `PublishedProjection == nil`。
// 调用方 `repairProjectionQueryGenerations` 只在 `repaired > 0 || failed > 0` 时打日志
// ⇒ 这些形态**既不计数也不打日志**，运维完全看不到「还有 N 个待人工」。
//
// 与既成损害的因果关系：生产那 28 个分区的发现过程本身就是因为「没人报」——自愈跑完打一条
// 「自愈完成」，而实际上还有一批分区永久不可发布。沉默的「不修」等价于「假装健康」。
//
// 修法（本文件的判据）：`RepairProjectionQueryGeneration` 增加 `needsManual` 返回值区分
// 「无需修复」与「需人工」，`repairProjectionQueryGenerations` 汇总成清单，调用方单独打一条
// WARN 列出分区与原因。健康分区必须**不**进清单（否则这条 WARN 变成噪音，运维会忽略它）。
//
// 变异验证（实测输出见报告）：把 needsManual 一律置空（改回只报 repaired/failed）
// ⇒ 本文件用例转红。

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// repairTestKey 给每个形态分配独立分区键，便于断言「清单里点名的是它」。
func repairTestKey(day string) catalog.PartitionKey {
	return catalog.PartitionKey{StorageNamespace: "inst:repair", UTCDay: day}
}

// putRepairRecord 走**正规入口**把一条记录放进 catalog（投影由 forceProjection 决定）。
//
// 为什么用 AppendJournal 而不是 PublishProjection 造坏形态：`PublishProjection` 的前置校验
// 恰好就是我们要造的这三种不合法形态的守门人（qgen != gen 一律拒绝），用它造夹具等于
// 「先让校验通过再造坏数据」——造不出来。AppendJournal 是 journal 的原始入口，
// 正是历史上把这些形态写进生产的同一条通道（见 repair_query_generation_test.go 的背景）。
func putRepairRecord(t *testing.T, cat *catalog.Catalog, key catalog.PartitionKey,
	gen uint64, qgen uint64, queryDir string, withProjection bool) {
	t.Helper()
	rec := catalog.NewStableRecord(key, catalog.OwnerHot, gen, "hot-"+key.UTCDay)
	if withProjection {
		rec.PublishedProjection = &catalog.PublishedProjection{
			ManifestVersion:      "manifest-" + key.UTCDay,
			ProjectionGeneration: "projection-" + key.UTCDay,
			CoverageComplete:     true,
			ConflictCount:        0,
			QueryLocationDirID:   queryDir,
			QueryGeneration:      qgen,
		}
	}
	require.NoError(t, cat.AppendJournal(catalog.JournalEntry{
		Key: key, Record: rec, State: rec.MigrationState,
		Detail: "fixture: build projection state for repair self-heal regression",
	}))
}

// TestRepairProjectionQueryGenerationsReportsNeedsManual 覆盖三种「需人工」形态 + 两类正常形态。
//
// 断言：
//  1. 可修的分区被修（repaired == 1，证明遍历与修复仍在工作，没有因为加了上报而失效）；
//  2. 三种需人工形态**全部**进 needsManual，且各自点名分区与原因；
//  3. 健康分区**不**进清单（否则 WARN 变成噪音）。
func TestRepairProjectionQueryGenerationsReportsNeedsManual(t *testing.T) {
	cat := catalog.New(catalog.NewMemJournal())

	// 可修：qgen(1) < gen(2)，目录一致 ⇒ 必须被修好，且不进需人工清单。
	fixable := repairTestKey("2026-10-01")
	putRepairRecord(t, cat, fixable, 2, 1, "hot-2026-10-01", true)

	// 健康：qgen == gen ⇒ 既不修也不报。
	healthy := repairTestKey("2026-10-02")
	putRepairRecord(t, cat, healthy, 2, 2, "hot-2026-10-02", true)

	// 需人工①：qgen(5) > gen(2)，反向矛盾（成因未知）。
	reversed := repairTestKey("2026-10-03")
	putRepairRecord(t, cat, reversed, 2, 5, "hot-2026-10-03", true)

	// 需人工②：qgen(1) < gen(2) 但投影目录已不是权威目录（另一类损坏）。
	dirMismatch := repairTestKey("2026-10-04")
	putRepairRecord(t, cat, dirMismatch, 2, 1, "some-old-dir", true)

	// 需人工③：没有已发布投影。
	noProjection := repairTestKey("2026-10-05")
	putRepairRecord(t, cat, noProjection, 2, 0, "", false)

	repaired, failed, needsManual := repairProjectionQueryGenerations(cat)
	require.Equal(t, 1, repaired, "只有投影落后且其余字段自洽的分区可被自动修复")
	require.Zero(t, failed, "本夹具不含返回 error 的形态")

	require.Len(t, needsManual, 3, "三种需人工形态都必须被报出来（早前它们既不计数也不打日志）")
	joined := strings.Join(needsManual, "\n")
	for _, key := range []catalog.PartitionKey{reversed, dirMismatch, noProjection} {
		require.Contains(t, joined, key.String(), "需人工清单必须点名分区：%s", key.String())
	}
	require.NotContains(t, joined, healthy.String(), "健康分区不得进需人工清单（否则 WARN 成为噪音）")
	require.NotContains(t, joined, fixable.String(), "已自动修好的分区不得进需人工清单")
	require.Contains(t, joined, "反向矛盾", "原因文案必须说清是哪种形态，运维才知道该怎么判")
	require.Contains(t, joined, "目录", "目录不一致必须单独说明是另一类损坏")
	require.Contains(t, joined, "没有已发布投影", "无投影形态必须被说明")
}

// TestRepairProjectionQueryGenerationsNilCatalog 确认空 Catalog 不会 panic 也不报需人工。
func TestRepairProjectionQueryGenerationsNilCatalog(t *testing.T) {
	repaired, failed, needsManual := repairProjectionQueryGenerations(nil)
	require.Zero(t, repaired)
	require.Zero(t, failed)
	require.Empty(t, needsManual)
}
