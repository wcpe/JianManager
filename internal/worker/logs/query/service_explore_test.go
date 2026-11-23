package query

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

func TestExploreNestedMetadataContextCannotResetBytesOrPhysicalFanout(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); _, _ = io.WriteString(w, `{"values":[]}`) }))
	defer server.Close()
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	parent := WithPhysicalReadBudget(context.Background(), Budget{MaxBytes: 1000, MaxFanout: 10})
	first := explorationReadContext(parent, Budget{MaxBytes: 40, MaxFanout: 1})
	nested := explorationReadContext(first, Budget{MaxBytes: 100, MaxFanout: 10})
	_, err = vl.Post(nested, "/select/logsql/field_names", nil)
	require.NoError(t, err)
	_, err = vl.Post(nested, "/select/logsql/field_names", nil)
	require.ErrorIs(t, err, ErrRangeQueryBudget)
	require.Equal(t, int32(1), calls.Load())
}

func TestExploreMetadataDoesNotFollowUnmeteredRedirects(t *testing.T) {
	var redirected atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/second" {
			redirected.Add(1)
			_, _ = io.WriteString(w, strings.Repeat("x", 1024))
			return
		}
		w.Header().Set("Location", "/second")
		w.WriteHeader(http.StatusTemporaryRedirect)
		_, _ = io.WriteString(w, strings.Repeat("x", 1024))
	}))
	defer server.Close()
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: server.URL})
	require.NoError(t, err)
	ctx := explorationReadContext(context.Background(), Budget{MaxBytes: 32, MaxFanout: 1})
	_, err = vl.Post(ctx, "/select/logsql/field_names", nil)
	require.ErrorIs(t, err, vlsup.ErrMetadataReadBudget)
	require.Zero(t, redirected.Load())
}

type explorationViewProbe struct {
	UnimplementedRangeClient
	calls int
	last  RangeQuery
}

func (p *explorationViewProbe) Fields(_ context.Context, _ AuthoritativeRange, q RangeQuery) (FieldsResult, error) {
	p.calls++
	p.last = q
	return FieldsResult{Fields: []string{"trace_id"}}, nil
}
func TestExploreWorkerBindsFiniteViewTimeAndFilterBeforeReading(t *testing.T) {
	probe := &explorationViewProbe{}
	service := NewService(NewPlanner(newCatalogWithHot(t), nil), probe, "test")
	req := QueryRequest{AuthorizedTargets: []string{"ns/game-1"}, TimeRange: TimeRange{FromUTC: "2026-09-20T00:00:00Z", ToUTC: "2026-09-21T00:00:00Z"}, Filter: `level:="INFO"`, ExploreFields: true}
	view := service.OpenView(req)
	require.Nil(t, view.Err)
	req.View = view.View
	for _, kind := range []string{"filter", "time"} {
		bad := req
		if kind == "filter" {
			bad.Filter = `level:="WARN"`
		} else {
			bad.TimeRange.FromUTC = "2026-09-20T12:00:00Z"
		}
		out := service.Fields(context.Background(), bad)
		require.NotNil(t, out.Err)
		require.Equal(t, ErrCodeViewStale, out.Err.Code)
		require.Zero(t, probe.calls)
	}
	unauthorized := req
	unauthorized.AuthorizedTargets = nil
	denied := service.Fields(context.Background(), unauthorized)
	require.NotNil(t, denied.Err)
	require.Equal(t, ErrCodeUnauthorized, denied.Err.Code)
	require.Zero(t, probe.calls)
	req.TimeRange = TimeRange{}
	out := service.Fields(context.Background(), req)
	require.Nil(t, out.Err)
	require.Equal(t, []string{"trace_id"}, out.Fields)
	require.Equal(t, viewTime(t, service, view.View.ViewID), probe.last.TimeRange)
	unbounded := QueryRequest{AuthorizedTargets: req.AuthorizedTargets, ExploreFields: true}
	out = service.Fields(context.Background(), unbounded)
	require.NotNil(t, out.Err)
	require.Equal(t, ErrCodeUnsupported, out.Err.Code)
}
func viewTime(t *testing.T, s *Service, id string) TimeRange {
	t.Helper()
	view, ok := s.planner.GetView(id)
	require.True(t, ok)
	return view.TimeRange
}
