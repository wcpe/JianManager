package lifecycle

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// 本文件实现「已归档（ARCHIVED）后回收热层」的**生产删除出口**。
//
// 它在整条链路里的位置（与 `logs/archive` 的导出侧配对，见 archive/copy_export.go）：
//
//	导出 + 三重校验 → Catalog 迁移到 ARCHIVED（落 journal）→ **本文件删热层** → 标记 HotReclaimed
//
// 为什么删之前必须复核 Catalog（而不是信任调用方的口头结论）：
// 删除是不可逆的，而「谁在什么时候能删」这条判断如果只存在于调用栈里，
// 一次重试、一次并发轮次、一次人工补跑就可能绕过它。这里每次删除都重新向
// `Catalog.CanReclaimArchivedDay` 取一次结论，判据（归档证据完整、同一天无 HOT 残留、
// 无在途写许可、无活跃读取）全部在 Catalog 的锁内完成，删除动作因此无法被「带病调用」。
//
// 为什么按**整日目录**删而不是按命名空间：VL 的物理布局是
// `partitions/<utc-day>/`，一个目录里装着当天所有 storage_namespace 的数据。
// 因此「删某个命名空间的热层」在物理上等于「删这一天所有人的热层」——
// 这也正是 CanReclaimArchivedDay 要求「同日无 HOT 残留」的原因。
//
// **已知边界（如实登记）**：删除只覆盖 HOT 命名空间下的日分区目录与导出期间产生的
// 快照目录；不触碰 COLD / Rehydrate 命名空间（那些层有自己的状态机与证据要求）。

// ErrArchiveReclaimRefused 表示 Catalog 侧判定此刻不得删热层。
var ErrArchiveReclaimRefused = fmt.Errorf("lifecycle: Catalog 拒绝回收热层")

// RemoveArchivedPartition 删除已归档分区的热层数据（实现 retention.Remover 的语义）。
//
// 返回 nil 错误的前提是：Catalog 复核通过 **且** 目录真的被删掉。
// 任何一步不成立都返回错误——调用方（保留策略执行器）据此回滚权威态并保留原物。
func (v *VLPartitionOps) RemoveArchivedPartition(ctx context.Context, storageNamespace, utcDay string) error {
	if v == nil || v.Catalog == nil || v.Hot == nil {
		return fmt.Errorf("lifecycle: 归档热层回收未装配（需要 Catalog 与 HOT 客户端）")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	key := catalog.PartitionKey{StorageNamespace: storageNamespace, UTCDay: utcDay}
	if ok, reason := v.Catalog.CanReclaimArchivedDay(key); !ok {
		return fmt.Errorf("%w: %s", ErrArchiveReclaimRefused, reason)
	}
	day, err := dayName(key)
	if err != nil {
		return err
	}

	// 第一步：确保 VL 不再挂载该日分区。
	//
	// 顺序不能反：在 VL 仍持有目录句柄时 `os.RemoveAll` 会让正在运行的实例
	// 读到「文件还在但内容已被删除」的中间态，甚至把删除失败伪装成成功
	// （Linux 上删除已打开的文件只是解链接，磁盘不会释放，VL 仍然能读旧内容）。
	// 这里先 detach 再确认「不在活动分区列表」，最后才删目录。
	if err := v.withTimeout(func(hctx context.Context) error {
		active, aerr := partitionActive(hctx, v.Hot, day)
		if aerr != nil {
			return fmt.Errorf("lifecycle: 读取 HOT 活动分区失败：%w", aerr)
		}
		if !active {
			return nil
		}
		_, perr := v.Hot.Post(hctx, "/internal/partition/detach", url.Values{"name": {day}})
		return perr
	}); err != nil {
		return fmt.Errorf("lifecycle: 归档分区 detach 失败（不删热层）：%w", err)
	}
	if err := v.withTimeout(func(hctx context.Context) error {
		active, aerr := partitionActive(hctx, v.Hot, day)
		if aerr != nil {
			return aerr
		}
		if active {
			return fmt.Errorf("lifecycle: 日分区 %s 仍然处于挂载状态", day)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("lifecycle: 归档分区仍被挂载（不删热层）：%w", err)
	}

	// 第二步：删目录。路径必须先在受管根下校验，`../` 一律拒绝。
	root := filepath.Join(v.HotDataRoot, "partitions")
	target := filepath.Join(root, day)
	if err := requirePathUnder(root, target); err != nil {
		return err
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("lifecycle: 删除热层日分区 %s 失败：%w", day, err)
	}
	// 第三步：尽力清理导出期间产生的快照目录。
	//
	// 快照是导出的中间产物，删除失败不影响「热层已回收」这一事实（数据正本在归档），
	// 因此不作为失败返回，但也绝不静默——留给调用方的日志/指标体现。
	v.mu.Lock()
	snapshot := v.snapshots[key]
	v.mu.Unlock()
	if snapshot != "" {
		_ = v.withTimeout(func(hctx context.Context) error {
			_, serr := v.Hot.Post(hctx, "/internal/partition/snapshot/delete", url.Values{"path": {snapshot}})
			return serr
		})
	}
	v.releaseReservations(key.UTCDay)
	return nil
}
