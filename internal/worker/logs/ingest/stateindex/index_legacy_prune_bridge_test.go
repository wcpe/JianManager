package stateindex

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func indexPruneBridgeSource() State {
	st := fixtureState(1)
	key := st.Sources[0].Key
	st.Batches = []BatchRow{
		{Key: key, Ordinal: 0, StartPos: 0, EndPos: 100, State: "REQUEST_DONE"},
		{Key: key, Ordinal: 1, StartPos: 600, EndPos: 900, State: "UNKNOWN"},
		{Key: key, Ordinal: 2, StartPos: 100, EndPos: 200, State: "UNKNOWN"},
		{Key: key, Ordinal: 3, StartPos: 200, EndPos: 300, State: "REPLAY_REQUIRED"},
		{Key: key, Ordinal: 4, StartPos: 900, EndPos: 1100, State: "NOT_SENT"},
		{Key: key, Ordinal: 5, StartPos: 300, EndPos: 400, State: "REQUEST_DONE"},
		{Key: key, Ordinal: 6, StartPos: 400, EndPos: 500, State: "UNKNOWN"},
	}
	return st
}

func indexPruneBridgePhysical(source State, retained ...int) State {
	physical := source
	physical.Batches = nil
	for ordinal, index := range retained {
		batch := source.Batches[index]
		batch.Ordinal = int64(ordinal)
		physical.Batches = append(physical.Batches, batch)
	}
	return physical
}

func indexPruneBridgeDesired(source State) State {
	desired := upgradedIndexFixture()
	desired.Batches = source.Batches
	return desired
}

