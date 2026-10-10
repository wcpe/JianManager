package logcoord

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	workerquery "github.com/wcpe/JianManager/internal/worker/logs/query"
)

// 导出上限可配置（FR-480 导出闸）回归。
//
// 缺陷形态：Export 的行/字节上限是编译期常量，而前端导出刻意不传 limit
// （apps/control-plane-web/src/api/logFederation.ts 删除分页参数），
// 于是「数据超 10000 行」的用户没有任何途径拿到文件——165 目标 × 133 页、
// 每页仅数十行的真实查询在累计到 10000 行时被判 budget_exceeded，
// 用户只看到笼统的「导出未通过完整性校验」。修复把上限改为可配置，
// 默认值保持与常量一字不差（行为不变），并按行/字节给出可区分 reason。

// pagedWorker 高效分页替身。
//
// 为什么不用既有的 cursorWorker：它在**每一页**都对全部 items 重排并逐行
// time.Parse（O(页数 × n log n)），在万行级导出用例里会跑到超时——
// 那是替身的开销，不是被测逻辑的开销。此处排序键只算一次，逐页线性定位。
type pagedWorker struct {
	id     string
	target string

	items []Event
	keys  []SortKey
}

func newPagedWorker(id, target string, items []Event) *pagedWorker {
	sorted := append([]Event(nil), items...)
	SortEventsInPlace(sorted)
	keys := make([]SortKey, len(sorted))
	for i := range sorted {
		keys[i] = sorted[i].SortKey()
	}
	return &pagedWorker{id: id, target: target, items: sorted, keys: keys}
}

func (w *pagedWorker) targetResult() []WorkerTargetResult {
	return []WorkerTargetResult{{TargetID: w.target, State: CoverageSuccess, CatalogGeneration: "1"}}
}

func (w *pagedWorker) OpenView(context.Context, WorkerSearchRequest) (*WorkerSearchResponse, error) {
	return &WorkerSearchResponse{ViewID: "wv-" + w.id, Targets: w.targetResult()}, nil
}

func (w *pagedWorker) Search(_ context.Context, req WorkerSearchRequest) (*WorkerSearchResponse, error) {
	start := 0
	if req.Cursor != "" {
		cursor, err := workerquery.DecodeCursor(req.Cursor)
		if err != nil {
			return nil, err
		}
		boundary := SortKey{
			EventTimeUTC:     MustParseRFC3339(cursor.SortKey.EventTimeUTC),
			LogSourceID:      cursor.SortKey.LogSourceID,
			SourceGeneration: cursor.SortKey.SourceGeneration,
			RecordStart:      cursor.SortKey.RecordStart,
			RecordEnd:        cursor.SortKey.RecordEnd,
			EventID:          cursor.SortKey.EventID,
		}
		for start < len(w.items) && w.keys[start].Compare(boundary) <= 0 {
			start++
		}
	}
	rest := w.items[start:]
	exhausted := true
	if req.Budget.Limit > 0 && len(rest) > req.Budget.Limit {
		rest = rest[:req.Budget.Limit]
		exhausted = false
	}
	return &WorkerSearchResponse{
		ViewID: req.ViewID, Items: append([]Event(nil), rest...),
		Exhausted: exhausted, NextCursor: "worker-next", Targets: w.targetResult(),
	}, nil
}

func (w *pagedWorker) Tail(ctx context.Context, req WorkerSearchRequest, _ string) (*WorkerSearchResponse, error) {
	return w.Search(ctx, req)
}

func (w *pagedWorker) Fields(context.Context, WorkerSearchRequest) (*WorkerFieldsResponse, error) {
	return &WorkerFieldsResponse{ViewID: w.id, Fields: []string{"level"}, Targets: w.targetResult()}, nil
}

func (w *pagedWorker) Stats(context.Context, WorkerSearchRequest, []string, string) (*WorkerStatsResponse, error) {
	return nil, nil
}

func (w *pagedWorker) Facets(context.Context, WorkerSearchRequest, []string, uint32) (*WorkerFacetsResponse, error) {
	return nil, nil
}

// exportBudgetFixture 构造 rows 行、每行约 1KiB 的导出场景。
// 消息放大到千字节级：这样阈值能落在「超过一页、不足两页」处，
// 使行数与字节两条上限可分别验证。
func exportBudgetFixture(t *testing.T, rows int) (*Coordinator, []Event) {
	t.Helper()
	const msgBytes = 1024
	big := strings.Repeat("x", msgBytes)
	items := make([]Event, 0, rows)
	for i := 0; i < rows; i++ {
		items = append(items, mkEvent(
			fmt.Sprintf("e%05d", i), "node:1", "g1", uint64(i+1),
			time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC).Add(time.Duration(i)*time.Minute).Format(time.RFC3339),
			"I", fmt.Sprintf("m%d-%s", i, big)))
	}
	resolver := &fakeTargetResolver{targets: []TargetInfo{{ID: "node:1", WorkerID: "w1", Readiness: ReadyOnline}}}
	dialer := newFakeDialer()
	dialer.clients["w1"] = newPagedWorker("w1", "node:1", items)
	return New(resolver, dialer), items
}

