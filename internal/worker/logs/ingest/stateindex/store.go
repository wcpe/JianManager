package stateindex

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
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
//
// 为什么从 512 提到 4096（FR-498 切分改造的连带项）：采样环的消费者（apps/worker 的
// 分钟打点）按「样本身份游标」累计窗口增量，前提是「单窗口新增样本数 < 环容量」。
// 512 是按「一周期一采样」估的（60 源实测约 2.5 周期/s ⇒ 窗口 150 样本）；切分后一次周期
// 会产生多个提交单元采样（60 源 × 60 行/s 实测约 2 单元/周期 ⇒ 约 400 样本/分钟），
// 高负载档位会顶到 512，届时窗口增量只能是下界。4096 给窗口留出 10 倍余量，
// 每个 Sample 约 300 B，整环约 1.2 MiB（相对 Worker 的 GiB 级 RSS 可忽略）。
const sampleRing = 4096

// batchUpsertRows 是一条多值 UPSERT 语句合并的行数上限。
//
// 取值来自实测（生产 307MB 副本的 746,884 条缺口）：逐行 43.0s、每语句 32 行 14.8s、
// 64 行 15.1s、128 行 17.3s。32~64 已是平台段，128 起反而变差（单条语句的解析与参数绑定
// 本身变重），故取 32；同时它把单条语句的实参控制在 32×9=288 个以内，远离 SQLite 的参数上限。
const batchUpsertRows = 32

// batchDeleteRows 是一条多值 DELETE 语句合并的行数上限。
//
// 与 UPSERT 同源的理由（逐行 ExecContext 的语句解析/规划是主导成本）：实测删除单位成本
// 29.1 µs/行（写入 26.5 µs/行），而原实现是逐行一条 DELETE——600 源量级下每周期上千次
// 语句解析全花在删除上。合并行数取同一个 32：单条语句的实参最多 32×5=160 个（主键最长 5 列），
// 远离 SQLite 的参数上限，且与 UPSERT 的批量节奏一致，便于用同一条读数口径对比。
const batchDeleteRows = batchUpsertRows

// walCheckpointPages / sqlitePageSize 决定显式 WAL 归并的触发阈值（页数 × 页大小 ≈ 4 MiB），
// 与 SQLite 默认的 wal_autocheckpoint（1000 页）同量级——但**归并时机由我们自己决定**
// （见 Open 里关闭 wal_autocheckpoint 的理由与 checkpointPassiveIfNeeded 的说明）。
const (
	walCheckpointPages = 1000
	sqlitePageSize     = 4096
)

// DefaultCommitMaxRows / DefaultCommitMinRows / DefaultCommitTarget 是提交单元预算的默认值。
//
// 取值依据（FR-498 实测，60 源 × 60 行/s，真进程夹具 + 独立复刻探针）：
//   - 行成本近似线性且可测——写入 26.5 µs/行、删除 29.1 µs/行（批量前口径；批量化后单提交单元
//     p50 降到 ≈13 µs/行）；
//   - **MaxRows=512**：同夹具实测单提交单元 p50 ≈ 12.5 ms、p95 ≈ 40.6 ms；1024 行进制的 p95
//     已达 47 ms（探针）/ 58–64 ms（60 源真机档），贴着 50 ms 验收线没有余量。512 让 p95 留出
//     ≈20 % 余量，代价只有「提交次数翻倍」——60 源实测写入 ≈ 3.7 k 行/s ⇒ 约 7 次提交/秒，
//     每次 BEGIN+COMMIT 开销 ≈0.1 ms，合计 ≈1 ms/s，对吞吐无可测影响。
//   - MinRows=64：保证再慢的机器也不退化成逐行（逐行会让语句解析重新成为主导成本）。
//   - Target=40 ms：自适应控制器的收敛点，验收线 50 ms 留 25 % 余量吸收抖动（硬上界 = 目标 × 5/4）。
const (
	DefaultCommitMaxRows = 512
	DefaultCommitMinRows = 64
	DefaultCommitTarget  = 40 * time.Millisecond
)

