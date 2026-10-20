package stateindex

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// TestOpenMigratesLegacyInstanceBindingColumns 是 FR-502 的增量列迁移回归。
//
// 背景：`instance_binding` 新增 rule_key / relative_path 两列，而
// `CREATE TABLE IF NOT EXISTS` **不会为既有库补列**——必须靠 Open 内的
// addColumnIfMissing 幂等补齐。若漏掉，生产升级后写入会报
// 「table instance_binding has N columns but M values were supplied」。
//
// 本用例自建「旧版 5 列表 + 若干行」夹具（不依赖任何外部环境变量），
// 断言 Open 之后：① 两列已补出；② 既有行零丢失；③ 旧行新列为空
// （空规则名 ⇒ 回退 bukkit 规则，与历史行为逐字一致）。
func TestOpenMigratesLegacyInstanceBindingColumns(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")

	// 造出旧版库：只建 5 列的 instance_binding，并插入两行历史绑定。
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("打开旧库失败: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `CREATE TABLE instance_binding (
		uuid TEXT PRIMARY KEY, namespace TEXT NOT NULL, generation TEXT NOT NULL,
		mode TEXT NOT NULL, work_dir TEXT NOT NULL)`); err != nil {
		t.Fatalf("建旧表失败: %v", err)
	}
	if _, err := legacy.ExecContext(ctx,
		`INSERT INTO instance_binding (uuid, namespace, generation, mode, work_dir) VALUES
		 ('u-1','inst:1','g1','FILE_PRIMARY','/srv/a'),
		 ('u-2','inst:2','g2','STDIO_PRIMARY','/srv/b')`); err != nil {
		t.Fatalf("插入历史行失败: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("关闭旧库失败: %v", err)
	}

	// Open 应触发补列迁移。
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open（含迁移）失败: %v", err)
	}
	defer st.Close()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("打开校验连接失败: %v", err)
	}
	defer db.Close()

	// ① 两列已补出。
	cols := map[string]bool{}
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(instance_binding)")
	if err != nil {
		t.Fatalf("读列信息失败: %v", err)
	}
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("扫描列失败: %v", err)
		}
		cols[name] = true
	}
	rows.Close()
	for _, want := range []string{"rule_key", "relative_path"} {
		if !cols[want] {
			t.Errorf("迁移后缺列 %q；实际列: %v", want, cols)
		}
	}

	// ② 既有行零丢失。
	var total int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM instance_binding").Scan(&total); err != nil {
		t.Fatalf("计数失败: %v", err)
	}
	if total != 2 {
		t.Errorf("既有绑定行数 = %d，期望 2（迁移不得丢行）", total)
	}

	// ③ 旧行新列为空 ⇒ 回退 bukkit 规则（与历史行为逐字一致）。
	var fallback int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM instance_binding WHERE rule_key='' AND relative_path=''").Scan(&fallback); err != nil {
		t.Fatalf("查空值失败: %v", err)
	}
	if fallback != 2 {
		t.Errorf("旧行新列为空的数量 = %d，期望 2（空规则名 ⇒ 回退 bukkit）", fallback)
	}
}
