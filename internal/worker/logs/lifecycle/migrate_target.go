package lifecycle

import (
	"fmt"
	"strings"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// MigrateAction 是一次「分区下沉」请求在 Catalog 上应执行的动作。
type MigrateAction int

const (
	// MigrateActionStart 表示该 UTC 日尚未下沉，从状态机起点开始搬运。
	MigrateActionStart MigrateAction = iota
	// MigrateActionResume 表示该日已有在途迁移，续跑而不是重新开始。
	MigrateActionResume
	// MigrateActionSkip 表示该日已在冷层（或已完成清理），重复调用幂等成功。
	MigrateActionSkip
)

// ResolveMigrateTarget 判定 (storageNamespace, utcDay) 这次下沉请求该做什么。
//
// 为什么探针要分流，而不是一律按 (命名空间, 日) 精确查 Catalog：
//
//   - 真正被搬运的单位是**整天的物理分区**，不是单个逻辑命名空间 —— DayManager 的注释即
//     「migrates the physical UTC-day partition shared by every logical storage namespace」，
//     Start → BeginDayMigration + RecordsForDay 一次性批量推进该日全部命名空间。因此
//     storageNamespace 在本 RPC 里只是「这次调用被允许开始」的存在性探针，不是搬运范围。
//
//   - 节点级面板（NodeLogRuntimePanel）发的是 `node:<节点ID>`：它没有实例上下文，也不该有 ——
//     迁移既然是整天级的，让运维在实例里挑一个只是挑了一个探针，反而会误导成「只迁这个实例」。
//     而 `node:<id>` 形态在既有约定里本来就是节点维度目标（CP 的 archiveQueryBase 默认值、
//     authorizeArchiveTarget 的合法形态、config.go 里 storage_namespace 的白名单）。
//
//   - 但 catalog 里 `node:<id>` 分区**只在该节点配置了 log_sources 节点自身日志源时才存在**
//     （configs/worker.yml 的出厂示例整段是注释掉的）。默认部署只有 `inst:<id>` 分区 ⇒ 精确键
//     必然落空 ⇒ 前端那个按钮稳定 409 「is not in Catalog」，功能等于不可用。
//
// 所以：精确键命中时行为**逐字不变**（`inst:<id>` 与真配了 `node:` 源的部署零变化）；
// 只有节点维度目标才退化为「本节点该日是否已有分区」的日级探针。其余形态不放大 ——
// 认不出的命名空间仍然报错，避免把拼错的键静默变成「迁了一整天」。
func ResolveMigrateTarget(cat *catalog.Catalog, storageNamespace, utcDay string) (MigrateAction, error) {
	if cat == nil {
		// 没有 Catalog 就无从判断探针，交由上层按原有错误面拒绝。
		return MigrateActionStart, fmt.Errorf("lifecycle: catalog is unavailable")
	}
	key := catalog.PartitionKey{StorageNamespace: storageNamespace, UTCDay: utcDay}
	if rec, ok := cat.Get(key); ok {
		return migrateActionFor(rec), nil
	}
	if !isNodeScopedNamespace(storageNamespace) {
		return MigrateActionStart, notInCatalogErr(key)
	}
	records := cat.RecordsForDay(utcDay)
	if len(records) == 0 {
		// 该日整节点都没有分区：与精确键落空同一错误面（CP 侧稳定映射为 409）。
		return MigrateActionStart, notInCatalogErr(key)
	}
	// 与 DayManager 同口径取代表记录（records 已按 key 排序，首条即代表）：
	// 日级迁移要求该日全部记录状态一致，代表记录的状态就是这次请求该看到的真实状态。
	return migrateActionFor(records[0]), nil
}

// migrateActionFor 按单条记录推导动作，与改造前日志迁移闭包的三分支逐条对齐。
func migrateActionFor(rec *catalog.Record) MigrateAction {
	if rec == nil {
		return MigrateActionStart
	}
	if rec.Owner == catalog.OwnerCold {
		// 已在冷层：重复调用必须幂等成功，否则重试会把「已经好了」报成失败。
		return MigrateActionSkip
	}
	if rec.MigrationState != "" && rec.MigrationState != catalog.StateCleaned && rec.TargetOwner.Valid() {
		// 在途迁移：续跑。重新 Start 会与状态机的「不允许重开在途迁移」冲突并污染状态。
		return MigrateActionResume
	}
	return MigrateActionStart
}

// isNodeScopedNamespace 报告命名空间是否是节点维度目标（`node:<节点ID>` / `node:<节点UUID>`）。
//
// 只做形态识别，不在这里校验「是不是本节点」：CP 侧 authorizeArchiveTarget 已把 `node:`
// 限定为路由节点自身（跨节点在 CP 就 403 且不下发 RPC），Worker 侧再判一次既拿不到权威节点身份，
// 也与 `inst:<id>` 的既有做法（Worker 只把它当本地键查 Catalog）不一致。
func isNodeScopedNamespace(namespace string) bool {
	rest, ok := strings.CutPrefix(strings.TrimSpace(namespace), "node:")
	return ok && strings.TrimSpace(rest) != ""
}

// notInCatalogErr 保持改造前的错误文案：CP 侧按「Worker 返回失败」映射为 409 MIGRATION_FAILED，
// 运维与既有排查手册认的就是这句 "is not in Catalog"。
func notInCatalogErr(key catalog.PartitionKey) error {
	return fmt.Errorf("log partition %s is not in Catalog", key)
}
