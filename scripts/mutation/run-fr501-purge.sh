#!/bin/bash
# 变异验证脚本（FR-501 归档对象回收）。
#
# 用法：scripts/mutation/run-fire501-purge.sh
#
# 为什么用 -overlay 而不是手改文件：手改容易忘了改回去，而「忘了改回去」在评审时
# 看起来就像一次未被审计的行为变更。overlay 让原文件**零改动**，变异只存在于
# 一次性的替换副本里，跑完即失效。
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

PASS=0
FAIL=0

# mutate <编号> <文件(相对 ROOT)> <原串> <替换串> <测试命令...>
mutate() {
  local id="$1" file="$2" from="$3" to="$4"
  shift 4
  local target="$WORK/$id"
  mkdir -p "$target/$(dirname "$file")"
  cp "$ROOT/$file" "$target/$file"

  python3 - "$target/$file" "$from" "$to" <<'PY'
import sys
path, old, new = sys.argv[1], sys.argv[2], sys.argv[3]
src = open(path, encoding='utf-8').read()
if old not in src:
    sys.stderr.write("MUTATION TARGET NOT FOUND: %r\n" % old[:80])
    sys.exit(3)
open(path, 'w', encoding='utf-8').write(src.replace(old, new, 1))
PY
  if [ $? -ne 0 ]; then
    echo "  [$id] ✗ 变异点不存在（实现已变，需要更新脚本）"
    FAIL=$((FAIL+1))
    return
  fi

  cat > "$WORK/$id/overlay.json" <<EOF
{"Replace": {"$ROOT/$file": "$target/$file"}}
EOF

  if ( cd "$ROOT" && go test -overlay "$WORK/$id/overlay.json" -count=1 "$@" ) > "$WORK/$id.log" 2>&1; then
    echo "  [$id] ✗ 未转红（变异存活）——用例没有咬住这个缺陷"
    FAIL=$((FAIL+1))
  else
    echo "  [$id] ✓ 转红"
    grep -E '^(--- )?FAIL|panic:' "$WORK/$id.log" | head -3 | sed 's/^/        /'
    PASS=$((PASS+1))
  fi
}

echo "== 变异 1：删除前不判三闸 =="
mutate M1 internal/worker/logs/retention/archive_purge.go \
  'if open, reason := r.policy.DeleteGatesOpen(); !open {' \
  'if open, reason := r.policy.DeleteGatesOpen(); false && !open {' \
  ./internal/worker/logs/retention -run 'TestArchiveObjectPurgeRequiresAllThreeGates'

echo "== 变异 2：不看保留期（到期一律动手）=="
mutate M2 internal/worker/logs/retention/archive_purge.go \
  'case now.Before(t.EligibleAt):' \
  'case false && now.Before(t.EligibleAt):' \
  ./internal/worker/logs/retention -run 'TestArchiveObjectPurgeSkipsBeforeRetention|TestArchiveObjectPurgeRetentionBoundary'

echo "== 变异 3：删除失败不回滚（仍记失败但不退权威态）=="
mutate M3 internal/worker/logs/retention/archive_purge.go \
  'rbErr := r.authority.RollbackPurge(ctx, ref.StorageNamespace, ref.UTCDay, detail)' \
  'var rbErr error' \
  ./internal/worker/logs/retention -run 'TestArchiveObjectPurgeFailureRollsBack|TestArchiveObjectPurgeListFailureRollsBack'

echo "== 变异 4：逐对象删到一半就宣称完成 =="
mutate M4 internal/worker/logs/retention/archive_purge.go \
  'if derr := r.store.DeleteArchiveObject(ctx, ref.ProviderKind, ref.ObjectPrefix, rel); derr != nil {' \
  'if derr := error(nil); derr != nil {' \
  ./internal/worker/logs/retention -run 'TestArchiveObjectPurgeFailureRollsBack|TestArchiveObjectPurgeMarksCompleteOnlyAfterAllDeletes'

