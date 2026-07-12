package router

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakeUploadRouterStream 收集上传分片；CloseAndRecv 模拟真实 Worker 行为。
type fakeUploadRouterStream struct {
	grpc.ClientStream
	sent []*workerpb.UploadFileChunk
}

func (f *fakeUploadRouterStream) Send(c *workerpb.UploadFileChunk) error {
	f.sent = append(f.sent, c)
	return nil
}

func (f *fakeUploadRouterStream) CloseAndRecv() (*workerpb.UploadFileResponse, error) {
	if len(f.sent) == 0 {
		return &workerpb.UploadFileResponse{Success: false, Error: "缺少首帧"}, nil
	}
	var n int64
	for _, c := range f.sent {
		n += int64(len(c.Content))
	}
	return &workerpb.UploadFileResponse{Success: true, BytesWritten: n}, nil
}

// fakeUploadRouterWorker 支持 UploadFile 流式与 FR-051 快照所需的 ReadFile。
type fakeUploadRouterWorker struct {
	workerpb.WorkerServiceClient
	streams  []*fakeUploadRouterStream
	readResp *workerpb.ReadFileResponse // nil 表示文件不存在（快照自动跳过）
}

func (f *fakeUploadRouterWorker) UploadFile(_ context.Context, _ ...grpc.CallOption) (workerpb.WorkerService_UploadFileClient, error) {
	s := &fakeUploadRouterStream{}
	f.streams = append(f.streams, s)
	return s, nil
}

func (f *fakeUploadRouterWorker) ReadFile(_ context.Context, _ *workerpb.ReadFileRequest, _ ...grpc.CallOption) (*workerpb.ReadFileResponse, error) {
	if f.readResp == nil {
		return nil, status.Error(codes.NotFound, "文件不存在")
	}
	return f.readResp, nil
}

// uploadedContent 拼接非探测流（有帧的流）收到的内容。
func (f *fakeUploadRouterWorker) uploadedContent() []byte {
	var got []byte
	for _, s := range f.streams {
		for _, c := range s.sent {
			got = append(got, c.Content...)
		}
	}
	return got
}

// seedUploadInstance 建节点 + 实例并注入 fake Worker。
func seedUploadInstance(t *testing.T, db *gorm.DB, pool *cpgrpc.ClientPool, worker workerpb.WorkerServiceClient) *model.Instance {
	t.Helper()
	node := createTestNode(t, db)
	node.UUID = "node-upload-router"
	require.NoError(t, db.Save(node).Error)
	inst := &model.Instance{
		UUID: "inst-upload-router", NodeID: node.ID, Name: "upload-router",
		Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
		ProcessType: model.ProcessTypeDirect, StartCommand: "noop",
		Status: model.InstanceStatusStopped, WorkDir: "/srv/upload-router",
	}
	require.NoError(t, db.Create(inst).Error)
	pool.SetWorkerClientForTest(node.UUID, worker)
	return inst
}

// multipartBody 按给定顺序构造 multipart 表单；fields 先写字段，filePos 控制 file 部分位置。
func multipartBody(t *testing.T, fieldsBefore map[string]string, fileContent []byte, fieldsAfter map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	buf := &bytes.Buffer{}
	w := multipart.NewWriter(buf)
	for k, v := range fieldsBefore {
		require.NoError(t, w.WriteField(k, v))
	}
	fw, err := w.CreateFormFile("file", "upload.bin")
	require.NoError(t, err)
	_, err = fw.Write(fileContent)
	require.NoError(t, err)
	for k, v := range fieldsAfter {
		require.NoError(t, w.WriteField(k, v))
	}
	require.NoError(t, w.Close())
	return buf, w.FormDataContentType()
}

func postUpload(r http.Handler, url string, body *bytes.Buffer, contentType, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", url, body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestFileUpload_QueryParamPath 目标路径经 query 参数传递（FR-304 首选契约），内容流式转发完整。
func TestFileUpload_QueryParamPath(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupTestRouterWithPool(db, pool)
	token := getAdminToken(t, r)
	worker := &fakeUploadRouterWorker{}
	inst := seedUploadInstance(t, db, pool, worker)

	content := bytes.Repeat([]byte("streaming-upload!"), 8192) // ~128KB，跨多分片
	body, ct := multipartBody(t, nil, content, nil)
	w := postUpload(r, "/api/v1/instances/"+itoa(inst.ID)+"/files/upload?path=plugins/a.jar", body, ct, token)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, content, worker.uploadedContent(), "流式转发内容应逐字节一致")
}

// TestFileUpload_FormPathBeforeFile 兼容既有 form 传参：path 字段先于 file 部分时仍可用。
func TestFileUpload_FormPathBeforeFile(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupTestRouterWithPool(db, pool)
	token := getAdminToken(t, r)
	worker := &fakeUploadRouterWorker{}
	inst := seedUploadInstance(t, db, pool, worker)

	content := []byte("small config content")
	body, ct := multipartBody(t, map[string]string{"path": "config.yml"}, content, nil)
	w := postUpload(r, "/api/v1/instances/"+itoa(inst.ID)+"/files/upload", body, ct, token)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, content, worker.uploadedContent())
}

// TestFileUpload_MissingPath 无 query 参数且 path 字段不先于 file → 400 明确报错。
func TestFileUpload_MissingPath(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupTestRouterWithPool(db, pool)
	token := getAdminToken(t, r)
	worker := &fakeUploadRouterWorker{}
	inst := seedUploadInstance(t, db, pool, worker)

	// path 字段排在 file 之后：流式读到 file 时仍不知目标路径，应拒绝。
	body, ct := multipartBody(t, nil, []byte("x"), map[string]string{"path": "late.txt"})
	w := postUpload(r, "/api/v1/instances/"+itoa(inst.ID)+"/files/upload", body, ct, token)

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

// TestFileUpload_SnapshotBeforeOverwrite FR-051 回归：覆盖已存在文件前落改前快照版本。
func TestFileUpload_SnapshotBeforeOverwrite(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupTestRouterWithPool(db, pool)
	token := getAdminToken(t, r)
	worker := &fakeUploadRouterWorker{readResp: &workerpb.ReadFileResponse{Content: []byte("old version content")}}
	inst := seedUploadInstance(t, db, pool, worker)

	body, ct := multipartBody(t, nil, []byte("new content"), nil)
	w := postUpload(r, "/api/v1/instances/"+itoa(inst.ID)+"/files/upload?path=server.properties", body, ct, token)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var count int64
	require.NoError(t, db.Model(&model.FileVersion{}).
		Where("instance_id = ? AND file_path = ?", inst.ID, "server.properties").Count(&count).Error)
	require.Equal(t, int64(1), count, "上传覆盖前应产生一条改前快照版本")
}
