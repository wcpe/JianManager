package ingest

// 迁移一致性回归（FR-496 spec §3.1）：
//   旧 ingest.state.json → SQLite 索引后逐字段一致；任一字段被篡改/遗漏即红。

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// migrationFixture 构造一份「覆盖全部字段形态」的旧状态：
//   - 两个源：一个带完整账本（缺口/分段/轮转/恢复引用/投递批次/WAL 内联+引用/投影），
//     一个只有配置没有状态；
//   - 两个实例绑定；
//   - 刻意包含 nil 与空切片混用（区分空与未设置）。
func migrationFixture() *persistedState {
	entry := ledger.Entry{
		Key:           ledger.SourceKey{LogSourceID: "node:1", SourceGeneration: "g1"},
		Identity:      logtypes.SourceIdentity{LogSourceID: "node:1", SourceGeneration: "g1", ParserVersion: "v1"},
		Positions:     logtypes.Positions{Read: 1024, Durable: 1000, Delivery: 900, Reclaim: 800},
		DeliveryState: logtypes.DeliveryRequestDone,
		DeliveryBatches: []ledger.DeliveryBatch{
			{Start: 0, End: 500, State: logtypes.DeliveryRequestDone},
			{Start: 500, End: 800, State: logtypes.DeliveryReplayRequired},
		},
		Segments: []ledger.Segment{
			{Path: "/data/logs/latest.log", Kind: ledger.SegmentLive, StartPos: 0, EndPos: 1024, IdentityBytes: 64, IdentitySHA256: "abc"},
			{Path: "/data/logs/archive.log.gz", Kind: ledger.SegmentGzip, StartPos: 0, EndPos: 900},
		},
		Rotations: []ledger.RotationLink{
			{FromPath: "/data/logs/latest.log", ToPath: "/data/logs/rotated.log", EndPosFrom: 900, Generation: "g1"},
		},
		Gaps: []ledger.Gap{
			{StartPos: 300, EndPos: 350, Reason: "ROTATION_HOLE", Detail: "previous process", Resolved: true, Resolution: "recovered"},
			{StartPos: 700, EndPos: 750, Reason: "PAUSED", Detail: "disk 95.0%"},
		},
		RecoveryRefs: []ledger.RecoveryRef{
			{SegmentID: "seg-1", Path: "/data/recovery/seg-1", State: logtypes.RecoveryReleased,
				ReleaseReason: logtypes.ReleaseProjectionBacked, ResponsibilityReceiver: "catalog", CoversFrom: 0, CoversTo: 500},
		},
		ErrorCount:    3,
		IngestSeq:     42,
		AcquirePaused: false,
	}
	ev1 := logtypes.BuildEvent(entry.Identity, logtypes.RecordRange{Start: 100, End: 200},
		"2026-09-30T00:00:00Z", "2026-09-30T00:00:01Z", "INFO", "stdout", "line one")
	ev1.Fields = map[string]string{"mod": "core"}
	ev2 := logtypes.BuildEvent(entry.Identity, logtypes.RecordRange{Start: 200, End: 300},
		"2026-09-30T00:00:01Z", "2026-09-30T00:00:02Z", "ERROR", "stdout", "line two")

	return &persistedState{
		Sources: map[string]persistedSource{
			"node:1/g1": {
				PublicationPending:   true,
				Ledger:               []ledger.Entry{entry},
				WAL:                  []acquire.WALEntry{{Seq: 7, Event: ev1, Appended: true, Durable: true}},
				WALRefs:              []acquire.WALRef{{Seq: 6, Appended: true, Durable: true, EventID: ev2.EventID, RecordStart: 200, RecordEnd: 300}},
				EventsStoredThrough:  300,
				ProjectionGeneration: "projection-1",
				EventsStored:         true,
				Events:               nil,
			},
			"node:2/g1": {
				Ledger:               nil, // 刻意：空账本 + 无 WAL，检验 nil 形态
				ProjectionGeneration: "projection-2",
			},
		},
		SourceConfigs: map[string]SourceConfig{
			"node:1/g1": {
				LogSourceID: "node:1", SourceGeneration: "g1", Path: "/data/logs/latest.log",
				Mode: pipeline.ModeFilePrimary, RotateTo: "latest.log", ArchiveGlob: "*.gz",
				SourceCategory: logtypes.SourceInstance, StorageNamespace: "ns:1", UTCDay: "2026-09-30",
			},
			"node:2/g1": {LogSourceID: "node:2", SourceGeneration: "g1", Path: "/data/logs/2.log",
				// 这些字段刻意写全：Register 会对缺省字段补默认值（既有行为），
				// 补齐后「迁移一致性」断言才只考察迁移本身。
				Mode: pipeline.ModeFilePrimary, SourceCategory: logtypes.SourceInstance,
				StorageNamespace: "ns:2", UTCDay: "2026-09-30"},
		},
		Instances: map[string]InstanceBinding{
			"uuid-1": {UUID: "uuid-1", TargetID: "ns:1", Generation: "g1", Mode: pipeline.ModeFilePrimary, WorkDir: "/srv/inst-1"},
			"uuid-2": {UUID: "uuid-2", TargetID: "ns:2", Generation: "g2", Mode: pipeline.ModeStdioPrimary, WorkDir: "/srv/inst-2"},
		},
	}
}

