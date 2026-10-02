package ledger

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 位置不变式（2026-09-30 生产实证）：读位置**不得退到回收水位之下**。
//
// 背景：崩溃后 AdvanceRead 允许回退重建（tailer 自游标续读）；但一旦退到 reclaim 之下，
// releaseRecovery 的 from(=reclaim) >= to 便恒成立、静默不再建段 → 回收永久停滞 →
// 积压永不回落 → 源反复暂停（实测 read=2.24M < reclaim=13.4M，积压 7998 卡死数小时）。
//
// 变异验证：删掉 AdvanceRead 里的下界夹取，本测试立即转红。
func TestAdvanceRead_ClampsToReclaim(t *testing.T) {
	led := New()
	key := keyOf("clamp", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "clamp", SourceGeneration: "g1"})
	require.NoError(t, led.AdvanceDurable(key, 100))
	require.NoError(t, led.RecordDelivery(key, 0, 100, logtypes.DeliveryRequestDone))
	require.NoError(t, led.RegisterRecovery(key, RecoveryRef{
		SegmentID: "seg1", State: logtypes.RecoveryStaged, CoversFrom: 0, CoversTo: 100,
	}))
	require.NoError(t, led.TransitionRecovery(key, "seg1", logtypes.RecoveryDurableVerified, "", ""))
	require.NoError(t, led.TransitionRecovery(key, "seg1", logtypes.RecoveryWALResponsibilityXfer, "", "receiver-A"))

	pos, err := led.TryReclaim(key)
	require.NoError(t, err)
	require.Equal(t, uint64(100), pos, "回收应推进到分段末端")
	require.Equal(t, uint64(100), led.Get(key).Positions.Reclaim)

	// ① 回退到水位之下 → 必须被夹到 reclaim（不得低于）
	require.NoError(t, led.AdvanceRead(key, 30))
	require.Equal(t, uint64(100), led.Get(key).Positions.Read,
		"读位置不得退到回收水位之下：低于它的事件已被证明安全另存，重读会静默卡死回收")

	// ② 正常前移不受影响（只夹下界，不设上界）
	require.NoError(t, led.AdvanceRead(key, 250))
	require.Equal(t, uint64(250), led.Get(key).Positions.Read)

	// ③ 恰好等于水位：原样记录（边界不误伤）
	require.NoError(t, led.AdvanceRead(key, 100))
	require.Equal(t, uint64(100), led.Get(key).Positions.Read)
}
