package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wcpe/JianManager/internal/platform/dataroot"
	workercfg "github.com/wcpe/JianManager/internal/worker"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// rootForTest 构造一个数据根（不落地任何目录，装配层自己会建 var/log）。
func rootForTest(t *testing.T, dir string) *dataroot.Root {
	t.Helper()
	root, err := dataroot.Resolve(dir)
	if err != nil {
		t.Fatalf("resolve data root: %v", err)
	}
	return root
}

func splitLines(s string) []string {
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func joinLines(lines []string) string {
	return strings.Join(lines, "\n") + "\n"
}

// 本文件钉住 apps/worker 里 Catalog journal 的装配（2026-11-05 真机事故修复）。
//
// 事故形态：journal 被硬编码成纯内存 ⇒ 进程重启后分区记录归零 ⇒ 该源之前所有天在
// 查询侧变成「不存在」（VL 里行还在，没人知道去查它）。生产 inst:318 的 2026-11-02
// 有 1746 行、查询侧报「这天没日志」。
//
// 这里钉两件事：默认装配出来**真的落盘且能重放恢复**；显式关闭时退化为内存
// （而不是启动失败）。

func catalogTestConfig(enabled *bool) *workercfg.Config {
	cfg := &workercfg.Config{}
	cfg.LogCatalog.JournalEnabled = enabled
	return cfg
}

// TestOpenLogCatalogJournal_PersistsAndReplays 默认装配：写进去的分区记录重启后仍可恢复。
//
// 这是整条修复的端到端断言。转红方式（实测）：把 openLogCatalogJournal 改成
// 无条件 `return fallback, nil`（即恢复成纯内存），第二条断言会以「重启后分区记录丢失」变红。
func TestOpenLogCatalogJournal_PersistsAndReplays(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "var", "log"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := catalogTestConfig(nil) // 未设 ⇒ 默认开启

	journal, closer := openLogCatalogJournal(cfg, rootForTest(t, root), catalog.NewMemJournal())
	if journal == nil {
		t.Fatal("journal 不应为 nil")
	}
	if closer == nil {
		t.Fatal("落盘形态必须返回 closer，否则退出时最后一攒批记录不会落盘")
	}
	if _, isMem := journal.(*catalog.MemJournal); isMem {
		t.Fatal("默认配置下不应退化为纯内存（那会让重启后历史天不可查）")
	}

	// 写两天分区记录（走真实的 Catalog 提交路径）。
	cat := catalog.New(journal)
	for _, day := range []string{"2026-11-02", "2026-11-04"} {
		key := catalog.PartitionKey{StorageNamespace: "inst:318", UTCDay: day}
		rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-"+day)
		if err := cat.AppendJournal(catalog.JournalEntry{Key: key, Record: rec}); err != nil {
			t.Fatalf("append %s: %v", day, err)
		}
	}
	if err := closer(); err != nil {
		t.Fatalf("closer: %v", err)
	}

	// 「重启」：用同一份配置重新装配，分区记录必须回来。
	reopened, closer2 := openLogCatalogJournal(cfg, rootForTest(t, root), catalog.NewMemJournal())
	if closer2 != nil {
		defer func() { _ = closer2() }()
	}
	restarted := catalog.New(reopened)
	days := map[string]bool{}
	for _, k := range restarted.Keys() {
		if k.StorageNamespace == "inst:318" {
			days[k.UTCDay] = true
		}
	}
	for _, want := range []string{"2026-11-02", "2026-11-04"} {
		if !days[want] {
			t.Fatalf("重启后 inst:318 的 %s 分区记录必须仍存在（实测 %v）——丢了就是「VL 有数据但查询侧说这天没日志」", want, days)
		}
	}
}

// TestOpenLogCatalogJournal_ExplicitDisabledFallsBackToMemory 显式关闭 ⇒ 内存形态，不报错。
func TestOpenLogCatalogJournal_ExplicitDisabledFallsBackToMemory(t *testing.T) {
	root := t.TempDir()
	disabled := false
	cfg := catalogTestConfig(&disabled)
	mem := catalog.NewMemJournal()
	journal, closer := openLogCatalogJournal(cfg, rootForTest(t, root), mem)
	if journal != mem {
		t.Fatal("显式关闭时必须原样返回传入的内存 journal")
	}
	if closer != nil {
		t.Fatal("内存形态不应返回 closer")
	}
	if _, statErr := os.Stat(filepath.Join(root, "var", "log", "catalog.journal.jsonl")); statErr == nil {
		t.Fatal("显式关闭时不应创建 journal 文件")
	}
}

// TestOpenLogCatalogJournal_RejectsCorruptFileWithoutLosingIt 坏文件不能让进程卡死，也不能被静默丢掉。
//
// 中间行损坏（非末行）会让 NewJournalWithStore 报错。装配层的处置是：改名保留 + 重建空
// journal 继续启动。保留现场比「能启动」更重要——这是排障依据。
func TestOpenLogCatalogJournal_RejectsCorruptFileWithoutLosingIt(t *testing.T) {
	root := t.TempDir()
	logDir := filepath.Join(root, "var", "log")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(logDir, "catalog.journal.jsonl")
	// 造三行，把中间行弄坏（末行损坏是可容忍的，中间损坏才是真损坏）。
	store, err := catalog.NewJSONLFileStore(path)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	for i := 0; i < 3; i++ {
		key := catalog.PartitionKey{StorageNamespace: "inst:1", UTCDay: "2026-11-0" + string(rune('2'+i))}
		rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "d")
		if err := store.AppendEntry(catalog.JournalEntry{Seq: uint64(i + 1), Key: key, Record: rec}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := splitLines(string(b))
	if len(lines) < 3 {
		t.Fatalf("应有 3 行，实测 %d", len(lines))
	}
	lines[1] = "{corrupt"
	if err := os.WriteFile(path, []byte(joinLines(lines)), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	journal, closer := openLogCatalogJournal(catalogTestConfig(nil), rootForTest(t, root), catalog.NewMemJournal())
	if closer != nil {
		defer func() { _ = closer() }()
	}
	if journal == nil {
		t.Fatal("坏文件时也必须返回可用 journal（不能返回 nil 让调用方空指针）")
	}
	// 原文件必须被改名保留（而不是被删/被覆盖）。
	matches, globErr := filepath.Glob(path + ".corrupt-*")
	if globErr != nil {
		t.Fatalf("glob: %v", globErr)
	}
	if len(matches) == 0 {
		t.Fatal("坏文件必须改名保留为 .corrupt-*，不能静默丢弃（那是排障现场）")
	}
	kept, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(kept) != strings.Join(lines, "\n")+"\n" {
		t.Fatal("保留的坏文件内容必须与原文件逐字一致")
	}
}