// CommitBudget 是**单个提交单元**（一次 IMMEDIATE 事务）的资源预算：行数上界 + 耗时上界。
//
// 为什么需要（FR-498 实测，60 源）：跨源并发（8）把最多 8 个源的批次合并成一次 ApplyScoped，
// 于是同一个 SQLite 事务要写上万行，单次持久化 p95 随负载线性恶化：
// 66–73 ms @1.8k 行/s → 198–228 ms @3.6k → 348–366 ms @5.4k，是 60 源下**唯一越过**
// 「单次持久化 ≤50 ms」验收线的指标。既然行成本近似线性，把**事务规模**限住即可把耗时限住。
//
// 三个旋钮：
//   - MaxRows：单提交单元行数上限（计划切分的依据，配置上界）；
//   - MinRows：自适应收缩下限（再慢也不退化成逐行）；
//   - Target：单提交单元耗时目标——控制器按「实测行数 × Target ÷ 实测耗时」反推下一单元的行数
//     预算，实测偏慢即收缩、偏快且用满预算即扩张，夹在 [MinRows, MaxRows] 之间。
//
// 执行中另有**耗时硬上界**（Target × 5/4，默认 50 ms = 验收线本身）：事务内每开始一条语句前
// 检查已耗时，越界即把剩余行留给下一个提交单元。目标值负责收敛，硬上界负责兜住抖动尖峰。
type CommitBudget struct {
	MaxRows int
	MinRows int
	Target  time.Duration
}

// DefaultCommitBudget 返回默认提交单元预算。
func DefaultCommitBudget() CommitBudget {
	return CommitBudget{MaxRows: DefaultCommitMaxRows, MinRows: DefaultCommitMinRows, Target: DefaultCommitTarget}
}

// Normalized 把非法配置收敛到默认：非正的行数/耗时都是误写（0 行预算无法表达任何合法语义），
// 一律回退默认；MinRows > MaxRows 时夹到 MaxRows。配置误写不得让切分失效。
func (b CommitBudget) Normalized() CommitBudget {
	out := b
	if out.MaxRows <= 0 {
		out.MaxRows = DefaultCommitMaxRows
	}
	if out.MinRows <= 0 {
		out.MinRows = DefaultCommitMinRows
	}
	if out.MinRows > out.MaxRows {
		out.MinRows = out.MaxRows
	}
	if out.Target <= 0 {
		out.Target = DefaultCommitTarget
	}
	return out
}

// hardLimit 返回单提交单元的执行中耗时硬上界：目标 × 5/4（默认 40 ms → 50 ms，即验收线本身）。
func (b CommitBudget) hardLimit() time.Duration {
	return b.Target * 5 / 4
}

// pendingRow 是一条待写入/待删除的行：表下标 + 行数据（删除时只用 owner/key，
// values 不参与；owner 用于提交后定位镜像分组）。
type pendingRow struct {
	table int
	row   rowData
}

// Sample 是**一个提交单元**（一次 IMMEDIATE 事务）的耗时采样。
//
// 口径（FR-498 切分改造）：一次 ApplyScoped 可能被切成多个提交单元，每个单元一条采样。
// 口径改变的理由见 ApplyScoped 的注释：提交单元 = 原子单元 = 持写锁单元，验收线
// 「单次持久化 ≤50 ms」约束的就是这个单元。
type Sample struct {
	StartedAt    time.Time
	Duration     time.Duration
	RowsWritten  int
	RowsDeleted  int
	RowsPlanned  int
	BytesWritten int64
	// Planned 是规划行数的按表明细（见 Stats.Planned）。
	Planned map[string]int
	// CycleID 是本采样所属持久化周期的单调序号（一个周期可切成多个提交单元）。
	CycleID uint64
	// ChunkIndex / Chunks 是本单元在周期内的下标与周期内的单元总数。
	ChunkIndex int
	Chunks     int
}

// Rows 是本提交单元写入 + 删除的行数（「每事务行数有界」的直接观测量）。
func (s Sample) Rows() int { return s.RowsWritten + s.RowsDeleted }

