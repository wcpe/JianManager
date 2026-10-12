package stateindex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const migrationTestSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestCompletedMigrationOnlyReportsVerifiedOwner(t *testing.T) {
	ctx := context.Background()
	store := newTestStoreWithBudget(t, CommitBudget{CycleMaxRows: 3})
	if complete, err := store.CompletedMigration(ctx, migrationTestSHA, 1); err != nil || complete {
		t.Fatalf("无凭据库不能被视为完成: %v %v", complete, err)
	}
	desired := fixtureState(1)
	migration, err := store.BeginMigration(ctx, migrationTestSHA, 1, desired)
	if err != nil {
		t.Fatal(err)
	}
	if complete, err := store.CompletedMigration(ctx, migrationTestSHA, 1); err != nil || complete {
		t.Fatalf("已开始但未提交不能视为完成: %v %v", complete, err)
	}
	for i := 0; i < 10; i++ {
		stats, err := store.ResumeMigration(ctx, migration)
		if err != nil {
			t.Fatal(err)
		}
		if !stats.Incomplete {
			break
		}
	}
	if complete, err := store.CompletedMigration(ctx, migrationTestSHA, 1); err != nil || complete {
		t.Fatalf("提交完但未验全不能视为完成: %v %v", complete, err)
	}
	if err := store.FinalizeMigration(ctx, migration); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("DELETE FROM delivery_batch"); err != nil {
		t.Fatal(err)
	}
	before := mustLoad(t, store)
	reader, err := OpenReadOnly(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if complete, err := reader.CompletedMigration(ctx, strings.ToUpper(migrationTestSHA), 1); err != nil || !complete {
		t.Fatalf("已完成身份允许调用方另验合法剪枝: %v %v", complete, err)
	}
	for _, test := range []struct {
		sha     string
		version int
	}{
		{strings.Repeat("b", 64), 1}, {migrationTestSHA, 2}, {"invalid", 1}, {migrationTestSHA, 0},
	} {
		if complete, err := reader.CompletedMigration(ctx, test.sha, test.version); err == nil || complete {
			t.Fatalf("错误来源与版本必须拒绝: %+v %v %v", test, complete, err)
		}
	}
	assertSameState(t, before, mustLoad(t, store))
	if _, err := store.BeginMigration(ctx, migrationTestSHA, 1, desired); err == nil {
		t.Fatal("身份查询不能放宽Begin实际行验真")
	}
}

func TestCompletedMigrationRejectsCorruptedProof(t *testing.T) {
	for _, proof := range []string{
		`not-json`,
		`{"source_sha":"` + migrationTestSHA + `","mapping_version":1,"state_sha":"` + migrationTestSHA + `","total":3,"committed":2,"complete":true}`,
		`{"source_sha":"` + migrationTestSHA + `","mapping_version":1,"state_sha":"invalid","total":3,"committed":3,"complete":true}`,
		`{"source_sha":"` + migrationTestSHA + `","mapping_version":1,"state_sha":"` + migrationTestSHA + `","total":-1,"committed":-1,"complete":true}`,
	} {
		store := newTestStore(t)
		if _, err := store.db.Exec("INSERT INTO meta(key,value) VALUES(?,?)", migrationMetaKey, proof); err != nil {
			t.Fatal(err)
		}
		if complete, err := store.CompletedMigration(context.Background(), migrationTestSHA, 1); err == nil || complete {
			t.Fatalf("损坏完成凭据必须拒绝: %s %v %v", proof, complete, err)
		}
		var after string
		if err := store.db.QueryRow("SELECT value FROM meta WHERE key=?", migrationMetaKey).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if after != proof {
			t.Fatal("查询不得改写凭据")
		}
	}
}

func TestMigrationResumesAcrossThreeOpensWithinBudget(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "index.db")
	budget := CommitBudget{MaxRows: 4, MinRows: 2, Target: 40 * time.Millisecond, CycleMaxRows: 5}
	desired := chunkFixtureState(2, 16)
	for cycle := 0; cycle < 3; cycle++ {
		store, err := OpenWithBudget(path, budget)
		if err != nil {
			t.Fatal(err)
		}
		if cycle > 0 {
			if _, err := store.Apply(desired); err == nil {
				t.Fatal("重启不能绕过未完成迁移所有权")
			}
		}
		migration, err := store.BeginMigration(ctx, migrationTestSHA, 1, desired)
		if err != nil {
			t.Fatal(err)
		}
		stats, err := store.ResumeMigration(ctx, migration)
		if err != nil {
			t.Fatal(err)
		}
		if stats.RowsWritten > 5 || stats.MaxChunkRows > 4 {
			t.Fatalf("预算越界: %+v", stats)
		}
		if !stats.Incomplete {
			t.Fatal("前三级启动应保留未完成进度")
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	store, err := OpenWithBudget(path, budget)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	migration, err := store.BeginMigration(ctx, migrationTestSHA, 1, desired)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		stats, err := store.ResumeMigration(ctx, migration)
		if err != nil {
			t.Fatal(err)
		}
		if stats.RowsWritten > 5 || stats.MaxChunkRows > 4 {
			t.Fatalf("预算越界: %+v", stats)
		}
		if !stats.Incomplete {
			break
		}
		if i == 29 {
			t.Fatal("迁移未能收敛")
		}
	}
	if err := store.FinalizeMigration(ctx, migration); err != nil {
		t.Fatal(err)
	}
	assertSameState(t, desired, mustLoad(t, store))
	stats, err := store.Apply(desired)
	if err != nil || stats.RowsWritten != 0 {
		t.Fatalf("迁移后不应重写历史: %+v %v", stats, err)
	}
}

func TestMigrationRejectsChangedOwnerAndCommittedContent(t *testing.T) {
	for _, mutation := range []string{"source", "mapping", "desired", "aux", "wal", "extra", "missing"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			store := newTestStoreWithBudget(t, CommitBudget{CycleMaxRows: 7, MaxRows: 4, MinRows: 2})
			desired := fixtureState(1)
			migration, err := store.BeginMigration(ctx, migrationTestSHA, 1, desired)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.ResumeMigration(ctx, migration); err != nil {
				t.Fatal(err)
			}
			path := store.Path()
			if mutation == "aux" {
				_, err = store.db.Exec("UPDATE source_aux SET payload='篡改正文'")
			}
			if mutation == "wal" {
				_, err = store.db.Exec("UPDATE source_wal SET body='篡改正文'")
			}
			if mutation == "extra" {
				_, err = store.db.Exec("INSERT INTO instance_binding VALUES ('extra','n','g','m','w')")
			}
			if mutation == "missing" {
				_, err = store.db.Exec("DELETE FROM source_aux")
			}
			if err != nil {
				t.Fatal(err)
			}
			before := mustLoad(t, store)
			if err = store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			sha, version := migrationTestSHA, 1
			if mutation == "source" {
				sha = strings.Repeat("b", 64)
			}
			if mutation == "mapping" {
				version++
			}
			if mutation == "desired" {
				desired.WAL[0].Body = []byte("新映射正文")
			}
			if _, err = store.BeginMigration(ctx, sha, version, desired); err == nil {
				t.Fatal("改变的来源或已提交实际行必须拒绝")
			}
			assertSameState(t, before, mustLoad(t, store))
		})
	}
}

