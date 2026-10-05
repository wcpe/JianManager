package acquire

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// #66-③ 回归：采集热路径的状态判据必须走 O(1) 轻量读（Ledger.AcquireState / Ledger.Positions），
// 不得深克隆整条条目（Get 的克隆成本随「批次数」线性；现场批次数可达十万级 ⇒ 旧实现里
// 每次 Append 付两发克隆、每次回收/恢复评估再各一发，合计为单跳 ~1s 的组成之一，见 #65 归因 ✗）。
//
// 判别法（成本对比 + 比值断言，抗机器抖动）：
//   - 造一条「十万级批次数」的重条目（走 Restore 的克隆路径，避免逐条 RecordDelivery 的裁剪开销）；
//   - 同场测 Get（深克隆，ms 级）与 AcquireState（O(1)，µs 级）的单次耗时；
//   - 再测 Append 单次耗时：修后应远小于 Get。
//
// 变异（在 Append 热路径换回哪怕一次 Get）⇒ Append 成本跃至 Get 量级 ⇒ 比值断言转红 ✓。
func TestAcquireHotPathsUseLightState(t *testing.T) {
	key := testKey("src-light-state", "g1")
	led := ledger.New()

	// 十万级批次数：Restore 内部会 clone 每条 Entry（与现场"重条目"同形 ✓）。
	const batches = 200_000
	require.NoError(t, led.Restore([]ledger.Entry{{
		Key: key,
		Identity: logtypes.SourceIdentity{
			LogSourceID:      key.LogSourceID,
			SourceGeneration: key.SourceGeneration,
			ParserVersion:    "acquire-v1",
		},
		DeliveryBatches: make([]ledger.DeliveryBatch, batches),
	}}))

	wal := NewWAL(led, key)
	wal.SetLimits(1<<30, 0) // 不触发暂停，保证 Append 走正常路径
	evs := buildWALEvents("src-light-state", "g1", 0, 1, "line")

	avg := func(n int, f func()) time.Duration {
		start := time.Now()
		for i := 0; i < n; i++ {
			f()
		}
		return time.Since(start) / time.Duration(n)
	}

	dGet := avg(10, func() { _ = led.Get(key) })
	dState := avg(1000, func() { _, _ = led.AcquireState(key) })
	dAppend := avg(50, func() { _ = wal.Append(evs...) })

	// 主断言：热路径（Append）不得付出深克隆的代价。
	require.Less(t, dAppend, dGet/2,
		"Append 热路径不得深克隆：dAppend=%v 应远小于 dGet=%v（#66-③）", dAppend, dGet)
	// 对照断言：轻量读与深克隆的数量级差必须存在（否则本用例失去判别力）。
	require.Greater(t, dGet, 10*dState,
		"Get 深克隆应显著贵于 AcquireState：dGet=%v dState=%v", dGet, dState)
}
