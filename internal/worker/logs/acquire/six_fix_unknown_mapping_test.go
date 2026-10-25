package acquire

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

func TestSixFixUnknownPhysicalMappingCannotBecomeContentAnchor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.log.1")
	body := "surviving-durable-prefix\nunproven-tail\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0600))
	key, led := testKey("six-old-anchor", "g1"), ledger.New()
	imp := NewArchiveImporter(led, key)
	idBytes, idSHA, err := ContentIdentity(path)
	require.NoError(t, err)
	require.NoError(t, led.AdvanceRead(key, 1530))
	require.NoError(t, led.AdvanceDurable(key, 1530))
	require.NoError(t, led.RegisterSegment(key, ledger.Segment{Path: "legacy-live", Kind: ledger.SegmentLive, StartPos: 1530, EndPos: 1530, IdentityBytes: idBytes, IdentitySHA256: idSHA}))
	_, ok := ResolveContentAnchor(led.Get(key), path, int64(len(body)), 1530)
	require.False(t, ok, "旧false/0字段没有证明物理起点，不能当成精确锚")
	require.NoError(t, led.RegisterSegment(key, ledger.Segment{Path: path, Kind: ledger.SegmentRotatedText, StartPos: 0, EndPos: 10, Imported: true, IdentityBytes: 3, IdentitySHA256: "old-slot-content"}))
	result, err := imp.ImportText(path)
	require.NoError(t, err)
	require.Equal(t, ArchiveUnknown, result.Disposition)
	require.Empty(t, result.Events)
	require.Equal(t, 1, led.UnresolvedGapCount(key))
}

func TestSixFixLegacyReanchoredCoveredTailCannotReassignPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.log.1")
	// 旧状态真实映射为 physical=100 对应 logical=1000，后续50字节已经耐久。
	// 字段缺失时不能错算成 covered=50 后重导物理[50,150)并再造logical[1050,1150)。
	body := strings.Repeat("p", 99) + "\n" + strings.Repeat("t", 49) + "\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0600))
	key, led := testKey("six-old-tail-mapping", "g1"), ledger.New()
	imp := NewArchiveImporter(led, key)
	sha, err := filePrefixSHA256(path, 64)
	require.NoError(t, err)
	require.NoError(t, led.AdvanceRead(key, 1050))
	require.NoError(t, led.AdvanceDurable(key, 1050))
	require.NoError(t, led.RegisterSegment(key, ledger.Segment{Path: "legacy-live", Kind: ledger.SegmentLive, StartPos: 1000, EndPos: 1050, IdentityBytes: 64, IdentitySHA256: sha}))
	require.NoError(t, led.RegisterSegment(key, ledger.Segment{Path: path, Kind: ledger.SegmentRotatedText, StartPos: 0, EndPos: 10, Imported: true, IdentityBytes: 3, IdentitySHA256: "old-slot-content"}))
	result, err := imp.ImportText(path)
	require.NoError(t, err)
	require.Equal(t, ArchiveUnknown, result.Disposition)
	require.Empty(t, result.Events)
	require.Equal(t, 1, led.UnresolvedGapCount(key))
	require.False(t, led.Get(key).Segments[0].PhysicalMappingKnown)
}

func TestSixFixLegacyNonzeroLiveMappingCannotRereadPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "latest.log")
	require.NoError(t, os.WriteFile(path, []byte("surviving-durable-prefix\n"), 0600))
	key, led := testKey("six-old-live-mapping", "g1"), ledger.New()
	wal := NewWAL(led, key)
	idBytes, idSHA, err := ContentIdentity(path)
	require.NoError(t, err)
	require.NoError(t, led.AdvanceRead(key, 1530))
	require.NoError(t, led.AdvanceDurable(key, 1530))
	require.NoError(t, led.RegisterSegment(key, ledger.Segment{Path: path, Kind: ledger.SegmentLive, StartPos: 1530, EndPos: 1530, IdentityBytes: idBytes, IdentitySHA256: idSHA}))
	for round := 1; round <= 3; round++ {
		tailer := NewFileTailer(led, key, path, wal, nil)
		events, err := tailer.Poll()
		require.Error(t, err)
		require.Empty(t, events, "不能将未知残留前缀按新的逻辑身份重读")
		require.Equal(t, 1, led.UnresolvedGapCount(key))
		entry := led.Get(key)
		require.False(t, entry.Segments[0].PhysicalMappingKnown)
		led2 := ledger.New()
		require.NoError(t, led2.Restore(led.Snapshot()))
		led = led2
		wal = NewWAL(led, key)
	}
}

func TestSixFixLegacyArchiveCheckpointCannotUpgradeUnknownMapping(t *testing.T) {
	for _, kind := range []ledger.SegmentKind{ledger.SegmentRotated, ledger.SegmentRotatedText} {
		path := filepath.Join(t.TempDir(), "proxy.log.1")
		require.NoError(t, os.WriteFile(path, []byte("surviving-durable-prefix\n"), 0600))
		key, led := testKey("six-old-archive-mapping", "g1"), ledger.New()
		imp := NewArchiveImporter(led, key)
		idBytes, idSHA, err := ContentIdentity(path)
		require.NoError(t, err)
		require.NoError(t, led.AdvanceRead(key, 1530))
		require.NoError(t, led.AdvanceDurable(key, 1530))
		require.NoError(t, led.RegisterSegment(key, ledger.Segment{Path: path, Kind: kind, StartPos: 1530, EndPos: 1530, IdentityBytes: idBytes, IdentitySHA256: idSHA}))
		result, err := imp.ImportText(path)
		require.NoError(t, err)
		require.Equal(t, ArchiveUnknown, result.Disposition)
		require.Empty(t, result.Events)
		require.False(t, archiveSegmentOf(led, key, path).PhysicalMappingKnown)
		require.Equal(t, 1, led.UnresolvedGapCount(key))
	}
}
