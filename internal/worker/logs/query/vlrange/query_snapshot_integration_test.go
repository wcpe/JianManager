package vlrange

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// queryApprovedVL 起一个真实的批准版 VL 实例。
//
// 2026-10-29：冷热双层废除后只剩一个实例，故不再有 withCold 开关、也不再返回 cold 客户端。
// 原「通过物理迁移钉住 view」的集成用例随之删除——迁移链已不存在，
// 「有在途读取时物理日不得退休」这条不变量改由 Catalog.CanRetirePhysicalDay 的单元回归覆盖
// （见 internal/worker/logs/query 的同名回归）。
func queryApprovedVL(t *testing.T) (context.Context, string, *vlsup.Supervisor, *vlsup.Client) {
	t.Helper()
	binary := os.Getenv("JM_VL_BIN")
	if binary == "" {
		t.Skip("未提供本地批准 VL 副本")
	}
	require.NoError(t, vlsup.VerifyAsset(binary, vlsup.CurrentApprovedExeSHA256()))
	root := t.TempDir()
	ports := make(map[vlsup.Namespace]int)
	namespaces := []vlsup.Namespace{vlsup.NamespaceHot}
	for _, namespace := range namespaces {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		ports[namespace] = listener.Addr().(*net.TCPAddr).Port
		require.NoError(t, listener.Close())
	}
	supervisor, err := vlsup.New(vlsup.Options{BinaryPath: binary, AssetSHA256: vlsup.CurrentApprovedExeSHA256(),
		DataRoot: root, AuthUsername: "query-fixture", AuthPassword: "isolated-only", RetentionPeriod: "730d",
		Ports: ports, MemoryAllowedBytes: map[vlsup.Namespace]int64{vlsup.NamespaceHot: 64 << 20}})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	for _, namespace := range namespaces {
		require.NoError(t, supervisor.Start(ctx, namespace))
		require.Eventually(t, func() bool { return supervisor.Health(ctx, namespace) == nil }, 10*time.Second, 25*time.Millisecond)
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		supervisor.StopAll(stop)
	})
	hot, err := supervisor.ClientFor(vlsup.NamespaceHot)
	require.NoError(t, err)
	t.Logf("批准 SHA=%s 仅回环端口=%v 独立根=%s", vlsup.CurrentApprovedExeSHA256(), ports, root)
	return ctx, root, supervisor, hot
}

func queryPhysicalEvent(source, sourceGeneration string, end uint64, at time.Time, message string) logtypes.Event {
	return logtypes.BuildEvent(logtypes.SourceIdentity{LogSourceID: source, SourceGeneration: sourceGeneration, ParserVersion: "fixture/v1"},
		logtypes.RecordRange{Start: end - 1, End: end}, at.Format(time.RFC3339Nano), at.Add(time.Second).Format(time.RFC3339Nano), "INFO", "stdout", message)
}

func queryInsertPhysical(t *testing.T, ctx context.Context, vl *vlsup.Client, generation string, events []logtypes.Event) {
	t.Helper()
	var body strings.Builder
	for _, event := range events {
		row := map[string]any{"_time": event.EventTimeUTC, "_msg": event.Message, "event_id": event.EventID,
			"log_source_id": event.Source.LogSourceID, "source_generation": event.Source.SourceGeneration,
			"parser_version": event.Source.ParserVersion, "record_start": event.Record.Start, "record_end": event.Record.End,
			"ingest_time_utc": event.IngestTimeUTC, "level": event.Level, "stream": event.Stream,
			"canonical_content_hash": event.CanonicalHash, "projection_generation": generation}
		encoded, err := json.Marshal(row)
		require.NoError(t, err)
		body.Write(encoded)
		body.WriteByte('\n')
	}
	_, err := vl.InsertJSONLines(ctx, []byte(body.String()))
	require.NoError(t, err)
	// 隔离测试主动封闭本次可见窗口，不把 HTTP health 当成发布证据。
	_, err = vl.Post(ctx, "/internal/force_flush", nil)
	require.NoError(t, err)
}

