package grpc

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// makeTestZip 在 dir 下生成一个 zip/jar，内容为 entries（条目名→内容）；返回归档绝对路径。
func makeTestZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, content := range entries {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
}

func TestListZipEntries(t *testing.T) {
	work := t.TempDir()
	jar := filepath.Join(work, "plugin.jar")
	makeTestZip(t, jar, map[string]string{
		"plugin.yml":              "name: Foo\nmain: com.example.Foo\n",
		"config.yml":              "enabled: true\n",
		"com/example/Foo.class":   "\x00\x01\x02binary",
		"META-INF/MANIFEST.MF":    "Manifest-Version: 1.0\n",
	})

	entries, truncated, err := listZipEntries(jar)
	require.NoError(t, err)
	require.False(t, truncated)

	names := map[string]*struct{ size int64 }{}
	for _, e := range entries {
		s := e.Size
		names[e.Name] = &struct{ size int64 }{size: s}
	}
	require.Contains(t, names, "plugin.yml")
	require.Contains(t, names, "config.yml")
	require.Contains(t, names, "com/example/Foo.class")
	require.Contains(t, names, "META-INF/MANIFEST.MF")
	// plugin.yml 解压后大小应等于其原文长度。
	require.EqualValues(t, len("name: Foo\nmain: com.example.Foo\n"), names["plugin.yml"].size)
}

func TestListZipEntries_BadArchive(t *testing.T) {
	work := t.TempDir()
	notzip := filepath.Join(work, "broken.jar")
	require.NoError(t, os.WriteFile(notzip, []byte("not a zip at all"), 0o644))

	_, _, err := listZipEntries(notzip)
	require.Error(t, err)
}

func TestReadZipEntry_Text(t *testing.T) {
	work := t.TempDir()
	jar := filepath.Join(work, "plugin.jar")
	makeTestZip(t, jar, map[string]string{
		"plugin.yml": "name: Foo\n",
	})

	content, truncated, err := readZipEntry(jar, "plugin.yml", maxArchiveEntryBytes)
	require.NoError(t, err)
	require.False(t, truncated)
	require.Equal(t, "name: Foo\n", string(content))
}

func TestReadZipEntry_Truncated(t *testing.T) {
	work := t.TempDir()
	jar := filepath.Join(work, "big.zip")
	makeTestZip(t, jar, map[string]string{
		"big.txt": "0123456789abcdef", // 16 字节
	})

	// limit=8：应截断到 8 字节并 truncated=true。
	content, truncated, err := readZipEntry(jar, "big.txt", 8)
	require.NoError(t, err)
	require.True(t, truncated)
	require.Equal(t, "01234567", string(content))
}

func TestReadZipEntry_Missing(t *testing.T) {
	work := t.TempDir()
	jar := filepath.Join(work, "plugin.jar")
	makeTestZip(t, jar, map[string]string{"plugin.yml": "x\n"})

	_, _, err := readZipEntry(jar, "no-such-entry.txt", maxArchiveEntryBytes)
	require.Error(t, err)
}

func TestIsSafeArchiveEntryName(t *testing.T) {
	require.True(t, isSafeArchiveEntryName("plugin.yml"))
	require.True(t, isSafeArchiveEntryName("com/example/Foo.class"))
	require.True(t, isSafeArchiveEntryName("META-INF/MANIFEST.MF"))
	// zip-slip：逃逸、绝对路径、盘符一律拒绝。
	require.False(t, isSafeArchiveEntryName("../evil.sh"))
	require.False(t, isSafeArchiveEntryName("a/../../evil"))
	require.False(t, isSafeArchiveEntryName("/etc/passwd"))
	require.False(t, isSafeArchiveEntryName("c:/windows/system32"))
	require.False(t, isSafeArchiveEntryName(""))
}

func TestLooksBinary(t *testing.T) {
	require.False(t, looksBinary([]byte("plain text yaml: true")))
	require.True(t, looksBinary([]byte("class\x00\xca\xfe\xba\xbe")))
}

func TestIsArchivePath(t *testing.T) {
	require.True(t, isArchivePath("plugins/Foo.jar"))
	require.True(t, isArchivePath("backup.zip"))
	require.True(t, isArchivePath("UPPER.JAR"))
	require.False(t, isArchivePath("server.properties"))
	require.False(t, isArchivePath("config.yml"))
}
