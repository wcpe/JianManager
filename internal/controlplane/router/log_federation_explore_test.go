package router

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/controlplane/logcoord"
)

type explorationHTTPWorker struct {
	*fedWorker
	fieldsExplore, facetsExplore bool
}

func (w *explorationHTTPWorker) Fields(ctx context.Context, req logcoord.WorkerSearchRequest) (*logcoord.WorkerFieldsResponse, error) {
	w.fieldsExplore = req.ExploreFields
	return w.fedWorker.Fields(ctx, req)
}
func (w *explorationHTTPWorker) Facets(ctx context.Context, req logcoord.WorkerSearchRequest, dims []string, limit uint32) (*logcoord.WorkerFacetsResponse, error) {
	w.facetsExplore = req.ExploreFields
	return w.fedWorker.Facets(ctx, req, dims, limit)
}

func TestLogFederation_ExplorationOptInAndRequestValidation(t *testing.T) {
	worker := &explorationHTTPWorker{fedWorker: &fedWorker{targets: []logcoord.WorkerTargetResult{fedSuccess("inst:1", "w1", "20", "1")}}}
	resolver := &fedResolver{targets: []logcoord.TargetInfo{{ID: "inst:1", WorkerID: "w1", Readiness: logcoord.ReadyOnline}}}
	coord := logcoord.New(resolver, &fedDialer{clients: map[string]logcoord.WorkerClient{"w1": worker}})
	r, _ := setupFederationRouter(t, coord)
	token := getAdminToken(t, r)
	base := "/api/v1/logs/federation/"
	window := "?from=2026-09-20T00:00:00Z&to=2026-09-21T00:00:00Z"
	for _, path := range []string{"fields", "facets"} {
		extra := ""
		if path == "facets" {
			extra = "&dimensions=trace_id&dimensionLimit=20"
		}
		for _, explore := range []bool{false, true} {
			flag := ""
			if explore {
				flag = "&explore=true"
			}
			response := makeRequest(r, "GET", base+path+window+extra+flag, nil, token)
			require.Equal(t, http.StatusOK, response.Code, "%s", response.Body.String())
			var body map[string]any
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
			if explore {
				require.Equal(t, true, body["explore_applied"])
			} else {
				require.NotContains(t, body, "explore_applied")
			}
			if path == "fields" {
				require.Equal(t, explore, worker.fieldsExplore)
			} else {
				require.Equal(t, explore, worker.facetsExplore)
			}
		}
	}
	for _, query := range []string{"?explore=true", "?explore=true&from=2026-09-20T00:00:00Z"} {
		response := makeRequest(r, "GET", base+"fields"+query, nil, token)
		require.Equal(t, http.StatusBadRequest, response.Code)
	}
	for _, extra := range []string{"&dimensions=trace_id&dimensionLimit=0", "&dimensions=trace_id&dimensionLimit=21", "&dimensions=trace_id,latency_ms&dimensionLimit=20"} {
		response := makeRequest(r, "GET", base+"facets"+window+"&explore=true"+extra, nil, token)
		require.Equal(t, http.StatusBadRequest, response.Code, "%s", response.Body.String())
	}
}
