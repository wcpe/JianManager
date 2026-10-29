package acquire

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

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
