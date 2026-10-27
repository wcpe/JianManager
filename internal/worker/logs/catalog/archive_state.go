package catalog

import (
	"fmt"
	"sort"
	"time"
)

// 本文件实现「已归档（ARCHIVED）」权威态的迁移、回滚与热层回收闸门。
//
// 解决的具体问题：导出归档（`archive_copy`）此前只能在「导出 + 校验通过」后**保留原物**，
// 热层永不释放。原因不是不敢删，而是 Catalog 侧没有「已归档」这一权威态——
// 删掉一个仍属 HOT 权威的日分区，会让查询规划器指向一个已不存在的目录
// （数据在查询面上失联，且表现为「查不到」而非「坏了」）。
//
// 语义（单向、可审计）：
//
//	HOT --(导出 + 逐文件回读校验 + 三闸全开)--> ARCHIVED --(物理删除热层)--> ARCHIVED+HotReclaimed
//
// 三条硬边界：
//  1. **只有稳定 HOT 权威能进入 ARCHIVED**；在途迁移、失败态、已有归档证据的分区一律拒绝。
//  2. 证据不完整（无 provider/prefix/checksum、或对象数为 0）一律拒绝：
//     一份「空归档」绝不能成为删除一天的依据。
//  3. 回滚只在**热层尚未回收**时可行；已回收后再回滚等于凭空声明「HOT 有数据」，是伪造。

// ErrPartitionArchived 表示该分区的正本已在归档、不再接受热层写入。
var ErrPartitionArchived = fmt.Errorf("%w: partition archived", ErrCatalog)

// ErrArchiveEvidenceMissing 表示缺少可核验的归档证据。
var ErrArchiveEvidenceMissing = fmt.Errorf("%w: archive evidence missing or incomplete", ErrCatalog)

// Validate 校验归档证据是否足以支撑「热层可回收」这一判断。
//
// 校验口径刻意从严（宁可拒绝，不可少一项）：对象数为 0、coverage 未声明完整、
// manifest 校验和为空，都意味着「我们并不真的知道归档里有这一天」。
// 它同时校验回滚依据（迁移前权威快照）——已落库的记录必须两半齐全，
// 因为「能删」与「删失败能退回去」是同一套安全论证的两面。
func (e *ArchiveEvidence) Validate() error {
	if err := e.validateExported(); err != nil {
		return err
	}
	if e.PrevOwner == "" || e.PrevDirID == "" {
		return fmt.Errorf("%w: 缺少迁移前权威快照（回滚依据）", ErrArchiveEvidenceMissing)
	}
	return nil
}

// validateExported 只校验**导出侧提供**的那一半证据。
//
// 单独拆出来是因为时序：迁移函数先校验导出侧（调用方给的），
// 再补上迁移前权威快照；若一开始就做全量校验，一个尚未填好回滚依据的
// 合法调用会被误报成「证据不完整」，把正确的调用顺序变成假失败。
func (e *ArchiveEvidence) validateExported() error {
	if e == nil {
		return ErrArchiveEvidenceMissing
	}
	if e.ProviderKind == "" {
		return fmt.Errorf("%w: 缺少归档目的地类型（provider kind）", ErrArchiveEvidenceMissing)
	}
	if e.ObjectPrefix == "" {
		return fmt.Errorf("%w: 缺少归档对象前缀（object prefix）", ErrArchiveEvidenceMissing)
	}
	if e.ManifestChecksum == "" {
		return fmt.Errorf("%w: 缺少 manifest 校验和", ErrArchiveEvidenceMissing)
	}
	if !e.Coverage.Complete {
		return fmt.Errorf("%w: 归档覆盖未声明完整", ErrArchiveEvidenceMissing)
	}
	if e.Coverage.ObjectCount <= 0 {
		return fmt.Errorf("%w: 归档对象数为 0（空归档不得作为回收热层的依据）", ErrArchiveEvidenceMissing)
	}
	if e.ArchivedAt.IsZero() {
		return fmt.Errorf("%w: 缺少归档时刻", ErrArchiveEvidenceMissing)
	}
	return nil
}

