package vlrange

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// Runs only with an explicitly provided approved binary, always in a fresh
// temporary data directory on a random loopback port; never reads production VL.
func TestNativeLiveTailRealVictoriaLogs(t *testing.T) {
	bin := os.Getenv("JM_VL_BIN")
	if bin == "" {
		t.Skip("set JM_VL_BIN for isolated real VL test")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	addr := "127.0.0.1:" + strconv.Itoa(port)
	cmd := exec.Command(bin, "-storageDataPath="+t.TempDir(), "-httpListenAddr="+addr, "-retentionPeriod=1d", "-search.maxConcurrentRequests=4")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	client := &http.Client{Timeout: time.Second}
	deadline := time.After(8 * time.Second)
	ready := false
	for !ready {
		resp, e := client.Get("http://" + addr + "/health")
		if e == nil {
			resp.Body.Close()
			ready = resp.StatusCode == 200
		}
		if ready {
			break
		}
		select {
		case <-deadline:
			t.Fatal("isolated VL did not become ready")
		case <-time.After(25 * time.Millisecond):
		}
	}
	vl, err := vlsup.NewClient(vlsup.ClientOptions{BaseURL: "http://" + addr})
	require.NoError(t, err)
	c, err := New(vl)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	events := make(chan logtypes.Event, 8)
	finished := make(chan error, 1)
	go func() {
		finished <- c.LiveTail(ctx, query.QueryRequest{AuthorizedTargets: []string{"inst:12"}, Filter: "native-marker OR level:WARN", TimeRange: query.TimeRange{FromUTC: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)}}, func(ev logtypes.Event) error { events <- ev; return nil })
	}()
	for _, id := range []string{"inst:12/file", "inst:123/file"} {
		row, _ := json.Marshal(map[string]string{"event_id": id, "log_source_id": id, "_time": time.Now().Add(-2 * time.Second).UTC().Format(time.RFC3339Nano), "_msg": "native-marker", "level": "INFO", "stream": "file"})
		_, err = vl.InsertJSONLines(ctx, append(row, '\n'))
		require.NoError(t, err)
	}
	select {
	case ev := <-events:
		require.Equal(t, "inst:12/file", ev.Source.LogSourceID)
	case err := <-finished:
		t.Fatalf("native endpoint ended before matching event: %v", err)
	case <-ctx.Done():
		t.Fatal("real native stream did not deliver")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("real native HTTP query did not cancel")
	}
	select {
	case ev := <-events:
		t.Fatalf("out-of-scope event leaked: %+v", ev)
	default:
	}
}
