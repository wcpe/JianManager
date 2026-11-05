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
