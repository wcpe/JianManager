package stateindex

// 本文件原是一批围绕 source_wal 谓词范围删除的「预算/崩溃/取消」回归。
//
// 2026-10-29：`source_wal` 表整体删除、范围剪枝（WALPrune）整条链下线，因此
//   - TestIndexPruneCrashHelper / TestIndexPruneCanceledAndKilledUnitsPreserveMirrorAndWAL
//     / TestIndexPruneRepeatedOwnerUsesConservativeWatermark / TestIndexPruneRespectsActualEightRowBudget
//     里针对范围删除的断言失去对象。这里把其中仍然成立、且与表无关的两条不变量
//     （「崩溃/取消后镜像不提前更新」「每条 SQL 语句 / 每个提交单元都受预算约束」）
//     改用 gap 行（同为按归属增量表）重写成等价用例；
//   - 归并相关的两条（TestIndexControlledTruncateClearsCheckpointObservations /
//     TestIndexPassiveTracksUnfinishedFramesSeparately）原样保留。

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// gapBoundedState 构造「一个源 + count 条 gap 行」的夹具（count 足够大写以触发切分）。
func gapBoundedState(key string, count int) State {
	var st State
	st.Sources = append(st.Sources, SourceRow{Key: key, LogSourceID: "node:bounded", SourceGeneration: "g1", StorageNamespace: "ns:bounded"})
	st.Positions = append(st.Positions, PositionRow{Key: key, ReadPos: 200, DurablePos: 150, ReclaimPos: 100})
	st.Projections = append(st.Projections, ProjectionRow{Key: key, Generation: "projection-1"})
	st.Aux = append(st.Aux, AuxRow{Key: key, Payload: []byte(`{}`)})
	for i := 0; i < count; i++ {
		st.Gaps = append(st.Gaps, GapRow{
			Key: key, ID: int64(i), StartPos: uint64(i * 10), EndPos: uint64(i*10 + 9),
			Reason: "PAUSED", Detail: fmt.Sprintf("gap-%d", i),
		})
	}
	return st
}

