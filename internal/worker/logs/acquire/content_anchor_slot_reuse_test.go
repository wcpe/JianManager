package acquire

// 本文件守住「轮转槽位复用」的跨包判据（2026-10-22 生产缺陷）。
//
// 缺陷形态（取证结论）：归档「已导入」的判据原本**只有路径**，而账本分段按路径 upsert、
// 从不删除；BungeeCord 的 java.util.logging `%g` 是槽位复用语义（活文件 `proxy.log.0`，
// 轮转 `.0→.1`、`.1→.2` 移位改名）⇒ 同一路径上的内容会被反复换掉：
//
//   - 主流形态（进程活着）：换进槽位的内容正是原活文件内容，已被 FileTailer 读过并投递
//     ⇒ 跳过是正确的（不重复导入），本文件用 TestImportTextSkipsFullyDeliveredSlotContent 守住；
//   - 丢失窗口（停机跨过轮转，或停机瞬间的读尾尚未 durable）：换进槽位的内容**从未被投递过**
//     ⇒ 按路径跳过就是静默丢日志，本文件用 TestImportTextReimportsUndeliveredSlotContent 守住。
//
// 判据与取舍见 content_anchor.go（为什么不能用字节数/文件长度、为什么找不到锚必须保持跳过）。

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// slotFixture 造一个「槽位复用」的账本状态。
//
// stale 是**上一轮**这份路径导入过的旧内容：Imported=true，但内容已经不在了。
// content 是槽位里**现在**的内容（下一轮要判定的对象）。
type slotFixture struct {
	dir     string
	path    string
	led     *ledger.Ledger
	key     ledger.SourceKey
	imp     *ArchiveImporter
	staleID int64
}

func newSlotFixture(t *testing.T, logSourceID string, stale, content []string) slotFixture {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "proxy.log.1")
	writePlainText(t, path, content)

	key := testKey(logSourceID, "g1")
	led := ledger.New()
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, ParserVersion: "acquire-v1"})

	// 上一轮：这个路径上是另一份内容，已导入且位置覆盖整份（这就是「路径判据」的全部依据）。
	writePlainText(t, filepath.Join(dir, "stale-src"), stale)
	staleSize := uint64(len(joinLines(stale)))
	require.NoError(t, led.RegisterSegment(key, ledger.Segment{
		Path: path, Kind: ledger.SegmentRotatedText,
		StartPos: 0, EndPos: staleSize,
		Imported: true, ParserVersion: "acquire-v1",
	}))
	return slotFixture{dir: dir, path: path, led: led, key: key, imp: NewArchiveImporter(led, key)}
}

// joinLines 把行集合拼成归档正文（与 writePlainText 的写入口径一致）。
func joinLines(lines []string) string {
	out := ""
	for _, line := range lines {
		out += line + "\n"
	}
	return out
}

// registerContentAnchor 模拟 FileTailer 在**轮转落点/活文件**上登记的内容锚：
// (IdentityBytes, IdentitySHA256) 是那份内容的身份，StartPos 是它第 0 字节对应的逻辑位置，
// EndPos 是读位置。coveredEnd 通过 durable 水位间接控制（见 ResolveContentAnchor）。
func registerContentAnchor(t *testing.T, fx slotFixture, anchorPath string, base, read uint64, sourcePath string) {
	t.Helper()
	idBytes, idSHA, err := ContentIdentity(sourcePath)
	require.NoError(t, err)
	require.NotEmpty(t, idSHA, "夹具前提：锚必须有内容身份")
	require.NoError(t, fx.led.RegisterSegment(fx.key, ledger.Segment{
		Path: anchorPath, Kind: ledger.SegmentRotated,
		StartPos: base, EndPos: read,
		PhysicalMappingKnown: true,
		IdentityBytes:        idBytes, IdentitySHA256: idSHA,
		ParserVersion: "acquire-v1",
	}))
}

