package logcoord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	workerquery "github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/query/vlrange"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
	"github.com/wcpe/JianManager/proto/workerpb"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// The transport meters actual response-body Reader bytes. It exercises the
// production vlrange/vlsup clients, CP adapter, protobuf and Worker Service.
// A result-size estimate or a server-side write count cannot prove this bound.
type explorationTransport struct {
	fields   string
	values   string
	nonempty map[string]uint64
	waitPath string
	bytes    atomic.Uint64
	mu       sync.Mutex
	requests []*http.Request
}

type explorationBody struct {
	io.Reader
	bytes *atomic.Uint64
}

func (b *explorationBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.bytes.Add(uint64(n))
	return n, err
}
func (*explorationBody) Close() error { return nil }
func (m *explorationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	m.mu.Lock()
	m.requests = append(m.requests, req)
	m.mu.Unlock()
	if req.URL.Path == m.waitPath {
		<-req.Context().Done()
		return nil, req.Context().Err()
	}
	body := m.fields
	if strings.HasSuffix(req.URL.Path, "/field_values") {
		body = m.values
	}
	if req.URL.Path == "/select/logsql/query" {
		counts := make(map[string]uint64)
		for _, match := range regexp.MustCompile(`NOT ([A-Za-z_][A-Za-z0-9_]*):=""\) as (__jm_field_[0-9]+)`).FindAllStringSubmatch(req.URL.Query().Get("query"), -1) {
			count, ok := m.nonempty[match[1]]
			if !ok {
				count = 1
			}
			counts[match[2]] = count
		}
		encoded, _ := json.Marshal(counts)
		body = string(encoded)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: &explorationBody{Reader: strings.NewReader(body), bytes: &m.bytes}, Request: req}, nil
}
func (m *explorationTransport) count() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.requests) }

func explorationFixture(t *testing.T, m *explorationTransport) (*Coordinator, *review05PB, Query) {
	t.Helper()
	if m.fields == "" {
		m.fields = `{"values":[{"value":"trace_id","hits":3},{"value":"level","hits":3},{"value":"latency_ms","hits":3}]}`
	}
	if m.values == "" {
		m.values = `{"values":[{"value":"trace-1","hits":3}]}`
	}
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: "http://127.0.0.1:1", HTTPClient: &http.Client{Transport: m}})
	require.NoError(t, err)
	ranges, err := vlrange.New(vl)
	require.NoError(t, err)
	cat, _ := review05Catalog(t)
	coord, pb := review05Coordinator(t, cat, ranges)
	q := review05Query()
	q.ExploreFields = true
	q.Filter = `level:="INFO"`
	view, err := coord.CreateView(context.Background(), q)
	require.NoError(t, err)
	q.ViewID = view.ViewID
	return coord, pb, q
}

func TestExploreFieldsRejectsUnsafeReservedAndUnknownDimensions(t *testing.T) {
	for _, name := range []string{"event_id", "canonical_content_hash", "source_generation", "record_start", "record_end", "_time", "_msg", "ingest_time", "ingest_time_utc", "projection_generation", "message", "trace_id|stats", `trace_id"`, "bad field", strings.Repeat("x", 129), "not_present", "unknown.field", "unknown-field"} {
		t.Run(name, func(t *testing.T) {
			m := &explorationTransport{}
			coord, _, q := explorationFixture(t, m)
			out, err := coord.Facets(context.Background(), FacetsQuery{Query: q, Dimensions: []string{name}, DimensionLimit: 20})
			require.NoError(t, err)
			require.True(t, out.ExploreApplied)
			require.False(t, out.Coverage.Complete)
			require.Contains(t, out.Coverage.PartialReasons, "UNSUPPORTED")
			require.Empty(t, out.Dimensions)
			if name == "not_present" || name == "unknown.field" || name == "unknown-field" {
				require.Equal(t, 1, m.count())
			} else {
				require.Zero(t, m.count())
			}
		})
	}
}

