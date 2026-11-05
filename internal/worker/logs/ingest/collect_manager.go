// Package ingest wires the durable acquisition pipeline into a Worker process.
// It intentionally keeps source state and projection state under the Worker data
// root; CP only sees the resulting Catalog/Query View contract.

// 本文件是 Manager 的**类型定义与装配面**：SourceConfig / Options / persistedState
// / Manager 结构体、budget 与阈值常量、以及「自愈间隔」「扳手估算」这类小设置入口。
//
// 拆分纪律（与 runtime_startup.go 一致，必须保持）：
//   - 同包拆文件，不改变任何导出 API、不改可见性、不改行为；
//   - 方法仍直接访问 m.mu 等私有字段——同包内合法，无需导出；
//   - 跨文件互相调用是正常的，文件边界只是阅读边界，不是模块边界。
//
// 为什么按「读文件的人要找什么」切，而不是按「谁调谁」切：本包的方法调用近乎全连通
// （任何入口都会走到投递/持久化），按调用图切出来的文件彼此无法区分用途。
package ingest

import (
	"context"
	"crypto/sha256"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/archive"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"github.com/wcpe/JianManager/internal/worker/logs/sampling"
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
	// StartAtEOF 表示本源的物理载体刚被替换（FR-502 规则变更导致路径变化），
	// 应从**当前文件末尾**开始采集，而非从头重读整个文件。
	//
	// 为什么必须如此：规则升级会换到新文件，若从头重读，会把该文件里**已投递过的历史内容**
	// 再投一遍，产生重复（真机实测 30 倍）。既有 tailer 在「指纹不匹配」场景已有同款防护
	// （reanchorAtEOFLocked 注释：「禁止按旧偏移回读历史内容」），本字段是该语义在
	// 源登记路径上的入口。缺的内容交由既有对账机制补齐。
	// **不持久化**（json:"-"）：它只描述「这一次重建」的起点选择，落库后无意义
	// （源一旦建立，位置由账本自己维护）。此前带 `json:"start_at_eof,omitempty"` 被
	// `persisted := source` 一起写进索引，重启后再次命中 EOF 跳跃：文件已增长时静默跳过
	// [持久化 read, size) 这段字节且不记缺口；发生过轮转时 Durable > 当前文件大小，
	// AdvanceDurable 触发单调性检查报错 → Register 失败 → 采集运行时起不来（2026-10-19 评审发现）。
	StartAtEOF bool `json:"-" mapstructure:"-"`
	// Charset 源正文的字符集（auto/utf-8/gbk/gb18030）；空串表示跟随节点默认或 auto。
	// 中文 locale 的 JVM 会把 Java 日志写成 GBK，必须按源声明，不能靠猜（缺陷 B）。
	Charset string `json:"charset,omitempty" mapstructure:"charset"`
	// TimeZone 解释日志行内 [HH:MM:SS] 所用的时区（缺陷 C）：空串 = UTC（保持既有行为），
	// "local" = 跟随节点本地时区，其余按 IANA 名解析（如 Asia/Hong_Kong）。
	// 中文 locale 的 JVM 按本地时区写日志，若按 UTC 解释会整体偏移（现场实测 +8 小时）。
	TimeZone string `json:"time_zone,omitempty" mapstructure:"time_zone"`
}

type persistedSource struct {
	PublicationPending bool           `json:"publication_pending,omitempty"`
	Ledger             []ledger.Entry `json:"ledger"`
	// WAL 是**内存队列的镜像**（**不落盘**，重启不恢复）。
	//
	// 2026-10-29：WAL 改为纯内存队列（对齐 Filebeat queue.mem + registry），队列内容不再
	// 跨重启恢复，`source_wal` 表也一并删除。本字段因此不再有落盘语义，只在**本进程内**
	// 充当「canonical 事件集合重建」的补充输入（见 canonicalRecoveryEvents：已 durable
	// 但正文尚未进事件段存储的条目不能漏）。重启后它自然为空——可靠性由 position 水位
	// 回退到 reclaim 后重读承担（见 wal_reboot_replay.go 的 rollbackReadToReclaim）。
	WAL []acquire.WALEntry `json:"wal"`
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
	// IdentityConflicts 是不可裁定身份冲突的隔离记录；冲突行不得进入写入面，也不得被当作已落库证据。
	IdentityConflicts []identityConflictRecord `json:"identity_conflicts,omitempty"`
	// IdentityConflictClearances 是「人工确认并清空身份冲突」的审计凭据（最近若干次，见
	// identityConflictClearance 与 maxIdentityConflictClearances）。
	//
	// 为什么与 IdentityConflicts 同处一个 payload：清空的**唯一**可核验凭据就是
	// 「本条凭据 + 清空后的记录集」这一对事实。放在别处（例如账本条目）会引入一条
	// 本不存在的跨结构一致性要求——而两条数据分属不同写路径时，那条要求无法被守卫。
	IdentityConflictClearances []identityConflictClearance `json:"identity_conflict_clearances,omitempty"`
}

type persistedState struct {
	Sources       map[string]persistedSource `json:"sources"`
	SourceConfigs map[string]SourceConfig    `json:"source_configs,omitempty"`
	Instances     map[string]InstanceBinding `json:"instances,omitempty"`
}

// pendingSpoolRoot 是未绑定实例输出的持久化暂存目录。
const pendingSpoolRoot = "pending"

// startupRecoveryBatch 是启动恢复「读回权威集合 + 对账」的批大小。
//
// 为什么要分批（而不是像改动前那样一次性把所有源的 canonical 事件读进内存）：单源事件集在
// 生产可达数百 MB（全库 18GB），整批常驻会把启动的内存峰值推到 GB 级；分批后内存峰值 ≈
// 批大小 × 单源事件集，而**对账的并发度不受影响**（批内并发，见 Reconcile.Concurrency）。
const startupRecoveryBatch = 4

// startupRecoveryState 是启动恢复的后台进度（观测 + 就绪判据）。
type startupRecoveryState struct {
	// InProgress 表示恢复尚未结束：此时就绪面必须报「未完成」，不得宣告平台可切换。
	InProgress bool
	Total      int
	Completed  int
	// Failed 是**单源**失败计数：单源失败不再使整个 Manager 创建失败（改动前会——那样连
	// 采集与实例管理都起不来），而是记入 Reasons 并继续其余源。
	Failed int
	// Reasons 是逐源失败原因（有界：只保留前若干条，避免刷屏）。
	Reasons []string
	// ReplaysScheduled / ReplaysSkipped 是**启动恢复重发登记**的读数（2026-10-10 第二项修复）。
	//
	// 为什么单列：改动前「判定」与「重发」是同一个内联循环，`Completed` 一次就代表"这个源恢复了"。
	// 移到异步调度后两者必须分开记——`ReplaysScheduled` 表示"已在路上"、`ReplaysSkipped` 表示
	// "本次没有重发"。把二者合并会让现场无法判断到底有没有在重发，而那正是本轮要回答的问题。
	ReplaysScheduled int
	ReplaysSkipped   int
	// ReplaySkipReasons 是逐源跳过原因（有界，与 Reasons 同一上限）。跳过**不等于**已恢复：
	// 缺口仍由发布账本与就绪面照实呈现，此处只是"为什么没有发起重发"的解释。
	ReplaySkipReasons []string
	// ReplaysInFlight 是「已登记、尚无结论」的重发源键（升序，实时取自重建调度器）。
	//
	// 语义（必须说清，否则会被误读）：**非空表示重发仍在路上**，不表示失败；
	// 空也不表示成功——真正的成败判据是发布账本与就绪面（`projection_not_published`），
	// 本字段只回答"我登记的那件事做完没有"。放弃（确定性失败达上限）不在本集合里，
	// 而是以 `Failed` + Reasons 呈现（"不再重试" ≠ "已完成"）。
	ReplaysInFlight []string
}

