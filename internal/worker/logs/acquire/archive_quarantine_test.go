package acquire

// 隔离撤销的回归（2026-10-28）。
//
// 它守的是两条不得退让的性质：
//   1. 判定不再是 Unknown 时，诊断缺口与 ImportError **必须同时**被撑销
//      （否则源被 ResumeAcquire 永久拒绝）；
//   2. 只消**精确匹配**的那条，同源其它原因/其它位置/其它路径的缺口一律不得被连带。

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// recordUnknownGap 按 AssessArchive 的真实格式登记一条隔离缺口（零宽，位置 = EndPos）。
func recordUnknownGap(t *testing.T, led *ledger.Ledger, key ledger.SourceKey, path string, endPos uint64) {
	t.Helper()
	detail := fmt.Sprintf("%s: path=%q observed_size=100 identity_bytes=512 identity_sha256=deadbeef identity_error=<nil>; no content anchor for current slot content; archive quarantined for ownership recovery", ArchiveUnknownReason, filepath.Clean(path))
	require.NoError(t, led.RecordGap(key, endPos, endPos, ArchiveUnknownReason, detail))
}

func unresolvedCount(led *ledger.Ledger, key ledger.SourceKey) int {
	e := led.Get(key)
	if e == nil {
		return 0
	}
	return ledger.UnresolvedGapCountOf(e)
}

// TestClearUnknownQuarantineResolvesMatchedGap 正例：inode 命中后裁决为 Covered，
// AssessArchive 必须把诊断缺口与 ImportError 同时清掉。
func TestClearUnknownQuarantineResolvesMatchedGap(t *testing.T) {
	dir := t.TempDir()
	path, seg := writeWithInode(t, dir, "a.log.gz", "payload-v1\n")

	led := ledger.New()
	key := ledger.SourceKey{LogSourceID: "inst:q", SourceGeneration: "g1"}
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration})
	seg.ImportError = "ARCHIVE_CONTENT_UNKNOWN: previous run"
	require.NoError(t, led.RegisterSegment(key, *seg))
	recordUnknownGap(t, led, key, path, seg.EndPos)

	require.Equal(t, 1, unresolvedCount(led, key), "前置：确实存在一条未解决缺口")

	imp := NewArchiveImporter(led, key)
	decision, err := imp.AssessArchive(path)
	require.NoError(t, err)
	require.Equal(t, ArchiveCovered, decision.Disposition, "inode 命中 ⇒ 已覆盖")

	require.Zero(t, unresolvedCount(led, key), "缺口必须被撑销（否则源永久停采）✓")
	e := led.Get(key)
	require.Empty(t, e.Segments[0].ImportError, "ImportError 必须同时清空（否则 pendingArchives 永久跳过）✓")
}

// TestClearUnknownQuarantineDoesNotResolveFoldedNeighbour 守住“折叠条目”的真实语义：
//
// 折叠会把区间拉宽（生产实测：零宽 [17718,17718] → 宽 [17718,7878492]），
// 但**起点不变**（仍是那个分段的 EndPos），且 detail 里 path= 仍只一个
// ⇒ 它仍代表**同一份文件**，必须能被消解（否则生产 10 个源被它们永久挡住）。
//
// 因此本用例断言：折叠后的条目**能**被正常恢复。
func TestClearUnknownQuarantineResolvesFoldedEntry(t *testing.T) {
	dir := t.TempDir()
	path, seg := writeWithInode(t, dir, "fold-a.log.gz", "fold-payload\n")

	led := ledger.New()
	key := ledger.SourceKey{LogSourceID: "inst:q5", SourceGeneration: "g1"}
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration})
	seg.ImportError = "ARCHIVE_CONTENT_UNKNOWN: previous run"
	require.NoError(t, led.RegisterSegment(key, *seg))

	// 本分段的诊断缺口（零宽，位置 = EndPos）。
	recordUnknownGap(t, led, key, path, seg.EndPos)
	// 同原因、位置相邻的另一条 ⇒ 会被折并，区间拉宽但起点不变。
	recordUnknownGap(t, led, key, path, seg.EndPos+1)

	entry := led.Get(key)
	require.Len(t, entry.Gaps, 1, "前置：同原因相邻缺口应被折并为一条")
	require.Equal(t, seg.EndPos, entry.Gaps[0].StartPos,
		"前置：折叠后**起点不变**（仍是本分段的 EndPos）")

	imp := NewArchiveImporter(led, key)
	_, err := imp.AssessArchive(path)
	require.NoError(t, err)

	require.Zero(t, unresolvedCount(led, key),
		"折叠条目仍是同一文件的诊断 ⇒ 必须能消解（否则源被永久挡住）✓")
}

