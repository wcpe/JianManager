package catalog

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// 本文件实现「归档对象自身的回收」的权威态迁移（FR-501 的最后一块）。
//
// 解决的具体问题：`ArchiveEvidence` 只表达「正本已在归档」，却没有任何地方表达
// 「这份归档副本在保留期结束后被清掉了」。于是归档目的地只增不减：热层回收得再干净，
// 归档桶仍会按天累积（730d 之后仍留着的每一天都是白占）。这也是 spec §7 里
// 「归档对象自身的回收（桶内生命周期）未做」那一条。
//
// # 语义（单向、可审计、三步不可换序）
//
//	ARCHIVED --(保留期到期 + 三闸全开 + 权威态先落 journal)--> ARCHIVED+PurgeRequested
//	         --(逐对象删除，全部成功)--> ARCHIVED+Purged（终态）
//	         --(任一对象删除失败)--> ARCHIVED（回滚，PurgeRequestedAt 清空，记失败原因）
//
// # 为什么「已清除」是证据的属性而不是新的 Owner 取值
//
// 见 types.go 的 ArchiveEvidence 注释：新增 `OwnerPurged` 会让全仓十余处
// `Owner == OwnerHot/OwnerCold/OwnerArchive` 的分支落进各自的 default，
// 而那些 default 各写各的（TierOf 返回不存在的 tier、NamespaceForOwner 找不存在的
// namespace、writeAdmissionLocked 退化成「按 owner 猜一个新位置」）。
// 一个「数据已经不在」的事实绝不能伪装成「另一个存储层」。
//
// # 三条硬边界
//
//  1. **只有证据完整、且热层已回收的归档分区**才谈得上清除归档对象。热层未回收时清除
//     归档副本会让「正本在归档」这句话变成假的，随后若热层回收成功，最后一份就没了。
//  2. **存在待恢复标记（RecoveryRequired）的分区一律拒绝**：那种状态的真相是「恢复失败、
//     这份归档还是唯一可用的副本」，此时清除等于把「还能救」变成「救不回来了」。
//  3. 回滚**不清零** PurgeAttempts：一次失败尝试是真实发生过的事故，必须留在审计里。

// ErrArchivePurgeBlocked 表示该分区当前不满足「可以清除归档对象」的前置条件。
var ErrArchivePurgeBlocked = fmt.Errorf("%w: archive object purge blocked", ErrCatalog)

// checkArchivePurgePrerequisites 是「此刻能不能开始清除归档对象」的**唯一**判定处。
//
// 为什么必须只有一个真源（本文件最初把它写成两份，立刻被用例抓出不一致）：
// 同一个前置条件出现在两个地方（拒绝路径的错误措辞、观测面的原因文案）时，
// 两处迟早会分叉——而分叉的方向一旦是「观测面说没问题、执行侧其实会拒」，
// 运维就会去查保留期配置，而真正的原因是记录状态。
// 返回的 (blocked, already)：
//   - blocked 非 nil ⇒ 不能开始（调用方原样返回给上层）；
//   - already=true  ⇒ 已清除，本轮无需做任何事（**不是**错误）；
//   - 两者皆空 ⇒ 可以开始。
func checkArchivePurgePrerequisites(rec *Record, now time.Time) (already bool, blocked error) {
	if rec == nil {
		return false, fmt.Errorf("%w: nil record", ErrInvalidState)
	}
	ev := rec.ArchiveEvidence
	if rec.Owner != OwnerArchive || ev == nil {
		return false, fmt.Errorf("%w: 只有 ARCHIVED 权威态才可能清除归档对象（当前 %q）",
			ErrIllegalTransition, rec.Owner)
	}
	// 证据必须完整：一份说不清「归档里到底有什么」的记录，不能作为「删掉归档对象」的依据。
	// 注意这里校验的是**导出侧证据**（ArchivedAt / prefix / checksum / coverage），
	// 不含迁移前快照——后者是回滚依据，与清除无关。
	if err := ev.validateExported(); err != nil {
		return false, err
	}
	if ev.Purged {
		return true, nil
	}
	// 热层副本还在时禁止清除归档对象：此刻归档是「多存一份」的第二份，
	// 删掉它再让热层回收跑一轮，最后一份就消失了——而那天本不该消失。
	if !ev.HotReclaimed {
		return false, fmt.Errorf("%w: 热层副本尚未回收，清除归档对象会先于热层回收丢掉一份副本", ErrArchivePurgeBlocked)
	}
	// 待恢复标记为真 = 一次恢复失败还挂着（例如 rehydrate 校验没过）。此时归档对象
	// 是唯一可用的副本，清除它等于把「还能救」直接判成「救不回来」。
	if rec.RecoveryRequired {
		return false, fmt.Errorf("%w: 分区存在待恢复标记（%v），归档对象仍是唯一可用副本",
			ErrArchivePurgeBlocked, rec.PartialReasons)
	}
	// 有活跃查询租约时不动：清除期间若有读请求正在按归档位置取对象，删到一半会让它失败。
	if n := rec.ActiveLeaseCount(now); n > 0 {
		return false, fmt.Errorf("%w: 仍有活跃查询租约（%d）", ErrArchivePurgeBlocked, n)
	}
	// 本地存在已恢复投影时不动：rehydrate 复制出来的那份是**临时**的（它自己的保留期
	// 由 Rehydrate namespace 管），而归档对象是它的耐久来源。删掉归档、留下临时副本，
	// 等于把「两年后还能查」悄悄提前成「rehydrate TTL 之后就查不到」——
	// 而用户此刻正在查的数据看起来一切正常，没人会去查那条链路。
	if rec.PublishedProjection != nil {
		return false, fmt.Errorf("%w: 本地存在已恢复投影（rehydrate 副本是临时的，归档仍是其耐久来源）",
			ErrArchivePurgeBlocked)
	}
	return false, nil
}

