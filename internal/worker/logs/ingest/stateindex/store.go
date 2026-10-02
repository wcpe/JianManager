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
	// DefaultCycleMaxRows 是单次 ApplyScoped 的行数预算默认值。
	//
	// 取值依据（2026-10-02 事故）：稳态一轮采集的变更量在数百~数千行（60 源 × 60 行/s，
	// 250ms 一轮 ⇒ 单轮 ≈ 900 行），131 072 行给正常路径留出两个数量级余量——**正常负载下
	// 永远不会触发**（它不是为了限流，而是为了给「异常大批差异」设一个有限上界）。
	// 按单元实测 13 µs/行，131k 行 ≈ 1.7s（远小于 RPC/HTTP 的 10–300s 等待面），
	// 且它在后台续做路径上会被反复让路，不会独占持久化门。
	DefaultCycleMaxRows = 1 << 17
	// DefaultCycleMaxDuration 是单次 ApplyScoped 的墙钟预算默认值（同上：正常负载不会触发）。
	DefaultCycleMaxDuration = 3 * time.Second
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
// 另有两枚**每周期总预算**旋钮（CycleMaxRows / CycleMaxDuration）：它们约束的不是「一个提交单元」，
// 而是「一次 ApplyScoped 调用最多做多少」——超出即返回 `Stats.Incomplete=true`，剩余差异由调用方
// 下一轮再调（镜像按提交单元增量更新 ⇒ 重算差异天然就是续跑，不重复、不遗漏）。
//
// 为什么必须有（2026-10-02 生产事故：启动路径单线程、与规模成正比的持久化）：
// 单元预算只把工作**切碎**，循环体 `for wi < len(writes) || di < len(deletes)` 没有总数上界，
// 于是「一次调用」的耗时仍与整批差异行数成正比。启动恢复一次剪掉整个积压前缀（生产 3.4M 行）
// ⇒ 单次 ApplyScoped 要发 10 万条 DELETE，`ingest.New` 同步等它 ⇒ worker 20–30 分钟不监听。
type CommitBudget struct {
	MaxRows int
	MinRows int
	Target  time.Duration
	// CycleMaxRows 是单次 ApplyScoped 最多处理的行数（写 + 删）；≤0 用默认。
	CycleMaxRows int
	// CycleMaxDuration 是单次 ApplyScoped 的墙钟预算；≤0 用默认。检查落在提交单元之间
	// （单元内不可中断，其自身耗时由 hardLimit 兜住），故实际耗时可上浮「一个单元」。
	CycleMaxDuration time.Duration
}

// DefaultCommitBudget 返回默认提交单元预算。
func DefaultCommitBudget() CommitBudget {
	return CommitBudget{
		MaxRows: DefaultCommitMaxRows, MinRows: DefaultCommitMinRows, Target: DefaultCommitTarget,
		CycleMaxRows: DefaultCycleMaxRows, CycleMaxDuration: DefaultCycleMaxDuration,
	}
}

