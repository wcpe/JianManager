package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/archive"
)

// 本文件钉住四条硬约束。
//
// 四条里最容易在「优化」时被悄悄突破的是第 4 条（本地 gz 一律不动）：
// 吸存成功后删本地看上去很省空间，但那越过了本规格的边界——
// 这些 .gz 是轮转后的原始证据、用户明确要留的最后备份。
// 所以这里把它写成可执行断言，而不是只写在注释里。

func newAdoptTestRegistry(t *testing.T, root string) *archive.Registry {
	t.Helper()
	return archive.NewRegistry(archive.NewLocalArchive(root))
}

// makeGz 造一个「内容可辨识」的 .gz 文件。
//
// 内容不需要是真的 gzip：吸存按规格是「以文件身份原样保存」，不解析内容。
// 这一点本身也是约束——如果实现偷偷解压/重压，object_id 会变，
// 「同一文件不产生第二份」的去重就会失效。
func makeGz(t *testing.T, dir, name, content string, mtime time.Time) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	require.NoError(t, os.Chtimes(p, mtime, mtime))
	return p
}

// TestInstanceGzAdoptRegistersFilesVerbatim 验证约束 1+2：原样吸存、不解析。
func TestInstanceGzAdoptRegistersFilesVerbatim(t *testing.T) {
	archiveRoot := t.TempDir()
	workDir := t.TempDir()
	reg := newAdoptTestRegistry(t, archiveRoot)

	mtime := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	content := "pretend-gzip-bytes-not-a-valid-gzip-stream"
	makeGz(t, filepath.Join(workDir, "logs"), "2026-10-26-1.log.gz", content, mtime)

	a := &instanceGzAdopter{
		registry:     reg,
		instanceDirs: func() map[string]string { return map[string]string{"uuid-a": workDir} },
		now:          func() time.Time { return mtime },
	}
	stats, err := a.AdoptOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, stats.Scanned)
	require.Equal(t, 1, stats.Registered)
	require.Zero(t, stats.Failed)

	// 分区键用 mtime 的 UTC 日期
	key := archive.PartitionKey{StorageNamespace: "inst:uuid-a", UTCDay: "2026-10-26", Generation: 1}
	objects, err := archive.NewLocalArchive(archiveRoot).List(context.Background(), key)
	require.NoError(t, err)
	require.NotEmpty(t, objects, "归档里应出现该文件")

	// 原样：存下来的字节与源文件逐字节相等（没解压、没重压、没改一个字节）
	found := false
	for _, rel := range objects {
		got, err := archive.NewLocalArchive(archiveRoot).Get(context.Background(), key, rel)
		require.NoError(t, err)
		if string(got) == content {
			found = true
		}
	}
	require.True(t, found, "必须能找到与源文件逐字节相同的对象（原样吸存）")
}

// TestInstanceGzAdoptIsIdempotent 验证约束 3：同一文件重复吸存不产生第二份。
func TestInstanceGzAdoptIsIdempotent(t *testing.T) {
	archiveRoot := t.TempDir()
	workDir := t.TempDir()
	reg := newAdoptTestRegistry(t, archiveRoot)

	mtime := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	makeGz(t, filepath.Join(workDir, "logs"), "a.log.gz", "same-bytes", mtime)

	a := &instanceGzAdopter{
		registry:     reg,
		instanceDirs: func() map[string]string { return map[string]string{"uuid-b": workDir} },
		now:          func() time.Time { return mtime },
	}

	first, err := a.AdoptOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, first.Registered)
	require.Zero(t, first.AlreadyRegistered)

	second, err := a.AdoptOnce(context.Background())
	require.NoError(t, err)
	require.Zero(t, second.Registered, "第二轮不得再登记")
	require.Equal(t, 1, second.AlreadyRegistered, "应命中幂等去重")

	// 分区内只有一个**数据对象**（不是两份）。
	// 注意 List 会把 manifest.json 一起列出来——那是分区的索引，不是被吸存的文件；
	// 断言去重要数数据对象，不能把清单也算进去（那样会永远得到 2，是一种假红）。
	key := archive.PartitionKey{StorageNamespace: "inst:uuid-b", UTCDay: "2026-10-26", Generation: 1}
	objects, err := archive.NewLocalArchive(archiveRoot).List(context.Background(), key)
	require.NoError(t, err)
	dataObjects := 0
	for _, rel := range objects {
		if rel != archive.ManifestRelPath {
			dataObjects++
		}
	}
	require.Equal(t, 1, dataObjects, "幂等去重失效会得到 2 份数据对象，实际 %v", objects)
}

