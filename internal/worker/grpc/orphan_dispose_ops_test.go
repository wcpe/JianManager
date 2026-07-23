package grpc

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// TestDisposeOrphanRuntime_MissingInstance_Idempotent 未注册实例仍 success（已干净）。
func TestDisposeOrphanRuntime_MissingInstance_Idempotent(t *testing.T) {
	dir := t.TempDir()
	mgr := process.NewManager(dir)
	s := &Server{manager: mgr}

	resp, err := s.DisposeOrphanRuntime(context.Background(), &workerpb.DisposeOrphanRuntimeRequest{
		InstanceUuid: "no-such-inst",
	})
	require.NoError(t, err)
	require.True(t, resp.Success)
	require.Empty(t, resp.Error)
}

// TestDisposeOrphanRuntime_EmptyUUID 空 UUID 拒绝。
func TestDisposeOrphanRuntime_EmptyUUID(t *testing.T) {
	s := &Server{manager: process.NewManager(t.TempDir())}
	resp, err := s.DisposeOrphanRuntime(context.Background(), &workerpb.DisposeOrphanRuntimeRequest{})
	require.NoError(t, err)
	require.False(t, resp.Success)
}

// TestDisposeOrphanRuntime_RegisteredStopped_Removes 已注册未运行实例：移除注册。
func TestDisposeOrphanRuntime_RegisteredStopped_Removes(t *testing.T) {
	dir := t.TempDir()
	mgr := process.NewManager(dir)
	work := filepath.Join(dir, "servers", "i1")
	require.NoError(t, mgr.Create("i1", "n", "echo", "", work, nil, false, process.ProcessTypeDirect, "", "", 0, 0))

	s := &Server{manager: mgr}
	resp, err := s.DisposeOrphanRuntime(context.Background(), &workerpb.DisposeOrphanRuntimeRequest{
		InstanceUuid: "i1",
	})
	require.NoError(t, err)
	require.True(t, resp.Success)
	_, ok := mgr.GetInstance("i1")
	require.False(t, ok, "处置后应从注册表移除")
}
