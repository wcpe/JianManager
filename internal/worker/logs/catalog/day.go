package catalog

import (
	"fmt"
	"log/slog"
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

// activeDayRecords 返回该日**仍参与迁移**的记录子集（2026-10-22 复审 P0-1）。
//
// 为什么需要它：混合态 UTC 日必然出现 —— 某日整体下沉到冷层后，迟到的写入或
// 新出现的 storage namespace 仍会在同一天新建一条 `Owner=hot` 的记录
// （migrate_target.go 的注释自己承认这一点）。旧实现里每个日级操作都用
// `records[0].MigrationState` 当基准并要求**全部**记录同态，于是混合态日
// 会在链条的某一步必然报 divergence，而且往往**不写失败 journal** ⇒
// 该日永久不可迁移、写路由却已冻结，最终数据被 VL retention 删除。
//
// 判据：`""`（旧 journal 兼容）与 `CLEANED`（已下沉完成）不参与本次迁移；
// 其余记录必须处于同一状态 —— 那才是真正的「同一次迁移内不同步」，仍须拒绝。
func activeDayRecords(records []*Record) []*Record {
	active := make([]*Record, 0, len(records))
	for _, rec := range records {
		if rec == nil || rec.MigrationState == "" || rec.MigrationState == StateCleaned {
			continue
		}
		active = append(active, rec)
	}
	return active
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
	active := activeDayRecords(records)
	if len(active) == 0 {
		// 没有任何记录在迁移中：幂等成功（调用方可能重试）。
		return records, nil
	}
	from := active[0].MigrationState
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
	// **只切换「迁移中」的记录**（2026-10-22 复审 P0-2 同源）：`ApplyOwnerSwitch` 要求
	// 记录处于 `ATTACHED_STAGING`，而混合态日里已下沉的记录是 `CLEANED` ⇒ 对全部记录
	// 调用必然失败，导致该日永远切不过去。已完成的记录跳过即可（它已经是目标 owner）。
	active := activeDayRecords(records)
	for _, rec := range active {
		projection := projections[rec.Key]
		generation := rec.TargetGeneration
		if generation == 0 {
			generation = rec.MigrationFromGeneration + 1
		}
		if err := ApplyOwnerSwitch(rec, target, targetDirID, generation, projection); err != nil {
			return nil, err
		}
	}
	if len(active) == 0 {
		return records, nil
	}
	return c.commitBatchLocked(active, JournalEntry{Key: active[0].Key, State: StateOwnerSwitched,
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
	// **不得把「从未参与迁移」的记录一并结账**（2026-10-22 复审 P1-2）。
	//
	// 混合态日有两种非法残留，都必须在这里拦住：
	//   · `Owner=hot` 且 `MigrationState=""`   —— 在 `BeginDayMigration` 之后才出现
	//     （迟到写入 / 新 storage namespace），从未进入迁移；
	//   · 任何 `Owner=hot` 且已进入迁移的记录 —— 迁移没走完就来结账。
	//
	// 旧实现无条件下针对**整日全部**记录置 `JournalComplete/LastVerifyCompleted`：
	// 一旦该日走到这里，那条 hot 记录就被标成「已完成」，此后所有日级操作
	// （`activeDayRecords` 按 MigrationState 过滤）都把它当作不参与，
	// 而它的热分区会在 VL retention 到期时被直接删除 ⇒ 该 namespace 该日数据永久丢失，
	// 且因为 `JournalComplete=true`，任何一致性检查都不会再报警。
	//
	// 为什么要在这里拦而不是只在 `BeginDayMigration` 拦：这个缺口是**两套判据不一致**
	// 造成的（Begin 按 Owner 纳入、后续按 MigrationState 排除），只补一边会在另一边漏。
	for _, rec := range records {
		if rec.Owner == OwnerHot {
			// **必须留痕，否则这是无出口的死循环**（2026-10-22 终审 P2）。
			//
			// 调用链：`MarkDayComplete` 拒绝 → `Resume` 的 `fail()` → `FAILED_RETRYABLE`
			// → 下一轮 sweep 又从 `FAILED_RETRYABLE` 恢复到 `DETACHED` → 再次拒绝。
			// `retention.Mover` 的失败只记 `KeptOriginal`，既不重试计数也不升级到
			// `FAILED_MANUAL` ⇒ 每轮 sweep 追加 2 条 journal、重跑 Cleanup，永不收敛。
			// 守卫本身是对的（比静默丢数据好得多），但**拒绝必须可观测**，
			// 否则运维只看到「这个源一直在失败」却查不到原因。
			slog.Error("拒绝结账：该 UTC 日仍残留 hot 记录（迁移未覆盖它）",
				"utcDay", utcDay, "key", rec.Key.String(), "owner", rec.Owner,
				"migrationState", rec.MigrationState,
				"hint", "对该日重新执行 BeginDayMigration 以把该记录纳入迁移；或确认它可被丢弃后人工清理")
			return fmt.Errorf("%w: UTC day %s still has hot record %s (state=%q); "+
				"refuse to mark journal complete", ErrIllegalTransition, utcDay, rec.Key, rec.MigrationState)
		}
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
	// **只在迁移中的子集上打失败标**（同上）：混合态日里已下沉记录是 CLEANED，
	// 若要求全一致，失败本身都记不下来 ⇒ 现场连「为什么失败」都查不到。
	active := activeDayRecords(records)
	if len(active) == 0 {
		// **不能返回成功**（2026-10-22 复审 P2-8）：`FailDay` 的契约是「把失败记下来」，
		// 没有任何可失败的记录意味着这次调用**什么也没做**，返回 nil 会让调用方
		// （`DayManager.Resume` 的 `fail()` 闭包）误以为失败已入账，而现场查不到任何记录。
		// 与 `AdvanceDay`/`BeginDayMigration` 的空子集语义不同：那两个是幂等推进，
		// 空子集本来就等于「已完成」，返回成功是诚实的。
		return nil, fmt.Errorf("%w: UTC day %s has no record participating in migration (state=%q)",
			ErrIllegalTransition, utcDay, to)
	}
	from := active[0].MigrationState
	for _, rec := range active {
		if rec.MigrationState != from {
			return nil, fmt.Errorf("%w: UTC-day records have divergent states", ErrInvalidState)
		}
		if err := Transition(rec, to); err != nil {
			return nil, err
		}
		rec.RecoveryRequired = true
	}
	return c.commitBatchLocked(active, JournalEntry{Key: active[0].Key, State: to, FromState: from,
		Detail: detail, AtUnixMilli: time.Now().UnixMilli()})
}
