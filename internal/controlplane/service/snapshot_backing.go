package service

import (
	"fmt"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 快照底链（RootBackupID 指向的 Backup 行）可用性的读时派生与校验（B-1 / R5 / R8）。
//
// 与 snapshot.go 同属 SnapshotService，拆出的理由：原文件同时承载「创建」「回滚」
// 「保留裁剪」「启动清扫」「底链校验」五块职责（R26）。本文件只放底链读路径——
// 「这条快照现在到底能不能回滚」的判定，与写入/编排路径天然分层。
// 纯机械搬迁，无行为变化。

// annotateBacking 批量回填底链可用性（一次 IN 查询，避免逐条 N+1）。
//
// 批量查为硬删除（Unscoped）读取：GORM 默认软删过滤会把「已软删的底链」当成
// 不存在于结果集，二者对快照的含义相同（都不可回滚），但显式 Unscoped + 判定
// DeletedAt 能给出更准确的原因文案。
func (s *SnapshotService) annotateBacking(snaps []model.InstanceSnapshot) {
	if len(snaps) == 0 {
		return
	}
	ids := make([]uint, 0, len(snaps))
	seen := make(map[uint]struct{}, len(snaps))
	for i := range snaps {
		id := snaps[i].RootBackupID
		if id == 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	alive := make(map[uint]struct{}, len(ids))
	if len(ids) > 0 {
		var existing []model.Backup
		if err := s.db.Unscoped().Select("id", "deleted_at").
			Where("id IN ? AND deleted_at IS NULL", ids).Find(&existing).Error; err == nil {
			for i := range existing {
				alive[existing[i].ID] = struct{}{}
			}
		} else {
			// 查询失败：保守标记为「未校验」而非「可用」——绝不把未知当成可回滚。
			return
		}
	}
	for i := range snaps {
		switch {
		case !snaps[i].Rollable():
			// 状态本身不可回滚（pending/running/failed）：原因由 state 表达，不叠加底链文案。
			//
			// R8：这里对 `RootBackupID == 0` 也**不再**给「底链缺失」——归档失败（或尚未开始）
			// 的行必然没有底链 ID，但真实原因是归档本身没成功，把它渲染成「底层备份已不存在」
			// 会把排障方向引向错误的假设（去找一条从未被创建过的备份）。
			// 底链字段留空（Unchecked），语义对齐：不可回滚的原因由 state + failureReason 表达。
			snaps[i].RootBackupState = model.SnapshotBackingUnchecked
		case snaps[i].RootBackupID == 0:
			// 状态可回滚却没有底链：这是真正的不一致（归档完成但未回填 / 被人工改库），
			// 明确报「底链缺失」并给出可操作提示。
			snaps[i].RootBackupState = model.SnapshotBackingMissing
			snaps[i].NotRollableReason = "快照未关联底层备份（底链缺失），该快照不可回滚；如需保留请改用其它更早的可回滚快照"
		default:
			if _, ok := alive[snaps[i].RootBackupID]; ok {
				snaps[i].RootBackupState = model.SnapshotBackingOK
				continue
			}
			snaps[i].RootBackupState = model.SnapshotBackingMissing
			snaps[i].NotRollableReason = fmt.Sprintf(
				"底层备份 #%d 已不存在（可能被人工删除或保留策略清理），该快照不可回滚；如需保留请改用其它更早的可回滚快照",
				snaps[i].RootBackupID)
		}
	}
}

// GetRollableByID 取快照并校验其**真的**可回滚（状态 + 底链）。
//
// 回滚入口必须走这里而不是 GetByID+Rollable：底链缺失时给出显式降级原因
// （ErrSnapshotNotRollable + 具体说明），而不是让任务在回放阶段才报 record not found。
func (s *SnapshotService) GetRollableByID(snapshotID uint) (*model.InstanceSnapshot, error) {
	snap, err := s.GetByID(snapshotID)
	if err != nil {
		return nil, err
	}
	if !snap.Rollable() {
		return nil, fmt.Errorf("%w（当前状态 %s）", ErrSnapshotNotRollable, snapshotStateLabel(snap.State))
	}
	list := []model.InstanceSnapshot{*snap}
	s.annotateBacking(list)
	if list[0].NotRollableReason != "" {
		return nil, fmt.Errorf("%w：%s", ErrSnapshotNotRollable, list[0].NotRollableReason)
	}
	return &list[0], nil
}