// TestImportTextReimportsUndeliveredSlotContent 是**停机跨轮转**的核心用例：
// 槽位内容已被换掉，且这份内容还有**从未投递过**的尾巴 ⇒ 必须导入。
//
// 修复前该用例必红：路径判据只看 `Imported=true`，整包跳过 ⇒ 静默丢日志。
//
// 为什么用「未投递尾巴」而不是「整份未读过」：停机瞬间 FileTailer 往往已经**读**过一段
// （read 位置已前进），但那一段可能还没 durable（WAL 提交在停机时被打断）。把读位置当
// 已投递会丢掉这段从未落库的字节——这正是现场形态，也是本用例固定的形态。
func TestImportTextReimportsUndeliveredSlotContent(t *testing.T) {
	delivered := []string{"[12:00:00] [INFO] delivered-before-shutdown"}
	pendingTail := []string{"[12:10:00] [INFO] written-after-shutdown"}
	content := append(append([]string{}, delivered...), pendingTail...)

	fx := newSlotFixture(t, "src-slot-undelivered",
		[]string{"[09:00:00] [INFO] previous-cycle-content"}, content)

	// 这份内容的锚：第 0 字节落在逻辑位置 100；FileTailer 读到了 100+首行长度，
	// 但 durable 水位只有 100（首行**未被确认投递**）⇒ 整份都还得导。
	base := uint64(100)
	require.NoError(t, fx.led.AdvanceRead(fx.key, base))
	require.NoError(t, fx.led.AdvanceDurable(fx.key, base))
	registerContentAnchor(t, fx, filepath.Join(fx.dir, "proxy.log.0"), base,
		base+uint64(len(joinLines(delivered))), fx.path)

	res, err := fx.imp.ImportText(fx.path)
	require.NoError(t, err)
	require.False(t, res.Skipped, "槽位里这份内容还有从未投递的字节，不得整包跳过（修复前正是这里静默丢）")
	require.Len(t, res.Events, len(content), "未投递的整份内容都必须导入（durable 水位尚未覆盖任何字节）")
	require.Equal(t, delivered[0], res.Events[0].Message)
	require.Equal(t, pendingTail[0], res.Events[len(res.Events)-1].Message)
	require.Equal(t, base, res.Events[0].Record.Start, "起点必须是锚给出的逻辑位置（不得重排到流尾）")
	require.Zero(t, fx.imp.SkippedAlreadyLinked(), "这不是一次「跳过」")
}

// TestImportTextSkipsFullyDeliveredSlotContent 是**正常轮转**的守护用例：
// 槽位内容已被换掉，但换进来的正是原活文件内容、且**整份都已投递** ⇒ 不得重复导入。
//
// 这是把病治成更大的病（重复入库、inst:318 那类 event_id 多副本）的唯一入口，必须钉住。
func TestImportTextSkipsFullyDeliveredSlotContent(t *testing.T) {
	delivered := []string{"[12:00:00] [INFO] delivered-before-rotation"}
	fx := newSlotFixture(t, "src-slot-delivered",
		[]string{"[09:00:00] [INFO] previous-cycle-content"}, delivered)

	base := uint64(100)
	size := uint64(len(joinLines(delivered)))
	require.NoError(t, fx.led.AdvanceRead(fx.key, base+size))
	require.NoError(t, fx.led.AdvanceDurable(fx.key, base+size))
	registerContentAnchor(t, fx, filepath.Join(fx.dir, "proxy.log.0"), base, base+size, fx.path)

	res, err := fx.imp.ImportText(fx.path)
	require.NoError(t, err)
	require.True(t, res.Skipped, "整份都已投递 ⇒ 必须跳过（否则就是把已投递内容再导一遍）")
	require.Empty(t, res.Events)
	require.Equal(t, 1, fx.imp.SkippedAlreadyLinked())
}

