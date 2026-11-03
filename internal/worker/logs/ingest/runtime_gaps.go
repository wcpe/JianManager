// 本文件是**缺口裁定与恢复**面：ResolveCoveredGaps* 一族的准入、单源预算、
// 身份冲突的人工清空（ClearIdentityConflicts）与清空后的重发布。
//
// 与 runtime_recovery.go 的区别：那边是「采集暂停/复活的自动调度」，
// 这里是「缺口本身能否被判定已落库」以及「人工介入后的收口」。
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/eventstore"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/normalize"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
	"google.golang.org/grpc/metadata"
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

// resumeAcquireRespectingGates 恢复采集，但**不越权清除积压/容量闸的暂停**。
//
// 为什么（D4，2026-10-03 现场）：解算与放弃裁定的收尾此前无条件 `ResumeAcquire`，而积压闸带滞回
// （积压回落到上限一半以下才自动恢复）——于是「解一次缺口 / 做一次运维解阻」就把条目闸解除一次，
// 积压实测涨到 113.9 万条（名义上限 5000）。这里只放行「非积压闸造成」的暂停：积压闸的恢复
// 交给它自己的滞回路径（`WAL.pruneReclaimed` → `maybeResumeBacklogLocked`）。
func resumeAcquireRespectingGates(pipe *pipeline.Pipeline) error {
	if pipe == nil {
		return nil
	}
	if entry := pipe.Ledger().Get(pipe.Key()); entry != nil && entry.AcquirePaused &&
		acquire.IsBacklogPauseReason(entry.PauseReason) {
		return nil
	}
	return pipe.Ledger().ResumeAcquire(pipe.Key())
}

