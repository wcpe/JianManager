package eventstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// seedSegments 建一个每段一条事件的源：SetMaxSegmentBytes(1) 让每次追加都立即封段。
// 返回该源目录。
func seedSegments(t *testing.T, s *Store, key string, n int) string {
	t.Helper()
	s.SetMaxSegmentBytes(1)
	for i := 0; i < n; i++ {
		require.NoError(t, s.Append(key, []logtypes.Event{ev(i)}))
	}
	require.Len(t, s.Segments(key), n, "前置：应为每段一条事件")
	return filepath.Join(s.root, sectionDirName(key))
}

// segmentNamesInDir 返回目录里的段文件名集合（与 loadManifest 同一判据）。
func segmentNamesInDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, entry := range entries {
		if isSegmentName(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	return names
}

// messagesOf 读回事件正文，便于断言「留下的是哪几条」。
func messagesOf(t *testing.T, s *Store, key string) []string {
	t.Helper()
	var out []string
	require.NoError(t, s.Iterate(key, func(e logtypes.Event) error {
		out = append(out, e.Message)
		return nil
	}))
	return out
}

// 水位以下的整段被回收、水位以上的段保留，且剩余段仍可 Iterate/Snapshot。
func TestPruneReclaimsBelowWatermarkAndKeepsAbove(t *testing.T) {
	s, err := Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	key := "inst:1/g1"
	dir := seedSegments(t, s, key, 6) // ev(i) 的 record_end = i*10+9 ⇒ 9,19,29,39,49,59

	// 水位 30：前 3 段（ends 9/19/29）在水位以内，第 4 段（end 39）在水位之上。
	result, err := s.Prune(key, 30)
	require.NoError(t, err)
	require.Equal(t, 3, result.ReclaimedSegments)
	require.Positive(t, result.ReclaimedBytes)
	require.Equal(t, int64(3), result.ReclaimedEvents)
	require.Empty(t, result.Skipped)
	require.Equal(t, 3, result.RemainingSegments)

	// 剩下的必须是 ev(3)/ev(4)/ev(5)，顺序不变。
	require.Equal(t, []string{"line-3", "line-4", "line-5"}, messagesOf(t, s, key))
	n, err := s.Count(key)
	require.NoError(t, err)
	require.Equal(t, 3, n)

	// 段序号必须从 1 连续（loadManifest 的硬约束），文件名与序号一一对应。
	segs := s.Segments(key)
	require.Len(t, segs, 3)
	for i, meta := range segs {
		require.Equal(t, i+1, meta.Seq)
		require.Equal(t, segmentFileName(i+1), meta.Name)
	}
	require.ElementsMatch(t, []string{"seg-000001.ndjson", "seg-000002.ndjson", "seg-000003.ndjson"},
		segmentNamesInDir(t, dir))

	// Snapshot 在回收后仍然可用，且内容与 Iterate 一致。
	snapshot, err := s.Snapshot(key)
	require.NoError(t, err)
	require.Equal(t, 3, snapshot.Count())
	var fromSnapshot []string
	require.NoError(t, snapshot.Iterate(func(e logtypes.Event) error {
		fromSnapshot = append(fromSnapshot, e.Message)
		return nil
	}))
	require.NoError(t, snapshot.Close())
	require.Equal(t, []string{"line-3", "line-4", "line-5"}, fromSnapshot)

	// 按日聚合（另一条读路径）在回收后仍然工作。
	dayMaxEnd, err := s.DayMaxEnd(key, func(logtypes.Event) (string, error) { return "2026-09-24", nil })
	require.NoError(t, err)
	require.Equal(t, uint64(59), dayMaxEnd["2026-09-24"], "剩余段的最大 record_end 应保持")
}

// 回收后可继续追加：不重开进程也必须能接在剩余段之后，且清单/物理段仍自洽。
func TestPruneThenAppendContinuesCleanly(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)
	key := "inst:1/g1"
	seedSegments(t, s, key, 4)

	_, err = s.Prune(key, 20) // 回收前 2 段（ends 9/19）
	require.NoError(t, err)

	require.NoError(t, s.Append(key, []logtypes.Event{ev(4)}))
	require.Equal(t, []string{"line-2", "line-3", "line-4"}, messagesOf(t, s, key))
	require.NoError(t, s.Close())

	// 重开后清单必须能通过严格校验（序号连续、段名与序号对应、无未列出的段）。
	s2, err := Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	require.Equal(t, []string{"line-2", "line-3", "line-4"}, messagesOf(t, s2, key))
	n, err := s2.Count(key)
	require.NoError(t, err)
	require.Equal(t, 3, n)
}

