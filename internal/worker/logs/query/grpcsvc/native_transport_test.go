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
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stream, err := workerpb.NewWorkerServiceClient(conn).LogTailLive(ctx, &workerpb.LogTailRequest{Query: &workerpb.LogQueryRequestBase{AuthorizedTargets: &workerpb.LogAuthorizedTargets{TargetIds: []string{"inst:7"}}}})
	require.NoError(t, err)
	ev, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, "native-grpc", ev.EventId)
	require.Equal(t, "real-field", ev.Fields["trace_id"])
	cancel()
	_, err = stream.Recv()
	require.Error(t, err)
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("cancel did not reach VL HTTP request")
	}
}
