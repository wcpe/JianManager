package grpc

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// readZipEntries 解出 zip 字节流的「条目名→内容」映射，供断言。
func readZipEntries(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	require.NoError(t, err)
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)
		b, err := io.ReadAll(rc)
		_ = rc.Close()
		require.NoError(t, err)
		out[f.Name] = string(b)
	}
	return out
}

func TestZipEntryName(t *testing.T) {
	// 单文件：zip 内为该文件名（basename）。
	require.Equal(t, "server.properties", zipEntryName("server.properties", "server.properties"))
	// 目录条目：以所选条目为根的相对路径。
	require.Equal(t, "plugins/Essentials/config.yml", zipEntryName("plugins", filepath.Join("plugins", "Essentials", "config.yml")))
	// 嵌套所选条目：根取所选条目最后一段。
	require.Equal(t, "Essentials/config.yml", zipEntryName(filepath.Join("plugins", "Essentials"), filepath.Join("plugins", "Essentials", "config.yml")))
	// 选中目录自身（rel == sel）：取 basename。
	require.Equal(t, "plugins", zipEntryName("plugins", "plugins"))
}

func TestWriteZipArchive_FilesAndDirs(t *testing.T) {
	work := t.TempDir()
	writeCloneTmp(t, filepath.Join(work, "server.properties"), "server-port=25565\n")
	writeCloneTmp(t, filepath.Join(work, "plugins", "Essentials", "config.yml"), "enabled: true\n")
	writeCloneTmp(t, filepath.Join(work, "plugins", "readme.txt"), "hello")
	writeCloneTmp(t, filepath.Join(work, "world", "level.dat"), "DAT")

	var buf bytes.Buffer
	err := writeZipArchive(&buf, work, []string{"server.properties", "plugins"})
	require.NoError(t, err)

	entries := readZipEntries(t, buf.Bytes())
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)

	require.Equal(t, []string{
		"plugins/Essentials/config.yml",
		"plugins/readme.txt",
		"server.properties",
	}, names)
	require.Equal(t, "server-port=25565\n", entries["server.properties"])
	require.Equal(t, "enabled: true\n", entries["plugins/Essentials/config.yml"])
	// 未选中的 world/ 不应在包内。
	require.NotContains(t, entries, "world/level.dat")
}

func TestWriteZipArchive_SingleFile(t *testing.T) {
	work := t.TempDir()
	writeCloneTmp(t, filepath.Join(work, "config", "spigot.yml"), "x: 1\n")

	var buf bytes.Buffer
	err := writeZipArchive(&buf, work, []string{filepath.Join("config", "spigot.yml")})
	require.NoError(t, err)

	entries := readZipEntries(t, buf.Bytes())
	// 单文件入包，条目名为 basename。
	require.Equal(t, map[string]string{"spigot.yml": "x: 1\n"}, entries)
}

func TestWriteZipArchive_RejectsTraversal(t *testing.T) {
	work := t.TempDir()
	// 在 work 之外放一个文件，确保不会被打包出去。
	outside := filepath.Join(filepath.Dir(work), "secret.txt")
	require.NoError(t, os.WriteFile(outside, []byte("top-secret"), 0o644))
	t.Cleanup(func() { _ = os.Remove(outside) })

	var buf bytes.Buffer
	err := writeZipArchive(&buf, work, []string{filepath.Join("..", filepath.Base(outside))})
	require.Error(t, err)
}

func TestWriteZipArchive_EmptyPaths(t *testing.T) {
	work := t.TempDir()
	var buf bytes.Buffer
	err := writeZipArchive(&buf, work, nil)
	require.Error(t, err)
}

func TestWriteZipArchive_MissingEntry(t *testing.T) {
	work := t.TempDir()
	var buf bytes.Buffer
	err := writeZipArchive(&buf, work, []string{"does-not-exist.yml"})
	require.Error(t, err)
}
