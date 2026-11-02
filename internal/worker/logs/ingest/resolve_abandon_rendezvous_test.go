package ingest

// 人工裁定只允许显式完整源键入口；全源自动路径无论 metadata 是否齐备都不得放弃缺口。
// 显式入口仍须保留认证操作人、理由和永久缺失审计凭据。

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// abandonProjectionError 是**旧**拒绝文案（简化前：全源路径因「没有完整已发布投影」而拒绝）。
//
// 2026-11-02 投递路径简化后，`resolveOneSourceGaps` 不再读已发布投影（那个概念没了），
// 所以这个文案在生产代码里已完全消失。下面改为直接断言**它要守的语义**：
// 全源自动路径**无论 metadata 是否齐备都不得放弃缺口**（不断言具体文案）。
const abandonProjectionError = "has no complete published projection"

func TestResolveGapsAbandonRendezvous(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, []byte("[12:00:01] [Server thread/INFO]: hello\n"), 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:abandon/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:abandon", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source}})
	require.NoError(t, err)
	key := source.LogSourceID + "/" + source.SourceGeneration
	pipe := m.pipes[key]
	require.NotNil(t, pipe)

	// 前置：账本有已消费水位（Durable>0，回退支可用）但**从未发布** ⇒ 正是死结形态 ✓。
	require.NoError(t, pipe.Ledger().AdvanceDurable(pipe.Key(), 100))
	require.NoError(t, pipe.Ledger().RecordDelivery(pipe.Key(), 0, 100, logtypes.DeliveryRequestDone))
	// 放弃裁定覆盖的是「缺口」⇒ 夹具必须有缺口可放弃（与 resolve_round_bounded_test 同款 ✓）。
	require.NoError(t, pipe.Ledger().RecordGap(pipe.Key(), 0, 100, ledger.GapReasonDeliverError, "transport down"))

	before := pipe.Ledger().Get(pipe.Key())
	require.Equal(t, uint64(100), before.Positions.Durable)
	require.Equal(t, uint64(100), before.Positions.Delivery)
	require.Len(t, before.Gaps, 1)
	require.Equal(t, ledger.GapReasonDeliverError, before.Gaps[0].Reason)

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"x-jm-operator", "admin", "x-jm-reason", "ADR-101 永久空洞"))
	onlyReason := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-jm-reason", "ADR-101 无操作人"))
	for _, automatic := range []context.Context{context.Background(), onlyReason, ctx} {
		// 调用真实全源入口，齐备的人工 metadata 也不能让自动路径放弃缺口。
		//
		// 2026-11-02 简化：不再断言旧文案 `has no complete published projection`
		//（那条分歧线随已发布投影消失），但仍要求**调用不得留下任何放弃凭据**。
		_ = m.ResolveCoveredGaps(automatic)
		entry := pipe.Ledger().Get(pipe.Key())
		require.Equal(t, before.Positions, entry.Positions, "全源路径不得改变消费水位")
		require.Equal(t, before.Gaps, entry.Gaps, "未解决 DELIVER_ERROR 必须原样保留（全源路径不得放弃缺口）")
		require.Empty(t, entry.Abandonments, "全源路径不得留下永久放弃凭据")
	}

	// CP 完成对象授权后只传完整源键和认证操作人；缺键或缺操作人均不得落裁定。
	require.Error(t, m.ResolveCoveredGapsForSource(ctx, source.LogSourceID, "admin"))
	require.Error(t, m.ResolveCoveredGapsForSource(ctx, key, ""))
	require.Empty(t, pipe.Ledger().Get(pipe.Key()).Abandonments)
	require.NoError(t, m.ResolveCoveredGapsForSource(ctx, key, "admin"))
	entry := pipe.Ledger().Get(pipe.Key())
	require.NotNil(t, entry)
	require.Len(t, entry.Abandonments, 1, "放弃裁定必须留下唯一的持久审计凭据")
	abandonment := entry.Abandonments[0]
	require.Equal(t, "admin", abandonment.Operator, "操作人必须入档")
	require.Equal(t, "ADR-101 永久空洞", abandonment.Reason, "人工理由必须入档")
	require.Equal(t, ledger.GapReasonPermanentlyLost, abandonment.ReasonCode)
	require.Equal(t, uint64(0), abandonment.From)
	require.Equal(t, uint64(100), abandonment.To)
	require.Equal(t, uint64(100), abandonment.Through)
	require.NotEmpty(t, abandonment.AtUTC)
	require.True(t, entry.Gaps[0].Resolved)

	// 显式落库并从索引读回，不能把内存里的一条记录冒充持久凭据。
	require.NoError(t, m.persist())
	rows, err := m.index.Load()
	require.NoError(t, err)
	persisted, err := indexStateToState(rows)
	require.NoError(t, err)
	require.Len(t, persisted.Sources[key].Ledger, 1)
	require.Equal(t, entry.Abandonments, persisted.Sources[key].Ledger[0].Abandonments)
}