// Manager owns configured source pipelines and their durable state.
type Manager struct {
	// defaultCharset 是节点级默认字符集（源未显式配置时生效）；空串等价于 auto。
	defaultCharset string
	// defaultTimeZone 是节点级默认时区名（源未显式配置时生效）；空串等价于 UTC。
	defaultTimeZone string
	// acqPolicy 是运行期生效的采集策略（FR-503，见 policy.go）；未装载时一律按
	// DefaultAcqPolicy（规则引擎开）——不能让「没配置」变成「关闭规则」。
	//
	// 用 atomic.Pointer 承载而非 m.mu 保护的普通字段：采集轮**每源每轮**都要读它
	// （实时采集开关判据），热路径上不能再争 m.mu。
	acqPolicy atomic.Pointer[AcqPolicy]
	// 定时归档导入扫描的启停状态（FR-504，受 scanMu 保护）。
	//
	// 为什么需要它：扫描周期可经面板配置（策略每拍下发），值变化时必须能热启停，
	// 而不是只能在启动时按 worker.yml 决定。
	scanMu       sync.Mutex
	scanStop     func()
	scanInterval time.Duration
	scanRunning  bool
	// scanSynced 表示至少同步过一次（区分「从未同步」与「已同步为关闭」）：
	// 关闭状态下重复同步同一周期必须幂等，否则调用方（每拍心跳）会反复收到「发生变化」。
	scanSynced bool
	mu         sync.Mutex
	cycleMu    sync.Mutex
	// 缺口证据必须读取当前发布权威，而非冻结后的写入暂存目标。
	vlReadRoute           func(SourceConfig, *catalog.Record) (*vlsup.Client, error)
	bodyReferenceMu       sync.Mutex
	bodyReferenceProofs   map[string]map[[sha256.Size]byte]struct{}
	vlWritePermitRoute    func(SourceConfig, *catalog.WritePermit) (*vlsup.Client, error)
	resourceBudget        *vlsup.BudgetController
	readLimits            *acquire.ReadLimits
	walReserve            func(int64) (func(), error)
	materializationLimits stateindex.MaterializationLimits
	// cycleMu 持有者观测仪（#57，env/Options 开启；默认关 ⇒ observeCycleMu 返回 nil，调用点只有一次判空）。
	cycleMuProfile bool
	// cycleMuMu 是观测仪的**叶子锁**：只保护下面几个普通字段（绝不在持它时调用外部代码，也不打日志）。
	// 纪律（同 #55-A）：locked() 在**持 cycleMu 时**执行 ⇒ 锁内零 atomic、零日志，
	// 把观测自身的开销排除在「持有时长」之外。
	cycleMuMu          sync.Mutex
	cycleMuHolder      cycleMuHolderInfo // 当前持有者（section + goroutine id + 起表时刻）
	cycleMuHolderWatch *cycleMuWatch     // 该条记录属于谁（只有登记它的观测能清掉它）
	cycleMuWindowStart time.Time         // 取锁密度滚动窗起点
	cycleMuWindowAcqs  int               // 当前窗内取锁次数
	cycleMuRatePerSec  float64           // 上一个已关窗的密度读数（次/秒）
	// 慢持有 / 等锁读数（放锁后结算 ⇒ 可安全用 atomic）。
	cycleMuSlowHoldCount atomic.Int64 // 慢持有累计（超 cycleMuSlowHoldThreshold）
	cycleMuWarnAt        atomic.Int64 // UnixNano；慢持有告警独立节流窗
	cycleMuWaitTotal     atomic.Int64 // 有界获取侧：等锁累计时长（ns）
	cycleMuWaitMax       atomic.Int64 // 有界获取侧：最长单次等待（ns）
	cycleMuWaitAttempts  atomic.Int64 // 有界获取侧的**调用次数**（每次调用记一条，与"探针 20/20 全忙"口径可比）
	cycleMuWaitTimeouts  atomic.Int64 // 有界获取侧：超预算/被取消次数
	// republishSkipped：「发布确认快路径」累计**跳过整窗重发**的次数（单位=次，见 tryConfirmPendingPublication）。
	// 取值语义：pending=true 且非存量重放时，先按最近已发布代次在查询面复核；确认已完整可见 ⇒ 记 1 次。
	// 用途：#5 验收直接判「VL 完整 ⇒ 零重发」是否真的发生 ✓。
	republishSkipped atomic.Int64
	// replaceRowsWritten：写入面（差集后）实际落库行数读数（w21 稿 ③），用于 #12 判读「整窗重发 vs 只写差集」。
	replaceRowsWritten atomic.Int64
	// insertBatch 是「双阈值攒批 + 并发投递」的生效参数（New 时定格，之后不改写 ⇒ 读不加锁）。
	// 见 insert_batch.go 的文件头：4 MiB 字节 / 500 条 / 1s 时间，三者先到先发。
	insertBatch insertBatchLimits
	// insertBatchCounters 是攒批/投递读数（批大小分布、攒批等待、请求数、失败数）。
	// 内存计数器，供 InsertBatchStats() 读；接 /metrics 是下一个任务（FR-485）。
	insertBatchCounters insertBatchCounters
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
	// 单源实际完成 canonical 计数后的测试屏障，生产为 nil。
	reconcileCheckedHook func(string)
	// 仅测试在冻结段快照后暂停逐行读取，生产为nil。
	canonicalReadHook func()
	// lifecycleCtx/lifecycleCancel 是本 Manager 自身生命周期的 ctx：持久化的可取消面
	// （Stop 即取消，长落库不再"服务端继续为没人要的结果删库"）。**只在 New 中赋值、之后不再
	// 改写**（取消用 cancel 函数），故读取无需加锁，也不参与任何锁序。
	lifecycleCtx    context.Context
	lifecycleCancel context.CancelFunc
	// startupRecovery 是启动恢复（对账 + 重发 + 解阻回收）的**后台进度**。
	//
	// 为什么移出 New（2026-10-02 生产事故）：该链在 New 内逐源串行执行，而它的一端是
	// 「读回该源全部 canonical 事件」（生产 18GB）+「剪枝后逐行删索引」（一次 3.4M 行）；
	// New 不返回 ⇒ main 起不到反向隧道 ⇒ worker 20–30 分钟完全不可达（SIGQUIT 取证）。
	// 现在 New 只做「登记 + 账本/段恢复」，恢复在后台续做并在就绪面上如实标注未完成。
	startupRecovery startupRecoveryState
	// startupDone 在启动恢复结束（含提前取消）时关闭；WaitStartupRecovery 据此等待。
	startupDone chan struct{}
	// lastPersistStats 是最近一次落库的读数（观测面）：生产排障据此确认
	// 「①水位化差异删除是否真的启用」（RangePrunes > 0）、分步是否触发（Incomplete）。
	lastPersistStats stateindex.Stats
	// persistYield 是持久化「让路窗口」（见 persistStepYield 的说明）；0 用默认。
	persistYield time.Duration
	// persistPriorityStreak 是老化阈值（连续优先授予上限）；≤0 用默认（见 persistPriorityStreakMax）。
	persistPriorityStreak int
	// persistGateStage 是持久化门的测试观测口（生产 nil，零开销）：在「授予」与「交门」两个
	// 事件点回调，让回归按**事件序**断言公平性（而不是靠耗时阈值猜）。
	persistGateStage func(ctx context.Context, event string, seq int64, priority bool)
	// persistHotBudget 是热路径（采集/投递侧）在持久化门上的等待上界；超时即交给后台通道。
	persistHotBudget time.Duration
	// confirmRequeueBackoff 是「确认取证未出结论」后重排下一次只读确认的退避（测试注入用；
	// 0 表示沿生产默认值，见 confirmRequeueBackoffOf）。
	confirmRequeueBackoff time.Duration
	// persistStepHold 是持久化步骤的测试观测口（生产 nil，零开销）：在**持门执行步骤期间**回调，
	// 让回归能确定性地"把门按住"（阻断即持门），从而断言热路径的有界等待与交办行为。
	persistStepHold func()
	// persistPlanHold 是**门外规划**的测试观测口（生产 nil）：在 planPersist 开头回调，
	// 用于证明"长规划不占门"（变异=把规划移回门内 ⇒ 采集/投递单元被卡 ⇒ 红 ✓）。
	persistPlanHold func()
	// reconcileYield/SliceEvents 是**对账 CPU 让路**的运行时配置（见 groupEventsByUTCDayYielding）。
	reconcileYield       time.Duration
	reconcileSliceEvents int
	// reconcileYieldNotify 是让路次数观测口（生产 nil，零开销）。
	reconcileYieldNotify func()
	// resumeInterval 是**容量自愈兜底**的低频周期（键 log_capacity.resume_interval）。
	resumeInterval time.Duration
	// resumeBatchPerRound 是兜底环每轮处理上限（见 defaultResumeBatchPerRound）。
	resumeBatchPerRound int
	// recoveryQuotaFactor / recoveryDrainGrace / recoveryGraceUntil：**恢复期独立配额**的接线
	// （用户定调方案② ✓）。factor 默认 4（同倍拓宽，仍有界 ✓）；grace 默认 30m（现场排空动辄
	// 小时级 ⇒ 10m 有"刚回归即再锁"的风险 ✓）；graceUntil 是可观测的宽限截止 ✓。
	recoveryQuotaFactor float64
	recoveryDrainGrace  time.Duration
	recoveryGraceUntil  time.Time
	// recoveryProbe 是"恢复是否进行中"的测试注入点（生产 nil ⇒ 用 StartupRecoveryStatus() ✓）。
	recoveryProbe func() bool
	// lastSweepBacklog / lastSweepAt：兜底环**净涨背压**的两轮读数（上一轮的闸视图总积压 ✓）。
	// 现场形态（+463k ✗）：复活让源开始读文件 ⇒ 常规新数据进入 WAL ⇒ 若与此同时继续复活更多源，
	// 就是"越复活越涨" ✗✗ ⇒ 恶化期**不放宽、不复活** ✓（用户 Q18 批准的背压 ✓）。
	lastSweepBacklog int64
	lastSweepKnown   bool
	// sweepBacklogOf 是闸视图总积压的取数口（测试可注入 ✓）。
	sweepBacklogOf func() int64
	// resumeProbe 是兜底扫描的测试观测口（生产 nil）：每个"积压/容量类暂停"的源被扫到时回调。
	resumeProbe func(logSourceID string)
	// scanYield / scanSliceRows / scanYieldNotify：**段读聚合路径的让路**（见
	// publishedClosedForSourceCtx 的逐行回调）。现场 X 光片点名该路径（readSegment）是每轮重活的
	// I/O 大头 ✗：它本就可被 ctx 取消（每行校验 ctx.Err ✓），但**从不让路** ⇒ 单源扫描可以把一个
	// P 占满整轮 ✗。默认 1ms / 8192 行：10 万行源额外 ≈12ms（可忽略 ✓），却把 P 交还调度器 ✓。
	scanYield       time.Duration
	scanSliceRows   int
	scanYieldNotify func()
	// scanBudget 是**单源单轮段读聚合的时间预算**（键 log_index.scan_budget；0 ⇒ 默认 2s）。
	// 现场 `pollOnce` 的 WaitGroup 等分钟级 ✗ ⇒ 单个大源的整段扫描必须被截断：超预算即返回
	// "证据不完整"（= 不当作证据 ⇒ 不消解、不推进 ✓），下一轮续扫 ✓。
	scanBudget time.Duration
	// scanBudgetExceeded 统计"超预算截断"次数（排障观测口）。
	scanBudgetExceeded atomic.Int64
	// readingsConsumed* 是**可达面被消费的调用计数**（用户 2026-10-05 的更稳写法 ✓）：
	// 分钟打点（`sampleResumeDiagnostics`）按名调用这三个方法 ✓ ⇒ 计数随分钟递增即为
	// "**可达面确实被消费**"的直接证据 ✓✓（不读外部源码、不依赖测试工作目录深度 ✗）。
	// 现场读法：打点行里的 `consumed_*` 逐分钟增长 ⇒ 接线活着 ✓（恒 0 ⇒ 接线断了 ✗）。
	readingsConsumedDiagnostics atomic.Int64
	readingsConsumedStats       atomic.Int64
	readingsConsumedInvariants  atomic.Int64

	// backpressureSkips 统计因"上一轮净涨"而跳过的兜底轮数（可观测 ✓）。
	backpressureSkips atomic.Int64
	// sweepTicks / sweepPanics 是兜底环的**心跳与崩溃计数**（2026-10-05 现场：`candidates` 三次**恒定**
	// ⇒ 累计计数不再增长 ⇒ 更像"**环本身停了**" ✗✓，与"被过滤"外部读数无法区分 ✓）。
	// goroutine 内未捕获的 panic 会**静默杀死整个兜底环** ✗ ⇒ recover 连栈一起留证 ✓。
	sweepTicks  atomic.Int64
	sweepPanics atomic.Int64
	// sweepCursor / sweepEligibleNow：**轮转窗口的游标**与**合格暂停源总数（gauge）** ✓
	// （用户 2026-10-05 ②：只取前 N 且不轮转 ⇒ 长暂停源永不被覆盖 ✗✗；合格数单列 ⇒
	// 现场一眼看出"合格 38 vs 本轮处理 2" ✓）。
	sweepCursor      atomic.Uint64
	sweepEligibleNow atomic.Int64
	// sweepStepHold 是"有界步骤"的测试注入点（生产 nil、零开销 ✓）：在 `runSweepStepBounded`
	// 执行 `fn` **之前**回调。为什么注在**这一步内部**（2026-10-05 纪律⑥：注入点必须"准且稳" ✓）：
	// 用全局锁（`m.mu`）注入会**既测错位置**（环入口就要拿它 ✗）**又偶发**（时紧时松 ✗）✓。
	sweepStepHold func()
	// #56：追加被拒（暂停/上限）⇒「排空已 durable 存量」改为**调度**（不在摄取路径内当场执行）。
	// replayMu 只保护下面两个集合与唤醒通道（叶子锁：绝不在持它时调用任何外部代码）；
	// replayWake 是 cap=1 的**非阻塞唤醒**（同源/并发多次请求合并成一次唤醒）。
	replayMu         sync.Mutex
	replayPending    map[string]struct{} // 待排空的源（同源多次被拒 ⇒ 合并成一条）
	replayInflight   map[string]struct{} // 正在排空的源（单飞：同源不并发整窗 replay）
	replayWake       chan struct{}
	deferredDelivery bool
	// workClosed与WG.Add共用replayMu；包括超时后仍存活的旧sweep真实工作。
	workClosed   bool
	workWG       sync.WaitGroup
	stopMu       sync.Mutex
	stopComplete bool
	replayOrder  []string
	replayDue    map[string]time.Time
	// committedReceipts只存完成计划中的摘要；正文仍只由WAL负责，重启全部失效。
	committedReceipts map[string]*committedSourceReceipt
	// 每个已登记源至多一条written窗口提示；不存正文/逐ID表，不持久化，也不直接授予投递或ACK资格。
	writtenAttempts map[string]*writtenAttempt
	// durableDeliveryRetry 由m.mu保护，只记耐久门失败源的下次重试时点，不携正文或提交资格。
	durableDeliveryRetry map[string]time.Time
	// ── #63：重放回收（rolling drain）读数与上界 ───────────────────────────────────────────
	// 现场靶数：344 位/s/源（1.14 亿需 3.7 天）⇒ 最低线 4,000/s/源、四源合计 13,500/s（8h 收官）；
	// 及格语义 = **追平生成速率**（生产仍在灌料 ⇒ 循环必须可长期滚动），而非只清零存量 ✓。
	reclaimRollRounds       atomic.Int64 // 滚动回收调用轮数
	reclaimRollSteps        atomic.Int64 // 累计推进步数（每步 = 一次探测/补段/推进）
	reclaimRollAdvanced     atomic.Int64 // 累计推进位数（位 = 账本 record 位置）
	reclaimRollFirstAdvance atomic.Int64 // 累计"首步推进量"（= 陈旧覆盖段的上界缺口 ✓ 现场判据）
	reclaimRollNanos        atomic.Int64 // 累计墙钟（ns）⇒ 位·s⁻¹ = Advanced/(Nanos/1e9)
	reclaimRollLast         atomic.Value // ReclaimRollSnapshot：上一轮读数（reclaim/delivery/firstAdvance/steps/advanced）
	// reclaimRollMaxSteps / reclaimRollBudget 已于 2026-10-29 删除（它们只服务「分段链逐跳推进」）。

	// replayScheduledTotal 是「追加被拒 ⇒ 登记待办」的累计次数（观测面：证明调度确实发生）。
	replayScheduledTotal atomic.Int64
	// sweepStepBudget 是兜底环**单源单步**的有界等待预算（0 ⇒ 默认 150ms ✓，见 sweepStepBudgetOf）。
	sweepStepBudget time.Duration
	// skipBlocked 统计"本轮因**有界等待超时**而跳过"的次数（2026-10-05 现场 ✓）：
	// 兜底环是**维护环**，绝不该挂在重活上 ✗ —— 现场形态正是环第一轮卡在
	// `DeliverPending→deliver`（等 `m.mu`：被"长持 m.mu 的规划者"挡住 ✓）⇒ ticks=1 冻结 ✓。
	skipBlocked atomic.Int64
	// **三步各自耗时（累计 ns）+ 每轮总耗时 + 上轮处理源数**（新账 1 ✓）：单轮 4–6 分钟而
	// `skip_blocked` 只解释个位数秒 ⇒ 须能分辨"少数慢源"还是"每源均匀慢" ✓（平均每源 = 总耗时/源数 ✓）。
	nsDelivery   atomic.Int64
	nsResolve    atomic.Int64
	nsEvaluate   atomic.Int64
	nsRoundTotal atomic.Int64
	// **在飞被放弃调用的定界**（用户 2026-10-05 新账 2 ✓）：有界执行器只放弃**等待** ✗，
	// 被放弃的调用仍带着批次/事件载荷在后台等 `m.mu` ⇒ 多轮叠加会累积内存（现场 RSS 8–14GB/轮爬升 ✓，
	// 后证非单调 ⇒ 暂态可接受 ✓，但极端负载下必须**可定界** ✓）。
	sweepInflight atomic.Int64 // 当前在飞（含被放弃）的步骤数 ✓
	// 单次准入读数后的确定性交错测试屏障，生产为 nil。
	sweepAdmissionHook func()
	skipInflightCap    atomic.Int64 // 因"在飞已达上限"而拒绝发起的步数 ✓
	lastRoundSources   atomic.Int64
	// **四相下探**（2026-10-05 分型结论 ✓）：99.7% 在"三步之外" ⇒ 拆成三相计时 + 派生"其余" ✓。
	// 判读：某相**远超"逐源锁 × 59"合理量级** ⇒ 该相**被长持有者挡住**（与 m.mu 排队同源 ✓）；
	// 各相都在合理量级 ⇒ 是**遍历本身** ⇒ 批量化（三遍并一遍 ✓）。
	nsScanElig     atomic.Int64
	nsBacklogTotal atomic.Int64
	nsSyncQuota    atomic.Int64
	// **逐相"最长单次 m.mu 等待"**（支 A/B 判据 ✓，见 noteMuWait 注释 ✓）。
	muWaitBacklog atomic.Int64
	muWaitQuota   atomic.Int64
	muWaitSources atomic.Int64
	muWaitResume  atomic.Int64
	// **兜底环每轮的跳过原因分类计数**（2026-10-05 现场：41 家三项前置全满足却不复活 ✗ ⇒
	// 必须能直接读出"是哪一条把它挡了" ✓）。
	risingStreak     atomic.Int64
	skipBackpressure atomic.Int64
	skipHysteresis   atomic.Int64
	skipEnumeration  atomic.Int64
	sweepCandidates  atomic.Int64
	sweepRevived     atomic.Int64
	// selfHealDuePass / selfHealDueSkip（#66-⑤ 诊断，只读）：自愈面**限频闸**的命中/未命中计数。
	// 为什么要它：现场"四落后源零 `滚动回收` 行"的第一嫌疑是"自愈面根本没轮到它们"——而准入链上
	// 可能拦住它们的位置只有两处：① **状态闸**（未暂停且零未解决缺口 ⇒ 直接返回，**不计入**本对计数）；
	// ② **限频闸**（本对计数 ✓）。两者并读即可分辨（pass 有增长 ⇒ 状态闸放行了；skip 恒涨而 pass 不动
	// ⇒ 全被限频挡住；两个都恒为 0 ⇒ 连状态闸都没放行或自愈面未被调用 ⇒ 嫌疑上移到管道/轮转面 ✓）。
	selfHealDuePass atomic.Int64
	selfHealDueSkip atomic.Int64
	// selfHealStallCount / selfHealStallSkip（改动 C 诊断，只读）：自愈面「无进展退避」的
	// 计数——**进入退避**的累计次数，以及**本轮投递被退避挡下**的累计次数。
	// 读法：stall 恒涨而 skip 不动 ⇒ 每轮都有进展（退避被进展不断重置，正常 ✓）；
	// stall 与 skip 同步增长 ⇒ 该源确实卡住且已限频（正是退避要治的形态 ✓）。
	selfHealStallCount atomic.Int64
	selfHealStallSkip  atomic.Int64
	// #179 第 1 步（③ 观测，行为中性）：补段助手的**四处静默 return** 的按源去重计数 + 首次 Debug。
	// 为什么要它：现场"某源零 `滚动回收` 行、且看不出为什么不动"的根因就是这四处全静默 ✗。
	blockedSegHold     atomic.Int64 // 段 hold（HasRecoveryHold）
	blockedConfigHold  atomic.Int64 // 配置层 hold（m.recoveryHold）
	blockedNoMaterial  atomic.Int64 // to <= from（无"已确认未覆盖"区间）
	blockedWatermark   atomic.Int64 // 水位复读失败（变则弃）
	blockedStallWarns  atomic.Int64 // ② 有界判据：N 轮无进展 ⇒ 告警次数（可观测 ✓）
	blockedLoggedMu    sync.Mutex
	blockedLogged      map[string]struct{} // 按源去重（首次 Debug 一次，不刷屏 ✓）
	reclaimStallMu     sync.Mutex
	reclaimStallRounds map[string]int // ② 按源"回收停滞轮次"（reclaim 未前进的连续轮数 ✓）
	reclaimStallLast   map[string]uint64
	// blockedNoCopy 是「责任转移被拒：该区间还没有确认副本」的计数（2026-10-21 数据丢失修复）。
	// 单独分账而不并入 blocked* 三类：那三类的语义都是"暂无可推进的区间"（无进展但不是缺陷），
	// 而这一类是**本来会删掉未投递数据**的拒绝 ⇒ 必须能独立读出来，否则下次现场仍无法区分。
	// 位置放在既有对齐块**之后**：插进块中间会让 gofmt 重排整块（纯噪声 diff ✗）。
	blockedNoCopy atomic.Int64
	// sweepRollingProbes / nsRolling（#66-⑤ 诊断，只读）：兜底环轮转窗口里**实际发起**的
	// "只滚动/补段"自愈次数与耗时（前置是 O(1) 的"有欠账"判据 ⇒ 健康源不发起 ⇒ 本读数同时是
	// **成本定界**的证明 ✓）。
	sweepRollingProbes atomic.Int64
	nsRolling          atomic.Int64
	// sweepDeliveryProbes / nsRollingDelivery（#66-⑥ 诊断，只读）：同一窗口里**实际发起**的
	// "有界投递步"次数与耗时（前置是 O(1) 的"投递欠账"判据 delivery < read ✓ ⇒ 无欠账不发起 ✓）。
	sweepDeliveryProbes atomic.Int64
	nsRollingDelivery   atomic.Int64
	// sweepDirectDeliverProbes / nsRollingDirectDeliver（#179-② 诊断，只读）：兜底环轮转窗口里
	// **实际发起**的「破环直投」步次数与耗时。该步含 Ledger 深克隆 + WAL 全量快照 + 同步 VL 写入
	// （本文件自述"把单轮拖到 48.5 分钟"的 O(n) 重活），此前**没有任何读数** ⇒ 兜底环诊断里完全
	// 不可见；有了它才谈得上断言单轮有界（与相邻两步同款的前置 O(1) 判据）。
	sweepDirectDeliverProbes atomic.Int64
	nsRollingDirectDeliver   atomic.Int64
	// sweepBackpressureStreak 是窗口化背压的阈值（连续 N 轮净涨才跳过 ✓；0 ⇒ 默认 3）。
	sweepBackpressureStreak int
	// sweepInflightCap 是在飞并发上限（见 defaultSweepInflightCap ✓）。
	sweepInflightCap int
	// persistRequests/persistWorkerDone 是**后台持久化通道**：热路径超时后把「请落库」交给它，
	// 由独立 goroutine 完成（它才是允许阻塞的那一方）。cap=1：已有待办请求即无需重复入队。
	persistRequests   chan struct{}
	persistWorkerDone chan struct{}
	// resolvePersistBudget 是**解算收尾落库**在持久化门上的有界等待预算（#59；0 ⇒ 默认 2s）。
	// 现场（探针自栈两现）：handler 卡在 persistGate.Cond.Wait 达 45s —— 而该源的账本写
	// （AbandonGapsThrough）早已落账 ⇒ 卡的是"索引落库排队"，属**可交办**的工作。
	resolvePersistBudget time.Duration
	// persistResolveHandoffs 是解算收尾"交办后台"的累计次数（#59 观测读数：0 ⇒ 全部同步落库完成）。
	persistResolveHandoffs atomic.Int64
	// persistClearConflictsHandoffs 是「清空身份冲突」收尾"交办后台"的累计次数。
	// 与解算分开记账：合并计数会让探针无法区分"卡在解算"与"卡在清空"。
	persistClearConflictsHandoffs atomic.Int64
	// replayBudget 是整窗重发的切片预算（见 ReplayBudget）。
	replayBudget ReplayBudget
	// hotRetention 是热层 VL 的 retention 时长，用于「热层可发布窗口」判定
	// （见 hot_retention_window.go）；0 = 未知/不启用过滤（保持改动前的行为）。
	hotRetention time.Duration
	// vlProbeTimeout / vlNotReady 是 VL ready 门禁的读数：probe 超时与「哪些源当前被判未就绪」。
	// vlNotReady 只用于**状态变化时才打日志**（否则每轮每源刷屏，现场日志会被淹掉）。
	vlProbeTimeout time.Duration
	vlReadyWait    time.Duration
	vlNotReady     map[string]string
	root           string
	// logScratch 是所有**临时落盘面**的根目录（raw / pending spool）。
	//
	// 2026-10-30 用户裁定：机械盘不做任何落盘。而 raw（stdio 原始字节）与 pending
	// （未绑定实例的暂存）本质上都是**投递前的中转**，不是长期存储 ⇒ 放到 tmpfs。
	//
	// 为什么单独一个字段而不是改 root：root 同时用于定位配置文件、诊断等需要长期
	// 保留的东西；拿它当临时目录会一并搬走不该搬的东西。
	logScratch string
	vl         *vlsup.Client
	vlRoute    func(SourceConfig) (*vlsup.Client, bool, error)
	cat        *catalog.Catalog
	journal    catalog.Journal
	archive    *archive.Registry
	sources    map[string]SourceConfig
	pipes      map[string]*pipeline.Pipeline
	state      persistedState
	// index 是采集索引的嵌入式 SQLite 存储（FR-496，spec §2.1）：persist 只写变更行。
	// nil 表示尚未打开（测试直接构造 Manager 的场景在首次 persist 时按需打开）。
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
	// startAtEOF 标记「下一次登记该源时应从文件末尾开始」的一次性意图（FR-502）。
	//
	// 由规则变更路径（detachSourcesForRuleChange）写入，登记时被 takeStartAtEOF 取走并清除。
	// 用一次性标记而非持久字段：它只描述「这一次重建」的起点选择，落库后无意义
	// （源一旦建立，位置由账本自己维护）。
	startAtEOF          map[string]bool
	verificationTimeout time.Duration
	// verifyMaxQueriesPerChunk 是单簇单次校验的尝试次数上限（0 用默认）。
	verifyMaxQueriesPerChunk int
	// verifyLimiter 是校验/对账查询的全局天花板（速率 + 在飞）。
	verifyLimiter *verifyLimiter
	// verifyRoundProgress 记录**本轮已完成多少块校验**：轮级预算只允许在"本轮已有进展"之后
	// 才把后续源推迟到下一轮 ✗——否则慢环境（-race 放大 / VL 变慢）下第一个源就会吃掉整轮预算，
	// 之后每个源都被推迟 ⇒ 同一批源**每轮都被推迟**、永不判定 ✗✗（实测：race 下 12 源夹具停滞，
	// 未就绪源数长期不降）。保证"每轮至少推进一个"是本修复的硬要求 ✓。
	verifyRoundProgress atomic.Int64

	// verifyRoundBudget 是**每源每轮的校验时间预算**（可配，默认 1.5s）。
	//
	// 为什么（2026-10-03 现场 Q4：未暂停源 readΔ 全 0）：采集轮是**全源屏障**——轮内固定 worker
	// 逐源串行，少数源卡在"校验等待"就把 worker 占住，本轮其余源连 `Poll` 都轮不到 ✗。
	// 有了本预算：单源本轮最多等这么久，超出即**本轮放弃**（结论与"窗口内未可见"同族 ✓），
	// 交回下一轮 ⇒ 轮墙钟有界 ⇒ **每个源每轮都能跑到 Poll** ✓✓。
	verifyRoundBudget time.Duration
	// verifyVisibilityFloor 是"可见性错开"等待（键 log_index.verify.visibility_floor）：
	// >0 时整批首探前先等到"距批写入 ≥ floor"——VL 写入→可查询有 ~1.02s 硬地板，本函数紧跟
	// insert 之后调用 ⇒ 立即探测必然全落在地板内（0/0.2/0.6s 三次白探）；等到地板后再探 ⇒
	// 首探即命中。≤0 = 关（默认关）。
	verifyVisibilityFloor time.Duration
	// reconcileLockBudget 覆盖「取候选等 m.mu」的有界上限（0 用默认常量
	// reconcileCandidateLockBudget；测试用小值走真实限额路径，与 SetReconcileLoopConfig 同风格）。
	reconcileLockBudget time.Duration
	// reconcileCursorKeySnapshot 是"对账游标当前所指源 key"的**无锁快照**（2026-10-04）：
	// 供"锁超时"快速放弃路径在不取 m.mu 的前提下带上"已推进到哪"，解除点名掩蔽（父级裁定 ①）。
	reconcileCursorKeySnapshot atomic.Value // string
	// verifyRoundDeadline 是本轮的校验截止时刻（unix nanos；0 表示不限，用于非轮内调用路径）。
	verifyRoundDeadline atomic.Int64
	// verifySourceBudget 是**每源**的校验预算状态（key = "logSourceID/generation"）：
	// 每源在轮内被处理前**各自**设置截止时刻（见 pollOnce → setVerifySourceDeadline）。
	// 校验**优先**读本源状态（2026-10-03 回退修复 ③：消除"慢源吃掉整轮预算 ⇒ 轮内后段源
	// 全部 defer"的**队头阻塞**——深积压下单轮墙钟 ≫ 预算即触发）；未设 per-source 的
	// 路径（启动恢复/对账）回退轮级 verifyRoundDeadline（语义不变 ✓）。
	verifySourceBudget sync.Map // map[string]*verifySourceBudgetState
	// verifyChunkEstimates 是「单源校验预算随事件数放大」的档位（改动 B）。
	// 零值 = 完全关闭放大 ⇒ 预算与修复前逐字一致（保守默认，见 VerifyChunkEstimates）。
	verifyChunkEstimates VerifyChunkEstimates
	// verifyDeferred 是"因轮级预算而本轮放弃校验"的计数（可观测 ✓）。
	verifyDeferred atomic.Int64
	// verifyBackoffMin/verifyBackoffMax 是投影校验重试的退避区间（B1c）；0 用默认常量。
	verifyBackoffMin time.Duration
	verifyBackoffMax time.Duration
	// verifyChunkEvents 是单次校验查询覆盖的事件上限（分簇粒度，FR-498 P0）；0 用默认常量。
	// 作为字段而非裸常量：不同 VL 部署的返回体承载能力不同，且回归要用真实限额路径驱动。
	verifyChunkEvents int
	// verifyChunkConcurrency 是同批内各校验簇的查询并发度；0/负值取默认，1 为串行。
	verifyChunkConcurrency int
	// eventsStoredRows 缓存每源「段存储已落行数」，避免 appendEvents 每批 Count（其内部要
	// 校准末段 = O(末段行数) 的整段读取）。本进程是段的唯一写者，追加成功后按增量推进即可。
	eventsStoredRows map[string]int
	// eventsStoredEpoch 是每源「段物理版本号」：本进程每次真正追加段就自增
	//（appendEvents / appendEventTail 是本进程唯一的两个段写入出口），**段回收成功后也自增**
	//（invalidateEventStoreRowCaches，见 eventstore_prune.go）。它是 storedCoverageMemo
	// 的有效性凭据之一，与段行数一起回答「这段内容还是不是记备忘时那段」。
	//
	// 为什么这两个读数够：**追加路径**是只追加结构，除「末段半行截断」（崩溃残留校准）外没有
	// 改写路径，而截断会改变行数（见 eventStoreCoverage 的说明）。头部回收会减少行数，
	// 由上面的 epoch 自增 + 显式删备忘覆盖「回收后又追加回同样行数」的形态。
	eventsStoredEpoch map[string]uint64
	// storedCoverageMemo 缓存每源「某个内容指纹的事件集合已被段逐条覆盖」的核对结论
	//（见 eventStoreCoverage）。
	//
	// 为什么需要（这是性能正确性、不是可选优化）：覆盖判定必须按**身份**逐条比对（这正是
	// 「段物理行数被冗余行抬高 ⇒ 误判段超前于权威集合 ⇒ 永久硬失败」的修复），而逐条比对
	// 要遍历整段并 JSON 解码每一行——生产实证（inst:148，126MB / 135113 行）**单次 1.7s**
	//（本机只读实测，3 次取整）。
	// 偏偏慢路径是「同一份权威全量被反复问」的形态：`planDeliveryTail` 在「本批区间与已落段
	// 范围重叠」时退回慢路径（见其 minStart 判据），而重放/重建按 UTC 天切片推进，每天都带着
	// 同一份权威全量进来；单轮预算只有 90s（defaultReplayMaxDuration）且还要容纳 VL 写入。
	// 没有备忘就是「每批/每天一次全段遍历」，足以把整轮预算耗尽 ⇒ 收束时置 `PublicationPending`
	// ⇒ 回到「pending 钉死」的老路 ✗。备忘把重复问的代价压到近似零。
	storedCoverageMemo map[string]eventStoreCoverage
	// eventStoreIdentityConflicts 累计「段与权威集合对同一 event_id 持有不同内容」的次数
	//（观测面：这类冲突必须可见，不能被静默吞掉）。
	eventStoreIdentityConflicts atomic.Int64
	// storedCoverageHits 累计覆盖备忘命中次数（观测/回归用：让「重复问不再遍历段」可被断言，
	// 而不必依赖耗时阈值）。
	storedCoverageHits atomic.Int64
	// adjudications 是「正常身份裁定」留痕的节流聚合状态（见 traceIdentityAdjudication）。
	// 每个 Manager 一份：无包级可变状态，用例之间互不干扰。
	adjudications identityAdjudicationThrottle
	// conflictTraces 是「不可裁定身份冲突」留痕的节流聚合状态（见 traceIdentityConflict），
	// 由 New 用 newConflictThrottle() 初始化（阈值与正常裁定分开）。
	//
	// 为什么它必须与 adjudications **分开计数**：两者是不同性质的信号——正常收敛是收敛，
	// 真冲突是数据歧义。合用一个节流器会让「今天有多少次真冲突」被正常收敛的洪水量级淹没。
	conflictTraces identityAdjudicationThrottle
	// identityConflictTraces 累计「已留痕（含被节流折叠）的不可裁定冲突」条数（观测用）。
	//
	// 为什么必须导出读数：节流的前提是「总数仍可查」，否则它就是静默。
	identityConflictTraces atomic.Int64
	// canonicalRecoveryReuses 累计「一轮投递内复用权威集合」的次数（观测/回归用：让
	// "pending 轮不再重复全量重建"这一事实可被断言，而不必依赖耗时阈值）。
	canonicalRecoveryReuses atomic.Int64
	// stageSink 是阶段计时的测试观测口（生产为 nil，零开销）：让回归能断言「快路径确实生效」，
	// 而不必依赖耗时阈值或日志解析。
	stageSink        func(string)
	capacityProvider func() (acquire.CapacityBudget, error)
	// sampling 保存原始已校验策略，每个源创建独立采样器。
	sampling            sampling.Policy
	recoveryHold        func(SourceConfig, string) (bool, string)
	reconcileLoopCursor atomic.Int64 // 候选1：改为无锁游标（视图路径读写；对账单 goroutine 主导，丢失更新仅致幂等重做/跳过）
	// reconcileViewSnapshot 是"对账只读视图"（候选 1，2026-10-04）：keys+sources 同代快照，
	// 持锁写侧重建、atomic 发布；对账读侧零取 m.mu（发布点：源变更 force + 采集链/对账节流）。
	reconcileViewSnapshot atomic.Value // *reconcileView
	// 候选 2（2026-10-04）：m.mu 慢持锁观测 —— 计数 + 节流告警（先测量、后收窄）。
	mutexHoldSlowCount atomic.Int64
	// #55-A（2026-10-05）：把「等锁」从「持有」里拆出来单独计 —— 起表点原先在 Lock **之前**
	// ⇒ 读数混入等待（现场 9–19s），无法区分"自身持有"与"被挡住" ✗。两套计数器 + 各自节流窗口
	//（慢等待与慢持有各自一行告警），阈值同为 mutexHoldSlowThreshold。
	mutexWaitSlowCount atomic.Int64
	mutexWaitWarnAt    atomic.Int64 // UnixNano；慢等待告警节流窗口（独立于慢持有）
	// pollDispatchHook 是"采集分发前"的**仅测试用**观测点（D 修复红/绿）：断言进入分发段时 cycleMu 已释放。
	pollDispatchHook atomic.Value // func()
	// deliverWriteHook 是"投递成功"后调用的**仅测试用**观测点，参数是本次真正写进 VL 的事件。
	//
	// 2026-11-03：事件段（canonical 事件体副本）删除后，用例再无从「从段里读回投递了哪些事件」。
	// 这个钩子让它们仍能断言投递面到底写了什么；生产 nil ⇒ 零开销。
	deliverWriteHook atomic.Value // func(SourceConfig, []logtypes.Event)
	// deliveredBySource / deliveredMu 是**仅测试用**的投递记录（见 durableEvents）：
	// 事件段删除后，用例靠它读回「投递面写了什么」。生产不挂钩子 ⇒ 永不写入。
	deliveredBySource map[string][]logtypes.Event
	deliveredMu       sync.Mutex
	// deliverPlanHook 是"锁外投递规划（本批规范化 + 去重）"前调用的**仅测试用**观测点
	// （#54 修复红/绿）：断言该处未持 m.mu。
	deliverPlanHook atomic.Value // func()
	// resolveGapsPlanHook 是「解算的锁外计算结束、重新取锁复验之前」调用的**仅测试用**观测点（#58）：
	// 供用例在锁外窗口里注入一次账本写，断言第三段复验必须丢弃（不写入任何变更）。生产 nil ⇒ 零开销。
	resolveGapsPlanHook func()
	// clearConflictsAfterPlanHook 是「清空身份冲突」第一段（取计划快照）与第三段（复验提交）
	// 之间调用的**仅测试用**观测点。供用例确定性地注入"审阅后新增了一条冲突记录"，
	// 断言第三段复验必须跳过该源（未审阅的记录不得被顺手清掉）。生产 nil ⇒ 零开销。
	clearConflictsAfterPlanHook func()
	// rebuildSourceBuildHook 是"锁外构建源重建体"前调用的**仅测试用**观测点（#55-B 红/绿）：
	// 断言该处未持 m.mu；同时供"锁外期间源又变了 ⇒ 第三段必须丢弃重建"的确定性用例注入变更。
	rebuildSourceBuildHook atomic.Value // func()
	mutexHoldWarnAt        atomic.Int64 // UnixNano；告警节流窗口

	// gapResolves 是缺口消解的异步调度器（见 gap_resolve_async.go）。
	//
	// 为何必须异步：`resolveGapsLandedByDelivery` 内部要调 `landedVisibleInVLCtx` 核对
	// VL 可见性，而它的超时是 `verifyBackoffMax*4+1s = 3s`（为退避探测设计）。
	// 12KB selector 上限把大批切成 180 条/片后，退避预算被三次串行往返吃完
	// ⇒ 必然超时 ⇒ 缺口不消解 ⇒ WAL 不回收 ⇒ 积压丢最旧 ⇒ 更多缺口（正反馈）。
	// 长稳第 1 轮实测：1009 次超时 / 599 次丢弃 / RSS 4→693 MiB。
	gapResolves *gapResolveScheduler
	// gapWorkerOnce 保证异步 worker 只启动一次（见 ensureGapResolveWorker）。
	gapWorkerOnce sync.Once
	// ReconcileLoopCfgSnapshot 已随重建/对账链删除。
	// indexPrune 是历史投递批次裁剪的生效配置（已归一化，默认开启）：建每个源的账本时下发
	// （ledger.SetDeliveryBatchPrune）。裁剪发生在账本写路径内，因此配置必须落在账本上，
	// 而不是落库前再过滤一遍——后者只能让库变小，内存仍会随总量线性增长。
	indexPrune ledger.DeliveryBatchPruneConfig
	// indexCommit 是索引持久化「提交单元预算」的生效配置（已归一化）：单次持久化按
	// 行数 + 耗时双上界切成多个提交单元（FR-498 P0，见 stateindex.CommitBudget）。
	// 打开索引库时下发（stateindex.OpenWithBudget）。
	indexCommit stateindex.CommitBudget
	// cursorRegistry 是每个源「上次读到哪」的**最小持久化**（2026-10-30 裁定 D）。
	// 每源一行（path+inode+cursor），整表约 2KB；整表重写 + rename，**不 fsync**。
	// 详见 cursor_registry.go 文件头。
	cursorRegistry cursorRegistry
	// pendingResumeCursor 记下「本次登记是重启续读、起点在这里」，待管道入表后统一处置。
	//
	// 为何需要这层间接：`resumeCursorFor` 在 `pipeline.New` 的**参数**里被调用，
	// 而 `m.pipes[key] = p` 在那之后才赋值 ⇒ 那一刻拿不到账本，
	// 直接登记会静默失效。详见 persist_cursor_flush.go 的 recordPendingResumeSkipGaps。
	pendingResumeCursor map[string]resumeCursorNotice
	// cursorRegistryMu 保护 cursorRegistry 这张 map **本身**的并发访问。
	//
	// 为什么需要独立一把锁（而不是复用 m.mu）：写方有两条互不相同的线程——
	// 采集轮尾部 flushCursorRegistry 跑在 ticker goroutine 上，登记路径 resumeCursorFor
	// 跑在 gRPC 登记/实例发现的 goroutine 上；两者**确实会并发**（登记可以在采集轮之外的
	// 任何时刻发生），而 map 的并发读写是 Go 里唯一会直接 fatal 的一类竞态
	// （`concurrent map read and map write`，无法被 recover）。
	// 也不复用 m.mu：持 m.mu 去做整表落盘（open+write+rename）会把采集主线拖进磁盘 IO。
	// 锁序：cursorRegistryMu 在 m.mu **之外**获取（flushCursorRegistry 先取 m.mu 拿管道快照、
	// 放 m.mu 之后才取本锁），从不在持本锁时回头取 m.mu。
	cursorRegistryMu sync.Mutex
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
	// sourceErrs 记录每源最近一次已上报的采集错误，避免同一错误每 250ms 刷屏。
	sourceErrs map[string]string
	// gapAutoResolveAt 记录每源最近一次自愈尝试的时刻（缺陷 A）。
	// 自愈要投递存量并按天聚合事件区间，故按源限频（见 gapAutoResolveInterval）。
	gapAutoResolveAt map[string]time.Time
	// reReadGapProbeAt 记录每源最近一次"待重读缝直投"探测时刻（2026-10-06 快验回归修复）。
	// 该步含 Ledger.Get（深克隆）＋ WAL 全量 Snapshot ⇒ 只允许**有投递欠账**的源在冷却期外
	// 至多探一次（详见 directDeliverResolvedReReadGap 的门闸说明 ✓）。
	reReadGapProbeAt map[string]time.Time
	// selfHealInterval 是自愈的最小重试间隔；0 表示用默认常量。
	selfHealInterval time.Duration
	// verifiedCoverageEpoch 记录每源「逐字段校验覆盖」的**进展代次**（单调递增，由
	// recordVerifiedRunsReporting 在**真的新增了覆盖**时自增）。自愈面用它判断"本轮有没有推进"：
	// 有 ⇒ 不退避（否则会扼杀改动 A 的渐进收敛 ✗），无 ⇒ 退避（避免 10s 周期空转 ✗）。
	// 有界性：只在确有新覆盖时自增；键集合与 state.Sources 同规模（源数），不随失败次数增长 ✓。
	verifiedCoverageEpoch map[string]uint64
	// selfHealBackoff 记录每源**无进展失败**的退避状态（见 selfHealBackoffFor / noteSelfHealFailure）。
	// 与 verifiedCoverageEpoch 同锁（m.mu）：两者必须一起读，否则"有进展"与"退避代次"会撕裂。
	selfHealBackoff map[string]*selfHealBackoffState
	// generationProbeCache 是「该源该代次已确认未占用」的进程内缓存（复审 P2-11，
	// 见 projectionGenerationKnownFree / recordProjectionGenerationAttempt）。不持久化：
	// 重启后重新实探，正是保守方向。
	generationProbeCache map[string]map[string]bool
	// generationReserved 是「该源该代次名已被本进程**预留**（即将写入）」的表。
	//
	// 为什么必须有它（2026-10-10 生产实测，缺陷修复）：代次名的分配**不在任何锁内**
	//（nextFreeProjectionGeneration 在 runtime.go 的 deliver 里于取 publishMu **之前**调用），
	// 而它的判据是「实探 VL 未占用」（含 5s 级网络往返）＋ 缓存该名可用。两条同源投递各自
	// 实探「未占用」⇒ 双双算出**同一个名字** ⇒ 各写一份完整副本，VL 侧同一 (源, 代次) 出现
	// 2–3 倍重复行（生产 inst:172 实测 60000 唯一 / 180000 行 = 3×）。
	//
	// 语义：与 generationProbeCache 共用 m.mu（同一临界区，无新增锁 ⇒ 无新增锁序边）；
	// 取名与写入尝试都持 m.mu 读写它，故「探测 → 预留 → 释放」对同源是一次原子选择。
	// 释放时机：写入尝试**结束**（成功或失败）时删除——失败也必须释放（否则该名被永久
	// 钉死，重试只能一直进位，代次号无界增长）。残留只会让重试换一个名字，方向保守 ✓。
	generationReserved map[string]map[string]uint64
	// generationReservationSeq 分配预留所有权令牌（见 generationReservation.token）。
	// 原子量：只在取名与释放路径上自增，不需要与 m.mu 同临界区。
	generationReservationSeq atomic.Uint64

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
	// 按真实源与 UTC 日记录发布版本，其他源的发布不使本源快照失效。
	publicationEpochs map[string]map[string]uint64
	// 在途计数与尝试号由 m.mu 保护，遗留 pending 与在途写入分开管理。
	publicationAttempts map[string]uint64
	publicationInflight map[string]int
	// persistGate 让「构建 + 落库」同一时刻只有一次在跑（串行），并让短变更调用者只等
	// **一个周期**而不是排到队尾（见 persist 的合并说明）。
	persistGate persistGate
	// persistJoin / persistCovered 是合并算法的两个水位：调用方按到达顺序声明序号；
	// 每次落库声明它覆盖到的序号。声明号 ≤ 覆盖水位的调用方直接返回（它的变更已被落库）。
	persistJoin    atomic.Int64
	persistCovered atomic.Int64
	// 快照序在实际根行与派生行定格的临界区分配，与请求声明号分开。
	persistSnapshotSerial uint64
	persistAccepted       atomic.Uint64
	// 兼容直调单步入口，所有提交共用接纳与写入的串行边界，规划不持此锁。
	persistCommitMu sync.Mutex
	// persistPlanMu/persistPlan/persistPlanAt 是**热路径规划复用**缓存（2026-10-03 回退修复）：
	// 窗口内的多个调用者复用同一份门外计划，避免 N 倍冗余规划（每份都要持 m.mu 遍历全源 +
	// 重建镜像，60 源并发下实测吞吐 3.3k→1.5k 行/s）。Builds/Reuses 是"每轮只规划一次"的
	// 可观测指标（复验用：builds/轮 ≈1）。
	persistPlanMu     sync.Mutex
	persistPlan       *persistPlan
	persistPlanAt     time.Time
	persistPlanBuilds atomic.Int64
	persistPlanReuses atomic.Int64
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
	Root string
	// DeferredDelivery是内部组合选择；默认同步兼容，Worker生产组合显式启用source-key outbox。
	DeferredDelivery bool
	// Sampling 为零时保持原文恒等，启用时按源独立采样。
	Sampling sampling.Policy
	VL       *vlsup.Client
	Catalog  *catalog.Catalog
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
	// VLReadRoute 按当前已发布权威选择证据查询客户端。
	VLReadRoute func(SourceConfig, *catalog.Record) (*vlsup.Client, error)
	// 已获许可的写入使用绑定权威路由，不被后到的 admission 冻结改写。
	VLWritePermitRoute func(SourceConfig, *catalog.WritePermit) (*vlsup.Client, error)
	ResourceBudget     *vlsup.BudgetController
	ReadLimits         *acquire.ReadLimits
	WALReserve         func(int64) (func(), error)
	// nil 使用每窗口4份、同源同日每小时8份及1GiB的独立物化预算。
	MaterializationLimits *stateindex.MaterializationLimits
	// IndexPrune 是采集索引「历史投递批次（delivery_batch）裁剪」的配置面（FR-496 索引有界化，
	// spec §6）；nil 表示用默认（ledger.DefaultDeliveryBatchPruneConfig：**开启**、严格按
	// reclaim 水位）。判据与证明见 ledger/delivery_batch_prune.go。
	IndexPrune *ledger.DeliveryBatchPruneConfig
	// IndexCommit 是索引持久化「提交单元预算」的配置面（FR-498 P0，键 log_index.persist.*，
	// spec §3.4）；nil 表示用默认（stateindex.DefaultCommitBudget：每提交单元 128 行、目标 40ms、
	// 下限 32 行——真源是 stateindex 的 DefaultCommitMaxRows/DefaultCommitMinRows/DefaultCommitTarget。
	// FR-498 性能根治把 512/64 降为 128/32：单单元 p95 随行数近似线性，依据见 DefaultCommitMaxRows 注释）。
	// 单次持久化按行数 + 耗时双上界切成多个提交单元，见 stateindex.CommitBudget。
	IndexCommit *stateindex.CommitBudget
	// ReconcileYield / ReconcileSliceEvents 是**对账 CPU 让路**的配置面（键 log_reconcile.yield /
	// slice_events）：每 slice 条事件 `Sleep(yield)` 交还 P，避免对账与采集抢核 ✗（0/0 ⇒ 关闭）。
	ReconcileYield       time.Duration
	ReconcileSliceEvents int
	// ReconcileYieldNotify 是让路次数的观测口（测试/排障用，生产 nil）。
	ReconcileYieldNotify func()
	// ScanYield / ScanSliceRows 是**段读聚合路径的让路**配置（键 log_index.scan_yield /
	// scan_slice_rows；0 ⇒ 默认 1ms / 8192 行）。
	ScanYield       time.Duration
	ScanSliceRows   int
	ScanYieldNotify func()
	// Scan 是上面三个旋钮的合并形态（配置层一次下发；零值字段各自回退默认 ✓）。
	Scan *ScanTuning
	// ScanBudget 是单源单轮段读聚合的时间预算（0 ⇒ 默认 2s）。
	ScanBudget time.Duration
	// RecoveryQuotaFactor / RecoveryDrainGrace 是**恢复期独立配额**（0 ⇒ 默认 4 / 30m）。
	RecoveryQuotaFactor float64
	RecoveryDrainGrace  time.Duration
	// RecoveryProbe 是"恢复是否进行中"的注入点（生产 nil ⇒ 用 StartupRecoveryStatus()）。
	RecoveryProbe func() bool
	// SweepBackpressureStreak 是窗口化背压阈值：**连续 N 轮**闸视图积压净涨才跳过本轮
	// （0 ⇒ 默认 3 ✓；单轮抖动不再永久挡复活 ✗）。
	SweepBackpressureStreak int
	// SweepInflightCap 是在飞被放弃调用的并发上限（0 ⇒ 默认 4 ✓）。
	SweepInflightCap int
	// ReclaimRoll 已于 2026-10-29 删除（旧字段是 *ReclaimRollTuning，服务「分段链逐跳推进」；
	// 一次 `TryReclaim` 即可到顶后两个旋钮失去作用对象）。
	// ResolvePersistBudget 是解算收尾落库在持久化门上的有界等待预算（#59；0 ⇒ 默认 2s）。
	// 超预算即交办后台并返回（受理即返，最终一致），详见 persistResolveTail 的说明。
	ResolvePersistBudget time.Duration
	// CycleMuProfile 打开 cycleMu 持有者观测仪（#57；默认关，也可用 env JIANMANAGER_CYCLE_MU_PROFILE=1）。
	// 打开后：慢持有 Warn + 当前持有者快照 + 取锁密度 + 有界等锁读数；关闭时零开销。
	CycleMuProfile bool
	// ResumeBatchPerRound 是兜底环每轮最多处理的可解源数（0 ⇒ 默认 8）。
	ResumeBatchPerRound int
	// ResumeInterval 是**容量自愈兜底**周期（0 ⇒ 默认 30s）：低频重试「排空存量 → 推进回收 →
	// 按滞回解除积压/容量类暂停」，不依赖采集轮产生新批次 ✗。
	ResumeInterval time.Duration

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
	// InsertBatch 是「双阈值攒批 + 并发投递」的配置面（键 log_ingest.insert_batch.*）；nil ⇒ 全部用默认
	// （4 MiB 字节 / 500 条 / 1s 时间 / 4 并发 / 3 次尝试）。见 insert_batch.go 的文件头。
	InsertBatch *InsertBatchTuning
	// ResolveGapsBudget 是整节点解算（ResolveCoveredGaps）单次调用的预算（源数 + 墙钟），
	// 键 log_ingest.resolve_gaps_max_sources / log_ingest.resolve_gaps_max_duration；
	// nil 表示用默认（defaultResolveGapsMaxSources / defaultResolveGapsMaxDuration）。
	// 见 ResolveGapsBudget 的缺陷说明。
	ResolveGapsBudget *ResolveGapsBudget
	// StartupRecoveryBackground 让启动恢复（对账 + 重发 + 解阻回收 + 落库）在**后台**执行：
	// New 立刻返回，worker 随即可达 serving（反向隧道/WS/HTTP 不再等它），恢复进度经就绪面
	// （CutoverReadiness 的 startup_recovery_in_progress / _failed）如实上报。
	//
	// 为什么是开关而不是无条件（2026-10-02 生产事故的工程取舍）：启动恢复是「启动后状态何时
	// 落定」的语义边界，既有大量调用方/用例以「New 返回即恢复完成」为前置。开关让生产拿到
	// 「必定可达 serving」，同时把其余调用方的边界保持**显式**。两条路径跑的是**同一个驱动**
	// （runStartupRecovery），差别只在「在哪个 goroutine 里跑」——不存在「测试路径与生产路径
	// 是两份实现」的问题。
	StartupRecoveryBackground bool
	// VerifyBudget 是校验/对账查询的全局天花板（键 log_index.verify.max_per_second /
	// max_in_flight）；零值用默认（30/s、6 在飞）。天花板语义：正常路径零等待 ✓。
	VerifyBudget VerifyBudget
	// VerifyMaxQueriesPerChunk 是单簇单次校验的尝试次数上限（键
	// log_index.verify.max_queries_per_chunk）；≤0 用默认（12）。
	VerifyMaxQueriesPerChunk int
	// VerifyRoundBudget 是每源每轮的校验时间预算（键 log_index.verify.round_budget）；
	// ≤0 用默认（1.5s）。见 Manager.verifyRoundBudget 的说明。
	VerifyRoundBudget time.Duration
	// VerifyVisibilityFloor 是"可见性错开"等待（键 log_index.verify.visibility_floor）：
	// >0 时整批首探前先等到"距批写入 ≥ floor"（对齐 VL 写入→可查询的硬地板 ⇒ 首探即命中，
	// 不再做 0/0.2/0.6s 三次无效探测）；≤0 = 关（默认关，行为零变化）。
	VerifyVisibilityFloor time.Duration
	// VerifyChunkEstimates 是「单源校验预算随事件数放大」的档位（键
	// log_index.verify.chunk_estimate / chunk_budget_max）。
	//
	// 零值 = **完全关闭放大**（默认）：预算与修复前逐字一致（等价于原来的 round_budget），
	// 因此不影响任何现有源的现行行为。只有在运维显式给出单簇耗时估计时才放大。
	// 见 Manager.verifyBudgetOfEvents 的推导与上限说明。
	VerifyChunkEstimates VerifyChunkEstimates
	// PersistHotBudget 是热路径（采集/投递）在持久化门上的等待上界（键
	// log_index.persist.hot_budget）；≤0 用默认（30ms）。超时即交给后台持久化通道，采集不受阻。
	PersistHotBudget time.Duration
	// PersistYield 是持久化「让路窗口」（键 log_index.persist.cycle_yield）；≤0 用默认。
	// PersistPriorityStreak 是老化阈值（键 log_index.persist.priority_streak）；≤0 用默认 8。
	PersistPriorityStreak int
	// PersistYield 是持久化「让路窗口」（键 log_index.persist.cycle_yield）；≤0 用默认。
	PersistYield time.Duration
	// EventStoreManifestSyncEvery / EventStoreManifestSyncInterval 是事件段清单的落盘上界
	// （键 log_index.persist.manifest_sync_every / manifest_sync_interval）。
	//
	// 两者都为 0（默认）＝只在**段集合变化**（新建段、封段）时落清单：未封末段的
	// count/bytes/首末身份都是可从段内容逐字段重算的派生值 ⇒ 崩溃不丢数据，重启由校准精确补齐。
	// 机械盘上这把一次追加的耐久调用从 3 次降到 1 次（segment fsync 不可省）。
	// 见 eventstore.SetManifestSyncPolicy 的语义边界说明。
	EventStoreManifestSyncEvery    int
	EventStoreManifestSyncInterval time.Duration
	// VLReadyProbeTimeout 是对账/重发之前探测 VL 写入端就绪的超时（键
	// log_reconcile.vl_ready_timeout）；≤0 用默认（defaultVLReadyProbeTimeout）。
	//
	// 为什么必须有这道门（2026-10-02/03 现场）：worker 与 VL 同步重启时，启动对账会**抢跑**在
	// VL ready 之前 ⇒ 计数查询必然 `count_query_failed` ⇒ 保守回退整窗重发 ⇒ 几十万事件的插入
	// 打在未就绪的 VL 上，长时间失败重试（现场 18+ 分钟零成功写入、CPU 空烧）。门禁把「必然失败
	// 的竞态」变成「等下一轮」（与常驻对账「不可信查询不重发」的既有取舍一致）。
	VLReadyProbeTimeout time.Duration
	// VLReadyWait 是启动恢复**等待** VL 就绪的上界（键 log_reconcile.vl_ready_wait）；≤0 用默认。
	// 超出即记失败原因并交给运行期（常驻对账会重新发现缺失天并重发），不再占着启动恢复。
	VLReadyWait time.Duration
	// ReplayBudget 是**整窗重发**的切片预算（键 log_reconcile.replay_max_days /
	// replay_max_duration）；零值用默认（defaultReplayMaxDaysPerRound / defaultReplayMaxDuration）。
	//
	// 为什么要切片：整窗重发此前对**单个大源**就是「一次写完整个权威集合」（现场 330MB），
	// 既不切片也无预算 ⇒ 与采集轮抢共享持久化门、并把 VL 打满（其余源零成功写入）。
	ReplayBudget ReplayBudget
	// ReplayTuning 是上面三枚旋钮的合并形态（配置层一次下发；零值字段各自回退默认）。
	// 非零字段优先于上面的单独字段。
	ReplayTuning *ReplayTuning
	// RebuildTuning 是投影重建的限速与重试上限（零值 ⇒ 默认；见 rebuild_scheduler.go）。
	RebuildTuning *RebuildTuning
	// HotRetention 是**热层**（HOT namespace）VictoriaLogs 的 retention 时长，用于判定
	// 「热层可发布窗口」：早于 `now - HotRetention` 的 UTC 天在热层里不可能可见
	// （VL 对超保留期数据点返回 HTTP 200 但静默丢弃，生产实测），因此既不重建、也不算对账
	// 缺失天。判据与理由见 hot_retention_window.go。
	//
	// 零值语义是「未知 / 不启用过滤」⇒ **保持改动前的行为**（没配置就误滤掉正常数据，
	// 比不修更糟）。解析留在 main.go 侧完成（ingest 不反向依赖 config 包）。
	HotRetention time.Duration
	// RebuildYieldNotify 是重建批间让出的观测口（测试/排障用，生产 nil ⇒ 零开销）。
	RebuildYieldNotify func()
	// RebuildRunNotify 是「重建任务开始执行」的观测口（测试/排障用，生产 nil ⇒ 零开销）。
	// 注意它跑在**重建 worker goroutine** 上，不阻塞采集轮。
	RebuildRunNotify func(key string)
}

