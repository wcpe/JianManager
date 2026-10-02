package acquire

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// 首次见到的归档必须被**导入并登记绑定**（"实时边查表/无则登记"的登记动作）。
//
// 这一条守的是"历史归档导入"能力不被归属闸误杀：首次登记时目录里已有的 .gz **没有**
// 轮转事件可依（归档先于本源存在），此时既不可能命中注册表、也不可能有轮转关联。
// 若把"未命中"一律拒绝，该能力被直接删除且人工无法补救（无法指定一个不存在的"原代次"）。
//
// 转红方式（实测）：把注释里标注的「未命中即拒绝」加回去（即未命中时不放行），
// 本用例第一条断言立即红，同时 TestManagerAutoImportsHistoricalGzipBeforeCurrentFile 也会红。
func TestImportGzipRegistersFirstSeenArchiveBinding(t *testing.T) {
	dir := t.TempDir()
	gzPath := filepath.Join(dir, "2026-01-01-1.log.gz")
	writeGzip(t, gzPath, []string{"[12:00:00] [Server thread/INFO]: first seen"})

	key := testKey("src-first-seen", "g2")
	led := ledger.New()
	imp := NewArchiveImporter(led, key)
	imp.SetOwnerRegistry(func(string, string, string) (string, bool) { return "", false })

	res, err := imp.ImportGzip(gzPath)
	require.NoError(t, err)
	require.False(t, res.Skipped, "首次见到的归档必须被导入（历史归档能力）")
	require.Equal(t, 1, res.ImportedCount)
	require.Zero(t, imp.RefusedForeign())

	// 登记动作 = 把 (规范路径, archive_object_id) → 本代次 写进账本分段；
	// 这正是后续其它代次能查到归属、从而被①拒绝的依据。
	require.NoError(t, imp.MarkImported(gzPath, res.ArchiveObjectID, 0, 13))
	ent := led.Get(key)
	require.NotNil(t, ent)
	var seg *ledger.Segment
	for i := range ent.Segments {
		if ent.Segments[i].Path == gzPath {
			seg = &ent.Segments[i]
		}
	}
	require.NotNil(t, seg)
	require.True(t, seg.Imported)
	require.NotEmpty(t, seg.ArchiveObjectID, "绑定必须落到 archive_object_id（注册表的键分量）")
}

// 跨代次拒绝必须留下**可见**缺口且不重复导入：这是"静默重复"的唯一真入口。
//
// 转红方式（实测）：把 `found && owner != a.key.SourceGeneration` 那一支的返回去掉，
// 本用例（与 TestImportGzipRefusesArchiveOwnedByAnotherGeneration）第一条断言立即红。
func TestForeignGenerationRefusalLeavesVisibleGap(t *testing.T) {
	dir := t.TempDir()
	gzPath := filepath.Join(dir, "2026-01-01-1.log.gz")
	writeGzip(t, gzPath, []string{"[12:00:00] [Server thread/INFO]: foreign"})

	key := testKey("src-foreign-gap", "g2")
	led := ledger.New()
	imp := NewArchiveImporter(led, key)
	imp.SetOwnerRegistry(func(string, string, string) (string, bool) { return "g1", true })

	res, err := imp.ImportGzip(gzPath)
	require.NoError(t, err)
	require.True(t, res.Skipped)

	ent := led.Get(key)
	var n int
	for _, gap := range ent.Gaps {
		if gap.Reason == "ARCHIVE_FOREIGN_GENERATION" {
			n++
		}
	}
	require.Equal(t, 1, n, "跨代次拒绝必须留且只留一条可见缺口")
	// 失败态必须登记 size/mtime：上层据此跳过未变的文件，避免每轮重复记缺口。
	var seg *ledger.Segment
	for i := range ent.Segments {
		if ent.Segments[i].Path == gzPath {
			seg = &ent.Segments[i]
		}
	}
	require.NotNil(t, seg)
	require.False(t, seg.Imported)
	require.Contains(t, seg.ImportError, "ARCHIVE_FOREIGN_GENERATION")
	if info, statErr := os.Stat(gzPath); statErr == nil {
		require.Equal(t, info.Size(), seg.ObservedSize)
	}
}

// 查不到但**有轮转关联**时必须照常导入：那是本源自己的文件刚轮转出来的，归属明确。
//
// 这条守的是「闸不能过度拒绝」——否则正常轮转路径会被自己的归属闸挡住（真机上表现为
// 每次轮转后归档都进不来，且每轮记一条缺口）。
func TestImportGzipAcceptsRotationLinkedArchiveWithoutRegistry(t *testing.T) {
	dir := t.TempDir()
	gzPath := filepath.Join(dir, "2026-01-01-1.log.gz")
	writeGzip(t, gzPath, []string{"[12:00:00] [Server thread/INFO]: rotated own"})

	key := testKey("src-linked", "g1")
	led := ledger.New()
	imp := NewArchiveImporter(led, key)
	imp.SetOwnerRegistry(func(string, string, string) (string, bool) { return "", false })
	// 登记轮转关联（模拟 live 边观察到自己的 latest.log 轮转）。
	require.NoError(t, led.LinkRotation(key, filepath.Join(dir, "latest.log"), gzPath, 0))

	res, err := imp.ImportGzip(gzPath)
	require.NoError(t, err)
	require.False(t, res.Skipped, "有轮转关联的归档必须照常导入")
	require.Equal(t, 1, res.ImportedCount)
	require.Zero(t, imp.RefusedForeign())
}

