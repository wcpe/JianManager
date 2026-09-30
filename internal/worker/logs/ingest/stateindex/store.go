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

// batchUpsertRows 是一条多值 UPSERT 语句合并的行数上限。
//
// 取值来自实测（生产 307MB 副本的 746,884 条缺口）：逐行 43.0s、每语句 32 行 14.8s、
// 64 行 15.1s、128 行 17.3s。32~64 已是平台段，128 起反而变差（单条语句的解析与参数绑定
// 本身变重），故取 32；同时它把单条语句的实参控制在 32×9=288 个以内，远离 SQLite 的参数上限。
const batchUpsertRows = 32

// pendingRow 是一条待写入/待删除的行：表下标 + 行数据（删除时只用 owner/key，
// values 不参与；owner 用于提交后定位镜像分组）。
type pendingRow struct {
	table int
	row   rowData
}

// Sample 是一次 Apply 的耗时采样。
type Sample struct {
	StartedAt    time.Time
	Duration     time.Duration
	RowsWritten  int
	RowsDeleted  int
	RowsPlanned  int
	BytesWritten int64
	// Planned 是规划行数的按表明细（见 Stats.Planned）。
	Planned map[string]int
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
	// RowsPlanned 是本轮**参与规划**的期望行数（mirrorKey + fingerprint 做了多少行）。
	//
	// 它是「成本是否随索引总量增长」的直接观测量，且与 RowsWritten 互补：RowsWritten 只说明
	// 「写了几行」，而稳态的真正成本在「规划了几行」——全量规划 74.7 万行即使一行都不写也要
	// 1.5s（生产实测），按归属收敛到变更源后才与总量解耦。规模回归用它守住这条。
	RowsPlanned int
	// Planned 按表给出规划行数明细（表名 → 行数）。规模回归据此断言**数据表**（gap/
	// source_wal/delivery_batch，行数随数据量增长的那三张）的规划量与索引总量解耦；
	// 根表（source/position/...）的规划量按设计随源数增长（几百行量级，代价可忽略），
	// 全量比对它们正是元数据变更无须显式标记即正确的原因。
	Planned map[string]int
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
	// mirror 记录每张表当前已落库行的指纹，结构为「归属键 → 主键编码 → fp」。
	//
	// 为什么按归属再分一层：增量写入（ApplyScoped）只处理本批次变更的源，需要在不扫描全表的
	// 前提下拿到「该源已落库了哪些行」——删除判定必须知道这一点。若用扁平的
	// （主键编码 → fp）映射，就只能遍历全部键再逐个解码首段求归属，把 O(变更) 又拉回 O(全量)
	// （生产实测 74.7 万键逐条解码 ≈ 0.4s/次）。分层的镜像让每个源的行各自成组，代价只与
	// 变更源的行数相关。
	mirror map[string]map[string]map[string]uint64

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
	s := &Store{path: path, db: db, mirror: newMirror()}
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
		// cache_size 用负数表示 KiB（-131072 = 128 MiB）。索引库在真机上可达 GB 级，
		// 默认 2 MiB 缓存会让迁移/对账期的写入不断回落到读页；128 MiB 在 Worker 的
		// 内存预算内（真机 RSS 上限 2 GiB 量级），且实测对本仓库最大的表有稳定收益。
		"PRAGMA cache_size=-131072",
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
	s := &Store{path: path, db: db, readOnly: true, mirror: newMirror()}
	if _, err := db.ExecContext(context.Background(), "PRAGMA query_only=ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("stateindex: 设置只读模式失败: %w", err)
	}
	return s, nil
}

// Path 返回库文件路径。
func (s *Store) Path() string { return s.path }