// ReplayTuning 把「重发的外部条件与切片」三枚旋钮收在一起，供配置层一次下发
// （键 log_reconcile.vl_ready_timeout / vl_ready_wait / replay_max_days / replay_max_duration）。
type ReplayTuning struct {
	VLReadyProbeTimeout time.Duration
	VLReadyWait         time.Duration
	Budget              ReplayBudget
	// ReconcileYield / ReconcileSliceEvents：**对账 CPU 让路**（键 log_reconcile.yield /
	// slice_events）。0 ⇒ 用 ingest 默认（5ms / 4096 条）。
	ReconcileYield       time.Duration
	ReconcileSliceEvents int
}

// RebuildTuning 是**投影重建的限速与重试上限**旋钮（见 rebuild_scheduler.go）。
//
// 为什么不复用 ReplayBudget：ReplayBudget 切的是「天」，而生产循环的形态是**单天 57 万条**
// （`inst:220/file` 的 2026-10-05）——按天切片对它是零约束。这里的两枚旋钮作用在更细的粒度上：
// Throttle 管「批与批之间让出多久」，MaxAttempts 管「同一个源连续失败几次就停下」。
type RebuildTuning struct {
	// Throttle 是重建**批间让出**时长（0 ⇒ rebuildDefaultThrottleDelay）。
	Throttle time.Duration
	// MaxAttempts 是同源连续重建失败上限（0 ⇒ rebuildDefaultMaxAttempts）；达上限即停止
	// 自动重试并打 ERROR（数据仍视为未发布，缺口保持可见）。
	MaxAttempts int
}

