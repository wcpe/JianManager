package logtail_test

// 本文件验的是**查询侧能不能看到新采集器写的数据**。
//
// # 为何必须单写（这是历史上真踩过的盲区）
//
// 之前的「投递后能查到」没有任何用例断言：所有测试只验写入侧（VL 收到几行），
// 于是出现过“数据进了 VL、查询侧却完全看不到”的缺陷。
// 新采集器绕过了整条旧投递链，而查询入口 `Planner.Plan` 只认 Catalog 分区记录——
// 不补记录就是同型缺陷的重演。

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtail"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
)

// TestCollectedDataIsQueryable 锤死「采到 ⇒ 查得到」端到端。
func TestCollectedDataIsQueryable(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "servers", "game-01")
	logPath := filepath.Join(workDir, "logs", "latest.log")
	writeLines(t, logPath, `[18:00:01] [Server thread/INFO]: hello`)

	// 真 Catalog（内存 journal）+ 真 Planner：与生产同一条查询链。
	cat := catalog.New(catalog.NewMemJournal())
	planner := query.NewPlanner(cat, nil)
	fake := newFakeVL(t)

	runner, err := logtail.Start(context.Background(), logtail.Config{
		Enabled:          true,
		Paths:            []string{filepath.Join(root, "servers", "*", "logs", "*.log")},
		ReadFromHead:     true,
		RegistryPath:     filepath.Join(root, "registry.jsonl"),
		ScanInterval:     30 * time.Millisecond,
		ProgressInterval: 30 * time.Millisecond,
		TimeZone:         time.UTC,
		Bindings:         newBindings(t, workDir),
		Catalog:          cat,
	}, fake.client(t), nil)
	if err != nil {
		t.Fatalf("启动采集器: %v", err)
	}
	defer runner.Stop()

	if !waitFor(t, 5*time.Second, func() bool { return fake.count() >= 1 }) {
		t.Fatalf("未采到行，实际 %d", fake.count())
	}

	// 分区记录必须建在**行里 _time 所属的 UTC 日**上，且 namespace 与源 id 同一口径。
	day := time.Now().UTC().Format("2006-01-02")
	key := catalog.PartitionKey{StorageNamespace: "inst:1", UTCDay: day}
	if !waitFor(t, 3*time.Second, func() bool {
		_, ok := cat.Get(key)
		return ok
	}) {
		t.Fatalf("投递后 Catalog 里没有分区记录 %v（查询侧会报「这天没日志」）", key)
	}

	// 真跑一次查询规划：它必须把这天的分区选进视图。
	res := planner.Plan(query.PlanRequest{
		Transient: true,
		RequestID: "probe",
		TargetIDs: []string{"inst:1"},
		TimeRange: query.TimeRange{
			FromUTC: time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339),
			ToUTC:   time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339),
		},
	})
	if res.Err != nil {
		t.Fatalf("Plan 失败: %v", res.Err)
	}
	if len(res.Ranges) == 0 {
		t.Fatalf("Plan 一个目标都没选中（查询侧看不到数据）：coverage=%+v", res.Coverage)
	}
	found := false
	for _, r := range res.Ranges {
		if r.Key.StorageNamespace == "inst:1" && r.Key.UTCDay == day {
			found = true
		}
	}
	if !found {
		t.Fatalf("Plan 未选中 inst:1 的 %s 分区，实际 ranges=%+v", day, res.Ranges)
	}
}

// TestPartitionEnsureDoesNotOverwriteExisting 验证「已有分区记录不被改写」。
//
// 已有记录可能来自迁移/重建（owner 或目录不同），强行改写是数据面破坏。
func TestPartitionEnsureDoesNotOverwriteExisting(t *testing.T) {
	cat := catalog.New(catalog.NewMemJournal())
	day := "2026-11-05"
	key := catalog.PartitionKey{StorageNamespace: "inst:9", UTCDay: day}

	// 预置一条「已在 COLD」的记录。
	existing := catalog.NewStableRecord(key, catalog.OwnerCold, 7, "cold-dir-7")
	existing.PublishedProjection = &catalog.PublishedProjection{QueryLocationDirID: "cold-dir-7", QueryGeneration: 7}
	if err := cat.AppendJournal(catalog.JournalEntry{Key: key, Record: existing}); err != nil {
		t.Fatalf("预置记录: %v", err)
	}

	ensurer := logtail.NewPartitionEnsurer(cat)
	if err := ensurer.Ensure("inst:9", day); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	rec, ok := cat.Get(key)
	if !ok {
		t.Fatal("记录丢了")
	}
	if rec.Owner != catalog.OwnerCold || rec.Generation != 7 {
		t.Fatalf("已有记录被改写了（owner=%s gen=%d）；只应在缺失时补建", rec.Owner, rec.Generation)
	}
}
