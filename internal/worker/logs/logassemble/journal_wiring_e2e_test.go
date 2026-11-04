package logassemble

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

// 本文件是 2026-11-05 生产事故的端到端回归。
//
// # 事故形态
//
// 我在 apps/worker 里把 Catalog journal 从「纯内存」改成落盘，用的是
//
//	NewJournalWithStore(NewBatchedStore(NewJSONLFileStore(path), 2s, 256))
//
// 部署后**所有查询被拒**：
//
//	query lease admission rejected: catalog: custom journal has no durable lease store
//
// 根因：`NewJournalWithStore` 当时用**具体类型断言** `store.(*JSONLFileStore)` 决定
// 租约旁路文件路径；外面包了一层 `BatchedStore` 就断言失败 ⇒ 租约存储被标记为
// unavailable ⇒ 而那个分支**返回错误** ⇒ `Catalog` 的租约准入全线拒绝。
//
// # 为什么原有测试没抓到（本文件存在的全部理由）
//
// 当时的接线测试只验证了「重启后分区记录还在」。**journal 能加载 ≠ 查询能用。**
// 记录确实加载了，日志也确实写了，但查询在拿到记录之前就先死在租约准入上。
//
// 所以本文件必须**真的跑一次 Plan**，断言能产出 range；并且断言
// 「用同一份文件重建后仍然能查到」——只测前者会漏掉重放路径的问题。
//
// 注意本文件放在 logassemble 而不是 catalog：这里要同时用到
// catalog（journal/记录）与 query（Planner），而 query 依赖 catalog，
// 放在 catalog 里会构成测试包循环依赖。logassemble 正是真实的装配点，更贴近生产。

// writePartition 用真实的 Catalog 提交路径写一条分区记录（形状与
// ingest.ensureCatalogPartition 一致：有非 nil 投影、刻意不填代次）。
func writePartition(t *testing.T, cat *catalog.Catalog, namespace, day string) catalog.PartitionKey {
	t.Helper()
	key := catalog.PartitionKey{StorageNamespace: namespace, UTCDay: day}
	rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-"+day)
	rec.PublishedProjection = &catalog.PublishedProjection{
		QueryLocationDirID: "hot-" + day,
		QueryGeneration:    1,
	}
	if err := cat.AppendJournal(catalog.JournalEntry{Key: key, Record: rec}); err != nil {
		t.Fatalf("append journal %s: %v", key.String(), err)
	}
	return key
}

// planRanges 跑一次真实查询规划，返回 range 数与错误。
func planRanges(t *testing.T, stack *Stack, namespace, day string) int {
	t.Helper()
	res := stack.Planner.Plan(query.PlanRequest{
		TargetIDs: []string{namespace},
		TimeRange: query.TimeRange{
			FromUTC: day + "T00:00:00Z",
			ToUTC:   day + "T23:59:59Z",
		},
		Budget: query.Budget{Limit: 10},
	})
	if res.Err != nil {
		t.Fatalf("规划不得报错（租约准入失败会在这里暴露）：%v", res.Err)
	}
	return len(res.Ranges)
}