func TestExploreFieldsFiniteWindowAuthorizationAndFacetShape(t *testing.T) {
	m := &explorationTransport{}
	coord, _, q := explorationFixture(t, m)
	for _, dims := range [][]string{nil, {"trace_id", "level"}} {
		_, err := coord.Facets(context.Background(), FacetsQuery{Query: q, Dimensions: dims, DimensionLimit: 20})
		require.ErrorIs(t, err, ErrExplorationInvalid)
	}
	for _, limit := range []uint32{0, 21, 1000} {
		_, err := coord.Facets(context.Background(), FacetsQuery{Query: q, Dimensions: []string{"trace_id"}, DimensionLimit: limit})
		require.ErrorIs(t, err, ErrExplorationInvalid)
	}
	bad := q
	bad.PrincipalKey = "other-user"
	_, err := coord.Fields(context.Background(), bad)
	require.ErrorIs(t, err, ErrViewAuthz)
	bad = q
	bad.AuthorizedTargetIDs = []string{"inst:2"}
	_, err = coord.Fields(context.Background(), bad)
	require.ErrorIs(t, err, ErrViewAuthz)
	for _, bounds := range [][2]string{{"", ""}, {q.FromUTC, ""}, {"now-1h", "now"}, {q.ToUTC, q.FromUTC}} {
		bad = q
		bad.ViewID = ""
		bad.FromUTC, bad.ToUTC = bounds[0], bounds[1]
		_, err = coord.Fields(context.Background(), bad)
		require.ErrorIs(t, err, ErrExplorationInvalid)
	}
	require.Zero(t, m.count())
	// CP keeps its existing explicit-bound View reuse contract.
	bad = q
	bad.FromUTC, bad.ToUTC = "", ""
	_, err = coord.Fields(context.Background(), bad)
	require.ErrorIs(t, err, ErrViewReuseMismatch)
	out, err := coord.Fields(context.Background(), q)
	require.NoError(t, err)
	require.Contains(t, out.Fields, "trace_id")
	request := m.requests[0]
	require.Equal(t, "2026-09-22T00:00:00Z", request.URL.Query().Get("start"))
	require.Equal(t, `level:="INFO"`, request.URL.Query().Get("extra_filters"))
	require.Contains(t, request.URL.Query().Get("query"), "src-a")
	require.Contains(t, request.URL.Query().Get("query"), "g1")
}

func TestExploreFieldsMetadataAndValuesShareActualByteFanoutAndDeadlineBudget(t *testing.T) {
	t.Run("bytes_not_result_limit", func(t *testing.T) {
		m := &explorationTransport{values: `{"values":[{"value":"` + strings.Repeat("large", 1000) + `","hits":1}]}`}
		coord, _, q := explorationFixture(t, m)
		q.Budget.MaxBytes = 256
		out, err := coord.Facets(context.Background(), FacetsQuery{Query: q, Dimensions: []string{"trace_id"}, DimensionLimit: 1})
		require.NoError(t, err)
		require.Equal(t, uint64(256), m.bytes.Load(), "field_names and field_values must share one actual-byte allowance")
		require.Equal(t, 3, m.count())
		require.False(t, out.Coverage.Complete)
		require.Contains(t, out.Coverage.PartialReasons, "BUDGET_EXCEEDED")
		require.True(t, out.Truncated)
		require.Equal(t, StatsQPartial, out.Quality.StatsQuality)
	})
	t.Run("physical_fanout_includes_preflight", func(t *testing.T) {
		m := &explorationTransport{}
		coord, _, q := explorationFixture(t, m)
		q.Budget.MaxFanout = 1
		out, err := coord.Facets(context.Background(), FacetsQuery{Query: q, Dimensions: []string{"trace_id"}, DimensionLimit: 20})
		require.NoError(t, err)
		require.Equal(t, 1, m.count())
		require.Contains(t, out.Coverage.PartialReasons, "BUDGET_EXCEEDED")
		require.Empty(t, out.Dimensions)
	})
	t.Run("deadline_never_restarts_after_metadata", func(t *testing.T) {
		m := &explorationTransport{waitPath: "/select/logsql/field_values"}
		coord, _, q := explorationFixture(t, m)
		q.Budget.TimeoutMS = 30
		started := time.Now()
		out, err := coord.Facets(context.Background(), FacetsQuery{Query: q, Dimensions: []string{"trace_id"}, DimensionLimit: 20})
		require.NoError(t, err)
		require.Less(t, time.Since(started), time.Second)
		require.Contains(t, out.Coverage.PartialReasons, "BUDGET_EXCEEDED")
		require.Equal(t, 3, m.count())
		first, _ := m.requests[0].Context().Deadline()
		second, _ := m.requests[1].Context().Deadline()
		require.Equal(t, first, second)
		for _, request := range m.requests {
			timeout, err := time.ParseDuration(request.URL.Query().Get("timeout"))
			require.NoError(t, err)
			require.LessOrEqual(t, timeout, 30*time.Millisecond)
		}
	})
	t.Run("default_hard_cap_and_only_tightening", func(t *testing.T) {
		for _, budget := range []QueryBudget{{}, {MaxBytes: 32 << 20, TimeoutMS: 60000, MaxFanout: 1000}} {
			m := &explorationTransport{fields: `{"values":[{"value":"` + strings.Repeat("f", 2<<20) + `","hits":1}]}`}
			coord, _, q := explorationFixture(t, m)
			q.Budget = budget
			out, err := coord.Fields(context.Background(), q)
			require.NoError(t, err)
			require.True(t, out.Truncated)
			require.Equal(t, uint64(1<<20), m.bytes.Load())
			deadline, _ := m.requests[0].Context().Deadline()
			require.LessOrEqual(t, time.Until(deadline), 5*time.Second)
		}
	})
}

