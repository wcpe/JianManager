package acquire

// inode 判据的回归（2026-10-28，对齐 Filebeat 的 inode_deviceid）。
//
// 它守的是四条不得退让的性质：
//   1. 改名不改 inode ⇒ 认得出是同一份（这是整个能力的价值所在）；
//   2. 同名路径换内容（inode 变）⇒ 不得认成同一份（否则静默丢新字节）；
//   3. inode 相同但内容前缀不符（inode 复用）⇒ 不得认成同一份；
//   4. 旧账本没记 inode（InodeKnown=false）⇒ 不参与，回退到原有判据。

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// writeWithInode 写文件并把真实 inode 回填到分段。
func writeWithInode(t *testing.T, dir, name, content string) (string, *ledger.Segment) {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	info, err := os.Stat(path)
	require.NoError(t, err)
	dev, ino, ok := fileIdentity(info)
	require.True(t, ok, "本用例需 inode 语义（Unix）；Windows 下会 skip 整个文件")
	idBytes, idSHA, err := ContentIdentity(path)
	require.NoError(t, err)
	return path, &ledger.Segment{
		Path: path, Kind: ledger.SegmentGzip,
		StartPos: 0, EndPos: uint64(info.Size()),
		Imported:      true,
		IdentityBytes: idBytes, IdentitySHA256: idSHA,
		DeviceID: dev, Inode: ino, InodeKnown: true,
		ObservedSize: info.Size(), ObservedModNano: info.ModTime().UnixNano(),
	}
}

// TestInodeMatchesSegmentSurvivesRename 是本能力存在的理由：
// 文件改名（轮转/压缩）后 inode 不变 ⇒ 必须认得出是同一份。
func TestInodeMatchesSegmentSurvivesRename(t *testing.T) {
	dir := t.TempDir()
	path, seg := writeWithInode(t, dir, "latest.log", "line-one\nline-two\n")

	require.True(t, inodeMatchesSegment(seg), "未改名时必然匹配 ✓")

	// 改名：latest.log → latest.log.1（inode 不变）
	renamed := filepath.Join(dir, "latest.log.1")
	require.NoError(t, os.Rename(path, renamed))
	seg.Path = renamed // 账本里的路径也会随之更新

	require.True(t, inodeMatchesSegment(seg),
		"改名不改 inode ⇒ 必须认得出是同一份（这正是本能力要解决的问题）✓")
}

// TestInodeMatchesSegmentRejectsReplacedContent 守住：同名路径换了内容（inode 变）
// ⇒ 不得认成同一份，否则新内容会被当成“已导入”静默丢掉。
func TestInodeMatchesSegmentRejectsReplacedContent(t *testing.T) {
	dir := t.TempDir()
	path, seg := writeWithInode(t, dir, "proxy.log.1", "old-content\n")

	// 槽位复用：删掉重建，内容与 inode 都变。
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.WriteFile(path, []byte("brand-new-content\n"), 0o600))

	require.False(t, inodeMatchesSegment(seg),
		"inode 变了（槽位复用）⇒ 不得认成同一份 ✓")
}

// TestInodeMatchesSegmentRejectsInodeReuse 守住 inode 复用防护：
// 即使 inode 碰巧相同，内容前缀不符也必须拒绝。
func TestInodeMatchesSegmentRejectsInodeReuse(t *testing.T) {
	dir := t.TempDir()
	_, seg := writeWithInode(t, dir, "a.log", "original-bytes\n")

	// 伪造：路径上放不同内容，但把 inode 写成与当前一致（模拟 inode 复用）。
	otherPath := filepath.Join(dir, "b.log")
	require.NoError(t, os.WriteFile(otherPath, []byte("totally-different\n"), 0o600))
	info, err := os.Stat(otherPath)
	require.NoError(t, err)
	dev, ino, ok := fileIdentity(info)
	require.True(t, ok)
	seg.Path = otherPath
	seg.DeviceID, seg.Inode, seg.InodeKnown = dev, ino, true
	// 保持旧的内容身份不变（真实感：账本记的是当初那份内容的哈希）

	require.False(t, inodeMatchesSegment(seg),
		"inode 相同但内容前缀不符（inode 复用）⇒ 必须拒绝 ✓")
}

// TestInodeMatchesSegmentRequiresRecordedInode 守住旧账本兼容：
// 没记过 inode 的分段不得拿“当前 inode”去匹配——那等于凭空发证书。
//
// 构造要点：要真正验到 `for InodeKnown` 这行，必须让记录值**与当前实际相等**
// （device 也相同）。若只把记录置 0，第一个 dev/ino 比对就回 false 了，
// 测不到“无证据却放行”这条危险路径（前一个版本的变异验证就是因此存活的）。
func TestInodeMatchesSegmentRequiresRecordedInode(t *testing.T) {
	dir := t.TempDir()
	_, seg := writeWithInode(t, dir, "legacy.log.gz", "legacy\n")

	// 伪造出最优情形：记录值与当前完全一致，但**没有任何“已记录”的标记**。
	// 这正是旧账本的形态：字段缺失 = 零值，但磁盘上的文件就是它当时看的那份。
	// 此时必须仍然拒绝——“没记过”不能等同于“匹配上了”。
	seg.InodeKnown = false
	require.False(t, inodeMatchesSegment(seg),
		"InodeKnown=false 时必须拒绝（即便 dev/ino 数值碰巧相等）✓")

	// 反向确认：把标记打开就必须通过，证明这个字段是**唯一**的开关。
	seg.InodeKnown = true
	require.True(t, inodeMatchesSegment(seg), "标记打开时必然匹配 ✓")
}

// TestDecideArchiveReimportSkipsOnInodeMatch 确认接线：inode 命中时
// DecideArchiveReimport 直接判 ArchiveCovered（不再依赖内容锚）。
func TestDecideArchiveReimportSkipsOnInodeMatch(t *testing.T) {
	dir := t.TempDir()
	_, seg := writeWithInode(t, dir, "same.log.gz", "same-content\n")

	decision := DecideArchiveReimport(nil, seg)
	require.Equal(t, ArchiveCovered, decision.Disposition,
		"inode 命中 ⇒ 直接判已覆盖，不需要内容锚（旧账本也能走通）✓")
	require.False(t, decision.Import, "不得重导 ✓")
}
