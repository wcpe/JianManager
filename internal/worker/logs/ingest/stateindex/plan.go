package stateindex

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// planState 把索引的期望状态拆成各表的行计划，并按「归属键」分组。
//
// 归属键 = 表主键的第一列（缺口/WAL/批次/游标等都以日志源为归属，实例绑定表没有归属，用 ""）。
// 分组是「只写变更行」从 O(总量) 降到 O(变更源) 的前提：持久化只对本批次变更的源做行规划，
// 未变更源的行根本不会被构造、不会被指纹、不会与镜像比对。
//
// 只有「内容列」参与指纹：updated_at 之类的写入时刻列不参与，否则每次持久化都会让所有行
// 「看起来变了」，把增量写入退化成整本重写。
func planState(st State, nowUnixMilli int64) ([tableCount]map[string][]rowData, error) {
	var plans [tableCount]map[string][]rowData
	appendRow := func(table int, owner string, row rowData) {
		if plans[table] == nil {
			plans[table] = make(map[string][]rowData)
		}
		row.owner = owner
		plans[table][owner] = append(plans[table][owner], row)
	}
	for _, row := range st.Sources {
		row := row
		appendRow(tblSource, row.Key, rowData{
			key: mirrorKey(row.Key),
			fp:  fingerprint(row.LogSourceID, row.SourceGeneration, row.StorageNamespace),
			values: func() ([]any, error) {
				updated := row.UpdatedAtUnixMilli
				if updated == 0 {
					updated = nowUnixMilli
				}
				return []any{row.Key, row.LogSourceID, row.SourceGeneration, row.StorageNamespace, updated}, nil
			},
		})
	}
	for _, row := range st.Positions {
		row := row
		appendRow(tblPosition, row.Key, rowData{
			key: mirrorKey(row.Key),
			fp: fingerprint(row.ReadPos, row.DurablePos, row.ReclaimPos, row.AcquirePaused,
				nullableText(row.PauseReason)),
			values: func() ([]any, error) {
				return []any{row.Key, toInt64(row.ReadPos), toInt64(row.DurablePos), toInt64(row.ReclaimPos),
					boolToInt(row.AcquirePaused), nullableText(row.PauseReason)}, nil
			},
		})
	}
	for _, row := range st.Gaps {
		row := row
		appendRow(tblGap, row.Key, rowData{
			key: mirrorKey(row.Key, row.ID),
			fp: fingerprint(row.StartPos, row.EndPos, row.Reason, nullableText(row.Detail), row.Resolved,
				nullableText(row.Resolution)),
			values: func() ([]any, error) {
				return []any{row.Key, row.ID, toInt64(row.StartPos), toInt64(row.EndPos), row.Reason,
					nullableText(row.Detail), boolToInt(row.Resolved), nullableText(row.Resolution)}, nil
			},
		})
	}
	for _, row := range st.Projections {
		row := row
		appendRow(tblProjection, row.Key, rowData{
			key: mirrorKey(row.Key),
			fp:  fingerprint(row.Generation, row.EventsStoredThrough, row.Pending),
			values: func() ([]any, error) {
				updated := row.UpdatedAtUnixMilli
				if updated == 0 {
					updated = nowUnixMilli
				}
				return []any{row.Key, row.Generation, toInt64(row.EventsStoredThrough), boolToInt(row.Pending), updated}, nil
			},
		})
	}
	for _, row := range st.Instances {
		row := row
		// 归属取主键首列（uuid），与镜像载入侧的 mirrorOwner 同口径——两侧不一致会把同一行
		// 归到不同组，重启后该表出现「旧的被删、新的被写」（实测 instance_binding 一写一删）。
		// 该表不参与按归属增量（不在 scopedTables），归属值本身不承载语义，只要求两侧一致。
		appendRow(tblInstanceBinding, row.UUID, rowData{
			key: mirrorKey(row.UUID),
			fp:  fingerprint(row.Namespace, row.Generation, row.Mode, row.WorkDir),
			values: func() ([]any, error) {
				return []any{row.UUID, row.Namespace, row.Generation, row.Mode, row.WorkDir}, nil
			},
		})
	}
	for _, row := range st.Aux {
		row := row
		fp := fingerprint(row.Config, row.Payload)
		appendRow(tblSourceAux, row.Key, rowData{
			key: mirrorKey(row.Key),
			fp:  fp,
			values: func() ([]any, error) {
				return []any{row.Key, nullableBytes(row.Config), nullableBytes(row.Payload), int64(fp)}, nil
			},
		})
	}
	for _, row := range st.WAL {
		row := row
		fp := WALFingerprint(row)
		appendRow(tblSourceWAL, row.Key, rowData{
			key: mirrorKey(row.Key, row.Seq, row.EventID, row.RecordStart, row.RecordEnd),
			fp:  fp,
			values: func() ([]any, error) {
				return []any{row.Key, toInt64(row.Seq), row.EventID, toInt64(row.RecordStart),
					toInt64(row.RecordEnd), boolToInt(row.Appended), boolToInt(row.Durable),
					nullableBytes(row.Body), int64(fp)}, nil
			},
		})
	}
	for _, row := range st.Batches {
		row := row
		appendRow(tblDeliveryBatch, row.Key, rowData{
			key: mirrorKey(row.Key, row.Ordinal),
			fp:  fingerprint(row.StartPos, row.EndPos, row.State),
			values: func() ([]any, error) {
				return []any{row.Key, row.Ordinal, toInt64(row.StartPos), toInt64(row.EndPos), row.State}, nil
			},
		})
	}
	return plans, nil
}

