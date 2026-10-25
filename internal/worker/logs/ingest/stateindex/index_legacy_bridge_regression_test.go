package stateindex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 子进程以真实八列旧表与版本一凭据退出，不调用关闭，保留真实已提交 WAL。
func TestIndexLegacyV1FixtureHelper(t *testing.T) {
	path := os.Getenv("JM_INDEX_V1_FIXTURE")
	if path == "" {
		return
	}
	s, err := OpenWithBudget(path, CommitBudget{MaxRows: 3, MinRows: 1, CycleMaxRows: 3})
	if err != nil {
		t.Fatal(err)
	}
	migration, err := s.BeginMigration(context.Background(), migrationTestSHA, 1, fixtureState(1))
	if err != nil {
		t.Fatal(err)
	}
	for {
		stats, err := s.ResumeMigration(context.Background(), migration)
		if err != nil {
			t.Fatal(err)
		}
		if os.Getenv("JM_INDEX_V1_COMPLETE") != "1" {
			break
		}
		if !stats.Incomplete {
			if err := s.FinalizeMigration(context.Background(), migration); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	for _, column := range []string{"fold_spans", "fold_span_merges", "reason_code", "resolved_at_utc", "resolved_by", "fold_spans_unknown"} {
		if _, err := s.db.Exec("ALTER TABLE gap DROP COLUMN " + column); err != nil {
			t.Fatal(err)
		}
	}
	os.Exit(0)
}

func seedIndexLegacyV1Fixture(t *testing.T, path string, complete bool) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "-test.run=^TestIndexLegacyV1FixtureHelper$", "-test.timeout=30s")
	command.Env = append(os.Environ(), "JM_INDEX_V1_FIXTURE="+path)
	if complete {
		command.Env = append(command.Env, "JM_INDEX_V1_COMPLETE=1")
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("旧库夹具生成失败：%s %v", output, err)
	}
	info, err := os.Stat(path + "-wal")
	if err != nil || info.Size() == 0 {
		t.Fatalf("未产生真实残留WAL：%v", err)
	}
}

func upgradedIndexFixture() State {
	desired := fixtureState(1)
	desired.Gaps[0].FoldSpans = []byte(`[{"from":10,"to":12},{"from":18,"to":20}]`)
	desired.Gaps[0].FoldSpanMerges = 2
	desired.Gaps[0].ReasonCode = "PERMANENTLY_LOST"
	desired.Gaps[0].ResolvedAtUTC = "2026-10-22T12:00:00Z"
	desired.Gaps[0].ResolvedBy = "迁移测试"
	desired.Aux[0].Payload = []byte(`{"new_mapping":"字段完整"}`)
	return desired
}

func TestIndexLegacyV1BridgeCompletedAndPartialThreeOpens(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(map[bool]string{false: "旧未完成", true: "旧已完成"}[complete], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "legacy.db")
			seedIndexLegacyV1Fixture(t, path, complete)
			desired := upgradedIndexFixture()
			for round := 0; round < 3; round++ {
				s, err := OpenWithBudget(path, CommitBudget{MaxRows: 2, MinRows: 1, CycleMaxRows: 2})
				if err != nil {
					t.Fatal(err)
				}
				migration, err := s.BeginMigrationV2(context.Background(), migrationTestSHA, desired, fixtureState(1))
				if err != nil {
					t.Fatalf("第%d次重开升级失败：%v", round+1, err)
				}
				if round == 0 {
					ctx, cancel := context.WithCancel(context.Background())
					s.testCrashHook = cancel
					if _, err := s.ResumeMigration(ctx, migration); err == nil {
						t.Fatal("提交前取消必须返回失败")
					}
					s.testCrashHook = nil
				}
				for {
					stats, err := s.ResumeMigration(context.Background(), migration)
					if err != nil {
						t.Fatal(err)
					}
					if stats.RowsWritten+stats.RowsDeleted > 2 || stats.MaxChunkRows > 2 {
						t.Fatalf("旧桥补写越预算：%+v", stats)
					}
					if round < 2 {
						if !stats.Incomplete {
							t.Fatal("夹具应留有后续重开前缀")
						}
						break
					}
					if !stats.Incomplete {
						break
					}
				}
				if round == 2 {
					if err := s.FinalizeMigration(context.Background(), migration); err != nil {
						t.Fatal(err)
					}
					assertSameState(t, desired, mustLoad(t, s))
					if done, err := s.CompletedMigration(context.Background(), migrationTestSHA, 2); err != nil || !done {
						t.Fatalf("新凭据未完成：%v %v", done, err)
					}
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestIndexLegacyV1BridgeRejectsSourceAndActualTamper(t *testing.T) {
	for _, complete := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "legacy.db")
		seedIndexLegacyV1Fixture(t, path, complete)
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.BeginMigrationV2(context.Background(), strings.Repeat("b", 64), upgradedIndexFixture(), fixtureState(1)); err == nil {
			t.Fatal("旧来源改变仍续迁")
		}
		// 新增列被人篡改也不能靠忽略字段制造旧映射一致。
		if _, err := s.db.Exec("UPDATE gap SET fold_span_merges=9"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.BeginMigrationV2(context.Background(), migrationTestSHA, upgradedIndexFixture(), fixtureState(1)); err == nil {
			t.Fatal("新列篡改被旧字段比较掩盖")
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
