package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// TestRecoveryQuotaE2E（方案② 的两条端到端红证，2026-10-04 用户定调）：
//
//	① **恢复期回放/补账不得把源推成暂停**（独立配额 4× 之内 ✓）；
//	② **宽限过期后回归常规闸**，超常规闸者照常被处置（红线：常规期语义不削弱 ✗）。
//
// 2026-10-29 语义变更后（用户裁定：越界丢最旧、不暂停、不背压），“常规期语义”从
// 「照常暂停」变成**「照常裁到常规闸内」**。红线本身一字未变：**常规期的闸不得被削弱**。
// 新语义下它表现为两件事，合起来才等价于“常规闸真的在”：
//   - 宽限过期后生效上限必须回到 1×（拓宽已关闭）；
//   - 同一批数据在常规期**必须被裁**——队列不得停在拓宽后的大小上（“回归不生效”
//     恰恰就是这个形态：上限仍按 4× 算，8 条一条不丢）。
//
// 转红方式（实测两条，各自独立红 ✓）：
//   - 变异①：去掉恢复期拓宽（syncRecoveryQuota 不设 factor）⇒ 恢复期就按常规闸裁 ⇒ ①红；
//   - 变异②：回归不生效（宽限过期仍按 factor 判）⇒ 队列停在 8 条不裁 ⇒ ②红。
func TestRecoveryQuotaE2E(t *testing.T) {
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, []byte("[12:00:01] [Server thread/INFO]: seed\n"), 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:rquota/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:rquota", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source}})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	pipe := m.pipes[key]
	require.NotNil(t, pipe)

	mkEvents := func(n int) []logtypes.Event {
		identity := logtypes.SourceIdentity{LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, ParserVersion: "v1"}
		out := make([]logtypes.Event, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, logtypes.BuildEvent(identity,
				logtypes.RecordRange{Start: uint64(1000 + i*10), End: uint64(1009 + i*10)},
				"2026-10-04T10:00:00Z", "2026-10-04T10:00:00Z", "INFO", "stdout", fmt.Sprintf("line %d", i)))
		}
		return out
	}

	// 常规闸压到 4 条 ⇒ 8 条即越常规闸、但仍在 4× 拓宽（16）之内 ✓。
	pipe.WAL().SetLimits(4, 0)
	m.recoveryProbe = func() bool { return true } // 恢复中
	m.syncRecoveryQuota()
	require.NoError(t, pipe.WAL().Append(mkEvents(8)...))
	require.NoError(t, pipe.WAL().Commit())
	// 裁在**持久化之后**（见 WAL.TrimBacklog / pipeline.persistCommittedBatchContext）：
	// 不走这条路径的用例测的是“没人裁剪”。
	pipe.WAL().TrimBacklog()
	require.False(t, pipe.Ledger().Get(pipe.Key()).AcquirePaused,
		"① 恢复期回放/补账不得把源推成暂停（独立配额 4× 之内 ✓）")
	require.Len(t, pipe.WAL().Snapshot(), 8,
		"① 恢复期独立配额（4× = 16）之内一条都不得丢（回放是设计行为）")
	if n, _ := pipe.WAL().DropStats(); n != 0 {
		t.Fatalf("① 恢复期内（拓宽窗口）不得产生任何丢弃，实测 %d 条", n)
	}

	// 恢复结束 + 宽限过期 ⇒ 回归常规闸 ✓：同样 8 条（> 常规 4）必须照常被裁到 4 条 ✓（红线）。
	m.recoveryProbe = func() bool { return false }
	m.recoveryDrainGrace = 1 // 1ns ⇒ 立即过期
	m.syncRecoveryQuota()
	factor, _, recovering := m.RecoveryQuotaStats()
	require.False(t, recovering)
	require.Equal(t, 1.0, factor, "回归后生效系数必须是 1（常规闸 ✓）")

	pipe.WAL().TrimBacklog() // 常规期裁剪：上限已回到 4 ⇒ 8 条必须被裁
	entry := pipe.Ledger().Get(pipe.Key())
	require.False(t, entry.AcquirePaused, "丢弃语义下裁剪不得暂停采集")
	require.LessOrEqual(t, len(pipe.WAL().Snapshot()), 4,
		"② 宽限过期回归常规闸后，超出常规闸者必须被裁到 4 条内（红线：常规期语义不削弱 ✗）")
	if n, _ := pipe.WAL().DropStats(); n < 4 {
		t.Fatalf("② 常规期必须真截（8 条 ⇒ 至少丢 4 条），实测只丢 %d 条", n)
	}
}
