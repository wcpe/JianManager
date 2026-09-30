package stateindex

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	// 纯 Go 的嵌入式 SQLite 驱动：CGO_ENABLED=0 可直接编译（spec §2.1）。
	_ "modernc.org/sqlite"
)

// SchemaVersion 是本包写入结构当前版本，落在 meta 表供后续就地升级识别。
const SchemaVersion = "1"

// sampleRing 是持久化耗时的采样环容量（真机 P50/P95 验证用，见 spec §3.3）。
const sampleRing = 512

// Sample 是一次 Apply 的耗时采样。
type Sample struct {
	StartedAt    time.Time
	Duration     time.Duration
	RowsWritten  int
	RowsDeleted  int
	BytesWritten int64
}

// Latency 是采样环上的耗时分位（真机验收「单次持久化 ≤50ms」的读数口径）。
type Latency struct {
	Count int
	P50   time.Duration
	P95   time.Duration
	Max   time.Duration
}

// Stats 是一次 Apply 的写入统计。RowsWritten/BytesWritten 是「只写变更行」的直接观测量：
// 若退回整本重写，RowsWritten 会恒等于全量行数，从而被规模回归测试判红。
type Stats struct {
	RowsWritten  int
	RowsDeleted  int
	BytesWritten int64
	Duration     time.Duration
	// Written/Deleted 按表给出明细（表名 → 行数），便于排障与断言。
	Written map[string]int
	Deleted map[string]int
}

// Store 是索引库的句柄。单写者单连接（spec §2.1）：所有写入都经 SetMaxOpenConns(1) 的连接，
// 且每次写入都是一个 IMMEDIATE 事务——崩溃时未提交的事务整体丢弃，已提交的批次整体保留。
type Store struct {
	path     string
	db       *sql.DB
	readOnly bool

	mu sync.Mutex
	// mirror 记录每张表当前已落库行的指纹（表名 → 主键编码 → fp），
	// 使每次写入只需比较内存中的期望行，而不必回读库、更不必重写未变更行。
	mirror map[string]map[string]uint64

	samples []Sample

	// testCrashHook 仅供测试注入：在一次写入事务**提交之前**、写完全部变更行之后被调用，
	// 用于在子进程中模拟进程异常中断（os.Exit 或 SIGKILL），验证未提交批次不会残留。
	// 生产恒为 nil。
	testCrashHook func()
}

