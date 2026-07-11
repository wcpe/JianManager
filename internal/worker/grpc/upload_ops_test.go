package grpc

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jhump/grpctunnel"
	"github.com/jhump/grpctunnel/tunnelpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// chunksRecv 把预置分片序列变成 recv 回调：耗尽返回 io.EOF。
func chunksRecv(chunks []*workerpb.UploadFileChunk) func() (*workerpb.UploadFileChunk, error) {
	i := 0
	return func() (*workerpb.UploadFileChunk, error) {
		if i >= len(chunks) {
			return nil, io.EOF
		}
		c := chunks[i]
		i++
		return c, nil
	}
}

// requireNoUploadTemp 断言目录树下没有遗留上传临时文件。
func requireNoUploadTemp(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		require.NotContains(t, info.Name(), ".jm-upload-", "遗留上传临时文件: %s", path)
		return nil
	}))
}

// TestReceiveFileUpload_MultiFrame 多帧内容按序落盘，字节一致且无临时文件残留。
func TestReceiveFileUpload_MultiFrame(t *testing.T) {
	workDir := t.TempDir()
	want := []byte("hello streaming upload world")
	first := &workerpb.UploadFileChunk{Path: "plugins/data.bin", Content: want[:5]}
	rest := []*workerpb.UploadFileChunk{
		{Content: want[5:12]},
		{Content: nil}, // 空分片合法，跳过写入
		{Content: want[12:]},
	}

	written, err := receiveFileUpload(workDir, first, chunksRecv(rest))
	require.NoError(t, err)
	require.Equal(t, int64(len(want)), written)

	got, err := os.ReadFile(filepath.Join(workDir, "plugins", "data.bin"))
	require.NoError(t, err)
	require.Equal(t, want, got)
	requireNoUploadTemp(t, workDir)
}

// TestReceiveFileUpload_EmptyFile 首帧无内容即 EOF 也要创建空目标文件。
func TestReceiveFileUpload_EmptyFile(t *testing.T) {
	workDir := t.TempDir()
	first := &workerpb.UploadFileChunk{Path: "empty.txt"}

	written, err := receiveFileUpload(workDir, first, chunksRecv(nil))
	require.NoError(t, err)
	require.Zero(t, written)

	info, err := os.Stat(filepath.Join(workDir, "empty.txt"))
	require.NoError(t, err)
	require.Zero(t, info.Size())
	requireNoUploadTemp(t, workDir)
}

// TestReceiveFileUpload_RejectsTraversal 越界路径被拒，不落任何文件。
func TestReceiveFileUpload_RejectsTraversal(t *testing.T) {
	workDir := t.TempDir()
	first := &workerpb.UploadFileChunk{Path: "../escape.txt", Content: []byte("x")}

	_, err := receiveFileUpload(workDir, first, chunksRecv(nil))
	require.Error(t, err)

	entries, rerr := os.ReadDir(workDir)
	require.NoError(t, rerr)
	require.Empty(t, entries, "越界上传不得在工作目录留下任何文件")
	_, serr := os.Stat(filepath.Join(workDir, "..", "escape.txt"))
	require.True(t, os.IsNotExist(serr), "越界目标不得被创建")
}

// TestReceiveFileUpload_MidStreamErrorCleansTemp 中途断流：清临时文件、既有目标保持原状。
func TestReceiveFileUpload_MidStreamErrorCleansTemp(t *testing.T) {
	workDir := t.TempDir()
	old := []byte("old content must survive")
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "server.jar"), old, 0o644))

	first := &workerpb.UploadFileChunk{Path: "server.jar", Content: []byte("new partial ")}
	calls := 0
	recv := func() (*workerpb.UploadFileChunk, error) {
		calls++
		if calls == 1 {
			return &workerpb.UploadFileChunk{Content: []byte("data")}, nil
		}
		return nil, fmt.Errorf("connection reset")
	}

	_, err := receiveFileUpload(workDir, first, recv)
	require.Error(t, err)

	got, rerr := os.ReadFile(filepath.Join(workDir, "server.jar"))
	require.NoError(t, rerr)
	require.Equal(t, old, got, "中途失败不得破坏既有目标文件")
	requireNoUploadTemp(t, workDir)
}

