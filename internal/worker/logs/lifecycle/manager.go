package lifecycle

import (
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// Manager 是 Catalog 的分区旁路门面（路由 / 查询归属 / 恢复）。
//
// 2026-10-29：冷热双层已废除，迁移状态机（StartMigration / ResumeMigration / DayManager）
// 连同 `Ops` 的物理实现一起删除。`SetOps` / `SetRetentionGate` 保留是因为装配层仍会调用它们，
// 但已没有任何路径会触发物理搬迁。
type Manager struct {
	cat      *catalog.Catalog
	ops      Ops
	ret      RetentionGate
	runtime  Runtime
	observer StateObserver
}

// New 创建 Manager。cat 为 nil 时使用内存 Catalog（测试）。
func New(cat *catalog.Catalog, ops Ops) *Manager {
	if cat == nil {
		cat = catalog.New(nil)
	}
	return &Manager{cat: cat, ops: ops}
}

// Catalog 返回底层 Catalog。
func (m *Manager) Catalog() *catalog.Catalog { return m.cat }

// SetOps 注入/替换物理操作。
func (m *Manager) SetOps(ops Ops) { m.ops = ops }

// SetRetentionGate 注入 last-copy 保护钩子。
func (m *Manager) SetRetentionGate(g RetentionGate) { m.ret = g }

// SetRuntime 注入 VL 薄运行时（可选）。
func (m *Manager) SetRuntime(rt Runtime) { m.runtime = rt }

// SetObserver 注入状态观察回调（测试）。
func (m *Manager) SetObserver(fn StateObserver) { m.observer = fn }

// PutPartition 登记稳定分区（HOT 权威）。
func (m *Manager) PutPartition(key catalog.PartitionKey, owner catalog.Owner, generation uint64, dirID string) error {
	return m.cat.Put(catalog.NewStableRecord(key, owner, generation, dirID))
}

// Recover 启动恢复：journal 重放 + 每条记录逻辑恢复。
func (m *Manager) Recover() map[catalog.PartitionKey]catalog.RecoveryView {
	return m.cat.StartingRecover()
}

// RouteLateEvent 按 Catalog owner/generation 路由迟到事件。
// eventUTCDay 仅审计对照；禁止据此重新生成 HOT 权威分区。
func (m *Manager) RouteLateEvent(storageNamespace, utcDay, eventUTCDay string) (catalog.WriteTarget, bool) {
	key := catalog.PartitionKey{StorageNamespace: storageNamespace, UTCDay: utcDay}
	return m.cat.RouteWrite(key, eventUTCDay)
}

// QueryOwner 返回 QueryPlanner 唯一权威。
func (m *Manager) QueryOwner(storageNamespace, utcDay string) (catalog.QueryRef, bool) {
	key := catalog.PartitionKey{StorageNamespace: storageNamespace, UTCDay: utcDay}
	return m.cat.OwnerForQuery(key)
}

// IsDirQueryable 报告目录是否允许进入 QueryPlanner（残留 re-attach 过滤）。
func (m *Manager) IsDirQueryable(storageNamespace, utcDay, dirID string) bool {
	key := catalog.PartitionKey{StorageNamespace: storageNamespace, UTCDay: utcDay}
	return m.cat.IsQueryDirVisible(key, dirID)
}

// GetPartition 返回分区记录副本。
func (m *Manager) GetPartition(storageNamespace, utcDay string) (*catalog.Record, bool) {
	key := catalog.PartitionKey{StorageNamespace: storageNamespace, UTCDay: utcDay}
	return m.cat.Get(key)
}
