package config

// 采集索引（FR-496）配置键的接线回归：`log_index.batch_prune.*`（§6 索引有界化）与
// `log_index.persist.*`（§3.4 持久化切分）。
//
// 为什么需要本文件：`ledger.DeliveryBatchPruneConfig` 早就实现且默认开启，但此前只经
// `ingest.Options.IndexPrune` 在包内生效，**worker.yml 覆盖不到**（spec §6 曾把「YAML 接线
// 待做」原样登记）。接线若只加字段不验证，改坏一处（键名写错、默认值漏设、装配点忘记传）
// 的表现都是「配置改了没反应」，而采集照常跑、日志照常出——没人会发现。
//
// 三条断言各自可独立转红：
//  1. 默认值取自实现包的单一真源（把 SetDefault 删掉/写错默认值即红）；
//  2. YAML 键能真的绑定到结构体（键名写错即红）；
//  3. 配置关 → 裁剪关，且批次行数**退化到线性**（决定性断言：把装配链上任何一环改坏，
//     例如映射方法返回零值配置、或 Enabled 恒为 true，行数断言即红）。

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// TestLoad_LogIndexDefaults 零配置时两组键都取实现包的默认口径。
func TestLoad_LogIndexDefaults(t *testing.T) {
	cfg, err := Load(t.TempDir() + "/nonexistent.yaml")
	require.NoError(t, err)

	assert.True(t, cfg.LogIndex.IndexPrune().Enabled, "裁剪默认开启（ledger.DefaultDeliveryBatchPruneConfig）")
	assert.Equal(t, ledger.DefaultDeliveryBatchKeepRecent, cfg.LogIndex.IndexPrune().KeepRecent)
	assert.Equal(t, stateindex.DefaultCommitBudget(), cfg.LogIndex.CommitBudget(),
		"提交单元预算默认必须与 stateindex 的单一真源一致")
}

// TestLoad_LogIndexYAMLBinds YAML 键逐项绑定（键名/类型写错即红）。
func TestLoad_LogIndexYAMLBinds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.yml")
	require.NoError(t, os.WriteFile(path, []byte(
		"log_index:\n"+
			"  batch_prune:\n"+
			"    enabled: false\n"+
			"    keep_recent: 128\n"+
			"  persist:\n"+
			"    max_tx_rows: 256\n"+
			"    min_tx_rows: 8\n"+
			"    tx_duration_target: 15ms\n"), 0o600))
	cfg, err := Load(path)
	require.NoError(t, err)

	prune := cfg.LogIndex.IndexPrune()
	assert.False(t, prune.Enabled, "enabled: false 必须原样送达账本（应急逃生口）")
	assert.Equal(t, 128, prune.KeepRecent)

	budget := cfg.LogIndex.CommitBudget()
	assert.Equal(t, 256, budget.MaxRows)
	assert.Equal(t, 8, budget.MinRows)
	assert.Equal(t, 15*time.Millisecond, budget.Target)
}

// TestLogIndexConfigNormalizesIllegalValues 非法值一律回退默认：配置误写不得让裁剪失效、
// 也不得让切分消失（后者会把「单次持久化 ≤50ms」的达标线交还给配置运气）。
func TestLogIndexConfigNormalizesIllegalValues(t *testing.T) {
	assert.Equal(t, stateindex.DefaultCommitBudget(), LogIndexPersistConfig{MaxTxRows: -1}.CommitBudget())
	assert.Equal(t, stateindex.DefaultCommitBudget(), LogIndexPersistConfig{}.CommitBudget())
	assert.Equal(t, stateindex.DefaultCommitTarget,
		LogIndexPersistConfig{TxDurationTarget: "bogus"}.CommitBudget().Target)
	// 下限大于上限时夹到上限（否则自适应控制器会产出负的行数预算）。
	budget := LogIndexPersistConfig{MaxTxRows: 128, MinTxRows: 4096}.CommitBudget()
	assert.Equal(t, 128, budget.MinRows)
	// 负尾窗回退默认 0（只多留不少留，绝不因误写放宽判据）。
	assert.Equal(t, ledger.DefaultDeliveryBatchKeepRecent,
		LogIndexBatchPruneConfig{Enabled: true, KeepRecent: -5}.DeliveryBatchPruneConfig().KeepRecent)
}

