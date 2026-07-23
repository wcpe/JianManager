package grpc

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/platform/dataroot"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// newPermTestServer 带数据根与已 Create 的实例，供 List/Check/Chmod/Browse RPC 集成测。
func newPermTestServer(t *testing.T) (srv *Server, workDir, uuid string) {
	t.Helper()
	dataDir := t.TempDir()
	root, err := dataroot.Init(dataDir)
	require.NoError(t, err)
	srv = NewServer(process.NewManager(dataDir), "perm-node", nil, nil, root)

	uuid = "37337337-3733-3733-3733-373373373373"
	workDir = filepath.Join(dataDir, "var", "servers", "srv-perm")
	require.NoError(t, os.MkdirAll(workDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "server.properties"), []byte("motd=hi\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "plugins"), 0o755))

	resp, err := srv.CreateInstance(context.Background(), &workerpb.CreateInstanceRequest{
		InstanceUuid: uuid,
		Name:         "perm",
		StartCommand: "noop",
		WorkDir:      workDir,
		ProcessType:  "direct",
	})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Error)
	return srv, workDir, uuid
}

func TestListFiles_FillsPermMetadata(t *testing.T) {
	srv, _, uuid := newPermTestServer(t)
	resp, err := srv.ListFiles(context.Background(), &workerpb.ListFilesRequest{
		InstanceUuid: uuid,
		Path:         "",
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Files)

	var foundProps bool
	for _, f := range resp.Files {
		if f.Name == "server.properties" {
			foundProps = true
			require.False(t, f.IsDir)
			require.True(t, f.Readable, "应可读")
			if runtime.GOOS != "windows" {
				require.NotEmpty(t, f.ModeOctal, "Unix 应有 modeOctal")
			}
		}
		if f.Name == "plugins" {
			require.True(t, f.IsDir)
			require.True(t, f.Readable)
		}
	}
	require.True(t, foundProps, "应列出 server.properties")
}

func TestCheckPathAccess_AndChmod_RPC(t *testing.T) {
	srv, workDir, uuid := newPermTestServer(t)
	ctx := context.Background()

	ok, err := srv.CheckPathAccess(ctx, &workerpb.CheckPathAccessRequest{
		InstanceUuid: uuid,
		Path:         "server.properties",
	})
	require.NoError(t, err)
	require.True(t, ok.Success)
	require.True(t, ok.Exists)
	require.True(t, ok.Readable)

	miss, err := srv.CheckPathAccess(ctx, &workerpb.CheckPathAccessRequest{
		InstanceUuid: uuid,
		Path:         "no-such-file.txt",
	})
	require.NoError(t, err)
	require.True(t, miss.Success)
	require.False(t, miss.Exists)

	bad, err := srv.CheckPathAccess(ctx, &workerpb.CheckPathAccessRequest{
		InstanceUuid: uuid,
		Path:         "../outside",
	})
	require.NoError(t, err)
	require.False(t, bad.Success)
	require.NotEmpty(t, bad.Error)

	absOK, err := srv.CheckPathAccess(ctx, &workerpb.CheckPathAccessRequest{
		Path: workDir,
	})
	require.NoError(t, err)
	require.True(t, absOK.Success)
	require.True(t, absOK.Exists)
	require.True(t, absOK.IsDir)

	ch, err := srv.ChmodPath(ctx, &workerpb.ChmodPathRequest{
		InstanceUuid: uuid,
		Path:         "server.properties",
		Mode:         "",
	})
	require.NoError(t, err)
	require.True(t, ch.Success, ch.Error)
	require.NotEmpty(t, ch.ModeOctal)

	ch2, err := srv.ChmodPath(ctx, &workerpb.ChmodPathRequest{
		InstanceUuid: uuid,
		Path:         "server.properties",
		Mode:         "0644",
	})
	require.NoError(t, err)
	require.True(t, ch2.Success, ch2.Error)

	br, err := srv.BrowseDir(ctx, &workerpb.BrowseDirRequest{Path: workDir})
	require.NoError(t, err)
	require.True(t, br.Success, br.Error)
	require.NotEmpty(t, br.Dirs)
	for _, d := range br.Dirs {
		if d.Name == "plugins" {
			require.True(t, d.Readable)
		}
	}
}

func TestBrowseDir_ChineseErrorOnUnreadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 难造不可读目录")
	}
	srv, _, _ := newPermTestServer(t)
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	require.NoError(t, os.Mkdir(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	if _, err := os.ReadDir(locked); err == nil {
		t.Skip("当前用户仍能读 000 目录（可能是 root）")
	}

	br, err := srv.BrowseDir(context.Background(), &workerpb.BrowseDirRequest{Path: locked})
	require.NoError(t, err)
	require.False(t, br.Success)
	require.Contains(t, br.Error, "权限")
	require.Empty(t, br.Dirs)
}