// Latency 是采样环上的耗时分位（真机验收「单次持久化 ≤50ms」的读数口径）。
//
// P50/P95/Max 是**提交单元**口径；MaxRows 是环内单个提交单元的最大行数（每事务行数有界的
// 观测量），Cycles 是环内覆盖的持久化周期数（用于判断「一周期被切成了几个单元」）。
type Latency struct {
	Count   int
	P50     time.Duration
	P95     time.Duration
	Max     time.Duration
	MaxRows int
	Cycles  int
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
	// Chunks 是本周期切分出的提交单元数（≥1；无变更的空转周期也是 1）。
	Chunks int
	// MaxChunkRows 是本周期内单个提交单元的最大行数（写 + 删）。
	MaxChunkRows int
	// Statements 是本周期实际发出的 SQL 语句数（写 + 删）。
	//
	// 为什么要观测它：批量化的收益全在「一条语句处理几行」上——退回逐行时这个值会等于行数
	// （实测：删除逐行 29.1 µs/行，批量后每 32 行一条语句）。行数看不出批量是否还在生效，
	// 语句数一眼就能看出。删除批量化的回归断言正是用它（TestBatchDeleteRemovesExactlyTheMirroredRows）。
	Statements int
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

	// budget 是提交单元的配置预算（配置面下发，见 CommitBudget）；rowBudget 是自适应后的
	// 当前行数预算（在 [MinRows, MaxRows] 之间随实测耗时收缩/扩张），cycle 是周期序号。
	budget    CommitBudget
	rowBudget int
	cycle     uint64

	// testCrashHook 仅供测试注入：在一次写入事务**提交之前**、写完全部变更行之后被调用，
	// 用于在子进程中模拟进程异常中断（os.Exit 或 SIGKILL），验证未提交批次不会残留。
	// 生产恒为 nil。
	testCrashHook func()
}

// Open 打开（必要时创建）索引库：建表、设置 WAL/单写者、并把现有行的指纹读入镜像。
func Open(path string) (*Store, error) {
	return OpenWithBudget(path, DefaultCommitBudget())
}