func seedIndexPruneBridge(t *testing.T, source State, physical State, complete bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := s.BeginMigration(context.Background(), migrationTestSHA, 1, source)
	if err != nil {
		t.Fatal(err)
	}
	for {
		stats, err := s.ResumeMigration(context.Background(), migration)
		if err != nil {
			t.Fatal(err)
		}
		if !stats.Incomplete {
			break
		}
	}
	if err := s.FinalizeMigration(context.Background(), migration); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(physical); err != nil {
		t.Fatal(err)
	}
	if !complete {
		// 全部行已写入但未完成最终验真，仍不具备裁剪豁免资格。
		var record migrationRecord
		conn, err := s.db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		record, _, err = readMigrationRecord(context.Background(), conn)
		if err == nil {
			record.Complete = false
			err = writeMigrationRecord(context.Background(), conn, record)
		}
		_ = conn.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestIndexLegacyPrunedV1BridgeInterruptedThreeOpens(t *testing.T) {
	for _, tc := range []struct {
		name     string
		retained []int
		keeps    string
	}{
		{name: "严格水位", retained: []int{1, 4}, keeps: `{"node:000/g1":0}`},
		{name: "保留尾窗", retained: []int{1, 4, 5, 6}, keeps: `{"node:000/g1":2}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			source := indexPruneBridgeSource()
			physical := indexPruneBridgePhysical(source, tc.retained...)
			path := seedIndexPruneBridge(t, source, physical, true)
			desired := indexPruneBridgeDesired(source)
			through := map[string]uint64{source.Sources[0].Key: 500}
			for round := 1; round <= 3; round++ {
				s, err := OpenWithBudget(path, CommitBudget{MaxRows: 2, MinRows: 1, CycleMaxRows: 2})
				if err != nil {
					t.Fatal(err)
				}
				full, err := s.buildMigration(migrationTestSHA, 1, source)
				if err != nil {
					t.Fatal(err)
				}
				migration, err := s.BeginMigrationV2WithPrune(ctx, migrationTestSHA, desired, source, through)
				if err != nil {
					t.Fatalf("第%d次裁剪桥重开失败：%v", round, err)
				}
				if migration.record.LegacyStateSHA != full.record.StateSHA || migration.record.LegacyPruneKeeps != tc.keeps {
					t.Fatalf("丢失原全态或选定裁剪形态：%+v", migration.record)
				}
				if round == 1 {
					canceled, cancel := context.WithCancel(ctx)
					s.testCrashHook = cancel
					if _, err := s.ResumeMigration(canceled, migration); err == nil {
						t.Fatal("取消单元仍提交")
					}
					s.testCrashHook = nil
				}
				for {
					stats, err := s.ResumeMigration(ctx, migration)
					if err != nil {
						t.Fatal(err)
					}
					if stats.RowsWritten+stats.RowsDeleted > 2 || stats.MaxChunkRows > 2 {
						t.Fatalf("补写超预算：%+v", stats)
					}
					if round < 3 || !stats.Incomplete {
						break
					}
				}
				t.Logf("第%d次：新前缀=%d/%d，旧完整SHA=%s，裁剪形态=%s", round, migration.record.Committed, migration.record.Total, migration.record.LegacyStateSHA, migration.record.LegacyPruneKeeps)
				if round < 3 {
					if migration.record.Committed >= migration.record.Total {
						t.Fatal("必须真实留有续迁前缀")
					}
				} else {
					if err := s.FinalizeMigration(ctx, migration); err != nil {
						t.Fatal(err)
					}
					expected := desired
					expected.Batches = physical.Batches
					assertSameState(t, expected, mustLoad(t, s))
					if err := s.IntegrityCheck(); err != nil {
						t.Fatal(err)
					}
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestIndexLegacyPrunedV1BridgeRejectsNonPruneChanges(t *testing.T) {
	for _, tc := range []struct {
		name     string
		complete bool
		sql      string
		badSHA   bool
	}{
		{name: "未完成凭据", complete: false},
		{name: "来源字节改变", complete: true, badSHA: true},
		{name: "水位外删行", complete: true, sql: "DELETE FROM delivery_batch WHERE end_pos=900"},
		{name: "旧状态改写", complete: true, sql: "UPDATE delivery_batch SET state='REQUEST_DONE' WHERE end_pos=500"},
		{name: "起点改写", complete: true, sql: "UPDATE delivery_batch SET start_pos=401 WHERE end_pos=500"},
		{name: "新精确区间改写", complete: true, sql: "UPDATE gap SET fold_spans=x'5b5d'"},
		{name: "新合并次数改写", complete: true, sql: "UPDATE gap SET fold_span_merges=9"},
		{name: "新原因码改写", complete: true, sql: "UPDATE gap SET reason_code='TAMPERED'"},
		{name: "新审计时间改写", complete: true, sql: "UPDATE gap SET resolved_at_utc='2026-10-22T00:00:00Z'"},
		{name: "新操作者改写", complete: true, sql: "UPDATE gap SET resolved_by='篡改者'"},
		{name: "新未知标记改写", complete: true, sql: "UPDATE gap SET fold_spans_unknown=1"},
		{name: "附属正文改写", complete: true, sql: "UPDATE source_aux SET payload='{}'"},
		{name: "实际水位改写", complete: true, sql: "UPDATE position SET reclaim_pos=900"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := indexPruneBridgeSource()
			path := seedIndexPruneBridge(t, source, indexPruneBridgePhysical(source, 1, 4, 5, 6), tc.complete)
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			if tc.sql != "" {
				if _, err := s.db.Exec(tc.sql); err != nil {
					t.Fatal(err)
				}
			}
			sha := migrationTestSHA
			if tc.badSHA {
				sha = strings.Repeat("b", 64)
			}
			if _, err := s.BeginMigrationV2WithPrune(context.Background(), sha, indexPruneBridgeDesired(source), source, map[string]uint64{source.Sources[0].Key: 500}); err == nil {
				t.Fatal("非合法裁剪被桥接豁免")
			}
		})
	}
}

func TestIndexLegacyPrunedV1BridgeFreezesSelectedShape(t *testing.T) {
	for _, change := range []string{"实际批次再删", "批准水位改变", "凭据尾窗改变"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			source := indexPruneBridgeSource()
			path := seedIndexPruneBridge(t, source, indexPruneBridgePhysical(source, 1, 4, 5, 6), true)
			budget := CommitBudget{MaxRows: 2, MinRows: 1, CycleMaxRows: 2}
			s, err := OpenWithBudget(path, budget)
			if err != nil {
				t.Fatal(err)
			}
			through := map[string]uint64{source.Sources[0].Key: 500}
			migration, err := s.BeginMigrationV2WithPrune(ctx, migrationTestSHA, indexPruneBridgeDesired(source), source, through)
			if err != nil {
				t.Fatal(err)
			}
			stats, err := s.ResumeMigration(ctx, migration)
			if err != nil || !stats.Incomplete {
				t.Fatalf("未留下真实新前缀：%+v %v", stats, err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenWithBudget(path, budget)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			switch change {
			case "实际批次再删":
				if _, err := s.db.Exec("DELETE FROM delivery_batch WHERE end_pos=500"); err != nil {
					t.Fatal(err)
				}
			case "批准水位改变":
				through[source.Sources[0].Key] = 1000
			case "凭据尾窗改变":
				conn, err := s.db.Conn(ctx)
				if err != nil {
					t.Fatal(err)
				}
				record, _, err := readMigrationRecord(ctx, conn)
				if err == nil {
					record.LegacyPruneKeeps = `{"node:000/g1":0}`
					err = writeMigrationRecord(ctx, conn, record)
				}
				_ = conn.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.BeginMigrationV2WithPrune(ctx, migrationTestSHA, indexPruneBridgeDesired(source), source, through); err == nil {
				t.Fatal("新前缀续迁重新推断并接受改变后的裁剪形态")
			}
		})
	}
}

func TestIndexLegacyPrunedV1BridgeBindsTailAndSourceState(t *testing.T) {
	source := indexPruneBridgeSource()
	for _, retained := range [][]int{{0, 1, 4, 6}, {1, 4, 5, 6}} {
		path := seedIndexPruneBridge(t, source, indexPruneBridgePhysical(source, retained...), true)
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		desired := indexPruneBridgeDesired(source)
		if retained[0] == 0 {
			if _, err := s.BeginMigrationV2WithPrune(context.Background(), migrationTestSHA, desired, source, map[string]uint64{source.Sources[0].Key: 500}); err == nil {
				t.Fatal("任意删中间批次被当作合法尾窗")
			}
		} else {
			changed := source
			changed.Aux = append([]AuxRow(nil), source.Aux...)
			changed.Aux[0].Payload = []byte(`{"changed":"原全态不符"}`)
			if _, err := s.BeginMigrationV2WithPrune(context.Background(), migrationTestSHA, desired, changed, map[string]uint64{source.Sources[0].Key: 500}); err == nil {
				t.Fatal("原始版本一全态哈希改变仍被接受")
			}
			migration, err := s.BeginMigrationV2WithPrune(context.Background(), migrationTestSHA, desired, source, map[string]uint64{source.Sources[0].Key: 500})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.ResumeMigration(context.Background(), migration); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
