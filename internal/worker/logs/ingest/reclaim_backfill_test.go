package ingest

// #61-Z-a 的红/绿：**投递已确认、但回收位置无段覆盖**时必须在投递确认点补段。
//
// 现场（今晚全部悬案同源）：`ledger: reclaim blocked: no recovery segment covers position <R>`
// ⇒ WAL 不能回收 ⇒ 顶 8000/5000 ⇒ 源暂停 ⇒ 采集端落后（position 表：reclaim 落后上亿而
// delivery 已 = read ✗）。
//
// 本用例夹具就是那个形态：durable 存量已就位、投递水位**已确认到某处**、而**没有任何恢复分段**
// ⇒ 修复前 `TryReclaim` 恒报"无段覆盖"，源永远走不出滞回 ✗。
//
// 绿：投递确认点（`releaseRecovery` 的入口）先探一次回收，命中哨兵 `ErrReclaimNoSegment`
// ⇒ 按 [当前 Reclaim, 已确认投递水位) 补一段（状态 Xfer）⇒ 回收当场推进、滞回链打开 ✓。
//
// 变异回红：把该调用点去掉（或在 backfill 里直接 return nil）⇒ 首条断言（回收推进）转红。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// newReclaimBackfillFixture 造"投递已确认 + 无段覆盖"的现场形态：
//   - 4 条 durable 存量（区间连续、从 0 起 —— 投递水位要求"从回收位置起连续"）；
//   - 只把**前 2 条**登记为已确认投递（后 2 条保持未确认 ⇒ 用于证明"不越界未来" ✓）；
//   - 全程**不经过投递路径** ⇒ 一个恢复分段都没有（洞的形态 ✓）。
func newReclaimBackfillFixture(t *testing.T, drainLimit int) (*Manager, SourceConfig, string, *pipeline.Pipeline) {
	t.Helper()
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	src := SourceConfig{
		LogSourceID: "inst:backfill", SourceGeneration: "g1", Path: path,
		StorageNamespace: "inst:backfill", UTCDay: runtimeTestUTCDay(),
	}
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources:                 []SourceConfig{src},
		MaxReplayEventsPerDrain: drainLimit, // 排空单轮上限（夹具用它控制"哪些条目被确认"）
	})
	require.NoError(t, err)
	key := src.LogSourceID + "/" + src.SourceGeneration
	p := m.pipes[key]
	require.NotNil(t, p)
	identity := logtypes.SourceIdentity{LogSourceID: src.LogSourceID, SourceGeneration: src.SourceGeneration, ParserVersion: "v1"}
	for i := 0; i < 4; i++ {
		start := uint64(i * 5)
		ev := logtypes.BuildEvent(identity, logtypes.RecordRange{Start: start, End: start + 4},
			"2026-10-05T00:00:00Z", "2026-10-05T00:00:01Z", "INFO", "stdout", fmt.Sprintf("seed-%d", i))
		require.NoError(t, p.WAL().Append(ev))
	}
	require.NoError(t, p.WAL().Commit())
	// 投递水位：只确认前 2 条（区间 [0,9] 连续 ✓）。
	require.NoError(t, p.WAL().RecordHTTPResult(acquire.DeliveryResult{StartPos: 0, EndPos: 9, HTTPStatus: 200}))
	return m, src, key, p
}

// deliveryConfirmedRefs 数出该源账本里"投递确认补段"的条数与其状态。
func deliveryConfirmedRefs(t *testing.T, p *pipeline.Pipeline) []ledger.RecoveryRef {
	t.Helper()
	entry := p.Ledger().Get(p.Key())
	require.NotNil(t, entry)
	out := make([]ledger.RecoveryRef, 0, len(entry.RecoveryRefs))
	for _, ref := range entry.RecoveryRefs {
		if strings.HasPrefix(ref.SegmentID, "delivery-confirmed-") {
			out = append(out, ref)
		}
	}
	return out
}

