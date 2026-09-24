package eventstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func ev(i int) logtypes.Event {
	return logtypes.BuildEvent(
		logtypes.SourceIdentity{LogSourceID: "inst:1", SourceGeneration: "g1", ParserVersion: "acquire-v1"},
		logtypes.RecordRange{Start: uint64(i * 10), End: uint64(i*10 + 9)},
		"2026-09-24T00:00:00Z", "2026-09-24T00:00:01Z", "INFO", "stdout",
		fmt.Sprintf("line-%d", i),
	)
}

func collect(t *testing.T, s *Store, key string) []logtypes.Event {
	t.Helper()
	var out []logtypes.Event
	require.NoError(t, s.Iterate(key, func(e logtypes.Event) error {
		out = append(out, e)
		return nil
	}))
	return out
}

func TestAppendAndIterateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	var want []logtypes.Event
	for i := 0; i < 50; i++ {
		want = append(want, ev(i))
	}
	require.NoError(t, s.Append("inst:1/g1", want[:20]))
	require.NoError(t, s.Append("inst:1/g1", want[20:]))

	got := collect(t, s, "inst:1/g1")
	require.Len(t, got, len(want))
	for i := range want {
		require.Equal(t, want[i].EventID, got[i].EventID, "顺序与身份必须保持一致")
		require.Equal(t, want[i].Message, got[i].Message)
	}

	n, err := s.Count("inst:1/g1")
	require.NoError(t, err)
	require.Equal(t, 50, n)
}

// 段滚动：超过段上限即封段新建，读回仍连续。
func TestSegmentRolling(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.SetMaxSegmentBytes(1024) // 极小段，强制滚动

	for i := 0; i < 100; i++ {
		require.NoError(t, s.Append("inst:1/g1", []logtypes.Event{ev(i)}))
	}
	segs := s.Segments("inst:1/g1")
	require.Greater(t, len(segs), 1, "应产生多个段")
	// 除最后一段外都应已封闭。
	for i, seg := range segs {
		if i < len(segs)-1 {
			require.True(t, seg.Sealed, "段 %d 应已封闭", seg.Seq)
		}
	}
	require.Len(t, collect(t, s, "inst:1/g1"), 100)
}

// 重启后可恢复：重新 Open 同一目录，事件完整且计数一致。
func TestReopenRecoversSegments(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)
	s.SetMaxSegmentBytes(2048)
	for i := 0; i < 60; i++ {
		require.NoError(t, s.Append("inst:1/g1", []logtypes.Event{ev(i)}))
	}
	segsBefore := s.Segments("inst:1/g1")
	require.NoError(t, s.Close())

	s2, err := Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	require.Equal(t, len(segsBefore), len(s2.Segments("inst:1/g1")), "清单应持久")
	require.Len(t, collect(t, s2, "inst:1/g1"), 60)

	// 重启后继续追加不丢历史。
	require.NoError(t, s2.Append("inst:1/g1", []logtypes.Event{ev(60)}))
	require.Len(t, collect(t, s2, "inst:1/g1"), 61)
}

// 崩溃注入 A：**尾段**被截断（进程被杀留下的半行）→ 只丢半行，前面的行完整。
func TestTailTruncationRecoversCompleteLinesOnly(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)
	for i := 0; i < 10; i++ {
		require.NoError(t, s.Append("inst:1/g1", []logtypes.Event{ev(i)}))
	}
	require.NoError(t, s.Close())

	// 把最后一段截到中间位置，制造半截行。
	segs := s.Segments("inst:1/g1")
	last := filepath.Join(dir, sectionDirName("inst:1/g1"), segs[len(segs)-1].Name)
	st, err := os.Stat(last)
	require.NoError(t, err)
	require.NoError(t, os.Truncate(last, st.Size()-7))

	s2, err := Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	got := collect(t, s2, "inst:1/g1")
	require.Len(t, got, 9, "只应丢掉被截断的那一行")
	for i, e := range got {
		require.Equal(t, fmt.Sprintf("line-%d", i), e.Message)
	}
}

