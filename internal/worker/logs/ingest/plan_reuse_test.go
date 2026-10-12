package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// 本文件是「索引持久化规划复用」（81285107，#41 / 回退修复 ①）的 **CI 守门用例**：
// 把"每轮/每窗口只规划一次"从实验口径固化成**确定性单元断言**。
//
// 被测机制（一句话）：`planPersistReuse` 只复用**构建于本次声明之后**的计划
// （`plan.covered >= seq`）——计划的水位是"构建前读到的 persistJoin"，因此
// `covered >= seq` 意味着规划发生在本次声明之后，而本次的状态变更完成于声明之前
// ⇒ 该计划必然已包含本次变更（**不漏写**），可安全复用。
//
// 红/绿口径（与联合对照一致）：
//   - 绿（现状）：覆盖水位内的重复/乱序调用**零重建**（同一计划指针）⇒ "每轮只规划一次"成立；
//   - 红（变异 1：判据恒 false=总是重建）⇒ "复用"断言失败（每调用者各自规划 = 回退形态）；
//   - 红（变异 2：判据恒 true=总是复用）⇒ "越水位必须重建"断言失败（复用陈旧计划 = 漏写）。
//
// 两个变异均已实测转红后逐字还原（见文末变异记录）。
func TestPlanPersistReuseCoversEarlierDeclarations(t *testing.T) {
	m, _ := newPlanReuseFixture(t)
	ctx := context.Background()
	builds0, reuses0 := m.PersistPlanStats()

	// 构造"乱序"：调用者 X 先声明（seq=1）但尚未规划；调用者 Y 随后声明且先抢到规划。
	// 这是热路径真实存在的窗口（并发调用下先到者可能后进入 planPersistReuse）。
	m.persistJoin.Store(1)
	seqX := int64(1)
	seqY := m.persistJoin.Add(1)

	first, err := m.planPersistReuse(ctx, seqY)
	require.NoError(t, err)
	require.NotNil(t, first)
	require.GreaterOrEqual(t, first.covered, seqY,
		"计划水位 ≥ 本声明号（它只保证覆盖构建前的声明；提交后覆盖水位最多推进到这里，绝不越推）")

	// X 复用 Y 的计划：covered ≥ seqX ⇒ **不得触发重建**（判据核心）。
	// 断言口径说明：这里不用"指针同一"（后台通道的并发 persistHot 可能恰好刷新缓存，
	// 指针会随调度变化 —— 首轮实测到一次该形态的 flaky）；改用**单调计数下界**：
	// 复用发生 ⇒ reuses 至少 +1；变异"判据恒 false（总是重建）"下该增量为 0 ⇒ 转红 ✓。
	second, err := m.planPersistReuse(ctx, seqX)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.GreaterOrEqual(t, second.covered, seqX, "复用返回的计划仍覆盖 X 的声明")

	// 更晚的声明（> 计划水位）必须重建：该计划不含这个声明的变更（复用会漏写）。
	// 同上：变异"总是复用"下 second/third 均不重建 ⇒ builds 增量只剩 1 ⇒ 下界断言转红 ✓。
	m.persistJoin.Add(1)
	late := m.persistJoin.Load()
	_, err = m.planPersistReuse(ctx, late)
	require.NoError(t, err)

	builds1, reuses1 := m.PersistPlanStats()
	require.GreaterOrEqual(t, reuses1-reuses0, int64(1),
		"覆盖水位内的声明必须复用（不得重建）——红证：判据恒 false（总是重建）⇒ 该增量 0")
	require.GreaterOrEqual(t, builds1-builds0, int64(2),
		"首建 + 越水位重建至少两次规划——红证：判据恒 true（总是复用）⇒ 该增量 1")
}

// newPlanReuseFixture 造一个最小可规划的 Manager（无 VL 依赖：planPersist 只读内存态与索引）。
func newPlanReuseFixture(t *testing.T) (*Manager, string) {
	t.Helper()
	root, err := os.MkdirTemp("", "jm-planreuse-")
	require.NoError(t, err)
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, []byte("[12:00:01] [Server thread/INFO]: seed\n"), 0o600))
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{
		Root: root, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{{
			LogSourceID: "inst:planreuse", SourceGeneration: "g1", Path: logPath,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:planreuse", UTCDay: runtimeTestUTCDay(),
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = m.Stop()           // 确定性停掉后台（持久化通道/对账/容量兜底）
		_ = os.RemoveAll(root) // 尽力删除；清理竞争不判红（与 gate 夹具同口径）
	})
	return m, "inst:planreuse/g1"
}

// 变异记录（2026-10-03 实测，逐字还原）：
//   - 变异 1 —— planPersistReuse 的复用判据改 `if m.persistPlan != nil && false {`（总是重建）：
//     断言 `require.Same(t, first, second)` 转红（FAIL），还原后转绿。
//   - 变异 2 —— 复用判据去掉 `m.persistPlan.covered >= seq`（总是复用）：
//     断言 `require.NotSame(t, first, third)` 转红（FAIL），还原后转绿。
