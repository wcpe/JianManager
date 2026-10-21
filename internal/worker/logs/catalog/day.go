package catalog

import (
	"fmt"
	"sort"
	"time"
)

func (c *Catalog) dayRecordsLocked(utcDay string) ([]*Record, error) {
	var records []*Record
	for key, rec := range c.records {
		if key.UTCDay == utcDay {
			records = append(records, rec.Clone())
		}
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("%w: UTC day %s", ErrNotFound, utcDay)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Key.String() < records[j].Key.String() })
	return records, nil
}

func (c *Catalog) BeginDayMigration(utcDay string, target Owner, targetDirID string) ([]*Record, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	records, err := c.dayRecordsLocked(utcDay)
	if err != nil {
		return nil, err
	}
	// **跳过已下沉的记录，只推进仍需迁移的子集**（2026-10-21 架构复审 D2）。
	//
	// 旧实现要求「该日每一条记录都是 hot」，遇到一条已 COLD 就整日拒绝。而混合态
	// **必然会发生**：某日整体下沉到冷层之后，迟到的写入或新出现的 storage namespace
	// 仍会在同一天新建一条 Owner=hot 的记录（migrate_target.go 的注释自己承认这一点）。
	// 后果是该日**永久不可迁移**：迁移按钮恒报错，那条 hot 分区到期被 VL 原生删除
	// ⇒ 该 namespace 该日数据永久丢失，UI 上只留一次迁移失败。
	//
	// 语义：已 COLD 的记录是「已完成」而不是「冲突」，跳过即可；只对 hot 子集做
	// BeginMigration。若一条 hot 都没有，说明该日已全部下沉，按幂等成功返回。
	active := make([]*Record, 0, len(records))
	for _, rec := range records {
		if rec.Owner == OwnerCold || rec.Owner == OwnerArchive {
			// 已下沉：本次无事可做，计入 skipped（不报错）。
			continue
		}
		if rec.Owner != OwnerHot {
			return nil, fmt.Errorf("%w: %s owner=%s", ErrIllegalTransition, rec.Key, rec.Owner)
		}
		if err := BeginMigration(rec, target, targetDirID); err != nil {
			return nil, err
		}
		active = append(active, rec)
	}
	if len(active) == 0 {
		// 整日已全部下沉：幂等成功，不做任何提交。
		return records, nil
	}
	return c.commitBatchLocked(active, JournalEntry{Key: active[0].Key, State: StateRoutingFrozen,
		Detail: "begin physical UTC-day migration", AtUnixMilli: time.Now().UnixMilli()})
}

func (c *Catalog) AdvanceDay(utcDay string, to MigrationState) ([]*Record, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	records, err := c.dayRecordsLocked(utcDay)
	if err != nil {
		return nil, err
	}
	// **只推进参与本次迁移的记录**（2026-10-21 架构复审 D2，与 BeginDayMigration 同源）。
	//
	// 旧实现取 `records[0].MigrationState` 当基准并要求全一致，混合态日（部分已 COLD、
	// 部分仍 hot）必然报 `divergent states` ⇒ 迁移永久卡死。而混合态一定会发生
	// （迟到写入/新 namespace 会在已下沉的那天新建 hot 记录）。
	//
	// 判据：只有 `MigrationState != ""` 的记录在迁移中；已 CLEANED / 空态的记录
	// （包括早已下沉完成的）不参与本次推进。子集内部仍要求状态一致——那是真正的
	// 不一致（同一次迁移里各记录不同步），必须继续拒。
	active := make([]*Record, 0, len(records))
	from := MigrationState("")
	for _, rec := range records {
		if rec.MigrationState == "" || rec.MigrationState == StateCleaned {
			continue
		}
		if from == "" {
			from = rec.MigrationState
		}
		active = append(active, rec)
	}
	if len(active) == 0 {
		// 没有任何记录在迁移中：幂等成功（调用方可能重试）。
		return records, nil
	}
	for _, rec := range active {
		if rec.MigrationState != from {
			return nil, fmt.Errorf("%w: UTC-day records have divergent states", ErrInvalidState)
		}
		if err := Transition(rec, to); err != nil {
			return nil, err
		}
		if to == StateSnapshotting && rec.TargetGeneration == 0 {
			rec.TargetGeneration = rec.MigrationFromGeneration + 1
		}
	}
	return c.commitBatchLocked(active, JournalEntry{Key: active[0].Key, State: to, FromState: from,
		Detail: "advance physical UTC-day migration", AtUnixMilli: time.Now().UnixMilli()})
}

