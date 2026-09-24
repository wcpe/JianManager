package catalog

import "fmt"

// MigrationState 是 FR-476/FR-472 §5.2 的迁移状态。取值必须与契约完全一致。
type MigrationState string

const (
	// StateRoutingFrozen 冻结本分区写入路由。
	StateRoutingFrozen MigrationState = "ROUTING_FROZEN"
	// StateDraining 排空在途写入/投递。
	StateDraining MigrationState = "DRAINING"
	// StateSnapshotting 对权威副本做 snapshot。
	StateSnapshotting MigrationState = "SNAPSHOTTING"
	// StateStagingVerify 校验迁移暂存副本。
	StateStagingVerify MigrationState = "STAGING_VERIFY"
	// StateAttachedStaging 暂存副本已被 VL 挂载，但 QueryPlanner 必须排除。
	StateAttachedStaging MigrationState = "ATTACHED_STAGING"
	// StateOwnerSwitched 唯一的原子权威切换提交（owner/generation/PublishedProjection/写路由）。
	StateOwnerSwitched MigrationState = "OWNER_SWITCHED"
	// StateQueryLeaseDraining 旧 generation 查询租约排空。
	StateQueryLeaseDraining MigrationState = "QUERY_LEASE_DRAINING"
	// StateDetached 旧目录已 detach；不等于迁移完成。
	StateDetached MigrationState = "DETACHED"
	// StateCleaned 残留目录/journal/校验完成后的终态。
	StateCleaned MigrationState = "CLEANED"
	// StateFailedRetryable 可重试失败；权威停留在最后一次成功提交。
	StateFailedRetryable MigrationState = "FAILED_RETRYABLE"
	// StateFailedManual 需人工介入的失败。
	StateFailedManual MigrationState = "FAILED_MANUAL"
)

// HappyPath 是契约规定的正常迁移顺序（不含失败态）。
var HappyPath = []MigrationState{
	StateRoutingFrozen,
	StateDraining,
	StateSnapshotting,
	StateStagingVerify,
	StateAttachedStaging,
	StateOwnerSwitched,
	StateQueryLeaseDraining,
	StateDetached,
	StateCleaned,
}

// happyIndex 正常态在 HappyPath 中的下标。
var happyIndex = func() map[MigrationState]int {
	m := make(map[MigrationState]int, len(HappyPath))
	for i, s := range HappyPath {
		m[s] = i
	}
	return m
}()

// IsHappyPath reports whether s is on the normal migration path.
func IsHappyPath(s MigrationState) bool {
	_, ok := happyIndex[s]
	return ok
}

// IsFailure reports whether s is a failure terminal/recovery state.
func IsFailure(s MigrationState) bool {
	return s == StateFailedRetryable || s == StateFailedManual
}

// IsAtomicAuthoritySwitch reports whether s is the unique atomic authority-switch commit.
// 仅 OWNER_SWITCHED 允许改变查询/写入权威（owner + generation + write route + projection）。
func IsAtomicAuthoritySwitch(s MigrationState) bool {
	return s == StateOwnerSwitched
}

// HappyIndex returns the 0-based index of a happy-path state.
func HappyIndex(s MigrationState) (int, error) {
	i, ok := happyIndex[s]
	if !ok {
		return 0, fmt.Errorf("%w: %s not on happy path", ErrInvalidState, s)
	}
	return i, nil
}

// Next 返回正常路径上的下一状态。
func Next(s MigrationState) (MigrationState, error) {
	i, err := HappyIndex(s)
	if err != nil {
		return "", err
	}
	if i+1 >= len(HappyPath) {
		return "", fmt.Errorf("%w: %s is terminal", ErrInvalidState, s)
	}
	return HappyPath[i+1], nil
}

// CanTransition 报告 from→to 是否为允许的状态迁移。
//
// 规则：
//   - 正常态只能前进一格，或进入 FAILED_RETRYABLE / FAILED_MANUAL；
//   - FAILED_RETRYABLE 可重试回其失败前的逻辑状态（调用方从 journal 取 from-state）；
//   - FAILED_MANUAL / CLEANED 为恢复意义上的终态，不经本函数自动离开。
func CanTransition(from, to MigrationState) bool {
	if from == to {
		return true
	}
	if to == StateFailedRetryable || to == StateFailedManual {
		// CLEANED 之后不应再失败；失败态可互相升级（retryable→manual）。
		if from == StateCleaned {
			return false
		}
		if IsFailure(from) {
			return to == StateFailedManual
		}
		return IsHappyPath(from)
	}
	if from == StateFailedRetryable && IsHappyPath(to) {
		// 允许从可重试失败回到任意正常迁移态（由 journal/恢复动作指定精确点）。
		return true
	}
	if !IsHappyPath(from) || !IsHappyPath(to) {
		return false
	}
	fi, ti := happyIndex[from], happyIndex[to]
	// 只允许严格 +1；禁止跳步、禁止回退（回退须经 FAILED_RETRYABLE + 显式恢复）。
	return ti == fi+1
}

// Transition 应用一次迁移状态变更（不触碰 owner 权威；权威切换必须走 ApplyOwnerSwitch）。
func Transition(rec *Record, to MigrationState) error {
	if rec == nil {
		return fmt.Errorf("%w: nil record", ErrInvalidState)
	}
	if !CanTransition(rec.MigrationState, to) {
		return fmt.Errorf("%w: %s → %s", ErrIllegalTransition, rec.MigrationState, to)
	}
	rec.MigrationState = to
	return nil
}

// BeginMigration 将稳定分区切入 ROUTING_FROZEN，并登记迁移源/目标。
// 冻结写入路由并排空后才允许 snapshot。
func BeginMigration(rec *Record, target Owner, targetDirID string) error {
	if rec == nil {
		return fmt.Errorf("%w: nil record", ErrInvalidState)
	}
	if !target.Valid() {
		return fmt.Errorf("%w: target=%q", ErrInvalidOwner, target)
	}
	if targetDirID == "" {
		return fmt.Errorf("%w: target dir required", ErrInvalidState)
	}
	// 仅允许从“无在途迁移”的稳定态发起。
	if rec.MigrationState != "" && rec.MigrationState != StateCleaned && !IsFailure(rec.MigrationState) {
		return fmt.Errorf("%w: begin from %s", ErrIllegalTransition, rec.MigrationState)
	}
	rec.MigrationFromOwner = rec.Owner
	rec.MigrationFromDirID = rec.OwnerDirID
	rec.MigrationFromGeneration = rec.Generation
	rec.TargetOwner = target
	rec.TargetDirID = targetDirID
	rec.TargetGeneration = rec.Generation + 1
	rec.JournalComplete = false
	rec.RecoveryRequired = false
	rec.PartialReasons = nil
	rec.MigrationState = StateRoutingFrozen
	// 冻结写入路由：新写入不得再进入原权威目录；迟到事件走受控 late 目标。
	rec.WriteRoute.Frozen = true
	rec.WriteRoute.LateOwner = target
	rec.WriteRoute.LateDirID = targetDirID
	// 登记 staging 目录（尚未 attach 时角色仍为 staging）。
	rec.Dirs = append(rec.Dirs, PhysicalDir{ID: targetDirID, Role: DirStaging})
	return nil
}
