// Package ingest wires the durable acquisition pipeline into a Worker process.
// It intentionally keeps source state and projection state under the Worker data
// root; CP only sees the resulting Catalog/Query View contract.
package ingest

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/archive"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/eventstore"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/normalize"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// SourceConfig describes one persistent Worker log source.
type SourceConfig struct {
	LogSourceID      string               `json:"log_source_id" mapstructure:"log_source_id"`
	SourceGeneration string               `json:"source_generation" mapstructure:"source_generation"`
	Path             string               `json:"path" mapstructure:"path"`
	Mode             pipeline.AcquireMode `json:"mode" mapstructure:"mode"`
	RotateTo         string               `json:"rotate_to,omitempty" mapstructure:"rotate_to"`
	ArchiveGlob      string               `json:"archive_glob,omitempty" mapstructure:"archive_glob"`
	Stream           string               `json:"stream,omitempty" mapstructure:"stream"`
	SourceCategory   logtypes.Source      `json:"source_category,omitempty" mapstructure:"source_category"`
	StorageNamespace string               `json:"storage_namespace,omitempty" mapstructure:"storage_namespace"`
	UTCDay           string               `json:"utc_day,omitempty" mapstructure:"utc_day"`
	// Charset 源正文的字符集（auto/utf-8/gbk/gb18030）；空串表示跟随节点默认或 auto。
	// 中文 locale 的 JVM 会把 Java 日志写成 GBK，必须按源声明，不能靠猜（缺陷 B）。
	Charset string `json:"charset,omitempty" mapstructure:"charset"`
	// TimeZone 解释日志行内 [HH:MM:SS] 所用的时区（缺陷 C）：空串 = UTC（保持既有行为），
	// "local" = 跟随节点本地时区，其余按 IANA 名解析（如 Asia/Hong_Kong）。
	// 中文 locale 的 JVM 按本地时区写日志，若按 UTC 解释会整体偏移（现场实测 +8 小时）。
	TimeZone string `json:"time_zone,omitempty" mapstructure:"time_zone"`
}

type persistedSource struct {
	PublicationPending bool               `json:"publication_pending,omitempty"`
	Ledger             []ledger.Entry     `json:"ledger"`
	WAL                []acquire.WALEntry `json:"wal"`
	// WALRefs 是 WAL 条目的引用形式（B1a）：正文已在事件段存储中的条目只写引用，加载时
	// 按 EventID 水合。与 WAL **并存**（按条切分），二者互不覆盖、可分别存在。
	WALRefs []acquire.WALRef `json:"wal_refs,omitempty"`
	// EventsStoredThrough 是事件段存储已覆盖的最大 record_end（B1a 的**按条**判据来源）：
	// record_end <= 该值的 WAL 条目，其正文已 durable 在段存储中，持久化时只需引用。
	// 为什么不能按源级 EventsStored 一刀切：事件体是在投递路径（appendEvents）才写入段存储，
	// 未投递条目的正文尚未入库——按源切分会丢正文（曾由既有测试抓到，见方案文件）。
	EventsStoredThrough  uint64 `json:"events_stored_through,omitempty"`
	ProjectionGeneration string `json:"projection_generation"`
	// EventsStored 表示该源的 canonical 事件体已落在磁盘段（events/ 目录），不在本文件。
	EventsStored bool `json:"events_stored,omitempty"`
	// Events 仅在旧格式或段写入失败时使用；正常情况为空（权威副本在 eventstore）。
	Events []logtypes.Event `json:"events,omitempty"`
	// VerifiedRuns 是该源**写 VL 后逐字段可见性校验通过**的源位置连续区间（P1-2 的区间级凭据）。
	//
	// 为什么必须持久化：WAL 条目一旦被回收，就不可能再通过重投生成覆盖证据；此时「已发布投影」
	// 只能给出一个封闭水位（末端语义），无法证明缺口区间本身被逐字段校验覆盖过。区间级凭据
	// 让「已发布投影证据」自动消解出口有据可依，而不是按末端水位把中间的空洞一并宣称已落库。
	//
	// 有界性：只保留最近的 maxVerifiedRunsPerSource 段（相邻写入会就地合并，稳态下通常只有 1 段）。
	VerifiedRuns []ledger.PositionRange `json:"verified_runs,omitempty"`
}

type persistedState struct {
	Sources       map[string]persistedSource `json:"sources"`
	SourceConfigs map[string]SourceConfig    `json:"source_configs,omitempty"`
	Instances     map[string]InstanceBinding `json:"instances,omitempty"`
}

// pendingSpoolRoot 是未绑定实例输出的持久化暂存目录。
const pendingSpoolRoot = "pending"

// Manager owns configured source pipelines and their durable state.
type Manager struct {
	// defaultCharset 是节点级默认字符集（源未显式配置时生效）；空串等价于 auto。
	defaultCharset string
	// defaultTimeZone 是节点级默认时区名（源未显式配置时生效）；空串等价于 UTC。
	defaultTimeZone string
	mu              sync.Mutex
	cycleMu         sync.Mutex
	// registerMu 串行化「实例登记 / 采集绑定」自身，使这条路径**永不等待采集轮的长临界区**。
	//
	// 为什么要与 cycleMu 分离（2026-10-01 生产事故根因）：cycleMu 的持有者是「整轮采集」
	// （pollOnce：逐源 Poll → WAL → persist → VL 投递 → 投影校验，单轮在饱和期可达分钟级）
	// 与「预备切换 / 停止」（全管道 Flush + persist）。登记路径此前也取 cycleMu，于是
	// 「CP 启动实例 → Worker CreateInstance → 登记实例日志采集」这一步被整轮采集挡住，
	// 在 CP 的 10 秒 RPC 截止时间内必然 DeadlineExceeded（07:39/07:46/07:49/08:44 四次全中）。
	//
	// 登记为什么不需要 cycleMu 的互斥：登记只做三件事——读/写 m.state.Instances、按源键
	// 幂等登记管道（m.mu 内建表）、persist（m.mu 内整段临界区）。三者都在 m.mu 保护下，
	// 而 pollOnce 对本轮管道集合的取用是「m.mu 下快照、快照外轮询」，新增管道最多延到
	// 下一轮参与采集（首轮无数据，无正确性损失）。故两者之间不需要额外互斥。
	//
	// 锁序（任何路径都不得逆序）：cycleMu → registerMu → pendingMu → mu。
	// registerMu 永远不等待 cycleMu（否则本锁失去意义），Stop 则以 cycleMu → registerMu
	// 的顺序把「关闭采集索引」与登记串行开，避免登记在 Stop 之后重新打开索引句柄。
	registerMu sync.Mutex
	// pendingMu 串行化暂存写入与注册接管，避免绑定切换时发生乱序或重复回放。
	pendingMu sync.Mutex
	// resolveGapsBudget 是整节点解算（ResolveCoveredGaps）单次调用的预算（源数 + 墙钟）。
	//
	// 为什么需要（2026-10-02 压测现场实证）：解算此前对**全部源**做投影查询与账本变更且全程
	// 持 cycleMu，一次调用就让整节点采集停摆（read_pos 冻结 30 分钟、source_wal 卡 560 万行、
	// 索引 WAL 涨到 10.2 GB，HTTP 300s 超时后服务端仍继续持锁跑完）。锁纪律修正后剩下的风险
	// 是「单次调用无界」——源数 × 单源投影查询（段扫描随源数据量增长，生产单源可达秒级）
	// 没有任何上界。故遍历带上界，并把「没跑完」如实返回（不伪装成成功）。
	resolveGapsBudget ResolveGapsBudget
	// resolveGapsCursor 是下一次整节点解算的起始源下标（轮转）。
	//
	// 为什么需要游标而不是「每轮都从第一个源开始」：预算用尽后的重复调用必须能**继续**，
	// 否则永远停在同一个前缀、后面的源再也不会被解算（等于把「有界」变成「永远解不完整」）。
	// 游标只在**处理成功**一个源后前移，失败时停在出错的源上，便于修复后原地重试。
	resolveGapsCursor int
	// stopped 表示 Stop 已开始（索引句柄即将/已经关闭）。
	//
	// 为什么解算需要它：解算的变更段不再整段持有 cycleMu（见 resolveOneSourceGaps），于是
	// 它可能在两个源之间与 Stop 交错——此时再 persist 会把已关闭的索引句柄重新打开且无人关闭
	// （registerMu 注释里点名的同型形态）。停止后一律拒绝，而不是留下无人关闭的连接与 WAL 残留。
	stopped bool
	// resolveGapsStage 是解算的测试观测口（生产为 nil，零开销）：在**离锁**阶段按源回调，
	// 让回归能把「解算进行中」钉在确定位置，从而断言采集轮照常推进（与既有的 stageSink 同型）。
	resolveGapsStage func(string)
	root             string
	vl               *vlsup.Client
	vlRoute          func(SourceConfig) (*vlsup.Client, bool, error)
	cat              *catalog.Catalog
	journal          catalog.Journal
	archive          *archive.Registry
	sources          map[string]SourceConfig
	pipes            map[string]*pipeline.Pipeline
	state            persistedState
	statePath        string
	// index 是采集索引的嵌入式 SQLite 存储（FR-496，spec §2.1）：persist 只写变更行，
	// 启动时自动迁移旧 ingest.state.json（校验失败拒绝启动）。nil 表示尚未打开（
	// 测试直接构造 Manager 的场景在首次 persist 时按需打开）。
	index *stateindex.Store
	// persistedRev 记录「上次持久化时」各源账本的修订号（见 ledger.Ledger.Revision）。
	// persist 据此只处理变更源，把每次持久化的成本从 O(全部源的行数) 降到 O(变更源的行数)。
	//
	// 基线在 Register（恢复完账本、建好 pipeline）的**最后**一步写入，因此「刚启动」天然是
	// 干净的：内存状态与库内容一致，无需任何写入即达成一致。
	persistedRev map[string]uint64
	// persistedCover 记录「上次持久化时」各源的段存储覆盖水位。
	//
	// 为什么修订号之外还需要它：WAL 行是「内联正文」还是「只存引用」由
	// (EventsStored, EventsStoredThrough) 决定（B1a 的按条判据），而这两个字段由投递路径
	// 直接改 state——账本修订号看不见它。若不单独跟踪，段存储覆盖推进后旧的内联行不会被
	// 改写成引用行（或反之），源会在重启后按错误的形态恢复。
	//
	// 这里用「现值与上次记录值比较」而不是「在上层每个改动点显式标记」：前者不需要任何人
	// 记得标记（漏标记即静默丢更新），且比较只是两个标量，代价可忽略。
	persistedCover map[string]coverSignature
	// events 是 canonical 事件体的追加式磁盘段存储（FR-484）；权威副本，state 只存元数据。
	events              *eventstore.Store
	verificationTimeout time.Duration
	// verifyBackoffMin/verifyBackoffMax 是投影校验重试的退避区间（B1c）；0 用默认常量。
	verifyBackoffMin time.Duration
	verifyBackoffMax time.Duration
	// verifyChunkEvents 是单次校验查询覆盖的事件上限（分簇粒度，FR-498 P0）；0 用默认常量。
	// 作为字段而非裸常量：不同 VL 部署的返回体承载能力不同，且回归要用真实限额路径驱动。
	verifyChunkEvents int
	// verifyChunkConcurrency 是同批内各校验簇的查询并发度；0/负值取默认，1 为串行。
	verifyChunkConcurrency int
	// sourceMaxEnd 缓存每源「权威集合的最大 record_end」（封闭可见水位）。
	//
	// 为什么需要（FR-498 单位行成本 8.3× 的修复之一）：publish 原先把 maxEnd 算成
	// 「传入 events 的最大 record_end」，而 events 是**权威全量**——为拿这一个标量，每批
	// 都要遍历该源全部历史事件（生产实测单源段 18GB/63 源，单次遍历 8.7 µs/行）。
	// 该水位是**单调不减**的（段只追加、投递只前进），因此可以按源缓存、每批只与「本批最大值」
	// 取大。字段缺失时由 authoritativeMaxEnd 用 state 的 EventsStoredThrough 初始化。
	sourceMaxEnd map[string]uint64
	// eventsStoredRows 缓存每源「段存储已落行数」，避免 appendEvents 每批 Count（其内部要
	// 校准末段 = O(末段行数) 的整段读取）。本进程是段的唯一写者，追加成功后按增量推进即可。
	eventsStoredRows map[string]int
	// stageSink 是阶段计时的测试观测口（生产为 nil，零开销）：让回归能断言「快路径确实生效」，
	// 而不必依赖耗时阈值或日志解析。
	stageSink        func(string)
	capacityProvider func() (acquire.CapacityBudget, error)
	recoveryHold     func(SourceConfig, string) (bool, string)
	// reconcile 是启动增量对账（FR-497）的生效配置（已归一化）。
	reconcile ReconcileConfig
	// reconcileLoop 是常驻增量对账（后续项③）的生效配置（已归一化）：周期驱动、单轮预算与
	// 源数有界，默认温和（15m / 45s / 8 源轮转）。**关闭即完全不跑**（不打 VL 查询、
	// 不推进任何状态）；启动对账（reconcile）不受它影响。
	reconcileLoop ReconcileLoopConfig
	// reconcileLoopStats / reconcileLoopCursor / reconcileDayCounts 是常驻对账的观测读数、
	// 全源轮转游标与「按 UTC 天权威条数」缓存（见 reconcile_loop.go 的说明）。
	reconcileLoopStats  ReconcileLoopStats
	reconcileLoopCursor int
	reconcileDayCounts  map[string]reconcileDayCountsCache
	// indexPrune 是历史投递批次裁剪的生效配置（已归一化，默认开启）：建每个源的账本时下发
	// （ledger.SetDeliveryBatchPrune）。裁剪发生在账本写路径内，因此配置必须落在账本上，
	// 而不是落库前再过滤一遍——后者只能让库变小，内存仍会随总量线性增长。
	indexPrune ledger.DeliveryBatchPruneConfig
	// indexCommit 是索引持久化「提交单元预算」的生效配置（已归一化）：单次持久化按
	// 行数 + 耗时双上界切成多个提交单元（FR-498 P0，见 stateindex.CommitBudget）。
	// 打开索引库时下发（stateindex.OpenWithBudget）。
	indexCommit stateindex.CommitBudget
	// walLimits 是单源 WAL **真实积压**上限的配置面（键 log_capacity.max_wal_entries /
	// max_wal_bytes）；建管道时下发到 acquire.WAL.SetLimits。nil 表示沿用 acquire 包默认。
	//
	// 为什么要经 Manager 转发而不是在 acquire 里读配置：WAL 由本包创建（newWAL），
	// 配置也只到本层；acquire 不得反向依赖 worker 配置。
	walLimits *acquire.WALLimits
	// maxReplayEventsPerDrain 是暂停/恢复期单轮外发事件数上限（回放限速）；0 表示用默认。
	maxReplayEventsPerDrain int
	// multilineUnclosedTimeout 是未闭合多行缓冲的闲置超时（缺陷 B）；≤0 表示关闭强制冲刷。
	multilineUnclosedTimeout time.Duration
	// reconcileReports 保留最近一次启动对账的逐源结论（只读观测面）。
	reconcileReports []ReconcileReport
	// sourceErrs 记录每源最近一次已上报的采集错误，避免同一错误每 250ms 刷屏。
	sourceErrs map[string]string
	// gapAutoResolveAt 记录每源最近一次自愈尝试的时刻（缺陷 A）。
	// 自愈要投递存量并按天聚合事件区间，故按源限频（见 gapAutoResolveInterval）。
	gapAutoResolveAt map[string]time.Time
	// selfHealInterval 是自愈的最小重试间隔；0 表示用默认常量。
	selfHealInterval time.Duration
	// generationProbeCache 是「该源该代次已确认未占用」的进程内缓存（复审 P2-11，
	// 见 projectionGenerationKnownFree / recordProjectionGenerationAttempt）。不持久化：
	// 重启后重新实探，正是保守方向。
	generationProbeCache map[string]map[string]bool

	// pollConcurrency 是单轮采集的跨源并发度；0/负值取 defaultPollConcurrency，1 为串行。
	// 作为字段以便用真实限额路径做对照实验与回归，而非只断言常量本身。
	pollConcurrency int
	// publishMu 串行化「读当前权威 → 应用本源的投影 → CAS 写回」整段（见 publish）。
	//
	// 为什么必须有（FR-498 并发采集轮的前提）：同一 namespace 会有**多个源**（同一实例的
	// stdout/stderr、多文件源），而 publish 是读-改-写：并发轮下两个源各自读到同一份旧记录，
	// 后写者会把先写者的 SourceProjections 覆盖掉（丢失更新）。实测：两个 stdio 源并发投递后
	// 记录里只剩 1 条源投影（原串行实现为 2 条）。锁内重新读取当前记录即恢复串行语义。
	//
	// 锁序：它是叶子锁（cat 的回调不进入 Manager），可在 cycleMu 内、m.mu 之外获取。
	publishMu sync.Mutex
	// persistGate 让「构建 + 落库」同一时刻只有一次在跑（串行），并让短变更调用者只等
	// **一个周期**而不是排到队尾（见 persist 的合并说明）。
	persistGate persistGate
	// persistJoin / persistCovered 是合并算法的两个水位：调用方按到达顺序声明序号；
	// 每次落库声明它覆盖到的序号。声明号 ≤ 覆盖水位的调用方直接返回（它的变更已被落库）。
	persistJoin    atomic.Int64
	persistCovered atomic.Int64
	// pendingMaxStream/pendingMaxTotal 是 pending 暂存上限（M-6）；0 表示用默认常量。
	// 作为字段以便测试用小额度覆盖真实限额路径，而非只断言常量本身。
	pendingMaxStream int64
	pendingMaxTotal  int64
}

// SetSelfHealInterval 覆盖自愈最小重试间隔（测试用；<=0 表示沿用默认）。
func (m *Manager) SetSelfHealInterval(interval time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.selfHealInterval = interval
}

// SetPendingSpoolLimits 覆盖 pending 暂存上限（测试用）。
func (m *Manager) SetPendingSpoolLimits(perStream, total int64) {
	if m == nil {
		return
	}
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()
	m.pendingMaxStream = perStream
	m.pendingMaxTotal = total
}

// noteSourceError 记录并报告该源是否应再次上报该错误（同一文本只报一次）。
func (m *Manager) noteSourceError(sourceID, msg string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sourceErrs == nil {
		m.sourceErrs = map[string]string{}
	}
	if m.sourceErrs[sourceID] == msg {
		return false
	}
	m.sourceErrs[sourceID] = msg
	return true
}

// Options constructs a production ingestion manager. A nil VL client leaves
// sources durable but explicitly not delivered, preserving visible gaps.
type Options struct {
	Root    string
	VL      *vlsup.Client
	Catalog *catalog.Catalog
	// DefaultCharset 是节点级默认字符集（auto/utf-8/gbk/gb18030）；空串等价于 auto。
	// 源可用 SourceConfig.Charset 覆盖。
	DefaultCharset string
	// DefaultTimeZone 是节点级默认时区名（UTC/local/IANA 名）；空串等价于 UTC（既有行为）。
	// 源可用 SourceConfig.TimeZone 覆盖。节点的 JVM 与 Worker 同机部署时，配 local 即可对齐。
	DefaultTimeZone     string
	Journal             catalog.Journal
	Archive             *archive.Registry
	Sources             []SourceConfig
	VerificationTimeout time.Duration
	CapacityProvider    func() (acquire.CapacityBudget, error)
	RecoveryHold        func(SourceConfig, string) (bool, string)
	// VLRoute returns the Catalog-selected client and whether publication is
	// frozen. Nil preserves initial HOT behavior.
	VLRoute func(SourceConfig) (*vlsup.Client, bool, error)
	// Reconcile 是启动增量对账（FR-497）的配置面；nil 表示用默认（启用，
	// 并发 4、单源超时 30s、单查询超时 10s）。配置键登记见 spec §5。
	Reconcile *ReconcileConfig
	// ReconcileLoop 是常驻增量对账（后续项③）的配置面；nil 表示用默认（启用、15 分钟周期、
	// 单轮预算 45s、单轮 8 源轮转）。配置键登记为 `log_reconcile_loop.*`；与 Reconcile 同样，
	// 当前生效路径是 Options + Set（YAML → Options 接线需改 internal/worker/config.go）。
	ReconcileLoop *ReconcileLoopConfig
	// IndexPrune 是采集索引「历史投递批次（delivery_batch）裁剪」的配置面（FR-496 索引有界化，
	// spec §6）；nil 表示用默认（ledger.DefaultDeliveryBatchPruneConfig：**开启**、严格按
	// reclaim 水位）。判据与证明见 ledger/delivery_batch_prune.go。
	IndexPrune *ledger.DeliveryBatchPruneConfig
	// IndexCommit 是索引持久化「提交单元预算」的配置面（FR-498 P0，键 log_index.persist.*，
	// spec §3.4）；nil 表示用默认（stateindex.DefaultCommitBudget：每提交单元 512 行、目标 40ms、
	// 下限 64 行——真源是 stateindex 的 DefaultCommitMaxRows/DefaultCommitMinRows/DefaultCommitTarget）。
	// 单次持久化按行数 + 耗时双上界切成多个提交单元，见 stateindex.CommitBudget。
	IndexCommit *stateindex.CommitBudget
	// WALLimits 是单源 WAL **真实积压**上限（条目数 + 字节）的配置面（键 log_capacity.*）；
	// nil 表示沿用 acquire 包默认。接线到 acquire.WAL.SetLimits。
	WALLimits *acquire.WALLimits
	// MaxReplayEventsPerDrain 是暂停/恢复期单轮外发事件数上限（回放限速，键
	// log_capacity.max_replay_events_per_drain）；0 表示用默认。
	MaxReplayEventsPerDrain int
	// MultilineUnclosedTimeout 是未闭合多行缓冲的闲置超时（键
	// log_ingest.multiline_unclosed_timeout）；0 表示用默认（normalize.DefaultUnclosedTimeout），
	// 负值表示关闭强制冲刷。见 Pipeline.FlushStaleMultiline。
	MultilineUnclosedTimeout time.Duration
	// ResolveGapsBudget 是整节点解算（ResolveCoveredGaps）单次调用的预算（源数 + 墙钟），
	// 键 log_ingest.resolve_gaps_max_sources / log_ingest.resolve_gaps_max_duration；
	// nil 表示用默认（defaultResolveGapsMaxSources / defaultResolveGapsMaxDuration）。
	// 见 ResolveGapsBudget 的缺陷说明。
	ResolveGapsBudget *ResolveGapsBudget
}

type CutoverReadiness struct {
	LedgerReady bool
	CutoffTime  time.Time
	Reasons     []string
}

func (m *Manager) CutoverReadiness() CutoverReadiness {
	result := CutoverReadiness{LedgerReady: true, CutoffTime: time.Now().UTC()}
	if m == nil {
		return CutoverReadiness{Reasons: []string{"ingest_manager_unavailable"}}
	}
	pending, pendingErr := m.pendingSpoolState()
	if pendingErr != nil {
		result.LedgerReady = false
		result.Reasons = append(result.Reasons, "pending_spool_unreadable")
	}
	if len(pending) > 0 {
		result.LedgerReady = false
		for _, key := range pending {
			result.Reasons = append(result.Reasons, "pending_spool:"+key)
		}
	}
	m.mu.Lock()
	keys := make([]string, 0, len(m.pipes))
	for key := range m.pipes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pipes := make(map[string]*pipeline.Pipeline, len(m.pipes))
	sources := make(map[string]SourceConfig, len(m.sources))
	for key, pipe := range m.pipes {
		pipes[key] = pipe
		sources[key] = m.sources[key]
	}
	m.mu.Unlock()
	if len(keys) == 0 {
		if len(pending) == 0 && pendingErr == nil {
			return CutoverReadiness{Reasons: []string{"no_managed_log_sources"}}
		}
		result.LedgerReady = false
		result.Reasons = append(result.Reasons, "no_managed_log_sources")
		return result
	}
	for _, key := range keys {
		entry := pipes[key].Ledger().Get(pipes[key].Key())
		if entry == nil {
			result.LedgerReady = false
			result.Reasons = append(result.Reasons, key+":ledger_missing")
			continue
		}
		if entry.AcquirePaused {
			result.LedgerReady = false
			result.Reasons = append(result.Reasons, key+":acquire_paused")
		}
		if pipes[key].Ledger().UnresolvedGapCount(pipes[key].Key()) > 0 {
			result.LedgerReady = false
			result.Reasons = append(result.Reasons, key+":unresolved_gaps")
		}
		if entry.Positions.Durable < entry.Positions.Read {
			result.LedgerReady = false
			result.Reasons = append(result.Reasons, key+":durable_behind_read")
		}
		if entry.Positions.Reclaim < entry.Positions.Durable {
			result.LedgerReady = false
			result.Reasons = append(result.Reasons, key+":reclaim_behind_durable")
		}
		source := sources[key]
		m.mu.Lock()
		saved := m.state.Sources[key]
		m.mu.Unlock()
		// 零数据源（从未采集到任何事件，Durable==0）没有可发布的投影——「空」是合法状态，
		// 不得因此否决全局切换（与 CP 侧 2026-09-30「空≠缺」语义修正同型）。
		// 生产实证：inst:152/file（bungee 的空文件日志）与 inst:153/stdout（Beacon 空 stdout）
		// 在此永久产出 projection_not_published，使 cutover 闸门恒不可满足。
		if entry.Positions.Durable > 0 {
			closed, complete := m.publishedClosedForSource(source, saved)
			if !complete || closed < entry.Positions.Durable {
				result.LedgerReady = false
				result.Reasons = append(result.Reasons, key+":projection_not_published")
			}
		}
	}
	return result
}

