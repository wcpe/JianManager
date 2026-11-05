// 本文件是**生命周期与采集轮**面：New / Register / Start / Stop / CloseIndex、
// 采集轮 pollOnce / pollSource、归档扫描触发，以及轮锁（cycleMu）与 m.mu 的
// 观测辅助（watchMutexWait / noteCycleMuWait / MutexHoldSlowCount 等）。
//
// 这些方法只在「启动、停止、每一轮采集」这几个时刻被触发，不参与单次投递的
// 数据通路——按触发时机聚成一处，读起来才不会与投递逻辑混在一起。
package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/normalize"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

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
	// 2026-10-29：WAL 改纯内存后重启不再做队列恢复，`bodyLookup`（事件体水合）的唯一
	// 消费者已删除。因此源登记不再有两段式「是否需要 hydrate」的预判——直接一次持锁完成
	// 判定与登记（原本那段锁外磁盘遍历是启动期最贵的一步：单源实测 126MB/135k 行）。
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.pipes[key]; ok {
		if !sameSourceConfig(m.sources[key], source) {
			return fmt.Errorf("ingest: source identity already bound to another configuration")
		}
		return nil
	}
	led := ledger.New()
	recoveryDirty := false
	// 历史投递批次裁剪（FR-496 索引有界化）：配置默认开启，非法值已在 indexPruneConfigOf 回退。
	led.SetDeliveryBatchPrune(m.indexPrune)
	wal := newWAL(led, ledger.SourceKey{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration})
	if saved, ok := m.state.Sources[key]; ok {
		if err := led.Restore(saved.Ledger); err != nil {
			return err
		}
		// 重启后**不做 WAL 恢复**（2026-10-29 架构变更，对齐 Filebeat registry 语义）。
		//
		// ## 为什么删掉恢复
		//
		// WAL 现在是**纯内存队列**，不落盘。这不只是"少写一张表"，而是换了一套可靠性模型：
		//
		//   - 旧模型（Filebeat `queue.disk` 那档）：队列内容持久化 ⇒ 重启后逐条恢复；
		//   - 新模型（Filebeat `queue.mem` + registry 那档）：**队列重启即丢**，
		//     可靠性由「持久化的 offset（registry）+ 至少一次重读」承担。
		//
		// 业界原文（Elastic 官方 configuring-internal-queue）：
		//   "The disk queue stores pending events on the disk rather than main memory...
		//    and **to save events when a Beat or device is restarted**."
		// ⇒ 内存队列本身就不保证跨重启保存；靠 disk queue 才有那个保证。
		//
		// ## 那"已读未投递"的数据怎么不丢
		//
		// 靠**读水位回退到回收水位重读**（见下方 rollbackReadToReclaim）。这是 at-least-once：
		// 宁可重复投递（下游按 event_id 幂等 + projection_generation 换代次过滤），
		// 不可丢。重复的代价是一次重读，丢失的代价是永久少数据——两者不对称。
		//
		// ## 水位必须一起修正
		//
		// `durable` 原本和 WAL 行是**同一个提交单元**落库的，语义是"这些字节已在库里"。
		// WAL 不落盘后这个语义不再成立 ⇒ 必须把 `durable` 一并退到回收水位，
		// 否则 tailer 会按 `min(durable, read)` 从"以为已持久化"的位置继续读，
		// 跳过那段本应重读的字节。
		rollbackReadToReclaim(led, ledger.SourceKey{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration})
	}
	// FR-502：规则变更导致物理载体被替换时，从新文件末尾开始采集。
	//
	// **为什么不是记缺口**：本场景不是「内容丢失」，而是**有意的起点变更**——旧文件与
	// 新文件各有自己的字节流，新文件的既有内容不属于本源的采集范围（它在该文件被
	// 换进来之前就已存在，可能已由别的途径投递过）。把它登记成缺口会让源被
	// 「不可分类缺口」永久阻塞（真机实测），因为缺口语义是「这段本该有但没有」。
	//
	// **正确做法**：段基址（StartPos）保持 0，把**已读水位**推进到文件末尾
	// （AdvanceRead/AdvanceDurable = size）。判据在 acquire.FileTailer 侧：
	// 它的 cursor 是**文件内偏移**，由 `cursor = durable - segmentStart` 恢复
	// （acquire/tailer.go 的 NewFileTailer），因此 `segmentStart = 0 ∧ durable = size`
	// 恰好得到 `cursor = size` ⇒ 下次 Poll 从 EOF 续读。
	//
	// 2026-10-15 复核：此处曾把段基址也前移到 size（StartPos=size, EndPos=size），
	// 于是 `cursor = size - size = 0`，tailer 反而**从文件头**读起——规则升级后整份既有内容
	// 被以新位置重新摄取（真机 30 倍重复的形态），且使 CurrentSegmentUnread() 恒真、
	// 绕过下方「历史归档只能前置到未被读过的当前段」的守卫。段基址是**逻辑源**的起点，
	// 不是「开始读取的位置」；后者只能由水位表达。
	// 起点选择（2026-10-30 P2.1）：一律从**当前文件末尾**起读。
	//
	// **为什么全部源都锤 EOF，不只是规则变更的源**：P1（7456d716）把索引库改成 `:memory:`
	// SQLite 后，账本不再跨重启存活 ⇒ 旧实现按「四水位恢复 cursor」恒得 0 ⇒ 每个源
	// 重启后都会把**整份历史日志重读一遍并重投 VL**（源文件在生产上动辄几百 MB）。
	// 用户裁定「直接读取到的往 vl 塞啊，内存队列满了就丢啊」——不承诺跨重启不丢，
	// 也就不需要跨重启的游标。
	//
	// 旧的 `source.StartAtEOF` 分支（在账本里把 Read/Durable 水位推到 EOF）一并命去了：
	// 那个机制表达的是「用账本水位间接告诉 tailer 从哪读」，现在 tailer 自己就錨在 EOF，
	// 再由账本去表达只会多一套要维护的状态（而且水位推进本身需要账本跨重启存活才成立）。
	//
	// `source.StartAtEOF` 字段保留：`takeStartAtEOF` 仍会在实例规则升级时置位它，
	// 而现在的语义是「规则变更源与其它源同一样锤 EOF」——两者结果一致，日志仍留一条痕迹。
	if source.StartAtEOF && source.Mode == pipeline.ModeFilePrimary && source.Path != "" {
		slog.Info("源起点：规则变更后从文件末尾开始采集（与全局 EOF 起点一致）",
			"log_source_id", source.LogSourceID, "generation", source.SourceGeneration, "path", source.Path)
	}
	var deferred func()
	var committed func(context.Context, []acquire.CommitIdentity) error
	var p *pipeline.Pipeline
	if m.deferredDelivery {
		deferred = func() { m.schedulePendingReplay(key) }
		committed = func(ctx context.Context, required []acquire.CommitIdentity) error {
			err := m.persistCommittedBeforeDelivery(ctx, key, p, required)
			if err != nil {
				m.deferDurableDelivery(key)
			}
			return err
		}
	}
	var contextReclaim func(context.Context, []logtypes.Event) error
	if m.deferredDelivery {
		contextReclaim = func(ctx context.Context, events []logtypes.Event) error {
			return m.releaseRecovery(source, events, ctx)
		}
	}
	p, err = pipeline.New(pipeline.Options{
		Mode: source.Mode, LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration,
		Path: source.Path, RotateTo: source.RotateTo, Stream: source.Stream, Charset: source.Charset,
		Location:       location,
		SourceCategory: source.SourceCategory, Ledger: led, WAL: wal,
		SuppressRecursiveVL: source.SourceCategory == logtypes.SourceWorker || source.SourceCategory == logtypes.SourceNode,
		CapacityProvider:    m.capacityProvider,
		Sampling:            m.sampling,
		// 采集/投递侧的落库前置：有界等待；超时交办时必须阻止本批投递。
		DurablePersist: func() error {
			err := m.persistDurableBeforeDelivery()
			if err != nil {
				m.deferDurableDelivery(key)
			}
			return err
		},
		DeferredDelivery:        deferred,
		CommittedPersist:        committed,
		WALLimits:               m.walLimits,
		ReadLimits:              m.readLimits,
		WALReserve:              m.walReserve,
		MaxReplayEventsPerDrain: m.maxReplayEventsPerDrain,
		ReclaimProof:            func(events []logtypes.Event) error { return m.releaseRecovery(source, events) },
		ContextReclaimProof:     contextReclaim,
		ContextDelivery: func(ctx context.Context, events []logtypes.Event, replay bool) (pipeline.DeliveryResult, error) {
			result, err := m.deliverCtx(ctx, source, events, replay)
			if err != nil {
				m.deferDurableDelivery(key)
			}
			return result, err
		},
		// #56：追加被拒（暂停/上限）不再在本路径内同步整窗重投，改为**调度**一次路径外排空
		//（见 schedulePendingReplay：摄取路径内只登记待办 + 非阻塞唤醒）。
		AppendRejected: func() { m.schedulePendingReplay(key) },
		// 起点选择（2026-10-30 裁定 D）：
		//   - 游标表里有这个源 ⇒ ResumeCursor 生效，从上次位置续读（重启不重复投递）；
		//   - 没有（首次部署/换绑定）⇒ 从文件末尾起读，不回头吃别人的历史。
		StartAtEOF:   true,
		ResumeCursor: m.resumeCursorFor(key, source.Path),
	})
	if err != nil {
		return err
	}
	m.sources[key] = source
	m.pipes[key] = p
	// 管道已入表 ⇒ 账本可用 ⇒ 现在才能处置「重启续读跳过的未投递区间」。
	//
	// 为什么必须在这里而不是 resumeCursorFor 里（2026-11-05 第二次修正）：
	// resumeCursorFor 在 `pipeline.New` 的**参数**里被调用，那时 pipes[key] 还是 nil，
	// 拿不到账本，登记会静默失效。
	m.recordPendingResumeSkipGaps()
	// Pipeline换主即释放旧owner的回执与轻量索引，不能等下一次持久化才回收。
	delete(m.committedReceipts, key)
	// 归档导入已删除（2026-10-30）：归属闸与补账通道一并下线。
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
	// 源的持久状态必须**在登记时就有条**（2026-11-02 修）。
	//
	// 此前它只在第一次 `persist`/`appendEvents`/`writeProjectionPlan` 时才惰性建出来，
	// 于是“刚登记、还没写过任何东西”的窗口里 `m.state.Sources[key]` 不存在。
	// 而缺口消解路径的 `recordVerifiedRuns` 要求它存在（`saved, ok := ...; if !ok { return }`）
	// ⇒ 那个窗口里已确认可见的区间会被**静默丢掉**，verified runs 永不推进。
	// 实测（TestIngestSixFixEvidenceFailureAcrossDaysAndRecovery）：缺口已正常消解，
	// 但 `VerifiedRuns` 恒为空。
	if _, ok := m.state.Sources[key]; !ok {
		m.state.Sources[key] = persistedSource{}
	}
	// 持久化基线：账本刚由 Restore/WAL.Restore 填好，内存状态与索引内容一致，
	// 因此这里直接取当前修订号与段覆盖签名作为「已落库」的起点——否则启动后的首轮
	// persist 会把每个源都当成变更源重写一遍（对 13 源现场就是几 MB 的无效写入）。
	if p.Ledger() != nil && !recoveryDirty {
		if m.persistedRev == nil {
			m.persistedRev = make(map[string]uint64)
		}
		if m.persistedCover == nil {
			m.persistedCover = make(map[string]coverSignature)
		}
		m.persistedRev[key] = p.Ledger().Revision(p.Key())
		m.persistedCover[key] = coverSignatureOf(m.state.Sources[key])
	}
	if m.deferredDelivery {
		if backlog, _ := wal.Backlog(); backlog > 0 {
			m.schedulePendingReplay(key)
		}
	}
	return nil
}