// ReplayBudget 是整窗重发的切片预算：一轮最多重发几天 / 最多跑多久。
//
// 未完成时**不发布剩余天、不推进回收责任、不推进水位**（见复用的既有发布语义）；下一轮从剩余
// 天续跑（同一天的重发按 event_id 身份 + 逐字段校验幂等，重复只生成新代次并取代旧代次）。
type ReplayBudget struct {
	MaxDays     int
	MaxDuration time.Duration
}

// VerifyBudget 是**投影校验/对账查询**的全局上限（**天花板，不是节拍器**）。
//
// 为什么需要（2026-10-03 现场：近 5 分钟 `投影校验查询 ×410` 占满算力、未暂停源 readΔ=0）：
// 校验按簇退避重试到窗口耗尽，而并发上界只有「单批内 4」✗——多源同时校验时没有任何**全局**
// 速率/在飞上限，于是少量失败簇的重试就能把节点算力与 VL 容量吃光 ✗。
//
// 天花板语义（务必与"限速节拍"区分）：
//   - 令牌桶容量 = MaxPerSecond ⇒ 正常路径（几簇、1–2 次查询）**立即通过、零等待** ✗；
//   - 只有超过上限的**风暴**才被削峰 ✓；
//   - 采集侧的读/写**不经过本闸** ⇒ 算力与 VL 容量天然优先给采集 ✓。
type VerifyBudget struct {
	// MaxPerSecond 是校验/对账查询的每秒上限；≤0 用默认（30）。
	MaxPerSecond int
	// MaxInFlight 是在飞的校验/对账查询数上限；≤0 用默认（6）。
	MaxInFlight int
}