// TestInstanceGzAdoptDoesNotTouchLocalFiles 验证约束 4（最重要的一条）。
func TestInstanceGzAdoptDoesNotTouchLocalFiles(t *testing.T) {
	archiveRoot := t.TempDir()
	workDir := t.TempDir()
	reg := newAdoptTestRegistry(t, archiveRoot)

	mtime := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	src := makeGz(t, filepath.Join(workDir, "logs"), "keep-me.log.gz", "payload", mtime)
	before, err := os.Stat(src)
	require.NoError(t, err)

	a := &instanceGzAdopter{
		registry:     reg,
		instanceDirs: func() map[string]string { return map[string]string{"uuid-c": workDir} },
		now:          func() time.Time { return mtime },
	}
	// 跑两轮：如果实现里有「吸存成功后删本地」，第二轮就会看不到文件
	for i := 0; i < 2; i++ {
		stats, err := a.AdoptOnce(context.Background())
		require.NoError(t, err)
		require.Zero(t, stats.LocalDeleted, "读数里 localDeleted 必须恒为 0")
	}

	after, err := os.Stat(src)
	require.NoError(t, err, "本地 .gz 必须仍然存在（第 4 条）")
	require.Equal(t, before.Size(), after.Size(), "本地 .gz 大小不得变化")
	require.True(t, before.ModTime().Equal(after.ModTime()), "本地 .gz mtime 不得被改（否则会被当成新文件）")
}

// TestInstanceGzAdoptIgnoresNonGzAndMissingDir 验证范围边界：
// 只收 .gz，且「实例还没有 logs 目录」不是错误。
func TestInstanceGzAdoptIgnoresNonGzAndMissingDir(t *testing.T) {
	archiveRoot := t.TempDir()
	workDir := t.TempDir()
	reg := newAdoptTestRegistry(t, archiveRoot)

	logsDir := filepath.Join(workDir, "logs")
	require.NoError(t, os.MkdirAll(logsDir, 0o755))
	// 非 .gz 一律不收（latest.log 是采集侧的事，不属于吸存）
	require.NoError(t, os.WriteFile(filepath.Join(logsDir, "latest.log"), []byte("x"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(logsDir, "sub.gz"), 0o755)) // 目录即使叫 .gz 也不收

	mtime := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	a := &instanceGzAdopter{
		registry: reg,
		instanceDirs: func() map[string]string {
			return map[string]string{
				"uuid-d": workDir,
				"uuid-e": filepath.Join(workDir, "does-not-exist"), // logs 目录不存在
			}
		},
		now: func() time.Time { return mtime },
	}
	stats, err := a.AdoptOnce(context.Background())
	require.NoError(t, err, "logs 目录不存在不是错误")
	require.Zero(t, stats.Scanned, "只有 .gz 文件才算候选，实际扫到 %d", stats.Scanned)
	require.Zero(t, stats.Failed)
}

// TestInstanceGzAdoptNilRegistryIsSafe 验证未装配时不会 panic。
func TestInstanceGzAdoptNilRegistryIsSafe(t *testing.T) {
	require.Nil(t, newInstanceGzAdopter(nil, nil))
	var a *instanceGzAdopter
	stats, err := a.AdoptOnce(context.Background())
	require.NoError(t, err)
	require.Zero(t, stats.Scanned)
}
