package ingest

// 「缺口不健康标记」的红绿用例（2026-10-10）。
//
// 现场背景：生产 65 个源持有 215 条未决缺口，其中 WAL_BODY_MISSING（64 条）与
// ROTATED_SEGMENT_NOT_READY（3 条）**不在任何恢复白名单里** ⇒ 永远无自动出路，
// 而日志里它们与其他"还在等滞回"的源长得一模一样（都只印「积压已回落但暂不能恢复采集」），
// 外部无从区分「卡死」与「等待」✗。
//
// 本标记把这一区别变成显式读数：**未决缺口全部不可分类** ⇒ 判为永久阻塞并点名。

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// TestUnresolvedGapBlockedSources_FlagsNonClassifiableOnly —— 白名单外缺口 ⇒ 判永久阻塞。
//
// 转红方式（实测）：把 `d.Blocking != d.Unresolved` 判据删掉（任何有缺口的源都列入）
// ⇒ 下方"可分类缺口不应列入"的断言转红；反之把整个函数体短路成 return nil ⇒ 本条转红。
func TestUnresolvedGapBlockedSources_FlagsNonClassifiableOnly(t *testing.T) {
	client, _, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source},
	})
	require.NoError(t, err)

	key := source.LogSourceID + "/" + source.SourceGeneration
	m.mu.Lock()
	pipe := m.pipes[key]
	m.mu.Unlock()
	require.NotNil(t, pipe, "夹具应已建管道")

	led := pipe.Ledger()
	ledKey := pipe.Key()

	// 起点：无缺口 ⇒ 不列入。
	require.Empty(t, m.UnresolvedGapBlockedSources(), "无缺口的源不得被列入 ✓")

	// ① 白名单**内**的缺口（APPEND_REJECTED 可分类）⇒ 不列入（避免把"正在自愈"误报成"卡死"）。
	require.NoError(t, led.RecordGap(ledKey, 100, 200, ledger.GapReasonAppendRejected, "test"))
	require.Empty(t, m.UnresolvedGapBlockedSources(),
		"可分类缺口仍有自愈出路 ⇒ 不得判为永久阻塞 ✓")

	// ② 追加白名单**外**的缺口（WAL_BODY_MISSING）⇒ 此时"全部不可分类"不成立（还有一条可分类）⇒ 仍不列入。
	require.NoError(t, led.RecordGap(ledKey, 300, 400, "WAL_BODY_MISSING", "test"))
	require.Empty(t, m.UnresolvedGapBlockedSources(),
		"只要还有一条可分类缺口 ⇒ 仍有出路，不得列入（判据不得放宽）✓")
}

// TestUnresolvedGapBlockedSources_AllNonClassifiableIsBlocked —— 全部不可分类 ⇒ 判永久阻塞。
func TestUnresolvedGapBlockedSources_AllNonClassifiableIsBlocked(t *testing.T) {
	client, _, _ := newReconcileVL(t)
	root := t.TempDir()
	journal := catalog.NewMemJournal()
	source := reconcileTestSource(t, root)
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(journal), Journal: journal,
		Sources: []SourceConfig{source},
	})
	require.NoError(t, err)

	key := source.LogSourceID + "/" + source.SourceGeneration
	m.mu.Lock()
	pipe := m.pipes[key]
	m.mu.Unlock()
	require.NotNil(t, pipe)
	led := pipe.Ledger()
	ledKey := pipe.Key()

	// 只有白名单外的缺口 ⇒ 永久阻塞。
	require.NoError(t, led.RecordGap(ledKey, 300, 400, "WAL_BODY_MISSING", "test"))

	blocked := m.UnresolvedGapBlockedSources()
	require.Len(t, blocked, 1, "全部未决缺口均不可分类 ⇒ 应判为永久阻塞并列入 ✓")
	require.Equal(t, source.LogSourceID, blocked[0].LogSourceID)
	require.Equal(t, 1, blocked[0].Blocking)
	require.Equal(t, 1, blocked[0].Unresolved)

	// 打点行必须**点名**（外部据此区分"卡死"与"等待"）。
	lines := m.GapBlockedDiagnosticLines()
	require.Len(t, lines, 1)
	require.Contains(t, lines[0], source.LogSourceID)
	require.Contains(t, lines[0], "WAL_BODY_MISSING",
		"打点须带出阻塞原因（运维据此判断该走人工裁定还是重置）✓")
}
