package ingest

// #63 / #64 的红/绿（**2026-10-29 整文件重写**）：滚动回收（探测驱动的小循环）的
// 速率与有界性。
//
// ## 旧形态为何已不存在
//
// 旧文件的三条用例（`TestRollingReclaimKeepsUpWithGeneration` /
// `TestRollingReclaimBoundedAndHoldSafe` / `TestRollingReclaimEatsSegmentChainWithinStepCap`）
// 全部围绕**恢复分段链**：`TryReclaim` 单次上界 = 当前覆盖段的 `CoversTo`，因此
// 「陈旧段只到 5000 ⇒ 一轮只推 5000」「N 段链需要 N 步才吃完」「步数上界 32」都是
// 真实约束，`ReclaimRoll.MaxSteps` 这个旋钮就是为它们存在的。
//
// 新语义下 `reclaim = min(REQUEST_DONE 连续覆盖末端, max(Read, Durable))`——
// **一次 `TryReclaim` 就推进到认证覆盖末端**，不存在「段链逐跳」这个形态，
// 步数上界与速率靶数也就失去了被测对象（机制失去对象，用例随之删除）。
//
// ## 本文件现在守什么
//
// 保留下来的唯一仍然成立的关切：**滚动回收必须真的推进，且被暂停的源也能被它推进**
// （自愈面是“没人再投递”时唯一会碰回收的地方）。
//
// 转红方式（实测）：把 `selfHealPausedSource` 里的回收调用删掉 ⇒ 本用例立即红。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// reclaimRollDelivery 是夹具的"已确认投递水位"。
const reclaimRollDelivery = uint64(4_000_000)

// newRollingReclaimFixture 造 n 个「已确认投递到 D + 源暂停 + 投递面必失败」的源：
//   - 一条事件覆盖 [0, D]（位置跨度可以很大，WAL 仍只有一条）；
//   - 投递水位已确认到 D（凭据上界）；
//   - 源暂停 + 投递面必失败（⇒ 只有恢复/自愈面能治）。
func newRollingReclaimFixture(t *testing.T, n int) (*Manager, []SourceConfig, []*pipeline.Pipeline) {
	t.Helper()
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	cat := catalog.New(catalog.NewMemJournal())
	srcs := make([]SourceConfig, 0, n)
	for i := 0; i < n; i++ {
		p := filepath.Join(root, fmt.Sprintf("latest-%d.log", i))
		require.NoError(t, os.WriteFile(p, nil, 0o600))
		srcs = append(srcs, SourceConfig{
			LogSourceID: fmt.Sprintf("inst:roll%d", i), SourceGeneration: "g1", Path: p,
			StorageNamespace: fmt.Sprintf("inst:roll%d", i), UTCDay: runtimeTestUTCDay(),
		})
	}
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: srcs})
	require.NoError(t, err)
	m.SetSelfHealInterval(time.Nanosecond) // 让"每轮"都能真跑

	pipes := make([]*pipeline.Pipeline, 0, n)
	for _, src := range srcs {
		p := m.pipes[src.LogSourceID+"/"+src.SourceGeneration]
		require.NotNil(t, p)
		identity := logtypes.SourceIdentity{LogSourceID: src.LogSourceID, SourceGeneration: src.SourceGeneration, ParserVersion: "v1"}
		require.NoError(t, p.WAL().Append(logtypes.BuildEvent(identity,
			logtypes.RecordRange{Start: 0, End: reclaimRollDelivery},
			"2026-10-05T00:00:00Z", "2026-10-05T00:00:01Z", "INFO", "stdout", "rolling")))
		require.NoError(t, p.WAL().Commit())
		require.NoError(t, p.WAL().RecordHTTPResult(acquire.DeliveryResult{
			StartPos: 0, EndPos: reclaimRollDelivery, HTTPStatus: 200,
		}))
		primeConfirmedWALProjection(t, m, src, p, reclaimRollDelivery)
		// 暂停 + 投递面必失败（唯一出口 = 恢复/自愈面）。
		require.NoError(t, p.Ledger().PauseAcquire(p.Key(), "TEST_STALL: backlog over limit (fixture)"))
		p.SetDelivery(pipeline.FuncHook(func([]logtypes.Event, bool) (pipeline.DeliveryResult, error) {
			return pipeline.DeliveryResult{}, errors.New("stub: VL 不可用（本用例只考滚动回收）")
		}))
		pipes = append(pipes, p)
	}
	return m, srcs, pipes
}