// Start polls all configured sources until ctx is cancelled.
func (m *Manager) Start(ctx context.Context) {
	if m == nil {
		return
	}
	// 缺口消解 worker（见 gap_resolve_async.go）：与采集轮同生命周期的独立 goroutine。
	// 它把「VL 可见性核对」从投递路径挤出去——投递成功只做 O(1) 登记，
	// 核对该跑 30s 就跑 30s，不再拖住 WAL 回收链。
	if m.gapResolves != nil {
		if m.beginWork() {
			go m.runGapResolveWorker(ctx)
		}
	}
	// 容量自愈兜底：不依赖采集轮产生新批次（2026-10-03 现场：暂停后无人再评估 ⇒ 成批源停死）。
	go m.RunCapacityResumeLoop(ctx)
	m.syncRecoveryQuota() // 启动即同步一次（恢复期独立配额 ✓）
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
	m.stopMu.Lock()
	defer m.stopMu.Unlock()
	if m.stopComplete {
		return nil
	}
	// 关闭准入与WG.Add同序列化；取消后join所有真实调用，含旧sweep超时留下的后台调用。
	m.replayMu.Lock()
	m.workClosed = true
	m.replayMu.Unlock()
	m.mu.Lock()
	m.stopped = true
	m.mu.Unlock()
	// 先停启动恢复：它可能正在落库/写段，而本方法随后要 flush 并关索引句柄。
	// 取消即生效（驱动在每次让路与每步之间校验 ctx），故这里等待是有界的。
	if m.lifecycleCancel != nil {
		m.lifecycleCancel()
	}
	// 关停缺口消解调度器：不再接受新登记，并唤醒 worker 让它看到 closed 后退出。
	// 已在跑的任务由 ctx 取消（lifecycleCancel 已生效）⇒ workWG.Wait 有界。
	if m.gapResolves != nil {
		m.gapResolves.close()
	}
	m.workWG.Wait()
	m.mu.Lock()
	hints := m.writtenAttempts
	m.writtenAttempts = nil
	m.mu.Unlock()
	for _, hint := range hints {
		hint.pipe.WAL().ClearDeliveryMembers(hint.capture)
		if hint.earlier != nil {
			hint.pipe.WAL().ClearDeliveryMembers(hint.earlier.capture)
		}
	}
	if err := m.WaitStartupRecovery(context.Background()); err != nil {
		return err
	}
	if m.persistWorkerDone != nil {
		<-m.persistWorkerDone
	}
	w := m.observeCycleMu("stop")
	m.cycleMu.Lock()
	w.locked()
	defer func() {
		m.cycleMu.Unlock()
		w.done()
	}()
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
		p.RefreshDegrade(time.Now())
		flushCtx := context.Background()
		var flushOwner *managerDeliveryOwner
		if !m.deferredDelivery {
			// 独立Manager默认同步关停契约保持；此许可仅属于Stop当前栈，不重新开放准入。
			flushOwner = newManagerDeliveryOwner(m, p.Key().LogSourceID+"/"+p.Key().SourceGeneration)
			flushCtx = context.WithValue(flushCtx, managerDeliveryKey{}, flushOwner)
		}
		_, flushErr := p.FlushContext(flushCtx)
		if flushOwner != nil {
			flushOwner.active.Store(false)
		}
		if flushErr != nil {
			if m.deferredDelivery {
				return fmt.Errorf("ingest: 停止前本地冲刷未完成，保留句柄等待重试: %w", flushErr)
			}
			slog.Warn("独立同步组合关停冲刷未完成，保留WAL由重启恢复", "source", p.Key().String(), "error", flushErr)
		}
		if err := p.CloseArchiveReaders(); err != nil {
			slog.Warn("停止采集时关闭归档读取会话失败", "source", p.Key().String(), "error", err)
		}
	}
	// 关停前补写一次游标表：没有任何采集轮的进程（登记后立即停）也必须留下
	// 自己的源，否则下一次启动就“不知道采哪个文件”。
	//
	// 为什么走 OnStop 而不是 flushCursorRegistry：两者实现相同（同一个变更判据），
	// 但关停不属于周期节拍，它**只在确有变更时才写盘**——正常关停前采集轮已刷过，
	// 这里绝大多数时候是空操作（零磁盘 IO）。用独立的函数名把这件事写在调用点上。
	//
	// 调用位置：在全部管道 Flush 之后（此时 tailer.cursor 已推进到已处理字节）、
	// 在 persist 与 CloseIndex 之前（不需要索引参与，读写的是独立的 ingest.cursor）。
	// 锁状态：此处未持 m.mu（上面的 pipes 快照已释放），符合游标表的锁序纪律。
	m.flushCursorRegistryOnStop()
	// 关停落库：**有界尽力**（P0 复盘 2026-10-03 ✓）。
	//
	// 语义自检（改的是承诺的形状，不是数据安全 ✓）：
	//   · 旧承诺「**停服即已落库**」在巨量脏账下等价于"关停可能无限等" ✗ —— 现场实测 SIGTERM
	//     **200s / 30s 未退** ✗（编排/守护面只能强杀 ⇒ 反而更不干净 ✓）。
	//   · 新承诺「**有界尽力**」：给足宽限（默认 ``stopGrace``，见下 ✓），到时仍未落完即
	//     **降级退出**（WARN 不静默 ✓）；未落部分由**下次启动的恢复链 ＋ 常驻对账环**续做 ✓
	//     （既有机制 ✓，不引入新路径 ✗）⇒ 数据不丢、耐久性最终一致 ✓。
	//   · 仍未取消生命周期 ctx 的语义 ✓（这条注释原意保留 ✓）；只是把"无限"改成"有界" ✓。
	stopStarted := time.Now()
	if err := m.persistCtx(context.Background(), persistMode{priority: true, maxWait: stopGrace, failOnHandoff: m.deferredDelivery}); err != nil {
		return err
	}
	if elapsed := time.Since(stopStarted); elapsed >= stopGrace {
		// 不静默 ✓：超时即降级退出，且把"未落完"这件事显式告诉运维面 ✓。
		slog.Warn("关停落库超过有界宽限，已降级退出（未落部分由下次启动的恢复链/对账环续做）",
			"grace", stopGrace.String(), "elapsed", elapsed.String())
	}
	// 关闭索引句柄：显式把 WAL 归并回主库（见 stateindex.Store.Close）。
	//
	// 为什么必须在 Stop 里做（而不是留给进程退出）：演练实测不归并时 `-wal` 会残留到
	// 数百 MB——迁移的一次性大事务写完即有 453.6 MB，事后进程被 kill 就永久留在盘上。
	// 这里只有索引句柄关掉了，Manager 之后**不能再 persist**（这是 Stop 的语义）。
	if err := m.CloseIndex(); err != nil {
		return err
	}
	m.stopComplete = true
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

