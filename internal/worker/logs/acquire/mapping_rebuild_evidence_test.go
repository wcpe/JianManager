package acquire

// 物理映射证据驱动自愈的回归（2026-10-28 生产停采最后一环）。
//
// ## 现场
//
// 部署 acknowledged 重建修复并重启后，10 个源里最后一个阻塞冒出来：
//
//	PHYSICAL_MAPPING_UNKNOWN: path=".../latest.log" logical_start=1764264
//	refusing to assign new identities without a proven physical origin
//
// 查账本：live 分段 `start=1764264 end=1784612`，`physical_start` / `physical_mapping_known`
// 均为 null（旧账本从未写过这两个字段）；文件实际只有 2428 字节；
// 重算前缀哈希与记录的 `3785ea9c...` 不同。
//
// 上午删掉 `rebuildMappingForReplacedFile` 时我判断“新账本每个分段创建时都带这两个字段，
// 不会再进这个分支”——错了：重建后从账本恢复出来的分段仍带旧字段形态。
//
// ## 本文件钉住两条性质（对应两个方向的风险）
//
//   1. 能证明文件已换（内容身份不同）⇒ 必须自愈，否则源永久停采。
//   2. 不能证明（内容一致 / 无身份基准 / 文件太短）⇒ 必须继续拒绝，
//      否则会把已投递过的旧前缀当新日志重读（静默重复入库）。

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// newMappingFixture 造一个“旧账本形态”的 live 分段：
// 逻辑起点非零、无任何物理映射字段、有内容身份。
func newMappingFixture(t *testing.T, content string) (*FileTailer, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	key := testKey("mapping-evidence", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)

	idBytes, _, err := ContentIdentity(path)
	require.NoError(t, err)

	require.NoError(t, led.AdvanceRead(key, 1764264))
	require.NoError(t, led.AdvanceDurable(key, 1764264))
	// 旧账本形态：PhysicalStart=0、PhysicalMappingKnown=false（JSON 里就是 null）。
	require.NoError(t, led.RegisterSegment(key, ledger.Segment{
		Path: path, Kind: ledger.SegmentLive,
		StartPos: 1764264, EndPos: 1784612,
		IdentityBytes: idBytes, IdentitySHA256: "stale-identity-of-a-replaced-file",
	}))

	return NewFileTailer(led, key, path, wal, nil), path
}

// TestCheckPhysicalMappingRebuildsWhenFileReplaced 是本修复的正例。
//
// 记录的 identity 与实际文件不同 ⇒ 文件确实换了 ⇒ 必须自愈并放行。
func TestCheckPhysicalMappingRebuildsWhenFileReplaced(t *testing.T) {
	tailer, _ := newMappingFixture(t, "brand-new-content-from-restarted-server\n")

	require.NoError(t, tailer.checkPhysicalMapping(),
		"能证明文件已换（内容身份不同）时必须自愈，否则源永久停采")
	require.True(t, tailer.physicalMappingKnown)
	require.Zero(t, tailer.physicalStart)

	// 下一轮不得再次判成“变了”（身份已重置到当前文件）。
	require.NoError(t, tailer.checkPhysicalMapping(), "自愈后必须稳定")
}

// TestCheckPhysicalMappingRefusesWhenIdentityMatches 是**不可退让的反例**：
// 记录的 identity 与当前文件一致 ⇒ 文件就是当初那份 ⇒ 绝不能重建。
//
// 重建会让已投递过的残留前缀被当成新日志重读一遍（静默重复入库）。
func TestCheckPhysicalMappingRefusesWhenIdentityMatches(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	body := "surviving-durable-prefix\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	key := testKey("mapping-same", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)

	idBytes, idSHA, err := ContentIdentity(path)
	require.NoError(t, err)
	require.NoError(t, led.AdvanceRead(key, 1764264))
	require.NoError(t, led.AdvanceDurable(key, 1764264))
	// 关键：identity 与**当前**文件完全一致。
	require.NoError(t, led.RegisterSegment(key, ledger.Segment{
		Path: path, Kind: ledger.SegmentLive,
		StartPos: 1764264, EndPos: 1784612,
		IdentityBytes: idBytes, IdentitySHA256: idSHA,
	}))

	tailer := NewFileTailer(led, key, path, wal, nil)
	require.Error(t, tailer.checkPhysicalMapping(),
		"内容身份一致 ⇒ 绝不重建（否则重复入库）")
}

