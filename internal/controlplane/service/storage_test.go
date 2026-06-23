package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

// newStorageTestService 在临时目录初始化数据根 + 内存 DB，返回就绪服务与根路径。
func newStorageTestService(t *testing.T) (*StorageService, *dataroot.Root) {
	t.Helper()
	dir := t.TempDir()
	root, err := dataroot.Init(dir)
	require.NoError(t, err)

	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Asset{}))
	require.NoError(t, db.Exec("DELETE FROM assets").Error)

	return NewStorageService(db, root), root
}

// writeFile 在数据根相对路径写入内容并建父目录。
func writeFile(t *testing.T, root *dataroot.Root, rel string, content []byte) {
	t.Helper()
	abs := root.Abs(rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
	require.NoError(t, os.WriteFile(abs, content, 0o644))
}

// dirUsage 递归统计字节与文件数；空目录计 0，嵌套文件全计入。
func TestDirUsage_RecursiveCount(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644)) // 5
	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "b.bin"), []byte("world!!"), 0o644)) // 7

	size, count, exists, err := dirUsage(dir)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, int64(12), size)
	require.Equal(t, 2, count)
}

// dirUsage 对不存在目录返回 exists=false 且无错误（布局缺失子目录照常列出）。
func TestDirUsage_Missing(t *testing.T) {
	size, count, exists, err := dirUsage(filepath.Join(t.TempDir(), "nope"))
	require.NoError(t, err)
	require.False(t, exists)
	require.Zero(t, size)
	require.Zero(t, count)
}

// Overview 覆盖全部 FHS 子目录，cache 标 Clearable，占用按目录归集，总计累加。
func TestStorage_Overview(t *testing.T) {
	svc, root := newStorageTestService(t)
	writeFile(t, root, "cache/tmp.part", []byte("0123456789")) // 10B 入 cache
	writeFile(t, root, "var/artifacts/core/ab/x.jar", []byte("JAR")) // 3B 入 artifacts

	ov, err := svc.Overview()
	require.NoError(t, err)
	require.Equal(t, root.Base(), ov.Base)
	require.Len(t, ov.Dirs, len(fhsLayout))

	byLabel := map[string]DirUsage{}
	for _, d := range ov.Dirs {
		byLabel[d.Label] = d
	}
	require.Equal(t, int64(10), byLabel["cache"].Size)
	require.Equal(t, 1, byLabel["cache"].FileCount)
	require.True(t, byLabel["cache"].Clearable)
	require.False(t, byLabel["artifacts"].Clearable)
	require.Equal(t, int64(3), byLabel["artifacts"].Size)
	require.Equal(t, int64(13), ov.TotalSize)
	require.Equal(t, 2, ov.TotalFiles)
}

// Overview 的归档分布按 storage_state 聚合（FR-045 归档可见）。
func TestStorage_Overview_ArchiveSummary(t *testing.T) {
	svc, _ := newStorageTestService(t)
	require.NoError(t, svc.db.Create(&model.Asset{Type: model.AssetTypeCore, SHA256: "a", Size: 100, StorageState: model.AssetStorageHot}).Error)
	require.NoError(t, svc.db.Create(&model.Asset{Type: model.AssetTypeCore, SHA256: "b", Size: 200, StorageState: model.AssetStorageArchived}).Error)
	require.NoError(t, svc.db.Create(&model.Asset{Type: model.AssetTypePlugin, SHA256: "c", Size: 50, StorageState: model.AssetStorageExternal}).Error)

	ov, err := svc.Overview()
	require.NoError(t, err)
	require.Equal(t, 1, ov.Archive.HotCount)
	require.Equal(t, int64(100), ov.Archive.HotSize)
	require.Equal(t, 1, ov.Archive.ArchivedCount)
	require.Equal(t, int64(200), ov.Archive.ArchivedSize)
	require.Equal(t, 1, ov.Archive.ExternalCount)
	require.Equal(t, int64(50), ov.Archive.ExternalSize)
}

