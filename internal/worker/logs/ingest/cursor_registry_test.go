package ingest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// TestCursorRegistryRoundTrip 锤住「落盘 → 读回」的无损性。
//
// 这是裁定 D 的地基：只要这一条不对，重启就会要么丢字节、要么重复投递。
func TestCursorRegistryRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ingest.cursor")
	in := cursorRegistry{
		"inst:1/g1": {Path: "/srv/inst1/logs/latest.log", DeviceID: 64768, Inode: 1234567, Cursor: 8192, LogicalPos: 40960},
		"node:2/g3": {Path: "/var/log/worker.log", Cursor: 0, LogicalPos: 0},
	}
	require.NoError(t, saveCursorRegistry(path, in))

	out := loadCursorRegistry(path)
	require.Len(t, out, 2)
	require.Equal(t, in["inst:1/g1"], out["inst:1/g1"])
	require.Equal(t, in["node:2/g3"], out["node:2/g3"])
}

// TestCursorRegistryMissingFileIsEmpty 文件不存在 ⇒ 空表 + 不报错。
// 游标是可丢失的优化：**绝不能**因为读不回来而拒绝启动采集。
func TestCursorRegistryMissingFileIsEmpty(t *testing.T) {
	out := loadCursorRegistry(filepath.Join(t.TempDir(), "nope.cursor"))
	require.Empty(t, out)
}

// TestCursorRegistryCorruptLineSkipped 单行坏了不牵连整表（否则一次写坏会丢全部源的游标）。
func TestCursorRegistryCorruptLineSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ingest.cursor")
	content := "# header\n" +
		"inst:1/g1\t/srv/a.log\t64768\t111\t100\t500\n" +
		"这是坏行没有制表符\n" +
		"inst:2/g1\t/srv/b.log\tnotanumber\t222\t100\t500\n" +
		"inst:3/g1\t/srv/c.log\t64768\t333\t200\t900\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	out := loadCursorRegistry(path)
	require.Len(t, out, 2, "两条合法行必须保留，坏行跳过")
	require.Equal(t, int64(100), out["inst:1/g1"].Cursor)
	require.Equal(t, int64(200), out["inst:3/g1"].Cursor)
}

// TestResumeCursorForRequiresMatchingPath 路径对不上就不给续读点（换绑定的源必须重新从 EOF 起）。
func TestResumeCursorForRequiresMatchingPath(t *testing.T) {
	m := &Manager{cursorRegistry: cursorRegistry{
		"inst:1/g1": {Path: "/old/path.log", Cursor: 100},
	}}
	require.Nil(t, m.resumeCursorFor("inst:1/g1", "/new/path.log"), "路径变了不得复用旧游标")
	require.Nil(t, m.resumeCursorFor("inst:9/g1", "/old/path.log"), "没有游标返回 nil")

	got := m.resumeCursorFor("inst:1/g1", "/old/path.log")
	require.NotNil(t, got)
	require.Equal(t, int64(100), got.Cursor)
}

// TestManagerFlushCursorRegistryWritesFile 采集轮尾部刷盘写出的文件必须能被读回。
//
// 2026-10-30 修正：本用例原先根本没有调 flushCursorRegistry——它直接调 saveCursorRegistry
// 把一张手搭的表写出去，只验了序列化，名字与内容不符。现在走真实入口：建真管道 → 让它读几行 →
// flush → 读回盘上文件，并从**盘上那条游标**重建一个新管道验续读位置。
func TestManagerFlushCursorRegistryWritesFile(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	content := "line-1\nline-2\n"
	require.NoError(t, os.WriteFile(logPath, []byte(content), 0o644))

	pipe, err := pipeline.New(pipeline.Options{
		Mode: pipeline.ModeFilePrimary, LogSourceID: "inst:1", SourceGeneration: "g1",
		Path: logPath, Stream: "stdout",
	})
	require.NoError(t, err)
	ev, err := pipe.Poll()
	require.NoError(t, err)
	require.Len(t, ev, 2)

	m := &Manager{root: root, pipes: map[string]*pipeline.Pipeline{"inst:1/g1": pipe}}
	m.flushCursorRegistry()

	back := loadCursorRegistry(m.cursorRegistryPath())
	saved, ok := back["inst:1/g1"]
	require.True(t, ok, "刷盘必须把本源的游标写进去")
	require.Equal(t, logPath, saved.Path)
	require.EqualValues(t, len(content), saved.Cursor, "游标必须等于已读字节数")
	require.EqualValues(t, len(content), saved.LogicalPos)

	// 盘上那条游标必须是**能用的**：续读到的新管道不得重读已有两行。
	require.NoError(t, os.WriteFile(logPath, []byte(content+"line-3\n"), 0o644))
	next := m.resumeCursorFor("inst:1/g1", logPath)
	require.NotNil(t, next)
	resumed, err := pipeline.New(pipeline.Options{
		Mode: pipeline.ModeFilePrimary, LogSourceID: "inst:1", SourceGeneration: "g1",
		Path: logPath, Stream: "stdout", StartAtEOF: true, ResumeCursor: next,
	})
	require.NoError(t, err)
	ev2, err := resumed.Poll()
	require.NoError(t, err)
	require.Len(t, ev2, 1, "刷盘的游标必须能让下一个进程只读到新增行")
	require.Equal(t, "line-3", ev2[0].Message)
}

// TestManagerFlushCursorRegistryZeroValueSurvives 零值 Manager（游标表还是 nil）下刷盘不得 panic，
// 且必须把表初始化好——ticker 会在任何时刻调它，一条 panic 就能把整个采集进程带走。
func TestManagerFlushCursorRegistryZeroValueSurvives(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, []byte("x\n"), 0o644))
	pipe, err := pipeline.New(pipeline.Options{
		Mode: pipeline.ModeFilePrimary, LogSourceID: "inst:z", SourceGeneration: "g1",
		Path: logPath, Stream: "stdout",
	})
	require.NoError(t, err)
	_, err = pipe.Poll()
	require.NoError(t, err)

	m := &Manager{root: root, pipes: map[string]*pipeline.Pipeline{"inst:z/g1": pipe}}
	require.NotPanics(t, func() { m.flushCursorRegistry() })
	require.NotNil(t, m.cursorRegistry, "刷盘后游标表必须已初始化")
	require.Contains(t, m.cursorRegistry, "inst:z/g1")
}
