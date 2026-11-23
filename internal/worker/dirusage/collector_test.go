package dirusage

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fixedSource 固定目标列表的 Source。
type fixedSource struct{ targets []Target }

func (f fixedSource) WorkDirTargets() []Target { return f.targets }

// writeFile 在 dir 下写一个指定大小的文件。
func writeFile(t *testing.T, dir, name string, size int) {
	t.Helper()
	require := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("写测试文件失败: %v", err)
		}
	}
	require(os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755))
	require(os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o644))
}

// TestCollectOnce_SumsRegularFiles 常规文件尺寸之和（含子目录）。
func TestCollectOnce_SumsRegularFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "server.jar", 1024)
	writeFile(t, root, "world/region.mca", 2048)
	writeFile(t, root, "logs/latest.log", 512)

	c := New(fixedSource{targets: []Target{{UUID: "i1", WorkDir: root}}}, Options{})
	c.collectOnce(context.Background())

	got := c.Usages()["i1"]
	if !got.Available {
		t.Fatal("目录可读时必须 available=true")
	}
	if got.Bytes != 1024+2048+512 {
		t.Fatalf("尺寸之和应为 3584，实际 %d", got.Bytes)
	}
}

// TestCollectOnce_UnreadableDirIsAvailableFalse 目录不存在 → available=false。
//
// 这是本包最重要的语义：**「不知道」不是「占用为 0」**。若这里返回 available=true
// 且 bytes=0，CP 侧会把该实例按 0 计入组用量 —— 低估用量、放任超卖。
func TestCollectOnce_UnreadableDirIsAvailableFalse(t *testing.T) {
	c := New(fixedSource{targets: []Target{
		{UUID: "missing", WorkDir: filepath.Join(t.TempDir(), "does-not-exist")},
	}}, Options{})
	c.collectOnce(context.Background())

	got := c.Usages()["missing"]
	if got.Available {
		t.Fatal("目录不存在时必须 available=false，绝不能伪装成占用 0")
	}
	if got.Bytes != 0 {
		t.Fatalf("不可用时不应给出字节数，实际 %d", got.Bytes)
	}
}

// TestCollectOnce_EmptyWorkDirIsAvailableFalse 实例无工作目录 → available=false。
func TestCollectOnce_EmptyWorkDirIsAvailableFalse(t *testing.T) {
	c := New(fixedSource{targets: []Target{{UUID: "no-dir", WorkDir: ""}}}, Options{})
	c.collectOnce(context.Background())

	got := c.Usages()["no-dir"]
	if got.Available {
		t.Fatal("无工作目录时必须 available=false")
	}
}

// TestCollectOnce_OneFailureDoesNotPoisonOthers 单实例失败不影响其余实例。
//
// 这是「单实例统计失败不应让整个心跳失败」在采集层的实现保证：结果逐实例独立判定。
func TestCollectOnce_OneFailureDoesNotPoisonOthers(t *testing.T) {
	ok := t.TempDir()
	writeFile(t, ok, "a.dat", 100)

	c := New(fixedSource{targets: []Target{
		{UUID: "ok", WorkDir: ok},
		{UUID: "bad", WorkDir: filepath.Join(t.TempDir(), "nope")},
		{UUID: "ok2", WorkDir: ok},
	}}, Options{})
	c.collectOnce(context.Background())

	usages := c.Usages()
	if !usages["ok"].Available || usages["ok"].Bytes != 100 {
		t.Fatalf("可用实例读数不符: %+v", usages["ok"])
	}
	if usages["bad"].Available {
		t.Fatal("失败实例必须 available=false")
	}
	if !usages["ok2"].Available {
		t.Fatal("失败实例之后的实例仍应被统计")
	}
}

// TestCollectOnce_TimeoutMarksUnavailable 单实例超时 → available=false（不阻塞后续实例）。
//
// 超时用注入的 walk 模拟：遍历一个文件后 sleep 到超时后再检查 ctx，
// 复现「大目录遍历吃满预算」的真实形态。
func TestCollectOnce_TimeoutMarksUnavailable(t *testing.T) {
	c := New(fixedSource{targets: []Target{
		{UUID: "slow", WorkDir: "/irrelevant"},
		{UUID: "fast", WorkDir: "/irrelevant"},
	}}, Options{
		Timeout: 20 * time.Millisecond,
		Walk: func(_ string, fn func(string, fs.DirEntry, error) error) error {
			// 先睡过预算，再让回调在入口处检查 ctx 并放弃。
			time.Sleep(40 * time.Millisecond)
			return fn("f", fakeDirEntry{}, nil)
		},
	})
	c.collectOnce(context.Background())

	usages := c.Usages()
	if usages["slow"].Available || usages["fast"].Available {
		t.Fatalf("超时实例必须 available=false: %+v", usages)
	}
}

// fakeDirEntry 一个最小化的普通文件条目（Info 返回 1 字节）。
type fakeDirEntry struct{}

func (fakeDirEntry) Name() string               { return "f" }
func (fakeDirEntry) IsDir() bool                { return false }
func (fakeDirEntry) Type() fs.FileMode          { return 0 }
func (fakeDirEntry) Info() (fs.FileInfo, error) { return fakeInfo{}, nil }

type fakeInfo struct{}

func (fakeInfo) Name() string       { return "f" }
func (fakeInfo) Size() int64        { return 1 }
func (fakeInfo) Mode() fs.FileMode  { return 0o644 }
func (fakeInfo) ModTime() time.Time { return time.Time{} }
func (fakeInfo) IsDir() bool        { return false }
func (fakeInfo) Sys() any           { return nil }

// TestUsages_ReturnsCopy 快照返回拷贝：调用方长期持有不会看到后续采集的改写。
func TestUsages_ReturnsCopy(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.dat", 10)
	c := New(fixedSource{targets: []Target{{UUID: "i1", WorkDir: dir}}}, Options{})
	c.collectOnce(context.Background())

	snap := c.Usages()
	snap["i1"] = Usage{Bytes: 999, Available: true}

	if again := c.Usages()["i1"]; again.Bytes != 10 {
		t.Fatalf("外部修改不应回写到采集器内部状态，实际 %+v", again)
	}
}

// TestNilCollectorSafe 未装配（nil）时调用方无需判空——Usages 返回 nil。
func TestNilCollectorSafe(t *testing.T) {
	var c *Collector
	if got := c.Usages(); got != nil {
		t.Fatalf("nil 采集器应返回 nil 快照，实际 %+v", got)
	}
	c.Start(context.Background()) // 不得 panic
}
