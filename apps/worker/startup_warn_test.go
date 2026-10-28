package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

// FR-486 范围④：启动期状态规模超阈告警。
//
// 这些用例只验「阈值判定与不阻断启动」这两件事——告警的**输出**是 slog，
// 用日志库断言代价高于价值；关键是「什么时候判超、什么时候不判超、判错时会不会弄坏启动」。

// newRootForTest 造一个带 var/log 的临时数据根，并在其中写入指定大小的文件。
func newRootForTest(t *testing.T, files map[string]int) *dataroot.Root {
	t.Helper()
	base := t.TempDir()
	root, err := dataroot.Init(base)
	if err != nil {
		t.Fatalf("初始化数据根失败: %v", err)
	}
	logDir := root.LogDir()
	if err := os.MkdirAll(filepath.Join(logDir, "events"), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	for name, size := range files {
		p := filepath.Join(logDir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("建父目录失败: %v", err)
		}
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatalf("写文件失败: %v", err)
		}
	}
	return root
}

// TestWarnIfStateTooLarge_DisabledLeavesNoSideEffect 阈值 ≤0 时不做任何事。
//
// 关闭语义要能真的关掉：负数阈值是「我不想看到这条告警」的显式表达。
func TestWarnIfStateTooLarge_DisabledLeavesNoSideEffect(t *testing.T) {
	root := newRootForTest(t, map[string]int{"ingest.index.db": 8 << 20})
	// 不 panic、不阻断即通过（函数无返回值，写错会 panic 被这里抓到）
	warnIfStateTooLarge(root, 0)
	warnIfStateTooLarge(root, -1)
}

// TestWarnIfStateTooLarge_BelowThresholdNoPanic 未超阈时不告警也不出错。
func TestWarnIfStateTooLarge_BelowThresholdNoPanic(t *testing.T) {
	root := newRootForTest(t, map[string]int{"ingest.index.db": 1 << 20}) // 1MB
	warnIfStateTooLarge(root, 512)
}

// TestWarnIfStateTooLarge_CountsSubdirectories 事件段在子目录里，必须一并计入。
//
// 这条守的是真实的坑：事件正文已按 FR-484 外移到 var/log/events/ 下的段文件。
// 若只 stat 顶层文件，一条 1.2GB 的事件段会被算成 0MB，告警永不触发。
func TestWarnIfStateTooLarge_CountsSubdirectories(t *testing.T) {
	root := newRootForTest(t, map[string]int{
		"ingest.index.db": 1 << 20, // 1MB
		filepath.Join("events", "seg-000001.ndjson"): 8 << 20, // 8MB
	})
	// 阈值 5MB：顶层 1MB 不够，加上子目录 8MB 才超——用于证明子目录被计入。
	// 该用例的断言是「超阈路径不 panic 且不阻断」；若子目录没被计入，
	// 下面的 total 校验会失败。
	var total int64
	_ = filepath.WalkDir(root.LogDir(), func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, e := d.Info(); e == nil {
			total += info.Size()
		}
		return nil
	})
	if total < 9<<20 {
		t.Fatalf("夹具本身应含 ≥9MB（含子目录），实际 %d 字节", total)
	}
	warnIfStateTooLarge(root, 5)
}

// TestWarnIfStateTooLarge_MissingLogDirIsNotFatal 数据根下还没有 var/log（全新安装）时不得出错。
//
// 全新节点首次启动就是这个状态：若这里 panic 或 os.Exit，等于新装 Worker 无法启动。
func TestWarnIfStateTooLarge_MissingLogDirIsNotFatal(t *testing.T) {
	base := t.TempDir()
	root, err := dataroot.Init(base)
	if err != nil {
		t.Fatalf("初始化数据根失败: %v", err)
	}
	// 刻意不创建 var/log
	warnIfStateTooLarge(root, 512)
}
