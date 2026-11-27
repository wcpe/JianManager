package grpc

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/runtimescan"
	"github.com/wcpe/JianManager/proto/workerpb"
)

func TestServer_ProbeRuntime_ValidationAndSecurity(t *testing.T) {
	s := &Server{}
	s.SetRuntimeScanner(runtimescan.New(nil))

	// 缺少参数 → Valid=false，不返回 RPC error
	resp, err := s.ProbeRuntime(context.Background(), &workerpb.ProbeRuntimeRequest{Type: "nodejs", Path: "   "})
	require.NoError(t, err)
	require.False(t, resp.Valid)

	// 相对路径 → Valid=false
	resp, err = s.ProbeRuntime(context.Background(), &workerpb.ProbeRuntimeRequest{Type: "nodejs", Path: "node"})
	require.NoError(t, err)
	require.False(t, resp.Valid)

	resp, err = s.ProbeRuntime(context.Background(), &workerpb.ProbeRuntimeRequest{Type: "jdk", Path: "java"})
	require.NoError(t, err)
	require.False(t, resp.Valid)

	// 不支持的类型 → Valid=false
	resp, err = s.ProbeRuntime(context.Background(), &workerpb.ProbeRuntimeRequest{Type: "python", Path: t.TempDir()})
	require.NoError(t, err)
	require.False(t, resp.Valid)

	if runtime.GOOS == "windows" {
		return
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	fakeNode := filepath.Join(binDir, "node")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo v22.15.0; else echo x64; fi\n"
	require.NoError(t, os.WriteFile(fakeNode, []byte(script), 0o755))

	resp, err = s.ProbeRuntime(context.Background(), &workerpb.ProbeRuntimeRequest{Type: "nodejs", Path: dir})
	require.NoError(t, err)
	require.True(t, resp.Valid)
	require.NotNil(t, resp.Candidate)
	require.Equal(t, "22.15.0", resp.Candidate.Version)
	require.Equal(t, int32(22), resp.Candidate.MajorVersion)
	require.Equal(t, fakeNode, resp.Candidate.Path)
}