func queryIdentityDigest(events []logtypes.Event) string {
	type identity struct {
		ID, Hash, Source, Generation string
		Start, End                   uint64
		Time                         string
	}
	values := make([]identity, 0, len(events))
	for _, event := range events {
		values = append(values, identity{event.EventID, event.CanonicalHash, event.Source.LogSourceID,
			event.Source.SourceGeneration, event.Record.Start, event.Record.End, event.EventTimeUTC})
	}
	encoded, _ := json.Marshal(values)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func TestRealVLQueryFixedViewAppendPaginationKeepsSearchExportSet(t *testing.T) {
	ctx, _, _, hot := queryApprovedVL(t)
	client, err := New(hot)
	require.NoError(t, err)
	day := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	key := catalog.PartitionKey{StorageNamespace: "fixed-view", UTCDay: day.Format("2006-01-02")}
	var initial []logtypes.Event
	for i, source := range []string{"slow", "fast", "slow", "fast", "slow", "fast", "slow"} {
		end := uint64(10 * (i + 1))
		if source == "fast" {
			end += 500
		}
		initial = append(initial, queryPhysicalEvent(source, "s1", end, day.Add(12*time.Hour-time.Duration(i/2)*time.Minute), fmt.Sprintf("原集合-%d", i)))
	}
	queryInsertPhysical(t, ctx, hot, "base", initial)
	cat := catalog.New(nil)
	record := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-fixed")
	record.PublishedProjection = &catalog.PublishedProjection{ManifestVersion: "initial", QueryLocationDirID: "hot-fixed", QueryGeneration: 1,
		CoverageComplete: true, ClosedVisibleSeq: map[string]uint64{key.String(): 1000}, SourceProjections: []catalog.SourceProjection{
			{SourceGenerationRef: catalog.SourceGenerationRef{LogSourceID: "slow", SourceGeneration: "s1"}, ProjectionGenerations: []string{"base"}, ClosedVisibleSeq: 100},
			{SourceGenerationRef: catalog.SourceGenerationRef{LogSourceID: "fast", SourceGeneration: "s1"}, ProjectionGenerations: []string{"base"}, ClosedVisibleSeq: 1000},
		}}
	require.NoError(t, cat.PublishProjection(nil, record))
	planner := query.NewPlanner(cat, nil)
	t.Cleanup(func() { require.NoError(t, planner.Close()) })
	svc := query.NewService(planner, client, "real-fixed-append")
	req := query.QueryRequest{AuthorizedTargets: []string{key.String()}, Budget: query.Budget{Limit: 100, MaxBytes: 1 << 20}}
	baseline := svc.Search(ctx, req)
	require.Nil(t, baseline.Err)
	require.True(t, baseline.Coverage.Complete)
	require.Len(t, baseline.Items, len(initial))
	wantDigest := queryIdentityDigest(baseline.Items)
	req.View = &query.ViewRef{ViewID: baseline.View.ViewID}
	req.Budget.Limit = 2
	pages, totalAppends := 0, 0
	for _, exportMode := range []bool{false, true} {
		var exported []logtypes.Event
		req.View.Cursor = ""
		req.AllowProjectionDrift = exportMode
		pages = 0
		for pages < 10 {
			pages++
			totalAppends++
			// 每页前持续追加同物理代次，特意保持分区 max=1000，只有 slow 的真实前缀推进。
			late := queryPhysicalEvent("slow", "s1", uint64(100+totalAppends*10), day.Add(13*time.Hour-time.Duration(totalAppends)*time.Second), fmt.Sprintf("视图后追加-%d", totalAppends))
			queryInsertPhysical(t, ctx, hot, "base", []logtypes.Event{late})
			expected, _ := cat.Get(key)
			next := expected.Clone()
			next.PublishedProjection.ManifestVersion = fmt.Sprintf("append-%d", totalAppends)
			next.PublishedProjection.SourceProjections[0].ClosedVisibleSeq = late.Record.End
			require.NoError(t, cat.PublishProjection(expected, next))
			page := svc.Search(ctx, req)
			require.Nil(t, page.Err)
			require.True(t, page.Coverage.Complete)
			require.Equal(t, baseline.View.ViewID, page.View.ViewID)
			exported = append(exported, page.Items...)
			if page.Exhausted {
				break
			}
			require.NotEmpty(t, page.NextCursor, "非末页必须有已证明的安全边界")
			req.View.Cursor = page.NextCursor
		}
		require.Less(t, pages, 10)
		require.Greater(t, pages, 1)
		require.Equal(t, eventIDs(baseline.Items), eventIDs(exported))
		require.Equal(t, wantDigest, queryIdentityDigest(exported), "持续追加普通列表/导出多页必须与同 view Search 的身份、顺序和规范哈希完全一致")
		t.Logf("普通/导出模式 export=%v 真实页数=%d 固定行数=%d 固定哈希=%s", exportMode, pages, len(exported), wantDigest)
	}
	req.Budget.Limit, req.View.Cursor, req.AllowProjectionDrift = 100, "", false
	unchanged := svc.Search(ctx, req)
	require.Nil(t, unchanged.Err)
	require.Equal(t, wantDigest, queryIdentityDigest(unchanged.Items))
	stats := svc.Stats(ctx, req, nil)
	require.Nil(t, stats.Err)
	require.True(t, stats.Coverage.Complete)
	var count uint64
	for _, point := range stats.Points {
		count += point.Count
	}
	require.EqualValues(t, len(initial), count)
	req.View = nil
	fresh := svc.Search(ctx, req)
	require.Nil(t, fresh.Err)
	require.Len(t, fresh.Items, len(initial)+totalAppends, "对照：新 view 确实读得到已追加物理行")
	t.Logf("真实页数=%d 原集合=%d 新集合=%d 固定有序身份哈希=%s", pages, len(initial), len(fresh.Items), wantDigest)
}

type queryCountingRange struct {
	inner query.RangeClient
	calls atomic.Int32
}

func (c *queryCountingRange) Search(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery) (query.RangeResult, error) {
	c.calls.Add(1)
	return c.inner.Search(ctx, rng, q)
}
func (c *queryCountingRange) Stats(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery) (query.StatsResult, error) {
	return c.inner.Stats(ctx, rng, q)
}
func (c *queryCountingRange) Tail(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery, mode query.TailMode) (query.RangeResult, error) {
	return c.inner.Tail(ctx, rng, q, mode)
}

func TestRealVLQueryLegacyJournalZeroAndMissingPrefixThreeRestarts(t *testing.T) {
	ctx, root, _, hot := queryApprovedVL(t)
	client, err := New(hot)
	require.NoError(t, err)
	day := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	for _, shape := range []string{"missing-all", "missing-slow", "missing-source-identity", "legacy-target-zero", "legacy-zero", "legacy-proven", "legacy-single-default", "modern-zero"} {
		t.Run(shape, func(t *testing.T) {
			key := catalog.PartitionKey{StorageNamespace: "legacy-namespace", UTCDay: day.Format("2006-01-02")}
			generation := "legacy-" + shape
			projection := &catalog.PublishedProjection{ManifestVersion: generation, ProjectionGeneration: generation,
				CoverageComplete: true, QueryLocationDirID: "legacy-hot", QueryGeneration: 1,
				CoveredSourceGenerations: []catalog.SourceGenerationRef{{LogSourceID: "legacy-a", SourceGeneration: "s1"}, {LogSourceID: "legacy-b", SourceGeneration: "s1"}}}
			wantCount, ready := 0, true
			switch shape {
			case "missing-all":
				projection.CoveredSourceGenerations = nil
				ready = false
			case "missing-slow":
				projection.ClosedVisibleSeq = map[string]uint64{"legacy-b/s1": 1000, "default": 1000}
				ready = false
			case "missing-source-identity":
				projection.CoveredSourceGenerations = nil
				projection.ClosedVisibleSeq = map[string]uint64{"default": 1000}
				ready = false
			case "legacy-target-zero":
				projection.CoveredSourceGenerations = projection.CoveredSourceGenerations[:1]
				projection.ClosedVisibleSeq = map[string]uint64{key.String(): 0, "default": 1000}
			case "legacy-zero":
				projection.ClosedVisibleSeq = map[string]uint64{"legacy-a/s1": 0, "legacy-b/s1": 1000, "default": 1000}
				wantCount = 1
			case "legacy-proven":
				projection.ClosedVisibleSeq = map[string]uint64{"legacy-a/s1": 10, "legacy-b/s1": 20}
				wantCount = 2
			case "legacy-single-default":
				projection.CoveredSourceGenerations = projection.CoveredSourceGenerations[:1]
				projection.ClosedVisibleSeq = map[string]uint64{"default": 10}
				wantCount = 1
			case "modern-zero":
				projection.SourceProjections = []catalog.SourceProjection{{SourceGenerationRef: catalog.SourceGenerationRef{LogSourceID: "legacy-a", SourceGeneration: "s1"}, ProjectionGenerations: []string{generation}, ClosedVisibleSeq: 0}}
				projection.ClosedVisibleSeq = map[string]uint64{"default": 1000}
			}
			queryInsertPhysical(t, ctx, hot, generation, []logtypes.Event{queryPhysicalEvent("legacy-a", "s1", 10, day.Add(12*time.Hour), shape+"-a"), queryPhysicalEvent("legacy-b", "s1", 20, day.Add(11*time.Hour), shape+"-b")})
			journalPath := filepath.Join(root, shape+".jsonl")
			store, err := catalog.NewJSONLFileStore(journalPath)
			require.NoError(t, err)
			legacy := catalog.JournalEntry{Seq: 1, Key: key, State: catalog.StateCleaned, Owner: catalog.OwnerHot, Generation: 1,
				DirID: "legacy-hot", JournalComplete: true, PublishedProjection: projection,
				Dirs: []catalog.PhysicalDir{{ID: "legacy-hot", Role: catalog.DirOwner}}}
			require.NoError(t, store.AppendEntry(legacy))
			for restart := 1; restart <= 3; restart++ {
				journal, err := catalog.NewJournalWithStore(store)
				require.NoError(t, err)
				cat := catalog.New(journal)
				planner := query.NewPlanner(cat, nil)
				counting := &queryCountingRange{inner: client}
				response := query.NewService(planner, counting, "legacy-replay").Search(ctx, query.QueryRequest{AuthorizedTargets: []string{key.String()}, Budget: query.Budget{Limit: 100}})
				if ready {
					require.Nil(t, response.Err)
					require.True(t, response.Coverage.Complete)
					require.Len(t, response.Items, wantCount)
					view, ok := planner.GetView(response.View.ViewID)
					require.True(t, ok)
					require.NotEmpty(t, view.Projections[key.String()].SourceProjections, "充分证据须升级为现代逐源约束")
					if shape == "legacy-target-zero" {
						require.Zero(t, view.ClosedVisibleSeq[key.String()], "明确的目标零前缀不能借 default 的更大值")
					}
				} else {
					require.False(t, response.Coverage.Complete)
					require.False(t, response.Exhausted)
					require.Empty(t, response.Items)
					require.Zero(t, counting.calls.Load(), "未知旧前缀不能触碰 VL 更不能解释成无限")
				}
				record, ok := cat.Get(key)
				require.True(t, ok)
				require.Equal(t, projection, record.PublishedProjection, "升级只改变当前查询快照，原旧 journal 不可伪造或覆盖")
				require.NoError(t, planner.Close())
				t.Logf("真实旧 journal 三启 shape=%s round=%d ready=%v rows=%d VL_reads=%d", shape, restart, ready, len(response.Items), counting.calls.Load())
			}
		})
	}
}

type queryBlockingRange struct {
	inner   query.RangeClient
	once    sync.Once
	entered chan query.AuthoritativeRange
	exit    chan struct{}
}

func (c *queryBlockingRange) Search(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery) (query.RangeResult, error) {
	c.once.Do(func() { c.entered <- rng; <-c.exit })
	return c.inner.Search(ctx, rng, q)
}
func (c *queryBlockingRange) Stats(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery) (query.StatsResult, error) {
	return c.inner.Stats(ctx, rng, q)
}
func (c *queryBlockingRange) Tail(ctx context.Context, rng query.AuthoritativeRange, q query.RangeQuery, mode query.TailMode) (query.RangeResult, error) {
	return c.inner.Tail(ctx, rng, q, mode)
}