func exportQuery() Query {
	return Query{
		AuthorizedTargetIDs: []string{"node:1"},
		PrincipalKey:        "principal",
	}
}

// 默认配置（未注入）下上限仍是 10000 / 64MiB —— 行为与修复前一致。
func TestExportDefaultBudgetUnchanged(t *testing.T) {
	coord := New(&fakeTargetResolver{}, newFakeDialer())
	rows, bytes := coord.ExportBudgetLimits()
	require.Equal(t, DefaultExportRows, rows, "未注入时行上限必须保持默认 10000")
	require.Equal(t, uint64(DefaultExportBytes), bytes, "未注入时字节上限必须保持默认 64MiB")
	require.Equal(t, 10_000, DefaultExportRows, "默认行上限是既有常量值，不得改动")
	require.Equal(t, uint64(64<<20), uint64(DefaultExportBytes))

	// 零值注入（生产装配 Assemble 的默认形态）同样回退默认。
	coord.SetExportBudget(ExportBudget{})
	rows, bytes = coord.ExportBudgetLimits()
	require.Equal(t, DefaultExportRows, rows)
	require.Equal(t, uint64(DefaultExportBytes), bytes)
}

// 默认上限下，超过 10000 行的数据仍然被拒 —— 证明「默认行为不变」不是空话。
func TestExportDefaultBudgetStillRejectsOverflow(t *testing.T) {
	coord, _ := exportBudgetFixture(t, DefaultExportRows+200)
	coord.SetExportBudget(ExportBudget{}) // 显式走默认

	res, err := coord.Export(context.Background(), exportQuery())
	require.NoError(t, err)
	require.True(t, res.ExportIncomplete, "默认上限下超行数必须判为不完整")
	require.Nil(t, res.Artifact, "不完整时不得下发附件")
	require.Contains(t, res.IncompleteReasons, "row_limit_exceeded")
	require.LessOrEqual(t, len(res.Items), DefaultExportRows+DefaultPageLimit,
		"超限后应立即停止取页，而不是继续读全量")
}

// 配置调大后，同一份数据能通过（并给出成功附件）。
func TestExportConfiguredBudgetAllowsLargerExport(t *testing.T) {
	const rows = DefaultExportRows + 500
	coord, _ := exportBudgetFixture(t, rows)
	coord.SetExportBudget(ExportBudget{MaxRows: rows + 100, MaxBytes: 1 << 30})

	res, err := coord.Export(context.Background(), exportQuery())
	require.NoError(t, err)
	require.False(t, res.ExportIncomplete, "上限调大后同一份数据应完整导出")
	require.NotNil(t, res.Artifact, "完整导出必须产出附件")
	require.Len(t, res.Items, rows)
	require.Equal(t, rows+100, res.Limits.MaxRows, "响应需回传本次实际采用的上限")
	require.Equal(t, rows, res.Limits.Rows)
}

// 请求参数只能收紧，不能放宽：否则任意调用方传大 limit 即可把有界导出变成无界入口。
func TestExportRequestBudgetOnlyTightens(t *testing.T) {
	coord, _ := exportBudgetFixture(t, 300)
	coord.SetExportBudget(ExportBudget{MaxRows: 1000, MaxBytes: 1 << 30})

	// 请求要 1e6 行 → 仍按配置的 1000 行执行。
	q := exportQuery()
	q.Budget.Limit = 1_000_000
	res, err := coord.Export(context.Background(), q)
	require.NoError(t, err)
	require.False(t, res.ExportIncomplete)
	require.Equal(t, 1000, res.Limits.MaxRows, "请求侧不得放大服务端上限")

	// 请求要 10 行 → 按 10 行收紧（仍不完整，因为数据有 300 行）。
	q2 := exportQuery()
	q2.Budget.Limit = 10
	res2, err := coord.Export(context.Background(), q2)
	require.NoError(t, err)
	require.True(t, res2.ExportIncomplete, "请求收紧到 10 行时 300 行数据必然超限")
	require.Equal(t, 10, res2.Limits.MaxRows, "请求侧收紧必须生效")
	require.Contains(t, res2.IncompleteReasons, "row_limit_exceeded")
}

