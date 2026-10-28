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
	"sync/atomic"
	"time"

	// 纯 Go 的嵌入式 SQLite 驱动：CGO_ENABLED=0 可直接编译（spec §2.1）。
	_ "github.com/glebarez/go-sqlite"
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

// #67-B：受控 TRUNCATE 的**残余门**与超时 —— 2026-10-05 生产停摆（58min）根因：A′-1 在**持 s.mu 的
// 临界区内**直接对 3.6GB 的 WAL 做 TRUNCATE ⇒ 巨量页拷贝 + 等读者全部发生在锁内 ⇒ 落库/采样/读面
// 一起冻住 ✗✗。三条纠正：**(c) 默认关闭**、**(a) 先 PASSIVE 拷页、只有残余 ≤64MiB 才短截断**、
// **(b) 只在锁外经 TryLock 执行** ✓。
const (
	walTruncateResidualBytes = 64 << 20         // 残余门：仅当 WAL 已小于它才截断（截断阶段极短 ✓）
	walTruncateTimeout       = 30 * time.Second // 截断等待上限：超时只放弃等待，不改数据 ✓
)

// pruneDeleteBatchRows 是「范围谓词删除」与「镜像窗口重建」的**单批行数上界**（#67-A）。
//
// 为什么需要它：这两步都是"与单源数据量成正比的整段操作"，而删除那条语句跑在**提交单元事务内**
// ——该事务的耗时硬上界只在语句之间检查 ⇒ 一条无界 DELETE / 一次整段读回就能把单元耗时推到 10s 级
// （真机 p95=11,095ms、max=40,767ms ✗）。取 2000 是为了让单批在常见磁盘上稳定落在几十毫秒内，
// 同时不至于把批数抬得过高（语句开销约 1ms 量级）。
const pruneDeleteBatchRows = 2000

// walCheckpointPages / sqlitePageSize 决定显式 WAL 归并的触发阈值（页数 × 页大小 ≈ 4 MiB），
// 与 SQLite 默认的 wal_autocheckpoint（1000 页）同量级——但**归并时机由我们自己决定**
// （见 Open 里关闭 wal_autocheckpoint 的理由与 checkpointPassiveIfNeeded 的说明）。
const (
	walCheckpointPages = 1000
	sqlitePageSize     = 4096
)