// WALFingerprint 合成 source_wal 行的变更判据。
//
// 覆盖范围 = 该行除正文之外的全部列（序号、事件身份、标志、正文是否内联）+ 调用方给出的
// 正文判据 FP。事件身份（EventID）由 log_source_id / source_generation / parser_version /
// record 范围派生，因此同一身份下正文是否变化，完全由 FP 决定——调用方必须让 FP 覆盖正文的
// 每个字段（ingest 侧的 walBodyFingerprint 覆盖 logtypes.Event 的全部 JSON 字段，并有
// TestWALBodyFingerprintCoversEveryEventField 守着这一点）。
func WALFingerprint(row WALRow) uint64 {
	return fingerprint(row.Seq, row.EventID, row.RecordStart, row.RecordEnd, row.Appended, row.Durable,
		row.Body != nil, row.FP)
}

// Migration 缓存一次迁移的确定性行计划；跨进程只信库内凭据，不依赖这个句柄保存进度。
type Migration struct {
	store       *Store
	rows        []pendingRow
	record      migrationRecord
	dataVersion int64
}

type migrationRecord struct {
	SourceSHA      string `json:"source_sha"`
	MappingVersion int    `json:"mapping_version"`
	StateSHA       string `json:"state_sha"`
	Total          int    `json:"total"`
	Committed      int    `json:"committed"`
	Complete       bool   `json:"complete"`
}

const migrationMetaKey = "legacy_json_migration_v1"