// VerifyChunkEstimates 是「单源校验预算随事件数放大」的档位（键
// `log_index.verify.chunk_estimate` / `chunk_budget_max`）。
//
// 为什么需要它（2026-10-10 生产活锁修复，改动 B）：单源预算原先是一个与事件数无关的常数，
// 而大积压会被切成数百簇（现场 `inst:220/file`：544205 事件 / 2000 ⇒ **272 簇**，
// 生产实测单簇查询 17–23 秒）。常数预算下永远只跑得完其中极小一部分 ⇒ 整批拿不到
// 「全部可见」结论 ⇒ 不 publish ⇒ 下一轮重来（活锁）。按簇数放大预算让大积压有机会一轮跑完。
//
// **保守默认**：PerChunk ≤ 0 ⇒ 完全关闭放大，预算与修复前逐字一致（不影响任何现有源）。
// 只有运维显式给出单簇耗时估计时才按 `簇数 × PerChunk × 安全系数` 放大，且**必定**被
// Max 封顶（超过上限就依赖改动 A 的逐轮渐进收敛，绝不无限占用校验额度）。
type VerifyChunkEstimates struct {
	// PerChunk 是单簇校验查询的**保守**耗时估计（正数才启用放大）。
	//
	// 现场实测 17–23 秒是 VL 很慢时的读数，正常应远小于此；故这里没有内置默认值——
	// 让运维按自己的 VL 实况填一个「偏大」的数（宁可多给预算，也不要给不够）。
	PerChunk time.Duration
	// Max 是本批校验预算的**上限**（>0 才封顶；≤0 用 defaultVerifyChunkBudgetMax）。
	// 上限的意义：超过它的批次只能靠改动 A 的逐轮渐进收敛，而不是让单源占住校验额度一小时。
	Max time.Duration
	// Safety 是簇数折算的安全系数（≤0 用 defaultVerifyChunkBudgetSafety）。
	Safety float64
}