func (m *Manager) PrepareCutoverReadiness() CutoverReadiness {
	if m == nil {
		return CutoverReadiness{Reasons: []string{"ingest_manager_unavailable"}}
	}
	m.cycleMu.Lock()
	defer m.cycleMu.Unlock()
	m.mu.Lock()
	pipes := make([]*pipeline.Pipeline, 0, len(m.pipes))
	for _, pipe := range m.pipes {
		pipes = append(pipes, pipe)
	}
	m.mu.Unlock()
	var flushReasons []string
	for _, pipe := range pipes {
		if _, err := pipe.Flush(); err != nil {
			flushReasons = append(flushReasons, pipe.Key().String()+":flush_failed")
		}
	}
	if err := m.persist(); err != nil {
		flushReasons = append(flushReasons, "persist_failed")
	}
	result := m.CutoverReadiness()
	if len(flushReasons) > 0 {
		result.LedgerReady = false
		result.Reasons = append(result.Reasons, flushReasons...)
	}
	return result
}

// ResolveGapsBudget 是整节点解算（ResolveCoveredGaps）单次调用的预算。
//
// 为什么必须有（2026-10-02 压测现场实证）：整节点路径不传 storageNamespace，会对**全部源**
// 做投影查询与账本变更；此前它整段持 cycleMu（与采集轮同一把锁），于是一次调用即让整节点采集
// 停摆（read_pos 冻结 30 分钟、source_wal 卡 560 万行、索引 WAL 涨到 10.2 GB），且 HTTP 300s
// 超时后服务端仍继续持锁跑完（取消无效）。锁纪律修正（见 ResolveCoveredGaps）之后，剩下的
// 风险是「单次调用无界」：源数 × 单源投影查询（段级扫描随源数据量增长，生产单源可达秒级）
// 本身没有上界。故遍历带上界，并把「没跑完」如实返回——不伪装成功，也不静默继续。
type ResolveGapsBudget struct {
	// MaxSources 是单次调用最多处理的源数；≤0 用 defaultResolveGapsMaxSources。
	MaxSources int
	// MaxDuration 是单次调用的墙钟预算；≤0 用 defaultResolveGapsMaxDuration。
	//
	// 检查发生在**源与源之间**：单源内部不可中断（其可中断性由 ctx 承担，见
	// publishedClosedForSourceCtx），故实际耗时可上浮「一个源」的时长。
	MaxDuration time.Duration
}

const (
	// defaultResolveGapsMaxSources 覆盖单节点常见规模（生产实测 60–75 源）且仍是硬上界：
	// 超出即分片（见 resolveGapsCursor），而不是让一次调用跑到天荒地老。
	defaultResolveGapsMaxSources = 256
	// defaultResolveGapsMaxDuration 是兜底预算：主要服务于**没有 deadline 的调用方**
	// （RPC 侧自有截止时间，ctx 会先到点）。取值远大于「256 源 × 单源投影查询」的期望耗时。
	defaultResolveGapsMaxDuration = 2 * time.Minute
)

// DefaultResolveGapsMaxSources 返回默认的解算源数预算。
//
// 导出是为了让配置层（internal/worker/config.go 的错误消息与默认值）引用**同一个真源**，
// 而不是各写一份数字——两份数字迟早不一致，且不一致时没有任何编译错误提示。
func DefaultResolveGapsMaxSources() int { return defaultResolveGapsMaxSources }

// DefaultResolveGapsMaxDuration 返回默认的解算墙钟预算（同上，唯一真源）。
func DefaultResolveGapsMaxDuration() time.Duration { return defaultResolveGapsMaxDuration }

// ErrResolveGapsBudgetExhausted 是「本次调用在预算内没跑完」的哨兵错误。
//
// 为什么用错误而不是「部分成功」：解算是运维显式动作，调用方必须能区分「全部解完」与
// 「只解了前 N 个源，剩下的还没碰」。返回错误并把续跑方式写进消息，方可执行。
var ErrResolveGapsBudgetExhausted = errors.New("ingest: 全源解算预算用尽")

// ResolveGapsBudgetExhaustedError 说明预算用尽时的进度与续跑方式。
type ResolveGapsBudgetExhaustedError struct {
	Processed   int
	Total       int
	MaxSources  int
	MaxDuration time.Duration
	// Cursor 是下一次调用将从此下标（排序后的源键序）继续的位置。
	Cursor int
}

func (e *ResolveGapsBudgetExhaustedError) Error() string {
	return fmt.Sprintf("ingest: 全源解算预算用尽（本轮处理 %d/%d 源，预算 %d 源 / %s，下一次将从第 %d 个源继续）；再次调用同一接口即可续跑",
		e.Processed, e.Total, e.MaxSources, e.MaxDuration, e.Cursor)
}

// Unwrap 让调用方可用 errors.Is(err, ErrResolveGapsBudgetExhausted) 判定（如「稍后再调」重试）。
func (e *ResolveGapsBudgetExhaustedError) Unwrap() error { return ErrResolveGapsBudgetExhausted }

// resolveGapsBudgetOfOpts 归一化 Options 传入的预算指针（nil → 零值，由 resolveGapsBudgetOf 兜默认）。
//
// 复制而非共享指针：配置对象在装配层可能被复用，Manager 不得持有外部可变状态。
func resolveGapsBudgetOfOpts(opt *ResolveGapsBudget) ResolveGapsBudget {
	if opt == nil {
		return ResolveGapsBudget{}
	}
	return *opt
}

// resolveGapsBudgetOf 返回归一化后的解算预算（零值 → 默认）。
func (m *Manager) resolveGapsBudgetOf() ResolveGapsBudget {
	budget := m.resolveGapsBudget
	if budget.MaxSources <= 0 {
		budget.MaxSources = defaultResolveGapsMaxSources
	}
	if budget.MaxDuration <= 0 {
		budget.MaxDuration = defaultResolveGapsMaxDuration
	}
	return budget
}

// ResolveCoveredGaps 解算全部源的「已覆盖缺口」（整节点自动路径）。
//
// 并发纪律（2026-10-02 生产缺陷修复，与 FR-499「登记路径与长临界区解耦」同款）：
//
//	此前：cycleMu 全程持有 → 逐源投影查询（段级扫描）压在锁上 → 一次调用 = 整节点采集停摆，
//	      且调用方超时/断开后服务端无从得知，继续持锁跑完（取消无效）。
//	现在：①m.mu 下快照源集合（短）；②逐源在**离锁**状态下做投影查询（成本的大头）；
//	      ③仅对「必须与采集轮串行」的账本变更取 cycleMu（每源一段短临界区，源与源之间
//	      允许采集轮推进）。判据一字未改，见 resolveOneSourceGaps 的两阶段说明。
//
// ctx 贯穿遍历与投影查询：调用方（HTTP/RPC）超时或断开 → 服务端工作即时停止并返回明确错误，
// 绝不静默继续。全源遍历带预算（源数 + 墙钟，可配：Options.ResolveGapsBudget），
// 超出预算返回 ResolveGapsBudgetExhaustedError（含续跑游标），单次调用不会无界跑。
func (m *Manager) ResolveCoveredGaps(ctx context.Context) error {
	if m == nil {
		return fmt.Errorf("ingest: manager unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	keys := make([]string, 0, len(m.pipes))
	pipes := make(map[string]*pipeline.Pipeline, len(m.pipes))
	sources := make(map[string]SourceConfig, len(m.pipes))
	for key, pipe := range m.pipes {
		keys = append(keys, key)
		pipes[key] = pipe
		sources[key] = m.sources[key]
	}
	start := m.resolveGapsCursor
	stopped := m.stopped
	m.mu.Unlock()
	if stopped {
		return fmt.Errorf("ingest: manager 已停止，拒绝解算（停止后不得再写账本或重开索引句柄）")
	}
	// 源键排序 + 轮转游标：遍历顺序确定（原实现按 map 随机序），且预算用尽后的重复调用
	// 从断点继续，而不是每轮都停在同一个前缀（否则「有界」会退化成「永远解不完整」）。
	sort.Strings(keys)
	if len(keys) == 0 {
		return m.persist()
	}
	if start < 0 || start >= len(keys) {
		start = 0
	}
	budget := m.resolveGapsBudgetOf()
	deadline := time.Now().Add(budget.MaxDuration)
	processed := 0
	for i := 0; i < len(keys); i++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("ingest: 全源解算被取消（本轮已处理 %d/%d 源）: %w", processed, len(keys), err)
		}
		if processed >= budget.MaxSources || time.Now().After(deadline) {
			return &ResolveGapsBudgetExhaustedError{
				Processed: processed, Total: len(keys), MaxSources: budget.MaxSources,
				MaxDuration: budget.MaxDuration, Cursor: (start + i) % len(keys),
			}
		}
		key := keys[(start+i)%len(keys)]
		if err := m.resolveOneSourceGaps(ctx, key, pipes[key], sources[key]); err != nil {
			return err
		}
		processed++
		m.mu.Lock()
		m.resolveGapsCursor = (start + i + 1) % len(keys)
		m.mu.Unlock()
	}
	return m.persist()
}

// unverifiedRawWriteFailure 是两阶段共用的**逐字**判据：未解决的 Raw 写失败缺口一律拒绝
// （projection alone cannot resolve it）——原始正文没有落盘、投影里也没有，任何「已发布投影」
// 都不能证明它存在过。措辞与改动前一致（既有回归按该措辞断言）。
func unverifiedRawWriteFailure(key string, entry *ledger.Entry) error {
	for _, gap := range entry.Gaps {
		if !gap.Resolved && gap.Reason == "STDIO_RAW_WRITE_FAILED" {
			return fmt.Errorf("ingest: source %s has an unverified Raw write failure; projection alone cannot resolve it", key)
		}
	}
	return nil
}

// resolveOneSourceGaps 解指名源的缺口，分两阶段：
//
//   - 阶段 A（**不持** cycleMu）：测试观测口 → 账本快照 → 判据（Raw 写失败原因、投影完整性、
//     覆盖水位）→ 已发布投影查询（publishedClosedForSourceCtx，成本大头，ctx 可中断）。
//     该读路径在锁外执行**不是新引入的并发形态**：CutoverReadiness（CP 侧预备切换的就绪探测）
//     一直在不持 cycleMu 的情况下走同一条 publishedClosedForSource（账本快照 + 投影读），
//     本方法的阶段 A 与它同类，只是多了一步「随后在短临界区内复核」。
//   - 阶段 B（cycleMu **短临界区**）：用新鲜账本**逐字复核同一判据**后落变更
//     （ResolveGapsThrough / 静默源补段 / ResumeAcquire），随即释放锁。
//
// 为什么阶段 B 必须复核：A 与 B 之间采集轮可能推进真值（Durable 前移、新缺口出现）。复核只可能
// 让「投影已不足覆盖」的源**保守拒绝**（返回与旧实现同款的错误，运维重试即可），绝不会拿陈旧
// 证据去解一个已经不该解的缺口。反之，`closed` 取自 A 的已发布水位是**单调**证据：B 中它只会
// 显得更保守（ResolveGapsThrough(closed) 至多覆盖到该水位，不会越过）。
func (m *Manager) resolveOneSourceGaps(ctx context.Context, key string, pipe *pipeline.Pipeline, source SourceConfig) error {
	if pipe == nil {
		return fmt.Errorf("ingest: source %s pipeline missing", key)
	}
	if m.resolveGapsStage != nil {
		// 测试观测口（生产 nil，零开销）：回调发生在离锁阶段的最前，回归据此把「解算进行中」
		// 钉在确定位置，进而断言采集轮照常推进。
		m.resolveGapsStage(key)
	}
	// 阶段 A：离锁的判据 + 投影查询。
	entry := pipe.Ledger().Get(pipe.Key())
	if entry == nil {
		return fmt.Errorf("ingest: source %s ledger missing", key)
	}
	if err := unverifiedRawWriteFailure(key, entry); err != nil {
		return err
	}
	m.mu.Lock()
	saved := m.state.Sources[key]
	m.mu.Unlock()
	closed, complete, err := m.publishedClosedForSourceCtx(ctx, source, saved)
	if err != nil {
		// ctx 取消/超时或投影读取失败：明确失败（不得静默继续，也不得用半份证据解缺口）。
		return err
	}
	if !complete {
		return fmt.Errorf("ingest: source %s has no complete published projection", key)
	}
	if closed < entry.Positions.Durable || entry.Positions.Reclaim < entry.Positions.Durable {
		return fmt.Errorf("ingest: source %s recovery coverage is behind durable position", key)
	}

	// 阶段 B：与采集轮串行的最小窗口。
	//
	// 锁序：cycleMu → mu（见 cycleMu 注释的完整锁序）。
	// 最坏持有量：**单源变更段**——账本判据复核 + ResolveGapsThrough + 静默源补段（内存状态机）
	// + ResumeAcquire，不含任何投影查询、网络往返或持久化（persist 在调用方、锁外执行）。
	// 量化口径与 FR-499 一致：直接测「被它挡住的那件事」的时长，不用 TryLock 探锁（饥饿模式下
	// 锁空闲也返回 false）。回归 TestResolveCoveredGapsLockHoldIsBoundedPerSource 实测：
	// 解算总时长 372ms 的同一时段内，采集轮最坏单轮仅 1.37ms ⇒ 争用上界为毫秒级，
	// 与解算总时长（源数 × 单源投影查询）彻底解耦。
	m.cycleMu.Lock()
	defer m.cycleMu.Unlock()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("ingest: 全源解算被取消: %w", err)
	}
	m.mu.Lock()
	stopped := m.stopped
	m.mu.Unlock()
	if stopped {
		return fmt.Errorf("ingest: manager 已停止，拒绝解算（停止后不得再写账本或重开索引句柄）")
	}
	entry = pipe.Ledger().Get(pipe.Key())
	if entry == nil {
		return fmt.Errorf("ingest: source %s ledger missing", key)
	}
	if err := unverifiedRawWriteFailure(key, entry); err != nil {
		return err
	}
	if closed < entry.Positions.Durable || entry.Positions.Reclaim < entry.Positions.Durable {
		return fmt.Errorf("ingest: source %s recovery coverage is behind durable position", key)
	}
	if _, err := pipe.Ledger().ResolveGapsThrough(pipe.Key(), closed, "verified published projection"); err != nil {
		return err
	}
	// 静默源出口（2026-09-30 生产实证）：为「已全部投递、但无分段覆盖的尾部」补一段显式覆盖。
	//
	// 为何需要：建段只发生在 releaseRecovery（有新批次投递时）——而**静默流**（如某实例的
	// stderr，自 09-27 起再无新行）永远不会有新批次 → 尾部永远无段可依 → 回收停滞 → 积压不落 →
	// 源反复被暂停，且 read 已被夹到水位、自己永远走不出去。
	//
	// 安全性：仅当尾部**已全部确认投递**（delivery >= read）时才补段 —— 覆盖的是「已证明安全
	// 另存」的区间，不存在任何未投递数据被跳过；条件不满足则一律不动。
	//
	// 「无副本」标记（复审 P2-13）：这一段**没有任何物理副本**（路径不是文件、也不是
	// project://<代次>），此前却写 `manual://admin-confirmed` 并登记 `NEXT_COPY_VERIFIED`——
	// 前者看着像「管理员确认过某个已验证副本」，后者字面就是「下一份副本已验证」，
	// 于是状态链把「无副本」包装成了「已验证副本」，事后审计分不清这段到底有没有副本。
	// 现在如实标注：路径带 no-copy 标记，释放依据用 PROJECTION_BACKED（本段成立的前提正是
	// 「整段已投递并被逐字段校验」→ 已发布投影就是它的凭据），接收者显式说明无副本。
	if d := entry.Positions.Delivery; d >= entry.Positions.Read && entry.Positions.Read > entry.Positions.Reclaim {
		segID := fmt.Sprintf("manual-recovery-%d-%d", entry.Positions.Reclaim, entry.Positions.Read)
		if _, exists := pipe.RecoveryRef(segID); !exists {
			if err := pipe.BindRecoverySegment(segID, silentExitNoCopyPath, entry.Positions.Reclaim, entry.Positions.Read); err != nil {
				return err
			}
		}
		// 走完责任转移链，使 CanReclaim 放行（与 releaseRecovery 的状态机同构）。
		_ = pipe.TransitionRecovery(segID, logtypes.RecoveryDurableVerified, "", "")
		_ = pipe.TransitionRecovery(segID, logtypes.RecoveryWALResponsibilityXfer, "", silentExitNoCopyReceiver)
		// 释放依据必须与事实一致：没有第二份副本，凭据是「已发布投影 + 逐字段校验」。
		_ = pipe.TransitionRecovery(segID, logtypes.RecoveryReleased, logtypes.ReleaseProjectionBacked, silentExitNoCopyReceiver)
		// CLEANED 不登记新依据（账本契约：清理沿用已登记证明）；此处**不要**再传一次 reason，
		// 否则会把 RELEASED 阶段的依据覆盖成调用点写的值，「这段到底凭什么被释放」当场失真。
		_ = pipe.TransitionRecovery(segID, logtypes.RecoveryCleaned, "", "")
		// 推进回收并按当前水位剪枝；失败不置死——下一轮自动重试。
		_, _ = pipe.TryReclaim()
	}
	if err := pipe.Ledger().ResumeAcquire(pipe.Key()); err != nil {
		return err
	}
	return nil
}

// ResolveCoveredGapsForSource 是「显式人工确认」路径：只解指名源的缺口。
//
// 与自动路径（ResolveCoveredGaps）的区别，以及为什么必须要有它：
//   - 自动路径对 STDIO_RAW_WRITE_FAILED 一律拒绝（projection alone cannot resolve it），
//     且该拒绝发生在遍历所有源的循环内——一个源的不可验证缺口会中止**整节点**的解算；
//   - 当该源同时又是「唯一未就绪目标」时，就形成自我指涉：要解缺口需平台就绪，
//     要平台就绪需该源可查，而它正卡在缺口上。
//
// 语义（2026-10-02 收紧）：本路径是**放弃裁定**，不再是「把缺口标成已解决」——
// 解到该源当前读位置，等于「我确认这段缺口永久丢失、不再补齐」。因此：
//   - 走 Ledger.AbandonGapsThrough，留下结构化的原因码/时间/操作人 + 独立凭据，
//     使「补齐了」与「被放弃了」在数据上可区分；
//   - **操作人为空一律拒绝**（放弃不可无痕）。操作人由调用方（gRPC 层）从认证上下文取。
//
// 其它源一概不动。default-deny 判据不受影响：自动路径仍按原因排除，本方法只影响指名源。
//
// ctx 语义（2026-10-02 与整节点路径同批修正）：调用方（HTTP/RPC）超时或断开时**不得**把放弃
// 裁定落盘——放弃是唯一允许回收链跨过永久空洞的动作，必须来自一次完整、未被取消的显式请求。
// 故入口与临界区内各校验一次，取消即返回明确错误（不写账本、不恢复采集）。
func (m *Manager) ResolveCoveredGapsForSource(ctx context.Context, storageNamespace, operator string) error {
	if m == nil {
		return fmt.Errorf("ingest: manager unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if storageNamespace == "" {
		return fmt.Errorf("ingest: storage namespace is required")
	}
	if strings.TrimSpace(operator) == "" {
		// 拒绝而不是「用个占位名兜过去」：放弃是唯一允许回收链跨过永久空洞的动作，
		// 无痕放弃等于给静默丢日志开后门。
		return fmt.Errorf("ingest: 放弃缺口必须携带操作人（认证主体为空时拒绝执行）")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("ingest: 放弃裁定被取消（未写入任何变更）: %w", err)
	}
	m.cycleMu.Lock()
	defer m.cycleMu.Unlock()
	m.mu.Lock()
	stopped := m.stopped
	pipe := m.pipes[storageNamespace]
	m.mu.Unlock()
	if stopped {
		return fmt.Errorf("ingest: manager 已停止，拒绝放弃裁定（停止后不得再写账本或重开索引句柄）")
	}
	// 临界区内再校验一次：等锁期间调用方可能已取消——此时不得留下「无人认领的放弃」。
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("ingest: 放弃裁定被取消（未写入任何变更）: %w", err)
	}
	if pipe == nil {
		return fmt.Errorf("ingest: source %s not found", storageNamespace)
	}
	entry := pipe.Ledger().Get(pipe.Key())
	if entry == nil {
		return fmt.Errorf("ingest: source %s ledger missing", storageNamespace)
	}
	// 取值必须**越过所有未解决缺口的末端**：缺口总落在「已读位置」之后，
	// 只解到读位置覆盖不到它们，ResumeAcquire 会继续拒绝
	// （unresolved gaps still block acquisition）——这正是 409 的成因。
	through := entry.Positions.Read
	if entry.Positions.Durable > through {
		through = entry.Positions.Durable
	}
	for _, gap := range entry.Gaps {
		if !gap.Resolved && gap.EndPos > through {
			through = gap.EndPos
		}
	}
	if _, err := pipe.Ledger().AbandonGapsThrough(pipe.Key(), through, ledger.GapAbandonment{
		ReasonCode: ledger.GapReasonPermanentlyLost,
		Operator:   operator,
		Detail:     "manual abandonment via ResolveCoveredGapsForSource",
	}); err != nil {
		return err
	}
	if err := pipe.Ledger().ResumeAcquire(pipe.Key()); err != nil {
		return err
	}
	return m.persist()
}

