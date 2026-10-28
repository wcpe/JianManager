package grpc

import (
	"log/slog"
	"time"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// applyWorkDirUsages 把心跳携带的实例工作目录占用写入 instances 表（FR-003）。
//
// ## 为什么落库而不是查询时现算
//
// 组配额的存储维度要遍历组内全部实例。既有实时链路 GetInstanceResourceSnapshot 是
// **逐实例 gRPC**，直接接上去就是 N 次跨节点 RPC——配额页、组详情、创建期检查
// （service/instance.go 的 Create 事务）三处都会被拖慢。落库后三处都读同一列，
// 读路径退化成一次 SQL 聚合，且「创建时看到的用量」与「运行期强制判定的用量」
// 天然是同一份数据。
//
// ## 写入语义（三条，缺一都会让配额失真）
//
//  1. **available=true**：写 bytes + available=true + 时刻。
//  2. **available=false**：只把 available 置 false，**保留旧的 bytes 不动**。
//     清零会让用量凭空归零（低估 → 超卖）；保留旧值至少是个量级正确的下界，
//     而 available=false 已经告诉配额侧「别信这个数，回退备份估算」。
//  3. **本拍没带该实例**（老 Worker / 新实例首拍未统计）：整行不动。
//     写成 false 会把「从未上报」伪装成「上报了但失败」，两者的回退动作虽然一样，
//     但排障含义完全不同（前者要升级 Worker，后者要查目录权限）。
//
// 失败只记日志、不阻断心跳：配额观测数据不该让存活判定失败。
func (h *ControlPlaneHandler) applyWorkDirUsages(usages []*workerpb.InstanceWorkDirUsage) {
	if len(usages) == 0 {
		return
	}
	now := time.Now()
	for _, u := range usages {
		if u == nil || u.InstanceUuid == "" {
			continue
		}
		var err error
		if u.Available {
			// 负数字节没有物理意义（可能是 Worker 侧溢出/协议错），按不可用处理，
			// 绝不让它进库去做减法（会让组用量比真实值小）。
			if u.WorkDirBytes < 0 {
				slog.Warn("心跳上报的工作目录大小为负，按不可用处理",
					"instanceUUID", u.InstanceUuid, "bytes", u.WorkDirBytes)
				err = h.db.Model(&model.Instance{}).
					Where("uuid = ?", u.InstanceUuid).
					Update("work_dir_bytes_available", false).Error
			} else {
				err = h.db.Model(&model.Instance{}).
					Where("uuid = ?", u.InstanceUuid).
					Updates(map[string]any{
						"work_dir_bytes":           u.WorkDirBytes,
						"work_dir_bytes_available": true,
						"work_dir_bytes_at":        now,
					}).Error
			}
		} else {
			// 只降可用位，保留旧读数（见上文 ②）。
			err = h.db.Model(&model.Instance{}).
				Where("uuid = ?", u.InstanceUuid).
				Update("work_dir_bytes_available", false).Error
		}
		if err != nil {
			slog.Warn("写入实例工作目录占用失败",
				"instanceUUID", u.InstanceUuid, "available", u.Available, "error", err)
		}
	}
}