// TestImportTextReimportKeepsCoveredPrefix 守住 covered-prefix 语义：
// 已投递覆盖的前缀不得重导，只导它之后的部分。
//
// 为什么单列一条：只断言「导入了」不足以证明没把前缀重导一遍——两者都表现为「有事件」。
func TestImportTextReimportKeepsCoveredPrefix(t *testing.T) {
	delivered := []string{"[12:00:00] [INFO] already-delivered"}
	pendingTail := []string{"[12:10:00] [INFO] never-delivered"}
	content := append(append([]string{}, delivered...), pendingTail...)

	fx := newSlotFixture(t, "src-slot-prefix",
		[]string{"[09:00:00] [INFO] previous-cycle-content"}, content)

	base := uint64(100)
	deliveredBytes := uint64(len(joinLines(delivered)))
	durable := base + deliveredBytes
	require.NoError(t, fx.led.AdvanceRead(fx.key, durable+100))
	require.NoError(t, fx.led.AdvanceDurable(fx.key, durable))
	// 锚记录「读到了 durable+若干」（读位置领先 durable），覆盖只认 durable 那段。
	registerContentAnchor(t, fx, filepath.Join(fx.dir, "proxy.log.0"), base, durable+uint64(len(joinLines(pendingTail))), fx.path)

	res, err := fx.imp.ImportText(fx.path)
	require.NoError(t, err)
	require.False(t, res.Skipped)
	require.Len(t, res.Events, 1, "只有未被覆盖的尾行该被导入")
	require.Equal(t, pendingTail[0], res.Events[0].Message)
	require.Equal(t, durable, res.Events[0].Record.Start,
		"续读起点必须正好落在已覆盖前缀之后（覆盖前缀若被重导，这里会回到 base）")
	require.Equal(t, durable, res.ResumedFrom,
		"ResumedFrom 必须报出续读起点（= 已覆盖前缀的末端逻辑位置）")
}

// TestImportTextLegacyImportedSegmentKeepsSkipping 守住旧账本兼容：
// 存量分段没有内容身份、账本里也没有任何锚 ⇒ 行为必须与升级前**逐字一致**（跳过）。
//
// 为什么这条是硬要求：升级瞬间不能把历史归档全部重导一遍（那会是几十万事件的重复投递）。
func TestImportTextLegacyImportedSegmentKeepsSkipping(t *testing.T) {
	fx := newSlotFixture(t, "src-slot-legacy",
		[]string{"[09:00:00] [INFO] previous-cycle-content"},
		[]string{"[12:00:00] [INFO] history-archive-already-imported"})

	stale := archiveSegmentOf(fx.led, fx.key, fx.path)
	require.NotNil(t, stale)
	require.Empty(t, stale.IdentitySHA256, "夹具前提：旧账本的分段没有内容身份")

	res, err := fx.imp.ImportText(fx.path)
	require.NoError(t, err)
	require.True(t, res.Skipped, "找不到内容锚 ⇒ 必须保持跳过（重复导入比丢失更糟）")
	require.Empty(t, res.Events)
	require.Equal(t, 1, fx.imp.SkippedAlreadyLinked())
}

// TestImportTextSameContentStaysSkipped 守住「同一份内容重复发现」的幂等（主流形态的快路）：
// 分段已记录这份内容的身份且文件没变 ⇒ 跳过，不再做锚扫描。
func TestImportTextSameContentStaysSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proxy.log.1")
	lines := []string{"[12:00:00] [INFO] same-content"}
	writePlainText(t, path, lines)
	key := testKey("src-slot-same-content", "g1")
	led := ledger.New()
	fx := slotFixture{dir: dir, path: path, led: led, key: key, imp: NewArchiveImporter(led, key)}

	first, err := fx.imp.ImportText(fx.path)
	require.NoError(t, err)
	require.Len(t, first.Events, 1)
	require.NoError(t, fx.imp.MarkImported(fx.path, first.ArchiveObjectID,
		first.Events[0].Record.Start, first.Events[0].Record.End))

	seg := archiveSegmentOf(fx.led, fx.key, fx.path)
	require.NotEmpty(t, seg.IdentitySHA256, "已导入必须把内容身份落账（供下次比对）")

	again, err := fx.imp.ImportText(fx.path)
	require.NoError(t, err)
	require.True(t, again.Skipped, "内容没变 ⇒ 不得重复导入")
	require.Empty(t, again.Events)
}
