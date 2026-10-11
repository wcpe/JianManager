package stateindex

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// TestWALReplayFlagSurvivesRoundTrip（ACQ-1 stateindex 半部）：
// `acquire.WALRef.Replay` 已把回放标记带进内存条目，但只有它也随**索引持久化**才会在重启后
// 仍然成立——否则「恢复期回放不计入闸」的方案①在一次重启后失效，回放字节重新计入闸视图，
// 退回「复活 → re-append → 再越闸」的自锁形态。
//
// 转红方式（实测有效变异）：把 loadColumns / columns / plan.values 三处里的 replay 去掉任一处
// ⇒ 读回 Replay 恒 false ⇒ 本用例红。
func TestWALReplayFlagSurvivesRoundTrip(t *testing.T) {
	store := newTestStore(t)
	key := "inst:9/stdout"
	body := []byte(`{"event_id":"ev-replay-1","message":"m"}`)

	// 同一条 WAL 行写两次：先是常规条目，再改成回放条目。后者必须真的更新进库
	// （WALFingerprint 含 Replay ⇒ 镜像差异驱动它重写）。
	base := WALRow{
		Key: key, Seq: 1, EventID: "ev-replay-1", RecordStart: 10, RecordEnd: 19,
		Appended: true, Durable: true, Body: body, FP: fingerprint(body),
	}
	replayed := base
	replayed.Replay = true

	for _, state := range []State{
		{Sources: []SourceRow{{Key: key, LogSourceID: "inst:9", SourceGeneration: "g1", StorageNamespace: key}}, WAL: []WALRow{base}},
		{Sources: []SourceRow{{Key: key, LogSourceID: "inst:9", SourceGeneration: "g1", StorageNamespace: key}}, WAL: []WALRow{replayed}},
	} {
		if _, err := store.Apply(state); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(loaded.WAL) != 1 {
		t.Fatalf("WAL 行数应为 1，实际 %d", len(loaded.WAL))
	}
	if !loaded.WAL[0].Replay {
		t.Fatalf("回放标记必须随索引持久化并在读回后仍然为 true（实际 false）⇒ 重启后闸视图会把回放计入")
	}
	if !loaded.WAL[0].Durable || !loaded.WAL[0].Appended {
		t.Fatalf("其它列不得被 replay 的加入影响：%+v", loaded.WAL[0])
	}

	// 重新打开（模拟重启）后仍必须是回放条目。
	path := store.Path()
	if err := store.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("重新打开失败: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	reloaded, err := reopened.Load()
	if err != nil {
		t.Fatalf("重新读取失败: %v", err)
	}
	if len(reloaded.WAL) != 1 || !reloaded.WAL[0].Replay {
		t.Fatalf("重启后回放标记必须仍在（实际 %+v）⇒ 持久化往返不成立", reloaded.WAL)
	}
}

// legacySchemaSQL 复刻**本列加入之前**的 source_wal DDL（不含 replay），用于验证旧库升级路径。
const legacySchemaSQL = `
CREATE TABLE source (
  key TEXT PRIMARY KEY,
  log_source_id TEXT NOT NULL,
  source_generation TEXT NOT NULL,
  storage_namespace TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE source_wal (
  key TEXT NOT NULL REFERENCES source(key),
  seq INTEGER NOT NULL,
  event_id TEXT NOT NULL,
  record_start INTEGER NOT NULL,
  record_end INTEGER NOT NULL,
  appended INTEGER NOT NULL DEFAULT 0,
  durable INTEGER NOT NULL DEFAULT 0,
  body TEXT,
  fp INTEGER NOT NULL,
  PRIMARY KEY (key, seq, event_id, record_start, record_end)
);
`

// TestWALReplayColumnAddedForLegacyDB（ACQ-1 兼容路径）：既有旧库没有 replay 列时，
// Open 必须幂等补列、且旧行按「非回放」读回（零值语义），不得因缺列而打不开库。
func TestWALReplayColumnAddedForLegacyDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "var", "log", "legacy.index.db")
	if err := seedLegacyDB(path); err != nil {
		t.Fatalf("构造旧库失败: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("旧库补列后仍应可打开: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	if err := reopened.IntegrityCheck(); err != nil {
		t.Fatalf("补列后 integrity_check 应通过: %v", err)
	}
	loaded, err := reopened.Load()
	if err != nil {
		t.Fatalf("旧库读取失败: %v", err)
	}
	if len(loaded.WAL) != 1 {
		t.Fatalf("旧库 WAL 行数应为 1，实际 %d", len(loaded.WAL))
	}
	if loaded.WAL[0].Replay {
		t.Fatalf("旧库旧行必须按非回放读回（零值语义），实际为回放")
	}
	if !loaded.WAL[0].Durable {
		t.Fatalf("补列不得影响其它列：%+v", loaded.WAL[0])
	}

	// 补齐之后写入回放条目：新列必须真的可用（不是只加了列却无人写）。
	replayed := loaded.WAL[0]
	replayed.Replay = true
	src := loaded.Sources
	if len(src) == 0 {
		t.Fatalf("旧库应带 source 行（用于外键），实际为空")
	}
	if _, err := reopened.Apply(State{Sources: src, WAL: []WALRow{replayed}}); err != nil {
		t.Fatalf("补列后写入回放条目失败: %v", err)
	}
	again, err := reopened.Load()
	if err != nil {
		t.Fatalf("再读失败: %v", err)
	}
	if len(again.WAL) != 1 || !again.WAL[0].Replay {
		t.Fatalf("补列后回放标记必须可写可读（实际 %+v）", again.WAL)
	}

	// 第二次 Open 必须幂等（列已存在 ⇒ 不再 ALTER）。
	path2 := reopened.Path()
	if err := reopened.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	second, err := Open(path2)
	if err != nil {
		t.Fatalf("第二次打开失败（补列必须幂等）: %v", err)
	}
	defer func() { _ = second.Close() }()
}

// seedLegacyDB 造一个「升级前」的索引库：source_wal 无 replay 列，并放入一行常规条目。
func seedLegacyDB(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(legacySchemaSQL); err != nil {
		return err
	}
	if _, err := db.Exec(`INSERT INTO source (key, log_source_id, source_generation, storage_namespace, updated_at)
		VALUES ('inst:7/stdout','inst:7','g1','inst:7/stdout',1)`); err != nil {
		return err
	}
	if _, err := db.Exec(`INSERT INTO source_wal (key, seq, event_id, record_start, record_end, appended, durable, body, fp)
		VALUES ('inst:7/stdout',1,'ev-legacy',1,9,1,1,'{"event_id":"ev-legacy"}',42)`); err != nil {
		return err
	}
	return nil
}