// 崩溃注入 B：**中间段**损坏 → 必须硬失败，不能静默返回残缺集合。
func TestMiddleSegmentCorruptionFailsHard(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)
	s.SetMaxSegmentBytes(600) // 产生多段
	for i := 0; i < 40; i++ {
		require.NoError(t, s.Append("inst:1/g1", []logtypes.Event{ev(i)}))
	}
	require.NoError(t, s.Close())

	segs := s.Segments("inst:1/g1")
	require.GreaterOrEqual(t, len(segs), 2, "需要至少两三段才能测中间损坏")
	mid := filepath.Join(dir, sectionDirName("inst:1/g1"), segs[0].Name)
	// 破坏段内一行（而非截断尾部）。
	b, err := os.ReadFile(mid)
	require.NoError(t, err)
	idx := strings.Index(string(b), "\n")
	require.Greater(t, idx, 0)
	corrupted := append([]byte("{not-json"), b[idx+1:]...)
	require.NoError(t, os.WriteFile(mid, corrupted, 0o600))

	s2, err := Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	err = s2.Iterate("inst:1/g1", func(logtypes.Event) error { return nil })
	require.Error(t, err, "中间段损坏必须硬失败")
	require.True(t, errors.Is(err, ErrCorrupt), "应返回 ErrCorrupt，实际: %v", err)
}

// 清单版本不受支持 → 拒绝载入（不猜测）。
func TestUnsupportedManifestVersionRejected(t *testing.T) {
	dir := t.TempDir()
	sec := filepath.Join(dir, sectionDirName("inst:1/g1"))
	require.NoError(t, os.MkdirAll(sec, 0o700))
	b, err := json.Marshal(Manifest{Version: 999})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sec, manifestName), b, 0o600))

	_, err = Open(dir)
	require.Error(t, err, "未知清单版本必须拒绝")
	require.Contains(t, err.Error(), "unsupported manifest version")
}

// 计数与段元数据一致（不依赖读段）。
func TestCountMatchesSegmentMetadata(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.SetMaxSegmentBytes(512)
	for i := 0; i < 30; i++ {
		require.NoError(t, s.Append("inst:1/g1", []logtypes.Event{ev(i)}))
	}
	n, err := s.Count("inst:1/g1")
	require.NoError(t, err)
	require.Equal(t, 30, n)

	total := 0
	for _, m := range s.Segments("inst:1/g1") {
		total += m.Count
	}
	require.Equal(t, 30, total)
}

// 未知源 Iterate 返回空而非报错（未注册即无事件）。
func TestIterateUnknownSourceIsEmpty(t *testing.T) {
	s, err := Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.Empty(t, collect(t, s, "nope/g1"))
	n, err := s.Count("nope/g1")
	require.NoError(t, err)
	require.Zero(t, n)
}

// 并发追加后重启，事件数不丢不错。
func TestConcurrentAppendThenReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)
	s.SetMaxSegmentBytes(4096)

	var wg sync.WaitGroup
	const workers, each = 8, 12
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				_ = s.Append("inst:1/g1", []logtypes.Event{ev(w*each + i)})
			}
		}(w)
	}
	wg.Wait()
	require.NoError(t, s.Close())

	s2, err := Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	n, err := s2.Count("inst:1/g1")
	require.NoError(t, err)
	require.Equal(t, workers*each, n, "并发追加不得丢事件")

	seen := map[string]bool{}
	require.NoError(t, s2.Iterate("inst:1/g1", func(e logtypes.Event) error {
		require.False(t, seen[e.EventID], "不得重复: %s", e.EventID)
		seen[e.EventID] = true
		return nil
	}))
	require.Len(t, seen, workers*each)
}

// 源 key 含特殊字符时目录名安全，且不同 key 互不串扰。
func TestSourceKeyIsolation(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	require.NoError(t, s.Append("inst:1/g1", []logtypes.Event{ev(1)}))
	require.NoError(t, s.Append("inst:2/g1", []logtypes.Event{ev(2), ev(3)}))
	require.Len(t, collect(t, s, "inst:1/g1"), 1)
	require.Len(t, collect(t, s, "inst:2/g1"), 2)
}