// 回收后重启仍能正常追加，且新段从剩余段数 +1 继续编号。
func TestPruneSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)
	key := "inst:1/g1"
	seedSegments(t, s, key, 5)
	_, err = s.Prune(key, 30) // 回收前 3 段
	require.NoError(t, err)
	require.NoError(t, s.Close())

	s2, err := Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	require.Len(t, s2.Segments(key), 2)
	require.NoError(t, s2.Append(key, []logtypes.Event{ev(5)}))
	require.Equal(t, []string{"line-3", "line-4", "line-5"}, messagesOf(t, s2, key))
}

// 幂等：同一水位重复调用不再回收任何段，且不改变磁盘内容。
func TestPruneIsIdempotent(t *testing.T) {
	s, err := Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	key := "inst:1/g1"
	dir := seedSegments(t, s, key, 5)

	first, err := s.Prune(key, 30)
	require.NoError(t, err)
	require.Equal(t, 3, first.ReclaimedSegments)
	before := segmentNamesInDir(t, dir)

	second, err := s.Prune(key, 30)
	require.NoError(t, err)
	require.Zero(t, second.ReclaimedSegments, "重复调用不得再回收")
	require.Equal(t, 2, second.RemainingSegments)
	require.Equal(t, before, segmentNamesInDir(t, dir), "重复调用不得改动磁盘")
	require.Equal(t, []string{"line-3", "line-4"}, messagesOf(t, s, key))

	// 水位抬到覆盖全部剩余段时，仍必须保留最后一段（绝不把段删空）。
	third, err := s.Prune(key, 1<<40)
	require.NoError(t, err)
	require.Equal(t, 1, third.ReclaimedSegments)
	require.Equal(t, 1, third.RemainingSegments)
	require.Equal(t, []string{"line-4"}, messagesOf(t, s, key))
}

// 下界缺失（水位 0）一律不删：0 绝不能被当成「全部可回收」。
func TestPruneRefusesWithoutWatermark(t *testing.T) {
	s, err := Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	key := "inst:1/g1"
	dir := seedSegments(t, s, key, 4)
	before := segmentNamesInDir(t, dir)

	result, err := s.Prune(key, 0)
	require.NoError(t, err)
	require.Zero(t, result.ReclaimedSegments)
	require.NotEmpty(t, result.Skipped)
	require.Equal(t, before, segmentNamesInDir(t, dir))
}

// 段数不足（只有一段，或全部未封印）时不删：至少保留最后一段。
func TestPruneKeepsAtLeastOneSegment(t *testing.T) {
	s, err := Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	key := "inst:1/g1"
	require.NoError(t, s.Append(key, []logtypes.Event{ev(0)})) // 默认段大小 ⇒ 单段且未封印

	result, err := s.Prune(key, 1<<40)
	require.NoError(t, err)
	require.Zero(t, result.ReclaimedSegments)
	require.NotEmpty(t, result.Skipped)
	require.Equal(t, 1, result.RemainingSegments)
	require.Len(t, messagesOf(t, s, key), 1)
}