// OpenWithBudget 与 Open 相同，但由调用方指定提交单元预算（FR-498：配置键 log_index.persist.*）。
// 非法值经 Normalized 收敛，绝不因为配置误写而让切分失效（退化成单事务写全部）。
func OpenWithBudget(path string, budget CommitBudget) (*Store, error) {
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
	budget = budget.Normalized()
	s := &Store{path: path, db: db, mirror: newMirror(), budget: budget, rowBudget: budget.MaxRows}
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
		// 关闭 SQLite 的自动 WAL checkpoint（默认 wal_autocheckpoint=1000 页）：
		// 它会在**某个 COMMIT 内部**同步做「WAL 页回写主库 +（synchronous=NORMAL 下）随后的
		// fsync」，于是那一次提交的耗时被维护成本污染（同一夹具实测：开启时单提交单元 Max 225 ms、
		// 关闭时 56 ms）。归并本身不可省，但**时机可以自己定**——改由 checkpointPassiveIfNeeded
		// 在每个持久化周期末尾显式执行（阈值与默认一致 ≈4 MiB），成本仍完整计入周期总耗时。
		"PRAGMA wal_autocheckpoint=0",
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
	s := &Store{path: path, db: db, readOnly: true, mirror: newMirror(), budget: DefaultCommitBudget()}
	s.rowBudget = s.budget.MaxRows
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

// checkpointPassiveIfNeeded 在持久化周期末尾按阈值做一次**被动** WAL 归并。
//
// 为什么必须由我们自己驱动（而不是留着 SQLite 的 wal_autocheckpoint）：
// 自动 checkpoint 在某个 COMMIT 内部同步完成「WAL 页回写主库 + fsync」，那次提交因此被维护
// 成本拖长——60 源档位 WAL 增长约 3.7 MB/s，阈值 4 MiB ⇒ 每秒命中一次，约 5% 的提交被拖长，
// 直接把单提交单元 p95 顶到 58–64 ms。同夹具实测（关/开自动 checkpoint）：单提交单元
// Max 56 ms → 225 ms、p95 47 ms → 49 ms。归并的工作量不可省（WAL 必须回收），但**时机**完全
// 可以自己定：挪到周期末尾后，它不再延长任何一次提交，也不在持写锁期间做页拷贝；
// 成本仍完整计入周期总耗时（Stats.Duration）——口径诚实，没有把成本藏起来。
//
// PASSIVE 语义：不等待读者、不阻塞写者，能拷多少拷多少（有读者持旧快照时可能拷不完，WAL 继续
// 增长，下个周期再试）。失败只告警不阻断：数据始终在 WAL 里且可见，最坏情况是 WAL 偏大，
// 绝不让「归并没做完」变成「采集出错」。
func (s *Store) checkpointPassiveIfNeeded(ctx context.Context) {
	if s == nil || s.db == nil || s.readOnly {
		return
	}
	if s.WALBytes() < int64(walCheckpointPages)*sqlitePageSize {
		return
	}
	var busy, logPages, movedPages int
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &logPages, &movedPages); err != nil {
		slog.Warn("索引 WAL 被动归并未执行（数据仍在 WAL 中且可见，下个周期再试）", "error", err)
		return
	}
	if busy != 0 {
		slog.Debug("索引 WAL 被动归并未拷完（有读者持旧快照），下个周期再试",
			"log", logPages, "moved", movedPages)
	}
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
//
// # 提交单元切分（FR-498 P0：单次持久化 ≤50 ms）
//
// 一次 ApplyScoped = 一个持久化周期，周期内**不再只开一个事务**：差异算好之后，
// 变更行按「行数预算 + 耗时硬上界」切成若干**提交单元**，每个单元一个 IMMEDIATE 事务
// （见 commitChunks 与 CommitBudget）。原因（实测）：跨源并发把最多 8 个源的批次合并成
// 一次 ApplyScoped，单事务上万行，p95 从 66–73 ms（1.8k 行/s）涨到 348–366 ms（5.4k 行/s）；
// 行成本近似线性（写 26.5 µs/行、删 29.1 µs/行），限住事务规模即可限住耗时的上界。
//
// 口径随之明确：验收线「单次持久化 ≤50 ms」约束的是**单个提交单元**——提交单元 = 原子单元
// = 持写锁单元，三者在实现里是同一件事。周期总耗时仍由 Stats.Duration 给出（诚实读数，
// 总量不变：该写的字节一个不少，只是不再攒在一个事务里）。采样环每条样本对应一个提交单元
// （Sample.Cycles/ChunkIndex 可还原周期结构）。
//
// # 崩后语义（切分引入的唯一语义变化，逐条论证）
//
// 事务边界变多，崩后可留下「已提交的前缀」。回退幅度**不变**（仍 ≤ 一个周期：最坏情况是
// 第一个单元都没提交），但中间态的合法性必须自证。为此写入阶段按 writeOrderRank 排序：
// **水位表 position 永远最后**（父表 source 仍最先，外键不受影响）。于是任何前缀都满足
//
//	「WAL 行/缺口/投递批次已落库」 ⊇ 「position 里记的水位」
//
// 方向是安全的：position 偏旧 ⇒ 重启后 tailer 从更早的 durable/read 位置续读（
// acquire/tailer.go 的 cursor resume），已落库的 WAL 行与重读的区间重叠 ⇒ 至多产生**重复**
// 投递（与 FR-497「允许重复」同口径，且这正是崩后既有行为）；反方向（水位先进、WAL 行没进）
// 才会丢事件，而 position 最后落库恰好排除了它。删除按表倒序（子表在前）不受影响，
// 单元边界只会落在排序序列中间，故「删父表前先删子表」的顺序全局保持。
func (s *Store) ApplyScoped(desired State, owners []string) (Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return Stats{}, errors.New("stateindex: 只读库不接受写入")
	}
	started := time.Now()
	stats := Stats{Written: map[string]int{}, Deleted: map[string]int{}, Planned: map[string]int{}}
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
		stats.Duration = time.Since(started)
		stats.Chunks = 1
		s.cycle++
		s.recordSample(Sample{
			StartedAt: started, Duration: stats.Duration, CycleID: s.cycle, Chunks: 1,
			RowsPlanned: stats.RowsPlanned, Planned: stats.Planned,
		})
		return stats, nil
	}
	// 删除按表倒序（子表在前），满足外键约束；写入按 writeOrderRank 排序（父表在前、
	// 水位表 position 最后——为什么 position 必须最后见 ApplyScoped 的崩后语义论证）。
	sort.SliceStable(deletes, func(i, j int) bool { return deletes[i].table > deletes[j].table })
	sort.SliceStable(writes, func(i, j int) bool { return writeOrderRank[writes[i].table] < writeOrderRank[writes[j].table] })

	s.cycle++
	samples, err := s.commitChunks(ctx, writes, deletes, s.cycle, &stats)
	if err != nil {
		return Stats{}, err
	}
	// 采样逐单元记录，但周期结构（本单元是第几个、一个周期共几个）与周期口径的规划明细
	// 只有全部跑完才知道，故在最后统一补上；打点侧据此还原「一周期被切成了几个单元」。
	for index := range samples {
		samples[index].ChunkIndex = index
		samples[index].Chunks = len(samples)
		samples[index].RowsPlanned = stats.RowsPlanned
		samples[index].Planned = stats.Planned
		s.recordSample(samples[index])
	}
	// WAL 归并在周期末尾显式执行：它**不属于任何提交单元**（因此不会拖长任何一次提交的耗时，
	// 也不在持写锁期间做页拷贝），但成本完整计入下面这一行算出的周期总耗时。
	s.checkpointPassiveIfNeeded(ctx)
	stats.Duration = time.Since(started)
	return stats, nil
}

