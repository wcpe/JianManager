package grpc

import (
	"fmt"
	"time"

	"gorm.io/gorm/clause"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// upsertHostSnapshots 写入分挂载点与整机 top-N 进程快照（FR-513）。
//
// ## 为什么用 upsert 而不是「先删后插」
//
// 先删后插在两次心跳之间会有一瞬间「查询结果为空」，前端轮询正好落在那个窗口
// 就会看到数据闪一下消失。upsert 是原子的，读侧永远看到完整快照。
//
// ## 为什么不清理「已消失的挂载点/进程」
//
// 挂载点被卸载、进程退出后，旧行会留存到 TTL 清理为止（48h）。
// 这是有意的：删除需要先知道「哪些已消失」，那要额外一次全量比对；
// 而读侧本来就按 sampled_at 判鲜度（见 ResourceAttribution 的鲜度过滤），
// 陈旧行不会被展示。用 TTL 兜底比每次心跳做 diff 便宜得多。
//
// ## 失败语义
//
// 返回 error 由调用方记 WARN，**不阻断心跳**：观测数据写失败不该让 CP 认为节点失联。
func (h *ControlPlaneHandler) upsertHostSnapshots(req *workerpb.HeartbeatRequest) error {
	if req == nil || req.NodeUuid == "" {
		return nil
	}
	now := time.Now()

	if len(req.HostMounts) > 0 {
		rows := make([]model.HostMountSnapshot, 0, len(req.HostMounts))
		for _, m := range req.HostMounts {
			if m == nil || m.Mountpoint == "" {
				continue
			}
			rows = append(rows, model.HostMountSnapshot{
				NodeUUID:   req.NodeUuid,
				Mountpoint: m.Mountpoint,
				TotalBytes: m.TotalBytes,
				UsedBytes:  m.UsedBytes,
				UsedPct:    m.UsedPct,
				SampledAt:  now,
			})
		}
		if len(rows) > 0 {
			if err := h.db.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "node_uuid"}, {Name: "mountpoint"}},
				DoUpdates: clause.AssignmentColumns([]string{
					"total_bytes", "used_bytes", "used_pct", "sampled_at",
				}),
			}).Create(&rows).Error; err != nil {
				return fmt.Errorf("写入挂载点快照失败: %w", err)
			}
		}
	}

	if len(req.HostTopProcesses) > 0 {
		rows := make([]model.HostProcessSnapshot, 0, len(req.HostTopProcesses))
		for _, p := range req.HostTopProcesses {
			if p == nil || p.Pid == 0 {
				continue
			}
			rows = append(rows, model.HostProcessSnapshot{
				NodeUUID: req.NodeUuid,
				PID:      p.Pid,
				// comm 已经就是进程名；这里不做任何截断拼接，避免把别的东西混进来
				//（隐私边界：本结构没有 cmdline 字段，见 model 注释）。
				Comm:      p.Comm,
				CPUPct:    p.CpuPct,
				RSSBytes:  p.RssBytes,
				SampledAt: now,
			})
		}
		if len(rows) > 0 {
			if err := h.db.Clauses(clause.OnConflict{
				// 列名必须是 GORM 的实际映射：字段 PID 的默认列名是 p_id（不是 pid）。
				// 写错会得到 "no such column: pid"，而心跳失败只记 WARN——
				// 表现为「数据一直不落库但没人报错」，很难查。
				Columns: []clause.Column{{Name: "node_uuid"}, {Name: "p_id"}},
				DoUpdates: clause.AssignmentColumns([]string{
					"comm", "cpu_pct", "rss_bytes", "sampled_at",
				}),
			}).Create(&rows).Error; err != nil {
				return fmt.Errorf("写入进程快照失败: %w", err)
			}
		}
	}
	return nil
}