// VerifyChunkEstimatesOf 归一化档位（未启用放大时返回零值）。
func (m *Manager) verifyChunkEstimatesOf() VerifyChunkEstimates {
	if m == nil {
		return VerifyChunkEstimates{}
	}
	return m.verifyChunkEstimates
}

// verifyLimiter 是校验/对账查询的**全局**天花板（令牌桶 + 在飞信号量）。
type verifyLimiter struct {
	mu                                       sync.Mutex
	cond                                     *sync.Cond
	rate                                     int
	capacity                                 int
	tokens                                   float64
	last                                     time.Time
	inFlight                                 int
	maxInFlight                              int
	waiting                                  int
	acquired, completed, rejected, waitCount uint64
	waitNanoseconds                          int64
}

// newVerifyLimiter 按预算构造（零值 → 默认）。上限 ≤0 时按各自默认补齐。
func newVerifyLimiter(budget VerifyBudget) *verifyLimiter {
	rate := budget.MaxPerSecond
	if rate <= 0 {
		rate = defaultVerifyMaxPerSecond
	}
	inFlight := budget.MaxInFlight
	if inFlight <= 0 {
		inFlight = defaultVerifyMaxInFlight
	}
	l := &verifyLimiter{rate: rate, capacity: rate, tokens: float64(rate), maxInFlight: inFlight, last: time.Now()}
	l.cond = sync.NewCond(&l.mu)
	return l
}

