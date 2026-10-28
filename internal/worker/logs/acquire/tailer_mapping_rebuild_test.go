package acquire

// 物理映射重建的回归（2026-10-28）。
//
// 背景：生产 10/19 个源因 `PHYSICAL_MAPPING_UNKNOWN` 永久停采。根因是 `physical_mapping_known`
// 是后来才加的字段，旧账本缺它；而文件已被新实例重写（14:05 `Starting minecraft server`）。
//
// 判据的关键在于**能否区分两种形态完全相同的场景**：
//
//	A 文件里的字节是“新日志” → 必须重建（否则永久停采）
//	B 文件里的字节是“已投递过的残留前缀” → 绝不能重建（否则重复入库）
//
// 区分依据：账本记录的 IdentitySHA256 与文件当前前缀是否相同。
// A 不同（文件被换），B 相同（内容就是当初那份）。

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// newMappingRebuildFixture 造一个“live 段零宽 + 无物理映射 + 旧内容身份”的源。
func newMappingRebuildFixture(t *testing.T, content string) (*FileTailer, ledger.SourceKey, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	key := testKey("mapping-rebuild", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)

	// 旧账本形态：记录了内容身份（指向**旧**内容），但没有 physical_mapping_known。
	idBytes, _, err := ContentIdentity(path)
	require.NoError(t, err)
	require.NoError(t, led.AdvanceRead(key, 1530))
	require.NoError(t, led.AdvanceDurable(key, 1530))
	require.NoError(t, led.RegisterSegment(key, ledger.Segment{
		Path: path, Kind: ledger.SegmentLive,
		StartPos: 1530, EndPos: 1530,
		IdentityBytes: idBytes, IdentitySHA256: "stale-identity-of-replaced-file",
	}))

	tailer := NewFileTailer(led, key, path, wal, nil)
	return tailer, key, path
}

// TestPhysicalMappingRebuiltWhenContentIdentityDiffers 是本修复的正例：
// 文件内容身份与账本记录**不同** ⇒ 文件确实换了 ⇒ 必须重建并允许读。
func TestPhysicalMappingRebuiltWhenContentIdentityDiffers(t *testing.T) {
	tailer, key, _ := newMappingRebuildFixture(t, "[14:05:56] [ServerMain/INFO]: Starting minecraft server version 1.20.1\n")

	require.NoError(t, tailer.checkPhysicalMapping(),
		"内容身份不匹配（文件已换）⇒ 必须重建并放行 ✓")

	require.True(t, tailer.physicalMappingKnown, "重建后 physicalMappingKnown 必须为真 ✓")
	require.Zero(t, tailer.physicalStart, "重建后物理起点为文件头 ✓")

	// 账本的未能解决缺口不应包含 PHYSICAL_MAPPING_UNKNOWN（我们放行了）。
	entry := tailer.led.Get(key)
	for _, gap := range entry.Gaps {
		if gap.Reason == PhysicalMappingUnknownReason {
			require.True(t, gap.Resolved, "重建后不得留下未解决的 PHYSICAL_MAPPING_UNKNOWN ✓")
		}
	}
}

// TestPhysicalMappingNotRebuiltWhenContentIdentityMatches 守住**不可退让的*** :
// 内容身份与账本**相同**（文件就是当初那份）⇒ 绝不重建。
//
// 这正是既有用例 TestSixFixLegacyNonzeroLiveMappingCannotRereadPrefix 护的场景：
// 文件里剩下的字节是已投递过的残留前缀，重建会把它当新日志重复入库。
func TestPhysicalMappingNotRebuiltWhenContentIdentityMatches(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	body := "surviving-durable-prefix\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	key := testKey("mapping-keep", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	idBytes, idSHA, err := ContentIdentity(path)
	require.NoError(t, err)
	require.NoError(t, led.AdvanceRead(key, 1530))
	require.NoError(t, led.AdvanceDurable(key, 1530))
	// 关键：内容身份与**当前**文件**一致**。
	require.NoError(t, led.RegisterSegment(key, ledger.Segment{
		Path: path, Kind: ledger.SegmentLive,
		StartPos: 1530, EndPos: 1530,
		IdentityBytes: idBytes, IdentitySHA256: idSHA,
	}))

	tailer := NewFileTailer(led, key, path, wal, nil)
	require.Error(t, tailer.checkPhysicalMapping(),
		"内容身份一致 ⇒ 不得重建（否则重复入库）✓")

	_, pollErr := tailer.Poll()
	require.Error(t, pollErr, "仍必须拒绝读该文件 ✓")
}

// TestPhysicalMappingNotRebuiltWithoutIdentity 守住：无内容身份 ⇒ 无法证明文件已换 ⇒ 不重建。
func TestPhysicalMappingNotRebuiltWithoutIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte("some-content\n"), 0o600))

	key := testKey("mapping-noidentity", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	require.NoError(t, led.AdvanceRead(key, 1530))
	require.NoError(t, led.AdvanceDurable(key, 1530))
	require.NoError(t, led.RegisterSegment(key, ledger.Segment{
		Path: path, Kind: ledger.SegmentLive,
		StartPos: 1530, EndPos: 1530,
		// 无 IdentityBytes / IdentitySHA256
	}))

	tailer := NewFileTailer(led, key, path, wal, nil)
	require.Error(t, tailer.checkPhysicalMapping(),
		"无内容身份 ⇒ 无法证明文件已换 ⇒ 不得重建 ✓")
}

// TestPhysicalMappingNotRebuiltWhenPhysicalStartNonzero 守住：重锚过的非零物理起点不动。
func TestPhysicalMappingNotRebuiltWhenPhysicalStartNonzero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte("reanchored-content\n"), 0o600))

	key := testKey("mapping-reanchored", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	idBytes, _, err := ContentIdentity(path)
	require.NoError(t, err)
	require.NoError(t, led.AdvanceRead(key, 1530))
	require.NoError(t, led.AdvanceDurable(key, 1530))
	require.NoError(t, led.RegisterSegment(key, ledger.Segment{
		Path: path, Kind: ledger.SegmentLive,
		StartPos: 1530, EndPos: 1530,
		PhysicalStart: 4096, PhysicalMappingKnown: false, // 非零物理起点
		IdentityBytes: idBytes, IdentitySHA256: "stale",
	}))

	tailer := NewFileTailer(led, key, path, wal, nil)
	require.Error(t, tailer.checkPhysicalMapping(),
		"非零物理起点是重锚过的残段，不得重建 ✓")
}
