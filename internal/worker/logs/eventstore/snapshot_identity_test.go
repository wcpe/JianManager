package eventstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func TestLazySnapshotRejectsReplacedOrShortenedSegmentBeforeCallback(t *testing.T) {
	for _, change := range []string{"replace_same_shape", "shorten", "remove"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			key := "inst:1/file/g1"
			store, err := Open(root)
			require.NoError(t, err)
			require.NoError(t, store.Append(key, []logtypes.Event{ev(1)}))
			snapshot, err := store.Snapshot(key)
			require.NoError(t, err)
			t.Cleanup(func() { _ = snapshot.Close() })
			require.NoError(t, store.Close())
			path := filepath.Join(root, sectionDirName(key), snapshot.Segments()[0].Name)
			switch change {
			case "replace_same_shape":
				replacement := ev(1)
				replacement.Message = "swap-1"
				replacement.CanonicalHash = logtypes.CanonicalContentHash(replacement.EventTimeUTC, replacement.Level, replacement.Stream, replacement.Message)
				body, err := json.Marshal(replacement)
				require.NoError(t, err)
				body = append(body, '\n')
				require.EqualValues(t, snapshot.Segments()[0].Bytes, len(body), "同计数、字节数和首尾身份仍不能代替文件身份")
				temporary := path + ".replacement"
				require.NoError(t, os.WriteFile(temporary, body, 0o600))
				require.NoError(t, os.Rename(temporary, path))
			case "shorten":
				require.NoError(t, os.Truncate(path, 0))
			case "remove":
				require.NoError(t, os.Remove(path))
			}
			seen := 0
			err = snapshot.Iterate(func(logtypes.Event) error { seen++; return nil })
			require.ErrorIs(t, err, ErrCorrupt)
			require.Zero(t, seen, "惰性开段必须先复核身份与完整字节界，再接纳正文")
		})
	}
}