// ApplyArchiveObjectPurgeRequest 把归档分区推进到「本轮开始清除归档对象」（纯逻辑，不写 journal）。
//
// 已清除（Purged）时返回 already=true 且**不改任何字段**：重跑不推进尝试次数、
// 不改清除时刻，于是「进程在清理中途重启」的场景可以安全重入。
func ApplyArchiveObjectPurgeRequest(rec *Record, reason string, now time.Time) (already bool, err error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	already, err = checkArchivePurgePrerequisites(rec, now)
	if err != nil || already {
		return already, err
	}

	ev := rec.ArchiveEvidence
	ev.PurgeRequestedAt = now.UTC()
	ev.PurgeAttempts++
	if reason != "" {
		// 请求原因先落进来：若进程在删除中途退出，这一轮「为什么开始删」不会丢。
		ev.PurgeReason = reason
	}
	// 上一轮的失败结论保留到本轮结束：`PurgeAttempts` 与 `LastPurgeError` 一起才能
	// 说清「试过几次、上次为什么失败」。成功完成时才把它清空（见 Complete）。
	return false, nil
}

// ApplyArchiveObjectPurgeComplete 把归档分区标记为「归档对象已全部清除」（纯逻辑，不写 journal）。
//
// 只接受**已经走过请求步**的记录（PurgeRequestedAt 非零）：没有那一步就没有 journal
// 里的「从这一刻起开始删」，事后无从分辨「没删过」与「删完但没记」。
func ApplyArchiveObjectPurgeComplete(rec *Record, reason string, now time.Time) error {
	if rec == nil {
		return fmt.Errorf("%w: nil record", ErrInvalidState)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	ev := rec.ArchiveEvidence
	if rec.Owner != OwnerArchive || ev == nil {
		return fmt.Errorf("%w: 记录不在 ARCHIVED 权威态，无从标记已清除", ErrIllegalTransition)
	}
	if ev.Purged {
		// 幂等：重跑不改变首次清除时刻（审计时间线不得漂移）。
		return nil
	}
	if ev.PurgeRequestedAt.IsZero() {
		return fmt.Errorf("%w: 缺少清除请求记录（必须先落 MarkArchiveObjectsPurgeRequested）", ErrIllegalTransition)
	}
	ev.Purged = true
	ev.PurgedAt = now.UTC()
	if reason != "" {
		ev.PurgeReason = reason
	}
	// 清掉「进行中」标记：清除已完成，留着它会让观测面（与人工核对）读到
	// 「这个分区还在删」的假象。完成时刻由 PurgedAt 表达，二者不该同时非零。
	ev.PurgeRequestedAt = time.Time{}
	ev.LastPurgeError = ""
	return nil
}

// RevertArchiveObjectPurge 把「清除进行中」退回原态（纯逻辑，不写 journal）。
//
// 触发场景：逐对象删除中途失败（目的地不可达、部分对象删除被拒）。
// 回滚必须让「已清除」这个断言保持为假——删了一半就宣称清除，会让查询面
// 给出「已按保留期清除、不可恢复」的死结论，而归档里其实还有半份数据。
func RevertArchiveObjectPurge(rec *Record, reason string, now time.Time) error {
	if rec == nil {
		return fmt.Errorf("%w: nil record", ErrInvalidState)
	}
	ev := rec.ArchiveEvidence
	if rec.Owner != OwnerArchive || ev == nil {
		return fmt.Errorf("%w: 记录不在 ARCHIVED 权威态，无从回滚清除", ErrIllegalTransition)
	}
	if ev.Purged {
		// 已全部清除后回滚等于凭空声明「归档里还有对象」——那是伪造。
		return fmt.Errorf("%w: 归档对象已清除，不得回滚为未清除", ErrIllegalTransition)
	}
	// now 不参与这里的状态改写（回滚不产生新时刻，只留下失败原因）：
	// 保留参数是为了与其它迁移函数同签名，调用点不必为「这一步要不要时间」分叉。
	_ = now
	ev.PurgeRequestedAt = time.Time{}
	if reason == "" {
		reason = "归档对象清除失败"
	}
	ev.LastPurgeError = reason
	return nil
}

// MarkArchiveObjectPurgeRequested 落 journal 记录「本轮开始清除归档对象」。
//
// 返回值 already=true 表示此前已是同一归档位置的已清除态（本次**没有**追加 journal）。
func (c *Catalog) MarkArchiveObjectPurgeRequested(key PartitionKey, reason string, now time.Time) (rec *Record, already bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cur, ok := c.records[key]
	if !ok {
		return nil, false, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	next := cur.Clone()
	already, err = ApplyArchiveObjectPurgeRequest(next, reason, now)
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
		Detail:      "archive-object-purge-requested: 保留期到期，开始清除归档对象（" + reason + "）",
		AtUnixMilli: archiveNowMillis(now),
	})
	if err != nil {
		return nil, false, err
	}
	return committed, false, nil
}

