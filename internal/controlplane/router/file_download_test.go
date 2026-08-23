package router

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakeFileDownloadWorker 模拟 Worker 文件域行为的 fake 客户端。
// ReadFile 忠实复刻真实 Worker（internal/worker/grpc/file_ops.go）的编辑器护栏语义：
// 超过 10MiB 的内容被截断返回——单文件下载若复用它，大文件必然损坏（本测试所复现的缺陷）。
type fakeFileDownloadWorker struct {
	workerpb.WorkerServiceClient
	files map[string][]byte
}

func (f *fakeFileDownloadWorker) ReadFile(_ context.Context, req *workerpb.ReadFileRequest, _ ...grpc.CallOption) (*workerpb.ReadFileResponse, error) {
	b, ok := f.files[req.Path]
	if !ok {
		return nil, fmt.Errorf("读取文件失败: 文件不存在")
	}
	const maxSize = 10 * 1024 * 1024
	if len(b) > maxSize {
		b = b[:maxSize]
	}
	return &workerpb.ReadFileResponse{Content: b}, nil
}

// fakeDownloadFileStream 预分好片的 DownloadFile 客户端流；err 非空时首个 Recv 即返回该错误
// （模拟老 Worker 的 Unimplemented：真实 gRPC 建流懒惰，错误在首个 Recv 才暴露）。
type fakeDownloadFileStream struct {
	grpc.ClientStream
	chunks []*workerpb.DownloadFileChunk
	idx    int
	err    error
}

func (s *fakeDownloadFileStream) Recv() (*workerpb.DownloadFileChunk, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.idx >= len(s.chunks) {
		return nil, io.EOF
	}
	c := s.chunks[s.idx]
	s.idx++
	return c, nil
}

// DownloadFile 复刻真实 Worker 语义：64KiB 分片、首帧携带 totalSize、空文件也发一帧。
func (f *fakeFileDownloadWorker) DownloadFile(_ context.Context, req *workerpb.DownloadFileRequest, _ ...grpc.CallOption) (workerpb.WorkerService_DownloadFileClient, error) {
	b, ok := f.files[req.Path]
	if !ok {
		return &fakeDownloadFileStream{err: fmt.Errorf("打开文件失败: 文件不存在")}, nil
	}
	const chunkSize = 64 * 1024
	var chunks []*workerpb.DownloadFileChunk
	first := true
	for off := 0; off < len(b) || first; {
		n := len(b) - off
		if n > chunkSize {
			n = chunkSize
		}
		chunk := &workerpb.DownloadFileChunk{Content: b[off : off+n]}
		if first {
			chunk.TotalSize = int64(len(b))
			first = false
		}
		chunks = append(chunks, chunk)
		off += n
	}
	return &fakeDownloadFileStream{chunks: chunks}, nil
}

// fakeLegacyWorker 模拟未升级的老 Worker：只有 ReadFile，DownloadFile 在首个 Recv 报 Unimplemented。
type fakeLegacyWorker struct {
	workerpb.WorkerServiceClient
}

func (f *fakeLegacyWorker) DownloadFile(context.Context, *workerpb.DownloadFileRequest, ...grpc.CallOption) (workerpb.WorkerService_DownloadFileClient, error) {
	return &fakeDownloadFileStream{err: status.Error(codes.Unimplemented, "unknown method DownloadFile")}, nil
}

// largeFileContent 生成确定性的大文件内容（非全零，便于校验字节一致）。
func largeFileContent(size int) []byte {
	b := make([]byte, size)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

// setupFileDownloadTest 建路由 + 在线节点 + 带工作目录的实例 + 注入 fake Worker。
func setupFileDownloadTest(t *testing.T, worker workerpb.WorkerServiceClient) (r *gin.Engine, token string, instID uint) {
	t.Helper()
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r = setupTestRouterWithPool(db, pool)
	token = getAdminToken(t, r)
	node := createTestNode(t, db)
	pool.SetWorkerClientForTest(node.UUID, worker)
	inst := &model.Instance{
		NodeID:       node.ID,
		Name:         "download-large",
		Type:         model.InstanceTypeGeneric,
		Role:         model.InstanceRoleUniversal,
		ProcessType:  model.ProcessTypeDirect,
		StartCommand: "noop",
		Status:       model.InstanceStatusStopped,
		WorkDir:      "/srv/download",
	}
	require.NoError(t, db.Create(inst).Error)
	return r, token, inst.ID
}

// TestFileDownload_LargeFileNotTruncated 复现缺陷：>10MiB 文件经单文件下载端点
// GET /instances/:id/files/download 必须完整返回。修复前下载复用 ReadFile 的
// 编辑器 10MiB 上限，120MB 的 server.jar 只返回前 10485760 字节且无任何报错。
func TestFileDownload_LargeFileNotTruncated(t *testing.T) {
	const size = 12 * 1024 * 1024 // 超过 10MiB 护栏即可复现，不必真用 120MB
	content := largeFileContent(size)
	worker := &fakeFileDownloadWorker{files: map[string][]byte{"server.jar": content}}
	r, token, instID := setupFileDownloadTest(t, worker)

	w := makeRequest(r, http.MethodGet,
		"/api/v1/instances/"+itoa(instID)+"/files/download?path=server.jar", nil, token)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, size, w.Body.Len(), "下载内容被截断：期望 %d 字节，实得 %d 字节", size, w.Body.Len())
	require.True(t, bytes.Equal(content, w.Body.Bytes()), "下载内容与源文件字节不一致")
	require.Equal(t, itoa(uint(size)), w.Header().Get("Content-Length"), "应显式携带 Content-Length 供客户端校验完整性")
}

// TestFileDownload_SmallFile 常规小文件下载原样返回（回归护栏：改流式后不破既有行为）。
func TestFileDownload_SmallFile(t *testing.T) {
	content := []byte("motd=hello\nserver-port=25565\n")
	worker := &fakeFileDownloadWorker{files: map[string][]byte{"server.properties": content}}
	r, token, instID := setupFileDownloadTest(t, worker)

	w := makeRequest(r, http.MethodGet,
		"/api/v1/instances/"+itoa(instID)+"/files/download?path=server.properties", nil, token)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, content, w.Body.Bytes())
}

// TestFileDownload_MissingFile 文件不存在时返回 JSON 明确错误（首帧先行判错，不写半截文件响应）。
func TestFileDownload_MissingFile(t *testing.T) {
	worker := &fakeFileDownloadWorker{files: map[string][]byte{}}
	r, token, instID := setupFileDownloadTest(t, worker)

	w := makeRequest(r, http.MethodGet,
		"/api/v1/instances/"+itoa(instID)+"/files/download?path=nope.jar", nil, token)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	resp := parseJSON(t, w)
	require.Equal(t, "BUSINESS_ERROR", resp["error"])
}

// TestFileDownload_LegacyWorkerExplicitError 老 Worker 不支持 DownloadFile：
// 必须返回明确错误引导升级，绝不回退到会静默截断的 ReadFile。
func TestFileDownload_LegacyWorkerExplicitError(t *testing.T) {
	r, token, instID := setupFileDownloadTest(t, &fakeLegacyWorker{})

	w := makeRequest(r, http.MethodGet,
		"/api/v1/instances/"+itoa(instID)+"/files/download?path=server.jar", nil, token)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	resp := parseJSON(t, w)
	require.Equal(t, "BUSINESS_ERROR", resp["error"])
	require.Contains(t, resp["message"], "升级", "错误信息应引导升级节点")
}
