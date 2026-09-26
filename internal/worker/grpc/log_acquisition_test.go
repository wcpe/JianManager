package grpc

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

type registrationLogCollector struct {
	calls                          int
	target, generation, mode, path string
	err                            error
}

func (c *registrationLogCollector) RegisterInstance(uuid, target, generation, mode, path string) error {
	c.calls++
	c.target, c.generation, c.mode, c.path = target, generation, mode, path
	return c.err
}
func (*registrationLogCollector) AppendInstanceOutput(string, string, []byte) error { return nil }

func TestInstanceRegistrationAndResyncBindManagedLogs(t *testing.T) {
	manager := process.NewManager(t.TempDir())
	srv := NewServer(manager, "node", nil, nil, nil)
	collector := &registrationLogCollector{}
	srv.SetInstanceLogCollector(collector)
	request := &workerpb.CreateInstanceRequest{InstanceUuid: "uuid", Name: "fixture", WorkDir: t.TempDir(), ProcessType: "direct",
		LogTargetId: "inst:1", LogAcquireMode: "FILE_PRIMARY", LogSourceGeneration: "holder-g1"}
	response, err := srv.CreateInstance(context.Background(), request)
	require.NoError(t, err)
	require.True(t, response.Success)
	require.Equal(t, request.LogTargetId, collector.target)
	require.Equal(t, request.LogSourceGeneration, collector.generation)
	require.Equal(t, request.WorkDir, collector.path)
	_, err = srv.ResyncInstances(context.Background(), &workerpb.ResyncInstancesRequest{Instances: []*workerpb.CreateInstanceRequest{request}})
	require.NoError(t, err)
	require.Equal(t, 2, collector.calls, "already registered processes still need log binding on CP reconnect")
	collector.err = errors.New("metadata disk full")
	request.InstanceUuid = "cannot-register"
	response, err = srv.CreateInstance(context.Background(), request)
	require.NoError(t, err)
	require.False(t, response.Success)
	_, exists := manager.GetInstance(request.InstanceUuid)
	require.False(t, exists, "do not acknowledge registration when durable log binding failed")
}

func TestCreateInstanceAdoptsPendingOutputThroughGrpcBinding(t *testing.T) {
	root := t.TempDir()
	collector, err := ingest.New(ingest.Options{Root: root, Catalog: catalog.New(nil)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = collector.Stop() })
	require.ErrorIs(t, collector.AppendInstanceOutput("uuid-grpc-spool", "stdout", []byte("before grpc binding\n")), ingest.ErrInstanceBindingPending)

	manager := process.NewManager(t.TempDir())
	srv := NewServer(manager, "node", nil, nil, nil)
	srv.SetInstanceLogCollector(collector)
	request := &workerpb.CreateInstanceRequest{InstanceUuid: "uuid-grpc-spool", Name: "fixture", WorkDir: t.TempDir(), ProcessType: "direct",
		LogTargetId: "inst:grpc-spool", LogAcquireMode: "STDIO_PRIMARY", LogSourceGeneration: "holder-g1"}
	response, err := srv.CreateInstance(context.Background(), request)
	require.NoError(t, err)
	require.True(t, response.Success)
	identity := sha256.Sum256([]byte(request.InstanceUuid + "\x00" + request.LogSourceGeneration))
	rawPath := filepath.Join(root, "var", "log", "raw", fmt.Sprintf("%x", identity), "stdout.log")
	data, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	require.Equal(t, "before grpc binding\n", string(data))
}
