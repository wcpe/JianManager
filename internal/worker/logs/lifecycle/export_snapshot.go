package lifecycle

import (
	"fmt"
	"path/filepath"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// ExportSnapshot 为「导出归档副本」提供冻结快照目录。
//
// 它是 lifecycle 与 logs/archive 之间唯一的接缝：archive 只认「一个装着源文件的目录」，
// 而「快照怎么取、能读哪里」属于物理布局，必须留在这里。装配层把它注入
// `archive.NewArchiveCopyExporter` 的 Snapshot 字段。
//
// 顺序固定且不可调换：
//  1. 取冻结快照（`snapshot` 内部先做 `CheckPhysicalDayDrained` 双闸、再 force_flush，
//     最后要求 VL 返回**恰好一个**位于受管 snapshots 根下的稳定目录）；
//  2. 复核路径边界：调用方接下来会 WalkDir 读取该目录下每一个文件，
//     这道校验是「读哪里」的唯一边界。
//
// 这里**不**自己冻结/解冻：冻结必须横跨「快照 → 归档标记 → 删除/回滚」全过程，
// 由保留策略执行器在导出前建立、在未归档的退出路径上解除。
func (p *VLPartitionOps) ExportSnapshot(storageNamespace, utcDay string) (string, error) {
	if p == nil || p.Catalog == nil {
		return "", fmt.Errorf("lifecycle: 导出快照未装配（缺少 Catalog）")
	}
	catalogKey := catalog.PartitionKey{StorageNamespace: storageNamespace, UTCDay: utcDay}
	dir, err := p.snapshot(catalogKey, "")
	if err != nil {
		return "", fmt.Errorf("lifecycle: 冻结快照失败（拒绝导出未冻结的分区）：%w", err)
	}
	if err := requirePathUnder(filepath.Join(p.HotDataRoot, "partitions"), dir); err != nil {
		return "", err
	}
	return dir, nil
}