// New restores source ledger/WAL/projection state and creates configured pipelines.
func New(opts Options) (*Manager, error) {
	if opts.Root == "" {
		return nil, fmt.Errorf("ingest: root is required")
	}
	if opts.Catalog == nil {
		return nil, fmt.Errorf("ingest: catalog is required")
	}
	m := &Manager{
		root: opts.Root, vl: opts.VL, vlRoute: opts.VLRoute, cat: opts.Catalog, journal: opts.Journal, archive: opts.Archive,
		sources: make(map[string]SourceConfig), pipes: make(map[string]*pipeline.Pipeline),
		state:                    persistedState{Sources: make(map[string]persistedSource)},
		statePath:                filepath.Join(opts.Root, "var", "log", "ingest.state.json"),
		verificationTimeout:      opts.VerificationTimeout,
		capacityProvider:         opts.CapacityProvider,
		recoveryHold:             opts.RecoveryHold,
		resolveGapsBudget:        resolveGapsBudgetOfOpts(opts.ResolveGapsBudget),
		reconcile:                reconcileConfigOf(opts.Reconcile),
		reconcileLoop:            reconcileLoopConfigOf(opts.ReconcileLoop),
		indexPrune:               indexPruneConfigOf(opts.IndexPrune),
		indexCommit:              indexCommitBudgetOf(opts.IndexCommit),
		walLimits:                opts.WALLimits,
		maxReplayEventsPerDrain:  opts.MaxReplayEventsPerDrain,
		multilineUnclosedTimeout: multilineUnclosedTimeoutOf(opts.MultilineUnclosedTimeout),
	}
	m.defaultCharset = opts.DefaultCharset
	m.defaultTimeZone = opts.DefaultTimeZone
	// 节点级默认时区必须**运行期自证**（2026-10-02 真机复验：配了 local 却仍是 +8h，因为
	// 容器/systemd 里 local 恰好解析成 UTC）。这段日志与告警把「配置值 → 解析结果 → 解析依据」
	// 一次说清，避免下一次又去怀疑接线与恢复路径。
	if loc, err := ParseTimeZone(m.defaultTimeZone); err == nil {
		_, offset := time.Now().In(loc).Zone()
		if offset != 0 || strings.TrimSpace(m.defaultTimeZone) != "" {
			slog.Info("采集归一化节点默认时区已生效",
				"config", m.defaultTimeZone, "resolved", loc.String(), "offsetSeconds", offset)
		}
		if hint := DefaultTimeZoneHint(m.defaultTimeZone, loc); hint != "" {
			slog.Warn("节点默认时区配置未达到预期效果", "hint", hint)
		}
	}
	if strings.TrimSpace(m.defaultCharset) != "" {
		slog.Info("采集归一化节点默认字符集已生效", "config", m.defaultCharset)
	}
	if m.verificationTimeout <= 0 {
		// 5 分钟（原 30 秒）。依据 2026-09-28 生产实测：VL 的 /insert/jsonline 是「接收即
		// 返回 200、索引异步」，恢复期单批 5746 条的可见性延迟**超过 30 秒**——写入 200 成功、
		// 30 秒后校验仍报 `not fully visible before deadline`，而稍后查询该代号确有 5746 条。
		// 校验本就有指数退避重试，数据一旦可见即立刻通过，故放宽窗口只影响「最坏等待」，
		// 不影响成功路径的耗时。可用 Options.VerificationTimeout 覆盖。
		m.verificationTimeout = 5 * time.Minute
	}
	// 投影校验的退避参数（B1c）：默认 200ms 起、封顶 2s。字段化以便测试用短窗口驱动。
	if m.verifyBackoffMin <= 0 {
		m.verifyBackoffMin = defaultVerifyBackoffMin
	}
	if m.verifyBackoffMax <= 0 {
		m.verifyBackoffMax = defaultVerifyBackoffMax
	}
	// 投影校验的分簇与并发（FR-498 P0）：默认 2000 事件/簇、同批内 4 路并发。
	if m.verifyChunkEvents <= 0 {
		m.verifyChunkEvents = defaultVerifyChunkEvents
	}
	if m.verifyChunkConcurrency <= 0 {
		m.verifyChunkConcurrency = defaultVerifyChunkConcurrency
	}
	// 采集索引（FR-496）：打开 SQLite 索引，必要时一次性迁移旧 ingest.state.json；
	// 迁移/校验失败 → 拒绝启动采集（不静默降级），保留旧文件供人工处置。
	if err := m.openIndex(); err != nil {
		return nil, err
	}
	// 事件体走追加式磁盘段（FR-484）：state 只留元数据，避免整份重写与常驻切片。
	store, err := eventstore.Open(filepath.Join(opts.Root, "var", "log", "events"))
	if err != nil {
		return nil, err
	}
	m.events = store
	if err := m.load(); err != nil {
		_ = store.Close()
		return nil, err
	}
	// 旧格式迁移：state 里仍内联事件体时搬到段存储（失败则保留旧格式继续，不丢数据）。
	if err := m.migrateInlineEvents(); err != nil {
		_ = store.Close()
		return nil, err
	}
	restoredSources := make(map[string]SourceConfig)
	for key, source := range m.state.SourceConfigs {
		restoredSources[key] = source
	}
	for _, source := range opts.Sources {
		restoredSources[source.LogSourceID+"/"+source.SourceGeneration] = source
	}
	keys := make([]string, 0, len(restoredSources))
	for key := range restoredSources {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	// 启动恢复分三阶段（FR-497）：
	//   ① 登记全部源并重建权威集合（顺序与旧实现一致）；
	//   ② 并发对账「源 × UTC 天」的条数（只读 VL，双超时）；
	//   ③ 按源重发：只补缺失天 / 零重发 / 回退整窗（对账不可信时）。
	//
	// 为什么要拆开：对账是网络查询，必须能并发且失败只影响单源；而登记与重发仍要保持
	// 既有的确定性顺序（catalog 发布、段存储、回收责任推进都不接受乱序）。
	recovery := make([]startupSource, 0, len(keys))
	for _, sourceKey := range keys {
		source := restoredSources[sourceKey]
		if err := m.Register(source); err != nil {
			return nil, err
		}
		key := source.LogSourceID + "/" + source.SourceGeneration
		m.mu.Lock()
		saved := m.state.Sources[key]
		m.mu.Unlock()
		recoveryEvents, err := m.canonicalRecoveryEvents(key, saved)
		if err != nil {
			return nil, err
		}
		if len(recoveryEvents) == 0 {
			continue
		}
		if m.vl == nil && m.vlRoute == nil {
			// 无 VL 客户端：没有可对账、也没有可重发的目标（与旧行为一致）。
			continue
		}
		recovery = append(recovery, startupSource{source: source, key: key, events: recoveryEvents})
	}
	if len(recovery) > 0 {
		reports := m.reconcileStartup(context.Background(), recovery)
		m.recordReconcileReports(reports)
		for index, item := range recovery {
			if err := m.applyStartupRecovery(item, reports[index]); err != nil {
				return nil, err
			}
		}
	}
	if err := m.recoverPendingSpools(); err != nil {
		_ = store.Close()
		return nil, err
	}
	return m, nil
}

func (m *Manager) recoverPendingSpools() error {
	m.mu.Lock()
	bindings := make([]InstanceBinding, 0, len(m.state.Instances))
	for _, binding := range m.state.Instances {
		bindings = append(bindings, binding)
	}
	m.mu.Unlock()
	for _, binding := range bindings {
		m.pendingMu.Lock()
		err := m.flushPendingForBinding(binding)
		m.pendingMu.Unlock()
		if err != nil {
			m.recordRawWriteFailure(binding, pendingFlushStream(err), 0)
			return fmt.Errorf("ingest: recover pending instance output: %w", err)
		}
	}
	return nil
}

func (m *Manager) pendingSpoolState() ([]string, error) {
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()
	root := filepath.Join(m.root, "var", "log", pendingSpoolRoot)
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pending []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		files, readErr := os.ReadDir(filepath.Join(root, entry.Name()))
		if readErr != nil {
			return nil, readErr
		}
		for _, file := range files {
			if !file.IsDir() && strings.HasSuffix(file.Name(), ".spool") {
				pending = append(pending, entry.Name()+"/"+strings.TrimSuffix(file.Name(), ".spool"))
			}
		}
	}
	sort.Strings(pending)
	return pending, nil
}

func DiskCapacityProvider(root string, configured acquire.CapacityBudget) func() (acquire.CapacityBudget, error) {
	return func() (acquire.CapacityBudget, error) {
		usage, err := disk.Usage(root)
		if err != nil {
			return acquire.CapacityBudget{}, err
		}
		budget := configured
		if budget.DegradedAtPercent <= 0 {
			budget.DegradedAtPercent = 80
		}
		if budget.PauseAtPercent <= 0 {
			budget.PauseAtPercent = 90
		}
		budget.DiskUsagePercent = usage.UsedPercent
		return budget, nil
	}
}

// IsValidCharset 报告节点级默认字符集取值是否合法（供配置层在启动阶段显式拒绝非法值）。
//
// 为什么由本包再导出一层：字符集的判定与解码口径都在 normalize（唯一真源），而配置层
// （internal/worker/config.go）只依赖 ingest 这一层——多导出一个名字，避免配置层为了校验
// 而绕到 normalize 上去，也避免两侧各自维护一份字符集白名单（那才是真正的漂移源）。
// 取值：auto / utf-8 / gbk / gb18030（空串表示未配置，按 auto 处理）。
func IsValidCharset(raw string) bool {
	return normalize.IsValidCharset(raw)
}

// DefaultMultilineUnclosedTimeout 返回未闭合多行缓冲的默认强制闭合时限（真源在 normalize）。
//
// 同上：配置层只依赖 ingest，故在此再导出一层，避免配置层为取一个默认值而再引入
// normalize 依赖、也避免默认值在两侧各写一遍（那会在调参时漂移）。
func DefaultMultilineUnclosedTimeout() time.Duration {
	return normalize.DefaultUnclosedTimeout
}

// 静默源出口的「无副本」标注（复审 P2-13）：这一段的凭据是「整段已投递并被逐字段校验」，
// 而不是任何物理副本。路径与责任接收者都带上显式标记，使事后审计一眼能看出「这段没有副本」。
const (
	// silentExitNoCopyPath 是静默源出口补出的恢复分段路径（**不是**文件路径，也不是 project://代次）。
	silentExitNoCopyPath = "manual://delivered-no-copy"
	// silentExitNoCopyReceiver 是该分段的责任接收者标记。
	silentExitNoCopyReceiver = "manual:admin(delivered-no-copy)"
)

// Register adds one source; registration is idempotent by logical source key.
func (m *Manager) Register(source SourceConfig) error {
	// explicit 是调用方（登记路径 / 索引恢复路径）给的**显式**配置：后面会被节点默认填充，
	// 故这里先留一份原件，用于「索引只记显式口径」与逐源自证日志。
	explicit := source
	if source.LogSourceID == "" || source.SourceGeneration == "" {
		return fmt.Errorf("ingest: source identity is required")
	}
	if source.Mode == "" {
		source.Mode = pipeline.ModeFilePrimary
	}
	if source.SourceCategory == "" {
		source.SourceCategory = logtypes.SourceInstance
	}
	if source.StorageNamespace == "" {
		source.StorageNamespace = source.LogSourceID
	}
	if source.UTCDay == "" {
		source.UTCDay = time.Now().UTC().Format("2006-01-02")
	}
	if source.Charset == "" {
		source.Charset = m.defaultCharset
	}
	if !normalize.IsValidCharset(source.Charset) {
		return fmt.Errorf("ingest: 未知日志字符集 %q（source=%s）", source.Charset, source.LogSourceID)
	}
	if source.TimeZone == "" {
		source.TimeZone = m.defaultTimeZone
	}
	location, err := ParseTimeZone(source.TimeZone)
	if err != nil {
		return fmt.Errorf("ingest: 源 %s 的时区配置无效: %w", source.LogSourceID, err)
	}
	// 逐源自证「生效归类口径」（2026-10-02 真机复验的教训）：一次运行里每个源只登记一次，
	// 这条日志把「源 → 生效时区/字符集 → 解析出的偏移」钉死。现场排查时只要 grep 它，
	// 就能立刻分辨「默认没落到该源」与「默认本身就是 UTC 语义」——不必再逐层读代码猜。
	{
		_, offset := time.Now().In(location).Zone()
		slog.Info("日志源归一化口径已生效",
			"source", source.LogSourceID, "generation", source.SourceGeneration,
			"timeZone", source.TimeZone, "timeZoneInherited", explicit.TimeZone == "",
			"resolved", location.String(), "offsetSeconds", offset,
			"charset", source.Charset, "charsetInherited", explicit.Charset == "")
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.pipes[key]; ok {
		if !sameSourceConfig(m.sources[key], source) {
			return fmt.Errorf("ingest: source identity already bound to another configuration")
		}
		return nil
	}
	led := ledger.New()
	// 历史投递批次裁剪（FR-496 索引有界化）：配置默认开启，非法值已在 indexPruneConfigOf 回退。
	led.SetDeliveryBatchPrune(m.indexPrune)
	wal := newWAL(led, ledger.SourceKey{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration})
	if saved, ok := m.state.Sources[key]; ok {
		if err := led.Restore(saved.Ledger); err != nil {
			return err
		}
		if len(saved.WALRefs) > 0 {
			// 引用形式（B1a）：内联条目直接恢复，引用条目按 EventID 从事件段存储水合。
			missing := wal.RestoreMixed(saved.WAL, saved.WALRefs, m.eventBodyLookup(key))
			if len(missing) > 0 {
				// 取不到正文的条目不得静默丢：记可见缺口并告警（与 FR-474 §5#1 同口径）。
				var from, to uint64 = ^uint64(0), 0
				for _, r := range missing {
					if r.RecordStart < from {
						from = r.RecordStart
					}
					if r.RecordEnd > to {
						to = r.RecordEnd
					}
				}
				if from == ^uint64(0) {
					from = 0
				}
				_ = led.RecordGap(ledger.SourceKey{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration},
					from, to, "WAL_BODY_MISSING",
					fmt.Sprintf("%d 条 WAL 引用在事件段存储中找不到正文（B1a 引用恢复）", len(missing)))
				slog.Warn("WAL 引用恢复：部分条目正文缺失，已记可见缺口",
					"logSourceID", source.LogSourceID, "missing", len(missing))
			}
		} else if err := wal.Restore(saved.WAL); err != nil {
			return err
		}
	}
	p, err := pipeline.New(pipeline.Options{
		Mode: source.Mode, LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration,
		Path: source.Path, RotateTo: source.RotateTo, Stream: source.Stream, Charset: source.Charset,
		Location:       location,
		SourceCategory: source.SourceCategory, Ledger: led, WAL: wal,
		SuppressRecursiveVL:     source.SourceCategory == logtypes.SourceWorker || source.SourceCategory == logtypes.SourceNode,
		CapacityProvider:        m.capacityProvider,
		DurablePersist:          m.persist,
		WALLimits:               m.walLimits,
		MaxReplayEventsPerDrain: m.maxReplayEventsPerDrain,
		ReclaimProof:            func(events []logtypes.Event) error { return m.releaseRecovery(source, events) },
		Delivery: pipeline.FuncHook(func(events []logtypes.Event, replay bool) (pipeline.DeliveryResult, error) {
			return m.deliver(source, events, replay)
		}),
	})
	if err != nil {
		return err
	}
	m.sources[key] = source
	m.pipes[key] = p
	// 归档归属闸（2026-10-02）：把「跨代次归属注册表」接到本源的导入器上。
	// 缺了它，归档导入只能假设"属于当前代次"——那是静默重复的入口。
	p.SetArchiveOwnerRegistry(m.archiveOwnerLookup())
	// 命中其它代次时的**补账**通道：记回原代次的待投递账目（不投递 VL，见 backfillArchiveUnderOwner）。
	// 补账不可行时导入器会退回"拒绝 + 记缺口"，绝不静默归当前代次。
	p.SetArchiveBackfill(func(ownerGeneration, archivePath, objectID string) error {
		return m.backfillArchiveUnderOwner(source, ownerGeneration, archivePath, objectID)
	})
	if m.state.SourceConfigs == nil {
		m.state.SourceConfigs = make(map[string]SourceConfig)
	}
	// 索引里只记**显式**口径（2026-10-02 真机复验的连带修复）：Charset / TimeZone 为空表示
	// 「跟随节点默认」，绝不把继承来的值写回索引——否则「本次运行继承了什么」会被固化成
	// 「该源的显式值」，之后改 `log_ingest.time_zone`／`log_ingest.charset` 重启对已登记源**永不生效**
	// （现场表现与「默认没接线」一模一样，极难分辨）。其余字段（Mode/来源类别/命名空间/日分区）
	// 是结构默认、也是索引行与 catalog 键的输入，仍按生效值落库。
	persisted := source
	persisted.Charset, persisted.TimeZone = explicit.Charset, explicit.TimeZone
	m.state.SourceConfigs[key] = persisted
	// 持久化基线：账本刚由 Restore/WAL.Restore 填好，内存状态与索引内容一致，
	// 因此这里直接取当前修订号与段覆盖签名作为「已落库」的起点——否则启动后的首轮
	// persist 会把每个源都当成变更源重写一遍（对 13 源现场就是几 MB 的无效写入）。
	if p.Ledger() != nil {
		if m.persistedRev == nil {
			m.persistedRev = make(map[string]uint64)
		}
		if m.persistedCover == nil {
			m.persistedCover = make(map[string]coverSignature)
		}
		m.persistedRev[key] = p.Ledger().Revision(p.Key())
		m.persistedCover[key] = coverSignatureOf(m.state.Sources[key])
	}
	return nil
}

// Start polls all configured sources until ctx is cancelled.
func (m *Manager) Start(ctx context.Context) {
	if m == nil {
		return
	}
	// 常驻增量对账（后续项③）：与采集轮共用同一 ctx 生命周期。它只读对账不持锁，
	// 重发阶段持 cycleMu 与采集轮串行（见 reconcile_loop.go 的隔离说明）。
	go m.RunReconcileLoop(ctx)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.pollOnce()
		}
	}
}

// Stop flushes multiline tails, persists ledger/WAL state, and leaves any
// unresolved delivery responsibility visible for the next process.
func (m *Manager) Stop() error {
	if m == nil {
		return nil
	}
	m.cycleMu.Lock()
	defer m.cycleMu.Unlock()
	// 与登记串行（锁序 cycleMu → registerMu，见 registerMu 注释）：Stop 之后不能再有登记
	// 进来 persist——那会在索引句柄关闭后把它重新打开，留下无人关闭的连接与 WAL 残留。
	m.registerMu.Lock()
	defer m.registerMu.Unlock()
	m.mu.Lock()
	// 停止标记：解算的变更段是**每源一段短临界区**（见 resolveOneSourceGaps），因此它可能在
	// 两个源之间与 Stop 交错——若放任其在 CloseIndex 之后 persist，会把已关闭的索引句柄重新
	// 打开且无人关闭（留下 WAL 残留），与 registerMu 注释里点名的形态同型。停止后一律拒绝。
	m.stopped = true
	pipes := make([]*pipeline.Pipeline, 0, len(m.pipes))
	for _, p := range m.pipes {
		pipes = append(pipes, p)
	}
	m.mu.Unlock()
	for _, p := range pipes {
		_, _ = p.Flush()
	}
	if err := m.persist(); err != nil {
		return err
	}
	// 关闭索引句柄：显式把 WAL 归并回主库（见 stateindex.Store.Close）。
	//
	// 为什么必须在 Stop 里做（而不是留给进程退出）：演练实测不归并时 `-wal` 会残留到
	// 数百 MB——迁移的一次性大事务写完即有 453.6 MB，事后进程被 kill 就永久留在盘上。
	// 这里只有索引句柄关掉了，Manager 之后**不能再 persist**（这是 Stop 的语义）。
	// 事件段句柄在持久化之后关闭：先让 state 元数据落定，再释放段写入端。
	if err := m.CloseIndex(); err != nil {
		return err
	}
	if m.events != nil {
		return m.events.Close()
	}
	return nil
}

// CloseIndex 关闭采集索引句柄并归并 WAL。幂等：已关闭时是空操作。
//
// 单独暴露是为了让「只关索引、不动事件段」的场景（Stop 的分步、测试的崩溃现场模拟）
// 不必调用完整的 Stop；调用后 Manager 不能再 persist，直到重新打开索引。
func (m *Manager) CloseIndex() error {
	if m == nil {
		return nil
	}
	// 与 persist 串行（锁序 persistGate → m.mu）：persist 的落库已在 m.mu 之外，若这里只取
	// m.mu 就关闭句柄，可能与正在进行的 ApplyScoped 撞上（关闭一个正在被写入的库）。
	m.persistGateEnsure()
	m.persistGate.mu.Lock()
	for m.persistGate.running {
		m.persistGate.cond.Wait()
	}
	m.persistGate.mu.Unlock()
	m.mu.Lock()
	store := m.index
	m.index = nil
	m.persistedRev = nil
	m.persistedCover = nil
	m.mu.Unlock()
	if store == nil {
		return nil
	}
	if err := store.Close(); err != nil {
		return fmt.Errorf("ingest: 关闭采集索引失败: %w", err)
	}
	return nil
}

// persistGate 是持久化的「单执行者 + 广播」门：一次只有一个 goroutine 在构建与落库，
// 其余调用方等待当前周期结束（而不是排到队尾各自再执行一遍）。
type persistGate struct {
	mu      sync.Mutex
	cond    *sync.Cond
	running bool
	// err 是最近一个已完成周期的结果，供等待者返回（不吞错误）。
	err error
}

// defaultPollConcurrency 是单轮采集的跨源并发度默认值（0 或负值取它，1 表示回到串行）。
//
// 为什么需要并发（FR-498 实测根因，2026-10-01）：单源单轮的时间几乎全部是**等待**而非算力——
// 60 源基线实测单轮 99.6s 里 pollSource=99.6s、其中 deliver=90.8s、而 deliver 中
// verify（VL 的 LogsQL 校验查询，含重试退避）=85.3s（占整轮 85.6%）；同期 Worker CPU 仅
// 17–45%、VL CPU 仅 7–23%，两边都没打满。而采集轮是**逐源串行**的，等于把 60 份网络/磁盘
// 等待直接叠加成轮周期（实测 ≈1.65s/源 → ≈99s/轮）→ 采集上限被钉在
// ≈60 源 × 2000 行/轮 ÷ 99s ≈ 1.2k 行/s，不足 30 行/s × 60 源 生成速率的 70%。
//
// 语义边界：并发只发生在**源之间**（每源一个工作项），**同源仍然串行**——同一源的
// poll → WAL 追加 → durable 持久化 → 投递 → 投影校验顺序一个字节都不变，因此暂停/回收/
// 滞回与「不丢数据」的语义不受影响；跨源本来就没有顺序契约（本轮之前它们只是被同一个
// 250ms ticker 依次驱动）。
const defaultPollConcurrency = 8

func (m *Manager) pollOnce() {
	m.cycleMu.Lock()
	defer m.cycleMu.Unlock()
	m.mu.Lock()
	type sourcePipe struct {
		source SourceConfig
		pipe   *pipeline.Pipeline
	}
	pipes := make([]sourcePipe, 0, len(m.pipes))
	for key, p := range m.pipes {
		pipes = append(pipes, sourcePipe{source: m.sources[key], pipe: p})
	}
	workers := m.pollConcurrency
	m.mu.Unlock()
	if workers <= 0 {
		workers = defaultPollConcurrency
	}
	if workers > len(pipes) {
		workers = len(pipes)
	}
	if workers < 1 {
		workers = 1
	}

	dirty := make([]bool, len(pipes))
	work := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				item := pipes[i]
				p := item.pipe
				before, beforeOK := p.Positions()
				events, metadataChanged, err := m.pollSource(item.source, p)
				after, afterOK := p.Positions()
				if before != after || beforeOK != afterOK || len(events) > 0 || metadataChanged || err != nil {
					dirty[i] = true
				}
				// 缺陷 A 自愈：暂停源不再读取新数据，投递/回收/恢复评估整条链失去触发点；
				// 这里对「已暂停或仍持有未解决缺口」的源做一次限频自愈（见 selfHealPausedSource）。
				if m.selfHealPausedSource(item.source, p) {
					dirty[i] = true
				}
				if err != nil {
					// The pipeline records the gap; the Worker process remains available.
					// 但静默吞掉错误会掩盖 reclaim/projection 持续失败（真机 64 源实测曾整轮 reclaim=0
					// 却无任何日志）。按源限流暴露一次，便于运维定位。
					if m.noteSourceError(item.source.LogSourceID, err.Error()) {
						slog.Warn("日志采集循环错误", "source", item.source.LogSourceID, "error", err)
					}
				}
			}
		}()
	}
	for i := range pipes {
		work <- i
	}
	close(work)
	wg.Wait()

	anyDirty := false
	for i := range dirty {
		if dirty[i] {
			anyDirty = true
			break
		}
	}
	if anyDirty {
		_ = m.persist()
	}
}

// ImportArchivesNow 手动/定时触发归档导入扫描：对指定源（storageNamespace 为空 = 全部源）
// 立刻走一次与采集轮**完全相同**的导入管道。
//
// 为什么复用 importArchives 而不是另写一条：自动路径上的三件事都必须逐字成立——
// ① 同一条清洗管道（normalize 全套 + 采样收口）；② 归属闸（跨代次拒绝，见 acquire.ArchiveImporter）；
// ③ 幂等（已导入的分段由 `seg.Imported` 跳过，重复触发零新增）。另写一条必然在这三点上漂移。
//
// 为什么必须带操作人：本入口会推进源位置与账本，属于改变状态的动作；无痕触发等于给
// "谁在什么时候灌了数据"留下空白。定时扫描由系统身份传入（见 archiveScanOperator），
// 同样留痕。
//
// 返回 (扫描到的待导入归档数, 实际导入的归档数)。
func (m *Manager) ImportArchivesNow(storageNamespace, operator string) (int, int, error) {
	if m == nil {
		return 0, 0, fmt.Errorf("ingest: manager unavailable")
	}
	if strings.TrimSpace(operator) == "" {
		return 0, 0, fmt.Errorf("ingest: 手动导入归档必须携带操作人（认证主体为空时拒绝执行）")
	}
	m.cycleMu.Lock()
	defer m.cycleMu.Unlock()
	m.mu.Lock()
	type target struct {
		source SourceConfig
		pipe   *pipeline.Pipeline
	}
	targets := make([]target, 0, len(m.pipes))
	for key, p := range m.pipes {
		src, ok := m.sources[key]
		if !ok {
			continue
		}
		if ns := strings.TrimSpace(storageNamespace); ns != "" && src.StorageNamespace != ns {
			continue
		}
		if src.Mode != pipeline.ModeFilePrimary || src.Path == "" {
			continue // 无文件源就没有归档可扫
		}
		targets = append(targets, target{source: src, pipe: p})
	}
	m.mu.Unlock()

	scanned, imported := 0, 0
	var firstErr error
	for _, tg := range targets {
		archives, err := discoverSourceArchives(tg.source)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		pending := pendingArchives(tg.pipe, archives)
		scanned += len(pending)
		if len(pending) == 0 {
			continue
		}
		events, _, err := importArchives(tg.pipe, pending)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		// 只有真正产出事件的归档才算"导入"（被归属闸拒绝的不计，它们只记缺口）。
		if len(events) > 0 {
			imported += len(pending)
		}
		slog.Info("手动导入归档扫描完成",
			"source", tg.source.LogSourceID, "generation", tg.source.SourceGeneration,
			"operator", operator, "pending", len(pending), "events", len(events))
	}
	if err := m.persist(); err != nil && firstErr == nil {
		firstErr = err
	}
	return scanned, imported, firstErr
}

// archiveScanOperator 是定时扫描的系统身份（定时动作同样必须留痕，只是主体是调度器）。
const archiveScanOperator = "scheduler:archive-scan"