// List 列举目录直接子项，目录在前再按名，且不递归。
func TestStorage_List_Ordering(t *testing.T) {
	svc, root := newStorageTestService(t)
	writeFile(t, root, "var/artifacts/core/z.jar", []byte("z"))
	writeFile(t, root, "var/artifacts/core/a.jar", []byte("a"))
	require.NoError(t, os.MkdirAll(root.Abs("var/artifacts/plugin"), 0o755))

	entries, err := svc.List("var/artifacts")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	// core 与 plugin 均为目录，按名升序。
	require.Equal(t, "core", entries[0].Name)
	require.True(t, entries[0].IsDir)
	require.Equal(t, "plugin", entries[1].Name)

	// 下钻到 core，文件按名升序。
	sub, err := svc.List("var/artifacts/core")
	require.NoError(t, err)
	require.Len(t, sub, 2)
	require.Equal(t, "a.jar", sub[0].Name)
	require.False(t, sub[0].IsDir)
	require.Equal(t, "z.jar", sub[1].Name)
}

// List 对布局中声明但未创建的目录返回空列表（不报错）。
func TestStorage_List_MissingDirEmpty(t *testing.T) {
	svc, _ := newStorageTestService(t)
	require.NoError(t, os.RemoveAll(filepath.Join(svc.root.Base(), "bin")))
	entries, err := svc.List("bin")
	require.NoError(t, err)
	require.Empty(t, entries)
}

// List 对 ../ 越界路径拒绝（不得逃出数据根）。
func TestStorage_List_PathEscape(t *testing.T) {
	svc, _ := newStorageTestService(t)
	for _, bad := range []string{"../../etc/passwd", "var/../../secret", ".."} {
		_, err := svc.List(bad)
		require.ErrorIs(t, err, ErrStoragePathEscape, "应拒绝越界路径 %q", bad)
	}
}

// List 把前导斜杠的路径当作根相对解析（安全），不视为系统绝对路径。
func TestStorage_List_LeadingSlashIsRootRelative(t *testing.T) {
	svc, root := newStorageTestService(t)
	writeFile(t, root, "etc/control-plane.yaml", []byte("k: v"))
	entries, err := svc.List("/etc")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "control-plane.yaml", entries[0].Name)
}

// List 拒绝指向文件（非目录）的路径。
func TestStorage_List_NotDir(t *testing.T) {
	svc, root := newStorageTestService(t)
	writeFile(t, root, "etc/control-plane.yaml", []byte("k: v"))
	_, err := svc.List("etc/control-plane.yaml")
	require.ErrorIs(t, err, ErrStorageNotDir)
}

// ClearCache 仅清 cache/ 内容、保留 cache 目录与根内其它目录。
func TestStorage_ClearCache(t *testing.T) {
	svc, root := newStorageTestService(t)
	writeFile(t, root, "cache/a.part", []byte("aaa"))
	writeFile(t, root, "cache/sub/b.part", []byte("bbb"))
	writeFile(t, root, "var/artifacts/core/keep.jar", []byte("keep")) // 不得被清

	n, err := svc.ClearCache()
	require.NoError(t, err)
	require.Equal(t, 2, n) // a.part + sub/

	// cache 目录仍在但已空。
	entries, err := svc.List("cache")
	require.NoError(t, err)
	require.Empty(t, entries)
	require.DirExists(t, root.Abs("cache"))
	// artifacts 未受影响。
	require.FileExists(t, root.Abs("var/artifacts/core/keep.jar"))
}

// ClearCache 对空/缺失 cache 目录幂等返回 0。
func TestStorage_ClearCache_Empty(t *testing.T) {
	svc, _ := newStorageTestService(t)
	n, err := svc.ClearCache()
	require.NoError(t, err)
	require.Zero(t, n)
}

// 未配置数据根时各方法返回 ErrStorageNoRoot。
func TestStorage_NoRoot(t *testing.T) {
	svc := NewStorageService(nil, nil)
	_, err := svc.Overview()
	require.ErrorIs(t, err, ErrStorageNoRoot)
	_, err = svc.List("")
	require.ErrorIs(t, err, ErrStorageNoRoot)
	_, err = svc.ClearCache()
	require.ErrorIs(t, err, ErrStorageNoRoot)
}
