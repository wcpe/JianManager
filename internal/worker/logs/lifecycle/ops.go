package lifecycle

import (
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// Ops 迁移过程中的可注入物理操作。nil 字段表示跳过（纯逻辑推进）。
//
// 调用时机（与 catalog 状态对齐）：
//
//	Drain     进入 DRAINING 后、SNAPSHOTTING 前
//	Snapshot  进入 SNAPSHOTTING 后
//	Copy      进入 STAGING_VERIFY 后（snapshot → staging）
//	Verify    Copy 成功后（staging 校验）
//	Attach    进入 ATTACHED_STAGING 后（VL 挂载 staging；QueryPlanner 仍排除）
//	LeaseDrain OWNER_SWITCHED 后、QUERY_LEASE_DRAINING 中
//	Detach    旧 generation 租约排空后、CLEANED 前
type Ops struct {
	Drain      func(key catalog.PartitionKey) error
	Snapshot   func(key catalog.PartitionKey, fromDir string) (snapshotID string, err error)
	Copy       func(key catalog.PartitionKey, snapshotID, toDir string) (copyID string, err error)
	Verify     func(key catalog.PartitionKey, copyID, toDir string) (checksum string, err error)
	Attach     func(key catalog.PartitionKey, dirID string) error
	LeaseDrain func(key catalog.PartitionKey, oldGeneration uint64) error
	Detach     func(key catalog.PartitionKey, dirID string) error
	// Cleanup runs only after DETACHED is durably recorded.
	Cleanup func(key catalog.PartitionKey, dirID string) error
}

// StateObserver 在关键状态推进后回调（测试可断言 staging 不可查询等）。
type StateObserver func(key catalog.PartitionKey, rec *catalog.Record)

// RetentionGate last-copy 保护协同钩子。
//
// 契约：move_after_age / online_retention / VL runtime retention 分离；
// 下一层未完成责任接收和校验前，不得删除最后有效副本。
type RetentionGate interface {
	// AllowDetach 报告 fromDir（通常为迁移源 HOT）是否可被 detach/清理。
	// toDir 为已校验的目标侧目录。拒绝时返回 reason。
	AllowDetach(key catalog.PartitionKey, fromDir, toDir string) (ok bool, reason string)
}

// RetentionGateFunc 函数适配。
type RetentionGateFunc func(key catalog.PartitionKey, fromDir, toDir string) (bool, string)

// AllowDetach 实现 RetentionGate。
func (f RetentionGateFunc) AllowDetach(key catalog.PartitionKey, fromDir, toDir string) (bool, string) {
	return f(key, fromDir, toDir)
}

// MigrationResult 一次 StartMigration 的可观测结果。
type MigrationResult struct {
	Record        *catalog.Record
	SnapshotID    string
	CopyID        string
	Checksum      string
	StatesWalked  []catalog.MigrationState
	BlockedReason string
	OwnerSwitched bool
	StagingDirID  string
	FromDirID     string
	OpsCalled     []string
}
