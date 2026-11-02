package ingest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 本文件钉住「历史欠账记录必须能拿到自己的可见性证据」这条契约
// （2026-11-02 真机缺陷，本次长稳发散的**最终根因**）。
//
// 事故形态（单源 15 行/s × 150s 受控实验 + 离线取证）：
//
//	投影写入结束 354 次，全部 status=200 verified=N   ← 写入一直成功
//	投影校验查询 694 次，投影校验完成 0 次             ← 成功路径从不产出区间
//	verifiedRanges 长期只有 1 个                       ← 记账漏了
//	closed 停在 150418，而文件已 497879                ← 被欠账拖住
//
// 铁证：被判为欠账的记录 `69716..69933` 在 VL 里**已存在 3 份**
// （projection-3 / projection-15 / projection-17）——数据一份没丢。
//
// 成因（runtime.go 的 debt 分支）：
//
//	旧记录 A 从未进 verified
//	  → debt 恒为真
//	  → 每轮走 debt 分支、登记的是**本次新写入的 B**，不是 A
//	  → A 依然没登记 ⇒ 下一轮 debt 依然为真 ⇒ **A 永远等不到自己的区间**
//
// 修复：`hasKnownUnpublishedDayDebt` 返回「被卡住的那条记录」，
// debt 分支对它单独做一次可见性核对；通过则登记它的区间，下一轮它就不再算欠账。

// TestDayDebtReturnsBlockingRecord 钉住判据能指出「是谁卡住的」。
//
// 为什么重要：只回答 true/false 时，调用方无法为那条记录补登记证据——
// 这正是「同一批旧记录被反复判欠账、却永远拿不到 verified」的机制。
//
// 转红方式（实测）：把返回值改回只给 bool（第二个返回值恒为 nil）⇒ 本用例必红。
func TestDayDebtReturnsBlockingRecord(t *testing.T) {
	source, root := gapVisibility_TestSource(t)
	day := source.UTCDay

	events := gapVisibilityEvents(source, day, [][2]uint64{{0, 100}, {100, 200}}, "debt")
	client, _, _ := newReconcileVL(t)
	journal := catalog.NewMemJournal()
	cat := catalog.New(journal)

	m, err := New(Options{
		Root: root, VL: client, Catalog: cat, Journal: journal,
		Sources: []SourceConfig{source},
	})
	require.NoError(t, err)

	key := source.LogSourceID + "/" + source.SourceGeneration
	pipe, ok := m.pipeFor(key)
	require.True(t, ok, "必须有 pipeline 才能有 WAL 条目")

	// 造「已 durable、但从未进入 verified」的同日 WAL 条目（= 生产里那条旧记录 A）。
	//
	// **关键夹具**：本次写入面（`written`）必须**不覆盖**这条 WAL 条目，
	// 否则判据会走 `CoveredByPositionRanges(written, ...)` 那个 continue 分支直接跳过它。
	// 生产形态正是如此：A 是**旧的**历史批次，而本次投递的是**新尾**。
	oldEvents := events
	for _, ev := range oldEvents {
		pipe.WAL().Append(ev)
	}
	pipe.WAL().Commit()

	// 本次写入面只给出一段**新尾**（位置远在旧记录之后），不覆盖旧记录。
	tail := gapVisibilityEvents(source, day, [][2]uint64{{1000, 1100}}, "newtail")

	// verified 保持为空 ⇒ 旧记录必然被判欠账。
	debt, blocking, err := m.hasKnownUnpublishedDayDebt(context.Background(), source, tail, nil, true)
	require.NoError(t, err)
	require.True(t, debt,
		"夹具前提：WAL 里有同日、未进入 verified、且不被本次写入面覆盖的旧记录 ⇒ 必须判为欠账")
	require.NotNil(t, blocking,
		"判为欠账时必须交出**具体是哪条记录**卡住的；只给 bool 会让调用方无从为它补登记证据"+
			"——那正是 closed 被旧记录永久拖住的成因")
	require.NotZero(t, blocking.Record.End, "交出的记录必须带真实位置区间")
	require.Equal(t, oldEvents[0].Record.End, blocking.Record.End,
		"交出的必须是那条**旧记录**（而不是本次新尾）")
}

// TestDayDebtNilBlockingWhenNoDebt 钉住反方向：无欠账时不得凭空给出一条记录。
//
// 转红方式：无论有无欠账都返回同一个非 nil 指针 ⇒ 本用例必红。
func TestDayDebtNilBlockingWhenNoDebt(t *testing.T) {
	source, root := gapVisibility_TestSource(t)
	client, _, _ := newReconcileVL(t)
	journal := catalog.NewMemJournal()
	cat := catalog.New(journal)

	m, err := New(Options{
		Root: root, VL: client, Catalog: cat, Journal: journal,
		Sources: []SourceConfig{source},
	})
	require.NoError(t, err)

	// WAL 为空 ⇒ 必然无欠账。
	debt, blocking, err := m.hasKnownUnpublishedDayDebt(context.Background(), source, nil, nil, true)
	require.NoError(t, err)
	require.False(t, debt)
	require.Nil(t, blocking, "无欠账时不得返回任何记录（否则调用方会为不存在的欠账做核对）")
}

// 确保 logtypes 导入被使用。
var _ = logtypes.Event{}
