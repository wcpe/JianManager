package ingest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 本文件是「校验预算起点后移」（2026-10-04，父级裁定 b'）的确定性红/绿：
//
// 现场：暂停源持有 47 万未发布事件（237 簇）；单源处理链（全量重写 + 等待）远超单源预算，
// 而预算起点停在"源处理开头"⇒ 轮到校验时预算早已耗尽 ⇒ 每轮只查得动约 1 簇、其余 defer
// ⇒ 整批永不通过 ⇒ 不 publish ⇒ 下轮重写同一批（"events 恒定 × generation 递增"的自锁循环）。
//
// 绿（现状）：`verifyProjection` **入口重置**本批预算（`setVerifySourceDeadline`）⇒ 即使
// "源处理链已耗尽上一轮预算"，本批仍拥有完整预算 ⇒ 校验成功（nil）；
// 红（变异：删除入口重置）⇒ 预算（含保底已用）仍处耗尽态 ⇒ 返回 ErrVerifyDeferred。
func TestVerifyBudgetResetsAtVerifyEntry(t *testing.T) {
	client, _ := newProjectionVL(t)
	m := newVerifyTestManager(10*time.Millisecond, 50*time.Millisecond, 5*time.Second)
	m.verifyRoundBudget = 150 * time.Millisecond // 单源预算注入小值（走真实限额路径）
	src := SourceConfig{LogSourceID: "inst:1/file", SourceGeneration: "g1"}
	ev := verifyTestEvent()

	// 1) 复刻 verify 的真实前置：先 insert（mock VL 的 query 才能命中）
	if _, err := m.insertInBatches(client, []logtypes.Event{ev}, "projection-1"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// 2) 模拟"源处理链已把预算耗尽"：起点=now、且本源已推进过（保底已用 ⇒ 耗尽即 defer）
	m.setVerifySourceDeadline(src)
	st := m.verifySourceState(src)
	st.progressed.Store(1)
	time.Sleep(250 * time.Millisecond) // > 150ms 预算

	// 3) 断言：入口重置 ⇒ 本批拥有完整预算 ⇒ 校验成功
	err := m.verifyProjection(client, src, "projection-1", []logtypes.Event{ev})
	require.NoError(t, err,
		"预算起点后移后本批校验应拥有完整预算（红证=入口不重置 ⇒ 沿用已耗尽预算 ⇒ ErrVerifyDeferred）")
	require.Equal(t, int64(0), m.verifyDeferred.Load(), "本批不得计入 defer")
}

// 变异记录（2026-10-04 实测，逐字还原）：
//   - 变异：删除 `verifyProjection` 入口的 `m.setVerifySourceDeadline(source)` ⇒ 本用例转红
//     （ErrVerifyDeferred：沿用已耗尽且保底已用的预算）；还原后转绿。