// CompletedMigration 只读查询已验全的迁移来源身份，不证明当前业务行仍与旧 JSON 等价。
// 调用方须另行完整核验当前状态，只能豁免账本明确批准的剪枝，再补归档；未完成凭据不能走此路径。
func (s *Store) CompletedMigration(ctx context.Context, sourceSHA string, mappingVersion int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	decoded, err := hex.DecodeString(sourceSHA)
	if err != nil || len(decoded) != sha256.Size || mappingVersion < 1 {
		return false, errors.New("stateindex: 迁移来源或映射版本无效")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	record, exists, err := readMigrationRecord(ctx, conn)
	if err != nil || !exists {
		return false, err
	}
	stateSHA, err := hex.DecodeString(record.StateSHA)
	if err != nil || len(stateSHA) != sha256.Size || record.Total < 0 || record.Committed < 0 || record.Committed > record.Total || (record.Complete && record.Committed != record.Total) {
		return false, errors.New("stateindex: 迁移完成凭据损坏，保留两侧状态")
	}
	if record.SourceSHA != strings.ToLower(sourceSHA) || record.MappingVersion != mappingVersion {
		return false, errors.New("stateindex: 迁移来源或映射版本不一致，保留两侧状态")
	}
	return record.Complete, nil
}

// BeginMigration 校验来源、映射和实际已提交行后取得迁移所有权；无凭据非空库只接受完全等价的状态。
// 来源校验值应由调用方对旧 JSON 的原始字节计算 SHA256；同一映射的行顺序在重启后仍然确定。
func (s *Store) BeginMigration(ctx context.Context, sourceSHA string, mappingVersion int, desired State) (*Migration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly || s.migration != nil {
		return nil, errors.New("stateindex: 只读库或已有迁移不接受新的迁移")
	}
	decoded, err := hex.DecodeString(sourceSHA)
	if err != nil || len(decoded) != sha256.Size || mappingVersion < 1 {
		return nil, errors.New("stateindex: 迁移来源或映射版本无效")
	}
	if err := Validate(desired); err != nil {
		return nil, err
	}
	plans, err := planState(desired, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	migration := &Migration{store: s}
	for table, owners := range plans {
		for _, rows := range owners {
			for _, row := range rows {
				// 固定编码结果，防止调用方改动原始切片后让凭据与后续写入的正文脱节。
				values, err := row.values()
				if err != nil {
					return nil, err
				}
				row.values = func() ([]any, error) { return values, nil }
				migration.rows = append(migration.rows, pendingRow{table: table, row: row})
			}
		}
	}
	sort.Slice(migration.rows, func(i, j int) bool {
		a, b := migration.rows[i], migration.rows[j]
		if a.table != b.table {
			return writeOrderRank[a.table] < writeOrderRank[b.table]
		}
		return a.row.key < b.row.key
	})
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	for _, pending := range migration.rows {
		values, _ := pending.row.values()
		if err := encoder.Encode([]any{pending.table, migrationValues(pending.table, values)}); err != nil {
			return nil, err
		}
	}
	migration.record = migrationRecord{SourceSHA: strings.ToLower(sourceSHA), MappingVersion: mappingVersion, StateSHA: hex.EncodeToString(hash.Sum(nil)), Total: len(migration.rows)}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	var original migrationRecord
	var existed bool
	version, err := migrationReadSnapshot(ctx, conn, func(reader migrationQuery) error {
		stored, exists, err := readMigrationRecord(ctx, reader)
		if err != nil {
			return err
		}
		original, existed = stored, exists
		prefix := -1
		if exists {
			if !migration.sameOwner(stored) {
				return errors.New("stateindex: 迁移来源、映射或进度凭据不一致，保留两侧状态")
			}
			migration.record = stored
			prefix = stored.Committed
		}
		actual, err := migration.validateRows(ctx, reader, prefix)
		if err != nil {
			return err
		}
		if !exists {
			if actual != 0 && actual != len(migration.rows) {
				return errors.New("stateindex: 无迁移凭据的非空索引不一致，拒绝接管")
			}
			migration.record.Committed = actual
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// 全量校验不持写锁；短写事务核对 data_version 与凭据，封住读验到接管之间的竞态。
	err = s.runUnit(ctx, conn, func() error {
		if err := checkMigrationSnapshot(ctx, conn, version, original, existed); err != nil {
			return err
		}
		if !existed {
			return writeMigrationRecord(ctx, conn, migration.record)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	migration.dataVersion = version
	s.migration = migration
	s.migrationPending = !migration.record.Complete
	return migration, nil
}

// ResumeMigration 只写尚未提交的行，并把确定性前缀进度与每个提交单元放在同一事务中。
// 计划仅在 Begin 构造一次；正常续跑不扫描已提交历史，外部连接改变库时才重新验其实际内容。
func (s *Store) ResumeMigration(ctx context.Context, migration *Migration) (Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if migration == nil || migration.store != s || s.migration != migration {
		return Stats{}, errors.New("stateindex: 无效迁移句柄")
	}
	started := time.Now()
	stats := Stats{Written: map[string]int{}, Deleted: map[string]int{}, Planned: map[string]int{}}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return stats, err
	}
	var stored migrationRecord
	version, err := migrationReadSnapshot(ctx, conn, func(reader migrationQuery) error {
		current, exists, err := readMigrationRecord(ctx, reader)
		if err != nil {
			return err
		}
		if !exists || !migration.sameOwner(current) {
			return errors.New("stateindex: 迁移所有权凭据已改变")
		}
		stored = current
		var observedVersion int64
		if err := reader.QueryRowContext(ctx, "PRAGMA data_version").Scan(&observedVersion); err != nil {
			return err
		}
		if observedVersion != migration.dataVersion {
			_, err = migration.validateRows(ctx, reader, stored.Committed)
		}
		return err
	})
	_ = conn.Close()
	if err != nil {
		return stats, err
	}
	migration.record = stored
	migration.dataVersion = version
	if s.testMigrationAfterCheckHook != nil {
		s.testMigrationAfterCheckHook()
	}
	base := stored.Committed
	if base == stored.Total {
		return stats, nil
	}
	s.cycle++
	samples, progress, err := s.commitChunks(ctx, migration.rows[base:], nil, nil, s.cycle, &stats, func(conn *sql.Conn) error {
		var currentVersion int64
		if err := conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&currentVersion); err != nil {
			return err
		}
		if currentVersion != migration.dataVersion {
			return errors.New("stateindex: 迁移单元写入前发现外部提交，保留两侧状态")
		}
		return nil
	}, func(conn *sql.Conn, from, to int) error {
		current, exists, err := readMigrationRecord(ctx, conn)
		if err != nil {
			return err
		}
		if !exists || !migration.sameOwner(current) || current.Committed != base+from || current.Complete {
			return errors.New("stateindex: 迁移分块提交前凭据已改变")
		}
		current.Committed = base + to
		return writeMigrationRecord(ctx, conn, current)
	})
	if err != nil {
		return stats, err
	}
	migration.record.Committed = base + progress.writesDone
	stats.Incomplete = progress.remaining() > 0
	stats.RemainingRows = progress.remaining()
	stats.Duration = time.Since(started)
	for index := range samples {
		samples[index].ChunkIndex = index
		samples[index].Chunks = len(samples)
		s.recordSample(samples[index])
	}
	s.checkpointPassiveIfNeeded(ctx)
	return stats, nil
}

// FinalizeMigration 在一致只读快照中逐值验全，再用短写事务复核版本并标记完成；成功后才可归档旧 JSON。
func (s *Store) FinalizeMigration(ctx context.Context, migration *Migration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if migration == nil || migration.store != s || s.migration != migration {
		return errors.New("stateindex: 无效迁移句柄")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var validated migrationRecord
	version, err := migrationReadSnapshot(ctx, conn, func(reader migrationQuery) error {
		stored, exists, err := readMigrationRecord(ctx, reader)
		if err != nil {
			return err
		}
		if !exists || !migration.sameOwner(stored) || stored.Committed != stored.Total {
			return errors.New("stateindex: 迁移尚未完成或凭据已改变")
		}
		validated = stored
		_, err = migration.validateRows(ctx, reader, stored.Total)
		return err
	})
	if err != nil {
		return err
	}
	err = s.runUnit(ctx, conn, func() error {
		if err := checkMigrationSnapshot(ctx, conn, version, validated, true); err != nil {
			return err
		}
		validated.Complete = true
		return writeMigrationRecord(ctx, conn, validated)
	})
	if err == nil {
		migration.record.Complete = true
		s.migration = nil
		s.migrationPending = false
	}
	return err
}

func (m *Migration) sameOwner(record migrationRecord) bool {
	return record.SourceSHA == m.record.SourceSHA && record.MappingVersion == m.record.MappingVersion && record.StateSHA == m.record.StateSHA && record.Total == m.record.Total && record.Committed >= 0 && record.Committed <= record.Total && (!record.Complete || record.Committed == record.Total)
}

type migrationQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func migrationReadSnapshot(ctx context.Context, conn *sql.Conn, read func(migrationQuery) error) (int64, error) {
	var version int64
	if err := conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&version); err != nil {
		return 0, err
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	return version, read(tx)
}

func checkMigrationSnapshot(ctx context.Context, conn *sql.Conn, version int64, record migrationRecord, existed bool) error {
	var currentVersion int64
	if err := conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&currentVersion); err != nil {
		return err
	}
	current, exists, err := readMigrationRecord(ctx, conn)
	if err != nil {
		return err
	}
	if currentVersion != version || exists != existed || current != record {
		return errors.New("stateindex: 迁移验真后数据库已改变，保留两侧状态")
	}
	return nil
}

func readMigrationRecord(ctx context.Context, conn migrationQuery) (migrationRecord, bool, error) {
	var record migrationRecord
	var text string
	err := conn.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", migrationMetaKey).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return record, false, nil
	}
	if err != nil {
		return record, false, err
	}
	if err = json.Unmarshal([]byte(text), &record); err != nil {
		return record, true, fmt.Errorf("stateindex: 迁移凭据损坏: %w", err)
	}
	return record, true, nil
}

func writeMigrationRecord(ctx context.Context, conn *sql.Conn, record migrationRecord) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", migrationMetaKey, string(encoded))
	return err
}

// migrationValues 忽略自动更新时间及派生指纹，比较正文和全部业务列，避免相信被篡改的 fp。
func migrationValues(table int, values []any) []any {
	limit := len(loadColumns[table])
	if table == tblSource || table == tblProjection {
		limit--
	}
	return values[:limit]
}

func (m *Migration) validateRows(ctx context.Context, conn migrationQuery, committed int) (int, error) {
	if m.store.testMigrationValidateHook != nil {
		m.store.testMigrationValidateHook()
	}
	limit := committed
	if limit < 0 {
		limit = len(m.rows)
	}
	var expected [tableCount]map[string]rowData
	for _, pending := range m.rows[:limit] {
		if expected[pending.table] == nil {
			expected[pending.table] = make(map[string]rowData)
		}
		expected[pending.table][pending.row.key] = pending.row
	}
	count := 0
	for table, spec := range specs {
		rows, err := conn.QueryContext(ctx, "SELECT "+strings.Join(loadColumns[table], ",")+" FROM "+spec.name)
		if err != nil {
			return count, err
		}
		for rows.Next() {
			values, err := scanRow(rows, len(loadColumns[table]))
			if err != nil {
				_ = rows.Close()
				return count, err
			}
			key := mirrorKey(values[:len(spec.keyCols)]...)
			row, ok := expected[table][key]
			if !ok {
				_ = rows.Close()
				return count, fmt.Errorf("stateindex: %s 存在无迁移凭据的行，保留两侧状态", spec.name)
			}
			want, err := row.values()
			if err != nil {
				_ = rows.Close()
				return count, err
			}
			if !reflect.DeepEqual(migrationValues(table, values), migrationValues(table, want)) {
				_ = rows.Close()
				return count, fmt.Errorf("stateindex: %s 已提交实际行改变，保留两侧状态", spec.name)
			}
			count++
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return count, err
		}
	}
	if committed >= 0 && count != committed {
		return count, errors.New("stateindex: 迁移凭据对应的已提交行缺失，保留两侧状态")
	}
	return count, nil
}

func boolToInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

// nullableText 把空字符串转换为 NULL：让「未设置」与「空串」在库中形态一致，也使指纹对二者
// 给出同一结果（避免读回 NULL、写入 "" 造成每轮都判定为变更）。
func nullableText(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func nullableBytes(v []byte) any {
	if len(v) == 0 {
		return nil
	}
	return string(v)
}

// valuesSize 估算一行写入的字节量（文本/二进制按实际长度，其余标量按 8 字节），
// 供 Stats.BytesWritten 观测「写入成本是否随总量线性增长」。
func valuesSize(values []any) int64 {
	var total int64
	for _, value := range values {
		switch v := value.(type) {
		case nil:
		case string:
			total += int64(len(v))
		case []byte:
			total += int64(len(v))
		default:
			total += 8
		}
	}
	return total
}

// Validate 检查一份期望状态自身是否自洽（父行存在、主键不重复）。
//
// 这不是储层必须做的事，而是给上层的便宜自检：把「行键重复」「子行缺父行」这类编码错误在
// 写库之前暴露成明确错误，而不是让它变成外键报错或静默覆盖。
func Validate(st State) error {
	keys := make(map[string]struct{}, len(st.Sources))
	for _, row := range st.Sources {
		keys[row.Key] = struct{}{}
	}
	requireParent := func(table, key string) error {
		if _, ok := keys[key]; !ok {
			return fmt.Errorf("stateindex: %s 行引用了不存在的 source(%q)", table, key)
		}
		return nil
	}
	for _, row := range st.Positions {
		if err := requireParent("position", row.Key); err != nil {
			return err
		}
	}
	for _, row := range st.Gaps {
		if err := requireParent("gap", row.Key); err != nil {
			return err
		}
	}
	for _, row := range st.Projections {
		if err := requireParent("projection", row.Key); err != nil {
			return err
		}
	}
	for _, row := range st.Aux {
		if err := requireParent("source_aux", row.Key); err != nil {
			return err
		}
	}
	for _, row := range st.WAL {
		if err := requireParent("source_wal", row.Key); err != nil {
			return err
		}
	}
	for _, row := range st.Batches {
		if err := requireParent("delivery_batch", row.Key); err != nil {
			return err
		}
	}
	seenWAL := make(map[string]struct{}, len(st.WAL))
	for _, row := range st.WAL {
		key := mirrorKey(row.Key, row.Seq, row.EventID, row.RecordStart, row.RecordEnd)
		if _, ok := seenWAL[key]; ok {
			return fmt.Errorf("stateindex: source_wal 主键重复（同一源的 %d 号条目被登记两次）", row.Seq)
		}
		seenWAL[key] = struct{}{}
	}
	seenGap := make(map[string]struct{}, len(st.Gaps))
	for _, row := range st.Gaps {
		key := mirrorKey(row.Key, row.ID)
		if _, ok := seenGap[key]; ok {
			return fmt.Errorf("stateindex: gap 主键重复（%s 的 %d 号缺口被登记两次）", row.Key, row.ID)
		}
		seenGap[key] = struct{}{}
	}
	return nil
}
