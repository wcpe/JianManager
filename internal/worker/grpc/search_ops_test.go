package grpc

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/platform/dataroot"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/internal/worker/search"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// newSearchServer 构造一个带数据根的 Worker gRPC Server，并注册一个工作目录在 work 的实例。
func newSearchServer(t *testing.T) (*Server, context.Context, string) {
	t.Helper()
	dataDir := t.TempDir()
	root, err := dataroot.Init(dataDir)
	require.NoError(t, err)

	srv := NewServer(process.NewManager(dataDir), "test-node", nil, nil, root)
	ctx := context.Background()

	const uuid = "22222222-2222-2222-2222-222222222222"
	work := filepath.Join(dataDir, "var", "servers", "srv-search")
	require.NoError(t, os.MkdirAll(work, 0o755))
	resp, err := srv.CreateInstance(ctx, &workerpb.CreateInstanceRequest{
		InstanceUuid: uuid,
		Name:         "search",
		StartCommand: "noop",
		WorkDir:      work,
		ProcessType:  "direct",
	})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Error)
	return srv, ctx, uuid
}

func writeSearchFile(t *testing.T, work, rel, content string) {
	t.Helper()
	p := filepath.Join(work, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}

func TestSearchFiles_Content(t *testing.T) {
	srv, ctx, uuid := newSearchServer(t)
	inst, ok := srv.manager.GetInstance(uuid)
	require.True(t, ok)
	writeSearchFile(t, inst.WorkDir, "server.properties", "online-mode=false\nmotd=Hi")
	writeSearchFile(t, inst.WorkDir, "logs/latest.log", "online-mode noise in log")

	resp, err := srv.SearchFiles(ctx, &workerpb.SearchFilesRequest{
		InstanceUuid: uuid,
		Query:        "online-mode",
		Mode:         "content",
		MaxResults:   50,
	})
	require.NoError(t, err)
	require.False(t, resp.Indexing, "小目录经快路径同步出结果，不应 indexing=true")
	require.Len(t, resp.Hits, 1, "logs/ ignored, only server.properties should hit")
	require.Equal(t, "server.properties", resp.Hits[0].Path)
	require.Equal(t, int32(1), resp.Hits[0].Line)
	require.Contains(t, resp.Hits[0].Snippet, "online-mode=false")
}

// TestSearchFiles_IndexingThenReady 验证首建后台化的 RPC 契约（FR-113，ADR-024）：
// 构建在途时首查返回 indexing=true、空命中；就绪后再查返回 indexing=false + 命中。
func TestSearchFiles_IndexingThenReady(t *testing.T) {
	gate := make(chan struct{})
	prev := search.SetBuildStartHookForTest(func() { <-gate }) // 把后台构建卡在途
	defer search.SetBuildStartHookForTest(prev)

	srv, ctx, uuid := newSearchServer(t)
	inst, _ := srv.manager.GetInstance(uuid)
	writeSearchFile(t, inst.WorkDir, "server.properties", "online-mode=false")

	// 首查：构建被卡住，快路径预算内未就绪 → indexing=true、空命中。
	resp, err := srv.SearchFiles(ctx, &workerpb.SearchFilesRequest{
		InstanceUuid: uuid, Query: "online-mode", Mode: "content", MaxResults: 50,
	})
	require.NoError(t, err)
	require.True(t, resp.Indexing, "构建在途首查应返回 indexing=true")
	require.Empty(t, resp.Hits)

	// 放行构建并等就绪。
	close(gate)
	ix := srv.searchIndexFor(uuid)
	require.Eventually(t, ix.Ready, 2*time.Second, 10*time.Millisecond, "放行后索引应就绪")

	// 再查：indexing=false + 命中。
	resp, err = srv.SearchFiles(ctx, &workerpb.SearchFilesRequest{
		InstanceUuid: uuid, Query: "online-mode", Mode: "content", MaxResults: 50,
	})
	require.NoError(t, err)
	require.False(t, resp.Indexing)
	require.Len(t, resp.Hits, 1)
	require.Equal(t, "server.properties", resp.Hits[0].Path)
}

func TestSearchFiles_FilenameMode(t *testing.T) {
	srv, ctx, uuid := newSearchServer(t)
	inst, _ := srv.manager.GetInstance(uuid)
	writeSearchFile(t, inst.WorkDir, "bukkit.yml", "x")
	writeSearchFile(t, inst.WorkDir, "plugins/Essentials/config.yml", "y")

	resp, err := srv.SearchFiles(ctx, &workerpb.SearchFilesRequest{
		InstanceUuid: uuid,
		Query:        "yml",
		Mode:         "filename",
		MaxResults:   50,
	})
	require.NoError(t, err)
	paths := map[string]bool{}
	for _, h := range resp.Hits {
		paths[h.Path] = true
		require.Equal(t, int32(0), h.Line, "filename mode has no line")
	}
	require.True(t, paths["bukkit.yml"])
	require.True(t, paths["plugins/Essentials/config.yml"])
}

func TestSearchFiles_IncrementalAfterEdit(t *testing.T) {
	srv, ctx, uuid := newSearchServer(t)
	inst, _ := srv.manager.GetInstance(uuid)
	writeSearchFile(t, inst.WorkDir, "a.txt", "findme here")

	resp, err := srv.SearchFiles(ctx, &workerpb.SearchFilesRequest{InstanceUuid: uuid, Query: "findme", Mode: "content"})
	require.NoError(t, err)
	require.Len(t, resp.Hits, 1)

	// 删除文件后再搜，增量应移除命中。
	require.NoError(t, os.Remove(filepath.Join(inst.WorkDir, "a.txt")))
	resp2, err := srv.SearchFiles(ctx, &workerpb.SearchFilesRequest{InstanceUuid: uuid, Query: "findme", Mode: "content"})
	require.NoError(t, err)
	require.Len(t, resp2.Hits, 0)
}

func TestSearchFiles_UnknownInstance(t *testing.T) {
	srv, ctx, _ := newSearchServer(t)
	_, err := srv.SearchFiles(ctx, &workerpb.SearchFilesRequest{InstanceUuid: "no-such", Query: "x"})
	require.Error(t, err)
}

func TestSearchFiles_CustomIgnore(t *testing.T) {
	srv, ctx, uuid := newSearchServer(t)
	srv.SetSearchIgnore([]string{"private/"})
	inst, _ := srv.manager.GetInstance(uuid)
	writeSearchFile(t, inst.WorkDir, "open.txt", "secret KEYWORD")
	writeSearchFile(t, inst.WorkDir, "private/hidden.txt", "secret KEYWORD")

	resp, err := srv.SearchFiles(ctx, &workerpb.SearchFilesRequest{InstanceUuid: uuid, Query: "KEYWORD", Mode: "content"})
	require.NoError(t, err)
	require.Len(t, resp.Hits, 1)
	require.Equal(t, "open.txt", resp.Hits[0].Path)
}