func TestExploreFieldsEnumerationAndHighCardinalityAreExplicitlyTruncated(t *testing.T) {
	t.Run("field_count", func(t *testing.T) {
		var values []map[string]any
		for i := 0; i < 300; i++ {
			values = append(values, map[string]any{"value": fmt.Sprintf("user_field_%03d", i), "hits": 1})
		}
		for _, name := range []string{"event_id", "_msg", "_time", "source_generation", "ingest_time", "bad|stats"} {
			values = append(values, map[string]any{"value": name, "hits": 1})
		}
		body, err := json.Marshal(map[string]any{"values": values})
		require.NoError(t, err)
		m := &explorationTransport{fields: string(body)}
		coord, _, q := explorationFixture(t, m)
		out, err := coord.Fields(context.Background(), q)
		require.NoError(t, err)
		require.Len(t, out.Fields, 256)
		require.True(t, out.Truncated)
		require.False(t, out.Coverage.Complete)
		require.Contains(t, out.Coverage.PartialReasons, "BUDGET_EXCEEDED")
		require.NotContains(t, out.Fields, "event_id")
		require.NotContains(t, out.Fields, "_msg")
	})
	t.Run("facet_values", func(t *testing.T) {
		var values []map[string]any
		for i := 0; i < 30; i++ {
			values = append(values, map[string]any{"value": fmt.Sprintf("trace-%03d", i), "hits": 31 - i})
		}
		body, err := json.Marshal(map[string]any{"values": values})
		require.NoError(t, err)
		m := &explorationTransport{values: string(body)}
		coord, _, q := explorationFixture(t, m)
		out, err := coord.Facets(context.Background(), FacetsQuery{Query: q, Dimensions: []string{"trace_id"}, DimensionLimit: 20})
		require.NoError(t, err)
		require.True(t, out.Truncated)
		require.False(t, out.Coverage.Complete)
		require.Equal(t, StatsQPartial, out.Quality.StatsQuality)
		require.Len(t, out.Dimensions, 1)
		require.Len(t, out.Dimensions[0].Values, 20)
		require.Equal(t, uint64(31), out.Dimensions[0].Values[0].Count, "local trimming must not turn real hits into zero")
		for _, request := range m.requests {
			require.Empty(t, request.URL.Query().Get("limit"), "VL limit would zero hits under truncation")
		}
	})
}