func TestDeliveryConfirmedBackfillUnblocksReclaim(t *testing.T) {
	m, src, key, p := newReclaimBackfillFixture(t, 2) // 排空只发前 2 条（已确认的那两条）⇒ 后 2 条保持未确认

	pos, ok := p.Ledger().Positions(p.Key())
	require.True(t, ok)
	require.Equal(t, uint64(9), pos.Delivery, "前置：投递水位已到 9（前 2 条已确认）")
	require.Zero(t, pos.Reclaim, "前置：回收位置未动（洞的形态）")
	require.Empty(t, deliveryConfirmedRefs(t, p), "前置：一个补段都没有")

	// 红形态（修复前）：回收被"无段覆盖"拒 ⇒ 源永远走不出滞回 ✗。
	_, err := p.TryReclaim()
	require.Error(t, err)
	require.True(t, errors.Is(err, ledger.ErrReclaimNoSegment),
		"前置红形态：回收必须被\"无段覆盖\"拒（现场文案），实测 %v", err)

	// 绿：投递确认点补段 —— 直接驱动**生产三条路径共用的入口** `releaseRecovery`（正常批次 / 排空 /
	// 对账都调它 ✓）。不绕道 `DeliverPending`：那会额外耦合排空面的限速/抑制/状态闸（与本修复无关 ✗）。
	require.NoError(t, m.releaseRecovery(src, []logtypes.Event{
		{EventID: "confirmed", Record: logtypes.RecordRange{Start: 0, End: 9}},
	}))

	refs := deliveryConfirmedRefs(t, p)
	require.Len(t, refs, 1, "必须补出唯一一段「投递确认」覆盖段")
	require.Equal(t, uint64(0), refs[0].CoversFrom, "覆盖必须从**当前回收位置**起（否则洞仍在）")
	require.Equal(t, uint64(9), refs[0].CoversTo, "覆盖只到**已确认投递水位**（不越界未来 ✓）")
	require.True(t, strings.HasPrefix(refs[0].Path, "projection://"),
		"路径用投影背书段同款（释放依据才会登记成 PROJECTION_BACKED ✓）；来历由段名前缀标注 ✓，实测 %q", refs[0].Path)

	pos, ok = p.Ledger().Positions(p.Key())
	require.True(t, ok)
	require.Greater(t, pos.Reclaim, uint64(0), "回收必须当场推进（滞回链的根因被解掉 ✓）")

	// 不变量（default-deny 未放宽）：**未确认投递**的位置（> delivery）仍不得被回收。
	_, err = p.TryReclaim()
	require.Error(t, err)
	require.True(t, errors.Is(err, ledger.ErrReclaimNoSegment),
		"越过已确认投递水位的回收仍必须被拒（未确认数据一个字节都不许跳过 ✓），实测 %v", err)

	// 幂等：再跑一次投递确认点，不得重复建段。
	entry := p.Ledger().Get(p.Key())
	require.NoError(t, m.releaseRecovery(src, []logtypes.Event{{EventID: "x", Record: logtypes.RecordRange{Start: 0, End: 9}}}))
	require.Len(t, deliveryConfirmedRefs(t, p), 1, "同区间重复触发不得重复建段（段名含区间 ⇒ 幂等 ✓）")
	_ = key
	_ = entry
}

// TestDeliveryConfirmedBackfillSkipsWhenNoHole：**已有段覆盖当前回收位置**时**不得**补段
// （否则 RecoveryRefs 会随投递轮次无界增长 ✗）。闸的门槛正是"回收被哨兵 `ErrReclaimNoSegment` 拒" ✓。
//
// 说明：本用例只测**闸**（有覆盖即不补 ✓）；"覆盖范围是否越界未来"由生产绑定点保证
// （它们一律以 `delivery/read` 等已确认水位为 `CoversTo` ✓，见 #61 报告）。
func TestDeliveryConfirmedBackfillSkipsWhenNoHole(t *testing.T) {
	m, src, _, p := newReclaimBackfillFixture(t, 2)

	// 预置一段覆盖（= 健康路径上既有绑定点本该留下的形态），并推进到 Xfer。
	const segID = "projection-recovery-preexisting-19"
	require.NoError(t, p.BindRecoverySegment(segID, "projection://proj-pre", 0, 19))
	require.NoError(t, p.TransitionRecovery(segID, logtypes.RecoveryDurableVerified, "", ""))
	require.NoError(t, p.TransitionRecovery(segID, logtypes.RecoveryWALResponsibilityXfer, "", "projection:proj-pre"))
	_, err := p.TryReclaim()
	require.NoError(t, err, "前置：有段覆盖 ⇒ 回收本就该正常推进（哨兵不出现 ⇒ 闸不该开）")

	events := []logtypes.Event{{EventID: "post", Record: logtypes.RecordRange{Start: 0, End: 19}}}
	require.NoError(t, m.releaseRecovery(src, events), "投递确认点必须正常返回")
	require.Empty(t, deliveryConfirmedRefs(t, p),
		"已有覆盖 ⇒ 本次**不得**补段（补段只在\"回收被哨兵拒\"时发生 ✓）")
}