// writeLegacyState 把旧状态写成 var/log/ingest.state.json（与旧 Worker 的落盘路径一致）。
func writeLegacyState(t *testing.T, root string, state *persistedState) {
	t.Helper()
	path := filepath.Join(root, "var", "log", "ingest.state.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	data, err := json.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
}

func TestStateIndexMigrationPreservesLegacyStateFieldByField(t *testing.T) {
	root := t.TempDir()
	fixture := migrationFixture()
	writeLegacyState(t, root, fixture)

	// 启动 Manager：openIndex 应完成迁移+校验+归档。
	m, err := newTestManager(t, Options{Root: root, Catalog: catalog.New(catalog.NewMemJournal())})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Stop() })

	// 1) 内存状态与旧文件逐字段一致（迁移后首轮 load 就应等价）。
	require.Equal(t, fixtureNormalized(fixture), fixtureNormalized(&m.state),
		"迁移后内存状态必须与旧 JSON 逐字段一致")

	// 2) 旧文件已改名归档，原路径不存在。
	legacyPath := filepath.Join(root, "var", "log", "ingest.state.json")
	_, err = os.Stat(legacyPath)
	require.ErrorIs(t, err, os.ErrNotExist, "迁移后旧 JSON 必须改名归档")
	_, err = os.Stat(legacyPath + ".migrated")
	require.NoError(t, err, "归档文件必须存在（保留一个版本，不删除）")

	// 3) 索引库可直接（只读）查到缺口/暂停/实例绑定/源配置（spec §3.5 的 sqlite3 查询面）。
	store, err := stateindex.OpenReadOnly(filepath.Join(root, "var", "log", "ingest.index.db"))
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	rows, err := store.Load()
	require.NoError(t, err)
	require.NotEmpty(t, rows.Gaps)
	require.Len(t, rows.Instances, 2)
	require.Len(t, rows.Positions, 2, "每个源都必须有位置行")
	require.Len(t, rows.WAL, 2, "内联 WAL + 引用 WAL 各行其位")

	// 5) 派生字段也真落库了：Delivery 是 ledger.Restore 会重算的派生值（比对口径已归一化），
	//    故这里直接查一行，确认它被原样搬运而非被清零。
	var auxPayload string
	for _, row := range rows.Aux {
		if row.Key == "node:1/g1" {
			auxPayload = string(row.Payload)
		}
	}
	require.Contains(t, auxPayload, "\"delivery_pos\":900",
		"账本残余字段（含派生的 delivery_position）必须原样落库")

	// 4) 重启（重新打开）后仍然一致——验证「迁移 ≠ 一次性凑巧」。
	m2, err := New(Options{Root: root, Catalog: catalog.New(catalog.NewMemJournal())})
	require.NoError(t, err)
	require.Equal(t, fixtureNormalized(fixture), fixtureNormalized(&m2.state),
		"重启后从索引恢复的状态必须与旧 JSON 逐字段一致")
	require.NoError(t, crashClose(m2))
}

// fixtureNormalized 归一化后返回状态（迁移校验用的比较口径：含派生字段统一口径）。
func fixtureNormalized(st *persistedState) *persistedState {
	return comparisonState(st)
}