// MarkArchiveObjectsPurged 落 journal 记录「归档对象已全部清除」（终态）。
func (c *Catalog) MarkArchiveObjectsPurged(key PartitionKey, reason string, now time.Time) (*Record, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cur, ok := c.records[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	next := cur.Clone()
	if err := ApplyArchiveObjectPurgeComplete(next, reason, now); err != nil {
		return nil, err
	}
	detail := "archive-objects-purged: 归档对象已按保留期全部清除"
	if cur.ArchiveEvidence != nil && cur.ArchiveEvidence.Purged {
		// 幂等重跑：已清除时不追加第二条「清除完成」，避免审计里出现两个清除时刻。
		return cur.Clone(), nil
	}
	return c.commitRecordLocked(next, JournalEntry{
		Key:         key,
		State:       StateCleaned,
		Owner:       next.Owner,
		Generation:  next.Generation,
		DirID:       next.OwnerDirID,
		Detail:      detail,
		AtUnixMilli: archiveNowMillis(now),
	})
}

// RollbackArchiveObjectPurge 落 journal 把「清除进行中」退回未清除。
func (c *Catalog) RollbackArchiveObjectPurge(key PartitionKey, reason string, now time.Time) (*Record, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cur, ok := c.records[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	next := cur.Clone()
	if err := RevertArchiveObjectPurge(next, reason, now); err != nil {
		return nil, err
	}
	return c.commitRecordLocked(next, JournalEntry{
		Key:         key,
		State:       StateCleaned,
		Owner:       next.Owner,
		Generation:  next.Generation,
		DirID:       next.OwnerDirID,
		Detail:      "archive-object-purge-rollback: 归档对象清除失败，权威态退回未清除（" + reason + "）",
		AtUnixMilli: archiveNowMillis(now),
	})
}

// ArchiveObjectPurgeEligible 复核某分区此刻能否开始清除归档对象，第二个返回值是原因。
//
// 为什么要在 Catalog 里再判一次（调用方已经按策略判过保留期）：保留期判的是「该不该」，
// 这里判的是「此刻安不安全」。两次判定共用同一份权威记录，任何一方单独放宽都不会
// 造成「删掉仍有用的副本」——这正是既有 CanReclaimArchivedDay 的同构做法。
func (c *Catalog) ArchiveObjectPurgeEligible(key PartitionKey) (bool, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	rec, ok := c.records[key]
	if !ok {
		return false, "Catalog 记录不存在"
	}
	already, blocked := checkArchivePurgePrerequisites(rec, time.Now().UTC())
	if blocked != nil {
		// 把 err 里的 reason 文案原样给出去：观测面与拒绝路径必须说同一句话
		// （见 checkArchivePrereq 的说明），而不是各写一套措辞。
		return false, strings.TrimPrefix(blocked.Error(), ErrCatalog.Error()+": ")
	}
	if already {
		return false, "归档对象已清除，无需重复清理"
	}
	return true, "归档证据完整、热层已回收、无待恢复标记与活跃读取"
}

// ArchivedPartitions 返回当前仍处于 ARCHIVED 权威态的分区记录（含已清除的）。
//
// 供归档对象回收的候选枚举使用：清除与否由调用方按证据里的 Purged/PurgedAt 再判，
// 这里只负责「哪些分区曾经进过归档」这一层事实，不在这里做保留期计算
// （保留期属于策略包，Catalog 不该知道「730d」是多少）。
func (c *Catalog) ArchivedPartitions() []*Record {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*Record, 0, len(c.records))
	keys := make([]PartitionKey, 0, len(c.records))
	for key, rec := range c.records {
		if rec.Owner == OwnerArchive && rec.ArchiveEvidence != nil {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	for _, key := range keys {
		out = append(out, c.records[key].Clone())
	}
	return out
}
