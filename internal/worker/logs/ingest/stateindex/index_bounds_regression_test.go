package stateindex

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

func TestIndexPruneCrashHelper(t *testing.T) {
	path := os.Getenv("JM_INDEX_PRUNE_CRASH")
	if path == "" {
		return
	}
	s, err := OpenWithBudget(path, CommitBudget{MaxRows: 8, MinRows: 1, CycleMaxRows: 8})
	if err != nil {
		t.Fatal(err)
	}
	seed := walPruneState("crash-prune/g1", 20000, 1, 3)
	desired := seed
	desired.WAL = append([]WALRow(nil), seed.WAL[20000:]...)
	s.testCrashHook = func() {
		if err := os.WriteFile(path+".before-commit", []byte("已进入范围删除提交前"), 0600); err != nil {
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
	_, _ = s.ApplyScopedCtx(context.Background(), desired, []string{"crash-prune/g1"}, []WALPrune{{Owner: "crash-prune/g1", EndThrough: 200000}})
	t.Fatal("范围删除崩溃屏障未触发")
}

func TestIndexPruneCanceledAndKilledUnitsPreserveMirrorAndWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seed := walPruneState("crash-prune/g1", 20000, 1, 3)
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
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "-test.run=^TestIndexPruneCrashHelper$", "-test.timeout=30s")
	command.Env = append(os.Environ(), "JM_INDEX_PRUNE_CRASH="+path)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(err.Error(), "killed") {
		t.Fatalf("未真实强杀：%s %v", output, err)
	}
	if _, err := os.Stat(path + ".before-commit"); err != nil {
		t.Fatal("没有进入范围删除提交前屏障")
	}
	s, err = OpenWithBudget(path, CommitBudget{MaxRows: 8, MinRows: 1, CycleMaxRows: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if got := len(s.mirror["source_wal"]["crash-prune/g1"]); got != len(seed.WAL) {
		t.Fatalf("强杀后未提交删除残留：%d", got)
	}
	desired := seed
	desired.WAL = append([]WALRow(nil), seed.WAL[20000:]...)
	ctx, cancel := context.WithCancel(context.Background())
	s.testCrashHook = cancel
	if _, err := s.ApplyScopedCtx(ctx, desired, []string{"crash-prune/g1"}, []WALPrune{{Owner: "crash-prune/g1", EndThrough: 200000}}); err == nil {
		t.Fatal("取消仍被当成已提交")
	}
	s.testCrashHook = nil
	if got := len(s.mirror["source_wal"]["crash-prune/g1"]); got != len(seed.WAL) {
		t.Fatalf("取消更新了未提交镜像：%d", got)
	}
	for round := 0; round < 3; round++ {
		stats, err := s.ApplyScopedCtx(context.Background(), desired, []string{"crash-prune/g1"}, []WALPrune{{Owner: "crash-prune/g1", EndThrough: 200000}})
		if err != nil {
			t.Fatalf("取消后连接未恢复：%v", err)
		}
		if stats.RowsDeleted != 8 || !stats.Incomplete {
			t.Fatalf("取消/强杀后范围续跑越界或假完成：%+v", stats)
		}
	}
	var nonDurable int
	if err := s.db.QueryRow("SELECT count(*) FROM source_wal WHERE key=? AND durable=0", "crash-prune/g1").Scan(&nonDurable); err != nil {
		t.Fatal(err)
	}
	if nonDurable != 3 {
		t.Fatalf("范围剪枝删掉非耐久责任：%d", nonDurable)
	}
}

func TestIndexPruneRepeatedOwnerUsesConservativeWatermark(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	seed := walPruneState("duplicate-prune/g1", 3, 0, 0)
	if _, err := s.Apply(seed); err != nil {
		t.Fatal(err)
	}
	desired := seed
	desired.WAL = append([]WALRow(nil), seed.WAL[1:2]...)
	if _, err := s.ApplyScopedCtx(context.Background(), desired, []string{"duplicate-prune/g1"}, []WALPrune{{Owner: "duplicate-prune/g1", EndThrough: 30}, {Owner: "duplicate-prune/g1", EndThrough: 10}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.WAL) != 1 || loaded.WAL[0].RecordEnd != 20 {
		t.Fatalf("重复归属提示绕过最小水位等价性守门：%+v", loaded.WAL)
	}
}

func TestIndexPruneRespectsActualEightRowBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	const key = "bounded-prune/g1"
	const count = 20000
	seed := walPruneState(key, count, 1, 0)
	for {
		stats, err := s.Apply(seed)
		if err != nil {
			t.Fatal(err)
		}
		if !stats.Incomplete {
			break
		}
	}
	desired := seed
	desired.WAL = append([]WALRow(nil), seed.WAL[len(seed.WAL)-1:]...)
	s.budget.MaxRows, s.budget.MinRows, s.budget.CycleMaxRows, s.rowBudget = 8, 1, 8, 8
	before := len(s.Samples())
	stats, err := s.ApplyScopedCtx(context.Background(), desired, []string{key}, []WALPrune{{Owner: key, EndThrough: count * 10}})
	if err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := s.db.QueryRow("SELECT count(*) FROM source_wal WHERE key=? AND durable=1 AND record_end<=?", key, count*10).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	actual := count - remaining
	t.Logf("实际删行=%d，统计=%+v，剩余=%d", actual, stats, remaining)
	if actual > 8 || actual < 1 || stats.RowsDeleted != actual || stats.RangePruned != actual || stats.MaxChunkRows > 8 || !stats.Incomplete {
		t.Fatalf("范围剪枝必须按实际变化行受事务和周期预算约束，实际=%d，统计=%+v", actual, stats)
	}
	for _, sample := range s.Samples()[before:] {
		if sample.Rows() > 8 {
			t.Fatalf("单元越界：%+v", sample)
		}
	}
	// 未提交行必须仍在镜像，非耐久行也必须保留。
	if got := len(s.mirror["source_wal"][key]); got != len(seed.WAL)-actual {
		t.Fatalf("镜像提前删除：%d", got)
	}
	if _, err := s.ApplyScopedCtx(context.Background(), desired, []string{key}, []WALPrune{{Owner: key, EndThrough: count * 10}}); err != nil {
		t.Fatal(err)
	}
	var next int
	if err := s.db.QueryRow("SELECT count(*) FROM source_wal WHERE key=? AND durable=1 AND record_end<=?", key, count*10).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if next >= remaining || remaining-next > 8 {
		t.Fatalf("续跑未遵守预算：%d -> %d", remaining, next)
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
