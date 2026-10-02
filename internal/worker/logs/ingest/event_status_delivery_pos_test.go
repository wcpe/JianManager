package ingest

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// TestEventStatusFactsDeliveryPosIsReadSideDerived（用户 2026-10-05 批准的**常驻判别** ✓）：
// facts 的 `DeliveryPos` **必须等于账本已算好的 `Positions.Delivery`** ✓
// —— 两处构造点共用同一口径 ✓，谁也不许"另存一份 / 另算一套" ✗。
//
// 为什么（本轮教训）：`Delivery` 是 `contiguousDeliveryEnd(delivery_batch, reclaim)` 的**派生值**
// （Restore 每次重算 ✓）；把它落库/落 aux 会造成**第二真值** ✗（本次会话反复踩的"两本账" ✗）。
//
// 转红方式（实测两条方向，各自独立红 ✓）：
//   - 变异①：让 facts 恒给 0（= 只补一处构造点的形态 ✗）⇒ 红；
//   - 变异②：让 facts 自己另算一个值（如 Delivery+1 ⇒ "另算一套" ✗）⇒ 红。
func TestEventStatusFactsDeliveryPosIsReadSideDerived(t *testing.T) {
	key := ledger.SourceKey{LogSourceID: "inst:156/file", SourceGeneration: "g1"}
	// 用账本 API 造条目（**不依赖 Positions 的类型名** ✓：改快照字段即可 ✓）。
	l := ledger.New()
	l.Ensure(key, logtypes.SourceIdentity{
		LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, ParserVersion: "v1",
	})
	snap := l.Get(key)
	snap.Positions.Read = 99_776_039
	snap.Positions.Durable = 99_776_039
	snap.Positions.Reclaim = 68_528_090
	snap.Positions.Delivery = 68_528_090 // 账本已算好的值（Restore 派生 ✓）
	saved := persistedSource{Ledger: []ledger.Entry{*snap}}
	facts := eventStatusFactsOfPersisted(saved)
	require.Equal(t, uint64(68_528_090), facts.DeliveryPos,
		"facts.DeliveryPos 必须取账本现值（不得恒 0 ✗、不得另算一套 ✗）")
	require.Equal(t, saved.Ledger[0].Positions.Delivery, facts.DeliveryPos,
		"判别式：两处构造点必须与账本同源同口径 ✓")
}