// 未封印的末段即使整体在水位以下也不回收（它仍是本进程的追加目标）。
func TestPruneNeverTouchesUnsealedTail(t *testing.T) {
	s, err := Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	key := "inst:1/g1"
	s.SetMaxSegmentBytes(1)
	require.NoError(t, s.Append(key, []logtypes.Event{ev(0)}))
	require.NoError(t, s.Append(key, []logtypes.Event{ev(1)}))
	// 把段上限调大后再追加 ⇒ 新建一段且不封段。
	s.SetMaxSegmentBytes(1 << 30)
	require.NoError(t, s.Append(key, []logtypes.Event{ev(2)}))

	segs := s.Segments(key)
	require.Len(t, segs, 3)
	require.False(t, segs[len(segs)-1].Sealed, "前置：末段应未封印")

	result, err := s.Prune(key, 1<<40)
	require.NoError(t, err)
	require.Equal(t, 2, result.ReclaimedSegments, "只能回收封印段，末段必须留下")
	require.Len(t, messagesOf(t, s, key), 1)
	require.Equal(t, "line-2", messagesOf(t, s, key)[0])
}

// 段损坏时一律不删（fail-closed）：回收前重扫发现清单与内容不符 ⇒ ErrCorrupt 且磁盘不动。
func TestPruneRefusesCorruptCandidate(t *testing.T) {
	s, err := Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	key := "inst:1/g1"
	dir := seedSegments(t, s, key, 4)

	// 破坏第 1 段的完整行（不是尾部截断）：同 length 改字节，规避「字节数与清单不符」的早退，
	// 直接考「逐行重扫」这条判据。
	target := filepath.Join(dir, segmentFileName(1))
	b, err := os.ReadFile(target)
	require.NoError(t, err)
	require.NotEmpty(t, b)
	corrupted := append([]byte("{not-json}\n"), b...)
	require.NoError(t, os.WriteFile(target, corrupted, 0o600))

	before := segmentNamesInDir(t, dir)
	result, err := s.Prune(key, 1<<40)
	require.Error(t, err, "损坏候选段必须拒绝回收")
	require.True(t, errors.Is(err, ErrCorrupt), "应为 ErrCorrupt，实际: %v", err)
	require.Zero(t, result.ReclaimedSegments)
	require.Equal(t, before, segmentNamesInDir(t, dir), "损坏时不得删除任何段")
}

// 只读实例拒绝回收，且一个字节都不动。
func TestPruneRefusesReadOnlyStore(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)
	key := "inst:1/g1"
	seedSegments(t, s, key, 4)
	require.NoError(t, s.Close())

	ro, err := OpenReadOnly(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ro.Close() })
	before := segmentNamesInDir(t, filepath.Join(dir, sectionDirName(key)))

	_, err = ro.Prune(key, 1<<40)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrReadOnly), "应为 ErrReadOnly，实际: %v", err)
	require.Equal(t, before, segmentNamesInDir(t, filepath.Join(dir, sectionDirName(key))))
}

// 有在途读取快照时跳过回收（读者手里的段路径会在前移后失效），关掉读者后才回收。
func TestPruneSkipsWhileSnapshotLive(t *testing.T) {
	s, err := Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	key := "inst:1/g1"
	dir := seedSegments(t, s, key, 5)

	snapshot, err := s.Snapshot(key)
	require.NoError(t, err)
	before := segmentNamesInDir(t, dir)

	result, err := s.Prune(key, 30)
	require.NoError(t, err)
	require.Zero(t, result.ReclaimedSegments)
	require.Contains(t, result.Skipped, "在途读取快照")
	require.Equal(t, before, segmentNamesInDir(t, dir))

	// 在途读者仍能读完它冻结的完整字节（回收没动过文件）。
	require.Equal(t, 5, snapshot.Count())
	require.NoError(t, snapshot.Iterate(func(logtypes.Event) error { return nil }))
	require.NoError(t, snapshot.Close())

	// 读者退出后回收才生效。
	result, err = s.Prune(key, 30)
	require.NoError(t, err)
	require.Equal(t, 3, result.ReclaimedSegments)
}

