package ingest

// 本文件是从 runtime.go 拆出的**启动恢复与 VL 就绪**面（阶段 3 架构整理，2026-10-26）。
//
// 拆分依据：这些方法构成一条完整的语义链——启动时的源恢复、VL 就绪等待与降级登记、
// 回放预算、恢复完成/失败的收口。它们与采集热路径（投递/投影/持久化）不同：
// 只在启动窗口与恢复态被触发，不参与稳态的每轮流转。
//
// 拆分纪律（必须保持）：
//   - 同包拆文件，不改变任何导出 API、不改可见性、不改行为。
//   - 这些方法仍直接访问 m.mu 等私有字段——同包内合法，无需导出。
//   - 跨文件调用是正常的：本文件的方法被 runtime.go 的启动路径调用，反之亦然。

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

func (m *Manager) lifecycleContext() context.Context {
	if m == nil || m.lifecycleCtx == nil {
		return context.Background()
	}
	return m.lifecycleCtx
}

// startStartupRecovery 启动启动恢复驱动：background=true 时在后台 goroutine 里跑
// （生产语义：New 不阻塞），否则就地跑完（保持既有调用方的边界）。
// 错误语义按模式分工（同一条驱动，只有错误处置不同）：
//   - 同步模式：**保持既有契约**——任一步失败即让 New 失败（调用方由此得知"启动投影/回收责任
//     没落定"，走既有重试与告警面）；这正是既有回归守住的行为。
//   - 后台模式（生产）：单源失败**不致命**——记入就绪原因并继续其余源，就绪面照实报 not-ready
//     （`startup_recovery_failed` + 逐源原因）。理由：一个源的重发失败不该让整节点的实例管理、
//     采集与其它源的采集都起不来；「未就绪」已由就绪面显式表达，不比让 New 失败更弱。
func (m *Manager) startStartupRecovery(items []startupSource, background bool) error {
	m.mu.Lock()
	done := make(chan struct{})
	m.startupDone = done
	if len(items) == 0 {
		m.startupRecovery = startupRecoveryState{}
		m.mu.Unlock()
		close(done)
		return nil
	}
	m.startupRecovery = startupRecoveryState{InProgress: true, Total: len(items)}
	m.mu.Unlock()
	if background {
		go func() {
			defer close(done)
			// 后台模式的错误不外抛（没有调用方在等）：失败已记入原因并经就绪面表达。
			// asyncReplay=true：生产语义——重发登记给重建 worker（不占采集主线，见 runStartupRecovery）。
			_ = m.runStartupRecovery(m.lifecycleContext(), items, false, true)
		}()
		return nil
	}
	err := m.runStartupRecovery(m.lifecycleContext(), items, true, false)
	close(done)
	return err
}

// replayBudgetOf 归一化重发预算（零值 → 默认）。
func replayBudgetOf(budget ReplayBudget) ReplayBudget {
	if budget.MaxDays <= 0 {
		budget.MaxDays = defaultReplayMaxDaysPerRound
	}
	if budget.MaxDuration <= 0 {
		budget.MaxDuration = defaultReplayMaxDuration
	}
	return budget
}

// ensureVLReady 在对账/重发之前探测该源的 VL 写入端是否就绪；不就绪即返回错误（调用方**不得**
// 发起对账与重发，只能记错误等下一轮）。
//
// 日志策略：**只在状态变化时打一条**（就绪 → 未就绪、未就绪 → 就绪），否则每轮每源一条会把现场
// 日志淹掉，而这类「反复未就绪」恰恰是排障时最需要看清的信号。
func (m *Manager) ensureVLReady(ctx context.Context, source SourceConfig) error {
	key := source.LogSourceID + "/" + source.SourceGeneration
	client, frozen, err := m.clientForSource(source)
	if err != nil {
		m.noteVLNotReady(key, err)
		return err
	}
	if frozen {
		err := fmt.Errorf("ingest: VL 写入端已冻结（cutover 进行中），不发起对账/重发")
		m.noteVLNotReady(key, err)
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, m.vlProbeTimeout)
	defer cancel()
	if err := client.Health(probeCtx); err != nil {
		wrapped := fmt.Errorf("ingest: VL 未就绪（%s）: %w", err.Error(), err)
		m.noteVLNotReady(key, wrapped)
		return wrapped
	}
	m.noteVLReady(key)
	return nil
}

