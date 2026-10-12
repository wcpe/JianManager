package ingest

// 崩溃不丢回归（FR-496 spec §3.4）：
//   真被 kill -9 之后，索引仍可读，且只回退不超过一个批次——绝不出现「半个批次」。

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// crashRootEnv 是崩溃子进程的数据根目录。
const crashRootEnv = "JM_INGEST_CRASH_ROOT"

// crashBatchGapEnd 是每批推进的账本水位步长（用于「同一批次」的关联判据）。
const crashBatchGapEnd = 1000

// crashBatchFields 是同一批次必须同时成立的关联字段集合。
//
// 为什么这样断言：一次 persist 是一个事务，因此崩后读回的索引必须整体对应**某一个**批次 k。
// 只要这几个互相独立的字段能推到同一个 k，就证明没有任何一个是「半写」的
// ——退回逐行 autocommit（或先改内存后落库）时，必然出现 k 不一致。
type crashBatchFields struct {
	batch           int64  // 由 projection.generation 推出的批次号
	eventsThrough   uint64 // projection.events_stored_through = crashBatchGapEnd*k
	instanceWorkDir string // instance_binding.work_dir = /srv/inst-k
	gapCount        int    // 缺口逐批追加，数量应恰为 k
}

// TestIngestCrashHelper 是崩溃子进程入口（由父用例经 -test.run 定向拉起）：
// 反复「推进一个批次 → 持久化 → 落标记」，等待父进程 kill -9。
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
	pipe := m.pipes[key]
	if pipe == nil {
		t.Fatalf("子进程缺少 pipeline")
	}
	for batch := int64(1); ; batch++ {
		// 批次内容：投影代次/水位、实例绑定、账本缺口——互相独立的四个字段，同批推进。
		saved := m.state.Sources[key]
		saved.ProjectionGeneration = fmt.Sprintf("gen-%d", batch)
		saved.EventsStoredThrough = uint64(crashBatchGapEnd) * uint64(batch)
		m.state.Sources[key] = saved
		m.state.Instances["uuid-crash"] = InstanceBinding{
			UUID: "uuid-crash", TargetID: "ns:crash", Generation: "g1",
			Mode: pipeline.ModeFilePrimary, WorkDir: fmt.Sprintf("/srv/inst-%d", batch),
		}
		if err := pipe.Ledger().RecordGap(ledger.SourceKey{LogSourceID: source.LogSourceID,
			SourceGeneration: source.SourceGeneration},
			uint64(crashBatchGapEnd)*uint64(batch), uint64(crashBatchGapEnd)*uint64(batch)+1,
			fmt.Sprintf("BATCH-%d", batch), "crash helper"); err != nil {
			t.Fatalf("子进程登记缺口失败: %v", err)
		}
		if err := m.persist(); err != nil {
			t.Fatalf("子进程持久化失败: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("batch-%d.done", batch)), []byte("1"), 0o600); err != nil {
			t.Fatalf("子进程写标记失败: %v", err)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestManagerIndexNoHalfBatchAfterSIGKILL 是本回归主用例。
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

	// 崩溃后重启：索引必须可读、可校验、可继续用。
	m, err := New(Options{Root: root, Catalog: catalog.New(catalog.NewMemJournal()), Sources: []SourceConfig{{
		LogSourceID: "node:crash", SourceGeneration: "g1", Path: filepath.Join(root, "latest.log"),
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "ns:crash", UTCDay: "2026-09-30",
	}}})
	require.NoError(t, err, "kill -9 之后必须仍能打开索引并启动采集")
	t.Cleanup(func() { _ = crashClose(m) })
	require.NoError(t, m.index.IntegrityCheck(), "kill -9 之后 integrity_check 必须通过")

	rows, err := m.index.Load()
	require.NoError(t, err)
	require.NotEmpty(t, rows.Projections)

	// 读回四个互相关联的字段（它们必须指向同一个批次）。
	fields := crashBatchFields{instanceWorkDir: "", gapCount: len(rows.Gaps)}
	require.Len(t, rows.Projections, 1)
	var matched bool
	fields.batch, matched = parseCrashBatch(rows.Projections[0].Generation)
	require.True(t, matched, "投影代次必须形如 gen-<k>，实测 %q", rows.Projections[0].Generation)
	fields.eventsThrough = rows.Projections[0].EventsStoredThrough
	for _, row := range rows.Instances {
		if row.UUID == "uuid-crash" {
			fields.instanceWorkDir = row.WorkDir
		}
	}

	// ① 回退不超过一个批次：已观测到第 3 批，则读回批次必须 ≥ 3。
	require.GreaterOrEqual(t, fields.batch, int64(3),
		"观测到第 3 批完成后，索引不得回退到更早（回退幅度不得超过一个批次）")
	// ② 绝无半写：四个字段必须指向同一个批次。
	require.Equal(t, uint64(crashBatchGapEnd)*uint64(fields.batch), fields.eventsThrough,
		"投影水位与代次必须同批：gen-%d 但水位 %d", fields.batch, fields.eventsThrough)
	require.Equal(t, fmt.Sprintf("/srv/inst-%d", fields.batch), fields.instanceWorkDir,
		"实例绑定必须与投影同批（批次 %d）", fields.batch)
	require.Equal(t, int(fields.batch), fields.gapCount,
		"缺口条数必须与批次号一致（逐批一条），实测 %d", fields.gapCount)
	// ③ 缺口内容也必须是完整批次：第 k 条缺口的 reason 恰为 BATCH-k。
	for index, row := range rows.Gaps {
		require.Equal(t, fmt.Sprintf("BATCH-%d", index+1), row.Reason,
			"缺口必须逐批完整落库，实测第 %d 条为 %q", index+1, row.Reason)
	}
	// ④ 崩溃后仍可继续持久化（索引没有卡在损坏状态）。
	saved := m.state.Sources["node:crash/g1"]
	saved.ProjectionGeneration = fmt.Sprintf("gen-%d", fields.batch+1)
	m.state.Sources["node:crash/g1"] = saved
	require.NoError(t, m.persist(), "崩溃后必须能继续持久化")
	require.NoError(t, m.index.IntegrityCheck())
}

// parseCrashBatch 解析 "gen-<k>" 形式的投影代次。
func parseCrashBatch(generation string) (int64, bool) {
	var batch int64
	if _, err := fmt.Sscanf(generation, "gen-%d", &batch); err != nil {
		return 0, false
	}
	return batch, batch > 0
}
