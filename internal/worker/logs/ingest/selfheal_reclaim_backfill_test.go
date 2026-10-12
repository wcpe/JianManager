package ingest

// #62 的红/绿：**已停投的源**（顶闸暂停、没人再投递）也必须能被补段救出来。
//
// 现场：补段闸（#61-Z-a）只挂在投递确认面 ⇒ 四个落后源（`inst:160`/`202`/`203`/`179`，backlog 24–31× 上限）
// **一次都没轮到**它们 —— 因为没人再投递 ⇒ 谁也走不到投递确认面 ✗✓；而新触发过的 206/163 早已补段自愈 ✓。
//
// 本用例夹具就是那个形态：durable 存量 + 投递水位已确认 + **处于暂停**（顶闸）+ **零恢复分段**
// + **零投递活动**（不调 DeliverPending）⇒ 修复前 `TryReclaim` 恒报"无段覆盖"，源永远走不出滞回 ✗。
//
// 绿：恢复/自愈面（`selfHealPausedSource` 步 0）用**同一个助手**补段 ⇒ 回收推进、WAL 回落、采集可恢复 ✓。
//
// 变异回红：把自愈面的步 0 调用去掉 ⇒ 首条断言（补段 + 回收推进）转红。

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// newStalledPausedSourceFixture：顶闸暂停（WAL 上限 1 ⇒ 第 2 条追加即越界）+ 已确认投递水位 +
// 零恢复分段 + 零投递活动。
func newStalledPausedSourceFixture(t *testing.T) (*Manager, SourceConfig, *pipeline.Pipeline) {
	t.Helper()
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	src := SourceConfig{
		LogSourceID: "inst:stalled", SourceGeneration: "g1", Path: path,
		StorageNamespace: "inst:stalled", UTCDay: runtimeTestUTCDay(),
	}
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{src},
	})
	require.NoError(t, err)
	key := src.LogSourceID + "/" + src.SourceGeneration
	p := m.pipes[key]
	require.NotNil(t, p)
	p.WAL().SetLimits(4, 0) // 积压上限 4 ⇒ 第 5 条追加即越界 ⇒ 源暂停（现场"顶闸"形态 ✓）
	identity := logtypes.SourceIdentity{LogSourceID: src.LogSourceID, SourceGeneration: src.SourceGeneration, ParserVersion: "v1"}
	for i := 0; i < 5; i++ {
		start := uint64(i * 5)
		ev := logtypes.BuildEvent(identity, logtypes.RecordRange{Start: start, End: start + 4},
			"2026-10-05T00:00:00Z", "2026-10-05T00:00:01Z", "INFO", "stdout", "stalled")
		require.NoError(t, p.WAL().Append(ev))
	}
	require.NoError(t, p.WAL().Commit())
	// 投递水位：5 条全部已确认（区间 [0,24] 连续 ✓）——这是补段的上界（不越界未来 ✓）。
	require.NoError(t, p.WAL().RecordHTTPResult(acquire.DeliveryResult{StartPos: 0, EndPos: 24, HTTPStatus: 200}))
	m.SetSelfHealInterval(time.Nanosecond) // 自愈按源限频：压到必然过期，回归走真实路径 ✓
	// 投递面**必失败**（确定性桩）：现场"没人能走到投递确认面"的形态 ✓
	//（排空 ⇒ 投递失败 ⇒ proof 面不执行 ⇒ 只有恢复/自愈面能治这个洞 ✓）。
	p.SetDelivery(pipeline.FuncHook(func([]logtypes.Event, bool) (pipeline.DeliveryResult, error) {
		return pipeline.DeliveryResult{}, errors.New("stub: VL 不可用（本用例只考自愈面）")
	}))
	entry := p.Ledger().Get(p.Key())
	require.NotNil(t, entry)
	require.True(t, entry.AcquirePaused, "前置：源必须处于暂停态（自愈面的准入条件 ✓）")
	require.Empty(t, entry.RecoveryRefs, "前置：零恢复分段（洞的形态 ✓）")
	return m, src, p
}

func TestSelfHealBackfillsStalledReclaim(t *testing.T) {
	m, src, p := newStalledPausedSourceFixture(t)

	pos, ok := p.Ledger().Positions(p.Key())
	require.True(t, ok)
	require.Zero(t, pos.Reclaim)
	require.Equal(t, uint64(24), pos.Delivery)

	// 红形态（修复前）：停投的源没有任何面能补段 ⇒ 回收恒被拒。
	_, err := p.TryReclaim()
	require.True(t, errors.Is(err, ledger.ErrReclaimNoSegment), "前置红形态：%v", err)
	before, _ := p.WAL().Backlog()
	require.Positive(t, before, "前置：WAL 里有积压（顶闸的形态 ✓）")

	// 绿：恢复/自愈面（步 0）补段 ⇒ 回收推进 + WAL 回落 + 采集可恢复。
	changed := m.selfHealPausedSource(src, p)

	entry := p.Ledger().Get(p.Key())
	require.NotNil(t, entry)
	var backfilled []ledger.RecoveryRef
	for _, ref := range entry.RecoveryRefs {
		if strings.HasPrefix(ref.SegmentID, "delivery-confirmed-") {
			backfilled = append(backfilled, ref)
		}
	}
	require.Len(t, backfilled, 1, "自愈面必须补出唯一一段「投递确认」覆盖段")
	require.Equal(t, uint64(0), backfilled[0].CoversFrom)
	require.Equal(t, uint64(24), backfilled[0].CoversTo, "覆盖只到已确认投递水位（不越界未来 ✓）")

	pos, ok = p.Ledger().Positions(p.Key())
	require.True(t, ok)
	require.Equal(t, uint64(24), pos.Reclaim, "回收必须推进到已确认水位 ✓")
	after, _ := p.WAL().Backlog()
	require.Less(t, after, before, "WAL 必须回落（回收推进 ⇒ 剪枝 ✓）")

	entry = p.Ledger().Get(p.Key())
	require.False(t, entry.AcquirePaused, "积压回落后采集必须自动恢复（滞回条件满足 ✓）")
	require.True(t, changed, "自愈必须报告发生了改变（可观测 ✓）")
}
