package tailer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStaleEntryCleanedOnPut 钉住「同一物理文件只留一条位点」。
//
// 背景（2026-11-07 生产实测）：latest.log 被截断重写时内容变、指纹变，
// 旧条目原地残留。生产上 region1-zone1-lobby-01 一个文件积了 5 条记录、
// 全部指向同一 inode。陈旧条目的危害不是占几行 JSON，而是：
// 文件内容一旦与某个旧指纹重合，GetGrowing 会精确命中它并返回旧 offset，
// 若该 offset 已等于当前文件大小，startOffsetFor 判「已读完」⇒ 静默不采。
func TestStaleEntryCleanedOnPut(t *testing.T) {
	r := &Registry{m: map[string]FileState{}}

	oldID := FileIdentity{Fingerprint: "fp-old", Inode: 1, Dev: 1}
	r.Put(FileState{Identity: oldID, Path: "/x/latest.log", Offset: 20, Size: 20})
	if len(r.m) != 1 {
		t.Fatalf("初始应 1 条，实际 %d", len(r.m))
	}

	// 截断重写：同 inode、内容变了 ⇒ 新指纹
	newID := FileIdentity{Fingerprint: "fp-new", Inode: 1, Dev: 1}
	r.Put(FileState{Identity: newID, Path: "/x/latest.log", Offset: 10, Size: 10})

	if len(r.m) != 1 {
		t.Fatalf("同一 inode 应只留 1 条，实际 %d 条（旧条目未被清理）", len(r.m))
	}
	if _, ok := r.m[oldID.Key()]; ok {
		t.Fatal("旧指纹条目必须被清理")
	}

	// 关键回归：文件内容长回旧指纹形态时，绝不能命中那条已删的旧条目
	if st, ok := r.GetGrowing(oldID, "/x/latest.log", 20); ok && st.Offset >= 20 {
		t.Fatalf("仍命中了陈旧位点 offset=%d（会导致一条不采）", st.Offset)
	}
	t.Logf("同 inode 陈旧条目已清理；GetGrowing 不再误命中 ✓")
}

// TestDifferentInodesKeepSeparateEntries 反向约束：轮转后新文件（不同 inode）不得被误删。
func TestDifferentInodesKeepSeparateEntries(t *testing.T) {
	r := &Registry{m: map[string]FileState{}}

	a := FileIdentity{Fingerprint: "fp-a", Inode: 100, Dev: 1}
	b := FileIdentity{Fingerprint: "fp-b", Inode: 200, Dev: 1}
	r.Put(FileState{Identity: a, Path: "/x/latest.log", Offset: 10, Size: 10})
	r.Put(FileState{Identity: b, Path: "/x/latest.log", Offset: 20, Size: 20})

	if len(r.m) != 2 {
		t.Fatalf("不同 inode 应各留一条，实际 %d 条（误删了轮转目标的位点）", len(r.m))
	}
	t.Logf("不同 inode 的条目各自保留 ✓")
}

// TestTruncateRewriteEndToEnd 端到端：截断重写后内容长回原样，新内容必须被采到。
func TestTruncateRewriteEndToEnd(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "latest.log")

	var got []string
	cfg := CollectorConfig{
		Paths: []string{filepath.Join(dir, "*.log")}, ReadFromHead: true,
		ScanInterval: 150 * time.Millisecond, ProgressInterval: 150 * time.Millisecond,
	}
	c := NewCollector(cfg, nil, SinkFunc(func(_ context.Context, lines []Line) error {
		for _, l := range lines {
			got = append(got, string(l.Content))
		}
		return nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	wait := func(n int, d time.Duration) bool {
		dl := time.Now().Add(d)
		for time.Now().Before(dl) {
			if len(got) >= n {
				return true
			}
			time.Sleep(50 * time.Millisecond)
		}
		return false
	}

	os.WriteFile(p, []byte("A\nB\nC\n"), 0o644)
	if !wait(3, 3*time.Second) {
		t.Fatalf("首轮未采到: %v", got)
	}
	// 截断重写且变短
	os.WriteFile(p, []byte("X\nY\n"), 0o644)
	if !wait(5, 4*time.Second) {
		t.Fatalf("截断后新内容未采到: %v", got)
	}
	// 内容长回原样 —— 修复前这里一条不采
	os.WriteFile(p, []byte("A\nB\nC\n"), 0o644)
	if !wait(8, 4*time.Second) {
		t.Fatalf("内容长回后未采到（这是修复前的缺陷）：%v", got)
	}
	t.Logf("三轮内容全部采到: %v ✓", got)
}
