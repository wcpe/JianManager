package ingest

// 段 B 红绿（w21 稿 ② 第三落点）：**重放/微代必须累积，不得取代**。
//
// 现场（#13 run8 实测）：重启后的重放把已 ack 内容再写进新代，且 old 的实现让重放走 replace=true
// ⇒ publish 把读白名单**重置为仅新代**（scope.ProjectionGenerations = []string{new}）⇒ 旧代承载的
// 未变更事件被挤出读面 ✗，同时新代与旧代各留一份 ⇒ 裸存储跨代拷贝 ✗。
//
// 期望：重放 ⇒ `replace=false`（累积）＋写入面＝未确认差集 ⇒ 白名单是**并集**（旧代仍在 ✓），
// 新代只补差集 ✓。
//
// 变异（人工验证过）：把 deliver 内 replace 判据恢复成含 replay（任一处置回 `|| replay`）⇒ 白名单被
// 重置为仅新代 ⇒ `require.Contains(gens, genA)` 红；逐字装回 ⇒ 绿。

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

func TestReplayAccumulatesInsteadOfSuperseding(t *testing.T) {
	client, _ := newProjectionVL(t)

	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, []byte(
		"[12:00:01] [Server thread/INFO]: hello\n[12:00:02] [Server thread/INFO]: next\n"), 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "node:replay", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "node:replay", UTCDay: runtimeTestUTCDay(),
	}
	opts := Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source}}
	m, err := newTestManager(t, opts)
	require.NoError(t, err)

	// 首批：两行落库并发布（代次 A 进入读白名单）。
	m.pollOnce()
	key := source.LogSourceID + "/" + source.SourceGeneration
	genA := savedSource(t, m, key).ProjectionGeneration
	require.NotEmpty(t, genA, "前置：首批应完成发布")

	// 以「重放」语义投递一条**新**事件（replay=true 是 deliver 的第三参）。
	identity := logtypes.SourceIdentity{
		LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, ParserVersion: "v1",
	}
	batch := []logtypes.Event{
		logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 30, End: 40},
			"2026-09-24T01:00:00Z", "2026-09-24T01:00:01Z", "INFO", "stdout", "replay batch"),
	}
	_, err = m.deliver(source, batch, true)
	require.NoError(t, err)
	require.NoError(t, m.Stop())

	// 断言：读白名单必须**累积**——旧代 A 仍在（取代会把旧代摘下 ⇒ 未变更事件从读面消失 ✗），
	// 且新代存在（差集确实写入）。
	record, stored := cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
	require.True(t, stored, "前置：catalog 应有该分区记录")
	gens := publishedSourceProjectionGenerations(record.PublishedProjection, source)
	require.Contains(t, gens, genA, "重放不得把已发布旧代挤出读白名单（必须累积）✗")
	require.GreaterOrEqual(t, len(gens), 2, "重放应在旧代之外新增承载差集的代次 ✓")

	// 反证：重放的那条事件必须可从读面解析（累积并集口径下，它不在旧代里）。
	require.NotEqual(t, genA, savedSource(t, m, key).ProjectionGeneration, "重放应产生新代承载差集")
}
