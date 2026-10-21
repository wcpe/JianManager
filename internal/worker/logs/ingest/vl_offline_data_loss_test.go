package ingest

// VL 运行期失联（进程崩溃 / 端口不可达）时，事件**必须**安全留在 WAL，并在 VL 恢复后同批重投成功。
//
// ── 缺陷形态（2026-10-21 实证，修复前）────────────────────────────────────────────
// `source=worker` / `source=node` 自源为「避免失败日志再写回 VL」而在
// `acquire.Pipeline.Ingest` 的 suppressRecursiveVL 分支里 `return nil`，投递失败因此被上层
// 读成成功。调用点随后走到 `releaseRecovery`（RecoverProof），为**从未写入 VL、也不在
// canonical 集合里**的区间建恢复分段并置 CLEANED ⇒ `Ledger.tryReclaimLocked` 把 `Reclaim`
// 推到 `ref.CoversTo`（判据只看段状态，**不看投递水位**）⇒ `WAL.pruneReclaimed` 按
// `Durable && Record.End <= Reclaim` 剪掉条目 ⇒ 事件彻底消失。
//
// 实测读数（修复前，同一夹具）：`delivery=0 reclaim=44 WAL条目=0`，段 `projection://` 为
// CLEANED —— 账本声称「责任已转移、有副本」，而 VL 里一条都没有。
//
// ── 修复后的不变量 ──────────────────────────────────────────────────────────────
// 责任转移的前置判据是「该区间已有确认副本」（`positions.Delivery >= to`）。不满足即
// 不建段、不推进、不报错 ⇒ 条目留在 WAL，由维护环的投递欠账步（`Delivery < Read` 时触发
// `DeliverPending`）在 VL 恢复后重投。
//
// 变异验证（实测）：去掉 `releaseRecovery` 里的 `positions.Delivery < to` 闸 ⇒
// TestWorkerSourceVLOfflineKeepsEventsInWAL 立即转红（WAL 条目被剪到 0）。

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// vlOfflineFixture 造一个「日志文件已有内容 + 投递面可切换通断」的源。
//
// failing 为 true 时投递必失败（模拟 VL 进程崩溃 / 端口不可达）；置回 false 即模拟 VL 恢复。
func vlOfflineFixture(t *testing.T, category logtypes.Source, failing *bool) (*Manager, SourceConfig, *pipeline.Pipeline) {
	t.Helper()
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(path, []byte("2026-10-16 00:00:00 INFO vl offline probe line\n"), 0o600))
	src := SourceConfig{
		LogSourceID: "vloffline:" + string(category), SourceGeneration: "g1", Path: path,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "vloffline:" + string(category),
		UTCDay:         runtimeTestUTCDay(),
		SourceCategory: category,
	}
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{src},
	})
	require.NoError(t, err)
	p := m.pipes[src.LogSourceID+"/"+src.SourceGeneration]
	require.NotNil(t, p)
	p.SetDelivery(pipeline.FuncHook(func([]logtypes.Event, bool) (pipeline.DeliveryResult, error) {
		if *failing {
			return pipeline.DeliveryResult{}, errors.New("stub: VictoriaLogs 端口不可达（模拟 VL 进程崩溃）")
		}
		return pipeline.DeliveryResult{HTTPStatus: 200}, nil
	}))
	return m, src, p
}