// TestCheckPhysicalMappingRefusesWithoutIdentity 守住：无内容身份就没有比较基准。
func TestCheckPhysicalMappingRefusesWithoutIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte("some-content\n"), 0o600))

	key := testKey("mapping-noidentity", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	require.NoError(t, led.AdvanceRead(key, 1764264))
	require.NoError(t, led.AdvanceDurable(key, 1764264))
	require.NoError(t, led.RegisterSegment(key, ledger.Segment{
		Path: path, Kind: ledger.SegmentLive,
		StartPos: 1764264, EndPos: 1784612,
		// 无 IdentityBytes / IdentitySHA256
	}))

	tailer := NewFileTailer(led, key, path, wal, nil)
	require.Error(t, tailer.checkPhysicalMapping(),
		"没有内容身份就不能断言文件已换")
}

// TestCheckPhysicalMappingRefusesWhenFileShorterThanIdentity 守住边界：
// 文件比基准前缀还短时无法可靠比较，宁可不动。
//
// 注意这里测的是**行为**而不是哪一层拦下的：`filePrefixSHA256` 用 `io.CopyN`，
// 文件短于要求时返回 `io.EOF`，所以 `hashErr != nil` 那一层也会拦。
// 两条判据构成纵深防御，单独去掉任意一条都不会改变结果（变异验证已确认）。
// 本用例的价值是钉住**最终行为**：这类输入一律不得重建。
func TestCheckPhysicalMappingRefusesWhenFileShorterThanIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte("tiny\n"), 0o600))

	key := testKey("mapping-short", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	require.NoError(t, led.AdvanceRead(key, 1764264))
	require.NoError(t, led.AdvanceDurable(key, 1764264))
	// 基准 512 字节，而文件只有 5 字节。
	require.NoError(t, led.RegisterSegment(key, ledger.Segment{
		Path: path, Kind: ledger.SegmentLive,
		StartPos: 1764264, EndPos: 1784612,
		IdentityBytes: 512, IdentitySHA256: "whatever",
	}))

	tailer := NewFileTailer(led, key, path, wal, nil)
	require.Error(t, tailer.checkPhysicalMapping(),
		"文件短于基准前缀时无法比较，不得重建")
}

// TestCheckPhysicalMappingRefusesWhenPhysicalStartNonzero 守住：
// 重锦过的非零物理起点是残段，不属本机制处理范围。
func TestCheckPhysicalMappingRefusesWhenPhysicalStartNonzero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte("reanchored-content\n"), 0o600))

	key := testKey("mapping-reanchored", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	idBytes, _, err := ContentIdentity(path)
	require.NoError(t, err)
	require.NoError(t, led.AdvanceRead(key, 1764264))
	require.NoError(t, led.AdvanceDurable(key, 1764264))
	require.NoError(t, led.RegisterSegment(key, ledger.Segment{
		Path: path, Kind: ledger.SegmentLive,
		StartPos: 1764264, EndPos: 1784612,
		PhysicalStart: 4096, PhysicalMappingKnown: false, // 非零物理起点
		IdentityBytes: idBytes, IdentitySHA256: "stale",
	}))

	tailer := NewFileTailer(led, key, path, wal, nil)
	require.Error(t, tailer.checkPhysicalMapping(),
		"非零物理起点是重锦过的残段，不得重建")
}

// TestRebuildClearsStaleMappingGap 守住“重建后必须同时消缺口”：
// 只置标志不清缺口 ⇒ ResumeAcquire 仍被拒且本轮不再报错 ⇒ 静默死锁。
func TestRebuildClearsStaleMappingGap(t *testing.T) {
	tailer, path := newMappingFixture(t, "replaced-file-content\n")

	detail := PhysicalMappingUnknownReason + `: path="` + path + `" logical_start=1764264; refusing to assign new identities without a proven physical origin`
	require.NoError(t, tailer.led.RecordGap(tailer.key, 1764264, 1764264, PhysicalMappingUnknownReason, detail))
	require.Equal(t, 1, ledger.UnresolvedGapCountOf(tailer.led.Get(tailer.key)))

	require.NoError(t, tailer.checkPhysicalMapping())
	require.Zero(t, ledger.UnresolvedGapCountOf(tailer.led.Get(tailer.key)),
		"重建后必须同时撑销旧隔离缺口（否则静默死锁）")
}