// 非法配置值（负数 / 0）回退默认，绝不产出不可用预算。
// 配置层另有 fail-fast 校验（config.ValidateLogExportConfig），此处保证纵深防御。
func TestExportBudgetInvalidValuesFallBackToDefault(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget ExportBudget
	}{
		{name: "行数为负", budget: ExportBudget{MaxRows: -1}},
		{name: "行数为零", budget: ExportBudget{MaxRows: 0}},
		{name: "字节为零", budget: ExportBudget{MaxBytes: 0}},
		{name: "全零", budget: ExportBudget{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			coord := New(&fakeTargetResolver{}, newFakeDialer())
			coord.SetExportBudget(tc.budget)
			rows, bytes := coord.ExportBudgetLimits()
			require.Equal(t, DefaultExportRows, rows)
			require.Equal(t, uint64(DefaultExportBytes), bytes)
		})
	}
}

// 超限 reason 必须能区分「行数」与「字节」：前端据此给出不同文案。
func TestExportReasonsDistinguishRowFromByteLimit(t *testing.T) {
	const rows = 500

	t.Run("行数超限 → row_limit_exceeded 且不含 byte_budget_exceeded", func(t *testing.T) {
		coord, _ := exportBudgetFixture(t, rows)
		// 字节上限放到极大，只让行数成为唯一瓶颈。
		coord.SetExportBudget(ExportBudget{MaxRows: 100, MaxBytes: 1 << 40})

		res, err := coord.Export(context.Background(), exportQuery())
		require.NoError(t, err)
		require.True(t, res.ExportIncomplete)
		require.Contains(t, res.IncompleteReasons, "row_limit_exceeded")
		require.Contains(t, res.IncompleteReasons, "budget_exceeded", "既有 wire 契约码必须保留")
		require.NotContains(t, res.IncompleteReasons, "byte_budget_exceeded")
		require.Equal(t, 100, res.Limits.MaxRows)
	})

	t.Run("字节超限 → byte_budget_exceeded 且不含 row_limit_exceeded", func(t *testing.T) {
		coord, _ := exportBudgetFixture(t, rows)
		// 行数上限放到极大，只让字节成为唯一瓶颈：500 行 × 约 1KiB ≈ 500KiB，
		// 阈值取 300KiB ⇒ 单页（200 行 ≈ 200KiB）不越界、第二页累计越界。
		coord.SetExportBudget(ExportBudget{MaxRows: 1 << 20, MaxBytes: 300_000})

		res, err := coord.Export(context.Background(), exportQuery())
		require.NoError(t, err)
		require.True(t, res.ExportIncomplete)
		require.Contains(t, res.IncompleteReasons, "byte_budget_exceeded")
		require.Contains(t, res.IncompleteReasons, "budget_exceeded", "既有 wire 契约码必须保留")
		require.NotContains(t, res.IncompleteReasons, "row_limit_exceeded")
		require.Equal(t, uint64(300_000), res.Limits.MaxBytes)
	})
}

// BuildExport 的 reason 判定与 Limits 回传（不经 Coordinator 的直接单元验证）。
func TestBuildExportReasonAndLimits(t *testing.T) {
	items := []Event{
		mkEvent("e1", "s", "g", 2, "2026-09-20T12:00:00Z", "I", "a"),
		mkEvent("e2", "s", "g", 1, "2026-09-20T11:00:00Z", "I", "b"),
	}

	t.Run("行数超限", func(t *testing.T) {
		res := BuildExport(View{Budget: QueryBudget{Limit: 1, MaxBytes: 1 << 30}},
			Coverage{Complete: true}, Quality{}, items, true, true)
		assert.True(t, res.ExportIncomplete)
		assert.True(t, res.BudgetExceeded)
		assert.Nil(t, res.Artifact)
		assert.Contains(t, res.IncompleteReasons, "row_limit_exceeded")
		assert.Contains(t, res.IncompleteReasons, "budget_exceeded")
		assert.NotContains(t, res.IncompleteReasons, "byte_budget_exceeded")
		assert.Equal(t, 1, res.Limits.MaxRows)
		assert.Equal(t, len(items), res.Limits.Rows)
	})

	t.Run("字节超限", func(t *testing.T) {
		res := BuildExport(View{Budget: QueryBudget{Limit: 100, MaxBytes: 1}},
			Coverage{Complete: true}, Quality{}, items, true, true)
		assert.True(t, res.ExportIncomplete)
		assert.True(t, res.BudgetExceeded)
		assert.Contains(t, res.IncompleteReasons, "byte_budget_exceeded")
		assert.NotContains(t, res.IncompleteReasons, "row_limit_exceeded")
	})

	t.Run("完整通过时 Limits 仍回传且无预算 reason", func(t *testing.T) {
		res := BuildExport(View{Budget: QueryBudget{Limit: 100, MaxBytes: 1 << 30}},
			Coverage{Complete: true}, Quality{}, items, false, false)
		assert.False(t, res.ExportIncomplete)
		assert.NotNil(t, res.Artifact)
		assert.Equal(t, 100, res.Limits.MaxRows)
		assert.Equal(t, len(items), res.Limits.Rows)
	})
}