type ignoreExplorePB struct{ *review05PB }

func (p *ignoreExplorePB) LogFields(ctx context.Context, req *workerpb.LogFieldsRequest, _ ...grpc.CallOption) (*workerpb.LogFieldsResponse, error) {
	old := proto.Clone(req).(*workerpb.LogFieldsRequest)
	old.ExploreFields = false
	return p.svc.LogFields(ctx, old)
}
func (p *ignoreExplorePB) LogFacets(ctx context.Context, req *workerpb.LogFacetsRequest, _ ...grpc.CallOption) (*workerpb.LogFacetsResponse, error) {
	old := proto.Clone(req).(*workerpb.LogFacetsRequest)
	old.ExploreFields = false
	return p.svc.LogFacets(ctx, old)
}
func TestExploreFieldsOldWorkerCannotSilentlyIgnoreOptIn(t *testing.T) {
	m := &explorationTransport{}
	coord, pb, q := explorationFixture(t, m)
	coord.dialer.(*fakeDialer).clients["w1"] = NewWorkerClientAdapter(&ignoreExplorePB{pb})
	fields, err := coord.Fields(context.Background(), q)
	require.NoError(t, err)
	require.True(t, fields.ExploreApplied)
	require.Empty(t, fields.Fields)
	require.False(t, fields.Coverage.Complete)
	require.Contains(t, fields.Coverage.PartialReasons, "LOG_UNSUPPORTED")
	facets, err := coord.Facets(context.Background(), FacetsQuery{Query: q, Dimensions: []string{"level"}, DimensionLimit: 20})
	require.NoError(t, err)
	require.True(t, facets.ExploreApplied)
	require.Empty(t, facets.Dimensions)
	require.False(t, facets.Coverage.Complete)
	require.Contains(t, facets.Coverage.PartialReasons, "LOG_UNSUPPORTED")
	q.ExploreFields = false
	legacy, err := coord.Fields(context.Background(), q)
	require.NoError(t, err)
	require.False(t, legacy.ExploreApplied)
	require.True(t, legacy.Coverage.Complete)
	require.Contains(t, legacy.Fields, "level")
	require.NotContains(t, legacy.Fields, "trace_id")
}

func TestExploreFieldsCannotResetAnExistingParentPhysicalBudget(t *testing.T) {
	t.Run("bytes", func(t *testing.T) {
		m := &explorationTransport{values: `{"values":[{"value":"` + strings.Repeat("x", 2000) + `","hits":1}]}`}
		coord, _, q := explorationFixture(t, m)
		parent := workerquery.WithPhysicalReadBudget(context.Background(), workerquery.Budget{MaxBytes: 256, MaxFanout: 8})
		_, refund, err := workerquery.ReservePhysicalRead(parent, 128)
		require.NoError(t, err)
		refund(128)
		out, err := coord.Facets(parent, FacetsQuery{Query: q, Dimensions: []string{"trace_id"}, DimensionLimit: 1})
		require.NoError(t, err)
		require.Equal(t, uint64(128), m.bytes.Load())
		require.Contains(t, out.Coverage.PartialReasons, "BUDGET_EXCEEDED")
		_, _, err = workerquery.ReservePhysicalRead(parent, 1)
		require.ErrorIs(t, err, workerquery.ErrRangeQueryBudget)
	})
	t.Run("fanout", func(t *testing.T) {
		m := &explorationTransport{}
		coord, _, q := explorationFixture(t, m)
		parent := workerquery.WithPhysicalReadBudget(context.Background(), workerquery.Budget{MaxBytes: 1 << 20, MaxFanout: 2})
		_, refund, err := workerquery.ReservePhysicalRead(parent, 1)
		require.NoError(t, err)
		refund(0)
		out, err := coord.Facets(parent, FacetsQuery{Query: q, Dimensions: []string{"trace_id"}, DimensionLimit: 1})
		require.NoError(t, err)
		require.Equal(t, 1, m.count())
		require.Contains(t, out.Coverage.PartialReasons, "BUDGET_EXCEEDED")
	})
}