// writeOrderRank 给出写入阶段的表序：父表在前（外键），**水位表 position 永远最后**。
//
// 为什么 position 必须最后（见 ApplyScoped 的崩后语义论证）：切分让「已提交的前缀」成为
// 崩溃可留下的中间态，而 read/durable 水位一旦先落库、对应 WAL 行没落库，重启后 tailer 会从
// 新水位续读，那些事件就再也读不回来（真丢数据）。把水位排到最后，任何前缀都只会让水位
// 偏旧 ⇒ 至多重复投递（允许），不会丢。
//
// 其余表之间的次序只要求父表在子表之前：source 是全部七张表的父行，故它排在最前。
var writeOrderRank = func() [tableCount]int {
	order := []int{tblSource, tblGap, tblProjection, tblInstanceBinding, tblSourceAux, tblSourceWAL, tblDeliveryBatch, tblPosition}
	var rank [tableCount]int
	for index, table := range order {
		rank[table] = index
	}
	return rank
}()

// commitChunks 把本次变更按「行数预算 + 耗时硬上界」切成若干提交单元，逐单元提交。
//
// 每单元：BEGIN IMMEDIATE → 写本单元的变更行（同表连续行合并成多值 UPSERT）→ 删本单元的
// 陈旧行（同表连续行合并成多值 DELETE）→ 崩溃注入点 → COMMIT → 更新镜像 → 记录采样 →
// 用实测行数/耗时反推下一单元的预算。
//
// 单元边界只影响「哪些行同事务」，不影响行序与行内容：整个序列是 writes（按 writeOrderRank）
// 后接 deletes（按表倒序），单元是它的连续切片，因此外键顺序与「只写变更行」判据逐字不变。
//
// 返回本周期各单元的采样（**不**登记进采样环）：周期结构与周期口径的规划明细要等全部单元
// 跑完才知道，由 ApplyScoped 统一补齐后登记。
func (s *Store) commitChunks(ctx context.Context, writes, deletes []pendingRow, cycle uint64, stats *Stats) ([]Sample, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("stateindex: 获取写连接失败: %w", err)
	}
	defer func() { _ = conn.Close() }()

	limit := s.budget.hardLimit()
	wi, di := 0, 0
	var samples []Sample
	for wi < len(writes) || di < len(deletes) {
		chunkStarted := time.Now()
		writtenFrom, deletedFrom := wi, di
		budget := s.rowBudget
		if budget < 1 {
			budget = 1
		}
		rows := 0
		// 单元内的行数与字数据单独累计：采样是**单元**口径（打点按样本增量结账），
		// 周期的合计在提交成功后并入 stats。
		chunkStats := Stats{Written: map[string]int{}, Deleted: map[string]int{}}
		// 整个单元（写 + 删 + 提交）都在同一个 IMMEDIATE 事务内：BEGIN 在写入之前，
		// 否则语句会各自 autocommit——那正是「崩溃不丢」回归要抓的形态。
		if err := s.runUnit(ctx, conn, func() error {
			// 单元内先写后删：与不分单元时的顺序一致（写入建立/更新父行，删除随后收尾）。
			//
			// 每轮迭代只放「一条批量语句的行数」（batchUpsertRows/batchDeleteRows），因此耗时
			// 硬上界的检查粒度就是一条语句（约 1 ms），而不是一整段同表行（那可能是几十毫秒）；
			// rows 跨迭代累计，行数预算照常约束整个单元。rows > 0 的短路保证至少推进一行，
			// 绝不出现「开了事务却什么都不做」。
			for wi < len(writes) && rows < budget {
				if limit > 0 && rows > 0 && time.Since(chunkStarted) >= limit {
					break // 耗时硬上界：剩余行留给下一个提交单元
				}
				table := writes[wi].table
				maxRun := budget - rows
				if maxRun > batchUpsertRows {
					maxRun = batchUpsertRows
				}
				end := wi
				for end < len(writes) && writes[end].table == table && end-wi < maxRun {
					end++
				}
				if err := s.applyTableWrites(ctx, conn, specs[table], writes[wi:end], &chunkStats); err != nil {
					return err
				}
				rows += end - wi
				wi = end
			}
			for di < len(deletes) && rows < budget {
				if limit > 0 && rows > 0 && time.Since(chunkStarted) >= limit {
					break
				}
				table := deletes[di].table
				maxRun := budget - rows
				if maxRun > batchDeleteRows {
					maxRun = batchDeleteRows
				}
				end := di
				for end < len(deletes) && deletes[end].table == table && end-di < maxRun {
					end++
				}
				if err := s.applyTableDeletes(ctx, conn, specs[table], deletes[di:end], &chunkStats); err != nil {
					return err
				}
				rows += end - di
				di = end
			}
			return nil
		}); err != nil {
			return nil, err
		}
		// 提交成功后才更新镜像与周期合计：未提交的单元不碰镜像（镜像与库内容因此始终一致）。
		for _, pending := range writes[writtenFrom:wi] {
			s.setMirror(specs[pending.table].name, pending.row.owner, pending.row.key, pending.row.fp)
		}
		for _, pending := range deletes[deletedFrom:di] {
			s.deleteMirror(specs[pending.table].name, pending.row.owner, pending.row.key)
		}
		stats.RowsWritten += chunkStats.RowsWritten
		stats.RowsDeleted += chunkStats.RowsDeleted
		stats.BytesWritten += chunkStats.BytesWritten
		stats.Statements += chunkStats.Statements
		for name, count := range chunkStats.Written {
			stats.Written[name] += count
		}
		for name, count := range chunkStats.Deleted {
			stats.Deleted[name] += count
		}
		stats.Chunks++
		if rows > stats.MaxChunkRows {
			stats.MaxChunkRows = rows
		}
		elapsed := time.Since(chunkStarted)
		samples = append(samples, Sample{
			StartedAt: chunkStarted, Duration: elapsed, CycleID: cycle, Chunks: 1,
			RowsWritten: chunkStats.RowsWritten, RowsDeleted: chunkStats.RowsDeleted,
			BytesWritten: chunkStats.BytesWritten,
		})
		s.adaptBudget(rows, elapsed)
	}
	return samples, nil
}