// TestStateIndexMigrationRejectsTamperedField 是「篡改字段即红」的正向回归：
// 库中某一字段被改动后，重启时若旧 JSON 仍在（模拟归档前中断或人工恢复），必须逐字段
// 比对发现不一致并**拒绝启动**，不得静默以任意一侧覆盖另一侧。
func TestStateIndexMigrationRejectsTamperedField(t *testing.T) {
	root := t.TempDir()
	fixture := migrationFixture()
	writeLegacyState(t, root, fixture)

	m, err := New(Options{Root: root, Catalog: catalog.New(catalog.NewMemJournal())})
	require.NoError(t, err)
	require.NoError(t, crashClose(m))

	// 篡改实例绑定工作目录（模拟 sqlite3 UPDATE 或部分恢复）——单字段变化。
	// 直接用 database/sql 写库（驱动由 stateindex 的空白导入注册），避免为测试放宽生产 API。
	dbPath := filepath.Join(root, "var", "log", "ingest.index.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), "UPDATE instance_binding SET work_dir = 'tampered' WHERE uuid = 'uuid-1'")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	// 把旧 JSON 放回原路径（迁移已归档过；我们模拟“归档前崩溃+人工放回”的处置现场）。
	legacyPath := filepath.Join(root, "var", "log", "ingest.state.json")
	archived, err := os.ReadFile(legacyPath + ".migrated")
	require.NoError(t, err)
	// 注意：归档内容与索引不一致——索引里的 work_dir 被改了，而 JSON 是原值。
	require.NoError(t, os.WriteFile(legacyPath, archived, 0o600))

	// 重启：必须拒绝启动采集并明确报错（不静默降级）。
	_, err = New(Options{Root: root, Catalog: catalog.New(catalog.NewMemJournal())})
	require.Error(t, err, "索引被篡改且旧 JSON 在场时必须拒绝启动")
	require.Contains(t, strings.ToLower(err.Error()), "不一致", "报错必须点明不一致（校验失败拒绝启动）")
	require.Contains(t, strings.ToLower(err.Error()), "拒绝启动", "报错必须点明拒绝启动采集")

	// 旧文件必须保留供人工处置，不得被覆盖或删除。
	data, err := os.ReadFile(legacyPath)
	require.NoError(t, err)
	require.Contains(t, string(data), "node:1", "旧状态必须原样保留")
}

// TestStateIndexMigrationRecoversInterruptedArchive 是「迁移在提交后、归档前崩溃」的恢复：
// 索引已写入且与旧 JSON 一致时，重启应补归档并正常启动（幂等，不二次迁移）。
func TestStateIndexMigrationRecoversInterruptedArchive(t *testing.T) {
	root := t.TempDir()
	fixture := migrationFixture()
	writeLegacyState(t, root, fixture)

	m, err := New(Options{Root: root, Catalog: catalog.New(catalog.NewMemJournal())})
	require.NoError(t, err)
	// 模拟崩溃点：迁移事务已提交、归档尚未发生，进程就消失了——不 Stop（Stop 会 persist），
	// 只关掉句柄。
	require.NoError(t, crashClose(m))

	// 复原现场：把归档文件改回原路径（内容与刚提交进索引的那一份完全一致）。
	legacyPath := filepath.Join(root, "var", "log", "ingest.state.json")
	archived, err := os.ReadFile(legacyPath + ".migrated")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(legacyPath, archived, 0o600))

	// 重启：内容一致 ⇒ 应补归档并正常启动，不报错。
	m2, err := New(Options{Root: root, Catalog: catalog.New(catalog.NewMemJournal())})
	require.NoError(t, err)
	require.Equal(t, fixtureNormalized(fixture), fixtureNormalized(&m2.state))
	require.NoError(t, crashClose(m2))
	_, err = os.Stat(legacyPath)
	require.ErrorIs(t, err, os.ErrNotExist, "重启后旧 JSON 应被补归档")
	// 归档链完整：原归档（.migrated）仍在，重启补归档用的是带时间戳的新名，不覆盖旧版本。
	_, err = os.Stat(legacyPath + ".migrated")
	require.NoError(t, err, "旧归档版本必须保留")
}

// crashClose 关闭索引句柄但**不做持久化**，用于模拟「进程突然消失」的现场。
func crashClose(m *Manager) error {
	if m == nil || m.index == nil {
		return nil
	}
	return m.index.Close()
}

// openIndexForTest 打开（不迁移）测试直接构造的 Manager 的索引库。
func openIndexForTest(m *Manager) (*stateindex.Store, error) {
	return stateindex.Open(m.indexPath())
}

// TestStateIndexMigrationRejectsCorruptJSON 验证旧 JSON 本身损坏时拒绝启动（不猜、不跳过）。
func TestStateIndexMigrationRejectsCorruptJSON(t *testing.T) {
	root := t.TempDir()
	legacyPath := filepath.Join(root, "var", "log", "ingest.state.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(legacyPath), 0o755))
	require.NoError(t, os.WriteFile(legacyPath, []byte("{not-json"), 0o600))
	_, err := New(Options{Root: root, Catalog: catalog.New(catalog.NewMemJournal())})
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "拒绝启动")
	_, err = os.Stat(legacyPath)
	require.NoError(t, err, "损坏的旧文件必须保留供人工处置")
}