// Close 关闭库：先显式把 WAL 归并回主库，再关闭连接。
//
// 为什么必须显式 checkpoint（而不是依赖「最后一个连接关闭时自动归并」）：
// `database/sql` 的连接池在 Close 时并不保证以「干净关闭」收尾——池里的连接若因空闲超时或
// 其它路径先行断开，最后一个连接消失时可能走的是隐式/被动路径，WAL 便留在盘上。生产演练实测：
// 迁移写入 746,884 行后 `-wal` 残留 453.6 MB，且因为 Manager.Stop 从不关闭索引句柄，
// 残留的 WAL 会一直挂到进程被 kill（此时 Windows 上还会连带 -shm 一起留下）。
// 显式 TRUNCATE 把归并这一步变成确定性的，同时把 `-wal` 截回 0 字节。
//
// checkpoint 失败（例如有未结束的读事务）不阻断关闭：这会退化成旧行为（WAL 留盘、下次打开
// 时由 SQLite 自动归并），不影响正确性，故只记录不报错——Close 必须总是能关闭句柄。
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	_ = s.Checkpoint()
	return s.db.Close()
}

// Checkpoint 显式把 WAL 内容归并进主库并截断 `-wal`（TRUNCATE 模式）。
//
// 语义与「正常关闭后 WAL 应为空」一致：调用后 `-wal` 归零，重启不会带着巨大的 WAL 启动。
// 它是幂等的：无 WAL 或已归并时是廉价空操作。
func (s *Store) Checkpoint() error {
	if s == nil || s.db == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkpoint(context.Background())
}

func (s *Store) checkpoint(ctx context.Context) error {
	// wal_checkpoint(TRUNCATE) 返回 (busy, log, checkpointed)：busy=1 表示本次因并发读者
	// 未能完全归并。此时不算错误（数据仍在 WAL 里、依然可见），但必须让调用方知道，
	// 故以错误形态暴露，由调用点决定是否降级。
	var busy, logPages, movedPages int
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logPages, &movedPages); err != nil {
		return fmt.Errorf("stateindex: WAL 归并失败: %w", err)
	}
	if busy != 0 {
		return fmt.Errorf("stateindex: WAL 未完全归并（仍有读者持有事务：log=%d moved=%d）", logPages, movedPages)
	}
	return nil
}

