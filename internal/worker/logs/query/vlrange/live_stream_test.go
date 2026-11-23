package vlrange

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

func TestLiveTailUsesNativeEndpointWithIndependentScopeAndCancellation(t *testing.T) {
	requested := make(chan string, 1)
	disconnected := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/select/logsql/tail", r.URL.Path)
		require.Equal(t, `error OR level:WARN`, r.URL.Query().Get("query"))
		require.Contains(t, r.URL.Query().Get("extra_filters"), `^inst:12(/.*)?$`)
		require.NotContains(t, r.URL.Query().Get("extra_filters"), "error OR")
		requested <- r.URL.Query().Get("extra_filters")
		io.WriteString(w, `{"event_id":"e1","log_source_id":"inst:12/file","_time":"2026-11-11T00:00:01Z","_msg":"error"}`+"\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(disconnected)
	}))
	defer srv.Close()
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
	require.NoError(t, err)
	c, err := New(vl)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	seen := make(chan logtypes.Event, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.LiveTail(ctx, query.QueryRequest{AuthorizedTargets: []string{"inst:12"}, Filter: `error OR level:WARN`, TimeRange: query.TimeRange{FromUTC: "2026-11-11T00:00:00Z"}}, func(ev logtypes.Event) error { seen <- ev; cancel(); return nil })
	}()
	select {
	case ev := <-seen:
		require.Equal(t, "e1", ev.EventID)
	case <-time.After(time.Second):
		t.Fatal("native endpoint did not deliver before closing stream")
	}
	require.Contains(t, <-requested, "2026-11-11T00:00:00Z")
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not release reader")
	}
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("VL request still running")
	}
}

func TestLiveTailRefusesEmptyNonInstanceScopeAndPipes(t *testing.T) {
	for _, targets := range [][]string{nil, {"*"}, {"node:1"}, {"inst:1/file"}, {"inst:0"}, {"inst:1 OR *"}} {
		require.Error(t, (&Client{}).LiveTail(context.Background(), query.QueryRequest{AuthorizedTargets: targets}, func(logtypes.Event) error { return nil }))
	}
	require.Error(t, (&Client{}).LiveTail(context.Background(), query.QueryRequest{AuthorizedTargets: []string{"inst:1"}, Filter: "* | union (*)"}, func(logtypes.Event) error { return nil }))
}

func TestLiveTailNeverForwardsOutOfScopeRowAndStopsAtBudget(t *testing.T) {
	for _, tc := range []struct {
		name, line string
		limit      uint32
		want       string
	}{
		{"scope", `{"event_id":"x","log_source_id":"inst:123/file","_msg":"x"}`, 10, "unauthorized"},
		{"budget", strings.Repeat(`{"event_id":"x","log_source_id":"inst:12/file","_msg":"x"}`+"\n", 2), 1, "budget"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, tc.line+"\n") }))
			defer srv.Close()
			vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
			require.NoError(t, err)
			c, err := New(vl)
			require.NoError(t, err)
			count := 0
			err = c.LiveTail(context.Background(), query.QueryRequest{AuthorizedTargets: []string{"inst:12"}, Budget: query.Budget{Limit: tc.limit}}, func(logtypes.Event) error { count++; return nil })
			require.ErrorContains(t, err, tc.want)
			if tc.name == "scope" {
				require.Zero(t, count)
			} else {
				require.Equal(t, 1, count)
			}
		})
	}
}