func TestMigrationRejectsUnownedPartialAndAcceptsExact(t *testing.T) {
	for _, exact := range []bool{false, true} {
		store := newTestStoreWithBudget(t, CommitBudget{CycleMaxRows: 3})
		desired := fixtureState(1)
		for i := 0; i < 10; i++ {
			stats, err := store.Apply(desired)
			if err != nil {
				t.Fatal(err)
			}
			if !exact || !stats.Incomplete {
				break
			}
		}
		before := mustLoad(t, store)
		migration, err := store.BeginMigration(context.Background(), migrationTestSHA, 1, desired)
		if !exact {
			if err == nil {
				t.Fatal("无凭据半库不得被猜测接管")
			}
			assertSameState(t, before, mustLoad(t, store))
		} else {
			if err != nil {
				t.Fatal(err)
			}
			stats, err := store.ResumeMigration(context.Background(), migration)
			if err != nil || stats.RowsWritten != 0 || stats.Incomplete {
				t.Fatalf("等价库不应重写: %+v %v", stats, err)
			}
			if err = store.FinalizeMigration(context.Background(), migration); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestMigrationCrashDiscardsRowsAndProgress 同时检验数据与凭据在真正的进程中断后保留同一个前缀。
func TestMigrationCrashDiscardsRowsAndProgress(t *testing.T) {
	if path := os.Getenv("STATEINDEX_MIGRATION_CRASH_PATH"); path != "" {
		store, err := OpenWithBudget(path, CommitBudget{MaxRows: 3, MinRows: 3, CycleMaxRows: 6})
		if err != nil {
			t.Fatal(err)
		}
		migration, err := store.BeginMigration(context.Background(), migrationTestSHA, 1, fixtureState(1))
		if err != nil {
			t.Fatal(err)
		}
		units := 0
		store.testCrashHook = func() {
			units++
			if units == 2 {
				os.Exit(73)
			}
		}
		_, _ = store.ResumeMigration(context.Background(), migration)
		t.Fatal("崩溃注入未触发")
	}
	path := filepath.Join(t.TempDir(), "crash.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestMigrationCrashDiscardsRowsAndProgress$")
	cmd.Env = append(os.Environ(), "STATEINDEX_MIGRATION_CRASH_PATH="+path)
	out, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 73 {
		t.Fatalf("预期退出73: %v %s", err, out)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	migration, err := store.BeginMigration(context.Background(), migrationTestSHA, 1, fixtureState(1))
	if err != nil {
		t.Fatalf("崩溃后行与凭据前缀不匹配: %v", err)
	}
	counts, err := store.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if counts.Sources+counts.Positions+counts.Gaps+counts.Projections+counts.Instances+counts.Aux+counts.WAL+counts.Batches != 3 {
		t.Fatalf("只应保留第一提交单元: %+v", counts)
	}
	for i := 0; i < 10; i++ {
		stats, err := store.ResumeMigration(context.Background(), migration)
		if err != nil {
			t.Fatal(err)
		}
		if !stats.Incomplete {
			break
		}
	}
	if err = store.FinalizeMigration(context.Background(), migration); err != nil {
		t.Fatal(err)
	}
	assertSameState(t, fixtureState(1), mustLoad(t, store))
}

func TestMigrationResumeDoesNotRescanHistory(t *testing.T) {
	store := newTestStoreWithBudget(t, CommitBudget{MaxRows: 4, MinRows: 2, CycleMaxRows: 5})
	validations := 0
	store.testMigrationValidateHook = func() { validations++ }
	desired := chunkFixtureState(2, 128)
	migration, err := store.BeginMigration(context.Background(), migrationTestSHA, 1, desired)
	if err != nil {
		t.Fatal(err)
	}
	cycles := 0
	totalWritten := 0
	for cycles < 100 {
		cycles++
		stats, err := store.ResumeMigration(context.Background(), migration)
		if err != nil {
			t.Fatal(err)
		}
		totalWritten += stats.RowsWritten
		if stats.RowsPlanned != 0 {
			t.Fatalf("续跑不得重新规划全量: %+v", stats)
		}
		if !stats.Incomplete {
			break
		}
	}
	if cycles < 50 || cycles >= 100 {
		t.Fatalf("小周期必须推进且收敛: %d", cycles)
	}
	if validations != 1 {
		t.Fatalf("正常续跑不应重新扫描历史: %d", validations)
	}
	if totalWritten != len(migration.rows) {
		t.Fatalf("已提交行不能重写: %d != %d", totalWritten, len(migration.rows))
	}
	if err := store.FinalizeMigration(context.Background(), migration); err != nil {
		t.Fatal(err)
	}
	if validations != 2 {
		t.Fatalf("只允许开始与最终验全各一次: %d", validations)
	}
	t.Logf("周期=%d，实际写入=%d，完整扫描=%d", cycles, totalWritten, validations)
}

func TestMigrationRejectsExternalCommittedTamperBeforeResume(t *testing.T) {
	store := newTestStoreWithBudget(t, CommitBudget{CycleMaxRows: 7})
	migration, err := store.BeginMigration(context.Background(), migrationTestSHA, 1, fixtureState(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResumeMigration(context.Background(), migration); err != nil {
		t.Fatal(err)
	}
	external, err := Open(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	if _, err = external.db.Exec("UPDATE source_aux SET payload='外部篡改' "); err != nil {
		t.Fatal(err)
	}
	before := mustLoad(t, store)
	if _, err = store.ResumeMigration(context.Background(), migration); err == nil {
		t.Fatal("外部提交篡改后不得继续写入")
	}
	assertSameState(t, before, mustLoad(t, store))
}

// TestMigrationUnitGuardPreservesFutureExternalRows 放行真实独立写者，覆盖预检后和两个提交单元之间的写入窗口。
func TestMigrationUnitGuardPreservesFutureExternalRows(t *testing.T) {
	for _, window := range []string{"after_check", "between_chunks"} {
		t.Run(window, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := newTestStoreWithBudget(t, CommitBudget{MaxRows: 2, MinRows: 2, CycleMaxRows: 2})
			desired := fixtureState(1)
			migration, err := store.BeginMigration(ctx, migrationTestSHA, 1, desired)
			if err != nil {
				t.Fatal(err)
			}
			if stats, err := store.ResumeMigration(ctx, migration); err != nil || !stats.Incomplete {
				t.Fatalf("初始提交失败: %+v %v", stats, err)
			}
			store.budget.CycleMaxRows = 12
			external, err := Open(store.Path())
			if err != nil {
				t.Fatal(err)
			}
			defer external.Close()
			allow := make(chan struct{})
			done := make(chan error, 1)
			future := desired.WAL[len(desired.WAL)-1]
			const foreignBody = "独立写者的未来正文"
			go func() {
				select {
				case <-allow:
				case <-ctx.Done():
					return
				}
				_, err := external.db.ExecContext(ctx, "INSERT INTO source_wal(key,seq,event_id,record_start,record_end,appended,durable,body,fp) VALUES(?,?,?,?,?,?,?,?,?)", future.Key, toInt64(future.Seq), future.EventID, toInt64(future.RecordStart), toInt64(future.RecordEnd), boolToInt(future.Appended), boolToInt(future.Durable), foreignBody, int64(WALFingerprint(future)))
				done <- err
			}()
			fired := false
			barrier := func() {
				if fired {
					return
				}
				fired = true
				close(allow)
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("独立写者未在屏障内完成提交")
				}
			}
			if window == "after_check" {
				store.testMigrationAfterCheckHook = barrier
			} else {
				store.testCommitChunkHook = barrier
			}
			stats, err := store.ResumeMigration(ctx, migration)
			store.testMigrationAfterCheckHook = nil
			store.testCommitChunkHook = nil
			if !fired {
				t.Fatal("屏障没有覆盖实际竞态窗口")
			}
			if err == nil {
				t.Errorf("独立写入后未拒绝，stats=%+v，错误地验全结果=%v", stats, store.FinalizeMigration(ctx, migration))
			}
			var body string
			if readErr := external.db.QueryRowContext(ctx, "SELECT body FROM source_wal WHERE key=? AND seq=? AND event_id=? AND record_start=? AND record_end=?", future.Key, toInt64(future.Seq), future.EventID, toInt64(future.RecordStart), toInt64(future.RecordEnd)).Scan(&body); readErr != nil {
				t.Fatal(readErr)
			}
			if body != foreignBody {
				t.Errorf("外部行被迁移UPSERT覆盖: want=%q got=%q", foreignBody, body)
			}
			conn, readErr := store.db.Conn(ctx)
			if readErr != nil {
				t.Fatal(readErr)
			}
			record, _, readErr := readMigrationRecord(ctx, conn)
			_ = conn.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			wantCommitted := 2
			if window == "between_chunks" {
				wantCommitted = 4
			}
			if record.Committed != wantCommitted || record.Complete {
				t.Errorf("凭据推进越过拒绝单元: %+v", record)
			}
		})
	}
}

func TestMigrationFinalizeRejectsCommitAfterReadSnapshot(t *testing.T) {
	store := newTestStore(t)
	migration, err := store.BeginMigration(context.Background(), migrationTestSHA, 1, fixtureState(1))
	if err != nil {
		t.Fatal(err)
	}
	if stats, err := store.ResumeMigration(context.Background(), migration); err != nil || stats.Incomplete {
		t.Fatalf("迁移失败: %+v %v", stats, err)
	}
	external, err := Open(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	// 一致只读快照已被凭据查询固定；独立写者仍可提交，但不得把陈旧验真变成完成证明。
	store.testMigrationValidateHook = func() {
		if _, err := external.db.Exec("UPDATE source_wal SET body='验真期间篡改'"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.FinalizeMigration(context.Background(), migration); err == nil {
		t.Fatal("验真到标完成之间的独立提交必须拒绝")
	}
	store.testMigrationValidateHook = nil
	conn, err := store.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := readMigrationRecord(context.Background(), conn)
	_ = conn.Close()
	if err != nil || record.Complete {
		t.Fatalf("不得标记已完成: %+v %v", record, err)
	}
}

func TestMigrationDefaultCommitBudgetRemainsBounded(t *testing.T) {
	store := newTestStore(t)
	migration, err := store.BeginMigration(context.Background(), migrationTestSHA, 1, chunkFixtureState(2, 1024))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		stats, err := store.ResumeMigration(context.Background(), migration)
		if err != nil {
			t.Fatal(err)
		}
		if stats.MaxChunkRows > DefaultCommitMaxRows || stats.RowsWritten > DefaultCycleMaxRows {
			t.Fatalf("默认预算越界: %+v", stats)
		}
		if !stats.Incomplete {
			break
		}
	}
	budget := store.CommitBudget()
	if budget.MaxRows != 512 || budget.Target != 40*time.Millisecond {
		t.Fatalf("迁移不能放大默认单元: %+v", budget)
	}
	if err := store.FinalizeMigration(context.Background(), migration); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationFinalizeRejectsContentTamperAndPrematureFinish(t *testing.T) {
	store := newTestStoreWithBudget(t, CommitBudget{CycleMaxRows: 3})
	migration, err := store.BeginMigration(context.Background(), migrationTestSHA, 1, fixtureState(1))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.FinalizeMigration(context.Background(), migration); err == nil {
		t.Fatal("不能提前标记完成")
	}
	if _, err = store.Apply(fixtureState(1)); err == nil {
		t.Fatal("迁移期间不能夹杂普通持久化")
	}
	for i := 0; i < 10; i++ {
		stats, err := store.ResumeMigration(context.Background(), migration)
		if err != nil {
			t.Fatal(err)
		}
		if !stats.Incomplete {
			break
		}
	}
	if _, err = store.db.Exec("UPDATE source_wal SET body='内容改变但保留fp'"); err != nil {
		t.Fatal(err)
	}
	before := mustLoad(t, store)
	if err = store.FinalizeMigration(context.Background(), migration); err == nil {
		t.Fatal("最终检查必须逐值检查正文而非只信fp")
	}
	assertSameState(t, before, mustLoad(t, store))
}