// Open 打开（必要时创建）索引库：建表、设置 WAL/单写者、并把现有行的指纹读入镜像。
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("stateindex: 库路径不能为空")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("stateindex: 创建目录失败: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("stateindex: 打开索引库失败: %w", err)
	}
	// 单写者单连接：连接池上限 1，避免同进程内出现并发写事务。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{path: path, db: db, mirror: make(map[string]map[string]uint64)}
	ctx := context.Background()
	// journal_mode=WAL + synchronous=NORMAL（spec §2.1）：提交即持久、崩溃可恢复；
	// busy_timeout 用于同进程内只读连接短暂持锁时等待，而非立即报错。
	if err := execPragma(ctx, db, "PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, err
	}
	for _, pragma := range []string{
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("stateindex: 设置 %s 失败: %w", pragma, err)
		}
	}
	for _, ddl := range []string{specDDL, extensionDDL} {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("stateindex: 建表失败: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO meta (key, value) VALUES ('schema_version', ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
		SchemaVersion); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("stateindex: 写入 schema 版本失败: %w", err)
	}
	if err := s.loadMirror(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// execPragma 执行会返回一行的 PRAGMA（如 journal_mode），丢弃返回值。
func execPragma(ctx context.Context, db *sql.DB, pragma string) error {
	var value string
	if err := db.QueryRowContext(ctx, pragma).Scan(&value); err != nil {
		return fmt.Errorf("stateindex: 设置 %s 失败: %w", pragma, err)
	}
	return nil
}

// OpenReadOnly 以只读方式打开已存在的索引库（导出 JSON、排障时的程序化查询）。
//
// 只读连接同样能读到 WAL 库的已提交内容；库不存在或不可读时返回错误，不做任何创建。
func OpenReadOnly(path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("stateindex: 索引库不可读: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("stateindex: 只读打开索引库失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{path: path, db: db, readOnly: true, mirror: make(map[string]map[string]uint64)}
	if _, err := db.ExecContext(context.Background(), "PRAGMA query_only=ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("stateindex: 设置只读模式失败: %w", err)
	}
	return s, nil
}

// Path 返回库文件路径。
func (s *Store) Path() string { return s.path }

// Close 关闭库；WAL 在最后一个连接关闭时自动归并。
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// IsEmpty 报告索引是否不含任何数据行。迁移判据之一：空库 + 存在旧 JSON ⇒ 需要迁移。
func (s *Store) IsEmpty() (bool, error) {
	counts, err := s.Counts()
	if err != nil {
		return false, err
	}
	return counts.Empty(), nil
}

// Counts 返回各表行数。
func (s *Store) Counts() (Counts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts(context.Background())
}

func (s *Store) counts(ctx context.Context) (Counts, error) {
	var counts Counts
	targets := []struct {
		table string
		out   *int64
	}{
		{"source", &counts.Sources},
		{"position", &counts.Positions},
		{"gap", &counts.Gaps},
		{"projection", &counts.Projections},
		{"instance_binding", &counts.Instances},
		{"source_aux", &counts.Aux},
		{"source_wal", &counts.WAL},
		{"delivery_batch", &counts.Batches},
	}
	for _, target := range targets {
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+target.table).Scan(target.out); err != nil {
			return Counts{}, fmt.Errorf("stateindex: 统计 %s 行数失败: %w", target.table, err)
		}
	}
	return counts, nil
}

// IntegrityCheck 执行 PRAGMA integrity_check；返回错误表示库已损坏。
func (s *Store) IntegrityCheck() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result string
	if err := s.db.QueryRowContext(context.Background(), "PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("stateindex: integrity_check 执行失败: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(result), "ok") {
		return fmt.Errorf("stateindex: integrity_check 未通过: %s", result)
	}
	return nil
}