// Normalized 把非法配置收敛到默认：非正的行数/耗时都是误写（0 行预算无法表达任何合法语义），
// 一律回退默认；MinRows > MaxRows 时夹到 MaxRows。配置误写不得让切分失效。
func (b CommitBudget) Normalized() CommitBudget {
	out := b
	if out.CycleMaxRows <= 0 {
		out.CycleMaxRows = DefaultCycleMaxRows
	}
	if out.CycleMaxDuration <= 0 {
		out.CycleMaxDuration = DefaultCycleMaxDuration
	}
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

// WALPrune 是「source_wal 按**谓词水位**丢弃整段旧行」的请求（①水位化差异删除）。
//
// 语义与判据：删除该归属中 `durable = 1 且 record_end <= EndThrough` 的全部行——与采集侧
// 内存剪枝 `WAL.pruneReclaimed(pos)` 的判据**逐字同源**（那里也是 `Durable && Record.End <= pos`，
// 见 internal/worker/logs/acquire/wal.go）。
//
// 为什么需要它（2026-10-02 生产事故：启动路径单线程、与规模成正比的持久化）：启动恢复一次把
// 整段积压判为可回收（`releaseRecovery` → `TryReclaim`），内存里那段被剪掉，索引侧于是要把
// 「镜像里有、期望里没有」的行逐行删掉——生产 3.4M 行 × 每语句 32 行 = 10.6 万条 DELETE，
// 单线程在 19.5GB 库上跑 20–30 分钟且零进展。本类型把这段差异表达成**一条**谓词范围删除。
//
// 安全性由 ApplyScopedCtx 的等价性守门保证（见 walPrunePlan）：只有「期望集里不存在任何满足
// 该谓词的行」时才启用范围删除，否则整段退回逐行路径。`durable = 1` 这一条不能省——期望集里
// 未耐久（durable=0）的行即使 record_end ≤ 水位也必须留在库里（否则重启后 WAL 少条 ⇒ 未投递
// 正文丢失）。
type WALPrune struct {
	Owner      string
	EndThrough uint64
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
	// Incomplete 表示本次调用受**每周期总预算**约束而提前返回：差异尚未做完。
	//
	// 调用方语义：状态**已落库的部分一致且可用**（镜像按提交单元增量更新，与库内容逐单元一致），
	// 未完成的行没有被写入也没有被宣称完成；再次调用同一入口即可续跑（重算差异。
	// 因为镜像已反映已提交单元，重算的结果天然只剩剩余行 ⇒ 不重复、不遗漏）。
	Incomplete bool
	// RemainingRows 是预算耗尽时尚未处理的差异行数（写 + 删）下界，供调用方决定是否继续。
	RemainingRows int
	// RangePrunes 是本周期发出的**谓词范围删除语句数**（①水位化路径是否启用，一眼可见）。
	//
	// 为什么单独观测「发出了几条」而不是只看行数：稳态下这些语句常常影响 0 行（该段早已删净），
	// 行数会让「路径有没有生效」完全不可见。生产排障要知道的正是前者。
	RangePrunes int
	// RangePruned 是经**谓词范围删除**丢弃的行数（①水位化路径；0 表示本周期没走该路径）。
	// 与 RowsDeleted 分开观测：两者相加才是本周期实际删除的行数。
	RangePruned int
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

	// walPruneApplied 记录各归属**已应用**的 source_wal 剪枝水位：水位未前进即不再重复发
	// 范围删除（幂等跳过）。进程重启后表为空 ⇒ 首次提示会重放一条谓词删除（幂等、一条语句）。
	walPruneApplied map[string]uint64
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
	s := &Store{path: path, db: db, mirror: newMirror(), budget: budget, rowBudget: budget.MaxRows,
		walPruneApplied: make(map[string]uint64)}
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
	return s.ApplyScopedCtx(context.Background(), desired, owners, nil)
}

// ApplyScopedCtx 是 ApplyScoped 的**可取消**形态：ctx 贯穿到每一条语句
// （sql.Conn 的 ExecContext/QueryContext），且在每个提交单元之间校验——取消即停止并回滚当前
// 未提交单元（已提交单元不受影响，镜像与库仍逐单元一致）。
//
// 为什么必须收调用方的 ctx（2026-10-02 事故）：此前这里硬编码 context.Background()，
// 于是启动恢复这类长流程既不可超时也不可中断——调用方（HTTP/RPC/关停）早已放弃，服务端仍在
// 为没人要的结果逐行删库。
// prunes 是**谓词范围删除**提示（可为 nil）：见 WALPrune。
func (s *Store) ApplyScopedCtx(ctx context.Context, desired State, owners []string, prunes []WALPrune) (Stats, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return Stats{}, errors.New("stateindex: 只读库不接受写入")
	}
	started := time.Now()
	stats := Stats{Written: map[string]int{}, Deleted: map[string]int{}, Planned: map[string]int{}}
	if err := ctx.Err(); err != nil {
		return Stats{}, err
	}
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
	//
	// ①水位化差异删除的规划：先做**等价性守门**，通过者用一条谓词删除覆盖整段，未通过的归属
	// 原样走逐行路径（安全方向：宁可慢，不可错删）。
	prunePlans, pruneRows := s.planWALPrunes(desired, prunes)
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
			walPrune := prunePlans[index][owner]
			for key := range mirror {
				if _, ok := seen[key]; ok {
					continue
				}
				if walPrune != nil && walMirrorKeyAtOrBelow(specs[index], key, walPrune.EndThrough) {
					// 该行落在谓词范围内：由一条范围删除覆盖，不再逐行发语句
					// （这正是「与规模成正比的 DELETE 数」变成「每源一条」的地方）。
					continue
				}
				// 归属原样带上：删除后更新镜像时要用它定位分组。
				deletes = append(deletes, pendingRow{table: index, row: rowData{owner: owner, key: key}})
			}
		}
	}
	if len(writes) == 0 && len(deletes) == 0 && len(pruneRows) == 0 {
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
	samples, progress, err := s.commitChunks(ctx, writes, deletes, pruneRows, s.cycle, &stats)
	if err != nil {
		return Stats{}, err
	}
	// 每周期总预算耗尽：如实报告未完成与剩余行数（**不**宣称完成）。剩余部分由调用方下一轮
	// 再调本入口续做；因为镜像已按提交单元更新到与库一致，下一轮重算的差异天然只剩剩余行。
	if progress.remaining() > 0 {
		stats.Incomplete = true
		stats.RemainingRows = progress.remaining()
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
	//
	// 预算耗尽（本周期未完）时不归并：那些页迟早要在收尾时归并一次，提前做只会把本已有限的
	// 周期预算吃在页拷贝上，让「有限的一步」变成「不可预期的一步」。
	if !stats.Incomplete {
		s.checkpointPassiveIfNeeded(ctx)
	}
	stats.Duration = time.Since(started)
	return stats, nil
}

// walPrunePlan 是一个**已通过等价性守门**的范围剪枝（按表下标 + 归属索引）。
type walPrunePlan struct {
	owner      string
	EndThrough uint64
}

// planWALPrunes 规划本轮的 source_wal 范围剪枝，返回「表下标 → 归属 → 计划」与待执行清单。
//
// 等价性守门（**这是范围删除能安全替换逐行删除的全部依据**）：
//   - 水位未前进（≤ 该归属已应用水位）⇒ 跳过：重放没有意义（幂等但白花一条语句）；
//   - 期望集（内存 WAL，权威）里**不存在** `Durable && RecordEnd <= EndThrough` 的行 —— 否则
//     范围删除会把一条仍然有效的行删掉（重启后 WAL 少条 ⇒ 未投递正文丢失）⇒ 该归属整段退回
//     逐行路径。这条与采集侧的剪枝判据同源：`pruneReclaimed(pos)` 之后内存里不可能再有满足
//     `Durable && Record.End <= pos` 的条目，故正常情况下守门必然通过；一旦不通过（例如状态被
//     外部改动/版本错配），我们退化为慢但正确的那条路。
func (s *Store) planWALPrunes(desired State, prunes []WALPrune) (map[int]map[string]*walPrunePlan, []walPrunePlan) {
	if len(prunes) == 0 {
		return nil, nil
	}
	// 期望集里各归属的「最小未耐久末端」：只关心满足谓词的行，故按 (durable, end) 判。
	pending := make(map[string]bool, len(prunes))
	for _, prune := range prunes {
		pending[prune.Owner] = true
	}
	blocked := make(map[string]bool)
	for _, row := range desired.WAL {
		if !pending[row.Key] {
			continue
		}
		if row.Durable && row.RecordEnd <= pruneWatermark(prunes, row.Key) {
			blocked[row.Key] = true
		}
	}
	plans := make(map[int]map[string]*walPrunePlan, 1)
	var rows []walPrunePlan
	for _, prune := range prunes {
		if prune.Owner == "" || prune.EndThrough == 0 || blocked[prune.Owner] {
			continue
		}
		if prune.EndThrough <= s.walPruneApplied[prune.Owner] {
			continue
		}
		plan := &walPrunePlan{owner: prune.Owner, EndThrough: prune.EndThrough}
		if plans[tblSourceWAL] == nil {
			plans[tblSourceWAL] = make(map[string]*walPrunePlan, len(prunes))
		}
		plans[tblSourceWAL][prune.Owner] = plan
		rows = append(rows, *plan)
	}
	return plans, rows
}

// pruneWatermark 取该归属的提示水位（同一归属只应有一条提示；多条时取最小，保守方向）。
func pruneWatermark(prunes []WALPrune, owner string) uint64 {
	best := uint64(0)
	for _, prune := range prunes {
		if prune.Owner != owner {
			continue
		}
		if best == 0 || prune.EndThrough < best {
			best = prune.EndThrough
		}
	}
	return best
}

// walMirrorKeyAtOrBelow 判断一条 source_wal 镜像键的 record_end 是否 ≤ 水位。
// 键的列序即 specs 的 keyCols（见 decodeMirrorKey），末尾即 record_end。
func walMirrorKeyAtOrBelow(spec tableSpec, mirrorKey string, through uint64) bool {
	values, err := decodeMirrorKey(spec, mirrorKey)
	if err != nil || len(values) != len(spec.keyCols) {
		return false // 解不开就按「不覆盖」处理，退回逐行删除（安全方向）
	}
	raw, ok := values[len(values)-1].(int64)
	if !ok {
		return false
	}
	if raw < 0 {
		return false
	}
	return uint64(raw) <= through
}

// applyWALPrune 执行一条谓词范围删除：**一条语句**删掉该归属 `durable=1 && record_end <= 水位`
// 的全部行。返回受影响行数。
//
// 为什么可以省掉 durable 判断之外的任何条件：谓词与内存剪枝同源，且守门已保证期望集里不存在
// 满足谓词的行 ⇒ 被删的每一行都不在期望集里。
func (s *Store) applyWALPrune(ctx context.Context, conn *sql.Conn, plan walPrunePlan, stats *Stats) (int, error) {
	result, err := conn.ExecContext(ctx,
		"DELETE FROM source_wal WHERE key = ? AND durable = 1 AND record_end <= ?", plan.owner, int64(plan.EndThrough))
	if err != nil {
		return 0, fmt.Errorf("stateindex: 范围剪枝 source_wal 失败（归属 %s）: %w", plan.owner, err)
	}
	stats.Statements++
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, nil // 行数不可读不是失败：语句已提交，观测值退化为 0
	}
	return int(affected), nil
}