// WALBytes 返回当前 `-wal` 文件大小；文件不存在时为 0。用于关闭路径与巡检的观测口径。
func (s *Store) WALBytes() int64 {
	if s == nil || s.path == "" {
		return 0
	}
	info, err := os.Stat(s.path + "-wal")
	if err != nil {
		return 0
	}
	return info.Size()
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

// newMirror 构造空的镜像结构。
func newMirror() map[string]map[string]map[string]uint64 {
	return make(map[string]map[string]map[string]uint64, tableCount)
}

// mirrorTable 取（必要时创建）某张表的镜像。
func (s *Store) mirrorTable(name string) map[string]map[string]uint64 {
	table := s.mirror[name]
	if table == nil {
		table = make(map[string]map[string]uint64)
		s.mirror[name] = table
	}
	return table
}

// loadMirror 把各表现有行的指纹读入内存镜像（按归属分组）。
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
		table := make(map[string]map[string]uint64)
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
			key := mirrorKey(values[:len(spec.keyCols)]...)
			var fp uint64
			if spec.fpSelf {
				// 带 fp 列的表（source_aux/source_wal）直接读最后一列。
				if values[len(values)-1] == nil {
					_ = rows.Close()
					return fmt.Errorf("stateindex: %s 指纹列为空", spec.name)
				}
				fp = mustFingerprint(values[len(values)-1])
			} else {
				// 无 fp 列的表（spec 五表）由列值现算，与写入侧同一函数、同一类型归一化。
				fp = fingerprint(values[len(spec.keyCols):]...)
			}
			owner, err := mirrorOwner(spec, values)
			if err != nil {
				_ = rows.Close()
				return fmt.Errorf("stateindex: 解析 %s 归属失败: %w", spec.name, err)
			}
			if table[owner] == nil {
				table[owner] = make(map[string]uint64)
			}
			table[owner][key] = fp
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

// mirrorOwner 从镜像行的列值取出归属键（主键首列的文本值）。
//
// 归属就是「这一行属于哪个日志源」（或无归属表的主键首列值）：source/position/projection/
// source_aux/gap/source_wal/delivery_batch 都以日志源为归属，instance_binding 以实例 UUID 为
// 归属。归属值本身不承载语义，只要求**写入侧（planState）与载入侧（本函数）同口径**——
// 两侧不一致会把同一行归到不同组，表现为重启后该行「旧的被删、新的被写」。
//
// keyCols 为空（镜像查询不返回主键列）的形态归到 ownerNone，用于全量对账统一处理。
func mirrorOwner(spec tableSpec, values []any) (string, error) {
	if len(spec.keyCols) == 0 || len(values) == 0 {
		return ownerNone, nil
	}
	text, ok := values[0].(string)
	if !ok {
		return "", fmt.Errorf("主键首列不是文本（%T）", values[0])
	}
	return text, nil
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

// scopedTables 是「按归属增量写入」的表：只有这些表的行数随数据量增长，也只有它们值得
// 把比较范围收敛到变更源。
//
// 其余表（source/position/projection/source_aux/instance_binding）的行数是 O(源数)：每轮全量
// 比较它们的代价只有几百行级别，而它们的**变更信号不在账本修订号里**（投影代次、发布待定、
// 段覆盖水位由上层直接改内存状态）。把它们也纳入增量范围就必须依赖上层在每个改动点显式标记，
// 漏一处即静默丢更新；保留全量比较则天然正确，代价可忽略。取舍：正确性优先，收益在此之外。
var scopedTables = [tableCount]bool{
	tblGap:           true,
	tblSourceWAL:     true,
	tblDeliveryBatch: true,
}

// Apply 把索引的期望状态写入库：只写「指纹变化」的行、删除不再存在的行，全部在一个事务内提交
// （提交即持久；未提交部分在崩溃时整体丢弃——索引回退幅度不超过一个批次）。
//
// 全量形态：调用方给出完整期望状态，本方法自行与镜像比对差异。
func (s *Store) Apply(desired State) (Stats, error) {
	return s.ApplyScoped(desired, nil)
}

// ApplyScoped 是 Apply 的增量形态：owners 非 nil 时，**scopedTables 中的表**只处理这些归属
// （表主键首列，即日志源键）的行，其余归属的行一律假定未变更——不规划、不比对、不写。
//
// 为什么必须有这一层：即使指纹口径完全正确，全量形态每次仍要对**全部**行做规划
// （mirrorKey + fingerprint）与差异比对。生产 307MB 副本（74.7 万行缺口）实测这段固定成本
// 为 Validate 774ms + planState 1.55s + 差异比对 239ms ≈ 2.6s，与「本批次改了哪个源」无关——
// 稳态每 250ms 一轮轮询都付这个钱，达标线（≤50ms）无从谈起。按归属收敛到「本批次变更的源」
// 后，稳态成本只与本批次变更源的行数相关。
//
// owners 为 nil 表示全量（迁移、整表对账、测试仍走这条）。
//
// owners 的语义是**并集**：`[]string{}`（非 nil 空集）表示「没有任何归属变更」，
// 因此 scopedTables 中的所有行都不参与本轮；nil 才是全量。二者必须区分，
// 否则「空变更批次」会被误当成全量重写。
func (s *Store) ApplyScoped(desired State, owners []string) (Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return Stats{}, errors.New("stateindex: 只读库不接受写入")
	}
	started := time.Now()
	stats := Stats{Written: map[string]int{}, Deleted: map[string]int{}, Planned: map[string]int{}}
	// finish 统一记录耗时与采样后返回（避免用 defer 改返回值副本的陷阱）。
	finish := func() Stats {
		stats.Duration = time.Since(started)
		s.recordSample(started, stats)
		return stats
	}
	ctx := context.Background()
	scoped := owners != nil
	owned := make(map[string]struct{}, len(owners))
	for _, owner := range owners {
		owned[owner] = struct{}{}
	}
	// 增量形态下先把「按归属增量」的表裁剪到变更归属：这些表占全量规划的绝大多数成本
	// （生产实测 74.7 万缺口：指纹 0.97s + 主键编码 0.38s），裁剪后它们根本不被规划。
	// 非增量表原样保留，仍走全量比较。
	planned := desired
	if scoped {
		planned = scopeState(desired, owned)
	}
	if err := Validate(planned); err != nil {
		return Stats{}, err
	}
	plans, err := planState(planned, started.UnixMilli())
	if err != nil {
		return Stats{}, err
	}
	for index := range plans {
		spec := specs[index]
		for _, rows := range plans[index] {
			stats.RowsPlanned += len(rows)
			stats.Planned[spec.name] += len(rows)
		}
	}

	// 先算差异（不触库），再开事务，尽量缩短持锁时间。
	var writes []pendingRow
	var deletes []pendingRow
	for index := range specs {
		spec := specs[index]
		table := s.mirrorTable(spec.name)
		// 本轮要处理的归属集合：按归属增量的表在增量形态下 = 调用方指定的集合；
		// 其余情况 = 期望状态与镜像两侧出现过的全部归属（两侧都取，否则「期望状态里已不存在
		// 的源」其陈旧行不会被删掉）。
		var targets map[string]struct{}
		if scoped && scopedTables[index] {
			targets = owned
		} else {
			targets = make(map[string]struct{}, len(plans[index])+len(table))
			for owner := range plans[index] {
				targets[owner] = struct{}{}
			}
			for owner := range table {
				targets[owner] = struct{}{}
			}
		}
		for owner := range targets {
			rows := plans[index][owner]
			mirror := table[owner]
			seen := make(map[string]struct{}, len(rows))
			for _, row := range rows {
				seen[row.key] = struct{}{}
				if previous, ok := mirror[row.key]; ok && previous == row.fp {
					continue // 内容未变：不生成任何写语句——这就是「只写变更行」的判据点
				}
				writes = append(writes, pendingRow{table: index, row: row})
			}
			// 删除：只在本轮覆盖的归属内比对。未覆盖归属的镜像行必然仍在期望状态里
			// （否则该归属就属于变更集合），若在此全表扫描会把它们误删。
			for key := range mirror {
				if _, ok := seen[key]; ok {
					continue
				}
				// 归属原样带上：删除后更新镜像时要用它定位分组。
				deletes = append(deletes, pendingRow{table: index, row: rowData{owner: owner, key: key}})
			}
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

	if err := s.applyWrites(ctx, conn, writes, &stats); err != nil {
		return Stats{}, err
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
		s.setMirror(specs[pending.table].name, pending.row.owner, pending.row.key, pending.row.fp)
	}
	for _, pending := range deletes {
		s.deleteMirror(specs[pending.table].name, pending.row.owner, pending.row.key)
	}
	return finish(), nil
}

// setMirror 更新镜像中的一行指纹。
func (s *Store) setMirror(table, owner, key string, fp uint64) {
	group := s.mirror[table]
	if group == nil {
		group = make(map[string]map[string]uint64)
		s.mirror[table] = group
	}
	rows := group[owner]
	if rows == nil {
		rows = make(map[string]uint64)
		group[owner] = rows
	}
	rows[key] = fp
}

// deleteMirror 移除镜像中的一行（该源已无任何行时整组删除，避免空组长期占位）。
func (s *Store) deleteMirror(table, owner, key string) {
	group := s.mirror[table]
	if group == nil {
		return
	}
	rows := group[owner]
	delete(rows, key)
	if len(rows) == 0 {
		delete(group, owner)
	}
}

// scopeState 把期望状态中「按归属增量」的表（scopedTables）裁剪到指定归属集合，
// 使未变更归属的行不参与规划（mirrorKey + fingerprint）。
//
// 不在 scopedTables 里的表原样保留：它们行数为 O(源数)，仍由 ApplyScoped 做全量比较。
// 裁剪只按归属维度做，不改变任何行的内容。
func scopeState(st State, owned map[string]struct{}) State {
	var out State
	keep := func(owner string) bool {
		_, ok := owned[owner]
		return ok
	}
	for _, row := range st.Gaps {
		if keep(row.Key) {
			out.Gaps = append(out.Gaps, row)
		}
	}
	for _, row := range st.WAL {
		if keep(row.Key) {
			out.WAL = append(out.WAL, row)
		}
	}
	for _, row := range st.Batches {
		if keep(row.Key) {
			out.Batches = append(out.Batches, row)
		}
	}
	// 非增量的表原样保留（含 source/position/projection/aux/instances）。
	out.Sources = st.Sources
	out.Positions = st.Positions
	out.Projections = st.Projections
	out.Instances = st.Instances
	out.Aux = st.Aux
	return out
}

// applyWrites 在事务内写入全部变更行。同一张表连续的行尽量合并成多值 UPSERT —— 逐行
// ExecContext 的语句解析/规划开销是迁移 74 万行时的主导成本（实测 43.0s → 14.8s）。
// 合并只改变「一条语句写几行」，不改变写哪些行，也不改变任何行的取值。
func (s *Store) applyWrites(ctx context.Context, conn *sql.Conn, writes []pendingRow, stats *Stats) error {
	// writes 已按表下标分组（specs 顺序），这里按表切片后逐表写出。
	for start := 0; start < len(writes); {
		table := writes[start].table
		end := start
		for end < len(writes) && writes[end].table == table {
			end++
		}
		spec := specs[table]
		if err := s.applyTableWrites(ctx, conn, spec, writes[start:end], stats); err != nil {
			return err
		}
		start = end
	}
	return nil
}

// applyTableWrites 写一张表的一段变更行：支持批量的表按 batchUpsertRows 行合并，其余逐行写。
func (s *Store) applyTableWrites(ctx context.Context, conn *sql.Conn, spec tableSpec, rows []pendingRow, stats *Stats) error {
	batchSize := 0
	if spec.upsertBatch != "" {
		batchSize = batchUpsertRows
	}
	for start := 0; start < len(rows); {
		size := batchSize
		if size <= 1 {
			// 逐行：语句较简单，失败时报出具体表名即可。
			values, err := rows[start].row.values()
			if err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, spec.upsert, values...); err != nil {
				return fmt.Errorf("stateindex: 写入 %s 失败: %w", spec.name, err)
			}
			stats.RowsWritten++
			stats.Written[spec.name]++
			stats.BytesWritten += valuesSize(values)
			start++
			continue
		}
		if remaining := len(rows) - start; size > remaining {
			size = remaining
		}
		statement, args, written, err := buildBatchUpsert(spec, rows[start:start+size])
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, statement, args...); err != nil {
			return fmt.Errorf("stateindex: 批量写入 %s 失败: %w", spec.name, err)
		}
		stats.RowsWritten += size
		stats.Written[spec.name] += size
		stats.BytesWritten += written
		start += size
	}
	return nil
}

// buildBatchUpsert 把若干行拼成一条多值 UPSERT，返回语句、实参与这些行的字节量。
func buildBatchUpsert(spec tableSpec, rows []pendingRow) (string, []any, int64, error) {
	placeholders := make([]string, len(spec.columns))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	unit := "(" + strings.Join(placeholders, ", ") + ")"
	groups := make([]string, 0, len(rows))
	args := make([]any, 0, len(rows)*len(spec.columns))
	var written int64
	for _, pending := range rows {
		values, err := pending.row.values()
		if err != nil {
			return "", nil, 0, err
		}
		if len(values) != len(spec.columns) {
			// 行构造与表定义必须逐列对齐，否则批量语句会把列串位（静默写错数据）。
			return "", nil, 0, fmt.Errorf("stateindex: %s 行有 %d 列，表定义 %d 列",
				spec.name, len(values), len(spec.columns))
		}
		groups = append(groups, unit)
		args = append(args, values...)
		written += valuesSize(values)
	}
	statement := fmt.Sprintf(spec.upsertBatch, strings.Join(spec.columns, ", "), strings.Join(groups, ", "))
	if spec.onConflict != "" {
		statement += " " + spec.onConflict
	}
	return statement, args, written, nil
}

// ownerNone 是无归属表的归属键（instance_binding 不随任何日志源变更而变更，只在全量对账时处理）。
const ownerNone = ""

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
		RowsWritten: stats.RowsWritten, RowsDeleted: stats.RowsDeleted,
		RowsPlanned: stats.RowsPlanned, BytesWritten: stats.BytesWritten,
		Planned: stats.Planned,
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