// Acquire 取得一次查询许可（速率 + 在飞双闸）；ctx 取消即返回 error。
func (l *verifyLimiter) Acquire(ctx context.Context) error {
	if l == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	started := time.Now()
	l.mu.Lock()
	waiting := false
	defer func() {
		if waiting {
			l.waiting--
			l.waitNanoseconds += time.Since(started).Nanoseconds()
		}
		l.mu.Unlock()
	}()
	for {
		if err := ctx.Err(); err != nil {
			l.rejected++
			return err
		}
		now := time.Now()
		l.tokens += now.Sub(l.last).Seconds() * float64(l.rate)
		l.last = now
		if l.tokens > float64(l.capacity) {
			l.tokens = float64(l.capacity)
		}
		if l.tokens >= 1 && l.inFlight < l.maxInFlight {
			l.tokens--
			l.inFlight++
			l.acquired++
			return nil
		}
		if !waiting {
			waiting = true
			l.waiting++
			l.waitCount++
		}
		// 天花板语义：等待由速率/在飞决定（1/rate 量级），不是固定节拍。
		l.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Millisecond):
		}
		l.mu.Lock()
	}
}

// Release 归还一次查询许可。
func (l *verifyLimiter) Release() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.inFlight > 0 {
		l.inFlight--
		l.completed++
	}
	l.cond.Broadcast()
	l.mu.Unlock()
}