// TestIndexGapCrashHelper 在子进程里制造一段**有差异**的删除，并在提交前强杀自己。
//
// 必须制造差异：`Apply` 对相同内容会走幂等空转（不开事务、不调 commitChunks），
// 那样 testCrashHook 永远不会被调用。
func TestIndexGapCrashHelper(t *testing.T) {
	path := os.Getenv("JM_INDEX_CRASH")
	if path == "" {
		return
	}
	s, err := OpenWithBudget(path, CommitBudget{MaxRows: 8, MinRows: 1, CycleMaxRows: 8})
	if err != nil {
		t.Fatal(err)
	}
	s.testCrashHook = func() {
		if err := os.WriteFile(path+".before-commit", []byte("已进入提交前"), 0600); err != nil {
			t.Fatal(err)
		}
		process, err := os.FindProcess(os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		if err := process.Kill(); err != nil {
			t.Fatal(err)
		}
	}
	// 删掉一半 gap 行：必然产生差异 ⇒ 至少一个提交单元 ⇒ hook 在 COMMIT 前触发。
	_, _ = s.Apply(gapBoundedState("crash/g1", 100))
	t.Fatal("崩溃屏障未触发")
}

// TestIndexCanceledAndKilledUnitsPreserveMirror 钉住两条不变量：
//
//	① **强杀**（子进程 SIGKILL）后，镜像不得包含未提交的行（回退幅度 ≤ 一个提交单元）；
//	② **ctx 取消**时当前单元回滚、镜像不动，且连接可用、后续调用能接着做完（不越界、不假完成）。
//
// 转红方式：把镜像更新从"提交成功后"提前到"提交前"（或去掉 runUnit 的 ROLLBACK），
// ①/② 的镜像行数断言立刻变红。
func TestIndexCanceledAndKilledUnitsPreserveMirror(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seed := gapBoundedState("crash/g1", 20000)
	for {
		stats, err := s.Apply(seed)
		if err != nil {
			t.Fatal(err)
		}
		if !stats.Incomplete {
			break
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// ① 子进程强杀：未提交的单元整体丢弃。
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "-test.run=^TestIndexGapCrashHelper$", "-test.timeout=30s")
	command.Env = append(os.Environ(), "JM_INDEX_CRASH="+path)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(err.Error(), "killed") {
		t.Fatalf("未真实强杀：%s %v", output, err)
	}
	if _, err := os.Stat(path + ".before-commit"); err != nil {
		t.Fatal("没有进入提交前屏障")
	}
	s, err = OpenWithBudget(path, CommitBudget{MaxRows: 8, MinRows: 1, CycleMaxRows: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if got := len(s.mirror["gap"]["crash/g1"]); got != len(seed.Gaps) {
		t.Fatalf("强杀后未提交删除残留：%d（期望 %d）", got, len(seed.Gaps))
	}

	// ② 取消：本单元回滚，镜像不动；随后同一目标可以续跑完。
	desired := gapBoundedState("crash/g1", 100) // 删掉大量 gap 行
	ctx, cancel := context.WithCancel(context.Background())
	s.testCrashHook = cancel
	if _, err := s.ApplyScopedCtx(ctx, desired, []string{"crash/g1"}); err == nil {
		t.Fatal("取消仍被当成已提交")
	}
	s.testCrashHook = nil
	if got := len(s.mirror["gap"]["crash/g1"]); got != len(seed.Gaps) {
		t.Fatalf("取消更新了未提交镜像：%d", got)
	}
	rounds := 0
	for {
		stats, err := s.ApplyScopedCtx(context.Background(), desired, []string{"crash/g1"})
		if err != nil {
			t.Fatalf("取消后连接未恢复：%v", err)
		}
		rounds++
		if !stats.Incomplete {
			break
		}
		if stats.RowsDeleted > 8 {
			t.Fatalf("单个提交单元越过行数预算：%+v", stats)
		}
		if rounds > 100000 {
			t.Fatal("续跑不收敛")
		}
	}
	if rounds < 2 {
		t.Fatalf("20000 行差异在 8 行/单元下必须分多轮完成，实测 %d 轮", rounds)
	}
	loaded, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Gaps) != len(desired.Gaps) {
		t.Fatalf("续跑后 gap 行数不符：得到 %d，期望 %d", len(loaded.Gaps), len(desired.Gaps))
	}
}

// TestIndexDeleteRespectsRowBudget 把「每个提交单元 / 每条 SQL 语句都受行数预算约束」
// 钉在**普通逐行路径**上（范围剪枝已不存在，这是现在唯一的删除路径）：
// 8 行/单元时删 20000 行必须分多轮，每轮删除行数 ≤ 8，最终删净且不误删。
func TestIndexDeleteRespectsRowBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	const key = "bounded/g1"
	const count = 20000
	seed := gapBoundedState(key, count)
	for {
		stats, err := s.Apply(seed)
		if err != nil {
			t.Fatal(err)
		}
		if !stats.Incomplete {
			break
		}
	}
	desired := gapBoundedState(key, count/2) // 删掉后一半
	s.budget.MaxRows, s.budget.MinRows, s.budget.CycleMaxRows, s.rowBudget = 8, 1, 8, 8
	before := len(s.Samples())
	stats, err := s.ApplyScopedCtx(context.Background(), desired, []string{key})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("实际删行=%d，统计=%+v", stats.RowsDeleted, stats)
	if stats.RowsDeleted == 0 || stats.RowsDeleted > 8 || stats.MaxChunkRows > 8 || !stats.Incomplete {
		t.Fatalf("删除必须受事务与周期预算约束，实际=%+v", stats)
	}
	for _, sample := range s.Samples()[before:] {
		if sample.Rows() > 8 {
			t.Fatalf("单元越界：%+v", sample)
		}
	}
	// 未提交的行必须仍在镜像里（镜像只在提交成功后更新）。
	if got := len(s.mirror["gap"][key]); got != count-int(stats.RowsDeleted) {
		t.Fatalf("镜像提前删除：%d（期望 %d）", got, count-int(stats.RowsDeleted))
	}
	// 续跑：下一个单元也只能删 ≤8 行，且确实在推进。
	next, err := s.ApplyScopedCtx(context.Background(), desired, []string{key})
	if err != nil {
		t.Fatal(err)
	}
	if next.RowsDeleted == 0 || next.RowsDeleted > 8 {
		t.Fatalf("续跑未遵守预算：%+v", next)
	}
}

func TestIndexControlledTruncateClearsCheckpointObservations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	const key = "truncate-telemetry/g1"
	desired := State{Sources: []SourceRow{{Key: key, LogSourceID: "truncate-telemetry", SourceGeneration: "g1"}}, Aux: []AuxRow{{Key: key, Payload: []byte("initial")}}}
	if _, err := s.Apply(desired); err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	tx, err := reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var payload string
	if err := tx.QueryRow("SELECT payload FROM source_aux WHERE key=?", key).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		desired.Aux[0].Payload = []byte(fmt.Sprintf("%02d", i) + strings.Repeat(string(rune('A'+i)), 1<<20))
		if _, err := s.Apply(desired); err != nil {
			t.Fatal(err)
		}
	}
	if s.Latency().CheckpointRemainingFrames <= 0 {
		t.Fatal("未构造未完成被动归并")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	s.SetControlledTruncate(true)
	if !s.TryControlledTruncate(context.Background()) {
		t.Fatal("受控归并未执行")
	}
	actual := s.Latency()
	if actual.CheckpointRemainingFrames != 0 || actual.CheckpointIncompleteDuration != 0 || actual.WALBytes != 0 {
		t.Fatalf("成功受控截断后仍谎报旧未完成帧/持续时间：%+v", actual)
	}
}

func TestIndexPassiveTracksUnfinishedFramesSeparately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	const key = "reader-pinned/g1"
	desired := State{Sources: []SourceRow{{Key: key, LogSourceID: "reader-pinned", SourceGeneration: "g1"}}, Aux: []AuxRow{{Key: key, Payload: []byte("initial")}}}
	if _, err := s.Apply(desired); err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	tx, err := reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var payload string
	if err := tx.QueryRow("SELECT payload FROM source_aux WHERE key=?", key).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		desired.Aux[0].Payload = []byte(fmt.Sprintf("%02d", i) + strings.Repeat(string(rune('A'+i)), 512<<10))
		if _, err := s.Apply(desired); err != nil {
			t.Fatal(err)
		}
	}
	s.checkpointPassiveIfNeeded(context.Background())
	latency := reflect.ValueOf(s.Latency())
	pending := latency.FieldByName("CheckpointRemainingFrames")
	unfinished := latency.FieldByName("CheckpointIncomplete")
	if !pending.IsValid() || !unfinished.IsValid() || pending.Int() <= 0 || unfinished.Int() <= 0 {
		t.Fatalf("PASSIVE未完成帧必须独立于busy记录：busy=%d log=%d moved=%d", s.ckptBusy.Load(), s.ckptLogPages.Load(), s.ckptMoved.Load())
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	s.checkpointPassiveIfNeeded(context.Background())
	latency = reflect.ValueOf(s.Latency())
	if got := latency.FieldByName("CheckpointRemainingFrames").Int(); got != 0 {
		t.Fatalf("释放快照后仍报剩余帧：%d", got)
	}
	if got := latency.FieldByName("CheckpointIncompleteDuration").Int(); got != 0 {
		t.Fatalf("释放快照后未重置持续时间：%d", got)
	}
	if s.ckptTruncate.Load() != 0 {
		t.Fatal("默认不得强制截断")
	}
}