// reclaimOf 读某源当前回收位置。
func reclaimOf(t *testing.T, p *pipeline.Pipeline) uint64 {
	t.Helper()
	pos, ok := p.Ledger().Positions(p.Key())
	require.True(t, ok)
	return pos.Reclaim
}

// TestRollingReclaimAdvancesPausedSource：被暂停的源也必须能被自愈面推进回收，
// 且推进到**认证覆盖末端**（一次到位，不再需要逐跳）。
//
// 转红方式（实测）：把 `selfHealPausedSource` 里的回收调用删掉 ⇒ 本用例立即红。
func TestRollingReclaimAdvancesPausedSource(t *testing.T) {
	m, srcs, pipes := newRollingReclaimFixture(t, 4)

	before := make([]uint64, len(pipes))
	for i, p := range pipes {
		before[i] = reclaimOf(t, p)
		require.Zero(t, before[i], "前置：回收尚未推进")
		m.selfHealPausedSource(srcs[i], p)
	}
	var advanced uint64
	for i, p := range pipes {
		advanced += reclaimOf(t, p) - before[i]
		require.Equal(t, reclaimRollDelivery, reclaimOf(t, p),
			"第 %d 源必须一次推进到认证覆盖末端（新语义：不再逐段跳）", i)
	}
	require.Positive(t, advanced, "自愈面必须真的推进回收")

	st := m.ReclaimRollingStats()
	t.Logf("#63 观测：四源合计推进 %d 位 · 一轮 steps=%d firstAdvance=%d elapsed=%s",
		advanced, st.Last.Steps, st.Last.FirstAdvance, st.Last.Elapsed)
	require.Positive(t, st.Last.Advanced, "推进量必须可观测（只读读数）")

	// 二轮"追平生成速率"：各源追加一截**新确认**区间（模拟生产仍在灌料）。
	const gen = uint64(1_000_000)
	for i, p := range pipes {
		identity := logtypes.SourceIdentity{LogSourceID: srcs[i].LogSourceID, SourceGeneration: srcs[i].SourceGeneration, ParserVersion: "v1"}
		// 暂停态下 WAL.Append 会被门禁拒（设计如此）⇒ 造增量时临时放行、随后重新暂停，
		// 以保持"停投 + 自愈面驱动"的夹具形态不变。
		require.NoError(t, p.Ledger().ResumeAcquire(p.Key()))
		require.NoError(t, p.WAL().Append(logtypes.BuildEvent(identity,
			logtypes.RecordRange{Start: reclaimRollDelivery, End: reclaimRollDelivery + gen},
			"2026-10-05T00:01:00Z", "2026-10-05T00:01:01Z", "INFO", "stdout", "gen")))
		require.NoError(t, p.WAL().Commit())
		require.NoError(t, p.Ledger().PauseAcquire(p.Key(), "TEST_STALL: backlog over limit (fixture)"))
		require.NoError(t, p.WAL().RecordHTTPResult(acquire.DeliveryResult{
			StartPos: reclaimRollDelivery, EndPos: reclaimRollDelivery + gen, HTTPStatus: 200,
		}))
		primeConfirmedWALProjection(t, m, srcs[i], p, reclaimRollDelivery+gen)
		m.selfHealPausedSource(srcs[i], p)
		require.Equal(t, reclaimRollDelivery+gen, reclaimOf(t, p),
			"第 %d 源必须继续追平新生数据（及格语义：追平生成速率 > 清零存量）", i)
	}
}
