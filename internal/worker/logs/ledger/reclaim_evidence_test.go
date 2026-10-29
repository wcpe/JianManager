package ledger

// reclaim 上界的证据口径回归（2026-10-29）。
//
// 背景：reclaim 改按投递水位推进时，第一版实现把 `Positions.Delivery` 直接当回收上界。
// **这是数据丢失级缺陷**：Delivery 的语义是「所有字节均有请求结果」，而请求结果**包括 UNKNOWN**
// —— acquire.recordHTTPResultLocked 把「响应丢失 / 非 2xx」也登记成批次（为了可审计）。
// 拿它当上界就等于把「发出去了但不知道落没落库」的数据从内存队列里删掉。
//
// 本文件钉死三件事：
//   1. UNKNOWN 区间 **不** 能被回收（reclaim 停在它之前）；
//   2. REQUEST_DONE 区间能被回收，且停在下一个空洞前；
//   3. 相邻事件之间空出的 1 字节（行分隔符）不算空洞。
//
// 转红方式（变异验证）：把 tryReclaimLocked 的 `contiguousCertifiedEnd(e)` 换回
// `e.Positions.Delivery` ⇒ 用例 1 立即红（reclaim 会越过 UNKNOWN 的 100）。

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func reclaimKey(t *testing.T) (*Ledger, SourceKey) {
	t.Helper()
	led := New()
	key := SourceKey{LogSourceID: "reclaim-evidence", SourceGeneration: "g1"}
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "reclaim-evidence", SourceGeneration: "g1"})
	return led, key
}

// UNKNOWN（响应丢失 / 非 2xx）不得成为回收依据。
func TestReclaimDoesNotCrossUnknownDelivery(t *testing.T) {
	led, key := reclaimKey(t)
	require.NoError(t, led.AdvanceRead(key, 200))
	require.NoError(t, led.AdvanceDurable(key, 200))

	// 0..100 真成功、100..200 请求失败（UNKNOWN）。
	require.NoError(t, led.RecordDelivery(key, 0, 100, logtypes.DeliveryRequestDone))
	require.NoError(t, led.RecordDelivery(key, 100, 200, logtypes.DeliveryUnknown))

	// Delivery 会推到 200（「所有字节均有请求结果」），但 reclaim 必须停在 100。
	pos, ok := led.Positions(key)
	require.True(t, ok)
	require.EqualValues(t, 200, pos.Delivery, "Delivery 的语义是请求结果（含 UNKNOWN），应推到 200")

	reclaim, err := led.TryReclaim(key)
	require.NoError(t, err)
	require.EqualValues(t, 100, reclaim,
		"reclaim 不得越过 UNKNOWN 区间：那是「发出去了但不知落没落库」的数据，删了就真丢了")
}

// REQUEST_DONE 覆盖能被回收，并在真实空洞前停住。
func TestReclaimStopsAtRealHole(t *testing.T) {
	led, key := reclaimKey(t)
	require.NoError(t, led.AdvanceRead(key, 300))
	require.NoError(t, led.AdvanceDurable(key, 300))

	// [0,100) 与 [150,300) 成功；[100,150) 从未投递（真实空洞 ≥2 字节）。
	require.NoError(t, led.RecordDelivery(key, 0, 100, logtypes.DeliveryRequestDone))
	require.NoError(t, led.RecordDelivery(key, 150, 300, logtypes.DeliveryRequestDone))

	reclaim, err := led.TryReclaim(key)
	require.NoError(t, err)
	require.EqualValues(t, 100, reclaim, "回收必须在真实空洞 [100,150) 之前停住")
}

// 相邻事件之间那 1 字节行分隔符不算空洞。
func TestReclaimToleratesSingleByteSeparator(t *testing.T) {
	led, key := reclaimKey(t)
	require.NoError(t, led.AdvanceRead(key, 201))
	require.NoError(t, led.AdvanceDurable(key, 201))

	// [0,100) 与 [101,201)：中间空出 1 字节（换行符）。
	require.NoError(t, led.RecordDelivery(key, 0, 100, logtypes.DeliveryRequestDone))
	require.NoError(t, led.RecordDelivery(key, 101, 201, logtypes.DeliveryRequestDone))

	reclaim, err := led.TryReclaim(key)
	require.NoError(t, err)
	require.EqualValues(t, 201, reclaim,
		"相隔 1 字节（分隔符）必须视为相接，否则回收永远以每行 1 字节步进")
}

// 回收上界同时受已读入量限制：已确认投递但尚未读入的部分不得回收。
func TestReclaimClampedByReadFrontier(t *testing.T) {
	led, key := reclaimKey(t)
	require.NoError(t, led.AdvanceRead(key, 120))
	require.NoError(t, led.AdvanceDurable(key, 120))
	require.NoError(t, led.RecordDelivery(key, 0, 200, logtypes.DeliveryRequestDone))

	reclaim, err := led.TryReclaim(key)
	require.NoError(t, err)
	require.EqualValues(t, 120, reclaim, "回收上界不得越过 max(Read, Durable)")
}

// Delivery 不再以 Reclaim 为 floor：回收推进后重算不应让 Delivery 倒退。
//
// 这是那个「推进依据被裁剪吃掉」缺陷的直接回归：旧写法 floor=Reclaim 时，
// 回收推进到 200 后重算会得到 contiguousDeliveryEnd(batches, 200)，一旦批次被裁就退成 200。
func TestDeliveryDoesNotRegressAfterReclaimAdvances(t *testing.T) {
	led, key := reclaimKey(t)
	require.NoError(t, led.AdvanceRead(key, 500))
	require.NoError(t, led.AdvanceDurable(key, 500))
	require.NoError(t, led.RecordDelivery(key, 0, 500, logtypes.DeliveryRequestDone))

	require.EqualValues(t, 500, func() uint64 { p, _ := led.Positions(key); return p.Delivery }())
	_, err := led.TryReclaim(key)
	require.NoError(t, err)

	// 再登记一批（模拟后续投递），重算后 Delivery 不得低于 500。
	require.NoError(t, led.RecordDelivery(key, 500, 600, logtypes.DeliveryRequestDone))
	pos, ok := led.Positions(key)
	require.True(t, ok)
	require.GreaterOrEqual(t, pos.Delivery, uint64(500), "Delivery 单调不减，不因回收推进而倒退")
	require.EqualValues(t, 600, pos.Delivery)
}