// unverifiedRawWriteFailure 是两阶段共用的**逐字**判据：未解决的 Raw 写失败缺口一律拒绝
// （projection alone cannot resolve it）——原始正文没有落盘、投影里也没有，任何「已发布投影」
// 都不能证明它存在过。措辞与改动前一致（既有回归按该措辞断言）。
func unverifiedRawWriteFailure(key string, entry *ledger.Entry) error {
	for _, gap := range entry.Gaps {
		if !gap.Resolved && gap.Reason == ledger.GapReasonStdioRawWriteFailed {
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
	// 阶段 A（离锁）：用**VL 实际可见性**取证，不再要求「已发布投影」。
	//
	// 2026-11-02 简化：投递路径不再发布投影（一批数据只写一次，event_id 幂等），
	// 所以 `publishedClosedForSource` 恒为不完整。若仍以它为前置，本方法对每个源
	// 都只会返回 "no complete published projection" 错误——那个自动解算入口就废了。
	//
	// 新证据面（比旧白名单更硬）：把该源正文里落在未解决缺口区间的事件拿去问 VL
	// 「你们在不在」。查得到的就构成落库证据，查不到的不算。
	//
	// 消解是 best-effort：失败不影响本源的其它步骤（它们是幂等的，下一轮照跑）。
	m.autoResolveGapsFromPublished(source, pipe)

	// 阶段 B：与采集轮串行的最小窗口。
	//
	// 锁序：cycleMu → mu（见 cycleMu 注释的完整锁序）。
	// 最坏持有量：**单源变更段**——账本判据复核 + ResolveGapsThrough + 静默源补段（内存状态机）
	// + ResumeAcquire，不含任何投影查询、网络往返或持久化（persist 在调用方、锁外执行）。
	// 量化口径与 FR-499 一致：直接测「被它挡住的那件事」的时长，不用 TryLock 探锁（饥饿模式下
	// 锁空闲也返回 false）。回归 TestResolveCoveredGapsLockHoldIsBoundedPerSource 实测：
	// 解算总时长 372ms 的同一时段内，采集轮最坏单轮仅 1.37ms ⇒ 争用上界为毫秒级，
	// 与解算总时长（源数 × 单源投影查询）彻底解耦。
	// #57：本处是"被 ResolveCoveredGaps 逐源调用"的实际持有者 ⇒ tag=resolveOne（现场 20/20 全忙的嫌疑面）。
	w := m.observeCycleMu("resolveOne")
	m.cycleMu.Lock()
	w.locked()
	defer func() {
		m.cycleMu.Unlock()
		w.done()
	}()
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
	// 静默源出口（2026-09-30 生产实证；2026-10-29 简化）：静默流（如某实例的 stderr，自 09-27
	// 起再无新行）永远不会有新批次 —— 旧实现靠「补一段无副本的恢复分段」把 reclaim 推出去。
	// reclaim 改为按投递水位推进后，这一步只剩**推进动作**本身。
	//
	// 安全性由账本门禁保证：`reclaim = min(delivery, max(read, durable))`，尾部全部确认投递时
	// （`delivery >= read`）它才会跨过 [reclaim, read) 这一段；任何未投递数据都不会被跳过。
	if d := entry.Positions.Delivery; d >= entry.Positions.Read && entry.Positions.Read > entry.Positions.Reclaim {
		// 推进回收并按当前水位剪枝；失败不置死——下一轮自动重试。
		_, _ = pipe.TryReclaim()
	}
	if err := resumeAcquireRespectingGates(pipe); err != nil {
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
// resolveGapsCycleMuWait 是"放弃裁定"等待采集轮锁（cycleMu）的墙钟预算（2026-10-04 C 修复）：
// 现场 resolve-gaps 33/53 调用在 cycleMu 上排队 45-60s 全 TIMEOUT（持有者 pollOnce 被 work
// 通道阻塞，见专项报告）。放弃裁定"等不到就明确失败"是安全的——它不写账本、不改状态，
// 调用方可重试；而"无限等"只会把调用方超时与服务端排队叠加成双倍失败面。
const resolveGapsCycleMuWait = 5 * time.Second

// acquireCycleMuBounded 在有界预算内获取 cycleMu（TryLock 轮询；ctx 取消立即放弃）。
// 返回 false 表示超预算/被取消 —— 调用方必须**未做任何变更**地返回错误。
func (m *Manager) acquireCycleMuBounded(ctx context.Context, budget time.Duration) bool {
	// #57 等锁侧观测（探针"每次都吃满 5s 预算"的直接对照）：start 同时作为 deadline 基准
	// ⇒ 与改前相比**不多一次** time.Now；观测关闭时 noteCycleMuWait 立即返回 ⇒ 零开销。
	start := time.Now()
	deadline := start.Add(budget)
	for {
		if m.cycleMu.TryLock() {
			m.noteCycleMuWait(time.Since(start), true)
			return true
		}
		if ctx != nil && ctx.Err() != nil {
			m.noteCycleMuWait(time.Since(start), false)
			return false
		}
		if !time.Now().Before(deadline) {
			m.noteCycleMuWait(time.Since(start), false)
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

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
	// C 修复（2026-10-04，resolve-gaps 挂死专项）：cycleMu 的**有界获取**。
	// 现场（pprof 铁证）：pollOnce 持 cycleMu 卡在 `work <- i`（2347）——worker 全堵在
	// m.mu 的 33.8 万条 WAL 序列化上 ⇒ 本路径**无限等 cycleMu** ⇒ 舰队 33/53 调用
	// 45-60s 全 TIMEOUT。放弃裁定"忙即快速失败"是安全的：它**未写入任何变更**，
	// 返回明确错误让调用方重试，远优于"调用方超时但服务端仍在排队"。
	// C 修复（2026-10-04，resolve-gaps 挂死专项）：cycleMu 的**有界获取**。
	// 现场（pprof 铁证）：pollOnce 持 cycleMu 卡在 `work <- i`（2347）——worker 全堵在
	// m.mu 的 33.8 万条 WAL 序列化上 ⇒ 本路径**无限等 cycleMu** ⇒ 舰队 33/53 调用
	// 45-60s 全 TIMEOUT。放弃裁定"忙即快速失败"是安全的：它**未写入任何变更**，
	// 返回明确错误让调用方重试，远优于"调用方超时但服务端仍在排队"。
	// #57：持有观测（tag=resolveGapsForSource）—— 获取侧走 acquireBounded（等锁读数在那边记）。
	// #58：三段式收窄（照 #55-B 范式）。修前整个函数体都在 cycleMu 内 —— 含**账本克隆 + 缺口扫描 +
	// 放弃写入 + 恢复采集 + 最重的 m.persist()**（索引规划/落库/fsync/WAL 剪枝）⇒ 现场实测
	// `cycleMu 慢持有 section=resolveGapsForSource hold=1m26.374992592s`（观测仪首条 Warn）。
	// 第一段（持锁）只取 O(1) 守卫快照；第二段（锁外）做重活；第三段复验守卫后做最小必要变更；
	// persist 移到临界区外 —— 与同族路径 ResolveCoveredGaps（末尾 m.persist() 本就在 cycleMu 外）同型。
	w := m.observeCycleMu("resolveGapsForSource")
	if !m.acquireCycleMuBounded(ctx, resolveGapsCycleMuWait) {
		return fmt.Errorf("ingest: 采集轮忙（cycleMu 等待超 %s 预算），放弃裁定未写入任何变更，请稍后重试", resolveGapsCycleMuWait)
	}
	w.locked()
	// 第一段（持 cycleMu，全 O(1)）：停止标记 / 管道句柄 / 账本修订号（守卫）。
	m.mu.Lock()
	stopped := m.stopped
	pipe := m.pipes[storageNamespace]
	m.mu.Unlock()
	if stopped {
		m.cycleMu.Unlock()
		w.done()
		return fmt.Errorf("ingest: manager 已停止，拒绝放弃裁定（停止后不得再写账本或重开索引句柄）")
	}
	// 临界区内再校验一次：等锁期间调用方可能已取消——此时不得留下「无人认领的放弃」。
	if err := ctx.Err(); err != nil {
		m.cycleMu.Unlock()
		w.done()
		return fmt.Errorf("ingest: 放弃裁定被取消（未写入任何变更）: %w", err)
	}
	if pipe == nil {
		m.cycleMu.Unlock()
		w.done()
		return fmt.Errorf("ingest: source %s not found", storageNamespace)
	}
	// 守卫：账本修订号。Ledger.touch 在**所有写路径的入口** require() 内推进 ⇒ 任何账本写都会变。
	rev := pipe.Ledger().Revision(pipe.Key())
	m.cycleMu.Unlock()
	w.done()

	// ── 第二段（**锁外**）：重活 —— 克隆账本条目 + 扫描未解决缺口求 through（O(缺口数)）。
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

	// 仅测试观测点（生产 nil，零开销）：锁外计算到此结束 ⇒ 用例可在此注入「源又变了」以验证复验。
	if m.resolveGapsPlanHook != nil {
		m.resolveGapsPlanHook()
	}
	// ── 第三段（复验守卫 + 最小必要变更）：重新取有界 cycleMu；守卫（停止标记 / 管道身份 /
	// 账本修订号）任一变化 ⇒ **不写入任何变更**地返回明确错误（调用方重试即可；与 C 修复同型）。
	// 为什么要复验：第二段在锁外算 through，期间采集轮/投递/回收都可能写该源账本 ⇒ 直接用旧
	// through 落「放弃」会放弃一段**未经判定**的缺口区间（审计凭据失真）⇒ 丢弃重算。
	wc := m.observeCycleMu("resolveGapsForSource.commit")
	if !m.acquireCycleMuBounded(ctx, resolveGapsCycleMuWait) {
		return fmt.Errorf("ingest: 采集轮忙（cycleMu 等待超 %s 预算），放弃裁定未写入任何变更，请稍后重试", resolveGapsCycleMuWait)
	}
	wc.locked()
	m.mu.Lock()
	stoppedNow := m.stopped
	pipeNow := m.pipes[storageNamespace]
	m.mu.Unlock()
	if stoppedNow || pipeNow != pipe || pipeNow.Ledger().Revision(pipeNow.Key()) != rev {
		m.cycleMu.Unlock()
		wc.done()
		return fmt.Errorf("ingest: 源 %s 在放弃裁定计算期间发生变化（未写入任何变更），请重试", storageNamespace)
	}
	if err := ctx.Err(); err != nil {
		m.cycleMu.Unlock()
		wc.done()
		return fmt.Errorf("ingest: 放弃裁定被取消（未写入任何变更）: %w", err)
	}
	reason := ""
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if values := md.Get("x-jm-reason"); len(values) > 0 {
			reason = strings.TrimSpace(values[0])
		}
	}
	if _, err := pipe.Ledger().AbandonGapsThrough(pipe.Key(), through, ledger.GapAbandonment{
		ReasonCode: ledger.GapReasonPermanentlyLost,
		Reason:     reason,
		Operator:   operator,
		Detail:     "manual abandonment via ResolveCoveredGapsForSource",
	}); err != nil {
		m.cycleMu.Unlock()
		wc.done()
		return err
	}
	if err := resumeAcquireRespectingGates(pipe); err != nil {
		m.cycleMu.Unlock()
		wc.done()
		return err
	}
	m.cycleMu.Unlock()
	wc.done()
	// #58：落库（索引规划/Apply/fsync/WAL 剪枝）在 cycleMu **之外**。
	// #59：且**有界**——现场（探针自栈两现）handler 正是在这里无界等持久化门达 45s（账本写已落账，
	// 卡的是索引落库排队）⇒ 超预算交后台（受理即返，最终一致；写不丢，见 persistResolveTail）。
	_, err := m.persistResolveTail()
	return err
}

// clearIdentityConflictsCycleMuWait 是「人工确认并清空身份冲突」等待采集轮锁（cycleMu）的墙钟预算。
//
// 取值与理由逐条同 resolveGapsCycleMuWait / archiveScanCycleMuWait（同类现场形态：调用方在
// 45-60s 后被自己的截止时间打断，而服务端仍在排队）：远小于任何 RPC/HTTP 截止时间，且大于
// 采集轮读快照段的期望持有时长。等不到即**未做任何变更**地明确失败，由运维重试。
const clearIdentityConflictsCycleMuWait = 5 * time.Second

// IdentityConflictClearResult 是清空操作的逐源结果（审计凭据的对外可读部分）。
//
// 注意这里有两件**不同**的事，结果必须分开表达（2026-10-21 复审缺陷 B7）：
//   - 「冲突记录已清空」= ClearedCount / ClearedSources，本操作的契约，失败会返回 error；
//   - 「派生计数（conflict_count）已对齐」= 尽力而为，失败**不**让整个操作失败，
//     但必须落在 DerivedRefreshFailures 里对调用方可观测。
//
// 早前后者失败只打一行 Warn 就从结果里消失了，于是 API 返回 clearedCount=3、无 error，
// 运维以为已生效，而历史分区的 conflict_count 仍是旧值、查询/导出继续被拦。
type IdentityConflictClearResult struct {
	// ClearedCount 本次真正清空的冲突记录条数（跨源合计）。
	ClearedCount int
	// ClearedSources 实际发生清空的源键（升序）。
	ClearedSources []string
	// SkippedSources 计划内但**未**清空的源键（升序）：或期间被并发写新增了冲突记录
	// （见三段式复验），或本就已无冲突记录（幂等目标态）。
	SkippedSources []string
	// DerivedRefreshFailures 派生计数**没能刷新**的分区/源清单（按发现顺序，有界）。
	//
	// 语义：冲突记录本身已清空（那是本操作的契约），但这些位置的已发布投影里
	// conflict_count 仍是旧值 ⇒ 查询覆盖到这些日期时仍会报 duplicate_quality=conflict、
	// 导出仍被拦。**历史分区尤其危险**：它们不会再有任何投递触发 publishSnapshot，
	// 所以「待下次投递刷新」对它们是永久性的，没有自发重试路径。
	//
	// 为什么放进结果而不是只打日志：这是运维判断「清空是否真的生效」的唯一凭据。
	DerivedRefreshFailures []string `json:"derived_refresh_failures,omitempty"`
}

// clearConflictsPlan 是「清空身份冲突」第一段（持锁）取到的**逐源计划快照**。
type clearConflictsPlan struct {
	key string
	// records 是申请时读到的冲突记录**逐字段快照**。第三段据此复验：只有当库里仍是这一份
	// 完全相同的集合时才允许清空——期间若有新冲突写入，那批记录**未经人工审阅**，
	// 必须跳过而不是顺手一起清掉（否则"确认后清空"就退化成"无条件清空"）。
	records []identityConflictRecord
}

// ClearIdentityConflicts 人工确认并清空「身份冲突」记录，使查询面的质量标记不再为
// conflict/partial（见 planner 的 DupConflict/StatsPartial 派生）。
//
// 语义（必须与实现一致）：运维**审阅后确认**这些冲突已被正确处理。判据是系统在不可裁定形态下
// 一律「保留先到者并跳过冲突行」（appendEvents / planDeliveryBatchOutsideLock 的身份收敛），
// 即保留的是正确版本，冲突行从未进入写入面；故清空只移除**诊断凭据**，不改任何日志内容。
//
// storageNamespace 为空 = 清本 Worker 全部持有冲突记录的源；非空 = 只清该源（完整源键
// LogSourceID/SourceGeneration，与 ResolveCoveredGapsForSource 取同一口径）。
//
// 红线（逐条对应实现）：
//  1. **操作人为空一律拒绝**：本操作会让查询面不再提示该源存在内容歧义，无痕清空等于给
//     「掩盖数据歧义」开后门（与缺口放弃同族的可审计要求）。
//  2. **理由为空一律拒绝**：凭据里必须有"为什么可以清"。
//  3. **留审计凭据**：identityConflictClearance（谁 / 何时 / 为何 / 清了多少条 / 被清记录明细）
//     随该源 payload 落库；**凭据落点没有 revision 可用**，详见 identityConflictClearance 注释。
//  4. **只动 identity_conflicts**：payload 里其它字段（verified_runs / abandonments /
//     ledger_core / events_stored / events / wal…）在清空前后逐字节不变。
//  5. **必须持久化**：走有界落库（同 #59 取舍），且**在 cycleMu 之外**。
//  6. **有界等锁**：acquireCycleMuBounded + 明确预算错误，绝不用无界 cycleMu.Lock()。
//
// 三段式（照 #55-B / #58 范式，理由同 ResolveCoveredGapsForSource）：第一段持锁只做守卫与
// 逐源计划快照（O(待清记录数)，有界 ≤ maxIdentityConflictRecords/源）→ 锁外无重活 →
// 第三段复验后做最小变更；最重的 persist 在临界区之外。
//
// 返回值的两件事必须分开读（2026-10-21 复审缺陷 B7）：
//   - 「冲突记录已清空」是本方法的契约，失败即 error；
//   - 「派生计数 conflict_count 已对齐」是尽力而为的收尾，失败**不**返回 error，
//     而是记在 IdentityConflictClearResult.DerivedRefreshFailures 里。
//
// 调用方不得因为 err==nil 就认为查询面已恢复正常：还要看 DerivedRefreshFailures 是否为空。
func (m *Manager) ClearIdentityConflicts(ctx context.Context, storageNamespace, operator, reason string) (IdentityConflictClearResult, error) {
	var result IdentityConflictClearResult
	if m == nil {
		return result, fmt.Errorf("ingest: manager unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// 操作人与理由必须在**取任何锁之前**校验：拒绝的请求不得产生任何可观测副作用。
	if strings.TrimSpace(operator) == "" {
		// 拒绝而不是用占位名兜过去：本操作会让查询面不再提示该源存在内容歧义（质量标记消失），
		// 无痕清空等于给「掩盖数据歧义」开后门。
		return result, fmt.Errorf("ingest: 清空身份冲突必须携带操作人（认证主体为空时拒绝执行）")
	}
	if strings.TrimSpace(reason) == "" {
		return result, fmt.Errorf("ingest: 清空身份冲突必须携带理由（理由为空时拒绝执行）")
	}
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("ingest: 清空身份冲突被取消（未写入任何变更）: %w", err)
	}
	key := strings.TrimSpace(storageNamespace)

	// ── 第一段（持 cycleMu，只做守卫 + 计划快照）：停止标记 / 源存在性 / 待清记录快照。
	if !m.acquireCycleMuBounded(ctx, clearIdentityConflictsCycleMuWait) {
		return result, m.clearConflictsBusyErr(ctx)
	}
	m.mu.Lock()
	stopped := m.stopped
	plans := make([]clearConflictsPlan, 0, 1)
	if key != "" {
		saved, ok := m.state.Sources[key]
		if !ok {
			m.mu.Unlock()
			m.cycleMu.Unlock()
			return result, fmt.Errorf("ingest: source %s not found", key)
		}
		plans = append(plans, clearConflictsPlan{key: key,
			records: append([]identityConflictRecord(nil), saved.IdentityConflicts...)})
	} else {
		keys := make([]string, 0, len(m.state.Sources))
		for sourceKey, saved := range m.state.Sources {
			if len(saved.IdentityConflicts) == 0 {
				continue
			}
			keys = append(keys, sourceKey)
		}
		sort.Strings(keys)
		for _, sourceKey := range keys {
			saved := m.state.Sources[sourceKey]
			plans = append(plans, clearConflictsPlan{key: sourceKey,
				records: append([]identityConflictRecord(nil), saved.IdentityConflicts...)})
		}
	}
	m.mu.Unlock()
	m.cycleMu.Unlock()
	if stopped {
		return result, fmt.Errorf("ingest: manager 已停止，拒绝清空身份冲突（停止后不得再写索引）")
	}
	if err := ctx.Err(); err != nil {
		// 等锁期间调用方可能已取消：不得留下「无人认领的清空凭据」。
		return result, fmt.Errorf("ingest: 清空身份冲突被取消（未写入任何变更）: %w", err)
	}
	if len(plans) == 0 {
		// 幂等目标态：请求覆盖范围内没有任何冲突记录。返回成功且计数为 0，不写凭据
		// （没有发生变更就不该产生一条"我清空了 0 条"的历史记录）。
		//
		// 但**仍要刷新派生字段**（2026-10-20 真机验收修正）：这一分支恰恰是「冲突早已清空、
		// 投影里 conflict_count 却还是旧值」的现场——实测 inst:320 clearedCount=0、
		// 索引已无记录，而 catalog 快照仍是 3（seq 1846 / projection-336），查询因此一直报
		// conflict、导出被永久禁用。若在此直接返回，就再没有任何入口能纠正它。
		// 结果里的失败项必须由调用方看到（见 DerivedRefreshFailures 的注释）：
		// 「冲突已清空」是真成功，因此这里**不**返回 error；但「派生计数已对齐」是另一件事，
		// 失败必须可观测，否则本分支就是典型的假成功（clearedCount=0、无 error、查询仍报 conflict）。
		result.DerivedRefreshFailures = append(result.DerivedRefreshFailures,
			m.republishAfterConflictClear(m.conflictClearRefreshTargets(key, nil))...)
		return result, nil
	}

	// ── 第二段（**锁外**）：本操作的"重活"只有一次锁外等待后的重新取锁；这里不做任何
	// 状态读取，避免把「计划快照」与「提交时读到的状态」混为一谈。
	//
	// 仅测试观测点（生产 nil，零开销）：锁外窗口到此结束 ⇒ 用例可在此注入「源又变了」以验证复验。
	if m.clearConflictsAfterPlanHook != nil {
		m.clearConflictsAfterPlanHook()
	}

	// ── 第三段（复验守卫 + 最小必要变更）：重新取有界 cycleMu；逐源复验记录集合未变，
	// 未变者清空并写凭据，已变者**跳过**（那批记录未经审阅）。
	atUTC := time.Now().UTC().Format(time.RFC3339Nano)
	if !m.acquireCycleMuBounded(ctx, clearIdentityConflictsCycleMuWait) {
		return result, m.clearConflictsBusyErr(ctx)
	}
	m.mu.Lock()
	stoppedNow := m.stopped
	if stoppedNow {
		m.mu.Unlock()
		m.cycleMu.Unlock()
		return result, fmt.Errorf("ingest: manager 已停止，拒绝清空身份冲突（停止后不得再写索引）")
	}
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		m.cycleMu.Unlock()
		return result, fmt.Errorf("ingest: 清空身份冲突被取消（未写入任何变更）: %w", err)
	}
	for _, plan := range plans {
		saved, ok := m.state.Sources[plan.key]
		if !ok || len(saved.IdentityConflicts) == 0 {
			result.SkippedSources = append(result.SkippedSources, plan.key)
			continue
		}
		if !sameIdentityConflicts(saved.IdentityConflicts, plan.records) {
			// 期间有新冲突写入（或记录被其它路径改写）：那批记录**未经本次人工审阅**，
			// 不得顺手清掉——否则"确认后清空"就退化成"无条件清空"。
			result.SkippedSources = append(result.SkippedSources, plan.key)
			slog.Warn("清空身份冲突：源在计划与提交之间新增了冲突记录，本次跳过该源（未清空任何记录）",
				"source", plan.key, "planned", len(plan.records), "current", len(saved.IdentityConflicts))
			continue
		}
		cleared := append([]identityConflictRecord(nil), saved.IdentityConflicts...)
		// 只清 identity_conflicts：本行之外不改动 persistedSource 的任何字段（防呆红线）。
		saved.IdentityConflicts = nil
		// 凭据：被清记录的明细是有界保留的（见 maxClearedConflictDetails），故条数单独记。
		detail := cleared
		if len(detail) > maxClearedConflictDetails {
			detail = detail[:maxClearedConflictDetails]
		}
		saved.IdentityConflictClearances = append(saved.IdentityConflictClearances, identityConflictClearance{
			Operator:       strings.TrimSpace(operator),
			Reason:         strings.TrimSpace(reason),
			AtUTC:          atUTC,
			ClearedCount:   len(cleared),
			ClearedRecords: append([]identityConflictRecord(nil), detail...),
		})
		if len(saved.IdentityConflictClearances) > maxIdentityConflictClearances {
			saved.IdentityConflictClearances = append([]identityConflictClearance(nil),
				saved.IdentityConflictClearances[len(saved.IdentityConflictClearances)-maxIdentityConflictClearances:]...)
		}
		m.state.Sources[plan.key] = saved
		result.ClearedCount += len(cleared)
		result.ClearedSources = append(result.ClearedSources, plan.key)
	}
	m.mu.Unlock()
	m.cycleMu.Unlock()

	if len(result.ClearedSources) == 0 {
		sort.Strings(result.SkippedSources)
		// 全源被跳过 ≠ 派生字段就是对的（2026-10-20 真机验收修正）：实测 inst:320 的
		// 冲突记录上一次已清空，本次因此 clearedCount=0、全部进 SkippedSources，
		// 而 catalog 里那次发布写下的 conflict_count 仍是 3 —— 陈旧快照恰恰出现在这里。
		// 若直接返回，就再没有任何入口能纠正它（该源无新数据投递，publishSnapshot 不会自发重跑）。
		// 故此处同样走一次「按需重发布」：内部有「快照已是 0 就跳过」的门槛，不会白付开销。
		// 失败项照样并进结果：本分支最需要它——「一条都没清」时运维唯一的判断依据就是
		// 「派生计数到底刷新了没有」。
		result.DerivedRefreshFailures = append(result.DerivedRefreshFailures,
			m.republishAfterConflictClear(m.conflictClearRefreshTargets(key, nil))...)
		return result, nil
	}
	sort.Strings(result.ClearedSources)
	sort.Strings(result.SkippedSources)
	slog.Info("已人工确认并清空身份冲突记录",
		"operator", operator, "reason", reason, "cleared", result.ClearedCount,
		"sources", len(result.ClearedSources), "skipped", len(result.SkippedSources))

	// ── 落库（索引规划/Apply/fsync 在 cycleMu 之外；有界，超预算交后台，见 #59）。
	//
	// 本段**刻意不碰 Catalog**：它只负责把「冲突记录已清空」这个人工凭据落到内存与索引。
	// 目录侧派生字段 conflict_count 的归零交给第四段（正规重发布 + 带 CAS 的分区对齐）——
	// 拆成两段是为了让「人工凭据变更」与「投影发布」的因果链各自可读：前者不含任何发布语义，
	// 后者每次都留下可核对（且**不改变权威代次**）的发布凭据。
	// 故本段只保证：内存与索引里已无冲突记录 ⇒ 之后任一发布（或第四段的对齐）必然算出 0。
	durable, err := m.persistClearConflictsTail()
	if err != nil {
		return result, err
	}
	if !durable {
		slog.Info("清空身份冲突的索引落库已交办后台，目录侧冲突计数待下一次投影发布时归零",
			"cleared", result.ClearedCount, "sources", len(result.ClearedSources))
	}

	// ── 第四段（已释放 cycleMu）：**主动重发布**，让派生字段 ConflictCount 当场归零。
	//
	// 为什么必须补这一步（2026-10-20 真机验收）：上面的注释把「下一次投影发布时重算」当作
	// 归零路径，代价写的是「已停止投递的源要等下一次发布才消失」。但真机实测表明这个代价
	// 对停投源等价于**永久卡死**：inst:320 清空成功（clearedCount=3、索引里已为 0），
	// 而 catalog 已发布投影里的 conflict_count 仍是 3（seq 1846 / projection-336）；
	// 该源没有任何新数据投递 ⇒ publishSnapshot 永不再被调用 ⇒ 查询一直读旧快照
	// ⇒ duplicate_quality=conflict ⇒ 前端导出被永久禁用。运维侧表现为「清了却看不出效果，
	// 也没有任何入口能推动它」。全仓也没有别的入口能刷新这个派生字段。
	//
	// 语义边界：仍然**不手改** catalog 里的字段，而是走同一条 publish 链路——
	// 当前分区由 publishSnapshot 用此刻已为 0 的 identityConflictCount 重算并发布；
	// 历史日期分区由 resetNamespaceConflictCounts 逐分区以 CAS 对齐派生计数。
	// 两条都是「提交一次发布」，只是把「等下一次」变成「现在就做一次」。
	// 注意并发：本段**不持 cycleMu**，与采集轮/投递路径是并发的，故两边写回都必须 CAS；
	// 输掉竞争的一方只告警「该分区待下次投递刷新」，由赢的那次真实发布留下它自己算出的计数。
	//
	// 失败**不**让本方法失败：冲突记录本身已清干净（那是本方法的契约），刷新派生字段属尽力而为。
	// 但失败必须能被调用方看到（2026-10-21 复审缺陷 B7）：
	//   - 「冲突已清空」与「派生计数已对齐」是**两件事**，后者失败不能让前者看起来像失败；
	//   - 也不能因为前者成功就把后者失败吞掉——历史分区不会再有任何投递触发 publishSnapshot，
	//     「待下次投递刷新」对它们是永久性的，没有任何自发重试路径；
	//   - 因此这里把失败项如实并进结果结构（DerivedRefreshFailures），逐条 Warn 日志照旧保留，
	//     使 API 调用方 / 运维能区分「清了」与「生效了」。
	//
	// **不挂在 durable 条件下**（2026-10-20 真机验收修正）：persistClearConflictsTail 在把
	// 索引落库交办后台时返回 durable=false（受 persist 预算影响，属常态而非失败），
	// 而派生字段的刷新与「索引是否已同步落库」无关——重发布走的是另一条正规链路。
	// 早前把刷新放在 durable 分支里，导致交办后台时静默跳过，表现为「清了但计数不动」。
	result.DerivedRefreshFailures = append(result.DerivedRefreshFailures,
		m.republishAfterConflictClear(m.conflictClearRefreshTargets(key, result.ClearedSources))...)
	if len(result.DerivedRefreshFailures) > 0 {
		// 汇总一条：逐条失败已在各自位置 Warn 过，这一条负责「别让人以为全都生效了」。
		slog.Warn("清空身份冲突：冲突记录已清空，但有分区的派生计数未能刷新（查询/导出仍可能被拦）",
			"failed", len(result.DerivedRefreshFailures), "detail", result.DerivedRefreshFailures)
	}
	return result, nil
}

// conflictClearRefreshTargets 汇总「需要刷新派生字段」的源键。
//
// 为什么不能只看 ClearedSources（2026-10-20 真机验收补记）：实测存在 ClearedCount=0 的形态——
// 索引里的冲突早已清空（上一次调用清掉了），但 catalog 里那次发布写下的 conflict_count
// 仍是旧值。此时源会被判成 skipped，若只按「本次真正清掉的源」刷新，就永远没人去纠正
// 陈旧快照。所以只要请求里点名了源键，就把它一并纳入刷新目标（没有可清的记录并不代表
// 投影里的派生值是对的）。
func (m *Manager) conflictClearRefreshTargets(requestedKey string, cleared []string) []string {
	out := make([]string, 0, len(cleared)+1)
	seen := make(map[string]struct{}, len(cleared)+1)
	for _, k := range cleared {
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	if k := strings.TrimSpace(requestedKey); k != "" {
		if _, ok := seen[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}

// republishAfterConflictClear 对被清空的源各触发一次投影重发布，使 ConflictCount 归零。
//
// 逐源独立：某个源刷新失败不影响其它源，也不影响「冲突已清空」这一既定结果。
// 不持 cycleMu 调用（本方法由 ClearIdentityConflicts 在释放 cycleMu 之后调用），
// 由 publishSnapshot 自己走既有锁序。
//
// 返回值是**没能刷新派生计数**的清单（2026-10-21 复审缺陷 B7）：这里的失败不构成操作失败
// （冲突记录确实已清空，那是 ClearIdentityConflicts 的契约），但绝不能只留在日志里——
// 历史日期分区不会再有任何投递触发 publishSnapshot，一旦这次失败，它们的 conflict_count
// 就永久停在旧值，查询/导出继续被拦。调用方把它并进结果结构，使「清了」与「生效了」
// 可区分。
//
// 并发形态（本方法因此**必须**用 CAS 写回）：这里与采集轮 pollOnce 是并发的——
// 采集轮持 cycleMu 在跑，本方法不持；同一 namespace 的多个源也可能同时走到这里。
// 于是「读某个分区快照」与「写回它」之间，真实发布（publishSnapshot → PublishProjection，
// 会重算 ConflictCount/manifest）、权威切换（OWNER_SWITCHED）都可能已经提交。
// 用旧快照无条件覆盖会把更晚的代次/权威回退掉、并误清真实计数，故：
//   - 分区级对齐 resetNamespaceConflictCounts 逐分区现读现写 + PublishProjection CAS；
//   - 当前分区随后再走一次正规发布（publishSnapshot 自身也是 CAS）。
//
// 任何一方输掉竞争都只告警 + 记入失败清单，不中断整个流程——
// 赢的那次真实发布已经写入了它自己算出的 ConflictCount，无需被本次对齐覆盖；
// 而调用方仍能从失败清单里看到「这个源/分区没被本次刷新覆盖」。
func (m *Manager) republishAfterConflictClear(sources []string) []string {
	if m == nil || len(sources) == 0 {
		return nil
	}
	var failures []string
	for _, sourceKey := range sources {
		m.mu.Lock()
		src, ok := m.sources[sourceKey]
		p, hasPipe := m.pipes[sourceKey]
		m.mu.Unlock()
		if !ok || !hasPipe || p == nil {
			continue // 源已不在运行时：没有投影可刷新，跳过即可。
		}
		// 门槛：只有投影里的派生值确实非 0 才值得付一次刷新。
		// 读的是已发布快照本身——这正是查询侧看到的那个值，因此判据与症状严格同源。
		if !m.projectionConflictCountNonZero(src) {
			continue
		}
		// 先把该 namespace 下**所有日期分区**的派生计数对齐（这才是查询窗口覆盖历史日期时
		// 仍报 conflict 的根源：29 个历史分区各自 cc=3，而它们不会再有任何投递去触发发布）。
		_, resetFailures := m.resetNamespaceConflictCounts(src.StorageNamespace, 0)
		if len(resetFailures) > 0 {
			slog.Warn("清空冲突后仍有分区的派生计数未能对齐（历史分区不会再有投递，需人工介入）",
				"namespace", src.StorageNamespace, "partitions", len(resetFailures))
			for _, f := range resetFailures {
				failures = appendDerivedRefreshFailure(failures, "namespace "+src.StorageNamespace+"/"+f)
			}
		}
		// 再让当前分区的投影走一次正规发布：它除了 ConflictCount 还会重算 manifest 等
		// 同源派生字段，保持「最后一行为一次真实发布」的因果可读性。
		// 权威事件集合取自账本/事件段（与恢复路径同源），确保重发布不会丢事件。
		events, err := m.canonicalRecoveryEvents(sourceKey, m.state.Sources[sourceKey])
		if err != nil {
			slog.Warn("清空冲突后重发布：取权威事件失败，派生计数待下次投递刷新",
				"source", sourceKey, "error", err)
			failures = appendDerivedRefreshFailure(failures,
				"source "+sourceKey+": 取权威事件失败，当前分区派生计数未刷新: "+err.Error())
			continue
		}
		gen := p.Key().SourceGeneration
		if err := m.publishSnapshot(src, gen, events, true, nil, nil, false); err != nil {
			slog.Warn("清空冲突后重发布：派生计数刷新失败，待下次投递再试",
				"source", sourceKey, "error", err)
			failures = appendDerivedRefreshFailure(failures,
				"source "+sourceKey+": 当前分区重发布失败，派生计数未刷新: "+err.Error())
			continue
		}
		slog.Info("清空冲突后已重发布投影，冲突计数应已归零", "source", sourceKey, "events", len(events))
	}
	return failures
}

// maxDerivedRefreshFailures 是 IdentityConflictClearResult.DerivedRefreshFailures 的条数上限。
//
// 为什么要封顶：分区数量级是「节点数 × 天数」（生产 283 个），同一 namespace 下的源又可能
// 多个，若不封顶，这条结果会随分区数无界增长并被塞进 API 响应与审计日志。汇总仍是
// 「至少这么多处失败」——封顶只会让清单更短，绝不会让失败消失（末尾会留一条截断提示）。
const maxDerivedRefreshFailures = 32

// appendDerivedRefreshFailure 把一条「派生计数刷新失败」计入清单，并做有界截断。
//
// 截断只影响逐条列举，不影响「存在失败」这一事实：清单非空 ⇔ 确有未刷新的位置，
// 这正是调用方与运维的唯一判据（见 IdentityConflictClearResult.DerivedRefreshFailures）。
func appendDerivedRefreshFailure(failures []string, item string) []string {
	if len(failures) < maxDerivedRefreshFailures {
		return append(failures, item)
	}
	if len(failures) == maxDerivedRefreshFailures {
		return append(failures,
			fmt.Sprintf("…以及更多未能逐条列出的刷新失败（已达上限 %d 项）", maxDerivedRefreshFailures))
	}
	return failures
}

// projectionConflictCountNonZero 报告该 **namespace 下是否还有分区**的 ConflictCount 非 0。
//
// 读 catalog 快照而非内存计数器，因为症状就出在这个快照上：内存/索引里冲突已清空，
// 但快照仍是那次发布写下的旧值。
//
// 为什么必须遍历全部日期分区而非只看当天（2026-10-20 真机验收修正）：ConflictCount 是
// **namespace 级**状态（identityConflictCount 按 StorageNamespace 汇总，与日期无关），
// 但它的载体是**按天分区的投影记录**。生产实测 inst:320 有 29 个日期分区各自 cc=3
// （2026-09-20 ~ 2026-10-20，缺 09-29/10-15）；早前只查/只刷当天分区，于是查询窗口一旦
// 覆盖到历史日期就再次读到 cc=3 ⇒ duplicate_quality=conflict 永远消不掉。
// 取不到记录时按「非 0」处理——宁可多刷一次，也不要把陈旧值留在查询面上。
func (m *Manager) projectionConflictCountNonZero(src SourceConfig) bool {
	if m == nil || m.cat == nil {
		return false
	}
	for _, key := range m.cat.Keys() {
		if key.StorageNamespace != src.StorageNamespace {
			continue
		}
		rec, ok := m.cat.Get(key)
		if !ok || rec == nil || rec.PublishedProjection == nil {
			return true
		}
		if rec.PublishedProjection.ConflictCount > 0 {
			return true
		}
	}
	return false
}

// resetNamespaceConflictCounts 把该 namespace 下**所有日期分区**的派生字段 ConflictCount
// 对齐到当前真实值（清空冲突后即 0），并逐分区落 journal，使重启重放后仍是 0。
//
// 为什么不能只靠 publishSnapshot：它一次只发布「当前 UTCDay」那一个分区，而历史日期分区
// 不会再有任何投递去触发发布（源早已停止写入那些天）。因此这里直接写派生字段本身：
//   - 只改 ConflictCount 这一个派生字段，不动任何物理写入凭据（closed_visible_seq、
//     manifest、source_projections 全部原样保留）；
//   - 逐分区走带 CAS 的发布（见 alignPartitionConflictCount），重启时重放成同一状态；
//   - 幂等：本来就是 0 的分区直接跳过，不产生 journal 噪音。
//
// 为什么写回必须 CAS（并发缺陷修正）：本方法由 republishAfterConflictClear 在**不持 cycleMu**
// 的情况下调用，而采集轮 pollOnce 正持着 cycleMu 在跑。因此在「读快照」与「写回」之间，
// 采集轮/投递路径完全可能对同一个 key 完成一次真实的投影发布（publishSnapshot →
// PublishProjection），甚至完成一次 OWNER_SWITCHED。若仍用旧快照无条件覆盖：
//   - 会拿陈旧代次自增，把更晚的真实代次/权威**回退**掉（旧的 owner 与目录也会被一起写回）；
//   - 会把那次真实发布算出的 ConflictCount 错误清零 ⇒ 前端误报「无冲突」，数据诚实性受损。
//
// 故这里不再自造 journal 项，而是以「刚读到的快照」为 CAS 基准走 PublishProjection：
// 它按 journal 序号 / owner / 代次 / 目录 / 写路由 / 恢复要求 / 投影清单逐项比对，
// 中途有任何真实写入赢得竞争即返回 ErrInvalidState —— 那次真实发布自己已写好
// ConflictCount，不需要（也不允许）被本次对齐覆盖，跳过交给下一次投递即可。
//
// 返回（实际改写的分区数, 没能刷新的分区/源清单）。
//
// 为什么失败项必须作为返回值交出去（2026-10-21 复审缺陷 B7）：早前这里对失败只打一行 Warn
// 就 continue，于是「清了 3 条冲突」与「派生计数有没有真的对齐」在对外结果上无法区分——
// 运维拿到 clearedCount=3 且无 error，会认为已生效。而失败的分区（例如处于
// `QueryGeneration != Generation` 不一致态、被 PublishProjection 一进门就拒的那些）
// 若属历史日期，**不会再有任何投递**来触发 publishSnapshot ⇒ 「待下次投递刷新」对它们
// 是永久性的，没有自发重试路径。故失败必须能传回调用方，由结果结构如实报出。
func (m *Manager) resetNamespaceConflictCounts(namespace string, want uint64) (changed int, failures []string) {
	if m == nil || m.cat == nil || namespace == "" {
		return 0, nil
	}
	for _, key := range m.cat.Keys() {
		if key.StorageNamespace != namespace {
			continue
		}
		// 逐分区现读现写：不预取快照列表，缩窄「读 → 写」窗口；
		// 窗口再窄也仍存在，故写回那一端必须 CAS（见 alignPartitionConflictCount）。
		rec, ok := m.cat.Get(key)
		if !ok || rec == nil || rec.PublishedProjection == nil {
			// 没有已发布投影的分区同样刷不上派生计数，必须报出去（查询侧会把它们
			// 按「投影未发布」处理，但调用方无从得知是哪几个分区）。
			failures = append(failures, key.UTCDay+": 分区没有已发布投影（无法对齐派生计数）")
			continue
		}
		if rec.PublishedProjection.ConflictCount == want {
			continue
		}
		before, err := m.alignPartitionConflictCount(rec, want)
		if err != nil {
			slog.Warn("清空冲突后对齐派生计数失败，该分区待下次投递刷新（历史分区不会再有投递）",
				"namespace", namespace, "utcDay", key.UTCDay, "error", err)
			failures = append(failures, key.UTCDay+": "+err.Error())
			continue
		}
		changed++
		slog.Info("清空冲突后已对齐分区派生计数",
			"namespace", namespace, "utcDay", key.UTCDay, "from", before, "to", want)
	}
	return changed, failures
}

// alignPartitionConflictCount 把 expected 所代表分区快照里的派生字段 ConflictCount 对齐为
// want，并以 expected 为 CAS 基准原子提交。返回对齐前的值。
//
// expected 必须来自 m.cat.Get（携带它当时的 JournalLastSeq），它是**唯一的并发判据**：
// 从 Get 到提交之间只要有人写过该分区（真实发布、权威切换、其它对齐），
// PublishProjection 就返回 ErrInvalidState，调用方据此放弃本次对齐。
//
// 为什么复用 PublishProjection 而不是自己拼 journal 项：它已经把「权威没变」的全部判据
// （journal 序号 / owner / 代次 / 目录 / 写路由 / 恢复要求 / 投影清单）写在一处并加锁比对，
// 复刻一份必然漂移。代价是 journal 凭据沿用 "verified projection publication"——
// 这与 publishSnapshot 每次发布走的是同一条通道，语义上没有新增歧义。
//
// 为什么**不动 Generation**（与旧实现的关键差异）：catalog 契约里 Generation 是查询/写入
// 权威代次，只有 OWNER_SWITCHED 原子提交才能变更（types.go 的 Record.Generation 注释、
// invariants.ApplyOwnerSwitch）；而 PublishedProjection.QueryGeneration 必须恒等于它
// （PublishProjection 的前置校验；查询侧 query/types.go 的 archiveRestored 与
// grpcsvc.runtimeCatalogReadiness 也按这条判「投影是否已发布」）。派生计数对齐不是一次新的
// 权威代次，故保持 Generation 不变。旧实现写 next.Generation = rec.Generation + 1，却把
// QueryGeneration 留在旧值，实测（可复现）造成两个后果：该分区之后**任何一次真实发布都被
// PublishProjection 前置校验拒绝**（"projection and authority do not match"），且查询侧把
// 它的投影判成「未发布」。
// 又因为 PublishProjection 本身禁止改权威（expected/next 的 owner/代次/目录/写路由必须相同），
// 这两条约束在这里是一致的，不是绕过校验。
func (m *Manager) alignPartitionConflictCount(expected *catalog.Record, want uint64) (uint64, error) {
	if m == nil || m.cat == nil || expected == nil {
		return 0, fmt.Errorf("%w: 缺少 Catalog 或分区快照", catalog.ErrInvalidState)
	}
	if expected.PublishedProjection == nil {
		return 0, fmt.Errorf("%w: 分区快照缺少已发布投影", catalog.ErrInvalidState)
	}
	// 克隆后只改派生计数：owner / 代次 / 目录 / 写路由 / 恢复要求 / 投影清单原样保留，
	// 既满足 PublishProjection 的「发布不得改权威」前置校验，也不会伪造一次新发布。
	next := expected.Clone()
	before := next.PublishedProjection.ConflictCount
	next.PublishedProjection.ConflictCount = want
	if err := m.cat.PublishProjection(expected, next); err != nil {
		return before, err
	}
	return before, nil
}

// clearConflictsBusyErr 构造"等不到 cycleMu"的明确错误（区分取消与超预算，便于运维判断）。
func (m *Manager) clearConflictsBusyErr(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return fmt.Errorf("ingest: 清空身份冲突被取消（未写入任何变更）: %w", ctx.Err())
	}
	return fmt.Errorf("ingest: 采集轮忙（cycleMu 等待超 %s 预算），清空身份冲突未写入任何变更，请稍后重试",
		clearIdentityConflictsCycleMuWait)
}

// sameIdentityConflicts 逐字段比较两份冲突记录集合（顺序敏感：清空是"这一份"的删除）。
func sameIdentityConflicts(a, b []identityConflictRecord) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// New restores source ledger/WAL/projection state and creates configured pipelines.
func New(opts Options) (*Manager, error) {
	if limits := materializationLimitsOf(opts.MaterializationLimits); limits.CompactionReserveBytes < 0 || limits.CompactionReserveBytes > limits.HourlyBytes {
		return nil, fmt.Errorf("ingest: 必要压缩保留字节必须在总小时物化字节预算以内")
	}
	// 在打开索引、创建目录及启动后台任务前校验原值，禁止默认化吞掉非法配置。
	if err := opts.Sampling.Validate(); err != nil {
		return nil, fmt.Errorf("ingest: 采样策略无效: %w", err)
	}
	if opts.Root == "" {
		return nil, fmt.Errorf("ingest: root is required")
	}
	if opts.Catalog == nil {
		return nil, fmt.Errorf("ingest: catalog is required")
	}
	m := &Manager{
		root: opts.Root, vl: opts.VL, vlRoute: opts.VLRoute, vlReadRoute: opts.VLReadRoute, cat: opts.Catalog, journal: opts.Journal, archive: opts.Archive,
		// 不可裁定冲突的留痕用**独立阈值**（汇总窗比正常收敛长，见 newConflictThrottle）。
		conflictTraces: newConflictThrottle(),
		// 临时中转目录放 tmpfs（见 logScratch 字段注释）。
		logScratch:            eventstore.MemoryRoot("scratch-" + eventstore.SegmentRootFor(opts.Root)),
		vlWritePermitRoute:    opts.VLWritePermitRoute,
		resourceBudget:        opts.ResourceBudget,
		readLimits:            opts.ReadLimits,
		walReserve:            opts.WALReserve,
		deferredDelivery:      opts.DeferredDelivery,
		materializationLimits: materializationLimitsOf(opts.MaterializationLimits),
		sources:               make(map[string]SourceConfig), pipes: make(map[string]*pipeline.Pipeline),
		state:                    persistedState{Sources: make(map[string]persistedSource)},
		verificationTimeout:      opts.VerificationTimeout,
		capacityProvider:         opts.CapacityProvider,
		sampling:                 opts.Sampling,
		recoveryHold:             opts.RecoveryHold,
		resolveGapsBudget:        resolveGapsBudgetOfOpts(opts.ResolveGapsBudget),
		reconcile:                reconcileConfigOf(opts.Reconcile),
		reconcileLoop:            reconcileLoopConfigOf(opts.ReconcileLoop),
		indexPrune:               indexPruneConfigOf(opts.IndexPrune),
		eventStorePrune:          eventStorePruneConfigOf(opts.EventStorePrune),
		indexCommit:              indexCommitBudgetOf(opts.IndexCommit),
		walLimits:                opts.WALLimits,
		reconcileYield:           reconcileYieldFromOpts(opts),
		reconcileSliceEvents:     reconcileSliceFromOpts(opts),
		reconcileYieldNotify:     opts.ReconcileYieldNotify,
		resumeInterval:           opts.ResumeInterval,
		resumeBatchPerRound:      opts.ResumeBatchPerRound,
		sweepBackpressureStreak:  opts.SweepBackpressureStreak,
		sweepInflightCap:         opts.SweepInflightCap,
		recoveryQuotaFactor:      opts.RecoveryQuotaFactor,
		recoveryDrainGrace:       opts.RecoveryDrainGrace,
		recoveryProbe:            opts.RecoveryProbe,
		scanYield:                scanValueOf(opts.ScanYield, opts.Scan, func(t *ScanTuning) time.Duration { return t.Yield }),
		scanSliceRows:            scanSliceRowsOf(opts.ScanSliceRows, opts.Scan),
		scanYieldNotify:          opts.ScanYieldNotify,
		scanBudget:               scanValueOf(opts.ScanBudget, opts.Scan, func(t *ScanTuning) time.Duration { return t.Budget }),
		maxReplayEventsPerDrain:  opts.MaxReplayEventsPerDrain,
		multilineUnclosedTimeout: multilineUnclosedTimeoutOf(opts.MultilineUnclosedTimeout),
		insertBatch:              insertBatchLimitsFrom(opts.InsertBatch),
	}
	// 生命周期 ctx：持久化的可取消面（Stop 取消）。只在 New 中赋值、之后不再改写 ⇒ 读不加锁。
	m.lifecycleCtx, m.lifecycleCancel = context.WithCancel(context.Background())
	m.persistYield = opts.PersistYield
	m.persistHotBudget = opts.PersistHotBudget
	if m.persistHotBudget <= 0 {
		m.persistHotBudget = defaultPersistHotBudget
	}
	m.persistRequests = make(chan struct{}, 1)
	m.persistWorkerDone = make(chan struct{})
	m.persistPriorityStreak = opts.PersistPriorityStreak
	// 投影重建调度器（见 rebuild_scheduler.go）：每源台账 + 单飞/合并队列 + 限速与退避旋钮。
	m.rebuilds = newRebuildScheduler()
	// 缺口消解异步调度器（见 gap_resolve_async.go）：把 VL 可见性核对从投递路径挤出去。
	m.gapResolves = newGapResolveScheduler()
	if tuning := opts.RebuildTuning; tuning != nil {
		if tuning.Throttle > 0 {
			m.rebuilds.throttle = tuning.Throttle
		}
		if tuning.MaxAttempts > 0 {
			m.rebuilds.maxAttempts = tuning.MaxAttempts
		}
	}
	m.rebuilds.yieldNotify = opts.RebuildYieldNotify
	m.rebuilds.hookRun = opts.RebuildRunNotify
	m.replayBudget = replayBudgetOf(opts.ReplayBudget)
	// 热层可发布窗口（2026-10-21 生产缺陷修复，见 hot_retention_window.go）：
	// 零值 = 未知/不启用过滤（保持改动前行为）。
	m.hotRetention = opts.HotRetention
	m.vlProbeTimeout = opts.VLReadyProbeTimeout
	m.vlReadyWait = opts.VLReadyWait
	if tuning := opts.ReplayTuning; tuning != nil {
		if tuning.VLReadyProbeTimeout > 0 {
			m.vlProbeTimeout = tuning.VLReadyProbeTimeout
		}
		if tuning.VLReadyWait > 0 {
			m.vlReadyWait = tuning.VLReadyWait
		}
		if tuning.Budget.MaxDays > 0 || tuning.Budget.MaxDuration > 0 {
			m.replayBudget = replayBudgetOf(tuning.Budget)
		}
	}
	if m.vlProbeTimeout <= 0 {
		m.vlProbeTimeout = defaultVLReadyProbeTimeout
	}
	if m.vlReadyWait <= 0 {
		m.vlReadyWait = defaultVLReadyWait
	}
	m.vlNotReady = make(map[string]string)
	m.defaultCharset = opts.DefaultCharset
	m.resolvePersistBudget = opts.ResolvePersistBudget
	// #64：滚动回收单轮上界旋钮已于 2026-10-29 删除（见 Options.ReclaimRoll 处注释）。
	// #57：cycleMu 持有者观测仪（env 默认关；Options 可显式打开，供回归与现场按需取读数）。
	m.cycleMuProfile = opts.CycleMuProfile || cycleMuProfileEnabled()
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
	m.verifyMaxQueriesPerChunk = opts.VerifyMaxQueriesPerChunk
	if m.verifyMaxQueriesPerChunk <= 0 {
		m.verifyMaxQueriesPerChunk = defaultVerifyMaxQueriesPerChunk
	}
	m.verifyLimiter = newVerifyLimiter(opts.VerifyBudget)
	m.verifyRoundBudget = opts.VerifyRoundBudget
	if m.verifyRoundBudget <= 0 {
		m.verifyRoundBudget = defaultVerifyRoundBudget
	}
	m.verifyVisibilityFloor = opts.VerifyVisibilityFloor
	m.verifyChunkEstimates = opts.VerifyChunkEstimates
	// 采集索引（FR-496）：打开 SQLite 索引，必要时一次性迁移旧 ingest.state.json；
	// 迁移/校验失败 → 拒绝启动采集（不静默降级），保留旧文件供人工处置。
	if err := m.openIndex(); err != nil {
		return nil, err
	}
	// 事件体走追加式磁盘段（FR-484）：state 只留元数据，避免整份重写与常驻切片。
	// 事件体存到 **tmpfs**（2026-10-30 用户裁定：机械盘不做任何落盘）。
	//
	// eventstore 的职责是「投递前的暂存 + 投递后可回收」，数据本来就短命；
	// 放到 /dev/shm 后写入不再碰机械盘，而段滚动与回收的内存上界能力全部保留。
	// 代价：Worker 重启后段没了——与「cursor 不落盘」是同一取舍。
	// 安全不变式（2026-11-02）：段根必须由**数据根绝对路径**派生，不能用 filepath.Base。
	// basename 不唯一——生产 `.../prod/worker/data` 与实验件 `.../fr498/data` 都是 `data`，
	// 共用段根时实验进程的 Prune/回收前滚会真删生产的事件段。见 eventstore.SegmentRootFor。
	store, err := eventstore.Open(eventstore.MemoryRoot(eventstore.SegmentRootFor(opts.Root)))
	if err != nil {
		return nil, err
	}
	// 段清单落盘策略（键 log_index.persist.manifest_sync_*）：机械盘上把一次追加的
	// 耐久调用从 3 次降到 1 次（清单里的派生元数据允许滞后，崩溃后由校准精确补齐）。
	store.SetManifestSyncPolicy(opts.EventStoreManifestSyncEvery, opts.EventStoreManifestSyncInterval)
	m.events = store
	// 采集游标（每源一行）必须在建管道**之前**读进来：
	// 登记路径的 resumeCursorFor 靠它决定「从上次位置继续」还是「从文件末尾起读」。
	// 读失败只是空表（最坏重读一段），不阻断启动。
	m.loadCursorRegistryIntoManager()
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
	// 源配置的跨重启载体（2026-10-30 第二轮）：索引库是 `:memory:`，重启后 m.state 是空的，
	// 而实例源本来就不在 opts.Sources 里（它由 CP 登记时产生）——不从这里恢复，重启后
	// `m.pipes` 就是 0，采集器“不知道该采哪个文件”（实测：SourceConfigs=0 pipes=0 游标表=1）。
	//
	// 恢复源与重建管道同处一个位置，不拆到两处：两边都是一个 key → 一份配置。
	// 优先级依次为（后者覆盖前者）：
	//	1. 索引里的持久配置（本进程内有效；重启后为空）
	//	2. 游标表里的源登记（上次运行的显式口径，跨重启唯一来源）
	//	3. Options.Sources（当次装配的显式配置）——**优先级最高**
	//
	// 为什么 opts.Sources 必须最高：它是当次进程的显式声明（worker.yml / 部署面），
	// 历史记录不得覆盖当前声明——否则改了 yml 的路径重启后仍按旧路径采，现场无从解释。
	//
	// **从 yml 删掉一个源，它不会因此消失**：游标表里那条登记仍在，下一轮启动仍会把它
	// 重建出来。这是**有意的**，不是 bug：
	//   - 该源是真的存在、真的在被读的（游标表里记着它的位置）；
	//   - 「停止采集某个源」应该是一个**显式删除动作**，而不是从「yml 里没有它」这种
	//     缺席证据去推断。靠缺席推断会在两处误伤：yml 临时只写部分源（灰度/排障）、
	//     以及实例源（它们本来就不在 opts.Sources 里，靠这条推断会全部失效）；
	//   - 如果以后真的要加「yml 即权威白名单」的开关，它必须能区分「显式源」与
	//     「实例源」两张面，不能是一刀切。
	registrySources := m.sourcesFromCursorRegistry()
	for key, source := range registrySources {
		restoredSources[key] = source
	}
	for _, source := range opts.Sources {
		restoredSources[source.LogSourceID+"/"+source.SourceGeneration] = source
	}
	if len(restoredSources) > 0 {
		slog.Info("采集源已恢复（启动重建）", "sources", len(restoredSources),
			"fromCursorRegistry", len(registrySources), "fromOptions", len(opts.Sources))
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
	recoveryPossible := m.vl != nil || m.vlRoute != nil
	// sourceConfigRecalced 记录「本次启动恢复确实按新算法改写了某个源的配置」。
	// 它必须自己触发一次落库，理由见下方 requestPersist 处的说明。
	sourceConfigRecalced := false
	for _, sourceKey := range keys {
		source := restoredSources[sourceKey]
		// FR-504：实例源按**当前规则**重算物理路径与归档 glob——账本里存的是当时代码算出的值，
		// 而规则算法会演进（bungee 的归档 glob 由 `proxy.log.[0-9]*` 改为空即一例）。若原样信任
		// 账本，修正对**已登记源**永不生效：真机实测 glob 修正上线并重启后，索引里的旧 glob 一直
		// 留着，采集轮仍按旧 glob 发现归档。定位不到绑定（worker.yml 显式源 / 绑定已摘除）时
		// recalcInstanceSourceConfig 原样返回，不猜测。
		if source.SourceCategory == logtypes.SourceInstance {
			recalced := m.recalcInstanceSourceConfig(source)
			if recalced.Path != source.Path || recalced.ArchiveGlob != source.ArchiveGlob {
				sourceConfigRecalced = true
				slog.Info("实例源配置已按当前规则重算（算法演进后的首次恢复）",
					"logSourceID", source.LogSourceID,
					"archiveGlob", recalced.ArchiveGlob, "path", recalced.Path)
			}
			source = recalced
			// 同步回索引：否则随后的持久化会把旧值又写回去（本函数在 New 的单线程构造期，
			// 与上方读取 restoredSources 同口径，无需取锁）。
			m.state.SourceConfigs[sourceKey] = source
		}
		if err := m.Register(source); err != nil {
			return nil, err
		}
		if !recoveryPossible {
			// 无 VL 客户端：没有可对账、也没有可重发的目标（与旧行为一致）。
			continue
		}
		// 只登记「待恢复的源」——**读回权威事件集与对账/重发全部移到后台**（见 runStartupRecovery）。
		// 改动前这里逐源把 canonical 事件全量读进内存再对账，于是 New 的耗时为
		// O(全部源的事件总量)（生产 18GB，单线程），期间 worker 完全不可达（2026-10-02 事故）。
		recovery = append(recovery, startupSource{source: source, key: source.LogSourceID + "/" + source.SourceGeneration})
	}
	if err := m.recoverPendingSpools(); err != nil {
		_ = store.Close()
		return nil, err
	}
	// StartupRecoveryBackground（生产）：恢复不阻塞 New——New 返回后 main 立即继续，
	// 反向隧道/WS/HTTP 随即可达，恢复在后台按批续做并经就绪面如实上报。
	// 其余调用方（默认）：就地执行同一驱动，保持「New 返回即恢复完成」的既有边界。
	go m.persistLoop(m.lifecycleContext())
	if sourceConfigRecalced {
		// 源配置重算必须**自己触发**一次落库（FR-504 复核，真机实测）。
		//
		// 缺陷形态：持久化的触发面是采集/投递侧的变更声明（`planPersist` 的 changed 只按账本修订号
		// 与段覆盖签名判定），而重算源配置不动账本 ⇒ 安静源（没有新日志）永远不会声明变更，
		// 索引里的 `source_aux.config` 于是长期停留在旧值：日志说"重算生效"，重启后旧 glob 又回来，
		// 采集轮继续按旧值找归档。
		//
		// 为什么用非阻塞的 requestPersist 而不是 m.persist()：New 不得被持久化拖住
		// （2026-10-02 事故：New 里做重活让 worker 整段不可达）。交办由后台通道保证最终完成；
		// 且索引根行（source/source_aux/…）每轮都全量比对，一次周期即可把新值写出去。
		//
		// 为什么不改持久化的**跳过判据**：源配置变化本就无需参与判据——根行每轮全量比对，
		// 变更必然被写；真正缺的只是"有没有人触发这一轮"。把配置纳入每轮判据反而要在锁内多做
		// 一次逐源比对，属于为一个已成立的机制补重复保险。
		m.requestPersist()
	}
	if err := m.startStartupRecovery(recovery, opts.StartupRecoveryBackground); err != nil {
		_ = store.Close()
		return nil, err
	}
	return m, nil
}

// lifecycleContext 返回 Manager 生命周期 ctx（持久化的可取消面）。字段只在 New 中赋值，
// 故读取不加锁、也不参与锁序。
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
	root := filepath.Join(m.logScratch, pendingSpoolRoot)
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