// TestClearUnknownQuarantineLeavesOtherGapsIntact 守住精确性：
// 同源其它原因/其它位置的缺口绝不能被连带消解。
//
// 构造要避开两层干扰：
//   - 同位置 + 不同路径的 UNKNOWN 会被账本**折叠合并**成一条（区间只扩大不缩小），
//     所以“同位置不同路径”只能靠**不同原因**来验（不同原因不合并）。
//   - 若只用不同位置验，位置闸就已经把它们排除了，
//     路径闸与原因闸**永远不会被走到**（变异 M2/M3 会存活）。
//
// 故本用例同时包含：同位置不同原因（验原因闸）、不同位置同原因（验位置闸）。
func TestClearUnknownQuarantineLeavesOtherGapsIntact(t *testing.T) {
	dir := t.TempDir()
	path, seg := writeWithInode(t, dir, "b.log.gz", "payload-v2\n")

	led := ledger.New()
	key := ledger.SourceKey{LogSourceID: "inst:q2", SourceGeneration: "g1"}
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration})
	seg.ImportError = "ARCHIVE_CONTENT_UNKNOWN: previous run"
	require.NoError(t, led.RegisterSegment(key, *seg))

	// ① 本分段的隔离缺口（应被消）
	recordUnknownGap(t, led, key, path, seg.EndPos)
	// ② 同位置、但**不同原因**——验“原因闸”。这类缺口是真丢数据的证据，绝不能动。
	//    不同原因不会被折叠合并，故能共存于同位置。
	require.NoError(t, led.RecordGap(key, seg.EndPos, seg.EndPos, "WAL_BODY_MISSING", "real loss at same position"))
	// ③ 同原因、不同位置——验“位置闸”。
	recordUnknownGap(t, led, key, path, seg.EndPos+9999)
	// ④ 同原因、不同位置、不同路径——验“路径闸”。
	recordUnknownGap(t, led, key, filepath.Join(dir, "other.log.gz"), seg.EndPos+88888)

	before := unresolvedCount(led, key)
	require.Equal(t, 4, before, "前置：4 条未解决")

	imp := NewArchiveImporter(led, key)
	_, err := imp.AssessArchive(path)
	require.NoError(t, err)

	after := unresolvedCount(led, key)
	require.Equal(t, 3, after, "只能消精确匹配的那 1 条，其余 3 条必须保留 ✓")

	// 逐条确认保留者的原因未被篡改。
	var keptReasons []string
	for _, gap := range led.Get(key).Gaps {
		if !gap.Resolved {
			keptReasons = append(keptReasons, gap.Reason)
		}
	}
	require.Contains(t, keptReasons, "WAL_BODY_MISSING", "真丢数据的证据绝不能被动 ✓")
	require.Equal(t, 2, countReason(keptReasons, "ARCHIVE_CONTENT_UNKNOWN"),
		"另两条 UNKNOWN（不同位置/不同路径）必须保留 ✓")
}

func countReason(reasons []string, want string) int {
	n := 0
	for _, r := range reasons {
		if r == want {
			n++
		}
	}
	return n
}

// TestClearUnknownQuarantineIsIdempotent 幂等：重复调用零副作用。
func TestClearUnknownQuarantineIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path, seg := writeWithInode(t, dir, "c.log.gz", "payload-v3\n")

	led := ledger.New()
	key := ledger.SourceKey{LogSourceID: "inst:q3", SourceGeneration: "g1"}
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration})
	seg.ImportError = "ARCHIVE_CONTENT_UNKNOWN: previous"
	require.NoError(t, led.RegisterSegment(key, *seg))
	recordUnknownGap(t, led, key, path, seg.EndPos)

	imp := NewArchiveImporter(led, key)
	for i := 0; i < 3; i++ {
		_, err := imp.AssessArchive(path)
		require.NoError(t, err)
	}
	require.Zero(t, unresolvedCount(led, key), "反复调用后仍应为 0 ✓")
}

// TestClearUnknownQuarantineKeepsGapWhenStillUnknown 反向：仍然是 Unknown 时
// 绝不能莫名其妙地撑销缺口（那是真丢数据的证据）。
func TestClearUnknownQuarantineKeepsGapWhenStillUnknown(t *testing.T) {
	dir := t.TempDir()
	// 文件不在盘上 ⇒ 无法证明归属 ⇒ 必须保持 Unknown。
	missing := filepath.Join(dir, "gone.log.gz")
	seg := &ledger.Segment{
		Path: missing, Kind: ledger.SegmentGzip,
		StartPos: 0, EndPos: 100, Imported: true,
	}

	led := ledger.New()
	key := ledger.SourceKey{LogSourceID: "inst:q4", SourceGeneration: "g1"}
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration})
	seg.ImportError = "ARCHIVE_CONTENT_UNKNOWN: previous"
	require.NoError(t, led.RegisterSegment(key, *seg))
	recordUnknownGap(t, led, key, missing, seg.EndPos)
	_ = os.Stat // 保持 import

	imp := NewArchiveImporter(led, key)
	decision, err := imp.AssessArchive(missing)
	require.NoError(t, err)
	require.NotEqual(t, ArchiveCovered, decision.Disposition, "文件不在时不得判已覆盖")
	require.Equal(t, 1, unresolvedCount(led, key), "仍为 Unknown ⇒ 缺口必须保留 ✓")
}
