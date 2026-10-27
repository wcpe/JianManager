package acquire

// 存量分段内容身份补建的回归（2026-10-28）。
//
// 它守的是三条不得退让的性质：
//   1. 只从**盘上真实字节**算身份（不编造）；
//   2. 槽位复用防护：登记时大小与当前不符 ⇒ 一律不补；
//   3. 幂等：已有身份直跳，已补过的不重复补。
//
// 变异验证（见提交信息）：
//   M1 去掉 ObservedSize 不匹配的防护 ⇒ TestBackfillLegacyIdentitiesSkipsResizedSlot 转红；
//   M2 去掉 ContentIdentity 调用而直接编一个 sha ⇒ TestBackfillLegacyIdentitiesUsesRealBytes 转红；
//   M3 去掉幂等跳过 ⇒ TestBackfillLegacyIdentitiesIsIdempotent 转红。

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func newLegacyLedger(t *testing.T, segments ...ledger.Segment) (*ledger.Ledger, ledger.SourceKey) {
	t.Helper()
	led := ledger.New()
	key := ledger.SourceKey{LogSourceID: "inst:legacy", SourceGeneration: "g1"}
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration})
	for _, seg := range segments {
		require.NoError(t, led.RegisterSegment(key, seg))
	}
	return led, key
}

// writeArchive 造一个真实归档文件，返回路径与大小。
func writeArchive(t *testing.T, content string) (string, int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "2026-10-01-1.log.gz")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	info, err := os.Stat(path)
	require.NoError(t, err)
	return path, info.Size()
}

// TestBackfillLegacyIdentitiesUsesRealBytes 钉住「身份来自真实字节」：
// 补出来的 SHA256 必须等于对该文件前缀重算的结果。
func TestBackfillLegacyIdentitiesUsesRealBytes(t *testing.T) {
	path, size := writeArchive(t, "legacy-archive-body")
	led, key := newLegacyLedger(t, ledger.Segment{
		Path: path, Kind: ledger.SegmentGzip,
		StartPos: 0, EndPos: uint64(size),
		ObservedSize: size, Imported: true,
		ImportError: ArchiveUnknownReason + ": legacy",
	})

	filled := BackfillLegacySegmentIdentities(key, led)
	require.Equal(t, 1, filled, "应补建 1 条 ✓")

	wantBytes, wantSHA, err := ContentIdentity(path)
	require.NoError(t, err)
	entry := led.Get(key)
	require.NotNil(t, entry)
	sh := entry.Segments[0]
	require.Equal(t, wantBytes, sh.IdentityBytes, "身份窗口长度必须与 ContentIdentity 同口径 ✓")
	require.Equal(t, wantSHA, sh.IdentitySHA256, "身份必须来自**真实字节**，不得编造 ✓")
}

// TestBackfillLegacyIdentitiesSkipsResizedSlot 守住槽位复用防护：
// 登记时大小与当前不符（槽位内容已换）⇒ 绝不能把新内容的身份挂在旧分段名下。
func TestBackfillLegacyIdentitiesSkipsResizedSlot(t *testing.T) {
	path, size := writeArchive(t, "new-content-after-slot-reuse")
	// 登记的是旧内容的大小（与当前文件不同）。
	led, key := newLegacyLedger(t, ledger.Segment{
		Path: path, Kind: ledger.SegmentGzip,
		StartPos: 0, EndPos: 100,
		ObservedSize: size + 999, // 与真实不符 ⇒ 模拟槽位已被复用
		Imported:     true,
	})

	filled := BackfillLegacySegmentIdentities(key, led)
	require.Zero(t, filled, "槽位内容已变时一律不补 ✓")
	entry := led.Get(key)
	require.Empty(t, entry.Segments[0].IdentitySHA256, "不得把新内容的身份挂在旧分段名下 ✓")
}

// TestBackfillLegacyIdentitiesIsIdempotent 守住幂等：第二轮零补建。
func TestBackfillLegacyIdentitiesIsIdempotent(t *testing.T) {
	path, size := writeArchive(t, "idempotent-body")
	led, key := newLegacyLedger(t, ledger.Segment{
		Path: path, Kind: ledger.SegmentGzip,
		StartPos: 0, EndPos: uint64(size), ObservedSize: size,
	})

	require.Equal(t, 1, BackfillLegacySegmentIdentities(key, led))
	require.Zero(t, BackfillLegacySegmentIdentities(key, led), "已有身份的分段必须直跳 ✓")
}

// TestBackfillLegacyIdentitiesSkipsMissingFile 守住“读不了就不补”：
// 文件不存在时绝不能凭空给出一个身份（否则会把“读不了”摸成“已覆盖”）。
func TestBackfillLegacyIdentitiesSkipsMissingFile(t *testing.T) {
	led, key := newLegacyLedger(t, ledger.Segment{
		Path: filepath.Join(t.TempDir(), "gone.log.gz"), Kind: ledger.SegmentGzip,
		StartPos: 0, EndPos: 100, ObservedSize: 100, Imported: true,
	})

	require.Zero(t, BackfillLegacySegmentIdentities(key, led), "文件不在必须不补 ✓")
	entry := led.Get(key)
	require.Empty(t, entry.Segments[0].IdentitySHA256)
}

// TestBackfillLegacyIdentitiesSkipsUnknownObservedSize 守住 2026-10-28 被回归用例
// TestPendingArchivesReimportsReusedSlotWithUndeliveredTail 当场抑住的 bug：
// `ObservedSize == 0` 的分段**从未记过观察大小**，本就没有任何“内容未变”的证据。
// 给它补身份会让 `DecideArchiveReimport` 的快路径误以为“内容就是这个”，
// 把尚未投递的尾巴当成已覆盖跳过 ⇒ 静默丢日志。
func TestBackfillLegacyIdentitiesSkipsUnknownObservedSize(t *testing.T) {
	path, _ := writeArchive(t, "unknown-observed-size")
	led, key := newLegacyLedger(t, ledger.Segment{
		Path: path, Kind: ledger.SegmentRotatedText,
		StartPos: 0, EndPos: 27,
		ObservedSize: 0, // ← 关键：从未记录过观察大小
		Imported:     true,
	})

	filled := BackfillLegacySegmentIdentities(key, led)
	require.Zero(t, filled, "ObservedSize 未知的分段一律不补 ✓")
	require.Empty(t, led.Get(key).Segments[0].IdentitySHA256,
		"不得给“没有未变证据”的段凭空发证书 ✓")
}

// TestBackfillLegacyIdentitiesIgnoresNonArchiveKinds 守住作用域：
// 只处理归档类形态（gzip / rotated_text），不碰 live / rotated / stdio。
func TestBackfillLegacyIdentitiesIgnoresNonArchiveKinds(t *testing.T) {
	path, size := writeArchive(t, "live-file")
	led, key := newLegacyLedger(t,
		ledger.Segment{Path: path, Kind: ledger.SegmentLive, StartPos: 0, EndPos: uint64(size), ObservedSize: size},
		ledger.Segment{Path: path, Kind: ledger.SegmentStdio, StartPos: 0, EndPos: uint64(size), ObservedSize: size},
	)

	require.Zero(t, BackfillLegacySegmentIdentities(key, led), "非归档形态不得被补身份 ✓")
}
