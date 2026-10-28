package stateindex

// 旧库兼容回归（2026-10-29）：`source_wal` 表废弃后，**已有生产库**里仍然躺着这张表和
// 最多 20 万行旧条目（生产实测 263MB body + 41MB 主键索引 = 索引库 88%）。
//
// 要求：打开这种库时——
//  1. 不报错（不能因为表/索引已不存在而失败）；
//  2. 表连同它上面的索引一起被删掉（幂等迁移）；
//  3. 其余表的数据完好无损（不能因迁移而丢数据）。
//
// 转红方式（变异验证）：把 Open 里的 `dropLegacySourceWAL` 调用去掉（或让它返回 nil 不执行
// DROP）⇒ 断言②「表必须已不存在」立即红。已实测。

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/glebarez/go-sqlite"
	"github.com/stretchr/testify/require"
)

// legacySourceWALDDL 是旧版本的 source_wal 定义（逐字取自被删除的 extensionDDL 片段），
// 连旧版本遗留的 idx_source_wal_key 也一并建出来。
const legacySourceWALDDL = `
CREATE TABLE IF NOT EXISTS source_wal (
  key          TEXT NOT NULL REFERENCES source(key),
  seq          INTEGER NOT NULL,
  event_id     TEXT NOT NULL,
  record_start INTEGER NOT NULL,
  record_end   INTEGER NOT NULL,
  appended     INTEGER NOT NULL DEFAULT 0,
  durable      INTEGER NOT NULL DEFAULT 0,
  replay       INTEGER NOT NULL DEFAULT 0,
  body         TEXT,
  fp           INTEGER NOT NULL,
  ack_body_digest BLOB,
  PRIMARY KEY (key, seq, event_id, record_start, record_end)
);
CREATE INDEX IF NOT EXISTS idx_source_wal_key ON source_wal(key);
`

// seedLegacyIndexDB 造一个「旧版本建的」索引库：完整的规范表 + 一张带数据的 source_wal。
func seedLegacyIndexDB(t *testing.T, path string, walRows int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	for _, ddl := range []string{specDDL, extensionDDL, recoveryAuditDDL, materializationDDL, legacySourceWALDDL} {
		_, err = db.ExecContext(ctx, ddl)
		require.NoError(t, err)
	}

	// 规范表里放真实数据：迁移后必须一字不少。
	const key = "node:legacy/g1"
	require.NoError(t, execAll(ctx, db,
		`INSERT INTO source (key, log_source_id, source_generation, storage_namespace, updated_at)
		 VALUES (?, 'node:legacy', 'g1', 'ns:legacy', 1)`, key))
	require.NoError(t, execAll(ctx, db,
		`INSERT INTO position (key, read_pos, durable_pos, reclaim_pos, acquire_paused, pause_reason)
		 VALUES (?, 300, 200, 100, 0, NULL)`, key))
	require.NoError(t, execAll(ctx, db,
		`INSERT INTO projection (key, generation, events_stored_through, pending, updated_at)
		 VALUES (?, 'projection-7', 250, 0, 1)`, key))
	require.NoError(t, execAll(ctx, db,
		`INSERT INTO source_aux (key, config, payload, fp) VALUES (?, '{"log_source_id":"node:legacy"}', '{}', 42)`, key))
	require.NoError(t, execAll(ctx, db,
		`INSERT INTO delivery_batch (key, ordinal, start_pos, end_pos, state)
		 VALUES (?, 0, 1, 100, 'REQUEST_DONE')`, key))
	require.NoError(t, execAll(ctx, db,
		`INSERT INTO gap (key, id, start_pos, end_pos, reason, detail, resolved)
		 VALUES (?, 0, 10, 20, 'PAUSED', 'legacy gap', 0)`, key))
	require.NoError(t, execAll(ctx, db,
		`INSERT INTO instance_binding (uuid, namespace, generation, mode, work_dir) VALUES ('u-1', 'ns:legacy', 'g1', 'file_primary', '/srv/x')`))

	// source_wal：带正文的旧条目（这正是那张 263MB 表的形状）。
	tx, err := db.Begin()
	require.NoError(t, err)
	stmt, err := tx.Prepare(`INSERT INTO source_wal
		(key, seq, event_id, record_start, record_end, appended, durable, replay, body, fp, ack_body_digest)
		VALUES (?,?,?,?,?,1,1,0,?,?,NULL)`)
	require.NoError(t, err)
	for i := 0; i < walRows; i++ {
		_, err = stmt.Exec(key, uint64(i+1), fmt.Sprintf("legacy-ev-%d", i),
			uint64(i*10), uint64(i*10+9), fmt.Sprintf(`{"event_id":"legacy-ev-%d"}`, i), uint64(i))
		require.NoError(t, err)
	}
	require.NoError(t, stmt.Close())
	require.NoError(t, tx.Commit())
}

func execAll(ctx context.Context, db *sql.DB, query string, args ...any) error {
	_, err := db.ExecContext(ctx, query, args...)
	return err
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRowContext(context.Background(),
		"SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)", name).Scan(&exists))
	return exists
}

func countRows(t *testing.T, db *sql.DB, table string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&n))
	return n
}