// DefaultCommitMaxRows / DefaultCommitMinRows / DefaultCommitTarget 是提交单元预算的默认值。
//
// # MaxRows 的重新取值（FR-498 性能根治，2026-10-27）
//
// 原值 512，依据是「512 让 p95 留出 ≈20% 余量」——那是在**开发机空载**下测的（p95≈40.6ms）。
// 生产真机（19 实例、机械盘、load 60–150）实测 p95=81ms、p50=22ms，
// 尾巴是验收线 1.6 倍，原前提被推翻。
//
// 归因（四条探针，均在真实 Store.ApplyScoped 路径上、交替执行排除机器噪声）：
//   - **body 不是主因**：body 从 nil→714B（生产均值）只把 p95 从 19.22ms 抬到 33.33ms；
//     而 714B→4KB（6 倍字节）只再抬 10ms。拆表/BODY 延迟写均被证伪：
//     SQLite 的 UPDATE 无论改哪几列都要重写**整行**，把 body 从 SET 里去掉反而因多一个分支更慢；
//     且同主键行的 body 在三条真实路径上会被改写（回放原位替换 acquire/wal.go:1384、
//     inline↔ref 双向切换 ingest/durable_outbox_test.go:2801-2813、ingest_time 变化），
//     所以「只在首次 INSERT 写 body」会直接破坏崩溃重放语义。
//   - **表规模不是主因**：8192 行/9.2MB 库 p95=49.90ms vs 94044 行/104.6MB 库 p95=41.33ms。
//   - **包装层不是主因**：绕开 stateindex 直接发同形态多值 upsert，p95=75.17ms（比走 Store 还慢）。
//   - **单元行数呈近似线性（本机 load≈83、body 714B、单源 8192 行，四档交替各 80 轮）**：
//     512 行 → 单单元 p95=43.30ms；256 → 27.31ms；128 → 13.96ms；64 → 7.00ms。
//     完成同一份 512 行工作的**周期总耗时**几乎不变（104.8 / 105.9 / 114.4 / 113.3 ms）。
//
// ⇒ 结论：尾巴 = 宿主噪声**按单元总成本放大**后透传到单次提交。噪声的绝对幅度与
// 「一次提交做多少活」成正比，所以把单元做小就是直接压缩尾巴，而总吞吐不付代价
// （周期总耗时不变，只是被切成更多次提交）。
//
// 取 **128**：它给出生产 p95 的估算值 ≈ 81ms × (128/512) ≈ 20ms（本机实测 13.96ms，
// 对 50ms 线有 3.6× 余量）。256 在本机也过线（27.31ms），但生产 p95 是本机的 1.9 倍，
// 512→256 只压缩 2 倍、没有余量吸收生产那种 load 60–150 的尖峰；128 才留出安全阀。
// 不取 64 是因为 p99/max 反而抬升（60 / 122.63ms）：单元越小，固定开销（BEGIN/COMMIT、
// 语句拼接）与噪声命中次数越多，而验收线看 p95——128 是四档里 p95 与 p99 的平衡点。
//
// # 其余旋钮
//
//   - MinRows=64：保证再慢的机器也不退化成逐行（逐行会让语句解析重新成为主导成本）。
//   - Target=40 ms：自适应控制器的收敛点。**必须与 MaxRows 一起读**：控制器只在
//     elapsed > Target 时收缩，所以 Target 就是「允许单元跑多慢」的实际上限——
//     原来 40ms 配合 512 行意味着「允许单单元跑到 40ms」，而验收线是 50ms，只留 25% 余量，
//     一旦碰上宿主尖峰就直接越线。保持 40ms 但把 MaxRows 降到 128 后，
//     「跑满 128 行也只用了目标的一半」，控制器不再长期顶在 MaxRows 收缩下限附近抖动。
//   - MinRows=32：自适应收缩的下限。**从 64 降到 32 是因为 MaxRows 降到了 128**：
//     控制器的最小收缩步是「减半」，若 MinRows 仍为 64 则 128→64 一步就见底，
//     控制器实质上退化为二档开关（128 或 64），失去连续调节能力（见 adaptBudget）。
//     32 行仍远高于「逐行」量级（语句解析不会重新成为主导成本：批量仍 32 行/语句，
//     即 MinRows 下每个单元恰好一条语句），故不会退化。
const (
	DefaultCommitMaxRows = 128
	DefaultCommitMinRows = 32
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
	// Noop 标记这是一个「空转周期」样本：规划后确认零差异，**不开事务、不执行任何语句**。
	//
	// 为什么必须分开（FR-498 真机实测）：空转样本记的 Duration 是整个 ApplyScoped 的墙钟，
	// 包含 scopeState + Validate + planState 的全部规划成本，却一行都没写。把它与提交单元
	// 一起算分位，会让「单次持久化 ≤50ms」这条验收线被不属于它的成本顶穿——
	// 生产实测 p50=2ms 与 p95=107ms 相差两个量级的割裂就是这个原因（每周期规划均值一百多毫秒，
	// 而真正落盘只占其中零头）。验收线约束的是**提交单元**，所以分位只在非空转样本上算。
	Noop bool
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
	// #67-C（只读）：本进程**累计**的"规划耗时 / 镜像重建耗时"（ns）。
	// 为什么必须单列：单元耗时分位只能告诉你"慢在单元内"，分不出"规划（planState 的指纹+主键编码）"
	// 与"镜像重建（无界谓词删除后的镜像修复）"——真机 p95 11s / max 40.7s 的定性就靠这三项 ✓。
	//
	// 原第三项 NsPrune（范围剪枝）已随 source_wal 表一起删除：范围剪枝只服务那张表。
	NsPlan          int64
	NsMirrorRebuild int64
	// #67-A′（只读）：WAL 体量、归并尝试/未拷完/受控截断次数、在飞只读句柄数。
	WALBytes           int64
	CheckpointAttempts int64
	CheckpointBusy     int64
	// 剩余帧与忙碌分别计数；持续时间是首次观察到未拷完以来的时间，不伪称读者真实年龄。
	CheckpointIncomplete         int64
	CheckpointRemainingFrames    int64
	CheckpointIncompleteDuration time.Duration
	WALHighWaterBytes            int64
	CheckpointTruncate           int64
	ReadersInFlight              int64
	// NoopCount / NoopP95 是**空转周期**（零差异、不开事务）的样本数与耗时 P95。
	//
	// 它们**不计入** P50/P95/Max——那三个字段只描述提交单元（验收线约束的对象）。
	// 单列的理由：空转周期的耗时主体是规划（scopeState + Validate + planState），
	// 是真实成本但不属于「单次持久化 ≤50ms」约束的范围；混在一起会让两个不同的东西互相污染。
	NoopCount int
	NoopP95   time.Duration
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
	// delivery_batch，行数随数据量增长的那两张；source_wal 已于 2026-10-29 删除）的规划量与索引总量解耦；
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
	// #67-C（只读观测，均在 ApplyScopedCtxRows 的持锁区内累加 ⇒ 无需原子）：
	// nsPlan/nsMirrorRebuild 是"规划 / 镜像重建"两段各自的累计墙钟（ns）（持锁区内累加 ⇒ 无需原子）。
	// 原 nsPrune（范围剪枝）已删除：它随 source_wal 表一起下线。
	nsPlan          int64
	nsMirrorRebuild int64
	// #67-A′（只读观测，原子 ⇒ 无锁可读）：WAL 归并与"在飞只读句柄"的读数。
	// 真机 `-wal`=3.59GB 与归并阈值 4MiB 并存 ⇒ 只有两种解释：**读者持旧快照（PASSIVE 拷不完，
	// busy≠0）** 或 **归并没被排到执行点**（周期迟迟不完成 ✗）——busy 与 walBytes 并读即可一次分辨 ✓。
	ckptAttempts        atomic.Int64 // 归并尝试次数（PASSIVE + 受控 TRUNCATE）
	ckptBusy            atomic.Int64 // 仅记录返回忙碌的次数，不等同于未完成归并
	ckptIncomplete      atomic.Int64
	ckptRemaining       atomic.Int64
	ckptIncompleteSince atomic.Int64
	ckptWarnAt          atomic.Int64
	walHighWater        atomic.Int64
	ckptLogPages        atomic.Int64 // 最近一次 WAL 页数
	ckptMoved           atomic.Int64 // 最近一次已拷页数
	ckptLastAt          atomic.Int64 // 最近一次尝试时刻（unix nano；0=从未）
	ckptTruncate        atomic.Int64 // 走"受控 TRUNCATE"点的次数
	// #67-B：受控截断**默认关闭**（(c)）；开关与残余门一起才允许截断 ✓。
	controlledTruncate bool
	// readersInFlight 指向**该库路径上**的在飞只读句柄计数（M1，2026-10-12 修复）。
	//
	// 必须是**进程级、按路径共享**的计数器，不能是实例字段：只读句柄由 `OpenReadOnly` 在
	// **另一个 Store 实例**上创建（该函数 `s := &Store{...}` 新建实例后 `Add(1)`），
	// 而截断判据读的是**写入用实例**的字段。用实例字段时写实例恒为 0 ⇒
	// `TryControlledTruncate` 的「无在飞读者才截断」护栏形同不存在，会在导出 JSON /
	// 事件状态查询持长时只读句柄时执行 `wal_checkpoint(TRUNCATE)` 与读者争抢
	// —— 正是 #67-A′/#67-B 要消除的 2026-10-05 停摆机制。
	readersInFlight *atomic.Int64
	// readerReleased 保证 `Close` 对 readersInFlight 只归还一次。
	//
	// 为什么需要：重复 `Close()` 会二次 `Add(-1)`，让计数变成**负数**；而护栏判据是
	// `readersInFlight.Load() != 0` —— 负数同样非零 ⇒ 受控截断被**永久**关闭。
	// 与「漏减」（Add(1) 后失败即返回）同样致命，只是方向相反，故两侧都要配对。
	readerReleased atomic.Bool
	cycle          uint64

	// testCrashHook 仅供测试注入：在一次写入事务**提交之前**、写完全部变更行之后被调用，
	// 用于在子进程中模拟进程异常中断（os.Exit 或 SIGKILL），验证未提交批次不会残留。
	// 生产恒为 nil。
	testCrashHook func()
	// testLoadTableHook 仅供测试在表间提交独立写事务，验证导出不会混合两代。
	testLoadTableHook func(int)
	// testMigrationValidateHook 仅供测试计数，守住续跑不能按单元反复扫描历史的复杂度。
	testMigrationValidateHook func()
	// 以下屏障仅供迁移回归放行独立写者，分别覆盖预检后与提交单元之间的窗口。
	testMigrationAfterCheckHook func()
	testCommitChunkHook         func()
}

