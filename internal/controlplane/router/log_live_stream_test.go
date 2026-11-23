package router

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/controlplane/logcoord"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

type nativeHTTPWorker struct {
	*fedWorker
	subscribe func(context.Context, logcoord.LiveWorkerRequest, func(logcoord.Event) error) error
}

func (w *nativeHTTPWorker) LiveTail(ctx context.Context, q logcoord.LiveWorkerRequest, send func(logcoord.Event) error) error {
	return w.subscribe(ctx, q, send)
}

func TestNativeLogHTTPStreamsImmediatelyAndCancelsWorker(t *testing.T) {
	stopped := make(chan struct{})
	worker := &nativeHTTPWorker{fedWorker: &fedWorker{targets: []logcoord.WorkerTargetResult{{TargetID: "inst:1", State: logcoord.CoverageSuccess}}}}
	worker.subscribe = func(ctx context.Context, q logcoord.LiveWorkerRequest, send func(logcoord.Event) error) error {
		defer close(stopped)
		require.Equal(t, []string{"inst:1"}, q.TargetIDs)
		if err := send(logcoord.Event{EventID: "real-time", LogSourceID: "inst:1/file", Message: "hello"}); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}
	coord := logcoord.New(&fedResolver{targets: []logcoord.TargetInfo{{ID: "inst:1", WorkerID: "w1", Readiness: logcoord.ReadyOnline}}}, &fedDialer{clients: map[string]logcoord.WorkerClient{"w1": worker}})
	r, _ := setupFederationRouter(t, coord)
	token := getAdminToken(t, r)
	server := httptest.NewServer(r)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/v1/logs/federation/live?source=instance&instanceId=1", nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Contains(t, response.Header.Get("Content-Type"), "text/event-stream")
	scanner := bufio.NewScanner(response.Body)
	found := false
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), `"real-time"`) {
			found = true
			break
		}
	}
	require.True(t, found, "must get a log before server EOF")
	cancel()
	response.Body.Close()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("worker subscription survived browser cancel")
	}
}

func TestNativeLogHTTPRejectsTextSourcesAndUnauthenticatedClient(t *testing.T) {
	coord := logcoord.New(&fedResolver{}, &fedDialer{})
	r, _ := setupFederationRouter(t, coord)
	token := getAdminToken(t, r)
	for _, query := range []string{"source=worker", "source=control_plane", "view=platform", "to=2026-11-11T00:00:00Z", "cursor=historical"} {
		resp := makeRequest(r, "GET", "/api/v1/logs/federation/live?"+query, nil, token)
		require.Equal(t, http.StatusBadRequest, resp.Code, resp.Body.String())
	}
	resp := makeRequest(r, "GET", "/api/v1/logs/federation/live?token="+token, nil, "")
	require.Equal(t, http.StatusUnauthorized, resp.Code)
}

func TestNativeLogHTTPRevocationStopsExistingConnection(t *testing.T) {
	stopped := make(chan struct{})
	worker := &nativeHTTPWorker{fedWorker: &fedWorker{targets: []logcoord.WorkerTargetResult{{TargetID: "inst:1", State: logcoord.CoverageSuccess}}}, subscribe: func(ctx context.Context, q logcoord.LiveWorkerRequest, send func(logcoord.Event) error) error {
		defer close(stopped)
		<-ctx.Done()
		return ctx.Err()
	}}
	coord := logcoord.New(&fedResolver{targets: []logcoord.TargetInfo{{ID: "inst:1", WorkerID: "w1", Readiness: logcoord.ReadyOnline}}}, &fedDialer{clients: map[string]logcoord.WorkerClient{"w1": worker}})
	r, db := setupFederationRouter(t, coord)
	token := getAdminToken(t, r)
	server := httptest.NewServer(r)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/v1/logs/federation/live?instanceId=1&source=instance", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	require.NoError(t, db.Model(&model.User{}).Where("username = ?", "admin").Update("status", model.UserStatusDisabled).Error)
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Contains(t, string(raw), "FORBIDDEN")
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("worker survived permission revocation")
	}
}

func TestLiveConnectionLimitIsReleased(t *testing.T) {
	a, ok := acquireLiveSlot(900001)
	require.True(t, ok)
	defer a()
	b, ok := acquireLiveSlot(900001)
	require.True(t, ok)
	_, ok = acquireLiveSlot(900001)
	require.False(t, ok)
	b()
	c, ok := acquireLiveSlot(900001)
	require.True(t, ok)
	c()
}