const (
	// defaultReplayMaxDaysPerRound 默认一轮重发 1 个 UTC 天：单个天已是「一次插入循环」的有界单位，
	// 且能让采集轮在其间完整跑若干轮（现场 330MB 源按天切开后每段可独立完成与发布）。
	defaultReplayMaxDaysPerRound = 1
	// defaultReplayMaxDuration 是一轮重发的墙钟预算（检查点在「天与天之间」）。
	defaultReplayMaxDuration = 90 * time.Second
	// defaultVLReadyProbeTimeout 是 VL ready 探针的超时。
	defaultVLReadyProbeTimeout = 2 * time.Second
	// defaultVLReadyWait 是启动恢复等待 VL ready 的上界（超过即记错误、留给下一轮）。
	defaultVLReadyWait = 90 * time.Second
)

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
	// 启动恢复未完成 ⇒ 平台不可宣告就绪。
	//
	// 为什么必须显式报告（2026-10-02 事故的语义侧）：改动前「恢复完成」是由 New 同步返回隐式
	// 表达的——worker 能连上就说明恢复做完了。恢复移出 New 之后，这个隐式前提消失，必须换成
	// 显式判据，否则 CP 会在账本/投影/回收责任尚未落定时就宣告可切换。
	m.mu.Lock()
	recovery := m.startupRecovery
	reasons := append([]string(nil), recovery.Reasons...)
	m.mu.Unlock()
	if recovery.InProgress {
		result.LedgerReady = false
		result.Reasons = append(result.Reasons, "startup_recovery_in_progress")
	} else if recovery.Failed > 0 {
		result.LedgerReady = false
		result.Reasons = append(result.Reasons, "startup_recovery_failed")
		result.Reasons = append(result.Reasons, reasons...)
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
	for key, pipe := range m.pipes {
		pipes[key] = pipe
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
		m.mu.Lock()
		saved := m.state.Sources[key]
		m.mu.Unlock()
		if saved.PublicationPending {
			result.LedgerReady = false
			result.Reasons = append(result.Reasons, key+":publication_pending")
		}
		// 零数据源（从未采集到任何事件，Durable==0）没有可发布的投影——「空」是合法状态，
		// 不得因此否决全局切换（与 CP 侧 2026-09-30「空≠缺」语义修正同型）。
		// 生产实证：inst:152/file（bungee 的空文件日志）与 inst:153/stdout（Beacon 空 stdout）
		// 在此永久产出 projection_not_published，使 cutover 闸门恒不可满足。
		//
		// **2026-11-02 投递路径简化：本条 `projection_not_published` 判据已删除。**
		//
		// 它原先问的是「该源的已发布投影是否覆盖到 Durable」。而投影发布（`publishSnapshot`）
		// 已从投递路径上摘除 ⇒ `rec.PublishedProjection` 永远不会被写出来 ⇒ 该判据对
		// **任何有数据的源**都恒为假 ⇒ cutover 闸门在生产上恒不可满足，而且没有任何数据面
		// 动作能让它变绿（不是“偶尔不就绪”，是永久不就绪）。
		//
		// 它守护的实质关切（「字节只写进了本地，没真正到 VL」）**没有丢**，只是位置前移了：
		// 现在由上方两条水位判据承担——
		//   - `durable_behind_read`：读到的东西必须先落库；
		//   - `reclaim_behind_durable`：回收只能在「真实 ACK 且逐字段认证的连续前缀」上推进
		//     （`contiguousCertifiedEnd`），因此 `Reclaim >= Durable` 就意味着这段字节
		//     已经拿到 VL 的请求完成回执并被登记为已投递。
		// 换言之，旧判据只是想从另一个角度再问一遍同一件事；把它留着只会把闸门焊死。
	}
	return result
}

func (m *Manager) PrepareCutoverReadiness() CutoverReadiness {
	if m == nil {
		return CutoverReadiness{Reasons: []string{"ingest_manager_unavailable"}}
	}
	w := m.observeCycleMu("cutover")
	m.cycleMu.Lock()
	w.locked()
	defer func() {
		m.cycleMu.Unlock()
		w.done()
	}()
	m.mu.Lock()
	pipes := make([]*pipeline.Pipeline, 0, len(m.pipes))
	for _, pipe := range m.pipes {
		pipes = append(pipes, pipe)
	}
	m.mu.Unlock()
	var flushReasons []string
	for _, pipe := range pipes {
		pipe.RefreshDegrade(time.Now())
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