// 未知源按幂等空操作处理（与 Count/Segments 一致），不报错。
func TestPruneUnknownSourceIsNoOp(t *testing.T) {
	s, err := Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	result, err := s.Prune("nope/g1", 100)
	require.NoError(t, err)
	require.Zero(t, result.ReclaimedSegments)
	require.NotEmpty(t, result.Skipped)
}

// 崩溃注入：意图日志已落、段名前移只做了一半 ⇒ 重开时必须前滚收敛到「回收后」的完整状态。
func TestPruneJournalRollsForwardOnOpen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)
	key := "inst:1/g1"
	seedSegments(t, s, key, 4)
	old := s.Segments(key)
	require.Len(t, old, 4)
	require.NoError(t, s.Close())

	secDir := filepath.Join(dir, sectionDirName(key))
	// 手工构造「回收前 2 段」的意图日志（正常路径里这份日志由 applyPruneLocked 写下）。
	next := Manifest{Version: manifestVer, Key: key}
	for i := 2; i < len(old); i++ {
		meta := old[i]
		meta.Seq = i - 2 + 1
		meta.Name = segmentFileName(meta.Seq)
		next.Segments = append(next.Segments, meta)
	}
	require.NoError(t, writePruneJournal(secDir, pruneJournal{
		Version: pruneJournalVer, Key: key, Offset: 2, Old: old, New: next,
	}))
	// 模拟「只完成了第一步前移」：旧第 3 段已改名为新第 1 段。
	require.NoError(t, os.Remove(filepath.Join(secDir, segmentFileName(1))))
	require.NoError(t, os.Rename(filepath.Join(secDir, segmentFileName(3)), filepath.Join(secDir, segmentFileName(1))))

	// 此刻目录对 loadManifest 而言是不一致的；Open 必须先前滚再校验。
	s2, err := Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	require.NoFileExists(t, filepath.Join(secDir, pruneJournalName), "前滚后意图日志必须被清掉")
	require.ElementsMatch(t, []string{segmentFileName(1), segmentFileName(2)}, segmentNamesInDir(t, secDir))
	require.Equal(t, []string{"line-2", "line-3"}, messagesOf(t, s2, key))
	n, err := s2.Count(key)
	require.NoError(t, err)
	require.Equal(t, 2, n)
}

// 意图日志本身不自洽（前移会改变内容元数据）时拒绝前滚，并硬失败而不是猜。
func TestPruneJournalContentMismatchIsRefused(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)
	key := "inst:1/g1"
	seedSegments(t, s, key, 3)
	old := s.Segments(key)
	require.NoError(t, s.Close())
	secDir := filepath.Join(dir, sectionDirName(key))

	bad := Manifest{Version: manifestVer, Key: key}
	tampered := old[1]
	tampered.Seq, tampered.Name = 1, segmentFileName(1)
	tampered.Count++ // 伪造：前移不该改变事件数
	bad.Segments = append(bad.Segments, tampered, func() SegmentMeta {
		meta := old[2]
		meta.Seq, meta.Name = 2, segmentFileName(2)
		return meta
	}())
	require.NoError(t, writePruneJournal(secDir, pruneJournal{
		Version: pruneJournalVer, Key: key, Offset: 1, Old: old, New: bad,
	}))

	_, err = Open(dir)
	require.Error(t, err, "前移改变内容元数据的日志必须被拒绝")
	require.True(t, errors.Is(err, ErrCorrupt), "应为 ErrCorrupt，实际: %v", err)
}

