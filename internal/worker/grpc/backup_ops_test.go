package grpc

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wxys233/JianManager/proto/workerpb"
)

// writeMaliciousArchive 手工写一个含越界路径条目（../escape.txt）的 tar.gz，用于测试 zip-slip 防御。
func writeMaliciousArchive(t *testing.T, absArchive string) {
	t.Helper()
	out, err := os.Create(absArchive)
	require.NoError(t, err)
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	body := []byte("pwned")
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "../escape.txt",
		Mode:     0o644,
		Size:     int64(len(body)),
		Typeflag: tar.TypeReg,
	}))
	_, err = tw.Write(body)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	require.NoError(t, out.Close())
}

// writeFile 在 dir 下写文件并设定固定 mtime，便于增量差异断言。
func writeFile(t *testing.T, dir, rel, content string, mtime time.Time) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	require.NoError(t, os.Chtimes(p, mtime, mtime))
}

func manifestMap(m []*workerpb.BackupManifestEntry) map[string]*workerpb.BackupManifestEntry {
	out := map[string]*workerpb.BackupManifestEntry{}
	for _, e := range m {
		out[e.Path] = e
	}
	return out
}

// TestWriteBackupArchive_FullCapturesAllFiles 全量备份打包全部常规文件并产出完整清单。
func TestWriteBackupArchive_FullCapturesAllFiles(t *testing.T) {
	work := t.TempDir()
	now := time.Unix(1_700_000_000, 0)
	writeFile(t, work, "server.properties", "a=1", now)
	writeFile(t, work, "world/level.dat", "world-data", now)

	archive := filepath.Join(t.TempDir(), "full.tar.gz")
	manifest, packed, size, err := writeBackupArchive(archive, work, nil, false)
	require.NoError(t, err)
	require.Equal(t, int64(2), packed)
	require.Greater(t, size, int64(0))

	m := manifestMap(manifest)
	require.Contains(t, m, "server.properties")
	require.Contains(t, m, "world/level.dat")
	require.Equal(t, int64(3), m["server.properties"].Size)
}

// TestWriteBackupArchive_IncrementalSkipsUnchanged 增量仅打包变化文件，清单仍为全量。
func TestWriteBackupArchive_IncrementalSkipsUnchanged(t *testing.T) {
	work := t.TempDir()
	now := time.Unix(1_700_000_000, 0)
	writeFile(t, work, "a.txt", "aaa", now)
	writeFile(t, work, "b.txt", "bbb", now)

	// 第一次全量，拿到基准清单。
	base := filepath.Join(t.TempDir(), "base.tar.gz")
	baseManifest, _, _, err := writeBackupArchive(base, work, nil, false)
	require.NoError(t, err)

	// 改动 a.txt（内容变长 + mtime 变），b.txt 不动。
	later := now.Add(time.Hour)
	writeFile(t, work, "a.txt", "aaaa-changed", later)

	inc := filepath.Join(t.TempDir(), "inc.tar.gz")
	incManifest, packed, _, err := writeBackupArchive(inc, work, manifestMap(baseManifest), true)
	require.NoError(t, err)
	// 仅 a.txt 被打包。
	require.Equal(t, int64(1), packed)
	// 清单仍含两文件（链回放与下次增量基准需要完整视图）。
	require.Len(t, incManifest, 2)
}

// TestRestoreChain_FullThenIncremental 链式恢复：全量基 + 增量按序回放得到最终态。
func TestRestoreChain_FullThenIncremental(t *testing.T) {
	work := t.TempDir()
	now := time.Unix(1_700_000_000, 0)
	writeFile(t, work, "keep.txt", "v1", now)
	writeFile(t, work, "edit.txt", "old", now)

	full := filepath.Join(t.TempDir(), "full.tar.gz")
	baseManifest, _, _, err := writeBackupArchive(full, work, nil, false)
	require.NoError(t, err)

	// 增量：仅 edit.txt 改为 new。
	later := now.Add(time.Hour)
	writeFile(t, work, "edit.txt", "new-content", later)
	inc := filepath.Join(t.TempDir(), "inc.tar.gz")
	_, packed, _, err := writeBackupArchive(inc, work, manifestMap(baseManifest), true)
	require.NoError(t, err)
	require.Equal(t, int64(1), packed)

	// 在一个干净目录里按链顺序回放。
	restore := t.TempDir()
	n1, err := extractBackupArchive(full, restore)
	require.NoError(t, err)
	require.Equal(t, int64(2), n1)
	n2, err := extractBackupArchive(inc, restore)
	require.NoError(t, err)
	require.Equal(t, int64(1), n2)

	// keep.txt 来自全量，edit.txt 被增量覆盖为最新。
	keep, err := os.ReadFile(filepath.Join(restore, "keep.txt"))
	require.NoError(t, err)
	require.Equal(t, "v1", string(keep))
	edit, err := os.ReadFile(filepath.Join(restore, "edit.txt"))
	require.NoError(t, err)
	require.Equal(t, "new-content", string(edit))
}

// TestExtractBackupArchive_RejectsZipSlip 解压拒绝越界条目（路径穿越防御）。
func TestExtractBackupArchive_RejectsZipSlip(t *testing.T) {
	// 构造一个包含 ../escape 条目的归档：直接复用 writeBackupArchive 无法制造越界，
	// 故手工写一个带越界路径的 tar.gz。
	work := t.TempDir()
	archive := filepath.Join(t.TempDir(), "evil.tar.gz")
	writeMaliciousArchive(t, archive)

	_, err := extractBackupArchive(archive, work)
	require.Error(t, err)
	require.Contains(t, err.Error(), "越界")
}