// 归档属于**另一个代次**时必须拒绝，且**绝不能**归到当前代次名下。
//
// 这是本闸的核心：注册表命中给出了明确的归属（owner != 当前代次），此时若"照常导入"，
// 同一份字节会以当前代次的 event_id 再进一次 VL（静默重复），而旧账的 VerifiedRuns
// 与新代次完全对不上。拒绝并把 owner 写进告警与缺口，交给人工按代次补账。
//
// 转红方式（实测）：把 `found && owner != a.key.SourceGeneration` 那一支的返回去掉
// （即命中其它代次时继续导入），本用例第一条断言立即红。
func TestImportGzipRefusesArchiveOwnedByAnotherGeneration(t *testing.T) {
	dir := t.TempDir()
	gzPath := filepath.Join(dir, "2026-01-01-1.log.gz")
	writeGzip(t, gzPath, []string{"[12:00:00] [Server thread/INFO]: belongs to g1"})

	current := testKey("src-cross", "g2")
	led := ledger.New()
	imp := NewArchiveImporter(led, current)
	// 注册表命中：该对象属于 g1。
	imp.SetOwnerRegistry(func(logSourceID, cleanPath, objectID string) (string, bool) {
		return "g1", true
	})

	res, err := imp.ImportGzip(gzPath)
	require.NoError(t, err)
	require.True(t, res.Skipped, "属于其它代次的归档必须被拒绝")
	require.Zero(t, res.ImportedCount, "不得把其它代次的归档归到当前代次")
	require.Contains(t, res.Reason, "g1")
	require.Equal(t, 1, imp.RefusedForeign())

	ent := led.Get(current)
	require.NotNil(t, ent)
	var found bool
	for _, gap := range ent.Gaps {
		if gap.Reason == "ARCHIVE_FOREIGN_GENERATION" {
			found = true
		}
	}
	require.True(t, found, "跨代次拒绝必须留可见缺口（禁止静默归账）")
}

// 注册表命中**本代次**（重试/幂等路径）时必须照常导入。
func TestImportGzipAcceptsArchiveOwnedBySameGeneration(t *testing.T) {
	dir := t.TempDir()
	gzPath := filepath.Join(dir, "2026-01-01-1.log.gz")
	writeGzip(t, gzPath, []string{"[12:00:00] [Server thread/INFO]: retry same gen"})

	key := testKey("src-same", "g2")
	led := ledger.New()
	imp := NewArchiveImporter(led, key)
	imp.SetOwnerRegistry(func(string, string, string) (string, bool) { return "g2", true })

	res, err := imp.ImportGzip(gzPath)
	require.NoError(t, err)
	require.False(t, res.Skipped, "命中本代次的重试必须照常导入")
	require.Equal(t, 1, res.ImportedCount)
	require.Zero(t, imp.RefusedForeign())
}

// 外来/跨代次文件被拒后，后续轮次不得反复产生新的导入（缺口风暴防护）：
// 拒绝路径登记失败态（含 size/mtime），上层 pendingArchives 据此跳过未变的文件。
func TestRefusedGenerationKeepsImportIdempotent(t *testing.T) {
	dir := t.TempDir()
	gzPath := filepath.Join(dir, "2026-01-01-1.log.gz")
	writeGzip(t, gzPath, []string{"[12:00:00] [Server thread/INFO]: foreign"})

	key := testKey("src-repeat", "g2")
	led := ledger.New()
	imp := NewArchiveImporter(led, key)
	imp.SetOwnerRegistry(func(string, string, string) (string, bool) { return "g1", true })

	var imported int
	for i := 0; i < 3; i++ {
		res, err := imp.ImportGzip(gzPath)
		require.NoError(t, err)
		require.True(t, res.Skipped)
		imported += res.ImportedCount
	}
	require.Zero(t, imported, "反复遇到跨代次归档时，一次事件都不得导入")
	require.Equal(t, 3, imp.RefusedForeign())
	var seg *ledger.Segment
	for i := range led.Get(key).Segments {
		if led.Get(key).Segments[i].Path == gzPath {
			seg = &led.Get(key).Segments[i]
		}
	}
	require.NotNil(t, seg, "拒绝必须登记失败态（含 size/mtime）供上层跳过")
	require.False(t, seg.Imported)
	require.Contains(t, seg.ImportError, "ARCHIVE_FOREIGN_GENERATION")
}
