package service

import (
	"math"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 组存储用量口径。
//
// ## 为什么必须抽成一个函数
//
// 存储用量有两个消费方向，**必须永远一致**：
//   - 创建期检查（instance.go 的 Create 事务）读 GetAllStorageUsage 那一套口径；
//   - 运行期强制（quota.go 的 groupDiskFreeMB）按余量派生单实例磁盘限额。
//
// 两边口径一旦漂移，就会出现「创建时按备份估算判通过、运行期按真实占用判超限」——
// 实例刚建好就被强制停服。此前两处各写一遍 SUM(file_size_mb)，正是这种漂移的温床；
// 本函数是唯一实现，两处都调它。
//
// ## 口径（逐实例二选一，不是整组二选一）
//
//	实例曾成功上报过工作目录大小 → 用 work_dir_bytes
//	否则                          → 用该实例的备份文件大小之和（回退估算）
//
// **回退是逐实例的**，不是「全组都能实时读才用实时值」。理由：组内常混着
// 「刚建的新实例（还没跑够一拍心跳）」与「老实例」，整组二选一会让新实例把整组的
// 精度拖回备份估算，白白丢掉已有的真实读数。
//
// ## 回退为什么不能用 0
//
// work_dir_bytes_available=false 的语义是「不知道占多少」，不是「占用为 0」。
// 按 0 计入会**低估**用量 → 创建期检查放行 → 超卖。用备份大小（一个真实存在的
// 磁盘占用量）做下界，方向上是保守的：宁可高估让用户提前扩容，不可低估让磁盘写爆。
//
// 注意方向性局限：备份大小不是工作目录大小的上界（没备份过的实例回退值是 0）。
// 这是既有口径的固有精度，本 FR 只负责「有实时值就用实时值」，不假装能消除它。
func groupUsedStorageMB(db *gorm.DB, groupID uint) (float64, error) {
	if groupID == 0 {
		return 0, nil
	}
	var rows []struct {
		InstanceID       uint
		WorkDirBytes     int64
		WorkDirAvailable bool
	}
	if err := db.Model(&model.Instance{}).
		Select("instances.id as instance_id, instances.work_dir_bytes as work_dir_bytes, "+
			"instances.work_dir_bytes_available as work_dir_available").
		Joins("JOIN group_instances ON group_instances.instance_id = instances.id").
		Where("group_instances.group_id = ?", groupID).
		Scan(&rows).Error; err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}

	// 需要回退的实例：一次 IN 查询取它们的备份大小之和，避免 N 次小查询。
	fallbackIDs := make([]uint, 0, len(rows))
	totalBytes := int64(0)
	for _, r := range rows {
		if r.WorkDirAvailable && r.WorkDirBytes >= 0 {
			// 负值不可能来自正常 Worker（applyWorkDirUsages 会拦），但若历史数据/外部
			// 直改库留下负值，按 0 计比让总量变负安全——总量变负会让「已用量」比实际小。
			if r.WorkDirBytes > 0 {
				totalBytes += r.WorkDirBytes
			}
			continue
		}
		fallbackIDs = append(fallbackIDs, r.InstanceID)
	}

	var fallbackMB float64
	if len(fallbackIDs) > 0 {
		var sum struct{ Total float64 }
		if err := db.Model(&model.Backup{}).
			Select("COALESCE(SUM(file_size_mb), 0) as total").
			Where("instance_id IN ?", fallbackIDs).
			Scan(&sum).Error; err != nil {
			return 0, err
		}
		fallbackMB = sum.Total
	}

	// 字节 → MB。向上取整：0.3 MB 的真实占用按 0 计会让用量永远差一截，
	// 攒够几十个实例就是明显的低估。向上取整只会让判定偏保守，方向上安全。
	liveMB := float64(totalBytes) / (1024 * 1024)
	return math.Ceil(liveMB) + fallbackMB, nil
}