// TestWorkerSourceVLOfflineKeepsEventsInWAL 是缺陷 1 的回归：
// VL 失联期间，worker 自源的事件**必须留在 WAL**、投递水位**不得推进**、
// 且**不得**出现"责任已转移"的恢复分段。
func TestWorkerSourceVLOfflineKeepsEventsInWAL(t *testing.T) {
	failing := true
	_, _, p := vlOfflineFixture(t, logtypes.SourceWorker, &failing)

	events, err := p.Drain()
	require.Len(t, events, 1, "夹具前提：必须真的产生一条事件")
	// 投递失败被吞掉是既有的「不递归写回 VL」契约（acquire_test 的
	// TestWorkerSourceSamePipelineNoRecursiveVL 守它），本用例不改该契约。
	require.NoError(t, err, "worker 自源不把投递失败冒泡（既有契约 ✓）")

	pos, ok := p.Ledger().Positions(p.Key())
	require.True(t, ok)
	require.Equal(t, uint64(0), pos.Delivery,
		"VL 失联时投递水位**不得**推进（推进即等于宣称已落库 ✗）")
	require.Equal(t, uint64(0), pos.Reclaim,
		"VL 失联时回收水位**不得**推进（越过未投递数据 = 数据丢失 ✗）")

	require.Equal(t, 1, len(p.WAL().Snapshot()), "事件必须留在 WAL，等待 VL 恢复后重投")

	// 采集轮每轮都会尝试回收：不得因此丢掉条目，也不得伪造"责任已转移"。
	_, _ = p.WAL().TryReclaim()
	require.Equal(t, 1, len(p.WAL().Snapshot()), "回收尝试后条目仍必须保留")

	ent := p.Ledger().Get(p.Key())
	require.NotNil(t, ent)
	for _, ref := range ent.RecoveryRefs {
		require.NotEqual(t, logtypes.RecoveryCleaned, ref.State,
			"没有确认副本就不得把恢复分段收敛为 CLEANED（= 宣称责任已转移）: %s", ref.Path)
	}
	require.GreaterOrEqual(t, ledger.UnresolvedGapCountOf(ent), 1,
		"投递失败必须留下可见缺口（不得静默）")
}

// TestWorkerSourceVLOfflineThenRecoveredRedelivers 是缺陷 1 的收尾：
// VL 恢复后，同一批事件必须被**重投成功**并推进投递水位。
func TestWorkerSourceVLOfflineThenRecoveredRedelivers(t *testing.T) {
	failing := true
	_, _, p := vlOfflineFixture(t, logtypes.SourceWorker, &failing)

	_, err := p.Drain()
	require.NoError(t, err)
	require.Equal(t, 1, len(p.WAL().Snapshot()), "失联期条目必须留在 WAL")

	// VL 恢复：排空面把已 durable 存量重投出去。
	failing = false
	delivered, err := p.DeliverPending()
	require.NoError(t, err, "VL 恢复后重投必须成功")
	require.True(t, delivered, "必须真的外发了存量（否则等于没重投）")

	pos, ok := p.Ledger().Positions(p.Key())
	require.True(t, ok)
	require.Greater(t, pos.Delivery, uint64(0), "重投成功后投递水位必须推进")
}

// TestNodeSourceVLOfflineKeepsEventsInWAL 是同一缺陷的第二个受影响类别（source=node 同样
// 走 suppressRecursiveVL），防止只修了 worker 分支。
func TestNodeSourceVLOfflineKeepsEventsInWAL(t *testing.T) {
	failing := true
	_, _, p := vlOfflineFixture(t, logtypes.SourceNode, &failing)

	_, err := p.Drain()
	require.NoError(t, err)
	pos, ok := p.Ledger().Positions(p.Key())
	require.True(t, ok)
	require.Equal(t, uint64(0), pos.Reclaim, "node 自源同样不得越过未投递数据")
	require.Equal(t, 1, len(p.WAL().Snapshot()), "node 自源事件必须留在 WAL")
}

// TestInstanceSourceVLOfflineKeepsEventsInWAL 是**对照组**（既有正确行为，用于证明本修复
// 没有把普通源的行為一起改掉）：普通源的投递失败本就冒泡、水位不动、条目保留。
func TestInstanceSourceVLOfflineKeepsEventsInWAL(t *testing.T) {
	failing := true
	_, _, p := vlOfflineFixture(t, logtypes.SourceInstance, &failing)

	_, err := p.Drain()
	require.Error(t, err, "对照组：普通源必须把投递失败上报")
	pos, ok := p.Ledger().Positions(p.Key())
	require.True(t, ok)
	require.Equal(t, uint64(0), pos.Delivery)
	require.Equal(t, uint64(0), pos.Reclaim)
	require.Equal(t, 1, len(p.WAL().Snapshot()))

	failing = false
	delivered, err := p.DeliverPending()
	require.NoError(t, err)
	require.True(t, delivered)
}