func (m *Manager) noteVLNotReady(key string, err error) {
	message := err.Error()
	m.mu.Lock()
	_, existed := m.vlNotReady[key]
	m.vlNotReady[key] = message
	m.mu.Unlock()
	if !existed {
		slog.Warn("VL 未就绪：本轮不发起对账/重发（等下一轮，避免必然失败的整窗重发）",
			"source", key, "error", message)
	}
}

func (m *Manager) noteVLReady(key string) {
	m.mu.Lock()
	_, existed := m.vlNotReady[key]
	delete(m.vlNotReady, key)
	m.mu.Unlock()
	if existed {
		slog.Info("VL 已就绪：恢复对账/重发", "source", key)
	}
}

// StartupRecoveryStatus 返回启动恢复的后台进度（观测面；CP 的运维接口与排障据此判断
// 「平台为什么还没就绪」）。
func (m *Manager) StartupRecoveryStatus() startupRecoveryState {
	if m == nil {
		return startupRecoveryState{}
	}
	m.mu.Lock()
	status := m.startupRecovery
	status.Reasons = append([]string(nil), m.startupRecovery.Reasons...)
	status.ReplaySkipReasons = append([]string(nil), m.startupRecovery.ReplaySkipReasons...)
	m.mu.Unlock()
	// 「已登记、尚无结论」的重发实时取自调度器（不缓存在本结构里，避免两份状态分叉）。
	status.ReplaysInFlight = m.startupReplayInFlight()
	return status
}