// TestOpenDropsLegacySourceWALTable 是本次改造的**旧库兼容**主回归：
// 带 500 行 source_wal 的旧库打开后：不报错、表被删掉、其余表数据一字不少。
func TestOpenDropsLegacySourceWALTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "var", "log", "ingest.index.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	seedLegacyIndexDB(t, path, 500)

	// 前置（自证夹具有效）：旧库里确实有那张表与 500 行。
	probe, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	require.True(t, tableExists(t, probe, "source_wal"), "前置：旧库必须含 source_wal 表")
	require.Equal(t, int64(500), countRows(t, probe, "source_wal"), "前置：旧库里必须有旧条目")
	require.Equal(t, int64(1), countRows(t, probe, "source"), "前置：须同时存在规范表数据")
	require.NoError(t, probe.Close())

	// ① 打开不报错（迁移就在 Open 里）。
	store, err := Open(path)
	require.NoError(t, err, "打开含遗留 source_wal 的旧库不得报错")
	t.Cleanup(func() { _ = store.Close() })

	// ② 表（连同旧索引）已被删掉。
	require.False(t, tableExists(t, store.db, "source_wal"), "source_wal 表必须已被幂等迁移删掉")
	var legacyIndex int
	require.NoError(t, store.db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_source_wal_key'").Scan(&legacyIndex))
	require.Zero(t, legacyIndex, "旧版本遗留的 idx_source_wal_key 必须随表一起消失")

	// ③ 其余表数据完好（逐表确认，含“行数 + 关键列值”两道口径）。
	var key, generation string
	var readPos, durablePos, reclaimPos int64
	require.NoError(t, store.db.QueryRowContext(context.Background(),
		"SELECT key, read_pos, durable_pos, reclaim_pos FROM position").Scan(&key, &readPos, &durablePos, &reclaimPos))
	require.Equal(t, "node:legacy/g1", key)
	require.Equal(t, []int64{300, 200, 100}, []int64{readPos, durablePos, reclaimPos}, "position 水位必须完好")
	require.NoError(t, store.db.QueryRowContext(context.Background(),
		"SELECT generation FROM projection WHERE key = ?", key).Scan(&generation))
	require.Equal(t, "projection-7", generation, "projection 行必须完好")
	require.Equal(t, int64(1), countRows(t, store.db, "source"))
	require.Equal(t, int64(1), countRows(t, store.db, "source_aux"))
	require.Equal(t, int64(1), countRows(t, store.db, "delivery_batch"))
	require.Equal(t, int64(1), countRows(t, store.db, "gap"))
	require.Equal(t, int64(1), countRows(t, store.db, "instance_binding"))

	// ④ 打开后可以正常读写（新 schema 与老数据同一份额）。
	loaded, err := store.Load()
	require.NoError(t, err)
	require.Len(t, loaded.Sources, 1)
	require.Len(t, loaded.Positions, 1)
	require.Len(t, loaded.Gaps, 1)
	require.Len(t, loaded.Batches, 1)
	require.NoError(t, store.IntegrityCheck())

	// ⑤ 幂等：再开一次不报错、也不因为“已经没有那张表”而失败。
	require.NoError(t, store.Close())
	reopened, err := Open(path)
	require.NoError(t, err, "再次打开必须幂等成功")
	defer func() { _ = reopened.Close() }()
	require.False(t, tableExists(t, reopened.db, "source_wal"))
	require.Equal(t, int64(1), countRows(t, reopened.db, "source"))
}

// 新建一个干净库时不得再建出 source_wal（DDL 已删）。变异：把 CREATE TABLE source_wal
// 加回 extensionDDL ⇒ 本用例红。
func TestOpenDoesNotCreateSourceWALTable(t *testing.T) {
	store := newTestStore(t)
	require.False(t, tableExists(t, store.db, "source_wal"),
		"新库不得再有 source_wal 表（DDL 已删，2026-10-29）")
	// 表的数量必须与枚举真源一致（tableCount 是 specs/loadColumns 的数组长度）。
	var tables int
	require.NoError(t, store.db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN "+
			"('source','position','gap','projection','instance_binding','source_aux','delivery_batch')").Scan(&tables))
	require.Equal(t, tableCount, tables, "规范表 + 扩展表的数量必须等于 tableCount")
}

// 旧库删除后 VACUUM 能把空闲页归还给文件系统（磁盘不会一直白占 263MB）。
// 这里只断言“显式入口可调用且库仍可读”——VACUUM 本身在不同平台上的字节数不稳定，
// 不用它做断言；真正的释放时机由运维/维护命令决定（不在启动路径上）。
func TestVacuumReclaimsFreelistAfterLegacyDrop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "var", "log", "ingest.index.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	seedLegacyIndexDB(t, path, 2000)

	store, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.False(t, tableExists(t, store.db, "source_wal"))

	freelist, err := store.FreelistBytes(context.Background())
	require.NoError(t, err)
	require.Positive(t, freelist, "DROP TABLE 后应有空闲页（否则说明迁移没真的释放页）")

	require.NoError(t, store.Vacuum(context.Background()))
	after, err := store.FreelistBytes(context.Background())
	require.NoError(t, err)
	require.Zero(t, after, "VACUUM 后 freelist 必须归零")
	require.NoError(t, store.IntegrityCheck())
	require.Equal(t, int64(1), countRows(t, store.db, "source"), "VACUUM 不得改数据")
}
