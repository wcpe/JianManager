package stateindex

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// 受影响行数必须来自 SQLite，不得用计划数给性能观测或真实事务预算造数。
func TestIndexSQLRowsAffectedCountersUseActualChanges(t *testing.T) {
	for _, table := range []int{tblSource, tblGap} {
		t.Run(specs[table].name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			source := fixtureState(1)
			if _, err := s.Apply(source); err != nil {
				t.Fatal(err)
			}
			var row pendingRow
			plans, err := planState(source, time.Now().UnixMilli())
			if err != nil {
				t.Fatal(err)
			}
			for _, rows := range plans[table] {
				for _, candidate := range rows {
					values, err := candidate.values()
					if err != nil {
						t.Fatal(err)
					}
					candidate.values = func() ([]any, error) { return values, nil }
					row = pendingRow{table: table, row: candidate}
					break
				}
				if row.row.key != "" {
					break
				}
			}
			if row.row.key == "" {
				t.Fatalf("%s 表未规划出任何行", specs[table].name)
			}
			conn, err := s.db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			spec := specs[table]
			if _, err := conn.ExecContext(ctx, fmt.Sprintf("CREATE TEMP TRIGGER ignore_write BEFORE INSERT ON %s BEGIN SELECT RAISE(IGNORE); END", spec.name)); err != nil {
				t.Fatal(err)
			}
			stats := Stats{Written: map[string]int{}, Deleted: map[string]int{}}
			if err := s.runUnit(ctx, conn, func() error { return s.applyTableWrites(ctx, conn, spec, []pendingRow{row}, &stats) }); err != nil {
				t.Fatal(err)
			}
			if stats.RowsWritten != 0 || stats.Written[spec.name] != 0 {
				t.Fatalf("计划写一行但SQLite实际忽略，不能报告写入：%+v", stats)
			}
			if _, err := conn.ExecContext(ctx, "DROP TRIGGER ignore_write"); err != nil {
				t.Fatal(err)
			}
			if err := s.runUnit(ctx, conn, func() error { return s.applyTableWrites(ctx, conn, spec, []pendingRow{row}, &stats) }); err != nil {
				t.Fatal(err)
			}
			if stats.RowsWritten != 1 || stats.Written[spec.name] != 1 {
				t.Fatalf("真实写入未按SQL计数：%+v", stats)
			}
			if table == tblGap {
				missing := pendingRow{table: table, row: rowData{owner: row.row.owner, key: mirrorKey(row.row.owner, int64(999))}}
				if err := s.runUnit(ctx, conn, func() error { return s.applyTableDeletes(ctx, conn, spec, []pendingRow{row, row, missing}, &stats) }); err != nil {
					t.Fatal(err)
				}
				if stats.RowsDeleted != 1 || stats.Deleted[spec.name] != 1 {
					t.Fatalf("重复/不存在主键不能按计划三行报告删除：%+v", stats)
				}
			}
		})
	}
}