// RunArchiveScan 按 interval 周期触发归档导入扫描；interval <= 0 表示**关闭**（默认关）。
//
// 为什么默认关：常规源在每个采集轮里已经自动发现并导入归档，定时扫描的价值只在
// "采集轮停了但归档还在攒"的场景（源被暂停/停止采集）。默认开会在每个部署上多出一份
// 周期性的目录扫描与账本写入，收益不明显而成本确定，故由配置显式打开。
//
// 返回一个 stop 函数；interval <= 0 时返回 nil（调用方据此知道未启用）。
func (m *Manager) RunArchiveScan(interval time.Duration) (stop func()) {
	if m == nil || interval <= 0 {
		return nil
	}
	done := make(chan struct{})
	var once sync.Once
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if _, _, err := m.ImportArchivesNow("", archiveScanOperator); err != nil {
					slog.Warn("定时归档导入扫描失败", "error", err)
				}
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}

// backfillArchiveUnderOwner 把一份归档记回**它自己的代次**账上（账本侧补账，不投递 VL）。
//
// 为什么只做账本侧、不调 deliver（这是与"补账通道"最关键的边界，源自一次硬点论证）：
// 投递路径的 ReclaimProof 钩子（releaseRecovery）按代次查 m.pipes 并要求存在活跃管道，
// 而"非活跃代次不得假装活跃"是既定约束 ⇒ 用旧代次走 deliver 必然失败；三条绕开它的出路
// （注册 headless 管道 / 跳过 ReclaimProof / 另造责任转移机制）分别违反既定约束或
// 动到不变量 R 与 VerifiedRuns 的底座，故一条都不走。
//
// 补账写成**旧代次的"待投递"账目**：
//   - 事件体入旧代次的段存（appendEvents）；
//   - 事件按 Durable 追加进旧代次的持久化 WAL（这就是它的 backlog）；
//   - 旧代次账本的位置推进到覆盖这批事件，并登记该归档为已导入；
//   - 整条记录回索引（持久化）。
//
// ⇒ 旧代次将来**再次活跃**时，其既有的 backlog 投递路径会自然把它们发出去（不丢）；
//
//	以新代次 event_id 重复入 VL 的路径被封死（不重）；当前代次名下永无归属（不归）。
//
// 返回错误即"补账不可行"，调用方必须退回"拒绝 + 记缺口"（不得静默改走当前代次）。
func (m *Manager) backfillArchiveUnderOwner(source SourceConfig, ownerGeneration, archivePath, objectID string) error {
	if m == nil {
		return fmt.Errorf("ingest: manager unavailable")
	}
	if strings.TrimSpace(ownerGeneration) == "" {
		return fmt.Errorf("ingest: 原代次为空，无法补账")
	}
	ownerKey := source.LogSourceID + "/" + ownerGeneration
	m.mu.Lock()
	saved, ok := m.state.Sources[ownerKey]
	m.mu.Unlock()
	if !ok || len(saved.Ledger) == 0 {
		// 寻址失败：该代次在原 Worker 的索引里没有账 —— 不可补，交给人工。
		return fmt.Errorf("ingest: 原代次 %q 的账本不可寻址", ownerGeneration)
	}

	// 用**原代次**构造导入器与账本：位置从原代次的既有水位继续（这才是"记回它自己的账"）。
	ownerSourceKey := ledger.SourceKey{LogSourceID: source.LogSourceID, SourceGeneration: ownerGeneration}
	ownerLedger := ledger.New()
	if err := ownerLedger.Restore(saved.Ledger); err != nil {
		return fmt.Errorf("ingest: 恢复原代次账本失败: %w", err)
	}
	// 刻意**不注入** lookupOwner/backfill：内层导入不再走归属闸（原代次就是它的家）。
	imp := acquire.NewArchiveImporter(ownerLedger, ownerSourceKey)
	res, err := imp.ImportGzip(archivePath)
	if err != nil {
		return fmt.Errorf("ingest: 原代次导入失败: %w", err)
	}
	if res == nil || len(res.Events) == 0 {
		// 没有事件可补：可能是已导入或空归档。登记已导入以免每轮重扫，不算失败。
		if res != nil {
			_ = imp.MarkImported(archivePath, res.ArchiveObjectID, res.ResumedFrom, res.ResumedFrom)
		}
		return m.commitBackfill(ownerKey, ownerLedger, nil, archivePath, res)
	}

	end := res.Events[len(res.Events)-1].Record.End
	if err := ownerLedger.AdvanceRead(ownerSourceKey, end); err != nil {
		return err
	}
	if err := ownerLedger.AdvanceDurable(ownerSourceKey, end); err != nil {
		return err
	}
	if err := imp.MarkImported(archivePath, res.ArchiveObjectID,
		res.Events[0].Record.Start, end); err != nil {
		return err
	}
	return m.commitBackfill(ownerKey, ownerLedger, res.Events, archivePath, res)
}

// commitBackfill 把补账结果写回内存状态并持久化（事件体入段存 + WAL 待投递账目 + 账本）。
func (m *Manager) commitBackfill(ownerKey string, ownerLedger *ledger.Ledger,
	events []logtypes.Event, archivePath string, res *acquire.ImportResult) error {
	// 事件体入原代次的段存：否则 WAL 引用恢复时取不到正文（B1a 的按条判据）。
	if len(events) > 0 {
		if err := m.appendEvents(ownerKey, events); err != nil {
			return fmt.Errorf("ingest: 补账事件入段存失败: %w", err)
		}
	}
	m.mu.Lock()
	cur := m.state.Sources[ownerKey]
	cur.Ledger = ownerLedger.Snapshot()
	// 追加为**已持久待投递**条目：旧代次再次活跃时由既有 backlog 路径外发。
	seq := uint64(len(cur.WAL) + len(cur.WALRefs))
	for _, ev := range events {
		seq++
		cur.WAL = append(cur.WAL, acquire.WALEntry{Seq: seq, Event: ev, Appended: true, Durable: true})
	}
	if len(events) > 0 {
		cur.EventsStored = true
		if through := events[len(events)-1].Record.End; through > cur.EventsStoredThrough {
			cur.EventsStoredThrough = through
		}
	}
	m.state.Sources[ownerKey] = cur
	m.mu.Unlock()

	if err := m.persist(); err != nil {
		return fmt.Errorf("ingest: 补账持久化失败: %w", err)
	}
	slog.Info("归档补账完成（记入原代次的待投递账目）",
		"ownerKey", ownerKey, "archive", archivePath, "events", len(events))
	if res != nil {
		_ = res
	}
	return nil
}

// AbandonedSourceRange 是一段被人工裁定为永久丢失的源位置区间（查询面数据）。
type AbandonedSourceRange struct {
	StorageNamespace string
	From             uint64
	To               uint64
	ReasonCode       string
	Operator         string
	AtUTC            string
}

// AbandonedRanges 返回与给定授权目标（形如 storage_namespace 或 storage_namespace/YYYY-MM-DD）
// 相关的、已被人工裁定永久丢失的源位置区间。
//
// 目标匹配用**前缀**（"<ns>/"）：调用方给的是 catalog 分区键（ns/day，见
// catalog.PartitionKey.String），而放弃凭据是按**源**（存储命名空间）存的。
// 用前缀而不是解析"取斜杠前那段"，是为了不依赖日期格式——解析格式一旦变了，
// 匹配会静默失配、标记静默消失（比不实现更坏）。
//
// 未接线/空的目标集表示「本 Worker 全部源」（与 planner「空 = 全部 Catalog 分区」同一口径）：
// 此时不按命名空间过滤，返回全部源的放弃凭据——否则"没传目标"会表现成"没有永久丢失"，
// 那正是静默跳过。
func (m *Manager) AbandonedRanges(targetIDs []string) []AbandonedSourceRange {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	pipes := make(map[string]*pipeline.Pipeline, len(m.pipes))
	namespaces := make(map[string]string, len(m.pipes)) // key -> storage namespace
	for key, p := range m.pipes {
		pipes[key] = p
		if src, ok := m.sources[key]; ok {
			namespaces[key] = src.StorageNamespace
		}
	}
	m.mu.Unlock()

	var out []AbandonedSourceRange
	for key, p := range pipes {
		ns := namespaces[key]
		if ns == "" {
			continue
		}
		if len(targetIDs) > 0 && !targetMatchesNamespace(targetIDs, ns) {
			continue
		}
		entry := p.Ledger().Get(p.Key())
		if entry == nil {
			continue
		}
		for _, ab := range entry.Abandonments {
			out = append(out, AbandonedSourceRange{
				StorageNamespace: ns, From: ab.From, To: ab.To,
				ReasonCode: ab.ReasonCode, Operator: ab.Operator, AtUTC: ab.AtUTC,
			})
		}
	}
	return out
}

// targetMatchesNamespace 报告授权目标里是否有指向该命名空间的分区键。
func targetMatchesNamespace(targetIDs []string, ns string) bool {
	prefix := ns + "/"
	for _, t := range targetIDs {
		if t == ns || strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return false
}

// archiveOwnerLookup 返回「归档归属代次」查询：在**既有持久化**里枚举同一 LogSourceID 的
// 各个代次，回答某个归档对象已被哪个代次导入过。
//
// 为什么用 m.state 而不是新建注册表（零迁移）：归档的逐件账本来就落在**账本分段**里
// （Segment{Kind: SegmentGzip, Path, ArchiveObjectID}），而分段存在以 (LogSourceID, 代次) 为键的
// 账本条目中、并随索引持久化。因此「同源各代次可枚举」这件事不需要新表——只需把已持久化的
// 各代次条目翻一遍。新表 + 迁移的风险（迁移期不可用、双写不一致）远大于这一次线性扫描的收益：
// 调用点是**每个待导入归档一次**，而不是每批事件一次。
//
// 判据口径与归档侧一致：规范化路径 + archive_object_id（后者已含大小与内容哈希，
// 见 ArchiveObjectID）——即 (源ID, 规范化路径, 大小, 内容哈希)。
//
// 查不到就是查不到：返回 found=false，由调用方按「来源不可追溯」拒绝导入，
// **绝不回退成「假设当前代次」**。
func (m *Manager) archiveOwnerLookup() func(logSourceID, cleanPath, objectID string) (string, bool) {
	return func(logSourceID, cleanPath, objectID string) (string, bool) {
		if m == nil || logSourceID == "" || objectID == "" {
			return "", false
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, saved := range m.state.Sources {
			for i := range saved.Ledger {
				entry := &saved.Ledger[i]
				if entry.Key.LogSourceID != logSourceID {
					continue
				}
				for j := range entry.Segments {
					seg := &entry.Segments[j]
					if seg.Kind != ledger.SegmentGzip || seg.ArchiveObjectID != objectID {
						continue
					}
					if filepath.Clean(seg.Path) != cleanPath {
						continue
					}
					return entry.Key.SourceGeneration, true
				}
			}
		}
		return "", false
	}
}

func (m *Manager) pollSource(source SourceConfig, p *pipeline.Pipeline) ([]logtypes.Event, bool, error) {
	if source.Mode != pipeline.ModeFilePrimary || source.Path == "" {
		events, err := p.Poll()
		return events, false, err
	}
	archives, err := discoverSourceArchives(source)
	if err != nil {
		_ = p.Ledger().RecordGap(p.Key(), 0, 0, "ARCHIVE_DISCOVERY_FAILED", err.Error())
		return nil, false, err
	}
	pending := pendingArchives(p, archives)
	if len(pending) > 0 {
		p.SetRotateTarget(pending[len(pending)-1])
	}

	// A fresh binding imports historical closed segments before assigning
	// positions to the current latest.log.
	var all []logtypes.Event
	metadataChanged := false
	if p.CurrentSegmentUnread() && len(pending) > 0 {
		imported, changed, importErr := importArchives(p, pending)
		all = append(all, imported...)
		metadataChanged = metadataChanged || changed
		if importErr != nil {
			return all, metadataChanged, importErr
		}
		if pos, ok := p.Positions(); ok {
			if err := p.RebaseCurrentSegment(pos.Durable); err != nil {
				return all, metadataChanged, err
			}
		}
		pending = pendingArchives(p, archives)
	}

	rotated, err := p.PrepareRotation()
	if err != nil {
		return all, metadataChanged, err
	}
	if rotated {
		metadataChanged = true
		closed, closeErr := p.FlushClosedSegment()
		all = append(all, closed...)
		if closeErr != nil {
			return all, metadataChanged, closeErr
		}
		if confirmErr := p.ConfirmRotationCoverage(); confirmErr != nil {
			return all, metadataChanged, confirmErr
		}
		if len(pending) == 0 {
			_ = p.Ledger().RecordGap(p.Key(), 0, 0, "ROTATED_SEGMENT_NOT_READY", "replacement latest.log detected before its rotated segment became readable")
			return all, metadataChanged, fmt.Errorf("ingest: rotated segment is not available yet")
		}
		imported, changed, importErr := importArchives(p, pending)
		all = append(all, imported...)
		metadataChanged = metadataChanged || changed
		if importErr != nil {
			return all, metadataChanged, importErr
		}
		if pos, ok := p.Positions(); ok {
			if err := p.RebaseCurrentSegment(pos.Durable); err != nil {
				return all, metadataChanged, err
			}
		}
	}

	polled, pollErr := p.Poll()
	all = append(all, polled...)
	// 缺陷 B：源静默时的唯一出路。未闭合缓冲平时只由「下一行到达」推进，源一旦长时间
	// 不再输出，缓冲既不产出事件也不推进 durable——而轮转恢复要靠 durable 覆盖已读前缀，
	// 于是这段悬挂区间既没被覆盖、也没有事件，链路卡在「轮转分段不可读」。
	// 放在 Poll 之后：轮转已在上面按 cross_rotation 语义闭合过，这里只处理「没有轮转、
	// 也没有新行」的静默形态（两者互斥，不会双发同一条悬挂记录）。
	if m.multilineUnclosedTimeout > 0 {
		if stale, staleErr := p.FlushStaleMultiline(time.Now(), m.multilineUnclosedTimeout); staleErr != nil {
			m.noteSourceError(source.LogSourceID, staleErr.Error())
		} else if len(stale) > 0 {
			all = append(all, stale...)
			metadataChanged = true
		}
	}
	return all, metadataChanged, pollErr
}

func discoverSourceArchives(source SourceConfig) ([]string, error) {
	pattern := strings.TrimSpace(source.ArchiveGlob)
	if pattern == "" {
		pattern = filepath.Join(filepath.Dir(source.Path), "*.gz")
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	sort.Slice(matches, func(i, j int) bool {
		leftName, rightName := filepath.Base(matches[i]), filepath.Base(matches[j])
		if leftName != rightName {
			return leftName < rightName
		}
		return matches[i] < matches[j]
	})
	return matches, nil
}

func pendingArchives(p *pipeline.Pipeline, paths []string) []string {
	entry := p.Ledger().Get(p.Key())
	imported := make(map[string]bool)
	if entry != nil {
		for _, segment := range entry.Segments {
			if segment.Kind == ledger.SegmentGzip && segment.Imported {
				imported[filepath.Clean(segment.Path)] = true
				continue
			}
			if segment.Kind == ledger.SegmentGzip && segment.ImportError != "" {
				if info, err := os.Stat(segment.Path); err == nil && info.Size() == segment.ObservedSize &&
					info.ModTime().UnixNano() == segment.ObservedModNano {
					imported[filepath.Clean(segment.Path)] = true
				}
			}
		}
	}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if !imported[filepath.Clean(path)] {
			out = append(out, path)
		}
	}
	return out
}

func importArchives(p *pipeline.Pipeline, paths []string) ([]logtypes.Event, bool, error) {
	var out []logtypes.Event
	changed := false
	for _, path := range paths {
		result, err := p.ImportArchive(path)
		if result != nil {
			changed = true
			out = append(out, result.Events...)
		}
		if err != nil {
			return out, changed, err
		}
	}
	return out, changed, nil
}

// deliveryTiming 记录一次投递内各阶段的耗时（**纯观测**，不改任何逻辑）。
//
// 为什么要有它：投递路径的单批耗时构成在现场不可见——`投影写入开始/结束` 只有秒级精度，
// verify 有自己的一条打点，而「进 deliver 到出 deliver」之间的其余阶段（身份集合重建、
// 权威集合构建、代次探测、缺口消解）此前没有任何读数。单位行成本 8.3× 的定位缺的就是它。
// 输出为 debug 级：生产默认 info 不受影响，压测/夹具把 log.level 调成 debug 即可。
type deliveryTiming struct {
	key    string
	events int
	start  time.Time
	last   time.Time
	marks  []string
	// sink 非 nil 时把阶段串交给测试观测口（见 Manager.stageSink）。
	sink func(string)
}

func newDeliveryTiming(key string, events int, sink func(string)) *deliveryTiming {
	now := time.Now()
	return &deliveryTiming{key: key, events: events, start: now, last: now, sink: sink}
}

// mark 记录自上一个 mark 起（或函数入口起）的耗时。
func (t *deliveryTiming) mark(stage string) {
	if t == nil {
		return
	}
	now := time.Now()
	t.marks = append(t.marks, fmt.Sprintf("%s=%dms", stage, now.Sub(t.last).Milliseconds()))
	t.last = now
}

// report 输出一批的阶段计时。
//
// 为什么用环境变量而不是日志级别门控：Worker 的 `log.level` 配置目前没有接到 slog 的
// handler 级别上（`log.level: debug` 不产生任何 DEBUG 输出，实测），因此 debug 级打点
// 在现场与夹具里都取不到。这里用显式开关：生产默认关闭（不刷屏），
// `JIANMANAGER_STAGE_TIMING=1` 打开（压测/夹具/定位现场）。
func (t *deliveryTiming) report() {
	if t == nil {
		return
	}
	if t.sink != nil {
		t.sink(strings.Join(t.marks, " "))
	}
	if !stageTimingEnabled() {
		return
	}
	slog.Info("采集批次阶段耗时",
		"source", t.key, "events", t.events,
		"stages", strings.Join(t.marks, " "), "totalMs", time.Since(t.start).Milliseconds())
}

var (
	stageTimingOnce sync.Once
	stageTimingOn   bool
)

func stageTimingEnabled() bool {
	stageTimingOnce.Do(func() { stageTimingOn = os.Getenv("JIANMANAGER_STAGE_TIMING") == "1" })
	return stageTimingOn
}

// deliveryTailPlan 描述「本批能否作为权威集合的增量尾部直接处理」。
//
// 背景（FR-498 单位行成本 8.3×，2026-10-02 实测定位）：
// 投递路径原先每批要做三次 O(段总行数) 的操作——身份集合重建（deliver）、权威全量构建
// （canonicalRecoveryEvents）、段行数校准（appendEvents 的 Count）。段**只增不减**且没有裁剪
// 路径（契约 §4.3/§5.3 要求它作为 VL 数据根丢失后的权威集合），于是单源段在生产上已达数十万行
// （全库 18 GB / 63 源），微基准实测单次遍历 8.7 µs/行 ⇒ 每批数秒，且成本随运行时长线性增长。
// 实验室（段从零开始）没有这个成本——这正是 0.36 → 2.95 ms/行（8.3×）的主因，
// 也解释了「资源全闲但达成率 0.79」：时间花在单线程的磁盘遍历上，不是算力。
type deliveryTailPlan struct {
	// Fast 为真时：本批区间整体在「段已覆盖水位」与「已投递水位」之后，且没有重放/待发布语义
	// ⇒ 三段 O(段) 操作全部可省。
	Fast bool
	// StoredRows 是段存储已落行数（快路径下由缓存给出，避免每批 Count 校准末段）。
	StoredRows int
	// MaxEnd 是权威集合的最大 record_end（缓存水位与本批取大，单调不减）。
	MaxEnd uint64
	// Events 是本批要追加进段存储的事件（快路径下即权威集合的增量尾部）。
	Events []logtypes.Event
}

// planDeliveryTail 判定本批是否可以走「增量尾部」快速路径，并给出段追加与发布水位所需的读数。
//
// 判据（三条全部成立才快，任一不成立即回退到原全量路径）：
//  1. **语义上不需要权威全量**：不是重放、没有待发布语义（「代次预算耗尽」已在 deliver 里并入
//     replace 判定，故这里只认 replay/PublicationPending）；
//  2. **事件体已在段存储**：否则权威集合在内联切片里，重建本来便宜，无需快路径；
//  3. **本批区间整体在水位之后**：最小 record_start >= 段已覆盖水位（不与段内容重叠 ⇒ 无重复可去）
//     且 >= 已投递水位（该区间从未投递过 ⇒ 身份集合里不可能命中）。
//
// 安全论证（为什么跳过身份重建不削弱保证）：
//   - 身份集合的唯一用途是「跳过已写入的同 ID 事件」与「检出同 ID 不同哈希」。重复只可能来自
//     「该区间曾经投递过」——判据 3 的第二条把它排除。
//   - 偶发的 IDENTITY_CONFLICT（同 ID 不同哈希）不会被静默放过：两行都会写入 VL，
//     随后**逐字段可见性校验**会以 duplicate / content mismatch 失败（fail loud，不是 fail silent）。
//   - tailer 回退/重读同一区间：第一次投递成功会推进已投递水位，第二次因 minStart < 水位
//     自动回退慢路径，仍走全量去重；第一次失败（水位未推进）时的重投本就是必须的重试。
func (m *Manager) planDeliveryTail(key string, saved persistedSource, events []logtypes.Event, replay bool) deliveryTailPlan {
	plan := deliveryTailPlan{}
	if replay || saved.PublicationPending || m.events == nil || len(events) == 0 {
		return plan
	}
	// 权威集合仍在**内联切片**里时必须走全量路径：那时重建本来就便宜，而且去重是必需的。
	// 注意判据不是 EventsStored：该标记是 state 的字段，段已存在但 state 新建/重置时它为假
	// （夹具与「换机重挂数据根」都命中这一形态），此时权威集合其实已经在段里。
	if len(saved.Events) > 0 {
		return plan
	}
	p, ok := m.pipeFor(key)
	if !ok {
		return plan
	}
	positions, ok := p.Positions()
	if !ok {
		return plan
	}
	minStart, maxEnd := events[0].Record.Start, events[0].Record.End
	for _, e := range events[1:] {
		if e.Record.Start < minStart {
			minStart = e.Record.Start
		}
		if e.Record.End > maxEnd {
			maxEnd = e.Record.End
		}
	}
	if minStart < saved.EventsStoredThrough || minStart < positions.Delivery {
		return plan
	}
	stored, ok := m.cachedStoredRows(key)
	if !ok {
		return plan
	}
	plan.Fast = true
	plan.StoredRows = stored
	plan.MaxEnd = m.advanceSourceMaxEnd(key, maxEnd)
	return plan
}

// pipeFor 返回该源的采集管道（快照语义；调用方只读 Positions）。
func (m *Manager) pipeFor(key string) (*pipeline.Pipeline, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pipes[key]
	return p, ok
}

// cachedStoredRows 返回段存储已落行数：首次用 Count 建立基线（含末段校准），此后按追加增量推进。
// 本进程是段的唯一写者，且段没有任何裁剪/改写路径（契约要求的「只换介质不裁剪」），
// 故缓存在进程生命周期内有效。
func (m *Manager) cachedStoredRows(key string) (int, bool) {
	if m.events == nil {
		return 0, false
	}
	m.mu.Lock()
	n, ok := m.eventsStoredRows[key]
	m.mu.Unlock()
	if ok {
		return n, true
	}
	n, err := m.events.Count(key)
	if err != nil {
		return 0, false
	}
	m.mu.Lock()
	if m.eventsStoredRows == nil {
		m.eventsStoredRows = map[string]int{}
	}
	m.eventsStoredRows[key] = n
	m.mu.Unlock()
	return n, true
}

// advanceSourceMaxEnd 推进并返回该源「权威集合的最大 record_end」（单调不减的水位缓存）。
//
// 为什么不直接遍历权威全量：那正是 8.3× 的成本来源之一（快路径下 events 只含本批，
// 而全量遍历要读该源全部历史事件）。水位的三个来源都单调不减：
//   - state 的 EventsStoredThrough（段已覆盖的最大 record_end，持久化字段）；
//   - 采集管道的 Durable 水位（WAL 已持久化到的 record_end，覆盖「已落 WAL 但未落段」的部分）；
//   - 本次 events 的最大 record_end。
//
// 三者取大即权威全量的 max：权威集合 = 段内容 + durable WAL（见 canonicalRecoveryEvents），
// 而两部分的右端分别由前两个水位给出。
func (m *Manager) advanceSourceMaxEnd(key string, batchMax uint64) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sourceMaxEnd == nil {
		m.sourceMaxEnd = map[string]uint64{}
	}
	cur := m.sourceMaxEnd[key]
	if saved, ok := m.state.Sources[key]; ok && saved.EventsStoredThrough > cur {
		cur = saved.EventsStoredThrough
	}
	if p, ok := m.pipes[key]; ok && p != nil {
		if pos, ok := p.Positions(); ok && pos.Durable > cur {
			cur = pos.Durable
		}
	}
	if batchMax > cur {
		cur = batchMax
	}
	m.sourceMaxEnd[key] = cur
	return cur
}

// projectionGenerationsExhausted 报告该源已发布的投影代次是否已达上限（需要整窗重发刷新）。
func (m *Manager) projectionGenerationsExhausted(source SourceConfig) bool {
	if m.cat == nil {
		return false
	}
	catKey := catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay}
	current, ok := m.cat.Get(catKey)
	if !ok {
		return false
	}
	return len(publishedSourceProjectionGenerations(current.PublishedProjection, source)) >= maxPublishedProjectionGenerations
}

