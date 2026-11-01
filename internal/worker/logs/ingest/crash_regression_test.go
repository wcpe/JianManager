package ingest

// 崩溃不丢回归（FR-496 spec §3.4）：真被 kill -9 之后，**不能出现半写的持久状态**。
//
// ## 2026-11-02：为什么本用例不再读"重启后的索引"
//
// 原用例：子进程反复 persist 批次，父进程 kill -9 后重新 `New()` 同一 root，再 `m.index.Load()`
// 断言四个字段指向同一批次 k。这在磁盘索引时代成立。索引库改成 `:memory:`（FR-505，机械盘
// 不落盘裁定）后，**进程一死，索引就没了**——重启后的库恒为空（实测 rows.Projections=[]），
// 所以它不是"half-batch"也不是任何其它东西，而是"没有"，旧断言在新架构下无法有意义地满足。
//
// 那"无半批次"还需要保吗？需要，但责任位置变了：
//  1. **原子提交**仍是真需求（一次 persist = 一个事务），它由 `stateindex` 自己的真实 SIGKILL
//     回归同构地守着（`TestStoreSIGKILLLeavesCompletePrefix` /
//     `TestStoreCrashDiscardsUncommittedBatch`）。那两条跑文件库，而内存库走的是同一个
//     `Apply` 路径、同样的切分与事务，原子性逻辑没有分叉。
//  2. **ingest 层真正能跨重启丢的东西不是索引，而是源登记与游标**。索引能重生，游标不能。
//     所以本用例改为锤 ingest 层真正的那条不变量：
//     **进程被 SIGKILL 后，它已经把"重启时该重建哪个源、读到哪"留在了持久面（游标表）上**。
//
// 用例名不变（它仍然是"强杀之后不丢东西、不出现半写"），断言换成了新架构下真正可验的形态。

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// crashRootEnv 是崩溃子进程的数据根目录。
const crashRootEnv = "JM_INGEST_CRASH_ROOT"

// crashBatchGapEnd 是每批推进的账本水位步长（用于「同一批次」的关联判据）。
const crashBatchGapEnd = 1000