// persistGate 是持久化的「单执行者 + 显式 FIFO 队列」门：一次只有一个 goroutine 在构建与落库，
// 其余调用方**按到达顺序排队**（而不是各自再执行一遍；也不是裸 Broadcast + 抢锁）。
type persistGate struct {
	mu      sync.Mutex
	cond    *sync.Cond
	running bool
	// err 是最近一个已完成周期的结果，供等待者返回（不吞错误）。
	err error
	// priorityQueue / normalQueue 是显式 FIFO 等待队列（存的是 persistJoin 序号）。
	// 授予规则：优先队列非空 ⇒ 只授予其队首（恢复/重放链让位给用户面）；否则授予普通队首。
	priorityQueue []int64
	normalQueue   []int64
	// priorityStreak 是「连续授予优先队列」的计数，用于**老化**：达上限即强制让一个普通队首通过
	// （否则采集轮持续到达时，恢复/重放链会被无限压制 ✗ 反方向同样致命）。
	priorityStreak int
}

// enqueueLocked 把序号加入对应队列（调用方需持 g.mu）。
func (g *persistGate) enqueueLocked(seq int64, priority bool) {
	if priority {
		g.priorityQueue = append(g.priorityQueue, seq)
		return
	}
	g.normalQueue = append(g.normalQueue, seq)
}

