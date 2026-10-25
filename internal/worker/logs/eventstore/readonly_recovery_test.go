package eventstore

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 取证入口显式选择只读实例，不能借用带持久校准能力的采集实例。
func openForensicStoreForTest(root string) (*Store, error) {
	return OpenReadOnly(root)
}

func TestForensicReadKeepsPartialTailAndManifestBytesAcrossThreeReopens(t *testing.T) {
	for _, first := range []string{"iterate", "count", "snapshot"} {
		t.Run(first, func(t *testing.T) {
			root := t.TempDir()
			key := "inst:1/file/g1"
			store, err := Open(root)
			require.NoError(t, err)
			require.NoError(t, store.Append(key, []logtypes.Event{ev(1), ev(2)}))
			require.NoError(t, store.Close())
			path := filepath.Join(root, sectionDirName(key), store.Segments(key)[0].Name)
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
			require.NoError(t, err)
			_, err = file.WriteString(`{"message":"另一个写者尚未完成`)
			require.NoError(t, err)
			require.NoError(t, file.Sync())
			require.NoError(t, file.Close())
			before := sectionTreeFingerprint(t, root)
			for round := 1; round <= 3; round++ {
				forensic, err := openForensicStoreForTest(root)
				require.NoError(t, err)
				switch first {
				case "iterate":
					require.NoError(t, forensic.Iterate(key, nil))
				case "count":
					count, err := forensic.Count(key)
					require.NoError(t, err)
					require.Equal(t, 2, count)
				case "snapshot":
					snapshot, err := forensic.Snapshot(key)
					require.NoError(t, err)
					require.Equal(t, 2, snapshot.Count())
					require.NoError(t, snapshot.Close())
				}
				got := collect(t, forensic, key)
				require.Len(t, got, 2)
				require.Equal(t, []string{ev(1).EventID, ev(2).EventID}, []string{got[0].EventID, got[1].EventID})
				require.NoError(t, forensic.Close())
				assert.Equal(t, before, sectionTreeFingerprint(t, root), "只读取证第%d轮不能截尾或发布清单", round)
			}
		})
	}
}

func TestForensicModeDoesNotCreateRootOrAcceptAppend(t *testing.T) {
	t.Run("missing_root", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "missing")
		store, err := openForensicStoreForTest(root)
		if store != nil {
			_ = store.Close()
		}
		assert.ErrorIs(t, err, os.ErrNotExist)
		_, statErr := os.Stat(root)
		assert.ErrorIs(t, statErr, os.ErrNotExist, "只读打开不能创建目录")
	})
	t.Run("append", func(t *testing.T) {
		root := t.TempDir()
		store, err := openForensicStoreForTest(root)
		require.NoError(t, err)
		before := sectionTreeFingerprint(t, root)
		assert.ErrorIs(t, store.Append("inst:1/file/g1", []logtypes.Event{ev(1)}), ErrReadOnly, "只读实例必须明确拒绝追加")
		assert.ErrorIs(t, store.Append("inst:1/file/g1", nil), ErrReadOnly)
		require.NoError(t, store.Close())
		assert.Equal(t, before, sectionTreeFingerprint(t, root))
	})
}

func TestForensicReadWorksWithReadOnlyFilePermissions(t *testing.T) {
	root := t.TempDir()
	key := "inst:1/file/g1"
	store, err := Open(root)
	require.NoError(t, err)
	require.NoError(t, store.Append(key, []logtypes.Event{ev(1)}))
	require.NoError(t, store.Close())
	dir := filepath.Join(root, sectionDirName(key))
	files, err := os.ReadDir(dir)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.Chmod(root, 0o700)
		_ = os.Chmod(dir, 0o700)
		for _, file := range files {
			_ = os.Chmod(filepath.Join(dir, file.Name()), 0o600)
		}
	})
	for _, file := range files {
		require.NoError(t, os.Chmod(filepath.Join(dir, file.Name()), 0o444))
	}
	require.NoError(t, os.Chmod(dir, 0o555))
	require.NoError(t, os.Chmod(root, 0o555))
	before := sectionTreeFingerprint(t, root)
	forensic, err := openForensicStoreForTest(root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = forensic.Close() })
	got := collect(t, forensic, key)
	require.Len(t, got, 1)
	require.Equal(t, ev(1).EventID, got[0].EventID)
	require.NoError(t, forensic.Close())
	require.Equal(t, before, sectionTreeFingerprint(t, root))
}

func TestForensicReadDoesNotBreakPausedWriterTail(t *testing.T) {
	root := t.TempDir()
	key := "inst:1/file/g1"
	store, err := Open(root)
	require.NoError(t, err)
	require.NoError(t, store.Append(key, []logtypes.Event{ev(1)}))
	require.NoError(t, store.Close())
	path := filepath.Join(root, sectionDirName(key), store.Segments(key)[0].Name)
	writer, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { _ = writer.Close() })
	data, err := json.Marshal(ev(2))
	require.NoError(t, err)
	middle := len(data) / 2
	_, err = writer.Write(data[:middle])
	require.NoError(t, err)
	require.NoError(t, writer.Sync())
	before := sectionTreeFingerprint(t, root)
	forensic, err := openForensicStoreForTest(root)
	require.NoError(t, err)
	require.Len(t, collect(t, forensic, key), 1)
	require.NoError(t, forensic.Close())
	assert.Equal(t, before, sectionTreeFingerprint(t, root), "只读者不能替另一个仍活跃的写者修复半行")
	_, err = writer.Write(append(data[middle:], '\n'))
	require.NoError(t, err)
	require.NoError(t, writer.Sync())
	require.NoError(t, writer.Close())
	completed, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.True(t, bytes.HasSuffix(completed, append(data, '\n')), "写者完成后原事件帧必须仍完整")
	forensic, err = openForensicStoreForTest(root)
	require.NoError(t, err)
	got := collect(t, forensic, key)
	require.NoError(t, forensic.Close())
	require.Len(t, got, 2)
	require.Equal(t, ev(2).EventID, got[1].EventID)
}

func TestForensicCorruptCompleteLineFailsWithoutRepair(t *testing.T) {
	root := t.TempDir()
	key := "inst:1/file/g1"
	store, err := Open(root)
	require.NoError(t, err)
	require.NoError(t, store.Append(key, []logtypes.Event{ev(1)}))
	require.NoError(t, store.Close())
	path := filepath.Join(root, sectionDirName(key), store.Segments(key)[0].Name)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = file.WriteString("{错误的完整行}\n")
	require.NoError(t, err)
	require.NoError(t, file.Close())
	before := sectionTreeFingerprint(t, root)
	forensic, err := openForensicStoreForTest(root)
	require.NoError(t, err)
	require.ErrorIs(t, forensic.Iterate(key, nil), ErrCorrupt)
	require.NoError(t, forensic.Close())
	require.Equal(t, before, sectionTreeFingerprint(t, root))
}