func (m *Manager) deliver(source SourceConfig, events []logtypes.Event, replay bool) (pipeline.DeliveryResult, error) {
	if len(events) == 0 {
		return pipeline.DeliveryResult{HTTPStatus: 204}, nil
	}
	if m.vl == nil && m.vlRoute == nil {
		return pipeline.DeliveryResult{HTTPStatus: 0}, fmt.Errorf("ingest: VictoriaLogs client is not ready")
	}
	// 阶段计时（纯观测）：投递路径的单批耗时构成在现场不可见，8.3× 单位成本定位缺的就是它。
	timing := newDeliveryTiming(source.LogSourceID+"/"+source.SourceGeneration, len(events), m.stageSink)
	defer timing.report()
	key := source.LogSourceID + "/" + source.SourceGeneration
	m.mu.Lock()
	saved := m.state.Sources[key]
	m.mu.Unlock()
	// 增量尾部快速路径（判据与安全论证见 planDeliveryTail）：满足时跳过「遍历该源全部历史事件」
	// 重建身份集合——这是单位行成本里最贵的一项（生产单源段实测可达 70 万行，微基准
	// 单次遍历 8.7 µs/行 ≈ 6 秒/批，而段只增不减，成本随运行时长线性增长）。
	tail := m.planDeliveryTail(key, saved, events, replay)
	var seen map[string]string
	if tail.Fast {
		seen = make(map[string]string, len(events))
		timing.mark("seen-skipped")
	} else {
		seen = make(map[string]string, len(saved.Events))
		m.mu.Lock()
		for _, event := range saved.Events {
			event, err := normalizeCanonicalEvent(event)
			if err != nil {
				m.mu.Unlock()
				return pipeline.DeliveryResult{}, err
			}
			seen[event.EventID] = event.CanonicalHash
		}
		eventsStored := saved.EventsStored && m.events != nil
		m.mu.Unlock()
		// 事件体已落段时，身份集合从段流式重建（不持全量切片）；旧格式仍走内联切片。
		if eventsStored {
			if err := m.events.Iterate(key, func(event logtypes.Event) error {
				normalized, err := normalizeCanonicalEvent(event)
				if err != nil {
					return err
				}
				seen[normalized.EventID] = normalized.CanonicalHash
				return nil
			}); err != nil {
				return pipeline.DeliveryResult{}, err
			}
		}
		timing.mark("seen")
	}
	m.mu.Lock()
	toWrite := make([]logtypes.Event, 0, len(events))
	for _, event := range events {
		if event.Record.End <= event.Record.Start {
			m.mu.Unlock()
			return pipeline.DeliveryResult{HTTPStatus: http.StatusUnprocessableEntity}, fmt.Errorf("ingest: zero-width event %s", event.EventID)
		}
		var err error
		event, err = normalizeCanonicalEvent(event)
		if err != nil {
			m.mu.Unlock()
			return pipeline.DeliveryResult{HTTPStatus: http.StatusUnprocessableEntity}, err
		}
		if existing, exists := seen[event.EventID]; exists {
			if existing != event.CanonicalHash {
				m.mu.Unlock()
				return pipeline.DeliveryResult{}, fmt.Errorf("ingest: IDENTITY_CONFLICT for event_id %s", event.EventID)
			}
			continue
		}
		toWrite = append(toWrite, event)
		seen[event.EventID] = event.CanonicalHash
	}
	m.mu.Unlock()
	timing.mark("toWrite")
	if len(toWrite) == 0 {
		// 本批事件此前已写入且逐字段一致（seen 命中同 ID 同哈希）：同样是「该区间已落库」的证据，
		// 故与写成功路径一样自动消解被它覆盖的缺口。
		m.resolveGapsLandedByDelivery(source, events)
		return pipeline.DeliveryResult{HTTPStatus: 204}, nil
	}
	// 权威全量的构建是 O(段行数)（生产单源实测可达 70 万行 ≈ 6 秒）；只有「整窗重发」语义
	// （重放 / 待发布 / 代次数达上限）才需要它作为写入面与证据面。快路径下本批就是权威集合的
	// 增量尾部，写入面 / 证据面 / 段追加 / 发布水位全部可由「本批 + 缓存水位」给出，
	// 故跳过全量重建（判据见 planDeliveryTail）。
	replace := saved.PublicationPending || replay
	if !replace && m.projectionGenerationsExhausted(source) {
		// 与 writeProjectionDay 内同一判据等价：它只取决于**本源自己**已发布的代次数，
		// 而同源的投递是串行的，所以提前到这里判定不会与后续判定不一致；
		// 提前的目的是在构建权威全量**之前**就知道它是否被需要。
		replace = true
	}
	var expected []logtypes.Event
	if tail.Fast && !replace {
		expected = toWrite
		timing.mark("recovery-skipped")
	} else {
		var err error
		expected, err = m.canonicalRecoveryEvents(key, saved)
		if err != nil {
			return pipeline.DeliveryResult{}, err
		}
		timing.mark("recovery")
	}
	// 代次名必须是 VL 中尚未出现过的名字：状态回滚/重置后计数器会回退，直接进位会重名
	// （见 nextFreeProjectionGeneration 的事故说明）。
	generation := m.nextFreeProjectionGeneration(source, saved.ProjectionGeneration)
	timing.mark("probe")
	writeEvents := toWrite
	if saved.PublicationPending {
		writeEvents = expected
	}
	// 取代 vs 累积（2026-09-30 生产实证）：`expected` 是该源的**全量** canonical 集 ✓，
	// `toWrite` 是**本批** ✓，且本批恒为全量的子集 ✓。故当两者**条数相等**时，本次写入
	// 已覆盖全量 → 新代包含旧代的一切 → **取代**安全；否则是增量微代次 → 必须累积
	// （既有测试 TestManagerNewBatchPublishesIsolatedView 守此契约）。
	// 此前的漏洞：只认 PublicationPending，**重启后的全量重放也走累积** ✗ → 查询并集无界
	// （实测 7 代）→ VL 排序超 51MB → 400 → 该目标恒 not_ready。
	// 取代 vs 累积（2026-09-30 生产实证）：replay=本次是「存量重放」（由 deliverPendingBestEffort
	// 发起，见 acquire/pipeline.go）；**只有它写入的是全量** → 新代包含旧代一切 → 取代安全
	// （旧代纯冗余）；普通批次仍累积（既有测试 TestManagerNewBatchPublishesIsolatedView 守此契约）。
	// 此前只认 PublicationPending，**重启后的存量重放也走累积** ✗ → 查询并集无界（实测 7 代）
	// → VL 排序超 51MB → 400 → 该目标恒 not_ready。
	// 注：曾试过用数据形状判别（EventsStored / EventsStoredThrough / eventsStored /
	// len(toWrite)==len(expected)），全部被证伪：真实重放的批次只是全量的子集。
	replace = replace || saved.PublicationPending || replay
	// 本次**实际写入面**（校验逐字段覆盖的就是它）：
	// replace 为真时 writeProjectionPlan 会把写入面扩为权威全量（整窗重发），否则就是本批。
	// 自动消解必须按实际写入面取证据，否则重放会「写了却没消解」，缺口又只能等人工。
	evidence := writeEvents
	if replace {
		evidence = expected
	}
	plan := projectionWritePlan{Write: writeEvents, Archive: toWrite, Replace: replace}
	if tail.Fast && !replace {
		// 增量尾部：段追加走 O(本批) 路径、发布水位走缓存（见 projectionWritePlan.Tail）。
		tail.Events = toWrite
		plan.Tail = &tail
	}
	result, err := m.writeProjectionPlan(source, expected, generation, plan)
	if err != nil {
		return result, err
	}
	timing.mark("write")
	// 投递成功（写 VL + 逐字段可见性校验 + catalog 发布）= 该写入面已确认落库 → 自动消解
	// 被它完全覆盖的缺口。这是缺陷 A 的「成功后自动消解」出口：重投成功即清零，不再依赖人工。
	m.resolveGapsLandedByDelivery(source, evidence)
	timing.mark("resolve")
	if os.Getenv("JIANMANAGER_LOG_INJECT_ACK_LOSS") == "1" {
		result.AckLost = true
	}
	return result, nil
}

// projectionWritePlan 描述一次投影写入的「写入面」与「发布语义」（FR-497 spec §2.2）。
//
// 为什么要拆开：重启恢复原先只有「整窗重发」一种形态——Replace=true 时把写入面**扩为
// 权威全量**。启动增量对账需要「只写缺失天，但发布语义仍是取代」（新代包含该天全量 →
// 取代安全），于是写入面与发布语义必须能分别指定。
type projectionWritePlan struct {
	// Write 是本次实际写入 VL 的事件集合。
	Write []logtypes.Event
	// Archive 是同时落归档的权威事件集合（现状恢复路径为 nil）。
	Archive []logtypes.Event
	// Replace 为 true 表示发布语义是「新代取代旧代白名单」（该天）；false 表示累积微代次。
	Replace bool
	// Days 非空表示**限定写入面**为这些 UTC 天（增量补天）：此时 Replace 不再把写入面扩为
	// 权威全量，且 Days 必须与 Write 的天集合完全一致——错位会「标记为已发布却没写数据」，
	// 故按硬失败处理（宁可启动失败，不接受静默丢数据）。
	Days []string
	// Tail 非 nil 表示本次是「权威集合的增量尾部」：段存储按增量追加、发布水位走缓存，
	// 不需要（也没有）权威全量（判据与安全论证见 deliveryTailPlan）。
	Tail *deliveryTailPlan
}

func (m *Manager) writeProjection(source SourceConfig, events []logtypes.Event, generation string, writeEvents, archiveEvents []logtypes.Event, replace bool) (pipeline.DeliveryResult, error) {
	return m.writeProjectionPlan(source, events, generation, projectionWritePlan{
		Write: writeEvents, Archive: archiveEvents, Replace: replace,
	})
}

// writeProjectionPlan 是 writeProjection 的完整形态：按计划写入 VL、校验、发布、落段。
//
// events 恒为权威全量（用于段存储补齐与 persist）；实际写入面由 plan 决定。
func (m *Manager) writeProjectionPlan(source SourceConfig, events []logtypes.Event, generation string, plan projectionWritePlan) (pipeline.DeliveryResult, error) {
	if len(events) == 0 {
		return pipeline.DeliveryResult{HTTPStatus: http.StatusNoContent}, nil
	}
	for _, event := range events {
		if event.Record.End <= event.Record.Start {
			return pipeline.DeliveryResult{HTTPStatus: http.StatusUnprocessableEntity}, fmt.Errorf("ingest: zero-width event %s", event.EventID)
		}
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	// 该代次名从此刻起可能出现在 VL 中（写入可能只完成一部分就失败）——因此**先**把它记入
	// 探测水位（复审 P2-11）：水位必须是「本进程为该源写过/尝试写过的最大序号」，否则一个
	// 半途失败的写入之后，同名候选会被缓存判成「未写过 ⇒ 未占用」，把重名风险重新引进来。
	m.recordProjectionGenerationAttempt(key, generation)
	m.mu.Lock()
	saved := m.state.Sources[key]
	saved.ProjectionGeneration = generation
	saved.PublicationPending = true
	m.state.Sources[key] = saved
	m.mu.Unlock()
	// The attempted generation is durable before any VL write. A lost response
	// or failed Catalog commit must rebuild into a different physical target.
	if err := m.persist(); err != nil {
		return pipeline.DeliveryResult{}, err
	}
	grouped, days, err := groupEventsByUTCDay(source, events)
	if err != nil {
		return pipeline.DeliveryResult{}, err
	}
	archiveGrouped, _, err := groupEventsByUTCDay(source, plan.Archive)
	if err != nil {
		return pipeline.DeliveryResult{}, err
	}
	writeGrouped, writeDays, err := groupEventsByUTCDay(source, plan.Write)
	if err != nil {
		return pipeline.DeliveryResult{}, err
	}
	switch {
	case len(plan.Days) > 0:
		// 增量补天：写入面已由调用方按天过滤；这里只校验二者一致并固定顺序。
		if err := sameUTCDays(writeDays, plan.Days); err != nil {
			return pipeline.DeliveryResult{}, err
		}
		writeDays = sortUTCDays(plan.Days)
	case plan.Replace:
		// 整窗重发（现状语义）：写入面扩为权威全量。
		writeDays = days
		writeGrouped = grouped
	}
	result := pipeline.DeliveryResult{HTTPStatus: http.StatusNoContent}
	for _, day := range writeDays {
		daySource := source
		daySource.UTCDay = day
		dayResult, writeErr := m.writeProjectionDay(daySource, grouped[day], writeGrouped[day], generation, archiveGrouped[day], plan.Replace, plan.Tail != nil)
		if dayResult.HTTPStatus != 0 {
			result.HTTPStatus = dayResult.HTTPStatus
		}
		if writeErr != nil {
			return result, writeErr
		}
	}
	m.mu.Lock()
	saved = m.state.Sources[key]
	saved.ProjectionGeneration = generation
	saved.PublicationPending = false
	m.state.Sources[key] = saved
	m.mu.Unlock()
	// 事件体写入磁盘段（先内容、后引用）：写入成功并 fsync 后，再让 state 元数据引用它。
	// 契约 §4.3/§5.3 要求这是 VL 数据根丢失后重建 projection 的权威集合，故不裁剪只换介质。
	//
	// 快路径（plan.Tail）下 events 就是「权威集合的增量尾部」，直接追加即可：
	// 省掉 appendEvents 的 Count（末段校准 = O(末段行数)）与全量比对。
	if plan.Tail != nil {
		if err := m.appendEventTail(key, *plan.Tail); err != nil {
			return result, err
		}
	} else if err := m.appendEvents(key, events); err != nil {
		return result, err
	}
	// 这里**不再**单独落库一次：本批次引发的 state 变更（投影代次 / 发布待定 / 段覆盖水位）
	// 会被随后任意一次 persist 覆盖，而 persist 是「按变更源重建 + 全量根表比对」的增量落库——
	// 后一次落库总是把前一次的遗漏补齐。紧随其后的落库点有两个：同源下一批的 DurablePersist
	// （VL 插入前的契约落库）与轮末的统一 persist（pollOnce 的 dirty 分支）。
	//
	// 为什么必须去掉（FR-498 并发采集轮的连带修复）：每源每批两次落库时，并发轮里 8 个 worker
	// 会在全局串行的持久化上排长队，把「短变更」调用者（实例登记）挤到队尾。去掉后单源每批
	// 只落库一次（VL 插入前那次，契约不变），持久化总量与排队长度减半。
	//
	// 崩溃语义没有变弱：投影状态最多晚一次落库到达索引（≤1 轮），而恢复路径本来就不依赖
	// state 里的投影标记——事件段存储是权威集合（见 canonicalRecoveryEvents），代次重名由
	// VL 侧占用探测兜底（见 nextFreeProjectionGeneration）。
	return result, nil
}

// appendEvents 把该源的 canonical 事件集合同步到磁盘段，并把该源标记为“事件体已落段”。
//
// 入参是**完整**权威集合（与旧版 `saved.Events` 的语义一致），函数内部只追加尚未落段的
// 尾部：段内已有事件数就是已落段前缀长度，而权威集合始终以该前缀开头（由
// canonicalRecoveryEvents 的构造顺序保证：先段内容、后 durable WAL），因此只需 append
// 其后的差额。这样每轮 poll 都幂等，不会把同一事件重复写入段。
//
// 标记与落段在同一临界区内推进：只有段写成功（已 fsync）后 state 才会引用它。
func (m *Manager) appendEvents(key string, events []logtypes.Event) error {
	if m.events == nil || len(events) == 0 {
		return nil
	}
	stored, err := m.events.Count(key)
	if err != nil {
		return err
	}
	if stored > len(events) {
		// 段内已落段事件多于权威集合：说明段内容与清单不一致（例如密封段被外部改写、
		// 或恢复集合构造有漏）。此时继续执行会静默“承认”一段谁也无法解释的前缀，
		// 并让缺失事件永久消失，故必须硬失败而非跳过追加。
		return fmt.Errorf("ingest: eventstore ahead of authoritative set for %s: stored=%d authoritative=%d", key, stored, len(events))
	}
	if stored < len(events) {
		if err := m.events.Append(key, events[stored:]); err != nil {
			return err
		}
	}
	m.mu.Lock()
	saved := m.state.Sources[key]
	saved.EventsStored = true
	// B1a：记录段存储已覆盖到的最大 record_end。`events` 是权威集合（按记录位置有序），
	// 其最大 record_end 即段存储已保证覆盖的范围；WAL 持久化据此按条决定「引用 or 内联」。
	var covered uint64
	for _, ev := range events {
		if ev.Record.End > covered {
			covered = ev.Record.End
		}
	}
	if covered > saved.EventsStoredThrough {
		saved.EventsStoredThrough = covered
	}
	// 已落段后不再保留内联副本，否则常驻切片会重新把 RSS 推高（FR-484 目标）。
	saved.Events = nil
	m.state.Sources[key] = saved
	m.mu.Unlock()
	return nil
}

// appendEventTail 把「增量尾部」事件追加进段存储（投递快路径专用）。
//
// 与 appendEvents 的差别：后者的入参是**权威全量**，需要 Count 校准末段、与 len(events) 比对后
// 追加差额；快路径已由 planDeliveryTail 保证「本批区间整体在段已覆盖水位之后」，
// 因此可以直接追加，行数走缓存（StoredRows），不再每批做 O(末段) 的 Count。
//
// 状态推进与 appendEvents 逐字一致：段写成功后才把 state 标记为「已落段」并推进
// EventsStoredThrough（B1a 的按条引用判据），并清掉内联副本（避免常驻切片把 RSS 推高）。
func (m *Manager) appendEventTail(key string, tail deliveryTailPlan) error {
	if m.events == nil || len(tail.Events) == 0 {
		return nil
	}
	if err := m.events.Append(key, tail.Events); err != nil {
		return err
	}
	m.mu.Lock()
	if m.eventsStoredRows == nil {
		m.eventsStoredRows = map[string]int{}
	}
	m.eventsStoredRows[key] = tail.StoredRows + len(tail.Events)
	saved := m.state.Sources[key]
	saved.EventsStored = true
	var covered uint64
	for _, ev := range tail.Events {
		if ev.Record.End > covered {
			covered = ev.Record.End
		}
	}
	if covered > saved.EventsStoredThrough {
		saved.EventsStoredThrough = covered
	}
	saved.Events = nil
	m.state.Sources[key] = saved
	m.mu.Unlock()
	return nil
}

// migrateInlineEvents 把旧格式（state 内联 events）搬到段存储并重写 state。
// 任一步失败即保留旧格式继续运行（不丢数据、不半途改格式）。
func (m *Manager) migrateInlineEvents() error {
	if m.events == nil {
		return nil
	}
	type pending struct {
		key    string
		events []logtypes.Event
	}
	var todo []pending
	m.mu.Lock()
	for key, saved := range m.state.Sources {
		if len(saved.Events) > 0 {
			todo = append(todo, pending{key: key, events: saved.Events})
		}
	}
	m.mu.Unlock()
	if len(todo) == 0 {
		return nil
	}
	// 先全部落段；任一端失败则整体放弃迁移（旧格式数据仍完整）。
	for _, p := range todo {
		if err := m.appendEvents(p.key, p.events); err != nil {
			return nil // 保留旧格式继续用，不阻断启动
		}
	}
	m.mu.Lock()
	for _, p := range todo {
		saved := m.state.Sources[p.key]
		if saved.EventsStored {
			continue
		}
		saved.Events = nil
		saved.EventsStored = true
		m.state.Sources[p.key] = saved
	}
	m.mu.Unlock()
	return m.persist()
}

const maxPublishedProjectionGenerations = 64

func (m *Manager) writeProjectionDay(source SourceConfig, events, writeEvents []logtypes.Event, generation string, archiveEvents []logtypes.Event, replace bool, tailPlanned bool) (pipeline.DeliveryResult, error) {
	catKey := catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay}
	expectedAuthority, _ := m.cat.Get(catKey)
	// tailPlanned 表示本次是「增量尾部」快路径：代次预算的判定已在 deliver 里做过
	// （同一判据、同源串行，见 projectionGenerationsExhausted），此处不得再改写 replace——
	// 快路径下 events 只是本批，没有可供整窗重发的权威全量。
	if current, ok := m.cat.Get(catKey); ok && !replace && !tailPlanned &&
		len(publishedSourceProjectionGenerations(current.PublishedProjection, source)) >= maxPublishedProjectionGenerations {
		replace = true
		writeEvents = events
	}
	client, frozen, err := m.clientForSource(source)
	if err != nil {
		return pipeline.DeliveryResult{}, err
	}
	// 诊断日志（2026-09-28 恢复期「批次未落地」排查）：VL 侧核对时，需要能对上
	// 「哪一批、多少条、什么时间范围、每片写入返回什么」。生产实测曾出现
	// 「校验等待 09-27/28 窗口的数据，而 VL 里只有该源 09-20 的数据」——需要本日志
	// 才能判定是「没写」还是「写了没落地」。
	batchFirst, batchLast := "", ""
	if len(writeEvents) > 0 {
		batchFirst = writeEvents[0].EventTimeUTC
		batchLast = writeEvents[len(writeEvents)-1].EventTimeUTC
	}
	slog.Info("投影写入开始",
		"generation", generation, "source", source.LogSourceID,
		"events", len(writeEvents), "archiveEvents", len(archiveEvents),
		"firstEventTime", batchFirst, "lastEventTime", batchLast,
	)
	status, err := m.insertInBatches(client, writeEvents, generation)
	slog.Info("投影写入结束",
		"generation", generation, "source", source.LogSourceID,
		"events", len(writeEvents), "status", status, "err", err)
	if err != nil {
		return pipeline.DeliveryResult{HTTPStatus: status}, err
	}
	if m.archive != nil && len(archiveEvents) > 0 {
		archivePayload, payloadErr := projectionPayload(archiveEvents, generation)
		if payloadErr != nil {
			return pipeline.DeliveryResult{HTTPStatus: status}, payloadErr
		}
		archiveKey := archive.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay, Generation: 1}
		if _, archiveErr := m.archive.RegisterRaw(context.Background(), archiveKey, archive.RawSource{
			Data: archivePayload, Origin: archive.OriginWorkerGenerated,
			LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration,
			ParserVersion: "worker-log-parser/v1", EventCount: uint64(len(archiveEvents)),
			EventTimeFromUTC: archiveEvents[0].EventTimeUTC,
			EventTimeToUTC:   archiveEvents[len(archiveEvents)-1].EventTimeUTC,
		}); archiveErr != nil {
			return pipeline.DeliveryResult{HTTPStatus: status}, archiveErr
		}
	}
	if err := m.verifyProjection(client, source, generation, writeEvents); err != nil {
		return pipeline.DeliveryResult{HTTPStatus: status}, err
	}
	if frozen {
		return pipeline.DeliveryResult{HTTPStatus: status}, fmt.Errorf("ingest: Catalog write route is frozen; verified staging generation %s remains unpublished", generation)
	}
	if err := m.publish(source, generation, events, replace, expectedAuthority); err != nil {
		return pipeline.DeliveryResult{HTTPStatus: status}, err
	}
	return pipeline.DeliveryResult{HTTPStatus: status}, nil
}

// insertBatchMaxEvents 是单次 VL 插入的批量上限。
//
// 背景（2026-09-28 生产事故）：vlsup 客户端是 http.Client{Timeout: 5 * time.Second}，
// 而恢复期会把积压一次性拼成一个大 payload 投递；当状态文件达 1.2 GB 时，
// 单请求远超 5s → `vlsup: insert request: ... context deadline exceeded`
// → writeProjectionDay 失败 → 「日志采集运行时创建失败」→ 采集静默停摆（无重试、无告警）。
// 分批后每批体量可控，单请求远低于该超时。
const insertBatchMaxEvents = 500

// insertInBatches 按条数上限把事件分批投递到 VL，返回最后一批的 HTTP 状态。
// 任一批失败即中断，并原样返回该批状态与错误（调用方据此判定投递结果）。
func (m *Manager) insertInBatches(client *vlsup.Client, events []logtypes.Event, generation string) (int, error) {
	status := 0
	for start := 0; start < len(events); start += insertBatchMaxEvents {
		end := start + insertBatchMaxEvents
		if end > len(events) {
			end = len(events)
		}
		payload, err := projectionPayload(events[start:end], generation)
		if err != nil {
			return status, err
		}
		st, err := client.InsertJSONLines(context.Background(), payload)
		status = st
		if err != nil {
			return status, err
		}
	}
	return status, nil
}