// dequeueLocked 把序号从两条队列里摘除（幂等；调用方需持 g.mu）。
func (g *persistGate) dequeueLocked(seq int64) {
	g.priorityQueue = removePersistSeq(g.priorityQueue, seq)
	g.normalQueue = removePersistSeq(g.normalQueue, seq)
}

func removePersistSeq(queue []int64, seq int64) []int64 {
	for index, value := range queue {
		if value == seq {
			return append(queue[:index], queue[index+1:]...)
		}
	}
	return queue
}

// grantableLocked 判断该序号此刻能否取门：门空闲、自己是**队首**，且优先队列的「连胜」未到上限
// （老化：见 persistPriorityStreakMax —— 否则持续到达的采集轮会把恢复链饿死 ✗ 反方向也不行）。
//
// 注意：让路窗口**不在这里判**——若在这里判，刚交门的调用者会落进 `Cond.Wait`，而窗口到期时
// 没有任何人 Broadcast（无人唤醒 ⇒ 永久睡眠 ✗，2026-10-03 自伤实测：整包测试挂死 600s）。
// 让路由调用方**显式 sleep** 实现（见 persistCtx 的 not-oneStep 分支）。调用方需持 g.mu。
func (g *persistGate) grantableLocked(seq int64, priority bool, streakMax int) bool {
	if g.running {
		return false
	}
	if priority {
		// 优先：队首即可（恢复链让位）。但连续授予达上限且普通队列有人等时，强制让普通队首先走。
		if len(g.priorityQueue) == 0 || g.priorityQueue[0] != seq {
			return false
		}
		return !(streakMax > 0 && g.priorityStreak >= streakMax && len(g.normalQueue) > 0)
	}
	if len(g.priorityQueue) > 0 && !(streakMax > 0 && g.priorityStreak >= streakMax) {
		return false
	}
	return len(g.normalQueue) > 0 && g.normalQueue[0] == seq
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
	if !m.beginWork() {
		return
	}
	defer m.workWG.Done()
	// D 修复（2026-10-04，resolve-gaps 挂死专项）：cycleMu 只覆盖"读快照"段。
	// 现场（pprof 铁证）：原实现 cycleMu 覆盖整轮 ⇒ worker 们堵在 m.mu 的 33.8 万条 WAL
	// 序列化时，"work <- i / wg.Wait()"这段把 cycleMu 也占住 ⇒ 解算/登记/停止全挂。
	// 语义自检（父级确认）：cycleMu 只需保证"本轮工作集（读到的源列表）"与"改 pipes 的路径"
	// 不并发；worker 执行期放锁不改变本轮工作集（item 已定格），轮与轮间由 ticker 串行。
	w := m.observeCycleMu("pollSnapshot")
	m.cycleMu.Lock()
	w.locked()
	// 轮级校验预算：本轮内单源校验最多等到这个时刻 ⇒ 轮墙钟不被少数源拖成分钟级 ✓
	// （本轮跑完即清 0，非轮内路径（启动恢复/对账重发）不受影响，仍按各自窗口 ✓）。
	m.verifyRoundDeadline.Store(time.Now().Add(m.verifyRoundBudgetOf()).UnixNano())
	m.verifyRoundProgress.Store(0) // 本轮进展计数（见 verifyRoundProgress 注释）
	defer m.verifyRoundDeadline.Store(0)
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
	// D 修复：读快照完成即放 cycleMu —— 其后"归一 workers / 分发 work / wg.Wait / 汇总"都在锁外。
	m.cycleMu.Unlock()
	w.done()
	// 仅测试观测点：断言进入分发段时 cycleMu 已释放（见 pollDispatchHook 注释）。
	if hook, _ := m.pollDispatchHook.Load().(func()); hook != nil {
		hook()
	}
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
				// **每源独立校验预算**（per-source，2026-10-03 回退修复 ③）：本源处理期间其校验
				// 只受**自身**截止约束 ⇒ 慢源耗尽自身预算不再连坐同轮其他源（消队头阻塞）✓。
				if !m.deferredDelivery {
					m.setVerifySourceDeadline(item.source)
				}
				before, beforeOK := p.Positions()
				events, metadataChanged, err := m.pollSource(item.source, p)
				after, afterOK := p.Positions()
				if before != after || beforeOK != afterOK || len(events) > 0 || metadataChanged || err != nil {
					dirty[i] = true
				}
				// 缺陷 A 自愈：暂停源不再读取新数据，投递/回收/恢复评估整条链失去触发点；
				// 这里对「已暂停或仍持有未解决缺口」的源做一次限频自愈（见 selfHealPausedSource）。
				if m.deferredDelivery {
					if backlog, _ := p.WAL().Backlog(); backlog > 0 {
						m.schedulePendingReplay(item.source.LogSourceID + "/" + item.source.SourceGeneration)
					}
				} else if m.selfHealPausedSource(item.source, p) {
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
		// 采集轮**不为落库阻塞**：有界等待、超时交给后台持久化通道（见 persistHot 的说明）。
		m.persistPollTail()
	}
	// 游标落盘与「脏不脏」无关：一个空的采集轮同样可能因为轮转把 cursor 归零了，
	// 那正是最需要记住的时刻。flushCursorRegistry 内部再判一次有没有变化（无变化零磁盘 IO）。
	m.flushCursorRegistry()
}

// archiveScanCycleMuWait 是归档扫描等待采集轮锁（cycleMu）的墙钟预算。
//
// 为什么与 resolveGapsCycleMuWait 同量级：两者都是「调用方可以是后台/定时/交互任务，等不到就
// 明确失败更安全」的路径——现场该类路径的故障形态一致（排队 45–60s 后被调用方超时打断，而
// 服务端仍在排队）。5s 的选择依据见 resolveGapsCycleMuWait：远小于任何 RPC/HTTP 截止时间，
// 且大于采集轮读快照段（D 修复后 cycleMu 只覆盖快照）的期望持有时长。
//
// 为什么扫描场景不需要更长：定时扫描每 interval（现场 30s）触发一次，且**幂等**
// （已导入分段由 seg.Imported 跳过）。本轮拿不到锁就跳过，下一轮再来；拉长预算只会让
// "扫不动"这件事更晚被观测到。手工触发则由运维按需重试。
const archiveScanCycleMuWait = 5 * time.Second

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
	// ── 归档导入已整体删除（2026-10-30 用户裁定）────────────────────────────
	//
	// 用户原话：「把读取gz归档日志通通给我删了，只需要读取日志然后往vl塞，
	// 有就有没有就没有了」。
	//
	// 本方法保留签名（grpcsvc.IngestArchiveImporter 接口成员、前端有按钮），
	// 但已无可导入之物：返回 (0, 0, nil) 表示"没有归档可扫"，与过去
	// "目录里确实没有待导入归档" 的返回完全一致，调用方与前端无需改动。
	//
	// 为什么保留而不删接口：删接口要同时改 proto、grpcsvc、前端三处，
	// 而这里的行为差异对用户是"这个按钮点了没反应"，与"确实没有归档"无法区分——
	// 保留更小的变更面，且不会让老客户端报错。
	_ = storageNamespace
	_ = operator
	return 0, 0, nil
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