// TestReceiveFileUpload_OverwritesExisting 覆盖已存在文件：新内容完整替换。
func TestReceiveFileUpload_OverwritesExisting(t *testing.T) {
	workDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "config.yml"), []byte("old"), 0o644))

	want := []byte("brand new content longer than old")
	first := &workerpb.UploadFileChunk{Path: "config.yml", Content: want}
	written, err := receiveFileUpload(workDir, first, chunksRecv(nil))
	require.NoError(t, err)
	require.Equal(t, int64(len(want)), written)

	got, rerr := os.ReadFile(filepath.Join(workDir, "config.yml"))
	require.NoError(t, rerr)
	require.Equal(t, want, got)
	requireNoUploadTemp(t, workDir)
}

// startUploadTestServer 起真实 gRPC Worker（bufconn + 生产 ServerOptions）并注册一个实例。
func startUploadTestServer(t *testing.T) (workerpb.WorkerServiceClient, string, string) {
	t.Helper()
	tmp := t.TempDir()
	srv := NewServer(process.NewManager(tmp), "upload-test-node", nil, nil, nil)
	lis := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer(ServerOptions()...)
	workerpb.RegisterWorkerServiceServer(grpcServer, srv)
	go func() { _ = grpcServer.Serve(lis) }()
	t.Cleanup(grpcServer.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	client := workerpb.NewWorkerServiceClient(conn)

	const uuid = "66666666-6666-6666-6666-666666666666"
	workDir := filepath.Join(tmp, "inst")
	resp, err := client.CreateInstance(context.Background(), &workerpb.CreateInstanceRequest{
		InstanceUuid: uuid, Name: "upload-e2e", StartCommand: "noop", WorkDir: workDir, ProcessType: "direct",
	})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Error)
	return client, uuid, workDir
}

// uploadViaStream 按 64KB 分片把 content 经 UploadFile 流发出。
func uploadViaStream(t *testing.T, client workerpb.WorkerServiceClient, uuid, path string, content []byte) *workerpb.UploadFileResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	stream, err := client.UploadFile(ctx)
	require.NoError(t, err)

	first := true
	for offset := 0; first || offset < len(content); {
		end := offset + uploadChunkSize
		if end > len(content) {
			end = len(content)
		}
		chunk := &workerpb.UploadFileChunk{Content: content[offset:end]}
		if first {
			chunk.InstanceUuid = uuid
			chunk.Path = path
			first = false
		}
		require.NoError(t, stream.Send(chunk))
		offset = end
	}
	resp, err := stream.CloseAndRecv()
	require.NoError(t, err)
	return resp
}

// TestUploadFile_ZeroFrameProbe 零帧即关流：业务级失败、无副作用（CP 能力探测约定）。
func TestUploadFile_ZeroFrameProbe(t *testing.T) {
	client, _, workDir := startUploadTestServer(t)

	stream, err := client.UploadFile(context.Background())
	require.NoError(t, err)
	resp, err := stream.CloseAndRecv()
	require.NoError(t, err, "零帧探测必须是业务级失败而非传输错误")
	require.False(t, resp.Success)
	require.NotEmpty(t, resp.Error)

	// 工作目录不存在（实例注册不建目录）或为空目录，都证明探测没触盘。
	entries, rerr := os.ReadDir(workDir)
	if rerr != nil {
		require.True(t, os.IsNotExist(rerr), "读工作目录意外失败: %v", rerr)
	} else {
		require.Empty(t, entries, "零帧探测不得触盘")
	}
}