// loadMirror 把各表现有行的指纹读入内存镜像。
//
// 带 fp 列的表（source_aux/source_wal）直接读 fp，因此**不需要读 WAL 正文**——单源万条积压时，
// 启动不会为正文多做一次编解码。
func (s *Store) loadMirror(ctx context.Context) error {
	for index := range specs {
		spec := specs[index]
		rows, err := s.db.QueryContext(ctx, spec.mirrorSQL)
		if err != nil {
			return fmt.Errorf("stateindex: 读取 %s 镜像失败: %w", spec.name, err)
		}
		table := make(map[string]uint64)
		for rows.Next() {
			columns, err := rows.Columns()
			if err != nil {
				_ = rows.Close()
				return fmt.Errorf("stateindex: %s 镜像列数不可读: %w", spec.name, err)
			}
			values, err := scanRow(rows, len(columns))
			if err != nil {
				_ = rows.Close()
				return fmt.Errorf("stateindex: 扫描 %s 镜像失败: %w", spec.name, err)
			}
			if spec.fpSelf {
				// 带 fp 列的表（source_aux/source_wal）直接读最后一列。
				if values[len(values)-1] == nil {
					_ = rows.Close()
					return fmt.Errorf("stateindex: %s 指纹列为空", spec.name)
				}
				table[mirrorKey(values[:len(spec.keyCols)]...)] = mustFingerprint(values[len(values)-1])
			} else {
				// 无 fp 列的表（spec 五表）由列值现算，与写入侧同一函数、同一类型归一化。
				table[mirrorKey(values[:len(spec.keyCols)]...)] = fingerprint(values[len(spec.keyCols):]...)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("stateindex: 遍历 %s 镜像失败: %w", spec.name, err)
		}
		_ = rows.Close()
		s.mirror[spec.name] = table
	}
	return nil
}

// mustFingerprint 读取镜像中的 fp 列值（写入侧按 int64 落库，读回即 int64）。
func mustFingerprint(value any) uint64 {
	parsed, _ := value.(int64)
	return uint64(parsed)
}

// Load 读出索引全部内容（行序见各表 orderBy）。
func (s *Store) Load() (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load(context.Background())
}

func (s *Store) load(ctx context.Context) (State, error) {
	var st State
	for index := range specs {
		spec := specs[index]
		columns := strings.Join(loadColumns[index], ", ")
		rows, err := s.db.QueryContext(ctx, "SELECT "+columns+" FROM "+spec.name+" ORDER BY "+spec.orderBy)
		if err != nil {
			return State{}, fmt.Errorf("stateindex: 读取 %s 失败: %w", spec.name, err)
		}
		for rows.Next() {
			values, err := scanRow(rows, len(loadColumns[index]))
			if err != nil {
				_ = rows.Close()
				return State{}, fmt.Errorf("stateindex: 扫描 %s 失败: %w", spec.name, err)
			}
			if err := appendRow(&st, spec, values); err != nil {
				_ = rows.Close()
				return State{}, err
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return State{}, fmt.Errorf("stateindex: 遍历 %s 失败: %w", spec.name, err)
		}
		_ = rows.Close()
	}
	sortState(&st)
	return st, nil
}

// Apply 把索引的期望状态增量写入库：只写「指纹变化」的行，删除不再存在的行，全部在一个事务内
// 提交（提交即持久；未提交部分在崩溃时整体丢弃——索引回退幅度不超过一个批次）。
func (s *Store) Apply(desired State) (Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return Stats{}, errors.New("stateindex: 只读库不接受写入")
	}
	started := time.Now()
	stats := Stats{Written: map[string]int{}, Deleted: map[string]int{}}
	// finish 统一记录耗时与采样后返回（避免用 defer 改返回值副本的陷阱）。
	finish := func() Stats {
		stats.Duration = time.Since(started)
		s.recordSample(started, stats)
		return stats
	}
	ctx := context.Background()
	if err := Validate(desired); err != nil {
		return Stats{}, err
	}
	plans, err := planState(desired, started.UnixMilli())
	if err != nil {
		return Stats{}, err
	}

	// 先算差异（不触库），再开事务，尽量缩短持锁时间。
	type pendingRow struct {
		table int
		row   rowData
	}
	var writes []pendingRow
	var deletes []pendingRow
	for index := range specs {
		spec := specs[index]
		mirror := s.mirror[spec.name]
		if mirror == nil {
			mirror = make(map[string]uint64)
			s.mirror[spec.name] = mirror
		}
		seen := make(map[string]struct{}, len(plans[index]))
		for _, row := range plans[index] {
			seen[row.key] = struct{}{}
			if previous, ok := mirror[row.key]; ok && previous == row.fp {
				continue // 内容未变：不生成任何写语句——这就是「只写变更行」的判据点
			}
			writes = append(writes, pendingRow{table: index, row: row})
		}
		for key := range mirror {
			if _, ok := seen[key]; ok {
				continue
			}
			deletes = append(deletes, pendingRow{table: index, row: rowData{key: key}})
		}
	}
	if len(writes) == 0 && len(deletes) == 0 {
		// 幂等空转：不开事务、不写任何字节（空闲轮询不得重写历史）。
		return finish(), nil
	}
	// 删除按表倒序（子表在前），满足外键约束；写入按表正序（父表在前）。
	sort.SliceStable(deletes, func(i, j int) bool { return deletes[i].table > deletes[j].table })

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("stateindex: 获取写连接失败: %w", err)
	}
	defer func() { _ = conn.Close() }()
	// BEGIN IMMEDIATE：一开始就取写锁，避免 WAL 下锁升级失败造成 SQLITE_BUSY。
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return Stats{}, fmt.Errorf("stateindex: 开启事务失败: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()

	for _, pending := range writes {
		spec := specs[pending.table]
		values, err := pending.row.values()
		if err != nil {
			return Stats{}, err
		}
		if _, err := conn.ExecContext(ctx, spec.upsert, values...); err != nil {
			return Stats{}, fmt.Errorf("stateindex: 写入 %s 失败: %w", spec.name, err)
		}
		stats.RowsWritten++
		stats.Written[spec.name]++
		stats.BytesWritten += valuesSize(values)
	}
	for _, pending := range deletes {
		spec := specs[pending.table]
		keyValues, err := decodeMirrorKey(spec, pending.row.key)
		if err != nil {
			return Stats{}, err
		}
		if _, err := conn.ExecContext(ctx, spec.deleteStmt, keyValues...); err != nil {
			return Stats{}, fmt.Errorf("stateindex: 删除 %s 旧行失败: %w", spec.name, err)
		}
		stats.RowsDeleted++
		stats.Deleted[spec.name]++
	}
	if s.testCrashHook != nil {
		// 提交前的注入点：模拟进程被强杀（未提交事务必须整体丢弃）。
		s.testCrashHook()
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return Stats{}, fmt.Errorf("stateindex: 提交事务失败: %w", err)
	}
	committed = true
	// 提交成功后才更新镜像：镜像与库内容因此始终一致。
	for _, pending := range writes {
		s.mirror[specs[pending.table].name][pending.row.key] = pending.row.fp
	}
	for _, pending := range deletes {
		delete(s.mirror[specs[pending.table].name], pending.row.key)
	}
	return finish(), nil
}

// scanRow 把一行结果读成 any 切片（nil 保持 nil，以便区分 NULL 与空串）。
func scanRow(rows *sql.Rows, columns int) ([]any, error) {
	values := make([]any, columns)
	targets := make([]any, columns)
	for i := range values {
		targets[i] = &values[i]
	}
	if err := rows.Scan(targets...); err != nil {
		return nil, err
	}
	for i, value := range values {
		if raw, ok := value.([]byte); ok {
			values[i] = string(raw)
		}
	}
	return values, nil
}

func asUint64(value any) (uint64, error) {
	switch v := value.(type) {
	case int64:
		return uint64(v), nil
	case string:
		return parseUint64(v)
	case []byte:
		return parseUint64(string(v))
	default:
		return 0, fmt.Errorf("无法把 %T 当作整数读取", value)
	}
}

func asInt64(value any) (int64, error) {
	switch v := value.(type) {
	case int64:
		return v, nil
	case string:
		parsed, err := strconv.ParseInt(v, 10, 64)
		return parsed, err
	case []byte:
		return strconv.ParseInt(string(v), 10, 64)
	case nil:
		return 0, nil
	default:
		return 0, fmt.Errorf("无法把 %T 当作整数读取", value)
	}
}

func parseUint64(text string) (uint64, error) {
	return strconv.ParseUint(text, 10, 64)
}

func intValue(value any) int64 {
	parsed, _ := asInt64(value)
	return parsed
}

func asBool(value any) bool {
	switch v := value.(type) {
	case int64:
		return v != 0
	case bool:
		return v
	case string:
		return v == "1" || strings.EqualFold(v, "true")
	case []byte:
		return string(v) == "1" || strings.EqualFold(string(v), "true")
	default:
		return false
	}
}

func asText(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func asBytes(value any) []byte {
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		return []byte(v)
	case []byte:
		return v
	default:
		return []byte(fmt.Sprintf("%v", v))
	}
}

// appendRow 把一行结果按表加入 State。
func appendRow(st *State, spec tableSpec, values []any) error {
	var err error
	switch spec.name {
	case "source":
		st.Sources = append(st.Sources, SourceRow{
			Key: asText(values[0]), LogSourceID: asText(values[1]), SourceGeneration: asText(values[2]),
			StorageNamespace: asText(values[3]), UpdatedAtUnixMilli: intValue(values[4]),
		})
	case "position":
		row := PositionRow{Key: asText(values[0]), AcquirePaused: asBool(values[4]), PauseReason: asText(values[5])}
		if row.ReadPos, err = asUint64(values[1]); err != nil {
			return err
		}
		if row.DurablePos, err = asUint64(values[2]); err != nil {
			return err
		}
		if row.ReclaimPos, err = asUint64(values[3]); err != nil {
			return err
		}
		st.Positions = append(st.Positions, row)
	case "gap":
		row := GapRow{Key: asText(values[0]), Reason: asText(values[4]), Detail: asText(values[5]),
			Resolved: asBool(values[6]), Resolution: asText(values[7])}
		if row.ID, err = asInt64(values[1]); err != nil {
			return err
		}
		if row.StartPos, err = asUint64(values[2]); err != nil {
			return err
		}
		if row.EndPos, err = asUint64(values[3]); err != nil {
			return err
		}
		st.Gaps = append(st.Gaps, row)
	case "projection":
		row := ProjectionRow{Key: asText(values[0]), Generation: asText(values[1]), Pending: asBool(values[3]),
			UpdatedAtUnixMilli: intValue(values[4])}
		if row.EventsStoredThrough, err = asUint64(values[2]); err != nil {
			return err
		}
		st.Projections = append(st.Projections, row)
	case "instance_binding":
		st.Instances = append(st.Instances, InstanceRow{
			UUID: asText(values[0]), Namespace: asText(values[1]), Generation: asText(values[2]),
			Mode: asText(values[3]), WorkDir: asText(values[4]),
		})
	case "source_aux":
		st.Aux = append(st.Aux, AuxRow{Key: asText(values[0]), Config: asBytes(values[1]), Payload: asBytes(values[2])})
	case "source_wal":
		row := WALRow{Key: asText(values[0]), EventID: asText(values[2]), Appended: asBool(values[5]),
			Durable: asBool(values[6]), Body: asBytes(values[7])}
		if row.Seq, err = asUint64(values[1]); err != nil {
			return err
		}
		if row.RecordStart, err = asUint64(values[3]); err != nil {
			return err
		}
		if row.RecordEnd, err = asUint64(values[4]); err != nil {
			return err
		}
		st.WAL = append(st.WAL, row)
	case "delivery_batch":
		row := BatchRow{Key: asText(values[0]), State: asText(values[4])}
		if row.Ordinal, err = asInt64(values[1]); err != nil {
			return err
		}
		if row.StartPos, err = asUint64(values[2]); err != nil {
			return err
		}
		if row.EndPos, err = asUint64(values[3]); err != nil {
			return err
		}
		st.Batches = append(st.Batches, row)
	default:
		return fmt.Errorf("stateindex: 未知表 %q", spec.name)
	}
	return nil
}

// recordSample 记录一次耗时采样。
func (s *Store) recordSample(started time.Time, stats Stats) {
	s.samples = append(s.samples, Sample{
		StartedAt: started, Duration: stats.Duration,
		RowsWritten: stats.RowsWritten, RowsDeleted: stats.RowsDeleted, BytesWritten: stats.BytesWritten,
	})
	if len(s.samples) > sampleRing {
		s.samples = append([]Sample(nil), s.samples[len(s.samples)-sampleRing:]...)
	}
}

// Samples 返回采样环副本（真机验证据此打印 P50/P95，见 spec §3.3）。
func (s *Store) Samples() []Sample {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Sample(nil), s.samples...)
}

// Latency 返回采样环上的 P50/P95/Max 耗时。
func (s *Store) Latency() Latency {
	s.mu.Lock()
	samples := append([]Sample(nil), s.samples...)
	s.mu.Unlock()
	if len(samples) == 0 {
		return Latency{}
	}
	durations := make([]time.Duration, 0, len(samples))
	var max time.Duration
	for _, sample := range samples {
		durations = append(durations, sample.Duration)
		if sample.Duration > max {
			max = sample.Duration
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	return Latency{
		Count: len(samples),
		P50:   percentile(durations, 0.50),
		P95:   percentile(durations, 0.95),
		Max:   max,
	}
}

func percentile(sorted []time.Duration, fraction float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	index := int(fraction * float64(len(sorted)))
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}