// TestBatchedJournalWiring_QueryWorksAndSurvivesRestart 是本次事故的完整回归。
//
// 三件事必须同时成立，缺一条就是「能加载但查不到」：
//  1. 攒批 journal 构造出的 Catalog **能过查询租约准入**（事故当天死在这里）；
//  2. 能规划出**非空 range**（记录在、查询面能选中它）；
//  3. 用同一份文件重建（模拟重启）后仍然能查到。
//
// 转红方式（实测）：把 NewJournalWithStore 里的 LeaseStoreProvider 判断改回
// `store.(*JSONLFileStore)`，本用例在第一步就以
// `query lease admission rejected: ... no durable lease store` 失败。
func TestBatchedJournalWiring_QueryWorksAndSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.journal.jsonl")
	const (
		namespace = "inst:318"
		day       = "2026-11-04"
	)

	// —— 用与 apps/worker 完全相同的构造链（JSONL → 攒批 → journal）——
	store, err := catalog.NewJSONLFileStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	batch := catalog.NewBatchedStore(store, time.Hour, 0)
	journal, err := catalog.NewJournalWithStore(batch)
	if err != nil {
		t.Fatalf("journal: %v", err)
	}

	stack := Build(journal, nil, "test-build")
	writePartition(t, stack.Catalog, namespace, day)

	// 判据 ①②：查询租约准入 + 规划出 range。
	if got := planRanges(t, stack, namespace, day); got == 0 {
		t.Fatal("投递后必须规划出至少一条 range；为 0 即「记录在、数据在，却查不到」")
	}

	// 落盘并「重启」。
	if err := batch.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopenedStore, err := catalog.NewJSONLFileStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	reopenedBatch := catalog.NewBatchedStore(reopenedStore, time.Hour, 0)
	defer func() { _ = reopenedBatch.Close() }()
	reopenedJournal, err := catalog.NewJournalWithStore(reopenedBatch)
	if err != nil {
		t.Fatalf("replay journal: %v", err)
	}
	restarted := Build(reopenedJournal, nil, "test-build")

	// 判据 ③：重启后仍能查到。
	if got := planRanges(t, restarted, namespace, day); got == 0 {
		t.Fatal("重启后必须仍能规划出 range——这正是「历史天在查询侧消失」的回归点")
	}
	// 记录本身也必须在（双重确认：既能查到，也确实从文件恢复出来了）。
	if _, ok := restarted.Catalog.Get(catalog.PartitionKey{StorageNamespace: namespace, UTCDay: day}); !ok {
		t.Fatal("重启后分区记录必须仍在 Catalog 里")
	}
}

// TestJournalWithStore_LeasePersistenceSurvivesWrapper 钉住「包一层不损失租约能力」。
//
// 事故的根因是能力被包装层吃掉。这里直接断言能力本身：攒批包装必须透传
// LeaseStoreProvider，而不是让上层退化成内存态。
//
// 转红方式（实测）：删掉 BatchedStore.LeaseStorePath，本用例变红。
func TestJournalWithStore_LeasePersistenceSurvivesWrapper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.journal.jsonl")
	store, err := catalog.NewJSONLFileStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	batch := catalog.NewBatchedStore(store, time.Hour, 0)
	defer func() { _ = batch.Close() }()

	// 能力必须透传（这是接口化的直接价值）。
	provider, ok := any(batch).(catalog.LeaseStoreProvider)
	if !ok {
		t.Fatal("BatchedStore 必须实现 LeaseStoreProvider：否则租约持久化会被包装层静默吃掉（本次生产事故）")
	}
	leasePath, ok := provider.LeaseStorePath()
	if !ok || leasePath == "" {
		t.Fatalf("租约旁路路径必须可得，实测 (%q,%v)", leasePath, ok)
	}
	if want := path + ".leases"; leasePath != want {
		t.Fatalf("租约路径应为 %s（与 journal 同前缀），实测 %s", want, leasePath)
	}

	// 而且真的能持久化：写入一次租约增量后，同文件重建应能读到。
	journal, err := catalog.NewJournalWithStore(batch)
	if err != nil {
		t.Fatalf("journal: %v", err)
	}
	if err := journal.AppendLeaseDelta(catalog.LeaseDelta{Revision: 7, ViewID: "v-1"}); err != nil {
		t.Fatalf("append lease delta: %v", err)
	}
	if err := batch.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopenedStore, err := catalog.NewJSONLFileStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	reopened, err := catalog.NewJournalWithStore(reopenedStore)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	deltas, err := reopened.LeaseDeltaSnapshot()
	if err != nil {
		t.Fatalf("lease snapshot: %v", err)
	}
	if len(deltas) == 0 {
		t.Fatal("租约增量必须已落盘并可被重放（为空说明退化成了纯内存态）")
	}
	if deltas[0].ViewID != "v-1" {
		t.Fatalf("重放出的租约增量应为 v-1，实测 %q", deltas[0].ViewID)
	}
}