// TestUploadFile_65MB_DirectDial 回归主证：65MB 上传经直拨（含 64MiB 单消息上限的生产
// ServerOptions）成功——WriteFile unary 在同场景被 ResourceExhausted 拒收（FR-304 复现）。
func TestUploadFile_65MB_DirectDial(t *testing.T) {
	client, uuid, workDir := startUploadTestServer(t)

	content := make([]byte, 65*1024*1024)
	for i := range content {
		content[i] = byte(i*31 + 7)
	}
	resp := uploadViaStream(t, client, uuid, "big.bin", content)
	require.True(t, resp.Success, resp.Error)
	require.Equal(t, int64(len(content)), resp.BytesWritten)

	got, err := os.ReadFile(filepath.Join(workDir, "big.bin"))
	require.NoError(t, err)
	require.Equal(t, sha256.Sum256(content), sha256.Sum256(got), "落盘内容与上传字节不一致")
	requireNoUploadTemp(t, workDir)
}

// TestUploadFile_65MB_ReverseTunnel 回归主证：同 65MB 经反向隧道（FR-281 生产构型）成功，
// 且与直拨行为一致（此前 WriteFile 在隧道下无上限整块缓冲，双模式行为不一致）。
func TestUploadFile_65MB_ReverseTunnel(t *testing.T) {
	handler := grpctunnel.NewTunnelServiceHandler(grpctunnel.TunnelServiceHandlerOptions{})
	cpServer := grpc.NewServer()
	tunnelpb.RegisterTunnelServiceServer(cpServer, handler.Service())
	lis := bufconn.Listen(1 << 20)
	go func() { _ = cpServer.Serve(lis) }()
	t.Cleanup(cpServer.Stop)

	tmp := t.TempDir()
	srv := NewServer(process.NewManager(tmp), "upload-tunnel-node", nil, nil, nil)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	rts := grpctunnel.NewReverseTunnelServer(tunnelpb.NewTunnelServiceClient(conn))
	workerpb.RegisterWorkerServiceServer(rts, srv)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _, _ = rts.Serve(ctx) }()
	t.Cleanup(rts.Stop)

	require.Eventually(t, func() bool { return len(handler.AllReverseTunnels()) == 1 }, 5*time.Second, 50*time.Millisecond)
	client := workerpb.NewWorkerServiceClient(handler.AsChannel())

	const uuid = "77777777-7777-7777-7777-777777777777"
	workDir := filepath.Join(tmp, "inst")
	createResp, err := client.CreateInstance(context.Background(), &workerpb.CreateInstanceRequest{
		InstanceUuid: uuid, Name: "upload-tunnel", StartCommand: "noop", WorkDir: workDir, ProcessType: "direct",
	})
	require.NoError(t, err)
	require.True(t, createResp.Success, createResp.Error)

	content := make([]byte, 65*1024*1024)
	for i := range content {
		content[i] = byte(i % 253)
	}
	resp := uploadViaStream(t, client, uuid, "big.bin", content)
	require.True(t, resp.Success, resp.Error)
	require.Equal(t, int64(len(content)), resp.BytesWritten)

	got, rerr := os.ReadFile(filepath.Join(workDir, "big.bin"))
	require.NoError(t, rerr)
	require.Equal(t, sha256.Sum256(content), sha256.Sum256(got))
	requireNoUploadTemp(t, workDir)
}

// TestUploadFile_UnknownInstance 未注册实例返回明确错误。
func TestUploadFile_UnknownInstance(t *testing.T) {
	client, _, _ := startUploadTestServer(t)

	stream, err := client.UploadFile(context.Background())
	require.NoError(t, err)
	require.NoError(t, stream.Send(&workerpb.UploadFileChunk{
		InstanceUuid: "no-such-instance", Path: "a.txt", Content: []byte("x"),
	}))
	_, err = stream.CloseAndRecv()
	require.Error(t, err)
	require.Contains(t, err.Error(), "不存在")
}