// refreshWALMirrorWindow 在范围删除提交后刷新该归属「record_end ≤ 水位」那一段的镜像：先把该段
// 现有镜像删掉，再按库内容重建。
//
// 为什么要刷新而不是直接套用范围删除**同一个谓词**：镜像的键里没有 durable（键 = key+seq+
// event_id+record_start+record_end），直接按谓词删镜像会连**未耐久**的行一起从镜像里抹掉，
// 而库里那些行还在 ⇒ 镜像与库分叉（下一步的差异比对会因此错过它们）。读回来重建是唯一不依赖
// 额外状态的做法，且这一次读只覆盖该归属的一段（不是全表）。
//
// 必须走**调用方的连接**（而不是 s.db）：本包是单写者单连接（SetMaxOpenConns(1)），
// 提交单元正占着那条连接，另开一条会把自己锁死。
func (s *Store) refreshWALMirrorWindow(ctx context.Context, conn *sql.Conn, plan walPrunePlan) error {
	spec := specs[tblSourceWAL]
	rows, err := conn.QueryContext(ctx, spec.mirrorSQL+" WHERE key = ? AND record_end <= ?", plan.owner, int64(plan.EndThrough))
	if err != nil {
		return fmt.Errorf("stateindex: 刷新 source_wal 镜像失败（归属 %s）: %w", plan.owner, err)
	}
	defer func() { _ = rows.Close() }()
	type mirrorEntry struct {
		key string
		fp  uint64
	}
	var rebuilt []mirrorEntry
	for rows.Next() {
		columns, err := rows.Columns()
		if err != nil {
			return err
		}
		values, err := scanRow(rows, len(columns))
		if err != nil {
			return err
		}
		// 与 loadMirror 逐字同构：键取前 len(keyCols) 列，fpSelf 表直接读最后一列。
		key := mirrorKey(values[:len(spec.keyCols)]...)
		if values[len(values)-1] == nil {
			return fmt.Errorf("stateindex: %s 指纹列为空", spec.name)
		}
		rebuilt = append(rebuilt, mirrorEntry{key: key, fp: mustFingerprint(values[len(values)-1])})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	table := s.mirrorTable(spec.name)
	owned := table[plan.owner]
	if owned == nil {
		owned = make(map[string]uint64)
		table[plan.owner] = owned
	}
	for key := range owned {
		if walMirrorKeyAtOrBelow(spec, key, plan.EndThrough) {
			delete(owned, key)
		}
	}
	for _, entry := range rebuilt {
		owned[entry.key] = entry.fp
	}
	return nil
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
func (s *Store) commitChunks(ctx context.Context, writes, deletes []pendingRow, prunes []walPrunePlan, cycle uint64, stats *Stats) ([]Sample, commitProgress, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, commitProgress{}, fmt.Errorf("stateindex: 获取写连接失败: %w", err)
	}
	defer func() { _ = conn.Close() }()

	limit := s.budget.hardLimit()
	cycleStarted := time.Now()
	cycleRows := 0
	wi, di := 0, 0
	prunesDone := 0
	var samples []Sample
	// 循环条件必须把范围剪枝算进去：当「差异只剩谓词剪枝」（写/删集为空）时，若漏掉它，
	// 整段剪枝就永远不会被执行——而且是静默的（无错误、无提示）。
	for wi < len(writes) || di < len(deletes) || len(prunes) > 0 {
		// 每周期总预算：它约束的是「一次调用做多少」，与单元预算（约束「一个事务多大」）正交。
		// 检查点在单元之间，故单次调用实际耗时可上浮「一个单元」（其自身由 hardLimit 兜住）。
		//
		// 至少有 completed > 0 才允许因预算停下（见下方 progress 语义）：否则极小预算会把调用
		// 变成永不动作者（每次进来都立刻返回"未完成"），那是死循环而不是有界。
		if (cycleRows >= s.budget.CycleMaxRows || time.Since(cycleStarted) >= s.budget.CycleMaxDuration) && (wi > 0 || di > 0 || prunesDone > 0) {
			return samples, commitProgress{
				writesDone: wi, deletesDone: di, writesTotal: len(writes), deletesTotal: len(deletes),
				prunesDone: prunesDone, prunesTotal: prunesDone + len(prunes),
			}, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, commitProgress{}, err
		}
		chunkStarted := time.Now()
		writtenFrom, deletedFrom := wi, di
		budget := s.rowBudget
		if budget < 1 {
			budget = 1
		}
		// 每周期总预算同时**封顶本单元的行数**：否则一个单元（默认 ≤512 行）就能越过整周期预算，
		// 「一次调用做多少」立刻失真（配置 8 行/周期却一次写了 512 行）。耗时维度仍以单元为粒度，
		// 故单次调用实际耗时可上浮「一个单元」（其自身由 hardLimit 兜住）。
		if remaining := s.budget.CycleMaxRows - cycleRows; remaining > 0 && remaining < budget {
			budget = remaining
		}
		rows := 0
		// 单元内的行数与字数据单独累计：采样是**单元**口径（打点按样本增量结账），
		// 周期的合计在提交成功后并入 stats。
		chunkStats := Stats{Written: map[string]int{}, Deleted: map[string]int{}}
		// 本单元内已执行的范围剪枝：提交后才刷新镜像与推进水位（镜像始终只在提交成功后更新）。
		var prunesApplied []walPrunePlan
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
			// 谓词范围删除：一条语句删掉整段旧行（①水位化）。放在删除阶段最前——它删的是子表
			// source_wal，与逐行删除集不相交（规划期已排除），故与逐行删除的先后无依赖。
			for len(prunes) > 0 {
				prune := prunes[0]
				if limit > 0 && rows > 0 && time.Since(chunkStarted) >= limit {
					return nil // 耗时硬上界：剩余（含范围删除）留给下一个提交单元
				}
				affected, err := s.applyWALPrune(ctx, conn, prune, &chunkStats)
				if err != nil {
					return err
				}
				chunkStats.RangePrunes++
				chunkStats.RangePruned += affected
				stats.RangePrunes++
				stats.RangePruned += affected
				prunes = prunes[1:]
				prunesDone++
				prunesApplied = append(prunesApplied, prune)
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
			return nil, commitProgress{}, err
		}
		// 提交成功后才更新镜像与周期合计：未提交的单元不碰镜像（镜像与库内容因此始终一致）。
		for _, pending := range writes[writtenFrom:wi] {
			s.setMirror(specs[pending.table].name, pending.row.owner, pending.row.key, pending.row.fp)
		}
		for _, pending := range deletes[deletedFrom:di] {
			s.deleteMirror(specs[pending.table].name, pending.row.owner, pending.row.key)
		}
		for _, prune := range prunesApplied {
			// 该段镜像按库内容重建（不能按谓词删：镜像键里没有 durable，会误抹未耐久行）。
			if err := s.refreshWALMirrorWindow(ctx, conn, prune); err != nil {
				// 刷新失败只可能让镜像**偏旧**（多留几行）——下一步的差异比对会把这些行的逐行
				// 删除重算一遍（幂等，无害），而水位不推进则会重放一次范围删除（同样幂等）。
				return nil, commitProgress{}, err
			}
			s.walPruneApplied[prune.owner] = prune.EndThrough
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
		cycleRows += rows
		elapsed := time.Since(chunkStarted)
		samples = append(samples, Sample{
			StartedAt: chunkStarted, Duration: elapsed, CycleID: cycle, Chunks: 1,
			RowsWritten: chunkStats.RowsWritten, RowsDeleted: chunkStats.RowsDeleted,
			BytesWritten: chunkStats.BytesWritten,
		})
		s.adaptBudget(rows, elapsed)
	}
	return samples, commitProgress{
		writesDone: wi, deletesDone: di, writesTotal: len(writes), deletesTotal: len(deletes),
		prunesDone: prunesDone, prunesTotal: prunesDone,
	}, nil
}

// commitProgress 是一次 ApplyScoped 的**进度**（而非「成功/失败」）：提交单元是连续切片，
// 因此「已提交到哪」可用两个下标完全表达。remaining() 为 0 表示本周期差异已全部落库。
type commitProgress struct {
	writesDone   int
	deletesDone  int
	writesTotal  int
	deletesTotal int
	prunesDone   int
	prunesTotal  int
}

func (p commitProgress) remaining() int {
	return (p.writesTotal - p.writesDone) + (p.deletesTotal - p.deletesDone) + (p.prunesTotal - p.prunesDone)
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