// runUnit 执行一个提交单元：BEGIN IMMEDIATE → body（写本单元的变更行、删本单元的陈旧行）
// → 崩溃注入点 → COMMIT。body 返回错误或 COMMIT 失败即回滚本单元，已提交的单元不受影响
// （回退幅度 ≤ 一个周期，见 ApplyScoped 的崩后语义论证）。
//
// BEGIN 必须在 body 之前：SQLite 默认 autocommit，若先写后 BEGIN，每条语句都会各自成事务，
// 崩溃就会留下半截批次（这正是 TestStoreCrashDiscardsUncommittedBatch 要抓的形态）。
func (s *Store) runUnit(ctx context.Context, conn *sql.Conn, body func() error) error {
	// BEGIN IMMEDIATE：一开始就取写锁，避免 WAL 下锁升级失败造成 SQLITE_BUSY。
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("stateindex: 开启事务失败: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()
	if err := body(); err != nil {
		return err
	}
	if s.testCrashHook != nil {
		// 提交前的注入点：模拟进程被强杀（未提交事务必须整体丢弃）。
		s.testCrashHook()
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("stateindex: 提交事务失败: %w", err)
	}
	committed = true
	return nil
}

// adaptBudget 用刚完成的提交单元的实测值反推下一个单元的行数预算（比例控制 + 单步限幅）。
//
// 规则（只在不达标或确实富余时动作，落在目标带内保持不动，避免抖动）：
//   - 实测耗时 > 目标：按「行数 × 目标 ÷ 实测」收缩（最少减半，下限 MinRows）；
//   - 实测耗时 < 目标/2 且本单元用满了预算：温和扩张 25%（上限 MaxRows）；
//   - 其余（含未用满预算的短单元）：不动——短单元的行数/耗时比被每单元的固定开销污染，
//     用它推预算会把「小周期」误读成「每行很慢」。
func (s *Store) adaptBudget(rows int, elapsed time.Duration) {
	target := s.budget.Target
	if rows <= 0 || elapsed <= 0 || target <= 0 {
		return
	}
	next := s.rowBudget
	switch {
	case elapsed > target:
		next = int(float64(rows) * float64(target) / float64(elapsed))
		if next > s.rowBudget/2 {
			next = s.rowBudget / 2
		}
	case elapsed < target/2 && rows >= s.rowBudget:
		next = s.rowBudget + s.rowBudget/4
	}
	if next < s.budget.MinRows {
		next = s.budget.MinRows
	}
	if next > s.budget.MaxRows {
		next = s.budget.MaxRows
	}
	s.rowBudget = next
}

// CommitBudget 返回本库当前的提交单元配置预算（观测用；自适应后的实时预算见 Latency）。
func (s *Store) CommitBudget() CommitBudget {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.budget
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

// applyTableDeletes 删除一张表的一段陈旧行：同表连续的主键合并成一条多值 DELETE。
//
// 为什么要批量（FR-498 实测）：原实现是「逐行一条 DELETE + 一次 ExecContext」，真机删除单位
// 成本 29.1 µs/行，而删除量在稳态与写入量同量级（60 源实测删除/写入 ≈ 0.97）——每周期上千次
// 语句解析/规划全花在这里。合并只改变「一条语句删几行」，不改变删哪些行。
//
// 它仍然完全由镜像差异驱动（deletes 来自 ApplyScoped 的比对结果），不新增任何绕过镜像的
// 删除路径：镜像里的行在、期望状态里没有的行，才进入这个函数。
func (s *Store) applyTableDeletes(ctx context.Context, conn *sql.Conn, spec tableSpec, rows []pendingRow, stats *Stats) error {
	for start := 0; start < len(rows); {
		size := batchDeleteRows
		if remaining := len(rows) - start; size > remaining {
			size = remaining
		}
		statement, args, err := buildBatchDelete(spec, rows[start:start+size])
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, statement, args...); err != nil {
			return fmt.Errorf("stateindex: 批量删除 %s 旧行失败: %w", spec.name, err)
		}
		stats.Statements++
		stats.RowsDeleted += size
		stats.Deleted[spec.name] += size
		start += size
	}
	return nil
}

// buildBatchDelete 把若干行合并成一条多值 DELETE，返回语句与实参。
//
// 形态按主键列数二选一（两种都被 SQLite 支持，已由 TestBuildBatchDeleteRunsOnSQLite 实跑守住）：
//
//	单列主键：DELETE FROM t WHERE k IN (?, ?, ...)
//	多列主键：DELETE FROM t WHERE (k1, k2, ...) IN (VALUES (?, ?, ...), (?, ?, ...))
//
// 实参个数 = 行数 × 主键列数（每语句最多 32×5 = 160 个），远离 SQLite 的参数上限。
func buildBatchDelete(spec tableSpec, rows []pendingRow) (string, []any, error) {
	if len(spec.keyCols) == 0 {
		return "", nil, fmt.Errorf("stateindex: %s 没有主键列，无法批量删除", spec.name)
	}
	unit := "(" + strings.TrimSuffix(strings.Repeat("?, ", len(spec.keyCols)), ", ") + ")"
	groups := make([]string, 0, len(rows))
	args := make([]any, 0, len(rows)*len(spec.keyCols))
	for _, pending := range rows {
		keyValues, err := decodeMirrorKey(spec, pending.row.key)
		if err != nil {
			return "", nil, err
		}
		if len(keyValues) != len(spec.keyCols) {
			// 主键列数与表定义必须逐列对齐，否则批量语句会把值串位（静默删错行）。
			return "", nil, fmt.Errorf("stateindex: %s 的主键有 %d 列，表定义 %d 列",
				spec.name, len(keyValues), len(spec.keyCols))
		}
		groups = append(groups, unit)
		args = append(args, keyValues...)
	}
	if len(spec.keyCols) == 1 {
		return fmt.Sprintf("DELETE FROM %s WHERE %s IN (%s)",
			spec.name, spec.keyCols[0], strings.Join(groups, ", ")), args, nil
	}
	return fmt.Sprintf("DELETE FROM %s WHERE (%s) IN (VALUES %s)",
		spec.name, strings.Join(spec.keyCols, ", "), strings.Join(groups, ", ")), args, nil
}

// applyTableWrites 写一张表的一段变更行：支持批量的表按 batchUpsertRows 行合并，其余逐行写。
//
// 为什么要批量：逐行 ExecContext 每一行都要重新解析并规划一次语句；迁移要写 74 万行时
// 这部分（而非磁盘）是主导成本——生产 307MB 副本实测逐行 UPSERT 43.0s、每语句 32 行 14.8s。
// 合并只改变「一条语句写几行」，不改变写哪些行，也不改变任何行的取值。
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
			stats.Statements++
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
		stats.Statements++
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

// recordSample 记录一个提交单元的采样。
func (s *Store) recordSample(sample Sample) {
	s.samples = append(s.samples, sample)
	if len(s.samples) > sampleRing {
		s.samples = append([]Sample(nil), s.samples[len(s.samples)-sampleRing:]...)
	}
}

// Samples 返回采样环副本（真机验证据此打印 P50/P95，见 spec §3.3）。
// 每条样本对应一个**提交单元**（一个周期可切成多个），CycleID + ChunkIndex 可还原周期结构。
func (s *Store) Samples() []Sample {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Sample(nil), s.samples...)
}

// Latency 返回采样环上的 P50/P95/Max 耗时（提交单元口径）与单单元最大行数。
func (s *Store) Latency() Latency {
	s.mu.Lock()
	samples := append([]Sample(nil), s.samples...)
	s.mu.Unlock()
	if len(samples) == 0 {
		return Latency{}
	}
	durations := make([]time.Duration, 0, len(samples))
	var max time.Duration
	cycles := make(map[uint64]struct{}, len(samples))
	result := Latency{}
	for _, sample := range samples {
		durations = append(durations, sample.Duration)
		if sample.Duration > max {
			max = sample.Duration
		}
		if rows := sample.Rows(); rows > result.MaxRows {
			result.MaxRows = rows
		}
		cycles[sample.CycleID] = struct{}{}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	result.Count = len(samples)
	result.Cycles = len(cycles)
	result.P50 = percentile(durations, 0.50)
	result.P95 = percentile(durations, 0.95)
	result.Max = max
	return result
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