echo "== 变异 5：List 错误当成空清单（幂等成功）=="
mutate M5 internal/worker/logs/retention/archive_purge.go \
  'objects, err := r.store.ListArchiveObjects(ctx, ref.ProviderKind, ref.ObjectPrefix)
	if err != nil {' \
  'objects, err := r.store.ListArchiveObjects(ctx, ref.ProviderKind, ref.ObjectPrefix)
	if err != nil && false {' \
  ./internal/worker/logs/retention -run 'TestArchiveObjectPurgeListFailureRollsBack'

echo "== 变异 6：候选已清除也照样删 =="
mutate M6 internal/worker/logs/retention/archive_purge.go \
  'case ref.Purged:' \
  'case false && ref.Purged:' \
  ./internal/worker/logs/retention -run 'TestArchiveObjectPurgeSkipsAlreadyPurgedCandidate'

echo "== 变异 6b：纯函数只靠调用方的 BlockedReason（漏填即删唯一副本）=="
mutate M6b internal/worker/logs/retention/archive_purge.go \
  'case !ref.HotReclaimed:' \
  'case false && !ref.HotReclaimed:' \
  ./internal/worker/logs/retention -run 'TestPlanArchiveObjectPurgeRefusesSelfEvidentBlockers'

echo "== 变异 6c：纯函数不看待恢复标记 =="
mutate M6c internal/worker/logs/retention/archive_purge.go \
  'case ref.RecoveryRequired:' \
  'case false && ref.RecoveryRequired:' \
  ./internal/worker/logs/retention -run 'TestPlanArchiveObjectPurgeRefusesSelfEvidentBlockers'

echo "== 变异 7：Catalog 请求步不落 journal（只改内存）=="
mutate M7 internal/worker/logs/catalog/archive_purge.go \
  'committed, err := c.commitRecordLocked(next, JournalEntry{
		Key:         key,
		State:       StateCleaned,
		FromState:   cur.MigrationState,
		Owner:       next.Owner,
		Generation:  next.Generation,
		DirID:       next.OwnerDirID,
		Detail:      "archive-object-purge-requested: 保留期到期，开始清除归档对象（" + reason + "）",
		AtUnixMilli: archiveNowMillis(now),
	})' \
  'c.records[key] = next
	committed, err := next.Clone(), error(nil)' \
  ./internal/worker/logs/catalog -run 'TestArchiveObjectPurgeSurvivesJournalReplay'

echo "== 变异 8：完成步不落 journal（只改内存）=="
mutate M8 internal/worker/logs/catalog/archive_purge.go \
  'return c.commitRecordLocked(next, JournalEntry{
		Key:         key,
		State:       StateCleaned,
		Owner:       next.Owner,
		Generation:  next.Generation,
		DirID:       next.OwnerDirID,
		Detail:      detail,
		AtUnixMilli: archiveNowMillis(now),
	})' \
  'c.records[key] = next
	return next.Clone(), nil' \
  ./internal/worker/logs/catalog -run 'TestArchiveObjectPurgeSurvivesJournalReplay'

echo "== 变异 9：热层未回收也允许清除归档对象 =="
mutate M9 internal/worker/logs/catalog/archive_purge.go \
  'if !ev.HotReclaimed {' \
  'if false && !ev.HotReclaimed {' \
  ./internal/worker/logs/catalog -run 'TestArchiveObjectPurgeRefusesWhenHotNotReclaimed'

echo "== 变异 10：有恢复标记也允许清除 =="
mutate M10 internal/worker/logs/catalog/archive_purge.go \
  'if rec.RecoveryRequired {' \
  'if false && rec.RecoveryRequired {' \
  ./internal/worker/logs/catalog -run 'TestArchiveObjectPurgeRefusesWhenRecoveryRequired'