func projectionPayload(events []logtypes.Event, generation string) ([]byte, error) {
	var b strings.Builder
	for _, event := range events {
		line := map[string]any{
			"_time": event.EventTimeUTC, "_msg": event.Message,
			"event_id": event.EventID, "log_source_id": event.Source.LogSourceID,
			"source_generation": event.Source.SourceGeneration,
			"parser_version":    event.Source.ParserVersion,
			"record_start":      event.Record.Start, "record_end": event.Record.End,
			"ingest_time_utc": event.IngestTimeUTC, "level": event.Level, "stream": event.Stream,
			"canonical_content_hash": event.CanonicalHash, "projection_generation": generation,
		}
		for k, v := range event.Fields {
			line[k] = v
		}
		data, err := json.Marshal(line)
		if err != nil {
			return nil, err
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}

// eventUTCDay 解析事件所属 UTC 日，与 normalizeCanonicalEvent 的时间策略一致：
// 优先事件语义时间，缺失/不可解析时回退 ingest 时间，再回退源配置的 UTCDay。
//
// 为什么必须回退而不是报错：真实日志行可能没有可解析的语义时间（解析失败或非标准格式）。
// 若此处直接报错，投递/投影路径会失败 → 受管恢复责任无法转移 → reclaim 永不推进 → WAL 保留
// 全部事件（真机 64 源实测：Worker RSS 涨到 2GiB、state 文件 285MB）。事件时间回退是既有契约行为。
func eventUTCDay(source SourceConfig, event logtypes.Event) (string, error) {
	for _, raw := range []string{event.EventTimeUTC, event.IngestTimeUTC} {
		if raw == "" {
			continue
		}
		if when, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return when.UTC().Format("2006-01-02"), nil
		}
	}
	if source.UTCDay != "" {
		return source.UTCDay, nil
	}
	return "", fmt.Errorf("ingest: event %s has no usable event/ingest time or partition day", event.EventID)
}

// canonicalEventTime 解析事件的规范时间：优先语义时间 event_time，缺失/不可解析时回退 ingest 时间。
// 与 normalizeCanonicalEvent 的时间策略一致（同一契约行为），避免「无解析时间的真实日志行」令
// 投递/校验路径失败而导致 reclaim 停滞。
func canonicalEventTime(event logtypes.Event) (time.Time, error) {
	for _, raw := range []string{event.EventTimeUTC, event.IngestTimeUTC} {
		if raw == "" {
			continue
		}
		if when, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return when.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("no usable event or ingest time")
}

func groupEventsByUTCDay(source SourceConfig, events []logtypes.Event) (map[string][]logtypes.Event, []string, error) {
	grouped := make(map[string][]logtypes.Event)
	for _, event := range events {
		day, err := eventUTCDay(source, event)
		if err != nil {
			return nil, nil, err
		}
		grouped[day] = append(grouped[day], event)
	}
	days := make([]string, 0, len(grouped))
	for day := range grouped {
		days = append(days, day)
	}
	sort.Strings(days)
	return grouped, days, nil
}

// publishedClosedForSource 返回该源已发布投影的保守封闭前缀（所有受影响分区的最小封闭水位）。
// 受影响分区由事件集合的 UTC 日推导：归档导入/轮转可能跨日，事件自身才是权威来源。
//
// 事件体在磁盘段时按日聚合最大末端位置（流式，不载入全量事件）；旧格式仍用内联切片。
func (m *Manager) publishedClosedForSource(source SourceConfig, saved persistedSource) (uint64, bool) {
	// ctx 形态是**唯一实现**（见 publishedClosedForSourceCtx）：此处传 Background，保持既有签名
	// 与语义（既有调用点逐字不变）。投影读取失败在无 ctx 语义下仍表现为「不完整」。
	closed, complete, _ := m.publishedClosedForSourceCtx(context.Background(), source, saved)
	return closed, complete
}

// publishedClosedForSourceCtx 是可取消形态：ctx 在源内聚合的逐行回调处生效（Iterate 逐行回调，
// 故取消能即时中止段扫描，不必等整源读完），并在内联事件路径的每条事件前校验。
//
// 为什么必须可取消（2026-10-02 压测现场）：单源投影查询要扫该源的全部磁盘事件段（生产单源段
// 达数十万行、全库 18GB），一次整节点解算 = 75 次这样的扫描。此前它压在 cycleMu 上且不可取消；
// 现在它离锁执行，但**仍必须能被打断**——否则 HTTP 300s 超时之后，服务端还会继续为一份没人
// 再等的响应扫下去。
func (m *Manager) publishedClosedForSourceCtx(ctx context.Context, source SourceConfig, saved persistedSource) (uint64, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	dayMaxEnd := map[string]uint64{}
	if saved.EventsStored && m.events != nil {
		key := source.LogSourceID + "/" + source.SourceGeneration
		agg, err := m.events.DayMaxEndCtx(ctx, key, func(ev logtypes.Event) (string, error) {
			return eventUTCDay(source, ev)
		})
		if err != nil {
			return 0, false, err
		}
		dayMaxEnd = agg
	} else {
		for _, event := range saved.Events {
			if err := ctx.Err(); err != nil {
				return 0, false, err
			}
			day, err := eventUTCDay(source, event)
			if err != nil {
				return 0, false, err
			}
			if event.Record.End > dayMaxEnd[day] {
				dayMaxEnd[day] = event.Record.End
			}
		}
	}
	closed, complete := m.publishedClosedFromDayMax(source, dayMaxEnd)
	return closed, complete, nil
}

// publishedClosedFromDayMax 是「按日最大末端 + 已发布投影」推导保守封闭前缀的纯计算部分
// （与改动前的实现逐字相同，只是从 publishedClosedForSource 中抽出以便 ctx 形态复用）。
func (m *Manager) publishedClosedFromDayMax(source SourceConfig, dayMaxEnd map[string]uint64) (uint64, bool) {
	days := make([]string, 0, len(dayMaxEnd))
	for day := range dayMaxEnd {
		days = append(days, day)
	}
	sort.Strings(days)
	if len(days) == 0 && source.UTCDay != "" {
		days = []string{source.UTCDay}
	}
	if len(days) == 0 {
		return 0, false
	}

	var closed uint64
	for _, day := range days {
		key := catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: day}
		rec, ok := m.cat.Get(key)
		if !ok || rec.PublishedProjection == nil || !rec.PublishedProjection.CoverageComplete {
			return 0, false
		}
		projection := rec.PublishedProjection
		dayClosed := uint64(0)
		if len(projection.SourceProjections) > 0 {
			found := false
			for _, scope := range projection.SourceProjections {
				if scope.LogSourceID == source.LogSourceID && scope.SourceGeneration == source.SourceGeneration {
					dayClosed, found = scope.ClosedVisibleSeq, true
					break
				}
			}
			if !found {
				return 0, false
			}
		} else {
			dayClosed = projection.ClosedVisibleSeq["default"]
			if sourceClosed := projection.ClosedVisibleSeq[key.String()]; sourceClosed > dayClosed {
				dayClosed = sourceClosed
			}
		}
		if dayMaxEnd[day] > dayClosed {
			return 0, false
		}
		if dayClosed > closed {
			closed = dayClosed
		}
	}
	return closed, true
}

// verifyProjection checks the actual VL query input before publishing a
// generation. JSON-stream request completion does not prove individual rows
// were accepted, and an uncertain target must never become query authority.
func (m *Manager) clientForSource(source SourceConfig) (*vlsup.Client, bool, error) {
	if m.vlRoute != nil {
		client, frozen, err := m.vlRoute(source)
		if err != nil {
			return nil, frozen, err
		}
		if client == nil {
			return nil, frozen, fmt.Errorf("ingest: Catalog-selected VictoriaLogs client is unavailable")
		}
		return client, frozen, nil
	}
	if m.vl == nil {
		return nil, false, fmt.Errorf("ingest: VictoriaLogs client is not ready")
	}
	return m.vl, false, nil
}

// 投影校验的退避默认值（B1c）。
//
// 上限由 2s 收紧到 500ms 的依据（2026-10-01 实测，探针见 docs/specs/log-verify-chunking/spec.md §6）：
// VL 的「写入 → 可查询」延迟**恒定**在 1011–1029 ms（20/20 次，见可见延迟探针），
// 而退避序列 200/400/800/封顶 2s 的探测点是 0 / 0.2 / 0.6 / **1.4** s —— 恰好在 1.0 s 附近
// 没有探测点，每次批级校验都白等约 0.4 s（真进程夹具批级 verify p50 = 1411 ms）。
// 封顶 500ms 后探测点变为 0 / 0.2 / 0.6 / 1.1 s，数据一可见就命中并返回。
//
// 为什么不是「固定 200ms 轮询」：2026-09-28 生产事故正是固定间隔轮询到 30 秒超时
// （单源最多约 150 次查询，把 VL 与磁盘一起压垮）。本改动保持**指数退避**语义，
// 只把封顶从 2s 收到 500ms——等待更久时（数据长期不可见）2 秒窗口内的查询次数是 5 次，
// 仍远低于事故时的量级；查询成本本身也已被分簇压到「每批 1 次」（见 verifyChunkEvents）。
const (
	defaultVerifyBackoffMin = 200 * time.Millisecond
	defaultVerifyBackoffMax = 500 * time.Millisecond
)

// nextVerifyBackoff 返回下一次退避时长：指数增长并封顶（B1c）。
func nextVerifyBackoff(cur, max time.Duration) time.Duration {
	if cur <= 0 {
		return defaultVerifyBackoffMin
	}
	if max <= 0 {
		max = defaultVerifyBackoffMax
	}
	next := cur * 2
	if next > max {
		next = max
	}
	return next
}

// 投影校验的分簇与并发参数（FR-498 P0 优化，2026-10-01）。
const (
	// defaultVerifyChunkEvents 是单次校验查询覆盖的事件上限。
	//
	// 与写入批上限（insertBatchMaxEvents=500）**解耦**：写入批上限保护的是单次插入请求的体积，
	// 而校验查询的成本由「查询窗口内返回的行数」决定，与「本次要求可见的事件数」几乎无关——
	// 分片越细，越多的查询会重复读回同一批行。实测（本地饱和夹具 60 源 ×30 行/s，详见
	// docs/specs/log-verify-chunking/spec.md）：旧实现按 500 条索引切片、各片时间窗相互重叠，
	// 读回量被放大到 k×n（k=片数）；单次校验查询 141ms 里 VL 首字节仅 3.5ms，
	// 其余全是返回体的传输与逐行 JSON 解析。2000 与「单源单次 poll 的典型满批」一致：
	// 常见情形下一批只需 1 次查询。
	defaultVerifyChunkEvents = 2000
	// defaultVerifyChunkConcurrency 是同批内各校验簇的查询并发度；0/负值取该默认，1 为串行。
	// 校验查询的成本几乎全在「返回体传输 + 客户端逐行 JSON 解析」，属可并行部分；
	// 跨源并发（pollConcurrency）只重叠了源与源之间的等待，源内部的等待仍逐个叠加。
	defaultVerifyChunkConcurrency = 4
	// verifySlowQueryThreshold 是单次校验查询的慢查询告警阈值。旧实现只打「查询开始」，
	// 慢查询在现场不可见（FR-498 定位耗时构成的主要困难之一）。
	verifySlowQueryThreshold = 500 * time.Millisecond
)

// verifyQueryStats 累计一次投影校验的读回量（观测面与回归读数）。
type verifyQueryStats struct {
	Queries int
	Rows    int64
	Bytes   int64
}

// verifyEventTimeKey 返回事件用于分簇排序的时间键；不可解析的时间按零值处理
// （排在最前，彼此相等，保证排序的严格弱序）。
func verifyEventTimeKey(event logtypes.Event) time.Time {
	if when, err := canonicalEventTime(event); err == nil {
		return when
	}
	return time.Time{}
}

// planVerifyChunks 把整批事件按**事件时间**排序后，每 max 条切成一簇。
//
// 为什么按时间切（FR-498 P0）：旧实现按**索引**每 500 条切一片，每片用「本片事件的
// [min-1s, max+1s]」查询。批内事件的时间与索引顺序并不一致（多条流混合、时间戳跳变、
// 跨日批次），各片时间窗因而相互重叠，**同一批的每一行都会被重复读回 k 次**（k=片数）。
// 按时间切簇后，各簇窗口只覆盖自己的时间范围（相邻簇最多在 ±1s 容差处相接），
// 读回总量从 k×n 降到 ≈n：簇内跨度是 min/max 决定的，排序本身就让相邻簇的窗口不再互相包含。
//
// 语义不变：每个事件仍必须在其所属簇的窗口内可见且内容逐字段一致；allowed（整批）的接受规则、
// 重复/多余检测、退避与超时语义全部保持原样（见 verifyProjectionQuery）。
func planVerifyChunks(events []logtypes.Event, max int) [][]logtypes.Event {
	if len(events) == 0 {
		return nil
	}
	if max <= 0 {
		max = defaultVerifyChunkEvents
	}
	ordered := make([]logtypes.Event, len(events))
	copy(ordered, events)
	// 稳定排序：同一时间戳的事件保持原索引顺序，不改变可观察的判定顺序。
	sort.SliceStable(ordered, func(i, j int) bool {
		return verifyEventTimeKey(ordered[i]).Before(verifyEventTimeKey(ordered[j]))
	})
	chunks := make([][]logtypes.Event, 0, (len(ordered)+max-1)/max)
	for start := 0; start < len(ordered); start += max {
		end := start + max
		if end > len(ordered) {
			end = len(ordered)
		}
		chunks = append(chunks, ordered[start:end])
	}
	return chunks
}

// verifyProjection 校验整批事件的投影可见性。
//
// 分批是必须的：VL 对单次查询有内存上限，而恢复期的事件数是整批积压（实测 747,822 条会让 VL
// 返回 400 `cannot calculate [sort by (_time) desc limit ...]`）。但**分批必须让期望集覆盖整批**：
// 各分片的时间范围会重叠（真实事件常共享时间戳），第 N 片的查询会返回其他分片的事件；
// 若只以本片为期望集，这些记录会被误判为 unexpected（2026-09-28 生产实测）。
//
// 分簇按**事件时间**而非索引（见 planVerifyChunks），簇内并发执行（见 verifyChunkConcurrency）。
// 错误语义与串行实现一致：任一簇失败即整体失败，返回**最先观察到**的那个错误，并取消其余簇。
func (m *Manager) verifyProjection(client *vlsup.Client, source SourceConfig, generation string, events []logtypes.Event) error {
	if len(events) == 0 {
		return nil
	}
	allowed := make(map[string]logtypes.Event, len(events))
	for _, event := range events {
		allowed[event.EventID] = event
	}
	chunks := planVerifyChunks(events, m.verifyChunkEvents)
	concurrency := m.verifyChunkConcurrency
	if concurrency <= 0 {
		concurrency = defaultVerifyChunkConcurrency
	}
	if concurrency > len(chunks) {
		concurrency = len(chunks)
	}
	started := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		mu          sync.Mutex
		stats       verifyQueryStats
		semanticErr error
		otherErr    error
	)
	next := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range next {
				chunkStats, err := m.verifyProjectionChunk(ctx, client, source, generation, chunks[idx], allowed)
				mu.Lock()
				stats.Queries += chunkStats.Queries
				stats.Rows += chunkStats.Rows
				stats.Bytes += chunkStats.Bytes
				if err != nil {
					// 判定类错误优先于「被取消的查询」产生的传输类噪音（见 verifyErrorIsSemantic）：
					// 否则首个失败取消其余簇后，现场看到的会是被取消查询的 context canceled /
					// 半行 JSON 解析错误，而真正的根因被掩盖。
					if verifyErrorIsSemantic(err) {
						if semanticErr == nil {
							semanticErr = err
						}
					} else if otherErr == nil {
						otherErr = err
					}
				}
				mu.Unlock()
				if err != nil {
					// 只有携带「明确根因」的失败才取消其余簇（永久失败 / 内容不一致、多余或重复、
					// 事件集非法这类**确定性**判定）：此时收敛是安全的，根因已经拿到。
					//
					// 超时类失败**不取消**：它往往只说明"本批数据尚不可见"，而其余簇此刻可能正读到
					// 真正的根因（例如某一行内容不一致）；取消会把那个结论一并抹掉，让现场只剩
					// 取消噪音（2026-10-01 在 `-race` 下实测到该形态：注入的"写错一行"被
					// `verification failed: context deadline exceeded` 覆盖）。
					//
					// 该策略此前**没有真正生效**：本簇到期唯一的错误文案
					// `not fully visible before deadline` 曾被 verifyErrorIsSemantic 判为判定类，
					// 于是任一簇到期都会走 cancel()（2026-10-02 复审 P1-3 修正，回归见
					// verify_cancel_classification_test.go）。
					if vlsup.IsPermanent(err) || verifyErrorIsSemantic(err) {
						cancel()
					}
				}
			}
		}()
	}
	for i := range chunks {
		if ctx.Err() != nil {
			break // 已有簇失败：不再派发后续簇（与串行实现「失败即返回」一致）
		}
		select {
		case next <- i:
		case <-ctx.Done():
		}
	}
	close(next)
	wg.Wait()

	elapsed := time.Since(started)
	mu.Lock()
	finalStats, verdict := stats, semanticErr
	if verdict == nil {
		verdict = otherErr
	}
	mu.Unlock()
	slog.Info("投影校验完成",
		"generation", generation, "source", source.LogSourceID,
		"events", len(events), "chunks", len(chunks), "concurrency", concurrency,
		"queries", finalStats.Queries, "rows", finalStats.Rows, "bytes", finalStats.Bytes,
		"elapsedMs", elapsed.Milliseconds(), "err", errText(verdict))
	return verdict
}

// verifyErrorIsTimeoutLike 判定错误是否只是「本簇自己的校验窗口到期」的产物
// （客户端取消/超时引发的传输类错误），而不是 VL 给出的语义结论。
func verifyErrorIsTimeoutLike(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "context deadline exceeded") || strings.Contains(msg, "context canceled")
}

