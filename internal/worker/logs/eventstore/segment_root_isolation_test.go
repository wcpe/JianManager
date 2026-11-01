package eventstore_test

// 段根隔离回归（2026-11-02）。
//
// 背景：2026-11-02 长稳子代理发现 `MemoryRoot(filepath.Base(opts.Root))` 会把
// 末级目录名相同的数据根映射到同一个 /dev/shm 段根。生产 `--data-dir .../prod/worker/data`
// 与实验件 `.../fr498-resume/data` 的 basename 都是 `data`，于是实验进程的
// Prune / 回收前滚 / recoverPruneJournal(allowWrite=true) 会真删生产的事件段。
//
// 本组用例钉住“不同数据根必得不同段根”这个安全不变式。

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/wcpe/JianManager/internal/worker/logs/eventstore"
)

func TestSegmentRootForDistinguishesSameBasename(t *testing.T) {
	prod := "/home/wxys233/jm/prod/worker/data"
	lab := "/home/wxys233/projects/JianManager/.tmp/fr498-resume/data"

	if filepath.Base(prod) != filepath.Base(lab) {
		t.Fatalf("前提失效：两个数据根应共享 basename，实际 %q vs %q",
			filepath.Base(prod), filepath.Base(lab))
	}

	a, b := eventstore.SegmentRootFor(prod), eventstore.SegmentRootFor(lab)
	if a == b {
		t.Fatalf("不同数据根不得共享段根（否则实验进程会删生产段）：两者都是 %q", a)
	}
}

func TestSegmentRootForStableForSamePath(t *testing.T) {
	p := "/home/wxys233/jm/prod/worker/data"
	if a, b := eventstore.SegmentRootFor(p), eventstore.SegmentRootFor(p); a != b {
		t.Fatalf("同一路径必须得同一段根（重启后才能接回自己的段）：%q vs %q", a, b)
	}
}

func TestSegmentRootForKeepsReadableBase(t *testing.T) {
	got := eventstore.SegmentRootFor("/srv/jm/worker/data")
	// 保留可读的 basename 前缀，便于 `ls /dev/shm/jianmanager-events/` 一眼认出归属。
	if !strings.HasPrefix(got, "data-") {
		t.Fatalf("段根应保留可读的 basename 前缀，实际 %q", got)
	}
	// 尾缀应是 12 位十六进制哈希。
	suffix := strings.TrimPrefix(got, "data-")
	if len(suffix) != 12 {
		t.Fatalf("哈希尾缀应为 12 位，实际 %q（长度 %d）", suffix, len(suffix))
	}
	for _, r := range suffix {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("哈希尾缀应为小写十六进制，实际 %q", suffix)
		}
	}
}

func TestSegmentRootForTrailingSlashAndDotSegmentsAgree(t *testing.T) {
	// 同一目录的不同写法不得产生不同段根，否则重启后接不回自己的段。
	base := "/srv/jm/worker/data"
	same := []string{
		"/srv/jm/worker/data",
		"/srv/jm/worker/data/",
		"/srv/jm/worker/./data",
		"/srv/jm/worker/x/../data",
	}
	want := eventstore.SegmentRootFor(base)
	for _, p := range same {
		if got := eventstore.SegmentRootFor(p); got != want {
			t.Errorf("%q 应得 %q，实际 %q", p, want, got)
		}
	}
}

func TestSegmentRootForEmptyAndRootFallback(t *testing.T) {
	// 不 panic、不产生空前缀：退化行为必须仍强于“所有同名目录全撞”。
	for _, p := range []string{"", "  ", "/", "."} {
		got := eventstore.SegmentRootFor(p)
		if got == "" || strings.HasPrefix(got, "-") {
			t.Errorf("输入 %q 产生了不可用的段根 %q", p, got)
		}
	}
}