// WaitStartupRecovery 等待启动恢复结束（含被取消）。ctx 取消即返回 ctx.Err()。
//
// 为什么需要它：恢复语义（「启动后多久，账本/投影/回收责任才落定」）此前由 New 的同步返回
// 隐式表达；移到后台后必须有**显式的等待点**，否则测试与调用方无从确定边界。
// 生产不需要等（这正是本修复的目的），故 main 不调用它。
func (m *Manager) WaitStartupRecovery(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	done := m.startupDone
	m.mu.Unlock()
	if done == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// runStartupRecovery 是启动恢复的后台驱动（FR-497 的三阶段，顺序与语义不变）：
// ① 按批读回权威集合（可取消、有界内存）；② 批内并发对账（网络）；③ 逐源重发 + 落库。
//
// 与改动前的两处行为差异，都是 2026-10-02 事故的直接对策：
//   - **不阻塞启动**：整段脱离 New，worker 立即可达（本方法的失败只影响就绪面，不再让
//     Manager 创建失败——改动前单源重发失败会导致整个采集运行时起不来）；
//   - **单源失败不致命**：记入 Reasons 并继续其余源（改动前直接 return err）。
//
// 2026-10-10 第二项修复（**重发移出本驱动**）：`asyncReplay=true`（= 生产，对应
// `StartupRecoveryBackground`）时，阶段③不再在本 goroutine 上跑「重发本身」——
// 它只做 O(天数) 的登记（见 startup_replay.go 的完整论证），重发由重建 worker 按
// **有界重试 + 退避 + 放弃**执行。驱动因此能在秒级内跑完全部批次并给出恢复结论，
// 而就绪面同时把「重发仍在路上」显式报出来（`startup_replay_in_flight`）。
//
// `asyncReplay=false`（同步模式）时阶段③**逐字保持**既有内联重发循环（有界趟数 +
// VL 等待窗口）：这条路径的契约是「返回即恢复完成」，调用方依赖它。
func (m *Manager) runStartupRecovery(ctx context.Context, items []startupSource, failFast, asyncReplay bool) error {
	allReports := make([]ReconcileReport, 0, len(items))
	for start := 0; start < len(items); start += startupRecoveryBatch {
		if err := ctx.Err(); err != nil {
			m.finishStartupRecovery()
			return err
		}
		end := start + startupRecoveryBatch
		if end > len(items) {
			end = len(items)
		}
		loaded := make([]startupSource, 0, end-start)
		for _, item := range items[start:end] {
			if err := ctx.Err(); err != nil {
				m.finishStartupRecovery()
				return err
			}
			snapshot := m.capturePublicationSnapshot(item.source, ctx)
			m.mu.Lock()
			saved := m.state.Sources[item.key]
			m.mu.Unlock()
			events, err := m.canonicalRecoveryEvents(item.key, saved)
			if err != nil {
				if failFast {
					m.finishStartupRecovery()
					return err
				}
				m.noteStartupRecoveryFailure(item.key, err)
				continue
			}
			if len(events) == 0 {
				continue
			}
			loaded = append(loaded, startupSource{source: item.source, key: item.key, events: events, snapshot: snapshot})
		}
		if len(loaded) == 0 {
			continue
		}
		reports := m.reconcileStartup(ctx, loaded)
		allReports = append(allReports, reports...)
		// 增量可见：排障中途也能看到「对账已经跑了哪些源」。
		//
		// **O(n²) 修正（2026-10-10）**：`allReports` 是**累计**集，而这里在批次循环里每批都调一次
		// ——若"记读数"与"打日志"合在一起（原实现），第 k 批会把前 k-1 批全部重打一遍，
		// 总日志量 = O(批数²)。生产现场 5 个启动窗口打出 4×1526 + 1×3359 条、同一
		// `(源, 缺失天)` 组合反复出现，正是这个放大（它同时淹没了"第一次是谁触发的"这一判据）。
		// 现在拆成两步：读数记**累计**（语义不变），日志只打**本批新增**（每份报告恰好一行）。
		m.recordReconcileReports(allReports)
		m.noteReconcileReports(reports)
		if asyncReplay {
			// 生产路径：只登记，不等重发（见本函数头）。阈值是 O(批次内天数)，与事件量无关。
			summary := m.scheduleStartupReplays(loaded, reports)
			m.noteStartupReplaySummary(summary)
			continue
		}
		if err := m.runStartupReplayInline(ctx, loaded, reports, failFast); err != nil {
			m.finishStartupRecovery()
			return err
		}
	}
	m.finishStartupRecovery()
	return nil
}

// runStartupReplayInline 是**同步模式**的启动恢复重发（逐字保留改动前的内联循环）。
//
// 为什么保留：它的契约是「返回即恢复完成」，既有回归与需要确定性边界的调用方依赖它；
// 生产不走这里（`apps/worker/main.go` 传 `StartupRecoveryBackground: true`）。
//
// 语义（与改动前一致，未做任何"顺手优化"）：
//   - 重发按**天**切片推进（见 replayWindow）：一轮没做完的源留在这里，下一趟继续；
//   - 趟数与等待上界分开计：只有「本趟有进展」才消耗趟数预算；纯等待（VL 未就绪）按固定
//     间隔轮询，并受 `vlReadyWait` 的墙钟上界约束——否则「等 VL 起来」会被趟数上限提前
//     掐断，而且记录下来的原因会退化成一句没用的「趟数用尽」；
//   - 任一步失败按 failFast 决定「让 New 失败」还是「记原因继续」。
func (m *Manager) runStartupReplayInline(ctx context.Context, loaded []startupSource, reports []ReconcileReport, failFast bool) error {
	type pendingReplay struct {
		item   startupSource
		report ReconcileReport
		window *replayWindow
		// vlWaitSince 记录「因 VL 未就绪而等待」的起点，用于给等待设上界。
		vlWaitSince time.Time
		// lastErr 是最近一次真实失败原因（用于「趟数/等待用尽」时如实报出原因，而不是一句
		// 「趟数用尽」——现场排障靠的就是这句话）。
		lastErr error
	}
	pending := make([]pendingReplay, 0, len(loaded))
	for index, item := range loaded {
		pending = append(pending, pendingReplay{item: item, report: reports[index]})
	}
	// liveWindows 收拢「本驱动当前还活着、还没到终态的窗」。
	//
	// 为什么必须显式收拢（而不是靠 window 被 GC）：代次预留只由 releaseProjectionGeneration
	// 释放，GC 不会代劳 ⇒ 任何「窗不再被续跑却没有释放」的出口都会把该代次名钉死到进程结束，
	// 该源此后每次取名都无谓进位。本驱动有 4 个放弃出口（趟数用尽 / ctx 取消 / failFast 提前
	// 返回 / 单源非致命失败），逐个手写释放必然漏一个；改成「登记 + 出口统一释放」后，
	// 新增出口天然不再需要记得释放。
	liveWindows := make(map[string]*replayWindow, len(loaded))
	forgetWindow := func(key string, w *replayWindow) {
		if w == nil {
			return
		}
		if cur, ok := liveWindows[key]; ok && cur == w {
			m.releaseReplayWindow(w)
			delete(liveWindows, key)
		}
	}
	defer func() {
		for key, w := range liveWindows {
			m.releaseReplayWindow(w)
			delete(liveWindows, key)
		}
	}()
	passes := 0
	waitStarted := time.Now()
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		if passes > maxStartupReplayPasses || time.Since(waitStarted) > m.vlReadyWait+time.Minute {
			for _, entry := range pending {
				cause := entry.lastErr
				if cause == nil {
					cause = fmt.Errorf("重发在 %d 趟内未完成", maxStartupReplayPasses)
				}
				remaining := []string(nil)
				if entry.window != nil {
					remaining = entry.window.days
				}
				forgetWindow(entry.item.key, entry.window)
				m.noteStartupRecoveryFailure(entry.item.key,
					fmt.Errorf("重发未完成（剩余天 %v）: %w", remaining, cause))
			}
			break
		}
		progressed := false
		next := make([]pendingReplay, 0, len(pending))
		for _, entry := range pending {
			if err := ctx.Err(); err != nil {
				return err // 出口统一释放所有仍活着的窗（见上面的 defer）
			}
			daysBefore := 0
			if entry.window != nil {
				daysBefore = len(entry.window.days)
			}
			done, window, err := m.applyStartupRecovery(ctx, entry.item, entry.report, entry.window)
			entry.window = window
			// 登记本源的活窗（供放弃出口与函数出口统一释放；同一个 key 的旧窗已被 apply
			// 那边释放或续用，这里以最新窗口为准）。
			if window != nil {
				liveWindows[entry.item.key] = window
			}
			if window != nil && len(window.days) < daysBefore {
				progressed = true // 本趟至少完成了一天（真进展）
			}
			if err != nil {
				entry.lastErr = err
				// VL 未就绪不是「源恢复失败」：它是可等待的外部条件（现场触发因就是它），
				// 在有界窗口内保留在 pending 里下趟再试；超出窗口才记失败。
				if m.vlNotReadyFor(entry.item.key) {
					if entry.vlWaitSince.IsZero() {
						entry.vlWaitSince = time.Now()
					}
					if time.Since(entry.vlWaitSince) < m.vlReadyWait {
						next = append(next, entry)
						continue
					}
					// 等待窗口用尽：**仍不致命**（两种模式一致）——VL 未就绪是外部条件，
					// 该源的缺失天会被运行期常驻对账重新发现并重发（见 reconcile_loop）；
					// 就绪面 meanwhile 如实报 not-ready。把外部条件当「源恢复失败」去中断
					// 整个启动恢复，正是 2026-10-02 现场把 worker 拖死的形态。
					// 本流在此放弃该源 ⇒ 窗不再续跑 ⇒ 释放其代次预留（防钉死）。
					forgetWindow(entry.item.key, entry.window)
					m.noteStartupRecoveryFailure(entry.item.key, err)
					continue
				}
				if failFast {
					return fmt.Errorf("ingest: 启动恢复重发失败（源 %s）: %w", entry.item.key, err)
				}
				// 非致命失败：同样放弃该源本轮的窗（缺失天由常驻对账重新登记）⇒ 释放预留。
				forgetWindow(entry.item.key, entry.window)
				m.noteStartupRecoveryFailure(entry.item.key, err)
				continue
			}
			entry.vlWaitSince = time.Time{}
			if !done {
				// 预算内没做完：已发布的天已完成，剩余天下一趟续跑（不推进回收责任）。
				// 窗仍活着 ⇒ 预留继续由它持有（下一趟用同一代次，正是预留的目的）。
				entry.lastErr = nil
				next = append(next, entry)
				continue
			}
			// 整窗完成：applyStartupRecovery 已在它自己的出口释放了预留 ⇒ 这里只注销登记
			//（forgetWindow 对已释放的句柄是空操作：句柄被置 nil 后 release 直接返回）。
			forgetWindow(entry.item.key, entry.window)
			m.noteStartupRecoveryCompleted()
		}
		pending = next
		if progressed {
			passes++
			// 让路：本趟已让出持久化门，这里再给采集轮一个完整的调度机会。
			time.Sleep(persistStepYield)
			continue
		}
		// 本趟零进展（典型：VL 未就绪）：按固定间隔轮询，把 CPU 让给采集轮，避免空转烧核。
		time.Sleep(vlReadyRetryInterval)
	}
	return nil
}

