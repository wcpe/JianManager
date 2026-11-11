package vlrange

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// This is a transport/selector test, not a fake implementation of LogsQL. The
// real-VL integration suite separately verifies VL's intersection semantics.
func TestFilterExpressionCannotReplaceSelectorOrHTTPTimeWindow(t *testing.T) {
	for _, filter := range []string{
		`_msg:re("error|warn") OR log_source_id:="inst:99/file"`,
		`log_source_id:="inst:99/file" OR (_time:>now-180d AND *)`,
		`NOT (log_source_id:="inst:11/file") OR *`,
	} {
		t.Run(filter, func(t *testing.T) {
			var calls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				require.NoError(t, r.ParseForm())
				require.Equal(t, filter, r.Form.Get("extra_filters"))
				require.NotContains(t, r.Form.Get("query"), "inst:99")
				require.Contains(t, r.Form.Get("query"), `log_source_id:="inst:11"`)
				require.Contains(t, r.Form.Get("query"), `log_source_id:"inst:11/"*`)
				require.Equal(t, "2026-09-22T10:00:00Z", r.Form.Get("start"))
				require.Equal(t, "2026-09-22T14:00:00Z", r.Form.Get("end"))
				_, _ = w.Write([]byte(`{"_time":"2026-09-22T12:00:00Z","_msg":"authorized","event_id":"allowed","log_source_id":"inst:11/file","level":"WARN"}` + "\n"))
			}))
			defer srv.Close()
			vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: srv.URL})
			require.NoError(t, err)
			client, err := New(vl)
			require.NoError(t, err)
			result, err := client.Search(context.Background(), query.AuthoritativeRange{
				Key: catalog.PartitionKey{StorageNamespace: "inst:11", UTCDay: "2026-09-22"},
			}, query.RangeQuery{
				Filter:    filter,
				TimeRange: query.TimeRange{FromUTC: "2026-09-22T10:00:00Z", ToUTC: "2026-09-22T14:00:00Z"},
				Budget:    query.Budget{Limit: 10, MaxBytes: 1 << 20},
			})
			require.NoError(t, err)
			require.Positive(t, calls)
			require.Len(t, result.Items, 1, "fixture must return a real authorized row, not a vacuous empty success")
			require.Equal(t, "inst:11/file", result.Items[0].Source.LogSourceID)
		})
	}
}