// TestIngestCrashHelper 是崩溃子进程入口（由父用例经 -test.run 定向拉起）：
// 反复「推进一个批次 → 持久化 → 刷持久面 → 落标记」，等待父进程 kill -9。
func TestIngestCrashHelper(t *testing.T) {
	root := os.Getenv(crashRootEnv)
	if root == "" {
		t.Skip("非崩溃子进程，跳过")
	}
	logPath := filepath.Join(root, "latest.log")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatalf("子进程写日志占位文件失败: %v", err)
	}
	journal := catalog.NewMemJournal()
	cat := catalog.New(journal)
	source := SourceConfig{
		LogSourceID: "node:crash", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "ns:crash", UTCDay: "2026-09-30",
	}
	m, err := New(Options{Root: root, Catalog: cat, Journal: journal, Sources: []SourceConfig{source}})
	if err != nil {
		t.Fatalf("子进程创建 Manager 失败: %v", err)
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	if m.pipes[key] == nil {
		t.Fatalf("子进程缺少 pipeline")
	}
	for batch := int64(1); ; batch++ {
		// 批次内容：投影代次/水位、实例绑定——互相独立的字段。
		saved := m.state.Sources[key]
		saved.ProjectionGeneration = fmt.Sprintf("gen-%d", batch)
		saved.EventsStoredThrough = uint64(crashBatchGapEnd) * uint64(batch)
		m.state.Sources[key] = saved
		m.state.Instances["uuid-crash"] = InstanceBinding{
			UUID: "uuid-crash", TargetID: "ns:crash", Generation: "g1",
			Mode: pipeline.ModeFilePrimary, WorkDir: fmt.Sprintf("/srv/inst-%d", batch),
		}
		if err := m.persist(); err != nil {
			t.Fatalf("子进程持久化失败: %v", err)
		}
		// 每批都把**跨重启唯一持久面**（游标表）刷一次：崩溃后靠它重建源。
		m.flushCursorRegistryOnStop()
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("batch-%d.done", batch)), []byte("1"), 0o600); err != nil {
			t.Fatalf("子进程写标记失败: %v", err)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestManagerIndexNoHalfBatchAfterSIGKILL 是本回归主用例。
//
// 断言（都在新架构下真实可验）：
//  1. 子进程真的跑过三批以上，然后被 SIGKILL 掉；
//  2. 崩溃后能**重新把 Manager 起起来**：索引重生（不撞墙）、integrity_check 通过；
//  3. 重启后的索引是**空的**——这是 FR-505 的既定语义，必须作为正面断言钉住
//     （否则将来有人把索引改回落盘时，本用例不会告诉任何人）；
//  4. 跨重启能重建源：游标表里有那个源，且**行内字段互相一致**（源配置与游标路径同源，
//     不是一次半写留下的混合态）。
func TestManagerIndexNoHalfBatchAfterSIGKILL(t *testing.T) {
	if testing.Short() {
		t.Skip("SIGKILL 用例在 -short 下跳过")
	}
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestIngestCrashHelper$")
	cmd.Env = append(os.Environ(), crashRootEnv+"="+root)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	// 等到第 3 批落盘后强杀（SIGKILL，不可捕获、无 defer、无 Close）。
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "batch-3.done")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatal("崩溃子进程未在期限内完成第 3 批")
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.NoError(t, cmd.Process.Kill())
	_ = cmd.Wait()

	// ① 崩溃后重启：索引必须可**重新打开**、可校验、可继续用。
	m, err := New(Options{Root: root, Catalog: catalog.New(catalog.NewMemJournal()), Sources: []SourceConfig{{
		LogSourceID: "node:crash", SourceGeneration: "g1", Path: filepath.Join(root, "latest.log"),
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "ns:crash", UTCDay: "2026-09-30",
	}}})
	require.NoError(t, err, "kill -9 之后必须仍能打开索引并启动采集")
	t.Cleanup(func() { _ = crashClose(m) })
	require.NoError(t, m.index.IntegrityCheck(), "kill -9 之后 integrity_check 必须通过")

	// ② 正面钉住 FR-505 的既定语义：索引不落盘 ⇒ 强杀后重启读到的索引是空的。
	// 这条以前是"读回批次 k"的前提；现在是本用例要守的性质本身。
	rows, err := m.index.Load()
	require.NoError(t, err)
	require.Empty(t, rows.Projections, "索引改为 :memory: ⇒ 强杀后重启必须是空库（FR-505 的明确取舍）")
	require.Empty(t, rows.Instances, "所有索引行（含实例绑定）都不跨重启，与 row 种类无关")

	// ③ 跨重启唯一持久面：游标表里必须留下被杀进程登记过的源。
	cursorPath := filepath.Join(root, "var", "log", "ingest.cursor")
	registry := loadCursorRegistry(cursorPath)
	require.NotEmpty(t, registry, "崩溃后必须能从游标表重建源（唯一跨重启的采集状态）")
	entry, ok := registry["node:crash/g1"]
	require.True(t, ok, "游标表里必须有被杀子进程登记过的源：%v", cursorRegistryKeys(registry))

	// ④ 不出现半写：一行游标必须**自洽**——源配置存在、且行内路径与源配置路径逐字相同。
	// 这正是 sourcesFromCursorRegistry 用来判断"这一行能不能拿来重建源"的判据：
	// 两者不一致说明它们不是同一次写入的产物（半写），重建源就会指错文件。
	require.NotNil(t, entry.Source, "v2 行必须带源配置（否则重启后不知道采哪个文件）")
	require.Equal(t, "node:crash", entry.Source.LogSourceID)
	require.Equal(t, "g1", entry.Source.SourceGeneration)
	require.Equal(t, entry.Source.Path, entry.Path, "游标路径与源配置路径必须一致（不一致 = 半写）")
	require.Zero(t, entry.Cursor, "空日志文件的游标必须停在 0（夹具全程不追加字节）")

	// ⑤ 崩溃后仍可继续持久化 + 继续刷游标（状态没有卡在损坏处）。
	saved := m.state.Sources["node:crash/g1"]
	saved.ProjectionGeneration = "gen-after-crash"
	m.state.Sources["node:crash/g1"] = saved
	require.NoError(t, m.persist(), "崩溃后必须能继续持久化")
	m.flushCursorRegistryOnStop()
	require.NoError(t, m.index.IntegrityCheck())
}

// cursorRegistryKeys 用于失败信息：列出表里的键，便于一眼看出少了哪个源。
func cursorRegistryKeys(registry cursorRegistry) string {
	keys := make([]string, 0, len(registry))
	for key := range registry {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}