// ApplyArchiveTransition 把稳定 HOT 分区切换为 ARCHIVED 权威态（纯逻辑，不写 journal）。
//
// 幂等语义（由调用方据此判定是否落 journal）：
//   - 已是 ARCHIVED 且 (provider, prefix) 一致 ⇒ 返回 already=true，**不改任何字段**；
//   - 已是 ARCHIVED 但归档位置不同 ⇒ 报错（一天两份归档必须人工裁定，不能后者覆盖前者）。
//
// 切换后的关键字段：
//   - Owner=archive、OwnerDirID=归档标识（**不再是**热层目录）；
//   - PublishedProjection=nil（旧投影指向即将被删的热层目录，留着就会产出读它的租约）；
//   - WriteRoute 冻结且无 late 目标（归档后不存在合法的迟到写入目的地）；
//   - 原热层目录登记进 RetiredHotDirs（确保 re-attach 也进不了 QueryPlanner）。
func ApplyArchiveTransition(rec *Record, ev ArchiveEvidence, now time.Time) (already bool, err error) {
	if rec == nil {
		return false, fmt.Errorf("%w: nil record", ErrInvalidState)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	// 先补全归档时刻再校验：调用方允许传零值时间（表示「以本次提交时刻为准」），
	// 否则一个纯时序参数会被误报成「证据不完整」。
	ev.ArchivedAt = now.UTC()
	if err := ev.validateExported(); err != nil {
		return false, err
	}

	if rec.Owner == OwnerArchive {
		cur := rec.ArchiveEvidence
		if cur == nil {
			// 归档态但无证据：权威与证据不自洽，只能人工处置，绝不自动「补证据」。
			return false, fmt.Errorf("%w: 记录已是 archive 权威但缺少归档证据", ErrInvalidState)
		}
		if cur.ProviderKind == ev.ProviderKind && cur.ObjectPrefix == ev.ObjectPrefix {
			return true, nil
		}
		return false, fmt.Errorf("%w: 分区已归档到 %s/%s，拒绝改挂到 %s/%s",
			ErrInvalidState, cur.ProviderKind, cur.ObjectPrefix, ev.ProviderKind, ev.ObjectPrefix)
	}
	if rec.Owner != OwnerHot {
		return false, fmt.Errorf("%w: 仅 HOT 权威可迁移到 ARCHIVED（当前 %q）", ErrIllegalTransition, rec.Owner)
	}
	if rec.MigrationState != "" && rec.MigrationState != StateCleaned {
		return false, fmt.Errorf("%w: 在途迁移状态 %s 下不得归档", ErrIllegalTransition, rec.MigrationState)
	}
	if rec.RecoveryRequired {
		return false, fmt.Errorf("%w: 存在待恢复标记的分区不得归档", ErrInvalidState)
	}
	if rec.ActiveLeaseCount(now) > 0 {
		// 有活跃读取租约时改权威会把「正在读的位置」掀掉：先让租约自然到期，下一轮再来。
		return false, fmt.Errorf("%w: 仍有活跃查询租约（%d）", ErrInvalidState, rec.ActiveLeaseCount(now))
	}

	hotDirID := rec.OwnerDirID
	if hotDirID == "" {
		return false, fmt.Errorf("%w: HOT 权威缺少目录登记", ErrInvalidState)
	}
	ev.PrevOwner = rec.Owner
	ev.PrevDirID = hotDirID
	ev.PrevGeneration = rec.Generation
	ev.PrevProjection = (&Record{PublishedProjection: rec.PublishedProjection}).Clone().PublishedProjection
	ev.HotReclaimed = false
	ev.HotReclaimedAt = time.Time{}
	if prev := rec.ArchiveEvidence; prev != nil {
		// 历史回滚计数必须延续：它记录的是「这个分区被退回过几次」，不能因新一轮归档归零。
		ev.RollbackCount = prev.RollbackCount
		ev.LastRollbackReason = prev.LastRollbackReason
	}

	rec.Owner = OwnerArchive
	rec.OwnerDirID = ev.ArchiveAuthorityID()
	rec.PublishedProjection = nil
	rec.MigrationState = StateCleaned
	rec.JournalComplete = true
	rec.RecoveryRequired = false
	rec.WriteRoute = WriteRoute{
		Owner:      OwnerArchive,
		Generation: rec.Generation,
		DirID:      rec.OwnerDirID,
		Frozen:     true,
		// 归档后不存在合法的 late 目标：数据正本已在归档，迟到写入只能被拒绝。
		LateOwner: "",
		LateDirID: "",
	}
	rec.ArchiveEvidence = ev.Clone()
	// 原热层目录转为「已退役」：即使 VL 重新 attach，也不得进入 QueryPlanner。
	rec.Dirs = removeDirByID(rec.Dirs, hotDirID)
	rec.RetiredHotDirs = append(rec.RetiredHotDirs, PhysicalDir{ID: hotDirID, Role: DirResidual})
	return false, nil
}

// RevertArchiveTransition 把 ARCHIVED 回滚为迁移前的 HOT 权威（纯逻辑，不写 journal）。
//
// 触发场景：归档已登记、但热层回收失败（删目录失败、租约未排空、目的地不可写等）。
// 回滚必须把**查询能力原样还回去**：恢复 Owner/OwnerDirID/Generation 与迁移前的
// PublishedProjection，否则「回滚成功」却读不到热层数据，等于把数据藏起来。
//
// 已回收（HotReclaimed=true）后拒绝回滚：那时热层数据已经不存在，
// 把权威改回 HOT 会让查询面指向空目录——比保持 ARCHIVED 更危险。
func RevertArchiveTransition(rec *Record, reason string, now time.Time) error {
	if rec == nil {
		return fmt.Errorf("%w: nil record", ErrInvalidState)
	}
	ev := rec.ArchiveEvidence
	if rec.Owner != OwnerArchive || ev == nil {
		return fmt.Errorf("%w: 记录不在 ARCHIVED 权威态，无从回滚", ErrIllegalTransition)
	}
	if ev.HotReclaimed {
		return fmt.Errorf("%w: 热层已回收，不得回滚为 HOT（数据已不在热层）", ErrIllegalTransition)
	}
	if ev.PrevOwner != OwnerHot || ev.PrevDirID == "" {
		return fmt.Errorf("%w: 缺少有效的前置 HOT 权威快照", ErrInvalidState)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	rec.Owner = ev.PrevOwner
	rec.OwnerDirID = ev.PrevDirID
	rec.Generation = ev.PrevGeneration
	rec.PublishedProjection = (&Record{PublishedProjection: ev.PrevProjection}).Clone().PublishedProjection
	rec.MigrationState = StateCleaned
	rec.JournalComplete = true
	rec.RecoveryRequired = false
	rec.WriteRoute = WriteRoute{
		Owner:      ev.PrevOwner,
		Generation: ev.PrevGeneration,
		DirID:      ev.PrevDirID,
		Frozen:     false,
		LateOwner:  ev.PrevOwner,
		LateDirID:  ev.PrevDirID,
	}
	// 热层目录重新登记回权威目录表；退役表里同步移除。
	rec.RetiredHotDirs = removeDirByID(rec.RetiredHotDirs, ev.PrevDirID)
	hasOwnerDir := false
	for _, d := range rec.Dirs {
		if d.ID == ev.PrevDirID {
			hasOwnerDir = true
		}
	}
	if !hasOwnerDir {
		rec.Dirs = append(rec.Dirs, PhysicalDir{ID: ev.PrevDirID, Role: DirOwner})
	}
	for i := range rec.Dirs {
		if rec.Dirs[i].ID == ev.PrevDirID {
			rec.Dirs[i].Role = DirOwner
		}
	}

	// 证据保留为**历史审计**（回滚次数/原因），但不再是当前权威的依据。
	ev.HotReclaimed = false
	ev.HotReclaimedAt = time.Time{}
	ev.RollbackCount++
	ev.LastRollbackReason = reason
	rec.ArchiveEvidence = ev.Clone()
	return nil
}

// MarkArchiveHotReclaimed 记录热层已真的回收（纯逻辑，不写 journal）。
func MarkArchiveHotReclaimed(rec *Record, now time.Time) error {
	if rec == nil {
		return fmt.Errorf("%w: nil record", ErrInvalidState)
	}
	if rec.Owner != OwnerArchive || rec.ArchiveEvidence == nil {
		return fmt.Errorf("%w: 只有 ARCHIVED 权威态才谈得上回收热层", ErrIllegalTransition)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rec.ArchiveEvidence.HotReclaimed = true
	rec.ArchiveEvidence.HotReclaimedAt = now.UTC()
	return nil
}

// MarkArchived 把分区切换到 ARCHIVED 权威态并写 journal（可审计、幂等）。
//
// 返回值 already=true 表示此前已是同一归档位置的 ARCHIVED 态：本次**没有**追加 journal，
// 调用方可以安全重跑（重试不会产生第二条归档记录，也不会把热层回收状态重置）。
func (c *Catalog) MarkArchived(key PartitionKey, ev ArchiveEvidence, now time.Time) (rec *Record, already bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cur, ok := c.records[key]
	if !ok {
		return nil, false, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	next := cur.Clone()
	already, err = ApplyArchiveTransition(next, ev, now)
	if err != nil {
		return nil, false, err
	}
	if already {
		return cur.Clone(), true, nil
	}
	committed, err := c.commitRecordLocked(next, JournalEntry{
		Key:         key,
		State:       StateCleaned,
		FromState:   cur.MigrationState,
		Owner:       next.Owner,
		Generation:  next.Generation,
		DirID:       next.OwnerDirID,
		Detail:      "archived: 正本已入归档且证据完整（" + next.ArchiveEvidence.ProviderKind + " " + next.ArchiveEvidence.ObjectPrefix + "）",
		AtUnixMilli: archiveNowMillis(now),
	})
	if err != nil {
		return nil, false, err
	}
	return committed, false, nil
}

// RollbackArchived 把归档态回滚为 HOT 并写 journal；返回该日是否仍有其它已归档成员。
//
// 「仍有其它已归档成员」供调用方决定是否解除日级冻结：只要还有成员在归档态，
// 这一天的归档工作就尚未结束，冻结继续有效（与 COLD 迁移同口径）。
func (c *Catalog) RollbackArchived(key PartitionKey, reason string, now time.Time) (stillArchivedOnDay bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cur, ok := c.records[key]
	if !ok {
		return false, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	next := cur.Clone()
	if err := RevertArchiveTransition(next, reason, now); err != nil {
		return false, err
	}
	if _, err := c.commitRecordLocked(next, JournalEntry{
		Key:         key,
		State:       StateCleaned,
		FromState:   cur.MigrationState,
		Owner:       next.Owner,
		Generation:  next.Generation,
		DirID:       next.OwnerDirID,
		Detail:      "archive-rollback: 热层回收失败，权威退回 HOT（" + reason + "）",
		AtUnixMilli: archiveNowMillis(now),
	}); err != nil {
		return false, err
	}
	for other, rec := range c.records {
		if other.UTCDay == key.UTCDay && rec.Owner == OwnerArchive {
			return true, nil
		}
	}
	return false, nil
}

// MarkArchiveReclaimed 记录热层回收完成并写 journal。
func (c *Catalog) MarkArchiveReclaimed(key PartitionKey, now time.Time) (*Record, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cur, ok := c.records[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	next := cur.Clone()
	if err := MarkArchiveHotReclaimed(next, now); err != nil {
		return nil, err
	}
	return c.commitRecordLocked(next, JournalEntry{
		Key:         key,
		State:       StateCleaned,
		Owner:       next.Owner,
		Generation:  next.Generation,
		DirID:       next.OwnerDirID,
		Detail:      "archive-reclaimed: 热层副本已回收",
		AtUnixMilli: archiveNowMillis(now),
	})
}

// CanReclaimArchivedDay 判定「该分区所在物理日」的热层目录此刻能否真的删除。
//
// 为什么必须按**日**而不是按分区判定：VL 的日分区目录是
// `partitions/<utc-day>/`，一个目录里装着当天**所有** storage_namespace 的数据。
// 只要同一天还有任何一个命名空间仍属 HOT 权威，删目录就等于把那份未归档的数据一起删掉。
// 该判据与既有 COLD 清理（canRetireDayLocked 的「HOT authority remains」）同一条红线，
// 只是把「接收方已校验」换成「归档证据完整」。
//
// 另外三条必须同时成立：
//   - 本分区已是 ARCHIVED 且证据完整；
//   - 该日没有在途物理写许可（正在写的目录不能删）；
//   - 没有活跃查询租约/视图钉住该日（正在被读的位置不能删）。
func (c *Catalog) CanReclaimArchivedDay(key PartitionKey) (bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now().UTC()
	rec, ok := c.records[key]
	if !ok {
		return false, "Catalog 记录不存在"
	}
	if rec.Owner != OwnerArchive {
		return false, "分区不在 ARCHIVED 权威态（拒绝删热层）"
	}
	if err := rec.ArchiveEvidence.Validate(); err != nil {
		return false, "归档证据不完整：" + err.Error()
	}
	if rec.ArchiveEvidence.HotReclaimed {
		return false, "热层已回收，无需重复删除"
	}
	if c.dayPermitCountLocked(key.UTCDay) > 0 {
		return false, "该物理日仍有在途写入许可"
	}
	if c.keyPermitCountLocked(key) > 0 {
		return false, "该分区仍有在途写入许可"
	}
	for other, member := range c.records {
		if other.UTCDay != key.UTCDay {
			continue
		}
		if member.Owner == OwnerHot {
			return false, "同一物理日仍有 HOT 权威分区：" + other.String()
		}
		if member.Owner == OwnerArchive && member.ArchiveEvidence.Validate() != nil {
			return false, "同一物理日存在归档证据不完整的分区：" + other.String()
		}
		for _, lease := range member.QueryLeases {
			if lease.Active(now, member.Generation) {
				return false, "该物理日仍有活跃查询租约：" + other.String()
			}
		}
	}
	// 视图/读钉：按天+归档 owner 精确判定（避免把别的天的活跃视图当成阻塞）。
	for _, view := range c.views {
		for _, snap := range view.snapshots {
			if snap.Key.UTCDay != key.UTCDay {
				continue
			}
			if view.pins > 0 || (view.invalid == "" && now.Before(snap.ExpiresAt)) {
				return false, "该物理日仍有活跃查询视图或读钉"
			}
		}
	}
	return true, "归档证据完整且无在途写入/活跃读取"
}

// FreezeDayForArchive 冻结某物理日的写入接纳，为归档导出提供一致性快照。
//
// 为什么归档也要冻结：导出必须读**冻结快照**（`VLPartitionOps.snapshot` 内部要求
// `CheckPhysicalDayDrained` = 日已冻结且无在途写许可）。不冻结就只能直接读正在写入的
// 目录——那不是一致视图，也无从证明它不再变化。这里**不放宽**那道校验，
// 而是把「归档」也纳入可冻结的正当理由。
//
// 与 COLD 迁移的区别：不推进迁移状态机（不建批次、不改记录 MigrationState），
// 日记录以 TargetOwner=archive 标记归属，供恢复循环与候选过滤识别。
func (c *Catalog) FreezeDayForArchive(utcDay string, archiveDirID string) error {
	if archiveDirID == "" {
		return fmt.Errorf("%w: 归档日目录标识不能为空", ErrInvalidState)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if day := c.days[utcDay]; day != nil && day.Frozen {
		// 已由 COLD 迁移冻结：归档不能与搬运抢同一天的写入路由。
		if day.TargetOwner != OwnerArchive {
			return fmt.Errorf("%w: 该物理日已在 %s 迁移中冻结", ErrPhysicalDayFrozen, day.TargetOwner)
		}
		return nil
	}
	records, err := c.dayRecordsLocked(utcDay)
	if err != nil {
		return err
	}
	day := &PhysicalDayMigration{UTCDay: utcDay, TargetOwner: OwnerArchive, TargetDirID: archiveDirID, Frozen: true}
	var changed []*Record
	for _, rec := range records {
		if rec.Owner != OwnerHot {
			continue
		}
		if rec.MigrationState != "" && rec.MigrationState != StateCleaned {
			return fmt.Errorf("%w: 该物理日存在在途迁移分区 %s", ErrInvalidState, rec.Key)
		}
		rec.WriteRoute.Frozen = true
		// 归档期间不接受迟到写入：没有合法承接方（正本即将进入归档）。
		rec.WriteRoute.LateOwner = ""
		rec.WriteRoute.LateDirID = ""
		changed = append(changed, rec)
	}
	_, err = c.commitDayLocked(day, changed, JournalEntry{
		Detail:      "freeze physical-day admission for archive export",
		AtUnixMilli: time.Now().UnixMilli(),
	})
	return err
}

// AbortArchiveFreeze 解除归档冻结，把写入接纳还给仍在 HOT 权威的分区。
//
// 只在「这一天已没有任何 ARCHIVED 成员」时由调用方使用（回滚到底的场景）；
// 仍有归档成员时保持冻结，避免把一天的一部分写成 HOT 另一部分已在归档。
func (c *Catalog) AbortArchiveFreeze(utcDay, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	day := c.days[utcDay]
	if day == nil || day.TargetOwner != OwnerArchive {
		// 幂等：没有归档冻结可解除时不是错误。
		// 调用方（回滚/导出失败）无法确定本轮是否真的建立过冻结，把「本来就没冻结」
		// 报成错误只会制造噪音告警，并诱使调用方去掩盖真正的失败。
		return nil
	}
	if !day.Frozen {
		return nil
	}
	var changed []*Record
	for key, rec := range c.records {
		if key.UTCDay != utcDay || rec.Owner != OwnerHot || !rec.WriteRoute.Frozen {
			continue
		}
		next := rec.Clone()
		next.WriteRoute = WriteRoute{
			Owner:      next.Owner,
			Generation: next.Generation,
			DirID:      next.OwnerDirID,
			LateOwner:  next.Owner,
			LateDirID:  next.OwnerDirID,
		}
		changed = append(changed, next)
	}
	day = day.Clone()
	day.Frozen = false
	_, err := c.commitDayLocked(day, changed, JournalEntry{
		Detail:      "abort archive freeze: " + reason,
		AtUnixMilli: time.Now().UnixMilli(),
	})
	return err
}

// CompleteArchiveFreeze 在全日归档并回收完成后解除冻结（不再接受该日写入）。
//
// 与 AbortArchiveFreeze 的区别：它只在「该日已无 HOT 权威」时解冻，
// 即整天的数据都已进入归档——解冻后该日仍会拒绝写入，但原因是记录本身已归档
// （ErrPartitionArchived），而不是「日接纳被冻结」。
func (c *Catalog) CompleteArchiveFreeze(utcDay string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	day := c.days[utcDay]
	if day == nil || day.TargetOwner != OwnerArchive || !day.Frozen {
		return nil
	}
	for key, rec := range c.records {
		if key.UTCDay == utcDay && rec.Owner == OwnerHot {
			return fmt.Errorf("%w: 该物理日仍有 HOT 权威分区", ErrPhysicalDayNotRetirable)
		}
	}
	next := day.Clone()
	next.Frozen = false
	_, err := c.commitDayLocked(next, nil, JournalEntry{
		Detail:      "complete archive day: 全日已归档，热层冻结解除",
		AtUnixMilli: time.Now().UnixMilli(),
	})
	return err
}

// PendingArchiveReclaims 返回「已归档但热层尚未回收」的分区（观测面：残留热层可查）。
//
// 这些分区是安全方向的不一致（数据多一份、盘没省下来），必须能被看见——
// 否则一次「标记归档成功、删除前进程退出」会永久留下一份谁也不知道的副本。
func (c *Catalog) PendingArchiveReclaims() []PartitionKey {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []PartitionKey
	for key, rec := range c.records {
		if rec.Owner != OwnerArchive || rec.ArchiveEvidence == nil {
			continue
		}
		if !rec.ArchiveEvidence.HotReclaimed {
			out = append(out, key)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// ArchivedDay 报告某物理日当前是否仍被归档流程**冻结**。
//
// 只看 Frozen 而不看 TargetOwner：解除冻结后 TargetOwner=archive 会作为
// 「这一天曾被归档」的审计痕迹保留下来，用 TargetOwner 判定会把已解冻的日子误报成仍在归档中。
func (c *Catalog) ArchivedDay(utcDay string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	day := c.days[utcDay]
	return day != nil && day.TargetOwner == OwnerArchive && day.Frozen
}

func (c *Catalog) keyPermitCountLocked(key PartitionKey) int {
	n := 0
	for _, p := range c.permits {
		if p.Key == key {
			n++
		}
	}
	return n
}

func removeDirByID(dirs []PhysicalDir, dirID string) []PhysicalDir {
	if dirID == "" || len(dirs) == 0 {
		return dirs
	}
	out := dirs[:0]
	for _, d := range dirs {
		if d.ID != dirID {
			out = append(out, d)
		}
	}
	return out
}

func archiveNowMillis(now time.Time) int64 {
	if now.IsZero() {
		return time.Now().UnixMilli()
	}
	return now.UTC().UnixMilli()
}