func (c *Catalog) RecordDayVerification(utcDay, checksum string) error {
	if checksum == "" {
		return fmt.Errorf("%w: verification checksum required", ErrInvalidState)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	records, err := c.dayRecordsLocked(utcDay)
	if err != nil {
		return err
	}
	for _, rec := range records {
		rec.LastVerifyCompleted = true
		rec.LastVerifyChecksum = checksum
	}
	_, err = c.commitBatchLocked(records, JournalEntry{Key: records[0].Key, State: records[0].MigrationState,
		Detail: "UTC-day staging copy verified", AtUnixMilli: time.Now().UnixMilli()})
	return err
}

func (c *Catalog) SwitchDayOwners(utcDay string, target Owner, targetDirID string, projections map[PartitionKey]*PublishedProjection) ([]*Record, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	records, err := c.dayRecordsLocked(utcDay)
	if err != nil {
		return nil, err
	}
	for _, rec := range records {
		projection := projections[rec.Key]
		generation := rec.TargetGeneration
		if generation == 0 {
			generation = rec.MigrationFromGeneration + 1
		}
		if err := ApplyOwnerSwitch(rec, target, targetDirID, generation, projection); err != nil {
			return nil, err
		}
	}
	return c.commitBatchLocked(records, JournalEntry{Key: records[0].Key, State: StateOwnerSwitched,
		FromState: StateAttachedStaging, AuthorityCommit: true,
		Detail: "UTC-day owner-switched atomic commit", AtUnixMilli: time.Now().UnixMilli()})
}

func (c *Catalog) ClearDayResiduals(utcDay string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	records, err := c.dayRecordsLocked(utcDay)
	if err != nil {
		return err
	}
	for _, rec := range records {
		rec.ResidualDirs = nil
	}
	_, err = c.commitBatchLocked(records, JournalEntry{Key: records[0].Key, State: records[0].MigrationState,
		Detail: "UTC-day residual directories cleared", AtUnixMilli: time.Now().UnixMilli()})
	return err
}

func (c *Catalog) MarkDayComplete(utcDay, checksum string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	records, err := c.dayRecordsLocked(utcDay)
	if err != nil {
		return err
	}
	for _, rec := range records {
		rec.JournalComplete = true
		rec.LastVerifyCompleted = true
		rec.LastVerifyChecksum = checksum
	}
	_, err = c.commitBatchLocked(records, JournalEntry{Key: records[0].Key, State: records[0].MigrationState,
		JournalComplete: true, Detail: "UTC-day journal complete", AtUnixMilli: time.Now().UnixMilli()})
	return err
}

func (c *Catalog) FailDay(utcDay string, to MigrationState, detail string) ([]*Record, error) {
	if to != StateFailedRetryable && to != StateFailedManual {
		return nil, fmt.Errorf("%w: invalid day failure state", ErrIllegalTransition)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	records, err := c.dayRecordsLocked(utcDay)
	if err != nil {
		return nil, err
	}
	from := records[0].MigrationState
	for _, rec := range records {
		if rec.MigrationState != from {
			return nil, fmt.Errorf("%w: UTC-day records have divergent states", ErrInvalidState)
		}
		if err := Transition(rec, to); err != nil {
			return nil, err
		}
		rec.RecoveryRequired = true
	}
	return c.commitBatchLocked(records, JournalEntry{Key: records[0].Key, State: to, FromState: from,
		Detail: detail, AtUnixMilli: time.Now().UnixMilli()})
}
