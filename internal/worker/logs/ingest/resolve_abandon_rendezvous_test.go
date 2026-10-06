package ingest

// 收尾件（ADR-101 / FR-483 §6）：放弃裁定在产品面**可达**——按钮在"无完整已发布投影"的死结上：
//  ① 无 metadata（CP 未注入操作人） ⇒ 照旧拒绝（不开静默口子 ✗）；
//  ③ 只有理由、没有操作人 ⇒ 同样拒绝（二者必须成对 ✓）；
//  ② 操作人＋显式理由齐备 ⇒ 同一错消失，且账本留痕可审计（GapReasonPermanentlyLost + Operator ✓）。
//
// 变异（人工验证过）：去掉 resolveOneSourceGaps 里的 AbandonGapsThrough 调用 ⇒ 留痕断言必红；
// 逐字装回 ⇒ 绿。夹具：manager 直调 + metadata.NewIncomingContext（see resolve_round_bounded_test 同型）。

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

	// ① 无 metadata ⇒ 照旧拒绝。
	err = m.resolveOneSourceGaps(context.Background(), key, pipe, source)
	require.ErrorContains(t, err, abandonProjectionError)

	// ③ 只有 reason、无 operator ⇒ 同样拒绝。
	onlyReason := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-jm-reason", "ADR-101 无操作人"))
	err = m.resolveOneSourceGaps(onlyReason, key, pipe, source)
	require.ErrorContains(t, err, abandonProjectionError)

	// ② 操作人＋理由齐备 ⇒ 该错消失 + 留痕可查（操作人与原因码都在）。
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"x-jm-operator", "admin", "x-jm-reason", "ADR-101 永久空洞"))
	if err2 := m.resolveOneSourceGaps(ctx, key, pipe, source); err2 != nil {
		t.Logf("带 operator+reason 的 resolve 返回（后续阶段允许失败，但不得再报死结错）：%v", err2)
		require.NotContains(t, err2.Error(), abandonProjectionError,
			"带操作人+理由后不得再报同一死结错（后续阶段可因其它前置失败 ✓）")
	} else {
		t.Logf("带 operator+reason 的 resolve 全程无错")
	}
	entry := pipe.Ledger().Get(pipe.Key())
	require.NotNil(t, entry)
	require.NotEmpty(t, entry.Abandonments, "放弃裁定必须留痕（持久可审计）")
	require.Equal(t, "admin", entry.Abandonments[0].Operator, "操作人必须入档")
	require.Equal(t, ledger.GapReasonPermanentlyLost, entry.Abandonments[0].ReasonCode, "原因码必须是永久丢失")
}