// readersInFlightRegistry 是**进程级**的「库路径 → 在飞只读句柄计数」注册表（M1）。
//
// 为什么需要它（而不是实例字段）：只读句柄由 `OpenReadOnly` 在**另一个 Store 实例**上创建，
// 而截断判据（`TryControlledTruncate`）跑在**写入用实例**上。两者不共享实例字段，
// 用实例字段会让写实例的计数恒为 0 ⇒ 护栏失效。按路径共享即可让两侧看到同一个数。
//
// 键用 `path` 原文（不做 filepath.Clean）：`Open` 与 `OpenReadOnly` 的调用方传的是同一字符串
// （见 index_store.go / event_status.go 的构造点），保持原样比较即可；条目常驻（进程内库路径
// 数量有限，且 Store 本身也常驻），不做淘汰以免引入生命周期竞态。
var readersInFlightRegistry sync.Map // map[string]*atomic.Int64

// readersInFlightFor 返回该库路径共享的在飞只读句柄计数器（不存在则创建）。
func readersInFlightFor(path string) *atomic.Int64 {
	if v, ok := readersInFlightRegistry.Load(path); ok {
		return v.(*atomic.Int64)
	}
	v, _ := readersInFlightRegistry.LoadOrStore(path, new(atomic.Int64))
	return v.(*atomic.Int64)
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
		readersInFlight: readersInFlightFor(path)}
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
	// 2026-10-29 遗留迁移：`source_wal` 表已废弃（WAL 已改为纯内存队列，不再落盘），但旧版本
	// 已经在既有生产库里把它建好了（表里可能有 20 万行旧条目）。先于建表执行：把旧表连同它
	// 的索引一起删掉（幂等，不存在则空操作），再让本次的 DDL 落地。
	//
	// 旧条目**不迁移、不读**：它们描述的是内存队列的一个旧快照，重启后已经由
	// rollbackReadToReclaim 按 position 水位重读接替。DROP 后页进入 freelist（文件本身不缩小，
	// 需显式 Vacuum 才回缩），这比一直拖着 300MB 空壳好。
	if dropped, err := dropLegacySourceWAL(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	} else if dropped {
		slog.Info("索引库已删除废弃的 source_wal 表（WAL 改纯内存队列后的遗留迁移）", "path", path,
			"freelistBytesHint", "可择机调用 stateindex.Store.Vacuum 回收磁盘空间")
	}
	for _, ddl := range []string{specDDL, extensionDDL, recoveryAuditDDL, materializationDDL} {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("stateindex: 建表失败: %w", err)
		}
	}
	if err := addColumnIfMissing(ctx, db, "materialization_attempt", "class", "TEXT NOT NULL DEFAULT 'rebuild'"); err != nil {
		_ = db.Close()
		return nil, err
	}
	// FR-502 增量迁移：`instance_binding` 的 rule_key / relative_path 是后加列，同样需要
	// 为既有库幂等补列。旧库旧行零值 = 空规则名与无覆盖 ⇒ 回退 bukkit 规则，与历史行为
	// 逐字一致，故本迁移是纯追加、不改动任何既有实例的采集路径。
	if err := addColumnIfMissing(ctx, db, "instance_binding", "rule_key", "TEXT NOT NULL DEFAULT ''"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := addColumnIfMissing(ctx, db, "instance_binding", "relative_path", "TEXT NOT NULL DEFAULT ''"); err != nil {
		_ = db.Close()
		return nil, err
	}
	// 缺口精确子区间和裁定审计属于同一行，旧库追加列但不凭凸包伪造已丢失的证据。
	for _, column := range []struct{ name, definition string }{
		{"fold_spans", "BLOB"},
		{"fold_span_merges", "INTEGER NOT NULL DEFAULT 0"},
		{"reason_code", "TEXT NOT NULL DEFAULT ''"},
		{"resolved_at_utc", "TEXT NOT NULL DEFAULT ''"},
		{"resolved_by", "TEXT NOT NULL DEFAULT ''"},
		{"fold_spans_unknown", "INTEGER NOT NULL DEFAULT 0"},
		// first_seen_unix：缺口首次登记时间（C 案超时降级需要）。老库为 0 ⇒ 视为已超时。
		{"first_seen_unix", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := addColumnIfMissing(ctx, db, "gap", column.name, column.definition); err != nil {
			_ = db.Close()
			return nil, err
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
	s := &Store{path: path, db: db, readOnly: true, mirror: newMirror(), budget: DefaultCommitBudget(),
		readersInFlight: readersInFlightFor(path)}
	s.rowBudget = s.budget.MaxRows
	if _, err := db.ExecContext(context.Background(), "PRAGMA query_only=ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("stateindex: 设置只读模式失败: %w", err)
	}
	// #67-A′：在飞只读句柄计数（Close 归还）——checkpoint 能否截断取决于它是否为零 ✓。
	//
	// **必须放在全部错误分支之后**：调用方拿到 (nil, err) 后不会再调 Close，若在这一行之前
	// 有失败出口且已 Add(1)，计数就只增不减 ⇒ `TryControlledTruncate` 的
	// `readersInFlight != 0` 护栏被**永久**钉死 ⇒ WAL 只涨不并（与本项要治的形态相反）。
	// 同理 Close 有 s.readOnly 判据保证「只有成功返回的实例才归还」，两侧严格配对。
	s.readersInFlight.Add(1)
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
	if s == nil {
		return nil
	}
	// 一次性归还：计数为负同样满足 `readersInFlight != 0`，护栏会被永久关闭 —— 与「漏减」
	// 同样致命，故用 CAS 保证只归还一次（重复 Close 不再产生副作用）。
	if s.readOnly && s.readerReleased.CompareAndSwap(false, true) {
		s.readersInFlight.Add(-1) // #67-A′：与 OpenReadOnly 的 +1 配对
	}
	if s.db == nil {
		return nil
	}
	if err := s.Checkpoint(); err != nil {
		slog.Warn("索引关闭前归并未完成，已提交内容仍保留在WAL", "error", err)
	}
	return s.db.Close()
}

// Checkpoint 显式把 WAL 内容归并进主库并截断 `-wal`（TRUNCATE 模式）。
//
// 语义与「正常关闭后 WAL 应为空」一致：调用后 `-wal` 归零，重启不会带着巨大的 WAL 启动。
// 它是幂等的：无 WAL 或已归并时是廉价空操作。
func (s *Store) Checkpoint() error {
	if s == nil || s.db == nil || s.readOnly {
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
	s.ckptAttempts.Add(1)
	s.ckptLastAt.Store(time.Now().UnixNano())
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logPages, &movedPages); err != nil {
		return fmt.Errorf("stateindex: WAL 归并失败: %w", err)
	}
	s.ckptLogPages.Store(int64(logPages))
	s.ckptMoved.Store(int64(movedPages))
	s.observeCheckpoint(busy, logPages, movedPages, s.WALBytes())
	if busy != 0 || logPages > movedPages {
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
	walBytes := s.WALBytes()
	if walBytes < int64(walCheckpointPages)*sqlitePageSize {
		return
	}
	s.ckptAttempts.Add(1)
	s.ckptLastAt.Store(time.Now().UnixNano())
	// #67-B（a）+（c）：**先做 PASSIVE 拷页**（不等读者、不阻塞写者 ✓），拷完再看是否值得"短截断"——
	// 只有残余已小（≤64 MiB）且**无在飞只读句柄**、且开关**已显式打开**时才截断 ✓；
	// 3.6GB 级页拷贝绝不在这里做 ✗（2026-10-05 停摆根因 ✓），异常收敛退回"多轮 PASSIVE + 大 WAL 告警"
	// （最坏代价 = WAL 偏大，那是已知形态 ✓，绝不再全停 ✗）。
	var busy, logPages, movedPages int
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &logPages, &movedPages); err != nil {
		slog.Warn("索引 WAL 被动归并未执行（数据仍在 WAL 中且可见，下个周期再试）", "error", err)
		return
	}
	s.ckptLogPages.Store(int64(logPages))
	s.ckptMoved.Store(int64(movedPages))
	// #67-B：**不在锁内截断** —— 本处不执行截断，真正的截断由锁外执行点 `TryControlledTruncate`
	// 承担（它以 TryLock 自证"没有在飞 apply/读"，绝不等待）。
	//
	// **本处不再做任何判定或状态登记**（INGEST-4，2026-10-12 终审清理）：原实现在这里判定
	// 「残余 ≤ 残余门 ⇒ 值得截断」并把结果写进 `truncateWanted` 字段，指望锁外读它；
	// 但 `TryControlledTruncate` **自己重算全部判据**（`walBytes` 门 + `controlledTruncate`
	// + `readersInFlight`），从不读该字段 ⇒ 它是「只写不读」的冗余状态。删除后语义不变，
	// 且不再让读者误以为存在跨阶段的意图传递。
	// 两处判据本就互补而非重复：锁外问「WAL 是否大到值得截断（非空转）」，
	// 而残余门的语义是「WAL 是否已小到可以收尾」——后者由 `TryControlledTruncate`
	// 执行后的 `ckptTruncate` 计数与 `WALBytes()` 读数共同表达，无需本处预登记。
	s.observeCheckpoint(busy, logPages, movedPages, walBytes)
}

// observeCheckpoint 不把被动归并的忙碌列当作完成标记；文件高水位也不等同于待归并内容。
func (s *Store) observeCheckpoint(busy, logPages, movedPages int, walBytes int64) {
	if busy != 0 {
		s.ckptBusy.Add(1)
	}
	for old := s.walHighWater.Load(); walBytes > old; old = s.walHighWater.Load() {
		if s.walHighWater.CompareAndSwap(old, walBytes) {
			break
		}
	}
	remaining := int64(logPages - movedPages)
	if remaining < 0 {
		remaining = 0
	}
	s.ckptRemaining.Store(remaining)
	if remaining == 0 {
		s.ckptIncompleteSince.Store(0)
		return
	}
	s.ckptIncomplete.Add(1)
	now := time.Now()
	s.ckptIncompleteSince.CompareAndSwap(0, now.UnixNano())
	last := s.ckptWarnAt.Load()
	if (last == 0 || now.Sub(time.Unix(0, last)) >= time.Minute) && s.ckptWarnAt.CompareAndSwap(last, now.UnixNano()) {
		slog.Warn("索引 WAL 被动归并未完成，保留剩余帧并延后重试",
			"busy", busy, "walBytes", walBytes, "log", logPages, "moved", movedPages,
			"remainingFrames", remaining, "incompleteDuration", now.Sub(time.Unix(0, s.ckptIncompleteSince.Load())),
			"readersInFlight", s.readersInFlight.Load())
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
// 带 fp 列的表（source_aux）直接读 fp，因此不需要为其余内容列现算指纹。
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
				// 带 fp 列的表（source_aux）直接读最后一列。
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
// source_aux/gap/delivery_batch 都以日志源为归属，instance_binding 以实例 UUID 为
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

// rollbackReadTransaction 兼容取消时驱动已回滚的正常情况，真正失败必须留下诊断。
func rollbackReadTransaction(tx *sql.Tx) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		slog.Warn("索引只读事务释放失败", "error", err)
	}
}

// Load 读出索引全部内容（行序见各表 orderBy）。
func (s *Store) Load() (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load(context.Background())
}

func (s *Store) load(ctx context.Context) (State, error) {
	// 固定首表查询建立的 WAL 快照；本句柄互斥锁不能阻止独立连接提交新一代。
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return State{}, fmt.Errorf("stateindex: 开启只读事务失败: %w", err)
	}
	defer rollbackReadTransaction(tx)
	var st State
	for index := range specs {
		spec := specs[index]
		columns := strings.Join(loadColumns[index], ", ")
		rows, err := tx.QueryContext(ctx, "SELECT "+columns+" FROM "+spec.name+" ORDER BY "+spec.orderBy)
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
		if s.testLoadTableHook != nil {
			s.testLoadTableHook(index)
		}
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
	return s.ApplyScopedCtx(context.Background(), desired, owners)
}

// ApplyScopedCtx 是 ApplyScoped 的**可取消**形态：ctx 贯穿到每一条语句
// （sql.Conn 的 ExecContext/QueryContext），且在每个提交单元之间校验——取消即停止并回滚当前
// 未提交单元（已提交单元不受影响，镜像与库仍逐单元一致）。
//
// 为什么必须收调用方的 ctx（2026-10-02 事故）：此前这里硬编码 context.Background()，
// 于是启动恢复这类长流程既不可超时也不可中断——调用方（HTTP/RPC/关停）早已放弃，服务端仍在
// 为没人要的结果逐行删库。
func (s *Store) ApplyScopedCtx(ctx context.Context, desired State, owners []string) (Stats, error) {
	return s.ApplyScopedCtxRows(ctx, desired, owners, 0)
}

// ApplyScopedCtxRows 是 ApplyScopedCtx 的「本次调用行数上限」覆盖形态（cycleRows>0 时生效）。
//
// 为什么需要（2026-10-03 现场二度复现 + `-race` 实测）：持久化门是**单持有者**，一个提交单元
// 的耗时就是其他调用者的等待上界。背景链（恢复/重放/交办的落库）应当用小步长，好让有硬截止的
// 调用者（实例登记 RPC、Stop）在一个小步之内拿到门；而单次调用自身的总预算仍由 CycleMaxRows
// 约束（两者是**与**关系）。
func (s *Store) ApplyScopedCtxRows(ctx context.Context, desired State, owners []string, cycleRows int) (Stats, error) {
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
	if cycleRows > 0 {
		// 只覆盖「本次调用的行数上界」，不动配置面（配置面仍由 CommitBudget 表达）。
		//
		// **必须先存原值再赋值**（M2，2026-10-12 修复）：此前写成
		// `s.budget.CycleMaxRows = cycleRows; defer func(original int){...}(s.budget.CycleMaxRows)`，
		// 而 Go 在 **defer 语句执行时**求值实参 —— 此时字段刚被改成 cycleRows，
		// 于是 `original` 等于 cycleRows，恢复成空操作 ⇒ 该 Store 实例的周期预算被**永久钉死**在
		// 调用方传的这个小值上，后续所有落库步长退化为极小值（每周期只推 N 行）。
		// 配合「未完成不推进水位」的表现是持续低吞吐而非报错：静默性能退化，且首启正常、
		// 表现滞后（与"二启才死"那类难查形态同构）。
		original := s.budget.CycleMaxRows
		s.budget.CycleMaxRows = cycleRows
		defer func() { s.budget.CycleMaxRows = original }()
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
	tPlan := time.Now()
	plans, err := planState(planned, started.UnixMilli())
	s.nsPlan += int64(time.Since(tPlan)) // #67-C：规划段（scopeState + Validate + planState）
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
			Noop: true,
		})
		return stats, nil
	}
	// 删除按表倒序（子表在前），满足外键约束；写入按 writeOrderRank 排序（父表在前、
	// 水位表 position 最后——为什么 position 必须最后见 ApplyScoped 的崩后语义论证）。
	sort.SliceStable(deletes, func(i, j int) bool { return deletes[i].table > deletes[j].table })
	sort.SliceStable(writes, func(i, j int) bool { return writeOrderRank[writes[i].table] < writeOrderRank[writes[j].table] })

	s.cycle++
	samples, progress, err := s.commitChunks(ctx, writes, deletes, s.cycle, &stats, nil, nil)
	if err != nil {
		return Stats{}, err
	}
	// 每周期总预算耗尽：如实报告未完成与剩余行数（**不**宣称完成）。剩余部分由调用方下一轮
	// 再调本入口续做；因为镜像已按提交单元更新到与库一致，下一轮重算的差异天然只剩剩余行。
	if progress.remaining() > 0 {
		// #67-A′：**把 WAL 归并从"周期完成"上解耦** —— 周期因预算/大单元迟迟不完成时，原来连
		// 归并都排不上（真机 `-wal` 3.59GB ✗）⇒ 在**未完成**这条出口也尝试一次（阈值门控 + 无读者
		// 才受控截断 ✓）。放在此处而不是"环的 tick"：本函数正是 WAL 增长的唯一来源路径
		// （每次提交都在这里），且此处已持锁 ⇒ 无需新plumbing、不引入新的并发面 ✓。
		s.checkpointPassiveIfNeeded(ctx)
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

// SetControlledTruncate 开关"受控 TRUNCATE"（#67-B（c）：**默认关闭** ✓，配置面显式打开才生效）。
func (s *Store) SetControlledTruncate(enabled bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.controlledTruncate = enabled
	s.mu.Unlock()
}

// TryControlledTruncate 是"受控 TRUNCATE"的**锁外执行点**（#67-B（b））：
// 只有 `s.mu.TryLock()` 成功（证明没有在飞 apply/读 ✓）、无在飞只读句柄、残余 ≤ 门限时才截断 ✓；
// 带**超时**（超时只放弃等待、不改数据 ✓）。返回是否真的发出了截断。
//
// 为什么必须锁外：TRUNCATE 会等读者、并做页拷贝 —— 在持 s.mu 时做，会把落库/采样/读面一起冻住 ✗✗
// （2026-10-05 生产 58min 停摆的机制本体 ✓）。
func (s *Store) TryControlledTruncate(ctx context.Context) bool {
	if s == nil || s.readOnly || s.db == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	walBytes := s.WALBytes() // 读文件大小（不取锁 ⇒ 可安全地在 TryLock 之前调用 ✓）
	if walBytes < int64(walCheckpointPages)*sqlitePageSize || walBytes >= walTruncateResidualBytes {
		return false
	}
	if !s.mu.TryLock() {
		return false // 有在飞 apply/读 ⇒ 本轮让路（下一轮再来 ✓，绝不等待 ✗）
	}
	defer s.mu.Unlock()
	if !s.controlledTruncate || s.readersInFlight.Load() != 0 {
		return false
	}
	s.ckptAttempts.Add(1)
	s.ckptLastAt.Store(time.Now().UnixNano())
	tctx, cancel := context.WithTimeout(ctx, walTruncateTimeout)
	defer cancel()
	var busy, logPages, movedPages int
	if err := s.db.QueryRowContext(tctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logPages, &movedPages); err != nil {
		slog.Warn("索引 WAL 受控截断未完成（超时/忙碌；数据仍在 WAL 中且可见，下轮再试）", "error", err)
		return false
	}
	s.ckptLogPages.Store(int64(logPages))
	s.ckptMoved.Store(int64(movedPages))
	s.observeCheckpoint(busy, logPages, movedPages, s.WALBytes())
	if busy != 0 || logPages > movedPages {
		return false
	}
	if s.ckptTruncate.Add(1) == 1 {
		slog.Warn("索引 WAL 受控截断首次生效（#67-B：锁外执行 ✓）", "walBytes", walBytes)
	}
	return true
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
	order := []int{tblSource, tblGap, tblProjection, tblInstanceBinding, tblSourceAux, tblDeliveryBatch, tblPosition}
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
func (s *Store) commitChunks(ctx context.Context, writes, deletes []pendingRow, cycle uint64, stats *Stats, unitGuard func(*sql.Conn) error, progressInUnit func(*sql.Conn, int, int) error) ([]Sample, commitProgress, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, commitProgress{}, fmt.Errorf("stateindex: 获取写连接失败: %w", err)
	}
	defer func() { _ = conn.Close() }()

	limit := s.budget.hardLimit()
	cycleStarted := time.Now()
	cycleRows := 0
	wi, di := 0, 0
	var samples []Sample
	for wi < len(writes) || di < len(deletes) {
		// 每周期总预算：它约束的是「一次调用做多少」，与单元预算（约束「一个事务多大」）正交。
		// 检查点在单元之间，故单次调用实际耗时可上浮「一个单元」（其自身由 hardLimit 兜住）。
		//
		// 至少有 completed > 0 才允许因预算停下（见下方 progress 语义）：否则极小预算会把调用
		// 变成永不动作者（每次进来都立刻返回"未完成"），那是死循环而不是有界。
		if (cycleRows >= s.budget.CycleMaxRows || time.Since(cycleStarted) >= s.budget.CycleMaxDuration) && (cycleRows > 0 || wi > 0 || di > 0) {
			return samples, commitProgress{
				writesDone: wi, deletesDone: di, writesTotal: len(writes), deletesTotal: len(deletes),
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
		// 每周期总预算同时**封顶本单元的行数**：否则一个单元（默认 ≤DefaultCommitMaxRows 行）
		// 就能越过整周期预算，「一次调用做多少」立刻失真（配置 8 行/周期却一次写了满预算行）。
		// 耗时维度仍以单元为粒度，
		// 故单次调用实际耗时可上浮「一个单元」（其自身由 hardLimit 兜住）。
		if remaining := s.budget.CycleMaxRows - cycleRows; remaining > 0 && remaining < budget {
			budget = remaining
		}
		rows := 0
		// 单元内的行数与字数据单独累计：采样是**单元**口径（打点按样本增量结账），
		// 周期的合计在提交成功后并入 stats。
		chunkStats := Stats{Written: map[string]int{}, Deleted: map[string]int{}}
		// 整个单元（写 + 删 + 提交）都在同一个 IMMEDIATE 事务内：BEGIN 在写入之前，
		// 否则语句会各自 autocommit——那正是「崩溃不丢」回归要抓的形态。
		if err := s.runUnit(ctx, conn, func() error {
			// BEGIN IMMEDIATE 已排除独立写者；在任何业务写入前拒绝已失效的迁移验真版本。
			if unitGuard != nil {
				if err := unitGuard(conn); err != nil {
					return err
				}
			}
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
			// 迁移凭据与本单元的数据一起提交，不能在 COMMIT 后另行记进度。
			if progressInUnit != nil {
				return progressInUnit(conn, writtenFrom, wi)
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
		actualRows := chunkStats.RowsWritten + chunkStats.RowsDeleted
		if actualRows > stats.MaxChunkRows {
			stats.MaxChunkRows = actualRows
		}
		cycleRows += rows
		elapsed := time.Since(chunkStarted)
		samples = append(samples, Sample{
			StartedAt: chunkStarted, Duration: elapsed, CycleID: cycle, Chunks: 1,
			RowsWritten: chunkStats.RowsWritten, RowsDeleted: chunkStats.RowsDeleted,
			BytesWritten: chunkStats.BytesWritten,
		})
		s.adaptBudget(rows, elapsed)
		if s.testCommitChunkHook != nil {
			s.testCommitChunkHook()
		}
	}
	return samples, commitProgress{
		writesDone: wi, deletesDone: di, writesTotal: len(writes), deletesTotal: len(deletes),
	}, nil
}

// commitProgress 是一次 ApplyScoped 的**进度**（而非「成功/失败」）：提交单元是连续切片，
// 因此「已提交到哪」可用两个下标完全表达。remaining() 为 0 表示本周期差异已全部落库。
type commitProgress struct {
	writesDone   int
	deletesDone  int
	writesTotal  int
	deletesTotal int
}

func (p commitProgress) remaining() int {
	return (p.writesTotal - p.writesDone) + (p.deletesTotal - p.deletesDone)
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
			// 调用方取消不能取消清理，否则同一连接可能遗留未结束事务。
			if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
				slog.Warn("索引提交单元回滚失败，后续操作须重新核验", "error", err)
			}
		}
	}()
	if err := body(); err != nil {
		return err
	}
	if s.testCrashHook != nil {
		// 提交前的注入点：模拟进程被强杀（未提交事务必须整体丢弃）。
		s.testCrashHook()
	}
	if err := ctx.Err(); err != nil {
		return err
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
//
// # 两个必须守住的边界（FR-498，2026-10-27）
//
//   - 下限夹取用 **min(MinRows, MaxRows)** 而不是直接 MinRows。这是**防御性**的：
//     `CommitBudget.Normalized` 已保证 MinRows ≤ MaxRows，故合法配置下两写法等价；
//     但它让“超目标 ⇒ 下次行数不多于本次”这条不变量对**任意输入**都成立
//     （例如诊断代码直接改 `s.budget`），不会因夹取顺序把“收缩”退化成“抬回 MaxRows”。
//     对应守卫：budget_adapt_test.go 的 TestAdaptBudgetNeverGrowsAfterOverrun。
//   - 扩张步受 MaxRows 约束，收缩步不低于下限。
//
// # 已知行为：20–40ms 是死区（不是 bug，但阅读数时必须知道）
//
// 控制器只在 elapsed > Target(40ms) 时收缩、在 elapsed < Target/2(20ms) 且用满预算时扩张。
// 所以单元耗时落在 [20ms, 40ms] 时**既不收缩也不扩张**，预算停在“上一次动作后的值”。
// 这意味着生产真机的 p50=22ms（落在死区）**不足以**推出“预算稳定顶在 MaxRows”——
// 真实形态更可能是“开库 512 → 某个 >40ms 的单元把它砍到 ~250 → 之后 22ms 让它停在那里”。
// 想确认实时预算，请看 `Samples()` 里每条 Sample.Rows() 的分布（那才是单元实际行数），
// 而不是 `Latency.MaxRows`（那是环内单单元最大行数，不是当前预算）。
func (s *Store) adaptBudget(rows int, elapsed time.Duration) {
	target := s.budget.Target
	if rows <= 0 || elapsed <= 0 || target <= 0 {
		return
	}
	floor := s.budget.MinRows
	if floor > s.budget.MaxRows {
		floor = s.budget.MaxRows
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
	if next < floor {
		next = floor
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
		result, err := conn.ExecContext(ctx, statement, args...)
		if err != nil {
			return fmt.Errorf("stateindex: 批量删除 %s 旧行失败: %w", spec.name, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		stats.Statements++
		stats.RowsDeleted += int(affected)
		stats.Deleted[spec.name] += int(affected)
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
			result, err := conn.ExecContext(ctx, spec.upsert, values...)
			if err != nil {
				return fmt.Errorf("stateindex: 写入 %s 失败: %w", spec.name, err)
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			stats.Statements++
			stats.RowsWritten += int(affected)
			stats.Written[spec.name] += int(affected)
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
		result, err := conn.ExecContext(ctx, statement, args...)
		if err != nil {
			return fmt.Errorf("stateindex: 批量写入 %s 失败: %w", spec.name, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		stats.Statements++
		stats.RowsWritten += int(affected)
		stats.Written[spec.name] += int(affected)
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
	parsed, err := asInt64(value)
	if err != nil {
		slog.Warn("索引辅助整数无法读取，保留既有零值回退", "error", err)
		return 0
	}
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

// dropLegacySourceWAL 幂等地删除已废弃的 `source_wal` 表（既有库兼容迁移），返回是否真的删了。
//
// 背景（2026-10-29）：WAL 改为**纯内存队列**（对齐 Filebeat `queue.mem` + registry 语义），
// 队列内容不再跨重启恢复，可靠性改由「持久化的 position 水位 + 重启重读」承担（见
// ingest/wal_reboot_replay.go 的 rollbackReadToReclaim）。于是这张表既没有读者也没有写者，
// 只剩磁盘占用：生产实测 263MB body + 41MB 主键索引 = 索引库的 88%。
//
// 为什么必须在 Open 里做：`CREATE TABLE IF NOT EXISTS` 只建不删，而旧版本的 DDL 已经把它建
// 在磁盘上了；不显式 DROP，既有生产库会永远拖着这 300MB 的空壳（表里现存的行也永远没人读）。
// DROP TABLE 同时会连带删掉它上面的索引（含旧版本遗留的 idx_source_wal_key），
// 因此不再需要单独的 DROP INDEX 维护项。
//
// 成本：DROP TABLE 是一次 schema 变更 + 把表占的页回收进 freelist，不重写其余表；
// 20 万行级别实测在毫秒量级。它不在任何临界区内（Open 阶段、业务写入之前）。
func dropLegacySourceWAL(ctx context.Context, db *sql.DB) (bool, error) {
	var exists bool
	if err := db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='source_wal')").Scan(&exists); err != nil {
		return false, fmt.Errorf("stateindex: 检查遗留 source_wal 表失败: %w", err)
	}
	if !exists {
		return false, nil
	}
	if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS source_wal"); err != nil {
		return false, fmt.Errorf("stateindex: 删除遗留 source_wal 表失败: %w", err)
	}
	return true, nil
}

// Vacuum 执行一次 VACUUM，把空闲页归还文件系统（库文件真正变小）。
//
// 为什么**不**在 Open（启动路径）上自动跑：VACUUM 会对整个库加写锁并重建全库，
// 临时需要约 2 倍磁盘空间，且耗时与库体积成正比——放在启动路径上就是一次无上限的停摆
// （与历史事故“启动路径单线程、与规模成正比的持久化”同型）。
//
// 那 DROP TABLE 之后磁盘会不小？准确说是“回收进 freelist，文件不变小”：
//   - SQLite 删表后把页加入 freelist，后续写入优先复用这些页 ⇒ 不会再因旧数据继续长；
//   - 但文件不会自动回缩（SQLite 没有自动收缩机制），所以 263MB 会“白占”到下次显式 VACUUM。
//
// 因此本条只提供**显式入口**：由运维在停机/低峰时调用一次，或由离线维护命令驱动。
// 判据不是“总是做”，而是“确实有大块空闲页”（freelist_count × 页大小）。
func (s *Store) Vacuum(ctx context.Context) error {
	if s == nil || s.db == nil || s.readOnly {
		return errors.New("stateindex: 只读句柄不接受 VACUUM")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx, "VACUUM"); err != nil {
		return fmt.Errorf("stateindex: VACUUM 失败: %w", err)
	}
	return nil
}

// FreelistBytes 返回空闲页占用量（估计值：freelist_count × 页大小），用于判断是否值得 VACUUM。
func (s *Store) FreelistBytes(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var pages, pageSize int64
	if err := s.db.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&pages); err != nil {
		return 0, fmt.Errorf("stateindex: 读取 freelist_count 失败: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return 0, fmt.Errorf("stateindex: 读取 page_size 失败: %w", err)
	}
	return pages * pageSize, nil
}

// addColumnIfMissing 按 PRAGMA table_info 幂等补列：SQLite 的 DDL 没有 ADD COLUMN IF NOT
// EXISTS，而本仓库的建表用的是 CREATE TABLE IF NOT EXISTS ⇒ 后加列对既有库必须显式补。
//
// 为什么不用「直接 ALTER 并忽略 duplicate column 错误」：那会把「列已存在」与「真错误」
// 混为一谈，且错误串随 SQLite 版本漂移。查 table_info 是显式、可读、可测的判据。
func addColumnIfMissing(ctx context.Context, db *sql.DB, table, column, def string) error {
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return fmt.Errorf("stateindex: 读取 %s 列清单失败: %w", table, err)
	}
	found := false
	for rows.Next() {
		var (
			cid       int
			name      string
			colType   string
			notNull   int
			dfltValue any
			pk        int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			_ = rows.Close()
			return fmt.Errorf("stateindex: 解析 %s 列清单失败: %w", table, err)
		}
		if name == column {
			found = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("stateindex: 遍历 %s 列清单失败: %w", table, err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("stateindex: 关闭 %s 列清单失败: %w", table, err)
	}
	if found {
		return nil
	}
	// 表刚由 CREATE TABLE IF NOT EXISTS 建出来时不会走到这里（列已在 DDL 里）；走到这里
	// 只可能是「既有旧库缺列」，补列后旧行零值即默认值，语义等价。
	if _, err := db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, def)); err != nil {
		return fmt.Errorf("stateindex: 为 %s 补列 %s 失败: %w", table, column, err)
	}
	return nil
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

// textAt 按下标取文本，越界返回空串。
//
// 用途：FR-502 增量列（rule_key/relative_path）在**旧库首次打开、补列尚未生效**
// 的窗口内，载入的行可能仍只有历史列；此时按缺失处理（空串）而非 panic。
func textAt(values []any, idx int) string {
	if idx < 0 || idx >= len(values) {
		return ""
	}
	return asText(values[idx])
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

// bytesAt 是 asBytes 的越界安全版：下标超出时返回 nil。
// 用于后加的列（旧库升级后旧行本列为 NULL，且某些读路径的列集可能不含它）。
func bytesAt(values []any, idx int) []byte {
	if idx < 0 || idx >= len(values) {
		return nil
	}
	return asBytes(values[idx])
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
			Resolved: asBool(values[6]), Resolution: asText(values[7]), FoldSpans: asBytes(values[8]),
			FoldSpanMerges: int(intValue(values[9])), ReasonCode: asText(values[10]),
			ResolvedAtUTC: asText(values[11]), ResolvedBy: asText(values[12]), FoldSpansUnknown: asBool(values[13])}
		row.FirstSeenUnix = intValue(values[14])
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
			RuleKey: textAt(values, 5), RelativePath: textAt(values, 6),
		})
	case "source_aux":
		st.Aux = append(st.Aux, AuxRow{Key: asText(values[0]), Config: asBytes(values[1]), Payload: asBytes(values[2])})
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
func (s *Store) Latency() (result Latency) {
	defer func() {
		result.CheckpointIncomplete = s.ckptIncomplete.Load()
		result.CheckpointRemainingFrames = s.ckptRemaining.Load()
		if since := s.ckptIncompleteSince.Load(); since != 0 {
			result.CheckpointIncompleteDuration = time.Since(time.Unix(0, since))
		}
		result.WALHighWaterBytes = s.walHighWater.Load()
	}()
	s.mu.Lock()
	samples := append([]Sample(nil), s.samples...)
	// INGEST-3 修复：三个累计字段在持锁区内读——写入侧（ApplyScopedCtxRows）在 s.mu 内累加，
	// 读写两侧不同锁 ⇒ -race 下构成数据竞争。
	nsPlan := s.nsPlan
	nsMirrorRebuild := s.nsMirrorRebuild
	s.mu.Unlock()
	if len(samples) == 0 {
		// #67-C：无样本也要带上累计读数（否则"一个单元都没采到"时三段耗时全不可见 ✗）。
		return Latency{
			NsPlan: nsPlan, NsMirrorRebuild: nsMirrorRebuild,
			WALBytes: s.WALBytes(), CheckpointAttempts: s.ckptAttempts.Load(),
			CheckpointBusy: s.ckptBusy.Load(), CheckpointTruncate: s.ckptTruncate.Load(),
			ReadersInFlight: s.readersInFlight.Load(),
		}
	}
	durations := make([]time.Duration, 0, len(samples))
	var noopDurations []time.Duration
	var max time.Duration
	cycles := make(map[uint64]struct{}, len(samples))
	result = Latency{}
	for _, sample := range samples {
		// 空转样本（零行、零语句）不计入**提交单元**分位：它的 Duration 是整周期的墙钟，
		// 主体是规划而非落盘。拿它算 p95 等于让规划成本冒充持久化耗时。
		// 单列 NoopCount / NoopP95 保留可观测性（真要看「空转周期有多重」时用）。
		if sample.Noop {
			result.NoopCount++
			noopDurations = append(noopDurations, sample.Duration)
			cycles[sample.CycleID] = struct{}{}
			continue
		}
		durations = append(durations, sample.Duration)
		if sample.Duration > max {
			max = sample.Duration
		}
		if rows := sample.Rows(); rows > result.MaxRows {
			result.MaxRows = rows
		}
		cycles[sample.CycleID] = struct{}{}
	}
	sort.Slice(noopDurations, func(i, j int) bool { return noopDurations[i] < noopDurations[j] })
	result.NoopP95 = percentile(noopDurations, 0.95)
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	// Count 是**提交单元**个数（空转周期不计），与 P50/P95/Max 同一分母；各自的口径必须自洽，
	// 否则「Count=335 但 durations 只有 40 个」会让人以为分位算错了。
	result.Count = len(durations)
	result.Cycles = len(cycles)
	result.P50 = percentile(durations, 0.50)
	result.P95 = percentile(durations, 0.95)
	result.Max = max
	result.NsPlan = nsPlan
	result.NsMirrorRebuild = nsMirrorRebuild
	result.WALBytes = s.WALBytes()
	result.CheckpointAttempts = s.ckptAttempts.Load()
	result.CheckpointBusy = s.ckptBusy.Load()
	result.CheckpointTruncate = s.ckptTruncate.Load()
	result.ReadersInFlight = s.readersInFlight.Load()
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