func (m *Manager) noteStartupRecoveryCompleted() {
	m.mu.Lock()
	m.startupRecovery.Completed++
	m.mu.Unlock()
}

func (m *Manager) noteStartupRecoveryFailure(key string, err error) {
	// **延期不是失败**（用户 2026-10-05 现场铁证 ✓）：`ErrVerifyDeferred`（[runtime.go:4020] ✓）
	// 是设计内的正常让路 ✓，却与本行**共用无条件 ERROR** ✗ ⇒ 现场 `inst:142/143` 在新二进制上仍持续
	// 打 ERROR（08:22:49 ✓）。**只对延期降级、其余照旧 ERROR** ✗（一刀切会把真问题淹没 ✓）。
	level, msg := startupRecoveryFailureLevel(err)
	slog.Log(context.Background(), level, msg, "source", key, "error", err)
	m.mu.Lock()
	m.startupRecovery.Failed++
	if len(m.startupRecovery.Reasons) < maxStartupRecoveryReasons {
		m.startupRecovery.Reasons = append(m.startupRecovery.Reasons, key+":"+err.Error())
	}
	m.mu.Unlock()
}

func (m *Manager) finishStartupRecovery() {
	m.mu.Lock()
	m.startupRecovery.InProgress = false
	m.mu.Unlock()
}

// maxStartupRecoveryReasons 是有界原因条数（避免逐源刷屏；总量由 Failed 计数表达）。
const maxStartupRecoveryReasons = 8

// vlReadyRetryInterval 是「等 VL 就绪」时的轮询间隔（零进展的趟之间）。
//
// 为什么需要它：等待期间若只做 5ms 让路，等待窗口（defaultVLReadyWait）内的趟数会把 CPU 空烧
// 掉（现场 2.67 核的形态之一）；固定 2s 间隔既让 CPU 让给采集轮，又能在 VL 起来后 2s 内接上。
const vlReadyRetryInterval = 2 * time.Second

// maxStartupReplayPasses 是单个源重发的最大推进趟数（每趟受 ReplayBudget 约束）。
//
// 它是有界性的最后一层保险：预算保证「每趟有限」，本常量保证「趟数有限」——否则一个永远做不完的
// 超大源会把启动恢复的循环一直占着（而它每一趟都只是在等外部条件）。
const maxStartupReplayPasses = 64

// vlNotReadyFor 报告该源当前是否被判为「VL 未就绪」（由 ensureVLReady 维护，状态变化才打日志）。
func (m *Manager) vlNotReadyFor(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, notReady := m.vlNotReady[key]
	return notReady
}