echo "== 变异 11：回滚清零尝试次数 =="
mutate M11 internal/worker/logs/catalog/archive_purge.go \
  'ev.PurgeRequestedAt = time.Time{}
	if reason == "" {' \
  'ev.PurgeRequestedAt = time.Time{}
	ev.PurgeAttempts = 0
	if reason == "" {' \
  ./internal/worker/logs/catalog -run 'TestArchiveObjectPurgeRollbackKeepsNotPurgedAndKeepsAttempts'

echo "== 变异 12：查询面仍把已清除报成「未恢复」=="
mutate M12 internal/worker/logs/query/planner.go \
  'if purgedArchive(rec) {
				lead = ReasonArchivePurged
			} else if rec != nil && rec.Owner == catalog.OwnerArchive {' \
  'if false {
				lead = ReasonArchivePurged
			} else if rec != nil && rec.Owner == catalog.OwnerArchive {' \
  ./internal/worker/logs/query -run 'TestPlanner_PurgedArchiveReportsPurgedNotMissing'

echo "== 变异 13：错误码仍复用 LOG_ARCHIVE_MISSING =="
mutate M13 internal/worker/logs/query/service.go \
  'return newErr(ErrCodeArchivePurged,' \
  'return newErr(ErrCodeArchiveMissing,' \
  ./internal/worker/logs/query -run 'TestNoRangeError_PurgedArchiveIsDistinctFromMissing'

echo "== 变异 14：分类器把已清除也当「未恢复」（制造两种原因并存）=="
mutate M14 internal/worker/logs/query/planner.go \
  'if st.ArchiveConfigured && !st.ArchiveRestored && (req.RequireArchive || archivedAuthority) && !purged {' \
  'if st.ArchiveConfigured && !st.ArchiveRestored && (req.RequireArchive || archivedAuthority) {' \
  ./internal/worker/logs/query -run 'TestPlanner_PurgedArchiveNeverCoexistsWithNotRestored'

echo "== 变异 14b：状态函数把已清除分区当普通归档（不认清除标记）=="
mutate M14b internal/worker/logs/query/types.go \
  'if rec != nil && rec.Owner == catalog.OwnerArchive && rec.ArchiveEvidence.ArchiveObjectPurged() {' \
  'if false && rec != nil && rec.Owner == catalog.OwnerArchive && rec.ArchiveEvidence.ArchiveObjectPurged() {' \
  ./internal/worker/logs/query -run 'TestDefaultPartitionStatus_PurgedArchiveIsNotQueryable|TestPlanner_PurgedArchiveNeverCoexistsWithNotRestored'

echo "== 变异 15：配置不校验「开了开关但没保留期」=="
mutate M15 internal/worker/config.go \
  'if cfg.ArchiveObjectRetention.Enabled && strings.TrimSpace(cfg.ArchiveObjectRetention.Retention) == "" {' \
  'if false && cfg.ArchiveObjectRetention.Enabled && strings.TrimSpace(cfg.ArchiveObjectRetention.Retention) == "" {' \
  ./internal/worker -run 'TestRetentionArchiveObjectRetentionEnabledWithoutRetentionRejected'

echo "== 变异 16：Normalize 为归档保留期补默认值（打开就承诺 730d 后删）=="
mutate M16 internal/worker/logs/retention/archive_purge.go \
  'func (p ArchiveObjectRetentionPolicy) Normalize() ArchiveObjectRetentionPolicy {
	out := p' \
  'func (p ArchiveObjectRetentionPolicy) Normalize() ArchiveObjectRetentionPolicy {
	out := p
	if out.Retention <= 0 {
		out.Retention = 730 * 24 * time.Hour
	}' \
  ./internal/worker/logs/retention -run 'TestPlanArchiveObjectPurgeWithoutRetentionNeverDue|TestPolicyNormalizeDoesNotInventArchiveObjectRetention|TestArchiveObjectRetentionActiveRequiresBoth'

echo "== 变异 17：读数不反映真实统计（写死 0）=="
mutate M17 internal/worker/logs/retention/archive_purge.go \
  'return st.LastRunAt, st.Purged, st.Failed, st.RolledBack' \
  'return st.LastRunAt, 0, 0, 0' \
  ./internal/worker/logs/retention -run 'TestMetricsHandlerReflectsArchiveObjectPurge|TestMetricsHandlerReflectsRealArchiveObjectPurgeRound'

echo "== 变异 17b：Step 不把本轮读数并入累计 =="
mutate M17b internal/worker/logs/retention/archive_purge.go \
  'defer func() { r.mergeStats(round) }()' \
  'defer func() { _ = round }()' \
  ./internal/worker/logs/retention -run 'TestMetricsHandlerReflectsRealArchiveObjectPurgeRound'

echo "== 变异 18：Catalog 复核与执行判定分叉（观测面放过待恢复标记）=="
mutate M18 internal/worker/logs/catalog/archive_purge.go \
  'if rec.RecoveryRequired {
		return false, fmt.Errorf("%w: 分区存在待恢复标记（%v），归档对象仍是唯一可用副本",
			ErrArchivePurgeBlocked, rec.PartialReasons)
	}' \
  '' \
  ./internal/worker/logs/catalog -run 'TestArchiveObjectPurgeRefusesWhenRecoveryRequired'

echo
echo "变异结果：转红 $PASS 个 / 存活 $FAIL 个"
[ "$FAIL" -eq 0 ]
