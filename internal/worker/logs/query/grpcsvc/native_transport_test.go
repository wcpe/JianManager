package grpcsvc_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/controlplane/logcoord"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/query/grpcsvc"
	"github.com/wcpe/JianManager/internal/worker/logs/query/vlrange"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
	"github.com/wcpe/JianManager/proto/workerpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestNativeTailCrossesRealGRPCBeforeVLStreamCloses(t *testing.T) {
	cancelled := make(chan struct{})
	vl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/select/logsql/tail", r.URL.Path)
		io.WriteString(w, `{"event_id":"native-grpc","log_source_id":"inst:7/file","_time":"2026-11-11T00:00:01Z","_msg":"hello","trace_id":"real-field"}`+"\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(cancelled)
	}))
	defer vl.Close()
	vc, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: vl.URL})
	require.NoError(t, err)
	rangeClient, err := vlrange.New(vc)
	require.NoError(t, err)
	service := grpcsvc.New(query.NewService(nil, &vlrange.Tiered{Main: rangeClient}, "live-test"), nil)
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	workerpb.RegisterWorkerServiceServer(server, service)
	go server.Serve(listener)
	defer server.Stop()
	conn, err := grpc.NewClient("passthrough:///native-test", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Exercise the production CP adapter, including its protocol version, rather
	// than an empty proto base that silently skips Worker compatibility checks.
	adapter := logcoord.NewWorkerClientAdapter(workerpb.NewWorkerServiceClient(conn))
	received := false
	err = adapter.LiveTail(ctx, logcoord.LiveWorkerRequest{
		WorkerSearchRequest: logcoord.WorkerSearchRequest{TargetIDs: []string{"inst:7"}},
		PermissionScope:     "native-transport-test",
	}, func(ev logcoord.Event) error {
		received = true
		require.Equal(t, "native-grpc", ev.EventID)
		require.Equal(t, "inst:7/file", ev.LogSourceID)
		require.Equal(t, "real-field", ev.Fields["trace_id"])
		cancel()
		return nil
	})
	require.True(t, received, "CP adapter must receive an event before cancellation; transport error: %v", err)
	require.Error(t, err, "cancellation must terminate the adapter's receive loop")
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("cancel did not reach VL HTTP request")
	}
}
