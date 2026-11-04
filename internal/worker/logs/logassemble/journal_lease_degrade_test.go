package logassemble

import (
	"testing"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

// bareStore 是**不实现** LeaseStoreProvider 的 store（模拟任意第三方/自定义存储）。
//
// 它对应的场景就是本次事故：只要某个 store 没提供租约旁路，旧实现就让整个查询面
// 拒绝服务。现在必须降级为内存租约，查询照常可用。
type bareStore struct{ inner *catalog.MemJournal }

func (b *bareStore) AppendEntry(catalog.JournalEntry) error { return nil }
func (b *bareStore) LoadEntries() ([]catalog.JournalEntry, error) {
	return nil, nil
}

// TestJournalWithoutLeaseStore_QueryStillWorks 无租约持久化时**降级**而不是拒绝服务。
//
// 这是 2026-11-05 事故的第二半修复：即使能力判断错了、或存储确实不支持租约旁路，
// 后果也只能是「旧视图失效得早一点」，不能是整个查询面 100% 失败。
//
// 判据（两层都要）：
//  1. `NewJournalWithStore` 在 store 不支持租约时**不报错**、能成功构造；
//  2. 用它构造出的 Catalog **能过查询租约准入**并规划出 range。
//
// 转红方式（实测）：把 leaseDeltaLog.loadLocked / compact 的 degraded 分支改回
// `return fmt.Errorf("... no durable lease store")`，本用例在 Plan 处以
// `query lease admission rejected` 失败。
func TestJournalWithoutLeaseStore_QueryStillWorks(t *testing.T) {
	store := &bareStore{inner: catalog.NewMemJournal()}
	// 前提：这套 store 确实不提供租约能力（否则本用例测的不是降级路径）。
	if _, ok := any(store).(catalog.LeaseStoreProvider); ok {
		t.Fatal("夹具前提：bareStore 不应实现 LeaseStoreProvider")
	}
	journal, err := catalog.NewJournalWithStore(store)
	if err != nil {
		t.Fatalf("无租约旁路时 journal 构造不得失败（降级而非拒绝）：%v", err)
	}

	stack := Build(journal, nil, "test-build")
	writePartition(t, stack.Catalog, "inst:318", "2026-11-04")

	res := stack.Planner.Plan(query.PlanRequest{
		TargetIDs: []string{"inst:318"},
		TimeRange: query.TimeRange{
			FromUTC: "2026-11-04T00:00:00Z",
			ToUTC:   "2026-11-04T23:59:59Z",
		},
		Budget: query.Budget{Limit: 10},
	})
	if res.Err != nil {
		t.Fatalf("查询租约不可持久化时规划仍必须可用（fail-closed 不该用在可用性辅助机制上）：%v", res.Err)
	}
	if len(res.Ranges) == 0 {
		t.Fatal("必须规划出 range：记录在、数据在，就不能报「查不到」")
	}
}

// TestJournalWithoutLeaseStore_LeaseOpsInMemory 租约增量的内存态自身必须可用（不报错）。
func TestJournalWithoutLeaseStore_LeaseOpsInMemory(t *testing.T) {
	store := &bareStore{inner: catalog.NewMemJournal()}
	journal, err := catalog.NewJournalWithStore(store)
	if err != nil {
		t.Fatalf("journal: %v", err)
	}
	// append / compact / snapshot 三个操作在降级形态下都必须成功并保持一致。
	if err := journal.AppendLeaseDelta(catalog.LeaseDelta{Revision: 1, ViewID: "v-1"}); err != nil {
		t.Fatalf("降级形态下 append 必须成功: %v", err)
	}
	if err := journal.CompactLeaseDeltas([]catalog.LeaseDelta{{Revision: 2, ViewID: "v-2"}}); err != nil {
		t.Fatalf("降级形态下 compact 必须成功: %v", err)
	}
	deltas, err := journal.LeaseDeltaSnapshot()
	if err != nil {
		t.Fatalf("降级形态下 snapshot 必须成功: %v", err)
	}
	if len(deltas) != 1 || deltas[0].ViewID != "v-2" {
		t.Fatalf("compact 后应只剩 v-2，实测 %+v", deltas)
	}
}
