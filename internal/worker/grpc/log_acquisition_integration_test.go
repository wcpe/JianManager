package grpc

import (
	"context"
	"net"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest"
	"github.com/wcpe/JianManager/internal/worker/logs/logassemble"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/query/vlrange"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

func TestRealProcessStdoutAndStderrReachManagedVL(t *testing.T) {
	bin := os.Getenv("JM_VL_BIN")
	if bin == "" {
		t.Skip("set JM_VL_BIN and approved JM_VL_SHA256 for real integration")
	}
	sha := os.Getenv("JM_VL_SHA256")
	require.NotEmpty(t, sha)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	sup, err := vlsup.New(vlsup.Options{BinaryPath: bin, AssetSHA256: sha, DataRoot: t.TempDir(),
		AuthUsername: "fixture", AuthPassword: "fixture-only", Ports: map[vlsup.Namespace]int{vlsup.NamespaceHot: port},
		MemoryAllowedBytes: map[vlsup.Namespace]int64{vlsup.NamespaceHot: 64 << 20}})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	require.NoError(t, sup.Start(ctx, vlsup.NamespaceHot))
	t.Cleanup(func() { require.NoError(t, sup.Stop(context.Background(), vlsup.NamespaceHot)) })
	vl, err := sup.ClientFor(vlsup.NamespaceHot)
	require.NoError(t, err)
	ranges, err := vlrange.New(vl)
	require.NoError(t, err)
	stack := logassemble.Build(nil, ranges, "real-instance-acquisition")
	collector, err := ingest.New(ingest.Options{Root: t.TempDir(), VL: vl, Catalog: stack.Catalog, Journal: stack.Catalog.Journal()})
	require.NoError(t, err)
	manager := process.NewManager(t.TempDir())
	manager.SetMemGuard(process.MemGuardConfig{ReserveMB: 128})
	manager.SetOutputHandler(func(id, stream string, data []byte) {
		if err := collector.AppendInstanceOutput(id, stream, data); err != nil {
			t.Errorf("Raw capture: %v", err)
		}
	})
	srv := NewServer(manager, "fixture-node", nil, nil, nil)
	srv.SetInstanceLogCollector(collector)
	srv.SetLogQueryService(stack.LogRPC)
	command := "printf 'instance-stdout\\n'; printf 'instance-stderr\\n' >&2"
	if runtime.GOOS == "windows" {
		command = "echo instance-stdout & echo instance-stderr 1>&2"
	}
	created, err := srv.CreateInstance(ctx, &workerpb.CreateInstanceRequest{InstanceUuid: "real-process", Name: "fixture", ProcessType: "direct",
		StartCommand: command, WorkDir: t.TempDir(), LogTargetId: "inst:7", LogAcquireMode: "STDIO_PRIMARY", LogSourceGeneration: "holder-1"})
	require.NoError(t, err)
	require.True(t, created.Success, created.Error)
	started, err := srv.StartInstance(ctx, &workerpb.InstanceActionRequest{InstanceUuid: "real-process"})
	require.NoError(t, err)
	require.True(t, started.Success, started.Error)
	done := make(chan struct{})
	go func() { defer close(done); collector.Start(ctx) }()
	defer func() { cancel(); <-done; require.NoError(t, collector.Stop()) }()
	require.Eventually(t, func() bool {
		result := stack.Query.Search(ctx, query.QueryRequest{AuthorizedTargets: []string{"inst:7"}, Budget: query.Budget{Limit: 10}})
		if result.Err != nil || !result.Coverage.Complete || len(result.Items) != 2 {
			return false
		}
		got := map[string]string{}
		for _, ev := range result.Items {
			got[ev.Stream] = strings.TrimSpace(ev.Message)
		}
		return got["stdout"] == "instance-stdout" && got["stderr"] == "instance-stderr"
	}, 25*time.Second, 200*time.Millisecond)
}