// ── 归档补账通道已删除（2026-10-30 用户裁定）──────────────────────────
//
// backfillArchiveUnderOwner / commitBackfill 原本服务于「归档命中了其它代次就记回它自己的账」。
// 归档整体不再读取，这条通道失去全部对象，一并删除。
// ─────────────────────────────────────────────────────────────────

// AbandonedSourceRange 是一段被人工裁定为永久丢失的源位置区间（查询面数据）。
type AbandonedSourceRange struct {
	LogSourceID      string
	SourceGeneration string
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
				LogSourceID: p.Key().LogSourceID, SourceGeneration: p.Key().SourceGeneration,
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

func (m *Manager) pollSource(source SourceConfig, p *pipeline.Pipeline) ([]logtypes.Event, bool, error) {
	// FR-504 应急停采（realtime_tail=false）：整源本轮不做任何读取/轮转/归档导入。
	//
	// 刻意在最前面整源返回，而不是只跳过 p.Poll()：轮转探测会触碰实例目录
	// （PrepareRotation / FlushClosedSegment 会在物理轮转时闭合段），而停采的语义是
	// 「只停读，不移动、不截断日志文件」。账本、WAL、游标与磁盘因此全部保持原样
	// ⇒ 恢复后由 FileTailer 维护的 cursor 断点续采，不丢位置、不产生缺口；
	// 实例进程与心跳（都在本方法之外）完全不受影响。
	if !m.AcqPolicySnapshot().RealtimeTail {
		return nil, false, nil
	}
	// 耐久门曾失败的静默源也需要重试；这里只按源限频登记，实际外发仍走既有单飞排空。
	m.retryDeferredDurableDelivery(source.LogSourceID+"/"+source.SourceGeneration, p)
	// 容量提供者可能执行磁盘查询，只在共享锁外刷新，覆盖本轮 Poll、导入与闭合输出。
	p.RefreshDegrade(time.Now())
	if source.Mode != pipeline.ModeFilePrimary || source.Path == "" {
		if stageTimingEnabled() {
			started := time.Now()
			events, err := p.Poll()
			m.recordRoundStage(source.LogSourceID, "poll", time.Since(started))
			return events, false, err
		}
		events, err := p.Poll()
		return events, false, err
	}
	// ── 归档采集已删除（2026-10-30 用户裁定）────────────────────────────
	//
	// 这里原先有三块：discoverSourceArchives 发现候选 / pendingArchives 过滤已导入 /
	// importArchivesWithBudget 导入。全部删除：用户要求「只读日志然后往 VL 塞，
	// 有就有没有就算了」——轮转后的 .gz 不再回读。
	//
	// 连带影响：
	//   · 不再有「新绑定先补历史段」的逻辑（那段也在归档链上）；
	//   · 轮转后不再等“轮转段可读”（那只服务于回读），直接继续读新文件。
	// ────────────────────────────────────────────────────────────
	var all []logtypes.Event
	metadataChanged := false

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

// ── 归档（gz 轮转文件）采集已按用户裁定整体删除（2026-10-30）──────────────────
//
// 用户原话：「把读取gz归档日志通通给我删了，只需要读取日志然后往vl塞，
// 有就有没有就没有了」
//
// 删除的东西：discoverSourceArchives / pendingArchives / importArchives /
// importArchivesWithBudget / Pipeline.ImportArchive / Pipeline.AssessArchive /
// acquire.ArchiveImporter 整套（约 2650 行主体 + 配套测试）。
//
// 留下的只有：tailer 读**当前活文件**（latest.log / proxy.log.0）→ 归一化 → 投 VL。
// 轮转后的历史文件不再回读：那属于“有就有、没有就算了”的用户明确取舍。
//
// 为什么这一刀必须整块切：归档导入与实时采集共用同一套位置账本与内容锚判定，
// 只要留一半，就会出现“发现说已导入、导入器却不看”的漂移，比直接不读更糟。
// ──────────────────────────────────────────────────────────────────

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

var (
	stageTimingOnce sync.Once
	stageTimingOn   bool
)

// recordRoundStage 记一次采集轮内的阶段耗时（仅 JIANMANAGER_STAGE_TIMING=1 时生效）。
//
// 为什么（2026-10-03 现场 Q4：未暂停源 readΔ 全 0，而栈上已无锁阻塞）：采集轮是"全源屏障"——
// 轮内固定 worker 逐源串行走「读 → WAL → 契约落库 → 投递 → 校验等待 → 发布 → 回收」，**少数源
// 卡在哪个阶段**决定了整轮墙钟，也决定其余源何时才轮到自己读 ✗。阶段打点让现场直接读出
// "时间去哪了"，不必再从 SIGQUIT 反推 ✓。
func (m *Manager) recordRoundStage(source string, stage string, elapsed time.Duration) {
	if m == nil || !stageTimingEnabled() {
		return
	}
	slog.Info("采集阶段耗时", "source", source, "stage", stage, "elapsedMs", elapsed.Milliseconds())
}

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
	// Events 是本批要追加进段存储的事件（快路径下即权威集合的增量尾部）。
	Events []logtypes.Event
}

// pipeFor 返回该源的采集管道（快照语义；调用方只读 Positions）。
func (m *Manager) pipeFor(key string) (*pipeline.Pipeline, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pipes[key]
	return p, ok
}

// 本批事件的实际 UTC 日才是代次数预算所属分区，不能只检查源登记日。
// ── #57：cycleMu 持有者观测仪（env 默认关；单变量观测件，零行为变更） ─────────────────────
//
// 动因（现场强证据）：resolve-gaps 探针 20/20 全忙、19/20 耗时 5.020–5.075s（±55ms）⇒ 每次都
// 吃满 5s 预算 ⇒ cycleMu **近乎连续被持有**（不是稀疏竞争窗 ✗✗）。现场锚
// `goroutine 3457 [sync.Mutex.Lock, 2 minutes]` 只能看见「有人在等」，上游持有者定不了名 ✗。
// 本仪器把「谁持有 / 多久 / 在哪 / 多密」变成可直接读出的三个面：
//  1. 慢持有 Warn 行（section + hold + slowTotal，独立节流窗）；
//  2. 当前持有者快照（section + goroutine id + heldFor）—— 探针可与 5.02s 同刻取用 ✓；
//  3. 取锁密度（滚动窗内每秒取锁次数）＋ 有界获取侧读数（等锁总时长/最长/尝试/超预算）。
//
// 零开销：默认关（env `JIANMANAGER_CYCLE_MU_PROFILE=1`，或 Options.CycleMuProfile）；
// 关闭时 observeCycleMu 返回 nil，且 watch 的所有方法对 nil 接收者安全 ⇒ 调用点只有一次判空。
const (
	// cycleMuSlowHoldThreshold 是「慢持有」阈值（100ms 起步，与 #55-A 的 m.mu 口径一致）。
	cycleMuSlowHoldThreshold = 100 * time.Millisecond
	// cycleMuWarnMinInterval 是慢持有告警的**独立**节流窗（不与其他仪器共享 ⇒ 互不压制）。
	cycleMuWarnMinInterval = 5 * time.Second
	// cycleMuDensityWindow 是取锁密度的滚动窗（每秒取锁次数按该窗计算）。
	cycleMuDensityWindow = 5 * time.Second
)

var (
	cycleMuProfileOnce sync.Once
	cycleMuProfileOn   bool
)

// cycleMuProfileEnabled 读观测仪开关（同 JIANMANAGER_STAGE_TIMING 家族：显式 env、默认关）。
func cycleMuProfileEnabled() bool {
	cycleMuProfileOnce.Do(func() { cycleMuProfileOn = os.Getenv("JIANMANAGER_CYCLE_MU_PROFILE") == "1" })
	return cycleMuProfileOn
}

// goroutineID 返回当前 goroutine 的数字 id（**仅观测仪开启时调用** ⇒ 生产默认零开销）。
//
// Go 不公开 goroutine id：这里取 runtime.Stack 首行（goroutine 123 [running]:），与常见诊断工具
// 同法。现场要的正是「这个字段能与 pprof 栈里的 goroutine 编号对上」。
func goroutineID() uint64 {
	var buf [64]byte
	line := string(buf[:runtime.Stack(buf[:], false)])
	const prefix = "goroutine "
	if !strings.HasPrefix(line, prefix) {
		return 0
	}
	line = line[len(prefix):]
	space := strings.IndexByte(line, ' ')
	if space < 0 {
		return 0
	}
	id, err := strconv.ParseUint(line[:space], 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// cycleMuHolderInfo 是「当前持有者」快照（零值 = 未持有 / 观测未开）。
type cycleMuHolderInfo struct {
	section   string
	goroutine uint64
	at        time.Time
	held      bool
}

// cycleMuWatch 是一次 cycleMu 持有观测：observeCycleMu 在**取锁前**创建，locked() 在**取到锁后**
// 立即调用，done() 在**放锁后**调用（与 #55-A 的 watchMutexWait/acquired 同型）。
type cycleMuWatch struct {
	m       *Manager
	section string
	at      time.Time
	waited  time.Duration
}

// observeCycleMu 创建一次观测（未启用 ⇒ nil；watch 的所有方法对 nil 接收者安全）。
func (m *Manager) observeCycleMu(section string) *cycleMuWatch {
	if m == nil || !m.cycleMuProfile {
		return nil
	}
	return &cycleMuWatch{m: m, section: section, at: time.Now()}
}

// locked 在取到 cycleMu **之后**立即调用：登记当前持有者、累计取锁密度、起表。
// 锁内只写普通字段（cycleMuMu 保护）—— 零 atomic、零日志 ✓。
func (w *cycleMuWatch) locked() {
	if w == nil {
		return
	}
	w.waited = time.Since(w.at)
	w.at = time.Now()
	goid := goroutineID() // 先算（runtime.Stack 有成本）⇒ 叶子锁只包字段写入
	w.m.cycleMuMu.Lock()
	w.m.cycleMuHolder = cycleMuHolderInfo{section: w.section, goroutine: goid, at: w.at, held: true}
	w.m.cycleMuHolderWatch = w
	w.m.noteCycleMuDensityLocked(w.at)
	w.m.cycleMuMu.Unlock()
}

// noteCycleMuDensityLocked 在滚动窗内累计「取锁次数」（**调用方必须持 cycleMuMu**）。
// 判读：现场「cycleMu 近乎连续被持有」应表现为**每秒取锁次数很低**（每次持有很久）＋ 慢持有行密集。
func (m *Manager) noteCycleMuDensityLocked(now time.Time) {
	if m.cycleMuWindowStart.IsZero() {
		m.cycleMuWindowStart = now
	} else if elapsed := now.Sub(m.cycleMuWindowStart); elapsed >= cycleMuDensityWindow {
		m.cycleMuRatePerSec = float64(m.cycleMuWindowAcqs) / elapsed.Seconds()
		m.cycleMuWindowStart = now
		m.cycleMuWindowAcqs = 0
	}
	m.cycleMuWindowAcqs++
}

// done 在**放锁之后**调用：结算持有、清空自己登记的持有者记录、慢持有上报（独立节流窗）。
func (w *cycleMuWatch) done() {
	if w == nil {
		return
	}
	hold := time.Since(w.at)
	w.m.cycleMuMu.Lock()
	// 只清「自己登记的那条」：解锁与 done() 之间可能已有人取到锁并登记了新持有者（不可误清 ✗）。
	if w.m.cycleMuHolderWatch == w {
		w.m.cycleMuHolder = cycleMuHolderInfo{}
		w.m.cycleMuHolderWatch = nil
	}
	w.m.cycleMuMu.Unlock()
	if hold < cycleMuSlowHoldThreshold {
		return
	}
	w.m.cycleMuSlowHoldCount.Add(1)
	last := w.m.cycleMuWarnAt.Load()
	now := time.Now().UnixNano()
	if last != 0 && time.Duration(now-last) < cycleMuWarnMinInterval {
		return
	}
	if w.m.cycleMuWarnAt.CompareAndSwap(last, now) {
		slog.Warn("cycleMu 慢持有", "section", w.section, "hold", hold.String(), "slowTotal", w.m.cycleMuSlowHoldCount.Load())
	}
}

// noteCycleMuWait 记一次有界获取的等锁时长（#57；关闭观测时立即返回 ⇒ 零开销）。
// 现场对照：探针 19/20 次恰好 5.020–5.075s ⇒ 本读数应呈现「近似恒等于预算」的形态。
func (m *Manager) noteCycleMuWait(d time.Duration, acquired bool) {
	if m == nil || !m.cycleMuProfile {
		return
	}
	m.cycleMuWaitTotal.Add(int64(d))
	m.cycleMuWaitAttempts.Add(1)
	if !acquired {
		m.cycleMuWaitTimeouts.Add(1)
	}
	if int64(d) > m.cycleMuWaitMax.Load() {
		m.cycleMuWaitMax.Store(int64(d))
	}
}

// CycleMuProfileEnabled 报告 cycleMu 观测仪是否开启（探针同刻取用；未开 ⇒ 其余读数无意义）。
func (m *Manager) CycleMuProfileEnabled() bool { return m != nil && m.cycleMuProfile }

// CycleMuSlowHoldCount 返回慢持有累计次数（阈值 cycleMuSlowHoldThreshold）。
func (m *Manager) CycleMuSlowHoldCount() int64 {
	if m == nil {
		return 0
	}
	return m.cycleMuSlowHoldCount.Load()
}

// CycleMuHolderSnapshot 返回「当前持有者」快照：section、goroutine id、已持有时长、是否持有。
// 探针可在每次 5s 超时**同刻**取一次 ⇒「谁持有」当场定点（现场锚 goroutine 3457 的答案面）。
func (m *Manager) CycleMuHolderSnapshot() (string, uint64, time.Duration, bool) {
	if m == nil {
		return "", 0, 0, false
	}
	m.cycleMuMu.Lock()
	defer m.cycleMuMu.Unlock()
	h := m.cycleMuHolder
	if !h.held {
		return "", 0, 0, false
	}
	return h.section, h.goroutine, time.Since(h.at), true
}

// CycleMuDensityPerSec 返回滚动窗内「每秒取锁次数」（含当前未完窗口的即时值；未启用 ⇒ 0）。
func (m *Manager) CycleMuDensityPerSec() float64 {
	if m == nil {
		return 0
	}
	m.cycleMuMu.Lock()
	defer m.cycleMuMu.Unlock()
	if m.cycleMuWindowStart.IsZero() {
		return 0
	}
	if elapsed := time.Since(m.cycleMuWindowStart); elapsed >= cycleMuDensityWindow {
		return m.cycleMuRatePerSec // 窗已满但尚无新取锁：用上一个已关窗读数
	} else if elapsed > 0 {
		return float64(m.cycleMuWindowAcqs) / elapsed.Seconds()
	}
	return 0
}

// CycleMuWaitStats 返回有界获取侧读数：等锁累计、最长单次等待、尝试次数、超预算/被取消次数。
func (m *Manager) CycleMuWaitStats() (total, max time.Duration, attempts, timeouts int64) {
	if m == nil {
		return 0, 0, 0, 0
	}
	return time.Duration(m.cycleMuWaitTotal.Load()), time.Duration(m.cycleMuWaitMax.Load()),
		m.cycleMuWaitAttempts.Load(), m.cycleMuWaitTimeouts.Load()
}

// mutexHoldSlowThreshold 是"慢持锁/慢等待"告警阈值（候选 2 测量，2026-10-04；#55-A 沿用）：
// 仅在关键临界区（投递规划/校验凭据/落段标记）上计时，超阈计数并按节流窗口告警。
const mutexHoldSlowThreshold = 100 * time.Millisecond

// mutexHoldWarnMinInterval 是慢持锁/慢等待告警节流窗口（避免高频投递路径告警风暴；计数仍逐次累加）。
const mutexHoldWarnMinInterval = 5 * time.Second

// mutexHoldWatch 是一次 m.mu 观测（候选 2；#55-A 起拆成「等锁」与「纯持有」两段）。
//
// 用法（两段式，热点处）：
//
//	w := m.watchMutexWait("X") // 起点：等锁
//	m.mu.Lock()
//	w.acquired()               // Lock 成功后：结算等待、重新起表（此后只计纯持有）
//	...
//	m.mu.Unlock()
//	w.done()                   // Unlock 之后：结算纯持有（等待与持有各自计数/告警）
//
// 兼容：watchMutexHold(section) 等价于"没有等待段"（起表即持有），供不必拆分的调用点使用。
type mutexHoldWatch struct {
	m       *Manager
	section string
	at      time.Time     // 持有起表时刻
	waitAt  time.Time     // 等锁起表时刻（零值 ⇒ 本 watch 不计等待）
	waited  time.Duration // 已结算的等锁时长
}

// watchMutexHold 开始一次持锁计时（零语义改动：不改变任何分支/返回值，仅观测）。
func (m *Manager) watchMutexHold(section string) *mutexHoldWatch {
	return &mutexHoldWatch{m: m, section: section, at: time.Now()}
}

// watchMutexWait 开始一次「等锁 + 持有」两段计时（#55-A，零语义改动）：调用方必须紧接
// `m.mu.Lock()`，并在 Lock 成功后调用 acquired()、Unlock 之后调用 done()。
//
// 为什么必须拆（现场 2026-10-05）：起表在 Lock **之前** ⇒ 读数混入等待，9–19s 的慢段
// 无法区分"本函数自己持锁 9–19s"与"被别的长持锁者挡在外面 9–19s"，收窄方向因此不唯一 ✗。
func (m *Manager) watchMutexWait(section string) *mutexHoldWatch {
	now := time.Now()
	return &mutexHoldWatch{m: m, section: section, at: now, waitAt: now}
}

// acquired 在 `m.mu.Lock()` **成功之后**立即调用：结算等待时长并重新起表（此后只计纯持有）。
//
// 注意：它是在**持锁内**执行的 ⇒ 只写本结构体字段，不做任何 atomic 计数、不打日志
// （避免把观测自身的开销算进持有）。
func (w *mutexHoldWatch) acquired() {
	if w == nil {
		return
	}
	if !w.waitAt.IsZero() {
		w.waited = time.Since(w.waitAt)
		w.waitAt = time.Time{}
	}
	w.at = time.Now()
}

// done 结束计时：等待与持有**各自**判定超阈、各自计数、各自一行 Warn（含各自节流窗口）。
func (w *mutexHoldWatch) done() {
	if w == nil || w.m == nil {
		return
	}
	// 等待（被挡住）：独立计数与告警行 —— 与"持有"混算正是 #55-A 要修的口径缺陷。
	if w.waited >= mutexHoldSlowThreshold {
		w.m.noteSlowSectionLock(messageSlowMutexWait, w.section, "wait", w.waited,
			&w.m.mutexWaitSlowCount, &w.m.mutexWaitWarnAt)
	}
	// 纯持有（Lock 后 → Unlock 后）：语义与 #55-A 之前逐字一致（阈值/计数/告警字段名不变）。
	hold := time.Since(w.at)
	if hold < mutexHoldSlowThreshold {
		return
	}
	w.m.noteSlowSectionLock(messageSlowMutexHold, w.section, "hold", hold,
		&w.m.mutexHoldSlowCount, &w.m.mutexHoldWarnAt)
}

// 两条慢段告警的固定文案（各一行：慢等待 / 慢持锁）。
const (
	messageSlowMutexHold = "m.mu 慢持锁"
	messageSlowMutexWait = "m.mu 慢等待"
)

// noteSlowSectionLock 记一次"超阈慢段"：计数 +1（逐次累加），距上次告警超过节流窗口时打一条
// Warn（含累计数）。等待与持有各传自己的计数器与节流时间戳 ⇒ 一方告警不压另一方。
//
// 命名带 Lock 后缀提醒：调用方必须已在**锁外**（本函数会写日志，锁内打日志会放大持有时长）。
func (m *Manager) noteSlowSectionLock(message, section, field string, d time.Duration, counter, warnAt *atomic.Int64) {
	counter.Add(1)
	last := warnAt.Load()
	now := time.Now().UnixNano()
	if last != 0 && time.Duration(now-last) < mutexHoldWarnMinInterval {
		return
	}
	if warnAt.CompareAndSwap(last, now) {
		slog.Warn(message, "section", section, field, d.String(), "slowTotal", counter.Load())
	}
}

// MutexHoldSlowCount 返回慢持锁（**纯持有**，#55-A 起不含等待）累计次数；供测试与诊断。
func (m *Manager) MutexHoldSlowCount() int64 {
	if m == nil {
		return 0
	}
	return m.mutexHoldSlowCount.Load()
}

// MutexWaitSlowCount 返回慢等待（被挡在 m.mu 外面的时长超阈）累计次数（#55-A 新增读数）：
// 与 MutexHoldSlowCount 一起把"自身持有"与"被挡住"分开，收窄方向才唯一。
func (m *Manager) MutexWaitSlowCount() int64 {
	if m == nil {
		return 0
	}
	return m.mutexWaitSlowCount.Load()
}

// deliveryPlanFailure 是「本批规范化 + 去重」的失败形态：零宽事件 / 规范化失败。
// 不可裁定身份冲突改为隔离记录并跳过变体，不再阻断整批投递。
type deliveryPlanFailure struct {
	result pipeline.DeliveryResult
	err    error
}

// eventIdentity 是投递/重建两面共用的身份对账项：规范哈希 + 记录区间 + 正文字节数。
// size/区间用于「同 ID 异哈希」的确定性裁定（2026-10-05 #179 现场：同区间、残行 947 B
// vs 重读完整行 1034 B ✓）；idx≥0 指该事件在本批 toWrite 中的下标（−1 表示来自既有存储
// 或内联集合，不在本批 ✗ —— 裁定取"后到者"时需按此决定替换还是追加 ✓）。
type eventIdentity struct {
	hash   string
	when   string
	start  uint64
	end    uint64
	size   int
	msg    string
	level  string
	stream string
	idx    int
}

// rememberStoredCanonicalIdentity 与权威恢复共用较长者胜/时间更正裁定，不让遗留段的末行覆盖胜者。
func rememberStoredCanonicalIdentity(seen map[string]eventIdentity, event logtypes.Event) {
	if existing, exists := seen[event.EventID]; exists {
		if existing.hash == event.CanonicalHash {
			return
		}
		_, takeIncoming := adjudicateSameIntervalIdentity(existing, event, len(event.Message))
		if !takeIncoming {
			return
		}
	}
	seen[event.EventID] = eventIdentity{hash: event.CanonicalHash, when: event.EventTimeUTC,
		start: event.Record.Start, end: event.Record.End, size: len(event.Message),
		msg: event.Message, level: event.Level, stream: event.Stream, idx: -1}
}

// identityConflictRecord 保存不可裁定的同 ID 异哈希变体，供恢复、查询质量与运维诊断使用。
type identityConflictRecord struct {
	EventID     string `json:"event_id"`
	Start       uint64 `json:"start"`
	End         uint64 `json:"end"`
	KeptHash    string `json:"kept_hash"`
	DroppedHash string `json:"dropped_hash"`
	ObservedAt  string `json:"observed_at"`
}

const maxIdentityConflictRecords = 256

// identityConflictClearance 是「人工确认并清空身份冲突」的**审计凭据**。
//
// 为什么必须有它（本功能最容易被做错的地方）：身份冲突记录是「该源存在同 ID 异内容」的
// 唯一凭据，而质量标记（planner 的 DupConflict/StatsPartial）正是由它派生。清空记录等于让
// 查询面**不再提示**这份歧义——若不留痕，这条路径就成了「掩盖数据歧义」的后门。
//
// 为什么不能照抄 GapAbandonment 的落点：缺口放弃凭据挂在账本条目上，而账本条目有 revision
// （Ledger.Revision）可做「期间是否被改过」的守卫；身份冲突记录直接挂在 persistedSource 上，
// **没有等价 revision**。故本凭据只能与它所描述的数据**同处一地**（同一个 payload），
// 用「清空后的记录集 + 本条凭据」这一对事实共同构成可核验的凭据：
// 后来者读到 cleared_count 与 cleared_records 就能知道「曾经有哪些冲突、被谁在何时以何理由清掉」。
type identityConflictClearance struct {
	// Operator 是做出确认的认证主体（为空一律拒绝执行，见 ClearIdentityConflicts）。
	Operator string `json:"operator"`
	// Reason 是操作人给出的清空理由（为空的请求同样被拒）。
	Reason string `json:"reason"`
	// AtUTC 是清空时刻（RFC3339Nano）。
	AtUTC string `json:"at_utc"`
	// ClearedCount 是本次真正清掉的记录条数。**恒等于** len(ClearedRecords) 与
	// 实际被移除条目数中的较小者不成立时以本条为准：明细有上限（见
	// maxClearedConflictDetails），故条数必须单独记，绝不用明细长度代替。
	ClearedCount int `json:"cleared_count"`
	// ClearedRecords 是被清空记录的**明细快照**（有界，最多 maxClearedConflictDetails 条）。
	//
	// 为什么要留明细而不是只留条数：清空之后这批记录就从活动数据里消失了，若只留条数，
	// 「当初保留的是哪个版本（kept_hash）」这一最关键的裁定证据就永久不可核验。留明细使
	// 清空成为**可回溯的归档**而不是抹除。
	ClearedRecords []identityConflictRecord `json:"cleared_records,omitempty"`
}

// maxIdentityConflictClearances 是每源保留的凭据条数上限（保留最近若干次）。
//
// 有界的理由：本路径是人工入口，正常运维下次数极少；无界增长会把 payload 撑大并进入每轮
// 落库的字节指纹计算。**如实说明其代价**：超过上限的最久远凭据会被裁掉，故它保证的是
// 「近期操作可回溯」而非「永久不可删」——这与缺口放弃凭据（必须比缺口活得久）的强度不同，
// 强度差异源于本操作**不回退任何查询语义**（只去掉一个提示标记），不构成数据丢失通道。
const maxIdentityConflictClearances = 32

// maxClearedConflictDetails 是单条凭据里保留的被清记录明细上限。
const maxClearedConflictDetails = 64
