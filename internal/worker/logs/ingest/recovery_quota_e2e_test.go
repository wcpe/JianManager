package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// TestRecoveryQuotaE2E（方案② 的两条端到端红证，2026-10-04 用户定调）：
//
//	① **恢复期回放/补账不得把源推成暂停**（独立配额 4× 之内 ✓）；
//	② **宽限过期后回归常规闸**，超常规闸者照常暂停（红线：常规期语义不削弱 ✗）。
//
// 转红方式（实测两条，各自独立红 ✓）：
//   - 变异①：去掉恢复期拓宽（syncRecoveryQuota 不设 factor）⇒ 恢复期越闸源被暂停 ⇒ ①红；
//   - 变异②：回归不生效（宽限过期仍按 factor 判）⇒ ②红。
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
	require.False(t, pipe.Ledger().Get(pipe.Key()).AcquirePaused,
		"① 恢复期回放/补账不得把源推成暂停（独立配额 4× 之内 ✓）")

	// 恢复结束 + 宽限过期 ⇒ 回归常规闸 ✓：同样 8 条（> 常规 4）必须照常暂停 ✓（红线）。
	m.recoveryProbe = func() bool { return false }
	m.recoveryDrainGrace = 1 // 1ns ⇒ 立即过期
	m.syncRecoveryQuota()
	require.NoError(t, pipe.WAL().Append(mkEvents(1)...)) // 追加 1 条触发越闸判定
	entry := pipe.Ledger().Get(pipe.Key())
	require.True(t, entry.AcquirePaused,
		"② 宽限过期回归常规闸后，超出常规闸者必须照常暂停（红线：常规期语义不削弱 ✗）")
	require.True(t, acquire.IsBacklogPauseReason(entry.PauseReason), "原因须是积压类 ✓")
	factor, _, recovering := m.RecoveryQuotaStats()
	require.False(t, recovering)
	require.Equal(t, 1.0, factor, "回归后生效系数必须是 1（常规闸 ✓）")
}