// 只读打开时遇到未完成的意图日志必须硬失败：只读取证实例不得前滚（改介质）。
func TestPruneJournalBlocksReadOnlyOpen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)
	key := "inst:1/g1"
	seedSegments(t, s, key, 3)
	old := s.Segments(key)
	require.NoError(t, s.Close())
	secDir := filepath.Join(dir, sectionDirName(key))

	next := Manifest{Version: manifestVer, Key: key}
	for i := 1; i < len(old); i++ {
		meta := old[i]
		meta.Seq, meta.Name = i, segmentFileName(i)
		next.Segments = append(next.Segments, meta)
	}
	require.NoError(t, writePruneJournal(secDir, pruneJournal{
		Version: pruneJournalVer, Key: key, Offset: 1, Old: old, New: next,
	}))

	_, err = OpenReadOnly(dir)
	require.Error(t, err)
	require.True(t, errors.Is(err, errPruneJournalPresent), "应为 errPruneJournalPresent，实际: %v", err)
}

// 意图日志的源键与目录清单不符时拒绝前滚（不能拿 A 源的日志改 B 源目录）。
func TestPruneJournalKeyMismatchIsRefused(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)
	key := "inst:1/g1"
	seedSegments(t, s, key, 3)
	old := s.Segments(key)
	require.NoError(t, s.Close())
	secDir := filepath.Join(dir, sectionDirName(key))

	next := Manifest{Version: manifestVer, Key: "other/g9"}
	for i := 1; i < len(old); i++ {
		meta := old[i]
		meta.Seq, meta.Name = i, segmentFileName(i)
		next.Segments = append(next.Segments, meta)
	}
	require.NoError(t, writePruneJournal(secDir, pruneJournal{
		Version: pruneJournalVer, Key: "other/g9", Offset: 1, Old: old, New: next,
	}))

	_, err = Open(dir)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrCorrupt), "应为 ErrCorrupt，实际: %v", err)
}

// 清单里记录的段字节数与物理文件不符（截断）时，回收前重扫必须拦住。
func TestPruneRefusesTruncatedCandidate(t *testing.T) {
	s, err := Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	key := "inst:1/g1"
	dir := seedSegments(t, s, key, 4)

	target := filepath.Join(dir, segmentFileName(1))
	st, err := os.Stat(target)
	require.NoError(t, err)
	require.NoError(t, os.Truncate(target, st.Size()-3)) // 尾部半行 ⇒ 已封印段不允许

	before := segmentNamesInDir(t, dir)
	_, err = s.Prune(key, 1<<40)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrCorrupt), "应为 ErrCorrupt，实际: %v", err)
	require.Equal(t, before, segmentNamesInDir(t, dir))
}

// 回收只影响目标源，不串扰其它源。
func TestPruneIsolatesSources(t *testing.T) {
	s, err := Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	seedSegments(t, s, "inst:1/g1", 4)
	require.NoError(t, s.Append("inst:2/g1", []logtypes.Event{ev(0)}))

	_, err = s.Prune("inst:1/g1", 20)
	require.NoError(t, err)
	require.Len(t, messagesOf(t, s, "inst:2/g1"), 1, "其它源不得受影响")
}

// 意图日志必须是合法 JSON 且带版本，便于将来演进时拒绝未知格式。
func TestPruneJournalCarriesVersionAndKey(t *testing.T) {
	s, err := Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	key := "inst:1/g1"
	dir := seedSegments(t, s, key, 3)

	_, err = s.Prune(key, 20)
	require.NoError(t, err)
	// 回收成功后日志必须已被清掉（不留残留状态）。
	require.NoFileExists(t, filepath.Join(dir, pruneJournalName))

	// 手工写一份日志再校验其可被解析回同一结构（字段语义稳定性的下限）。
	old := s.Segments(key)
	journal := pruneJournal{Version: pruneJournalVer, Key: key, Offset: 1, Old: old, New: Manifest{Version: manifestVer, Key: key}}
	require.NoError(t, writePruneJournal(dir, journal))
	raw, err := os.ReadFile(filepath.Join(dir, pruneJournalName))
	require.NoError(t, err)
	var decoded pruneJournal
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, pruneJournalVer, decoded.Version)
	require.Equal(t, key, decoded.Key)
	require.Equal(t, 1, decoded.Offset)
}