// verifyErrorIsSemantic 判定错误是否属于「校验自身的**确定性**判定结论」——内容不一致 /
// 多余或重复 / 永久失败 / 事件集非法——而不是「同批其他簇失败后取消」引发的传输类噪音，
// 也不是「本簇窗口到期仍不可见」这类**尚未定型**的结论。
//
// 为什么需要区分（2026-10-01 实测）：同批多簇并发执行时，首个失败会取消其余查询；被取消的
// 请求可能报出 `context canceled`，或把被截断的响应体报成 `unexpected end of JSON input`。
// 若让「最先观察到者胜出」，现场看到的就可能是被取消查询的噪音，真正根因（例如内容不一致）
// 被掩盖。故判定类错误优先返回（串行实现下本来也只会返回判定类错误，语义一致）。
//
// 为什么**不含**「not fully visible before deadline」（2026-10-02 复审 P1-3）：
// 那是本簇自己的窗口到期产生的**非确定性**结论（数据可能只是可见性延迟，退避重试本就是为它
// 准备的），而它恰恰是 chunk 超时时**唯一**的错误文案。一旦把它算作判定类，任一簇到期就会
// `cancel()` 其余簇——另一簇即将读到的真根因（例如内容不一致）被
// `aborted by batch cancellation` 覆盖，现场只剩「不可见 + 取消」的噪音。
// 因此取消只允许由 vlsup.IsPermanent 与真正的判定类错误触发（见 verifyProjection 的取消策略）。
func verifyErrorIsSemantic(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, marker := range []string{
		"projection content mismatch",
		"unexpected or duplicate projection event_id",
		"verification failed permanently",
		"duplicate canonical event_id",
		"invalid canonical event time",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// verifyProjectionChunk 校验单个簇的事件在 VL 中完全可见（退避重试 + 永久错误立即失败）。
// allowed 是「可接受集合」（整批积压的全部事件）：本簇之外的记录只要属于该集合且内容一致即接受。
// parent 是整批的 ctx：同批其他簇失败/超时会取消它——此时本簇不得把取消噪音（context canceled、
// 被截断响应体的 JSON 解析错误）当成自己的校验结论，否则现场看到的会是噪音而非真正根因。
func (m *Manager) verifyProjectionChunk(parent context.Context, client *vlsup.Client, source SourceConfig, generation string, events []logtypes.Event, allowed map[string]logtypes.Event) (verifyQueryStats, error) {
	ctx, cancel := context.WithTimeout(parent, m.verificationTimeout)
	defer cancel()
	// 退避重试（B1c）：原实现以固定 200ms 轮询直至超时（默认 30 秒 ≈ 最多约 150 次校验
	// 查询/源）。高负载时每次查询更慢、重试互相叠加，会把 VL 与磁盘一起压垮——2026-09-28
	// 生产事故的成因之一正是校验查询被如此重试（含 `cannot execute query [...]` 这类 4xx）。
	// 现改为指数退避封顶，并让「重试无意义」的错误（认证失败 / 4xx 语义错误）立即失败。
	backoff := m.verifyBackoffMin
	var lastErr error
	var stats verifyQueryStats
	for {
		queryStarted := time.Now()
		complete, queryStats, err := m.verifyProjectionQuery(ctx, client, source, generation, events, allowed)
		elapsed := time.Since(queryStarted)
		stats.Queries++
		stats.Rows += queryStats.Rows
		stats.Bytes += queryStats.Bytes
		if elapsed >= verifySlowQueryThreshold {
			// 慢查询单独留痕：读回量（rows/bytes）是判定「窗口内行数过多」还是「VL 慢」的关键。
			slog.Warn("投影校验慢查询",
				"generation", generation, "source", source.LogSourceID,
				"events", len(events), "rows", queryStats.Rows, "bytes", queryStats.Bytes,
				"elapsedMs", elapsed.Milliseconds())
		}
		// 父 ctx 已取消（同批其他簇已拿到明确根因）：本簇的"尚未可见"结论已无意义，立即收手。
		// 注意顺序——先处理本次查询自身的结论，再看取消：判定类错误是本簇的真实结论，
		// 不能被"别的簇先失败"抹掉（见 verifyProjection 的取消策略）。
		if complete && err == nil {
			return stats, nil
		}
		if err != nil {
			if vlsup.IsPermanent(err) {
				return stats, fmt.Errorf("ingest: projection %s verification failed permanently: %w", generation, err)
			}
			// 判定类错误（内容不一致 / 多余或重复 / 事件集非法）是**确定性结论**：VL 里的行内容
			// 已经定型，退避重试不会改变它。旧实现会把它当普通失败继续重试到窗口结束
			// （默认 5 分钟）才报出同一结论——纯浪费，且让现场多等一个窗口。
			// 错误前缀与「重试到窗口结束」路径保持逐字一致，调用方的断言与判读不受影响。
			if verifyErrorIsSemantic(err) {
				return stats, fmt.Errorf("ingest: projection %s verification failed: %w", generation, err)
			}
			if parent.Err() == nil {
				lastErr = err
			} else {
				return stats, fmt.Errorf("ingest: projection %s verification aborted by batch cancellation: %w", generation, parent.Err())
			}
		}
		if parentErr := parent.Err(); parentErr != nil {
			return stats, fmt.Errorf("ingest: projection %s verification aborted by batch cancellation: %w", generation, parentErr)
		}
		select {
		case <-ctx.Done():
			// 本簇窗口到期：根因就是「到期仍不可见」。lastErr 若只是本簇自己的超时引发的传输类
			// 错误（context deadline exceeded / canceled），不改变这一结论——否则现场会把
			// 「数据没可见」误读成「查询失败」（2026-10-01 在 `-race` 下实测到该形态：
			// 注入「少写一行」后错误被改写成 verification failed: context deadline exceeded）。
			if lastErr != nil && !verifyErrorIsTimeoutLike(lastErr) {
				return stats, fmt.Errorf("ingest: projection %s verification failed: %w", generation, lastErr)
			}
			return stats, fmt.Errorf("ingest: projection %s not fully visible before deadline: %w", generation, ctx.Err())
		case <-time.After(backoff):
		}
		backoff = nextVerifyBackoff(backoff, m.verifyBackoffMax)
	}
}

// errText 把可空错误渲染为日志字段（nil → 空串，避免日志里出现 "<nil>"）。
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (m *Manager) verifyProjectionOnce(ctx context.Context, source SourceConfig, generation string, events []logtypes.Event) (bool, error) {
	complete, _, err := m.verifyProjectionQuery(ctx, m.vl, source, generation, events, nil)
	return complete, err
}

// utcDayBounds 返回 "2006-01-02" 形式的 UTC 日的闭区间 [起, 止]。
// 格式不符时返回 ok=false，调用方保持原行为（向后兼容）。
func utcDayBounds(utcDay string) (time.Time, time.Time, bool) {
	day, err := time.Parse("2006-01-02", utcDay)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	return day, day.Add(24*time.Hour - time.Nanosecond), true
}

// sameSourceConfig 判定两次登记是否属于同一份源配置。
//
// UTCDay 是**登记日的派生默认值**（空值填「今天」，见 RegisterSource 归一化），
// 不构成源身份：跨天后同一源的再登记会因它不同而被误判为
// 「another configuration」→ 实例日志采集登记被拒 → 实例起不来
// （2026-10-01 生产实证：午夜后全部重登记被拒，11 台实例无法启动）。
// 故比较前把它归零，其余字段仍严格逐字段比较。
func sameSourceConfig(a, b SourceConfig) bool {
	a.UTCDay, b.UTCDay = "", ""
	return a == b
}

// verifyProjectionQuery 执行一次校验查询，并返回本次的读回量（行数/字节数）。
//
// 与 verifyProjectionOnceWithClient 的唯一差别是 allowed：分批校验时，各簇的时间范围可能相接，
// 查询会返回**同批其他簇**的记录。这些记录必须被接受（否则误判 unexpected），
// 但仍要求内容逐字段一致、且不得重复出现；完整性只要求本簇 events 全部可见。
// allowed 为 nil 时退化为「本簇即全部」的原语义。
//
// 判定规则与旧实现逐字一致（相同的 selector、时间窗、日夹取、逐字段比对与三类错误文案）；
// 新增的 verifyQueryStats 只用于观测与回归读数，不参与任何判定。
func (m *Manager) verifyProjectionQuery(ctx context.Context, client *vlsup.Client, source SourceConfig, generation string, events []logtypes.Event, allowed map[string]logtypes.Event) (bool, verifyQueryStats, error) {
	var stats verifyQueryStats
	if client == nil {
		return false, stats, fmt.Errorf("ingest: VictoriaLogs verification client is unavailable")
	}
	want := make(map[string]logtypes.Event, len(events))
	var first, last time.Time
	for _, event := range events {
		if _, exists := want[event.EventID]; exists {
			return false, stats, fmt.Errorf("duplicate canonical event_id %s", event.EventID)
		}
		when, err := canonicalEventTime(event)
		if err != nil {
			return false, stats, fmt.Errorf("invalid canonical event time for %s: %w", event.EventID, err)
		}
		if first.IsZero() || when.Before(first) {
			first = when
		}
		if last.IsZero() || when.After(last) {
			last = when
		}
		want[event.EventID] = event
	}
	// 选择器以**事件自身的** source 标识为准，而不是源配置：2026-09-28 生产实测——
	// 配置里的 SourceGeneration 与事件里的 Source.SourceGeneration 指向了不同实例
	// （login-01 的事件被按 beacon-main 的代号查询）→ 查询恒 0 条 → 判「不可见」→
	// 运行时创建失败。校验对象就是「刚写下去的这些事件」，故以它们为准；
	// 配置仅作为事件未携带标识时的兜底。
	sourceID, sourceGeneration := source.LogSourceID, source.SourceGeneration
	if len(events) > 0 {
		if v := events[0].Source.LogSourceID; v != "" {
			sourceID = v
		}
		if v := events[0].Source.SourceGeneration; v != "" {
			sourceGeneration = v
		}
	}
	selector := "projection_generation:=" + strconv.Quote(generation) +
		" AND log_source_id:=" + strconv.Quote(sourceID) +
		" AND source_generation:=" + strconv.Quote(sourceGeneration)
	// 时间窗必须**夹在本源的 UTC 日内**：±1s 的宽容窗会越界到相邻日，
	// 把「上一天批次的日界事件」（如 09-28 窗口下探 1s 落进 09-27T23:59:59.126）
	// 带进结果集；该事件不属于本批 allowed → 误判 `unexpected or duplicate
	// projection event_id` → 运行时创建失败 → 节点离线（2026-09-30 生产事故）。
	// 日界事件在自己那一天的窗口内仍被正常覆盖，完整性不受影响。
	start := first.Add(-time.Second)
	end := last.Add(time.Second)
	if dayStart, dayEnd, ok := utcDayBounds(source.UTCDay); ok {
		if start.Before(dayStart) {
			start = dayStart
		}
		if end.After(dayEnd) {
			end = dayEnd
		}
	}
	params := url.Values{
		"query": {selector + " | fields _time, _msg, event_id, level, stream, canonical_content_hash"},
		// 不设 limit：VL 按时间返回窗口内的记录，而分片是按**索引**切的，其时间跨度可能很宽
		// （2026-09-28 生产：500 条事件跨约 10 小时），窗口内除本片外还会有其他分片的记录。
		// 原先 `limit = len(events)+1` 会截断返回，导致本片记录凑不齐 → 误判「不可见」→
		// 运行时创建失败 → 采集静默停摆（多轮盲改后才由参数日志定位）。
		"start": {start.UTC().Format(time.RFC3339Nano)},
		"end":   {end.UTC().Format(time.RFC3339Nano)},
	}
	// 诊断（2026-09-28 生产）：校验恒「不可见」而数据确实在 VL 里时，必须能拿到
	// 平台**实际发出的**查询参数，与手工复刻查询逐字段对照。此前多轮修复都因缺少
	// 这一条日志而在盲改。
	slog.Info("投影校验查询",
		"generation", generation, "events", len(events),
		"query", params.Get("query"), "start", params.Get("start"),
		"end", params.Get("end"), "limit", params.Get("limit"))
	seen := make(map[string]bool, len(events))
	seenOther := make(map[string]bool)
	err := client.Stream(ctx, "/select/logsql/query", params, func(body io.Reader) error {
		scanner := bufio.NewScanner(io.LimitReader(body, 32<<20+1))
		scanner.Buffer(make([]byte, 64*1024), 4<<20)
		for scanner.Scan() {
			// 取消（同批其他簇已失败）时立即收手：否则会把被截断的响应体报成
			// `unexpected end of JSON input`，掩盖真正的根因。
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			stats.Rows++
			stats.Bytes += int64(len(scanner.Bytes())) + 1 // +1：NDJSON 行尾换行
			var row struct {
				EventID       string `json:"event_id"`
				CanonicalHash string `json:"canonical_content_hash"`
				EventTimeUTC  string `json:"_time"`
				Message       string `json:"_msg"`
				Level         string `json:"level"`
				Stream        string `json:"stream"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
				return err
			}
			event, inBatch := want[row.EventID]
			if !inBatch {
				other, ok := allowed[row.EventID]
				if !ok || seenOther[row.EventID] {
					return fmt.Errorf("unexpected or duplicate projection event_id %s", row.EventID)
				}
				if row.CanonicalHash != other.CanonicalHash || row.EventTimeUTC != other.EventTimeUTC ||
					row.Message != other.Message || row.Level != other.Level || row.Stream != other.Stream {
					return fmt.Errorf("projection content mismatch for event_id %s", row.EventID)
				}
				seenOther[row.EventID] = true
				continue
			}
			if seen[row.EventID] {
				return fmt.Errorf("unexpected or duplicate projection event_id %s", row.EventID)
			}
			if row.CanonicalHash != event.CanonicalHash || row.EventTimeUTC != event.EventTimeUTC ||
				row.Message != event.Message || row.Level != event.Level || row.Stream != event.Stream {
				return fmt.Errorf("projection content mismatch for event_id %s", row.EventID)
			}
			seen[row.EventID] = true
		}
		return scanner.Err()
	})
	if err != nil {
		return false, stats, err
	}
	return len(seen) == len(want), stats, nil
}

func (m *Manager) verifyProjectionOnceWithClient(ctx context.Context, client *vlsup.Client, source SourceConfig, generation string, events []logtypes.Event) (bool, error) {
	if client == nil {
		return false, fmt.Errorf("ingest: VictoriaLogs verification client is unavailable")
	}
	want := make(map[string]logtypes.Event, len(events))
	var first, last time.Time
	for _, event := range events {
		if _, exists := want[event.EventID]; exists {
			return false, fmt.Errorf("duplicate canonical event_id %s", event.EventID)
		}
		when, err := canonicalEventTime(event)
		if err != nil {
			return false, fmt.Errorf("invalid canonical event time for %s: %w", event.EventID, err)
		}
		if first.IsZero() || when.Before(first) {
			first = when
		}
		if last.IsZero() || when.After(last) {
			last = when
		}
		want[event.EventID] = event
	}
	// 选择器以**事件自身的** source 标识为准，而不是源配置：2026-09-28 生产实测——
	// 配置里的 SourceGeneration 与事件里的 Source.SourceGeneration 指向了不同实例
	// （login-01 的事件被按 beacon-main 的代号查询）→ 查询恒 0 条 → 判「不可见」→
	// 运行时创建失败。校验对象就是「刚写下去的这些事件」，故以它们为准；
	// 配置仅作为事件未携带标识时的兜底。
	sourceID, sourceGeneration := source.LogSourceID, source.SourceGeneration
	if len(events) > 0 {
		if v := events[0].Source.LogSourceID; v != "" {
			sourceID = v
		}
		if v := events[0].Source.SourceGeneration; v != "" {
			sourceGeneration = v
		}
	}
	selector := "projection_generation:=" + strconv.Quote(generation) +
		" AND log_source_id:=" + strconv.Quote(sourceID) +
		" AND source_generation:=" + strconv.Quote(sourceGeneration)
	// 时间窗必须**夹在本源的 UTC 日内**：±1s 的宽容窗会越界到相邻日，
	// 把「上一天批次的日界事件」（如 09-28 窗口下探 1s 落进 09-27T23:59:59.126）
	// 带进结果集；该事件不属于本批 allowed → 误判 `unexpected or duplicate
	// projection event_id` → 运行时创建失败 → 节点离线（2026-09-30 生产事故）。
	// 日界事件在自己那一天的窗口内仍被正常覆盖，完整性不受影响。
	start := first.Add(-time.Second)
	end := last.Add(time.Second)
	if dayStart, dayEnd, ok := utcDayBounds(source.UTCDay); ok {
		if start.Before(dayStart) {
			start = dayStart
		}
		if end.After(dayEnd) {
			end = dayEnd
		}
	}
	params := url.Values{
		"query": {selector + " | fields _time, _msg, event_id, level, stream, canonical_content_hash"},
		// 不设 limit：VL 按时间返回窗口内的记录，而分片是按**索引**切的，其时间跨度可能很宽
		// （2026-09-28 生产：500 条事件跨约 10 小时），窗口内除本片外还会有其他分片的记录。
		// 原先 `limit = len(events)+1` 会截断返回，导致本片记录凑不齐 → 误判「不可见」→
		// 运行时创建失败 → 采集静默停摆（多轮盲改后才由参数日志定位）。
		"start": {start.UTC().Format(time.RFC3339Nano)},
		"end":   {end.UTC().Format(time.RFC3339Nano)},
	}
	// 诊断（2026-09-28 生产）：校验恒「不可见」而数据确实在 VL 里时，必须能拿到
	// 平台**实际发出的**查询参数，与手工复刻查询逐字段对照。此前多轮修复都因缺少
	// 这一条日志而在盲改。
	slog.Info("投影校验查询",
		"generation", generation, "events", len(events),
		"query", params.Get("query"), "start", params.Get("start"),
		"end", params.Get("end"), "limit", params.Get("limit"))
	seen := make(map[string]bool, len(events))
	err := client.Stream(ctx, "/select/logsql/query", params, func(body io.Reader) error {
		scanner := bufio.NewScanner(io.LimitReader(body, 32<<20+1))
		scanner.Buffer(make([]byte, 64*1024), 4<<20)
		for scanner.Scan() {
			var row struct {
				EventID       string `json:"event_id"`
				CanonicalHash string `json:"canonical_content_hash"`
				EventTimeUTC  string `json:"_time"`
				Message       string `json:"_msg"`
				Level         string `json:"level"`
				Stream        string `json:"stream"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
				return err
			}
			event, ok := want[row.EventID]
			if !ok || seen[row.EventID] {
				return fmt.Errorf("unexpected or duplicate projection event_id %s", row.EventID)
			}
			if row.CanonicalHash != event.CanonicalHash || row.EventTimeUTC != event.EventTimeUTC ||
				row.Message != event.Message || row.Level != event.Level || row.Stream != event.Stream {
				return fmt.Errorf("projection content mismatch for event_id %s", row.EventID)
			}
			seen[row.EventID] = true
		}
		return scanner.Err()
	})
	if err != nil {
		return false, err
	}
	return len(seen) == len(want), nil
}

// releaseRecovery transfers WAL responsibility only after the delivery result
// and canonical projection have both been persisted. The projection itself is
// the durable receiver; HTTP 2xx alone never reaches this method.
func (m *Manager) releaseRecovery(source SourceConfig, events []logtypes.Event) error {
	if len(events) == 0 {
		return nil
	}
	key := source.LogSourceID + "/" + source.SourceGeneration
	m.mu.Lock()
	p := m.pipes[key]
	generation := m.state.Sources[key].ProjectionGeneration
	m.mu.Unlock()
	if p == nil {
		return fmt.Errorf("ingest: pipeline %s missing for reclaim proof", key)
	}
	to := events[len(events)-1].Record.End
	positions, ok := p.Positions()
	if !ok {
		return fmt.Errorf("ingest: missing ledger positions for %s", key)
	}
	from := positions.Reclaim
	currentSegID := fmt.Sprintf("projection-recovery-%s-%d", generation, to)
	segID := currentSegID
	ref, exists := p.RecoveryCovering(from, to)
	if exists {
		segID = ref.SegmentID
	}
	if !exists {
		if from >= to {
			return nil
		}
		if err := p.BindRecoverySegment(segID, "projection://"+generation, from, to); err != nil {
			return err
		}
		ref, _ = p.RecoveryRef(segID)
	}
	if ref.State == logtypes.RecoveryStaged {
		if err := p.TransitionRecovery(segID, logtypes.RecoveryDurableVerified, "", ""); err != nil {
			return err
		}
		ref, _ = p.RecoveryRef(segID)
	}
	receiver := "projection:" + generation
	if ref.State == logtypes.RecoveryDurableVerified {
		if err := p.TransitionRecovery(segID, logtypes.RecoveryWALResponsibilityXfer, "", receiver); err != nil {
			return err
		}
		ref, _ = p.RecoveryRef(segID)
	}
	if ref.State == logtypes.RecoveryCleaned {
		return nil
	}
	if m.recoveryHold != nil {
		_, days, err := groupEventsByUTCDay(source, events)
		if err != nil {
			return err
		}
		for _, day := range days {
			daySource := source
			daySource.UTCDay = day
			hold, reason := m.recoveryHold(daySource, generation)
			if !hold {
				continue
			}
			if err := p.SetRecoveryHold(segID, true); err != nil {
				return err
			}
			if err := m.persist(); err != nil {
				return err
			}
			return fmt.Errorf("ingest: recovery segment %s retained by hold for %s: %s", segID, day, reason)
		}
		if err := p.SetRecoveryHold(segID, false); err != nil {
			return err
		}
		ref, _ = p.RecoveryRef(segID)
	}
	if ref.State == logtypes.RecoveryWALResponsibilityXfer {
		// 释放依据必须与「这段到底有没有副本」一致（复审 P2-13）：
		//   - project://<代次>：本段的凭据就是该代次的已发布投影 → PROJECTION_BACKED；
		//   - manual://delivered-no-copy：静默源出口补出的**无副本**段，凭据同样是「整段已投递并
		//     被逐字段校验」（见 ResolveCoveredGaps 的说明）→ 仍是 PROJECTION_BACKED，
		//     绝不能登记成「下一份副本已验证」；
		//   - 其余路径：按既有语义视为「另有已验证副本」。
		reason := logtypes.ReleaseNextCopyVerified
		switch ref.Path {
		case "projection://" + generation, silentExitNoCopyPath:
			reason = logtypes.ReleaseProjectionBacked
		}
		if err := p.TransitionRecovery(segID, logtypes.RecoveryReleased, reason, receiver); err != nil {
			return err
		}
		ref, _ = p.RecoveryRef(segID)
	}
	if ref.State != logtypes.RecoveryReleased {
		return fmt.Errorf("ingest: recovery segment %s cannot be completed from %s", segID, ref.State)
	}
	if positions.Reclaim < ref.CoversTo {
		if _, err := p.TryReclaim(); err != nil {
			return err
		}
	}
	if err := p.TransitionRecovery(segID, logtypes.RecoveryCleaned, logtypes.ReleaseProjectionBacked, receiver); err != nil {
		return err
	}
	return m.persist()
}

func nextProjectionGeneration(current string) string {
	if current == "" {
		return "projection-1"
	}
	var n int
	if _, err := fmt.Sscanf(current, "projection-%d", &n); err == nil && n > 0 {
		return fmt.Sprintf("projection-%d", n+1)
	}
	return current + "-rebuild"
}

// 物理代次名的「不可复用」保证（2026-10-01 生产事故修复）。
//
// 背景：投影的隔离完全落在物理代次名上——写入按名字打标、校验按名字过滤、查询侧白名单也按
// 名字列举。而名字来自本机状态的单调计数器（nextProjectionGeneration）。状态一旦重生
// （旧 JSON→SQLite 迁移、scripts/rollback-log-index.sh 把归档 JSON 改回、state 被重置），
// 计数器就从头开始，新名字与 VL 里**上一轮生命周期**留下的行重名。VL 只追加不删除
// （「取代」只改查询侧白名单，物理行保留到 retention 到期），于是校验查询把旧行读成本次写入
// 的内容 → `unexpected or duplicate projection event_id` → 校验永不通过 → 运行时创建失败、
// 采集停摆；重试若仍从同一状态出发，还会反复写同一个名字，把 VL 越写越脏。
// 生产实测（2026-10-01 05:20）：projection-88 撞上当天 03:22 那一轮同名整窗重发，三次重试
// 全写 88，27 分钟无进展，最终只能空库重建恢复。
//
// 修复：选代次名前先问 VL「这个名字在该源上是否已有行」，已有就按 1/2/4/8… 递增跳到未占用的
// 名字。探测失败不阻断投递（写入自身失败由既有语义处理），只告警后沿用旧行为；探测覆盖本次
// 写入路由所在的 VL 目标（跨 VL 目标的极端情形仍按旧行为，不会比修复前更差）。
const (
	// maxProjectionGenerationProbes 是单次选名的探测上限。按 2 的幂递增，16 次可越过约 65535 个
	// 已占名字（真实事故只越过 7 个：88→94），同时保证极端情形下不会无限探测。
	maxProjectionGenerationProbes = 16
	// projectionProbeTimeout 是单次占用探测的超时。探测只是「避免重名」的保障，不允许把投递卡在
	// 网络上，故用远小于校验窗口（默认 5 分钟）的短超时。
	projectionProbeTimeout = 5 * time.Second
)

// nextFreeProjectionGeneration 返回该源在 VL 中尚未出现过的物理代次名。
//
// VL 客户端不可用时退回既有行为（只进位一次，不做占用探测）——探测是保障而非前置条件。
func (m *Manager) nextFreeProjectionGeneration(source SourceConfig, current string) string {
	client, _, err := m.clientForSource(source)
	if err != nil {
		return nextProjectionGeneration(current)
	}
	return m.nextFreeProjectionGenerationWithClient(client, source, current)
}

// nextFreeProjectionGenerationWithClient 是探测的实现（显式收客户端，便于按天路由与用例注入）。
func (m *Manager) nextFreeProjectionGenerationWithClient(client *vlsup.Client, source SourceConfig, current string) string {
	key := source.LogSourceID + "/" + source.SourceGeneration
	candidate := nextProjectionGeneration(current)
	first := candidate
	step := 1
	for probe := 0; probe < maxProjectionGenerationProbes; probe++ {
		if m.projectionGenerationKnownFree(key, candidate) {
			// 缓存命中（复审 P2-11）：本进程已为该源写过更大的代次名，而该源的代次名只有本
			// Worker 会写，故这个名字不可能已在 VL 中存在——省掉一次「无时间窗 + limit 1」
			// 的探测查询（60 源每轮一次，量级可观）。
			if candidate != first {
				slog.Info("投影代次与历史投影重名，已进位到新代次",
					"source", source.LogSourceID, "generation", candidate, "from", current)
			}
			return candidate
		}
		ctx, cancel := context.WithTimeout(context.Background(), projectionProbeTimeout)
		used, err := m.projectionGenerationUsed(ctx, client, source, candidate)
		cancel()
		if err != nil {
			slog.Warn("投影代次占用探测失败，按未占用继续", "source", source.LogSourceID, "generation", candidate, "error", err)
			return candidate
		}
		if !used {
			// 实探确认未占用 → 记入缓存，同一 (源, 代次) 不重复探测（复审 P2-11）。
			// 该条目会在真正写入该名字时立即失效（见 recordProjectionGenerationAttempt）。
			m.rememberProjectionGenerationFree(key, candidate)
			if candidate != first {
				slog.Info("投影代次与历史投影重名，已进位到新代次",
					"source", source.LogSourceID, "generation", candidate, "from", current)
			}
			return candidate
		}
		advanced, ok := bumpProjectionGeneration(candidate, step)
		if !ok {
			// 名字不是 `projection-<n>` 形态：不猜测语义，保持既有行为。
			break
		}
		candidate, step = advanced, step*2
	}
	return candidate
}

// projectionGenerationKnownFree 报告「本进程已确认该 (源, 代次) 未占用，无需再探测」。
//
// 缓存内容（复审 P2-11）：每源一组**已实探确认 VL 中不存在**的代次名。写入该名字时立即失效
// （见 recordProjectionGenerationAttempt），因为那一刻起 VL 里可能已经有它的行——失效是安全
// 方向的关键：半途失败的写入同样可能留下行，缓存不能把失败的尝试当成「依然未占用」。
//
// 边界（为什么不能更进一步）：稳态下候选名每批前进一名，因此「同一 (源, 代次) 被重复探测」
// 主要出现在「同一状态被重复推进」的场景（探测进位循环、未落写的重试窗口）。**不能**改成
// 「本进程写过 projection-N 就跳过 projection-N+1 的探测」：名字是否被占用只由 VL 决定，
// 而状态回滚/重置（2026-10-01 生产事故）会让 VL 里存在计数更大的同名旧行——那正是这道探测
// 存在的理由，去掉它等于把「同名旧行被读成本次写入 → 校验永不通过 → 采集停摆」重新引进来。
func (m *Manager) projectionGenerationKnownFree(key, generation string) bool {
	if generation == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	probed := m.generationProbeCache[key]
	if probed == nil {
		return false
	}
	return probed[generation]
}

// rememberProjectionGenerationFree 记录「该 (源, 代次) 已实探确认未占用」。
//
// 缓存按源有界（maxGenerationProbeCachePerSource）：条目在写入时失效，正常路径下每源同时
// 至多一两条；上限只是防止异常路径（同一状态被反复推进）把内存拖大。
func (m *Manager) rememberProjectionGenerationFree(key, generation string) {
	if generation == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.generationProbeCache == nil {
		m.generationProbeCache = map[string]map[string]bool{}
	}
	probed := m.generationProbeCache[key]
	if probed == nil || len(probed) >= maxGenerationProbeCachePerSource {
		probed = map[string]bool{}
		m.generationProbeCache[key] = probed
	}
	probed[generation] = true
}

// recordProjectionGenerationAttempt 使该 (源, 代次) 的「未占用」缓存失效。
//
// 时机是**写入尝试之前**：写入可能只完成一部分就失败，此后 VL 里可能已有该名字的行，
// 缓存必须立刻失效，否则重试会跳过探测、直接往同名旧行上写（同名冲突正是停摆的成因）。
func (m *Manager) recordProjectionGenerationAttempt(key, generation string) {
	if generation == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	probed := m.generationProbeCache[key]
	if probed == nil {
		return
	}
	delete(probed, generation)
}

// maxGenerationProbeCachePerSource 是单源「已确认未占用」代次名的缓存上限。
//
// 正常路径下每源的条目在写入时即失效（同时至多一两条），上限只防异常路径（同一状态被反复
// 推进）把内存拖大；越界即整组重建，退化为「重新实探」——保守方向。
const maxGenerationProbeCachePerSource = 64

// projectionGenerationUsed 报告 VL 中该源是否已有该物理代次的行（limit 1，命中即返回）。
func (m *Manager) projectionGenerationUsed(ctx context.Context, client *vlsup.Client, source SourceConfig, generation string) (bool, error) {
	if client == nil {
		return false, nil
	}
	// 选择器与写入/校验同口径（源 + 来源代次），但不带时间窗：同一名字在任何一天的旧行都会
	// 让「整窗重发」的校验失败，故占用判定必须覆盖该源的全部数据。
	selector := "projection_generation:=" + strconv.Quote(generation) +
		" AND log_source_id:=" + strconv.Quote(source.LogSourceID) +
		" AND source_generation:=" + strconv.Quote(source.SourceGeneration)
	params := url.Values{"query": {selector + " | fields _time | limit 1"}}
	used := false
	err := client.Stream(ctx, "/select/logsql/query", params, func(body io.Reader) error {
		scanner := bufio.NewScanner(io.LimitReader(body, 1<<20))
		scanner.Buffer(make([]byte, 64*1024), 1<<20)
		for scanner.Scan() {
			if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
				continue
			}
			used = true
			return nil
		}
		return scanner.Err()
	})
	if err != nil {
		return false, err
	}
	return used, nil
}

// bumpProjectionGeneration 把 `projection-<n>` 形态的代次名按 delta 进位；格式不符时返回 false
// （调用方保持既有行为，不再探测）。
func bumpProjectionGeneration(current string, delta int) (string, bool) {
	var n int
	if _, err := fmt.Sscanf(current, "projection-%d", &n); err != nil || n <= 0 {
		return "", false
	}
	return fmt.Sprintf("projection-%d", n+delta), true
}

// eventBodyLookup 返回「按 EventID 取回 canonical 事件体」的查询函数（B1a 引用水合）。
//
// 单次遍历该源的事件段存储建索引。存储不可用或遍历失败时返回恒 false 的查询函数——
// 调用方据此走「取不到正文 → 记可见缺口」的路径，而不是静默丢弃。
func (m *Manager) eventBodyLookup(key string) func(eventID string) (logtypes.Event, bool) {
	if m.events == nil {
		return func(string) (logtypes.Event, bool) { return logtypes.Event{}, false }
	}
	index := make(map[string]logtypes.Event)
	if err := m.events.Iterate(key, func(ev logtypes.Event) error {
		index[ev.EventID] = ev
		return nil
	}); err != nil {
		slog.Warn("WAL 引用恢复：遍历事件段存储失败，将按缺失记账", "key", key, "error", err)
		return func(string) (logtypes.Event, bool) { return logtypes.Event{}, false }
	}
	return func(eventID string) (logtypes.Event, bool) {
		ev, ok := index[eventID]
		return ev, ok
	}
}

// canonicalRecoveryEvents 返回该源完整的 canonical 事件集合（VL 数据根丢失后重建 projection 的权威输入）。
//
// 事件体来自磁盘段（eventstore）；仅当该源仍是旧格式（EventsStored=false）时才用内联切片。
// 集合语义不变：仍为**全部**已登账事件，不做任何裁剪。
func (m *Manager) canonicalRecoveryEvents(key string, saved persistedSource) ([]logtypes.Event, error) {
	seen := make(map[string]string, len(saved.WAL))
	events := make([]logtypes.Event, 0, len(saved.WAL))
	add := func(event logtypes.Event) error {
		if event.Record.End <= event.Record.Start {
			return nil // A legacy zero-width record cannot become a canonical event.
		}
		var err error
		event, err = normalizeCanonicalEvent(event)
		if err != nil {
			return err
		}
		// 该 event_id 的规范哈希已在本次登账集合中时不重复入账（只用于身份对账）。
		if existing, ok := seen[event.EventID]; ok {
			if existing != event.CanonicalHash {
				return fmt.Errorf("ingest: IDENTITY_CONFLICT for event_id %s", event.EventID)
			}
			return nil
		}
		seen[event.EventID] = event.CanonicalHash
		events = append(events, event)
		return nil
	}
	// 段的权威性**不依赖 state 里的 EventsStored 标记**：该标记随 state 一起持久化，而
	// state 可能被重置或丢失（运维处置/损坏恢复），此时段本身仍完好在盘上；若因标记为假而
	// 改用（已空的）内联集合，就会得到比段更短的权威集，进而触发 appendEvents 的
	// 「eventstore ahead of authoritative set」硬失败——2026-09-29 生产实测：重置 state 后
	// 采集再也起不来（每轮创建都失败），而事件本体其实一直都在段里。
	// 因此判据改为「段存储里确实有该源的事件」：有就以段为准（与设计意图一致：先段内容、
	// 后 durable WAL）。
	storedCount := 0
	if m.events != nil {
		n, err := m.events.Count(key)
		if err != nil {
			return nil, err
		}
		storedCount = n
	}
	if storedCount > 0 {
		if err := m.events.Iterate(key, add); err != nil {
			return nil, err
		}
	} else {
		for _, event := range saved.Events {
			if err := add(event); err != nil {
				return nil, err
			}
		}
	}
	for _, entry := range saved.WAL {
		if entry.Durable {
			if err := add(entry.Event); err != nil {
				return nil, err
			}
		}
	}
	return events, nil
}

func normalizeCanonicalEvent(event logtypes.Event) (logtypes.Event, error) {
	if event.EventTimeUTC != "" {
		return event, nil
	}
	when, err := time.Parse(time.RFC3339Nano, event.IngestTimeUTC)
	if err != nil {
		return event, fmt.Errorf("ingest: event %s has no stable event or ingest time: %w", event.EventID, err)
	}
	event.EventTimeUTC = when.UTC().Format(time.RFC3339Nano)
	event.CanonicalHash = logtypes.CanonicalContentHash(event.EventTimeUTC, event.Level, event.Stream, event.Message)
	return event, nil
}

func (m *Manager) publish(source SourceConfig, projectionGeneration string, events []logtypes.Event, replace bool, observed ...*catalog.Record) error {
	key := catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay}
	// 权威集合的最大 record_end（封闭可见水位）：按源缓存 + 本次 events 取大，避免为这一个标量
	// 遍历全部历史事件（见 advanceSourceMaxEnd）。快路径下 events 只含本批，
	// 全量历史由缓存（段覆盖水位 + Durable 水位）给出，两者等价。
	batchMax := uint64(0)
	for _, event := range events {
		if event.Record.End > batchMax {
			batchMax = event.Record.End
		}
	}
	maxEnd := m.advanceSourceMaxEnd(source.LogSourceID+"/"+source.SourceGeneration, batchMax)
	if maxEnd == 0 {
		return nil
	}
	// 串行化「读当前 → 应用本源投影 → CAS 写回」整段：共享 namespace 的多个源在并发采集轮下
	// 会互相覆盖（见 publishMu 的说明）。
	m.publishMu.Lock()
	defer m.publishMu.Unlock()
	rec, ok := m.cat.Get(key)
	expected := rec.Clone()
	if len(observed) > 0 {
		// 保留「校验期间权威变更」的拒绝语义：owner / 代数 / 写路由 / 恢复要求被换掉时，
		// 不得把本次投影发布到新权威上（TestOwnerChangeDuringVerificationCannotPublishToNewOwner）。
		//
		// 为什么不再比较投影清单（manifest）与日志序号：并发轮里其他源发布自己的投影是**正常**
		// 变更，若按原口径视为冲突，同一 namespace 的多源就会每轮互相打断（本轮实测：两个 stdio
		// 源只剩 1 条发布）。authority 之外的并发变更一律以「锁内读到的当前记录」为基础合并。
		if !sameCatalogAuthority(observed[0], rec) {
			return fmt.Errorf("%w: Catalog authority changed during projection verification", catalog.ErrInvalidState)
		}
	}
	if !ok {
		rec = catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-"+projectionGeneration)
	}
	if rec.PublishedProjection == nil {
		rec.PublishedProjection = &catalog.PublishedProjection{}
	}
	proj := rec.PublishedProjection
	previousGenerations := publishedProjectionGenerations(proj)
	if len(proj.SourceProjections) == 0 {
		for _, ref := range proj.CoveredSourceGenerations {
			closed := proj.ClosedVisibleSeq[ref.LogSourceID+"/"+ref.SourceGeneration]
			if closed == 0 {
				closed = proj.ClosedVisibleSeq["default"]
			}
			proj.SourceProjections = append(proj.SourceProjections, catalog.SourceProjection{
				SourceGenerationRef: ref, ProjectionGenerations: append([]string(nil), previousGenerations...), ClosedVisibleSeq: closed})
		}
	}
	ref := catalog.SourceGenerationRef{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration}
	index := -1
	for i := range proj.SourceProjections {
		if proj.SourceProjections[i].SourceGenerationRef == ref {
			index = i
			break
		}
	}
	if index < 0 {
		index = len(proj.SourceProjections)
		scope := catalog.SourceProjection{SourceGenerationRef: ref}
		// Older single-source records may omit their redundant source reference.
		if len(proj.SourceProjections) == 0 && len(proj.CoveredSourceGenerations) == 0 && !replace {
			scope.ProjectionGenerations = previousGenerations
		}
		proj.SourceProjections = append(proj.SourceProjections, scope)
	}
	scope := &proj.SourceProjections[index]
	if replace {
		scope.ProjectionGenerations = []string{projectionGeneration}
	} else {
		scope.ProjectionGenerations = appendUniqueGeneration(scope.ProjectionGenerations, projectionGeneration)
	}
	scope.ClosedVisibleSeq = maxEnd
	sort.Slice(proj.SourceProjections, func(i, j int) bool {
		a, b := proj.SourceProjections[i], proj.SourceProjections[j]
		if a.LogSourceID != b.LogSourceID {
			return a.LogSourceID < b.LogSourceID
		}
		return a.SourceGeneration < b.SourceGeneration
	})
	proj.ProjectionGeneration = projectionGeneration
	proj.ProjectionGenerations = nil
	proj.CoveredSourceGenerations = nil
	proj.ClosedVisibleSeq = make(map[string]uint64)
	var maxClosed uint64
	for _, sourceProjection := range proj.SourceProjections {
		proj.CoveredSourceGenerations = append(proj.CoveredSourceGenerations, sourceProjection.SourceGenerationRef)
		for _, generation := range sourceProjection.ProjectionGenerations {
			proj.ProjectionGenerations = appendUniqueGeneration(proj.ProjectionGenerations, generation)
		}
		proj.ClosedVisibleSeq[sourceProjection.LogSourceID+"/"+sourceProjection.SourceGeneration] = sourceProjection.ClosedVisibleSeq
		if sourceProjection.ClosedVisibleSeq > maxClosed {
			maxClosed = sourceProjection.ClosedVisibleSeq
		}
	}
	proj.ClosedVisibleSeq[key.String()], proj.ClosedVisibleSeq["default"] = maxClosed, maxClosed
	manifestData, err := json.Marshal(proj.SourceProjections)
	if err != nil {
		return err
	}
	proj.ManifestVersion = fmt.Sprintf("manifest-%x", sha256.Sum256(manifestData))
	proj.CoverageComplete = true
	proj.QueryLocationDirID = rec.OwnerDirID
	proj.QueryGeneration = rec.Generation
	rec.PublishedProjection = proj
	return m.cat.PublishProjection(expected, rec)
}

// sameCatalogAuthority 判定两份 Catalog 记录是否指向同一权威（owner / 代数 / 目录 / 写路由 /
// 恢复要求）。
//
// 为什么不比较投影清单（manifest）与 journal 序号：并发采集轮里**其他源**发布自己的投影会
// 推进这两者，但那是正常的多源合并，不是权威变更；若按原口径视为冲突，共享 namespace 的源
// 每轮互相打断（本轮实测：两个 stdio 源最终只剩 1 条投影）。两份都为 nil（记录尚不存在）时
// 视为同一权威（都被视为「无权威」）。
func sameCatalogAuthority(a, b *catalog.Record) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		// 一侧缺失：只有在另一侧是「本次发布可安全合并的初始 HOT 权威」时才算同一权威。
		// 为什么不能一律判否：并发轮里同一 namespace 的另一个源可能刚建立记录（HOT/第 1 代/
		// 未冻结/无需恢复），那是正常的多源首写，而不是权威切换；一律判否会让共享 namespace
		// 的源每轮互相打断（本轮实测：两个 stdio 源只剩 1 条投影）。
		other := a
		if other == nil {
			other = b
		}
		return isInitialHotAuthority(other)
	}
	return a.Owner == b.Owner && a.Generation == b.Generation && a.OwnerDirID == b.OwnerDirID &&
		a.WriteRoute == b.WriteRoute && a.RecoveryRequired == b.RecoveryRequired
}

// isInitialHotAuthority 判定记录是否是「HOT 首写」形态的权威（NewStableRecord(OwnerHot,1,...)）：
// 这种记录可以由并发源的投影发布安全合并，不需要按权威切换拒绝。
func isInitialHotAuthority(rec *catalog.Record) bool {
	if rec == nil {
		return false
	}
	return rec.Owner == catalog.OwnerHot && rec.Generation == 1 && !rec.RecoveryRequired &&
		rec.WriteRoute.Owner == catalog.OwnerHot && rec.WriteRoute.Generation == 1 &&
		rec.WriteRoute.DirID == rec.OwnerDirID && !rec.WriteRoute.Frozen
}

func publishedProjectionGenerations(projection *catalog.PublishedProjection) []string {
	if projection == nil {
		return nil
	}
	if len(projection.ProjectionGenerations) > 0 {
		return append([]string(nil), projection.ProjectionGenerations...)
	}
	if projection.ProjectionGeneration != "" {
		return []string{projection.ProjectionGeneration}
	}
	return nil
}

func publishedSourceProjectionGenerations(projection *catalog.PublishedProjection, source SourceConfig) []string {
	if projection != nil && len(projection.SourceProjections) > 0 {
		for _, scope := range projection.SourceProjections {
			if scope.LogSourceID == source.LogSourceID && scope.SourceGeneration == source.SourceGeneration {
				return scope.ProjectionGenerations
			}
		}
		return nil
	}
	return publishedProjectionGenerations(projection)
}

func appendUniqueGeneration(generations []string, generation string) []string {
	for _, existing := range generations {
		if existing == generation {
			return generations
		}
	}
	return append(generations, generation)
}

func (m *Manager) load() error {
	// FR-496：状态从嵌入式 SQLite 索引读入（旧 JSON 迁移已在 openIndex 完成）。
	if m.index == nil {
		// 兼容直接构造 Manager 的测试：没有索引可用，只能保持空状态。
		return nil
	}
	loaded, err := m.loadStateFromIndex()
	if err != nil {
		return err
	}
	m.state = *loaded
	return nil
}

// persist 把内存状态增量写入采集索引（FR-496 spec §2.3）：只写本批次变更的行（UPSERT），
// 事务提交即持久；未变更行不产生任何写语句（空闲轮询不重写历史）。
//
// 成本按「变更源」收敛（这是「60 源下单次持久化 ≤50ms」的关键）：
//   - 只有账本修订号变化、或段覆盖签名变化的源，才重建 WAL 行与账本派生行
//     （gap/source_wal/delivery_batch，占行数绝对多数）；
//   - 根表（source/position/projection/source_aux/instance_binding）行数为 O(源数)，
//     每轮由索引层全量比对（几百行量级），因此「投影代次 / 发布待定 / 实例绑定」这类由
//     Manager 直接改 state 的字段无须任何显式标记即天然正确——但前提是**每轮都必须跑
//     apply**：跳过整次写入等于连根表也不比对了，投影代次等元数据变更会静默丢失
//     （曾以 instances 重启用例转红抓到：发布后的 generation 未落库，重启后重复投影）。
//
// 生产 307MB 副本实测：修复前每次持久化对全部 746,884 行做规划+比对（≈3.0s），
// 且因指纹口径缺陷每次都整表重写（47s）；修复后无变更轮次只比对几百行根表（毫秒级）。
//
// 并发与排队（FR-498 并发采集轮的连带修复）：
//   - 单执行者：构建 + 落库同一时刻只有一次在跑，因此「构建顺序 = 落库顺序」，不可能出现
//     旧快照后写（那会让 persistedRev 已推进但索引回到旧值 → 变更永久丢失）；
//   - 落库在 m.mu 之外：SQLite 事务（批量源下几十毫秒级）不再阻塞登记的 m.mu 短临界区；
//   - 合并（coalescing）：等待者只等「一个周期」，且一个周期可被多个调用方共享——短变更的
//     调用者（实例登记，RPC 截止 10s）不会被采集轮里多个源的持久化挤到队尾
//     （race 实测：不合并时登记 ≥1s 截止，TestRegisterInstanceNotBlockedBySaturatedPollRound 转红）。
//
// 锁序：cycleMu → registerMu → pendingMu → persistGate → m.mu。
func (m *Manager) persist() error {
	m.persistGateEnsure()
	// 合并（coalescing）：本调用按到达顺序声明一个序号；任何「构建发生在声明之后」的落库都会
	// 覆盖它（见下方 covered 的读取位置），因此一个周期可被任意多个调用方共享，短变更调用者
	// （实例登记）不必排到队尾各自再执行一遍。
	//
	// 为什么必须有（FR-498 并发采集轮的连带问题）：并发后多个源同时调用 persist，登记这类
	// 「短变更」调用者排在 8 个 worker 之后（race 实测：单次持久化执行 ≈0.5s、排队累计 ≈1.7s，
	// 登记耗时 ≥1s 截止，使 TestRegisterInstanceNotBlockedBySaturatedPollRound 转红）。
	seq := m.persistJoin.Add(1)
	if m.persistCovered.Load() >= seq {
		return nil
	}
	g := &m.persistGate
	for {
		g.mu.Lock()
		if g.running {
			// 有周期在跑：等它结束。若它的构建发生在本声明之后，本变更已被它覆盖；否则继续
			// 等/接管下一个周期。等待的是「一个周期」，而不是排在队尾。
			g.cond.Wait()
			if m.persistCovered.Load() >= seq {
				err := g.err
				g.mu.Unlock()
				return err
			}
			g.mu.Unlock()
			continue
		}
		g.running = true
		g.mu.Unlock()

		// 覆盖水位必须在**构建之前**读取：在此之后声明的请求，其变更（声明前已写入 state）
		// 必然被本次构建看到，因此可以安全声明「已覆盖到该序号」。反过来（先构建后读）会把
		// 构建看不到的请求也算作已落库，那是静默丢更新。
		covered := m.persistJoin.Load()
		err := m.persistSnapshot()
		if err == nil {
			m.persistCovered.Store(covered)
		}

		g.mu.Lock()
		g.running = false
		g.err = err
		g.cond.Broadcast()
		g.mu.Unlock()
		return err
	}
}

// persistGateEnsure 惰性初始化持久化门（兼容直接构造 Manager 的测试路径）。
func (m *Manager) persistGateEnsure() {
	g := &m.persistGate
	g.mu.Lock()
	if g.cond == nil {
		g.cond = sync.NewCond(&g.mu)
	}
	g.mu.Unlock()
}

// persistSnapshot 在**持持久化门（本轮唯一执行者）**的前提下执行一次完整持久化：m.mu 下
// 构建期望状态，释放 m.mu 后再落库（SQLite 事务不再阻塞登记的短临界区；顺序由门保证，见 persist）。
func (m *Manager) persistSnapshot() error {
	m.mu.Lock()
	store := m.index
	if store == nil {
		// 直接构造 Manager 的测试路径：按需打开索引（不触发迁移）。
		opened, err := stateindex.OpenWithBudget(m.indexPath(), m.indexCommit)
		if err != nil {
			m.mu.Unlock()
			return fmt.Errorf("ingest: 打开采集索引失败: %w", err)
		}
		store = opened
		m.index = store
	}
	if m.persistedRev == nil {
		m.persistedRev = make(map[string]uint64)
	}
	if m.persistedCover == nil {
		m.persistedCover = make(map[string]coverSignature)
	}
	changed := make([]string, 0, len(m.pipes))
	// 源集合 = 管道（生产全部源都有管道）∪ 内存状态（测试可能直接构造只有状态的管理器）。
	// 只有状态的源没有账本修订号可查（管道才是权威），按「每轮都变更」处理，
	// 与旧的全量写入行为等价——这是测试路径的正确性底线，生产路径不经过这里。
	keys := make(map[string]struct{}, len(m.pipes)+len(m.state.Sources))
	for key := range m.pipes {
		keys[key] = struct{}{}
	}
	for key := range m.state.Sources {
		keys[key] = struct{}{}
	}
	// advanced 记录「本轮构建过的源」及其构建时的修订号/覆盖签名，**只在落库成功后才推进**。
	//
	// 为什么不能在构建期就推进（用户质疑 2，2026-10-02 复核）：一次落库失败（磁盘满 / 库被
	// 换成只读 / 进程在 apply 之前被杀）会把这些源永久标记成「已持久化」——下一轮不再重建，
	// 其陈旧行再也不会被重试，索引与内存就此静默分叉。现场形态正是「部分持久化残留的悬挂源
	// 被当成已全量落库」：重启后该源按陈旧水位重读（重复投递），或整行缺失被当成新源
	// （先前已投递的数据无人认账）。失败必须让水位**留在原处**，下一轮自动重试同一批源。
	type persistAdvance struct {
		key   string
		rev   uint64
		cover coverSignature
	}
	advanced := make([]persistAdvance, 0, len(keys))
	for key := range keys {
		p := m.pipes[key]
		if p == nil {
			// 无管道：没有修订号可依据，按变更处理（重建该源的账本派生行）。
			changed = append(changed, key)
			continue
		}
		rev := p.Ledger().Revision(p.Key())
		cover := coverSignatureOf(m.state.Sources[key])
		if m.persistedRev[key] == rev && m.persistedCover[key] == cover {
			continue // 账本与段覆盖都未变：该源本轮不参与行规划（只写变更行的前提）
		}
		m.rebuildSource(key, p)
		advanced = append(advanced, persistAdvance{
			key: key, rev: rev, cover: coverSignatureOf(m.state.Sources[key]),
		})
		changed = append(changed, key)
	}
	// 增量状态：账本派生行只按变更源构建，根行恒为全量（行数为 O(源数)，便宜且兜住
	// 元数据变更）；配合 ApplyScoped 让未变更源的账本派生行根本不参与规划。
	desired, err := m.stateToIndexStateScoped(&m.state, changed)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	// 快照已经构建完成：释放 m.mu（登记路径等的短临界区锁）后再落库。落库顺序由持久化门
	// 保证与构建顺序一致，因此「旧快照后写」不可能发生。
	m.mu.Unlock()
	if _, err := store.ApplyScoped(desired, changed); err != nil {
		// 不推进水位：本轮构建的源在下一轮会被重新构建并重试落库，悬挂源不会静默滞留。
		return fmt.Errorf("ingest: 持久化采集索引失败（水位未推进，下一轮自动重试）: %w", err)
	}
	// 落库成功后才推进水位。需要重新取锁：构建期已释放 m.mu，期间可能有新的变更被声明；
	// 即便账本在这段时间里又前进，这里记下的是**更旧**的修订号，下一轮会因不等而重建（安全方向）。
	m.mu.Lock()
	for _, a := range advanced {
		m.persistedRev[a.key] = a.rev
		m.persistedCover[a.key] = a.cover
	}
	m.mu.Unlock()
	return nil
}

// coverSignature 是「WAL 以何种形态落库」的判据（B1a 按条切分的内联/引用决策输入）。
type coverSignature struct {
	eventsStored bool
	through      uint64
}

func coverSignatureOf(saved persistedSource) coverSignature {
	return coverSignature{eventsStored: saved.EventsStored, through: saved.EventsStoredThrough}
}

// rebuildSource 用该源 pipeline 的当前账本/WAL 快照重建 state 条目。
//
// 它只重建**内容会随采集推进而变化**的部分（账本、WAL、事件覆盖水位）；投影代次、发布待定、
// 内联事件等由 Manager 自己维护的字段原样保留（这些字段的变更由索引层的根表全量比对兜住）。
func (m *Manager) rebuildSource(key string, p *pipeline.Pipeline) {
	prev := m.state.Sources[key]
	entry := persistedSource{Ledger: p.Ledger().Snapshot(), ProjectionGeneration: prev.ProjectionGeneration,
		PublicationPending: prev.PublicationPending, EventsStored: prev.EventsStored,
		EventsStoredThrough: prev.EventsStoredThrough,
		// VerifiedRuns 是 Manager 自己维护的字段（投递路径写入，见 recordVerifiedRuns）：
		// 重建时必须原样保留，否则每次 persist 都会把刚登记的区间级凭据清空
		// （此前漏掉它会使 autoResolveGapsFromPublished 永远拿不到凭据而静默失效）。
		VerifiedRuns: prev.VerifiedRuns}
	// B1a：按条切分——段存储已覆盖（record_end <= EventsStoredThrough）的条目只写引用；
	// 其余（尚未投递、正文未入库，或段存储不可用）必须内联保存，绝不丢正文。
	through := prev.EventsStoredThrough
	refs, inline := p.WAL().SnapshotSplit(func(ev logtypes.Event) bool {
		return entry.EventsStored && through > 0 && ev.Record.End <= through
	})
	entry.WALRefs = refs
	entry.WAL = append(entry.WAL, inline...)
	// 事件体已在磁盘段时不写回内联切片——这正是原先 state 涨到 180MB、每次 persist
	// 触发 130MB MarshalIndent 的根源（FR-484 阶段0 量测）。
	if !entry.EventsStored {
		entry.Events = append(entry.Events, prev.Events...)
	}
	m.state.Sources[key] = entry
}

func newWAL(led *ledger.Ledger, key ledger.SourceKey) *acquire.WAL {
	return acquire.NewWAL(led, key)
}

// multilineUnclosedTimeoutOf 归一化未闭合超时配置。
//
// 口径：0 ⇒ 默认（normalize.DefaultUnclosedTimeout，5s，零配置零行为变化）；
// 负值 ⇒ 关闭（返回 0，调用方据此跳过强制冲刷）。为什么不把 0 也当成「关闭」：
// 0 是 Go 的零值，无法与「未配置」区分，而「未配置」必须是安全默认（会冲刷）。
// 要关闭请显式写一个负值（配置侧 0 与负值都映射为关闭，见 Config.IngestMultilineUnclosedTimeout）。
func multilineUnclosedTimeoutOf(v time.Duration) time.Duration {
	if v < 0 {
		return 0
	}
	if v == 0 {
		return normalize.DefaultUnclosedTimeout
	}
	return v
}
