package lifecycle

import (
	"fmt"
	"log/slog"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// DayManager migrates the physical UTC-day partition shared by every logical
// storage namespace. Catalog changes use one batch journal entry per state.
type DayManager struct {
	cat *catalog.Catalog
	ops Ops
	ret RetentionGate
}

func NewDayManager(cat *catalog.Catalog, ops Ops, ret RetentionGate) *DayManager {
	return &DayManager{cat: cat, ops: ops, ret: ret}
}

func (m *DayManager) Start(utcDay string, target catalog.Owner, targetDirID string) error {
	if _, err := m.cat.BeginDayMigration(utcDay, target, targetDirID); err != nil {
		return err
	}
	return m.Resume(utcDay)
}

// activeDayRecordsOf 返回该日**仍参与迁移**的记录子集。
//
// 与 catalog.activeDayRecords 同口径（空态与 CLEANED 不参与），此处本地实现是因为
// catalog 侧未导出该函数；两处判据必须始终一致，否则又会出现「一套判据纳入、
// 另一套判据排除」的夹缝（2026-10-22 复审 P1-1/P1-2 的成因）。
func activeDayRecordsOf(records []*catalog.Record) []*catalog.Record {
	active := make([]*catalog.Record, 0, len(records))
	for _, rec := range records {
		if rec == nil || rec.MigrationState == "" || rec.MigrationState == catalog.StateCleaned {
			continue
		}
		active = append(active, rec)
	}
	return active
}

// reinitiateResidualHotMigration 为「残留的 hot 记录」重新发起迁移。
//
// 判据：`Owner == OwnerHot` 且处于 `""`/`CLEANED`（即不在迁移路径上）。
// 这类记录是「迁移途中新建的迟到记录」——它还有数据没下沉，必须再走一遍。
//
// 目标取该日**已有迁移记录**的 `TargetOwner/TargetDirID`：那是同一天同一个
// 物理分区的既定目标，不能另起一个新的（否则同一天的记录会被送到不同目录，
// 冷层出现两份互不相认的副本）。
//
// 幂等：已经处于迁移中的 hot 记录不会被重复发起（判据只覆盖 ""/CLEANED）。
// 无残留时是零开销的一次遍历。
func (m *DayManager) reinitiateResidualHotMigration(utcDay string, records []*catalog.Record) error {
	var residual []*catalog.Record
	var targetOwner catalog.Owner
	var targetDirID string
	for _, rec := range records {
		if rec == nil {
			continue
		}
		// 借同一天已在迁移/已迁移完成的记录取既定目标。
		if targetDirID == "" && rec.TargetDirID != "" && rec.TargetOwner.Valid() {
			targetOwner, targetDirID = rec.TargetOwner, rec.TargetDirID
		}
		if rec.Owner == catalog.OwnerHot &&
			(rec.MigrationState == "" || rec.MigrationState == catalog.StateCleaned) {
			residual = append(residual, rec)
		}
	}
	if len(residual) == 0 {
		return nil
	}
	// **该日已有迁移在飞时不得重新发起**（2026-10-22 P0）。
	//
	// 重新发起会把残留记录推到 `ROUTING_FROZEN`，而正在迁移的那条可能已到
	// `SNAPSHOTTING`/`ATTACHED_STAGING` ⇒ 同一天出现状态分歧 ⇒ `Resume` 的同态校验
	// 报 `UTC-day state divergence` ⇒ 整日无法推进、每轮 sweep 重复失败。
	//
	// 有在飞迁移时正确的做法是**什么都不做**：残留记录会在该日推进到
	// `DETACHED→CLEANED` 过程中被 `activeDayRecords` 纳入（它此时仍是 hot），
	// 或在该日结账时被 `MarkDayComplete` 的守卫拒绝并留痕。
	// 真正的重新发起只适用于「该日已完全静止、只剩残留」的形态。
	for _, rec := range records {
		if rec == nil {
			continue
		}
		if rec.MigrationState != "" && rec.MigrationState != catalog.StateCleaned &&
			!catalog.IsFailure(rec.MigrationState) {
			// 有记录正处在迁移中间态：交给正常推进，不插手。
			return nil
		}
	}
	if targetDirID == "" {
		// 找不到既定目标：说明这一天从未迁移过，不属于「残留」形态。
		// 交给调用方的初次 BeginDayMigration 处理，这里不擅自决定目标目录。
		return nil
	}
	if _, err := m.cat.BeginDayMigration(utcDay, targetOwner, targetDirID); err != nil {
		return fmt.Errorf("lifecycle: reinitiate residual migration for %s: %w", utcDay, err)
	}
	slog.Warn("该日存在未参与迁移的 hot 残留记录，已自动重新发起迁移（无需人工介入）",
		"utcDay", utcDay, "residualRecords", len(residual),
		"targetOwner", targetOwner, "targetDirID", targetDirID)
	return nil
}

// hasUnmigratedHotResidual 报告该日是否还有「未纳入迁移」的 hot 记录。
//
// 判据与 reinitiateResidualHotMigration 完全一致：`Owner==hot` 且处于
// `""`/`CLEANED`（即不在迁移路径上）。两处必须同源，否则会出现
// 「一边认为有残留、另一边认为没有」的夹缝。
func (m *DayManager) hasUnmigratedHotResidual(utcDay string) bool {
	for _, rec := range m.cat.RecordsForDay(utcDay) {
		if rec == nil {
			continue
		}
		if rec.Owner == catalog.OwnerHot &&
			(rec.MigrationState == "" || rec.MigrationState == catalog.StateCleaned) {
			return true
		}
	}
	return false
}

func (m *DayManager) Resume(utcDay string) error {
	records := m.cat.RecordsForDay(utcDay)
	if len(records) == 0 {
		return fmt.Errorf("lifecycle: UTC day %s not found", utcDay)
	}
	// **失败态判定必须基于活跃子集，不能取 `records[0]`**（2026-10-22 复审 P1-1）。
	//
	// `records[0]` 是按 key 排序的第一条，混合态日里它很可能是一条已 CLEANED 的记录
	// （`NewStableRecord` 的默认态就是 CLEANED）。旧代码用它判失败态，后果是：
	//   · `FAILED_RETRYABLE` 分支被整段跳过 ⇒ 这档唯一的自动恢复路径在混合态日失效；
	//   · 即便进入，`Journal().Entries(key)` 查的也是**另一条记录**的 journal
	//     （日级写入口的 journal key 一律是活跃子集首条的 key）；
	//   · 随后 switch 落到 default ⇒ 裸 return 错误且**不写失败 journal**
	//     ⇒ 该日停在失败态反复失败，只能人工介入，现场还查不到原因。
	//
	// 判据与下面的状态推进保持同源：只看「迁移中」的记录（跳过空态与 CLEANED）。
	// **给残留 hot 记录一个可执行出口**（2026-10-22 终审 P2）。
	//
	// 场景：某日已整体下沉到冷层（cold + CLEANED），之后迟到的写入或新出现的
	// storage namespace 在同一天新建了一条 hot 记录 —— 而 `NewStableRecord` 的默认态
	// 就是 `MigrationState=CLEANED`（见 types.go）⇒ 它天生是 `hot + CLEANED`，
	// 既不在迁移路径上（`activeDayRecordsOf` 按 state 过滤，CLEANED 被跳过），
	// 又会被 `MarkDayComplete` 的守卫拒绝结账。
	//
	// 若这里不处理，该日会陷入无解死循环：
	//   MarkDayComplete 拒绝 → fail() → FAILED_RETRYABLE
	//   → 下轮 sweep 从 FAILED_RETRYABLE 恢复 → 再次拒绝
	// 且 `retention.Mover` 的失败只记 `KeptOriginal`，既不重试计数也不升级
	// `FAILED_MANUAL` ⇒ 每轮 sweep 追加 2 条 journal、重跑 Cleanup，永不收敛。
	//
	// 出口 = **重新发起迁移**：`BeginMigration` 的守卫允许从 `CLEANED` 起跳
	// （只拒绝「已在迁移中」的状态）。重新发起后记录变成 `ROUTING_FROZEN`，
	// 后续日级操作自然把它纳入，走完 happy path 即被切到 cold，
	// `MarkDayComplete` 的守卫随之满足。
	//
	// 只在「该日**已有**迁移在跑或已跑完」时做，避免干扰初次迁移的入口
	// （初次迁移由调用方显式 BeginDayMigration 发起，target 由其指定）。
	if err := m.reinitiateResidualHotMigration(utcDay, records); err != nil {
		return err
	}
	records = m.cat.RecordsForDay(utcDay)
	active := activeDayRecordsOf(records)
	if len(active) > 0 {
		if active[0].MigrationState == catalog.StateFailedManual {
			return fmt.Errorf("lifecycle: manual recovery required")
		}
		if active[0].MigrationState == catalog.StateFailedRetryable {
			resume := catalog.MigrationState("")
			entries := m.cat.Journal().Entries(active[0].Key)
			for i := len(entries) - 1; i >= 0; i-- {
				if entries[i].State == catalog.StateFailedRetryable {
					resume = entries[i].FromState
					break
				}
			}
			if resume == "" {
				return fmt.Errorf("lifecycle: day retry state missing")
			}
			if _, err := m.cat.AdvanceDay(utcDay, resume); err != nil {
				return err
			}
		}
	}
	representative := records[0].Key
	fail := func(op string, err error) error {
		_, journalErr := m.cat.FailDay(utcDay, catalog.StateFailedRetryable, op+": "+err.Error())
		if journalErr != nil {
			return fmt.Errorf("lifecycle: %s failed: %v; journal: %v", op, err, journalErr)
		}
		return fmt.Errorf("lifecycle: %s failed: %w", op, err)
	}
	var snapshotID string
	for {
		records = m.cat.RecordsForDay(utcDay)
		// **只在「迁移中」的子集内要求状态一致**（2026-10-22 复审）。
		//
		// 旧实现取 `records[0].MigrationState` 当基准并要求该日**全部**记录一致。
		// 而混合态一定会发生：某日整体下沉到冷层后，迟到的写入或新出现的 namespace
		// 仍会在同一天新建一条 Owner=hot 的记录（migrate_target.go 的注释自己承认）。
		// 于是一旦 `BeginDayMigration` 成功、`Resume` 立刻在这里报 state divergence，
		// 且**不写失败 journal** ⇒ 该日 hot 分区停在 ROUTING_FROZEN、写入改走 late 目标、
		// 永不切换，比「直接拒绝」更糟（复审 P0-2）。
		//
		// 判据：已 CLEANED/空态 = 不参与本次迁移（早已下沉完成）；其余必须在同一状态。
		state := catalog.MigrationState("")
		var rep *catalog.Record
		for _, rec := range records {
			if rec.MigrationState == "" || rec.MigrationState == catalog.StateCleaned {
				continue
			}
			if state == "" {
				state = rec.MigrationState
				representative = rec.Key
				rep = rec
				continue
			}
			if rec.MigrationState != state {
				return fmt.Errorf("lifecycle: UTC-day state divergence")
			}
		}
		if state == "" {
			// 该日已无迁移中的记录：视为已完成（幂等）。
			return nil
		}
		// **rep 必须与 state 同源取自活跃子集**（2026-10-22 复审 P1-1）。
		//
		// 下面的所有目录/校验和/目标都必须读 `rep`，绝不能退回 `records[0]`：
		// `records[0]` 是按 key 排序的第一条，混合态日里它很可能是一条已 CLEANED 的记录，
		// 而它的 `LastVerifyCompleted` 在 MarkDayComplete 时已被置 true ⇒
		// `if !rep.LastVerifyCompleted` 恒假 ⇒ 整段 snapshot/copy/verify 被跳过，
		// 直接推进到 ATTACHED_STAGING ⇒ 新 namespace 的当日数据从未拷到冷层，
		// 却被标成「已迁移」并清理旧目录（与本次修复要消灭的丢数据同形）。
		//
		// 这条路径在混合态判定统一之前是**不可达**的（会先报 divergence 退出），
		// 所以必须与判定统一一起改：否则是把「响亮失败」换成「静默丢数据」。
		if rep == nil {
			// 理论上不可达（state != "" 意味着 loop 里赋过值）；显式失败胜过空指针。
			return fail("locate-representative", fmt.Errorf("lifecycle: no active record for %s", utcDay))
		}
		switch state {
		case catalog.StateRoutingFrozen:
			if m.ops.Drain != nil {
				if err := m.ops.Drain(representative); err != nil {
					return fail("drain", err)
				}
			}
			if _, err := m.cat.AdvanceDay(utcDay, catalog.StateDraining); err != nil {
				return fail("advance-draining", err)
			}
		case catalog.StateDraining:
			if _, err := m.cat.AdvanceDay(utcDay, catalog.StateSnapshotting); err != nil {
				return fail("advance-snapshotting", err)
			}
		case catalog.StateSnapshotting:
			if m.ops.Snapshot != nil {
				var err error
				snapshotID, err = m.ops.Snapshot(representative, rep.MigrationFromDirID)
				if err != nil {
					return fail("snapshot", err)
				}
			}
			if _, err := m.cat.AdvanceDay(utcDay, catalog.StateStagingVerify); err != nil {
				return fail("advance-staging-verify", err)
			}
		case catalog.StateStagingVerify:
			if !rep.LastVerifyCompleted {
				if snapshotID == "" && m.ops.Snapshot != nil {
					var err error
					snapshotID, err = m.ops.Snapshot(representative, rep.MigrationFromDirID)
					if err != nil {
						return fail("resume-snapshot", err)
					}
				}
				copyID := ""
				if m.ops.Copy != nil {
					var err error
					copyID, err = m.ops.Copy(representative, snapshotID, rep.TargetDirID)
					if err != nil {
						return fail("copy", err)
					}
				}
				checksum := ""
				if m.ops.Verify != nil {
					var err error
					checksum, err = m.ops.Verify(representative, copyID, rep.TargetDirID)
					if err != nil {
						return fail("verify", err)
					}
				}
				if checksum == "" {
					return fail("verify", fmt.Errorf("verification checksum missing"))
				}
				if err := m.cat.RecordDayVerification(utcDay, checksum); err != nil {
					return fail("record-verification", err)
				}
			}
			if _, err := m.cat.AdvanceDay(utcDay, catalog.StateAttachedStaging); err != nil {
				return fail("advance-attached-staging", err)
			}
		case catalog.StateAttachedStaging:
			if m.ops.Attach != nil {
				if err := m.ops.Attach(representative, rep.TargetDirID); err != nil {
					return fail("attach", err)
				}
			}
			projections := make(map[catalog.PartitionKey]*catalog.PublishedProjection, len(records))
			for _, rec := range records {
				generation := rec.TargetGeneration
				if generation == 0 {
					generation = rec.MigrationFromGeneration + 1
				}
				projections[rec.Key] = migratedProjection(rec.PublishedProjection, rec.TargetDirID, generation)
			}
			if _, err := m.cat.SwitchDayOwners(utcDay, rep.TargetOwner, rep.TargetDirID, projections); err != nil {
				return fail("owner-switch", err)
			}
		case catalog.StateOwnerSwitched:
			if _, err := m.cat.AdvanceDay(utcDay, catalog.StateQueryLeaseDraining); err != nil {
				return fail("advance-lease-draining", err)
			}
		case catalog.StateQueryLeaseDraining:
			for _, rec := range records {
				if m.ops.LeaseDrain != nil {
					if err := m.ops.LeaseDrain(rec.Key, rec.MigrationFromGeneration); err != nil {
						return fail("lease-drain", err)
					}
				}
				if m.ret != nil {
					if ok, reason := m.ret.AllowDetach(rec.Key, rec.MigrationFromDirID, rec.OwnerDirID); !ok {
						return fmt.Errorf("lifecycle: retention last-copy protection: %s", reason)
					}
				}
			}
			if m.ops.Detach != nil {
				if err := m.ops.Detach(representative, rep.MigrationFromDirID); err != nil {
					return fail("detach", err)
				}
			}
			if _, err := m.cat.AdvanceDay(utcDay, catalog.StateDetached); err != nil {
				return fail("advance-detached", err)
			}
		case catalog.StateDetached:
			// **该日仍有未纳入迁移的 hot 残留 ⇒ 本轮不结账**（2026-10-22 P0 配套）。
			//
			// 混合态日可能同时有「已走到 DETACHED 的记录」和「迟到新建的 hot+CLEANED 残留」。
			// 此时直接结账会被 `MarkDayComplete` 的守卫拒绝（数据诚实性要求，那是对的），
			// 但那个拒绝会把整日推进 FAILED_RETRYABLE，白白丢掉已经走完的进度。
			//
			// 正确处置：**先不结账，把残留留到下一轮**。下一轮进来时该日已无在飞迁移
			// （DETACHED 不算中间态的一个「推进中」状态？——它算，见下方判断），
			// 于是 `reinitiateResidualHotMigration` 会把残留推起来，
			// 再走一遍即可正常结账。这样既不丢数据，也不制造状态分歧。
			//
			// 为什么不能在本轮顺手重新发起：那会把残留推到 ROUTING_FROZEN，
			// 与当前 rep 的 DETACHED 形成状态分歧（见 reinitiateResidualHotMigration 的注释）。
			if m.hasUnmigratedHotResidual(utcDay) {
				// **返回 nil 而不是错误**：这是有意的、可自愈的推迟，不是失败。
				// 下一轮 sweep 会再进来，届时该日已无在飞迁移，
				// `reinitiateResidualHotMigration` 会把残留推起来，走完即可结账。
				// 若返回错误，调用方会把它计入失败、可能触发 FAILED_RETRYABLE
				// 并丢掉已经走完的进度（DETACHED 之前的 snapshot/copy/verify 全白做）。
				slog.Warn("该日仍有未纳入迁移的 hot 残留，推迟到下一轮完成结账（无需人工介入）",
					"utcDay", utcDay)
				return nil
			}
			if m.ops.Cleanup != nil {
				if err := m.ops.Cleanup(representative, rep.MigrationFromDirID); err != nil {
					return fail("cleanup", err)
				}
			}
			checksum := rep.LastVerifyChecksum
			if checksum == "" {
				return fail("journal-complete", fmt.Errorf("verification checksum missing"))
			}
			if err := m.cat.ClearDayResiduals(utcDay); err != nil {
				return fail("clear-residual", err)
			}
			if err := m.cat.MarkDayComplete(utcDay, checksum); err != nil {
				return fail("journal-complete", err)
			}
			if _, err := m.cat.AdvanceDay(utcDay, catalog.StateCleaned); err != nil {
				return fail("advance-cleaned", err)
			}
			return nil
		case catalog.StateCleaned:
			return nil
		default:
			return fmt.Errorf("lifecycle: cannot resume UTC-day state %s", state)
		}
	}
}