// TestLogIndexBatchPruneConfigDrivesLedgerBounds 是接线链的决定性断言：
//
//	worker.yml 的 log_index.batch_prune.enabled → LogIndexConfig.IndexPrune() →
//	ledger.SetDeliveryBatchPrune → 账本批次列表的真实长度
//
// 关闭时批次逐条留存（行数与批量 N **线性**，等价于改回不裁剪的旧行为）；默认开启时有界
// （只留水位之上的一段）。同一夹具、同一推进节奏，差别只来自那一个开关。
//
// 转红说明：把 IndexPrune() 改成返回默认配置（Enabled 恒 true），「关闭时线性」断言即红；
// 把它改成返回零值配置（Enabled 恒 false），「开启时有界」断言即红；键名写错则 YAML 绑定
// 用例先红。断言用「账本列表长度」而不是索引表行数：列表是裁剪的现场真源，索引行只是它的
// 落库投影（后者由 ingest 层的 TestDeliveryBatchPruneBoundsIndexGrowth 端到端守着）。
func TestLogIndexBatchPruneConfigDrivesLedgerBounds(t *testing.T) {
	const batches = 2000
	const advanceEvery = 100

	measure := func(cfg LogIndexBatchPruneConfig) int {
		led := ledger.New()
		led.SetDeliveryBatchPrune(cfg.DeliveryBatchPruneConfig())
		key := ledger.SourceKey{LogSourceID: "node:000", SourceGeneration: "g1"}
		led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "node:000", SourceGeneration: "g1", ParserVersion: "v1"})
		for i := 0; i < batches; i++ {
			start := uint64(i) * 100
			end := start + 100
			require.NoError(t, led.RecordDelivery(key, start, end, logtypes.DeliveryRequestDone))
			if (i+1)%advanceEvery == 0 {
				advanceLedgerReclaim(t, led, key, end)
			}
		}
		return len(led.Get(key).DeliveryBatches)
	}

	off := measure(LogIndexBatchPruneConfig{Enabled: false})
	assert.Equal(t, batches, off, "配置关闭裁剪时批次必须逐条留存（行数随批量线性增长）")

	on := measure(LogIndexBatchPruneConfig{Enabled: true})
	assert.LessOrEqual(t, on, advanceEvery, "默认（开启）时批次行数必须与批量 N 解耦，实测 %d 行", on)

	keep := measure(LogIndexBatchPruneConfig{Enabled: true, KeepRecent: 128})
	assert.Greater(t, keep, on, "审计尾窗只多留不少留：keep_recent=128 必须比默认留得更多")
	assert.Less(t, keep, off, "尾窗再多也不得退化成线性增长")
}

// advanceLedgerReclaim 走完责任转移链把 reclaim 推到 to（生产里由 releaseRecovery 完成）：
// 裁剪判据是 `batch.End <= Positions.Reclaim`，没有水位推进就没有可裁条目。
func advanceLedgerReclaim(t *testing.T, led *ledger.Ledger, key ledger.SourceKey, to uint64) {
	t.Helper()
	from := led.Get(key).Positions.Reclaim
	if to <= from {
		return
	}
	segID := fmt.Sprintf("seg-%d-%d", from, to)
	require.NoError(t, led.RegisterRecovery(key, ledger.RecoveryRef{
		SegmentID: segID, Path: "recovery://config-test", State: logtypes.RecoveryStaged,
		CoversFrom: from, CoversTo: to,
	}))
	require.NoError(t, led.TransitionRecovery(key, segID, logtypes.RecoveryDurableVerified, "", ""))
	require.NoError(t, led.TransitionRecovery(key, segID, logtypes.RecoveryWALResponsibilityXfer, "", "test:receiver"))
	pos, err := led.TryReclaim(key)
	require.NoError(t, err)
	require.Equal(t, to, pos, "水位应推进到恢复分段覆盖末端")
}
