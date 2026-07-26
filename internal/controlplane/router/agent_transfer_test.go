package router

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// testTransferMasterSecret 与 setupTestRouterWithPool 装配票据服务所用主密钥一致。
const testTransferMasterSecret = "test-secret-key-for-testing"

// setupTransferTestRouter 复用通用测试路由（已装配 agent-transfer 端点）。
func setupTransferTestRouter(t *testing.T, db *gorm.DB, pool *cpgrpc.ClientPool) *gin.Engine {
	t.Helper()
	return setupTestRouterWithPool(db, pool)
}

// fakeTransferWorker 支持票据端点所需的 UploadFile 流式与 DownloadFile 流式。
type fakeTransferWorker struct {
	workerpb.WorkerServiceClient
	uploaded []byte
	files    map[string][]byte
}

type fakeTransferUploadStream struct {
	grpc.ClientStream
	worker *fakeTransferWorker
	sent   int64
	frames int
}

func (s *fakeTransferUploadStream) Send(c *workerpb.UploadFileChunk) error {
	s.frames++
	s.worker.uploaded = append(s.worker.uploaded, c.Content...)
	s.sent += int64(len(c.Content))
	return nil
}

func (s *fakeTransferUploadStream) CloseAndRecv() (*workerpb.UploadFileResponse, error) {
	if s.frames == 0 {
		// 零帧探测流：新 Worker 返回业务级失败（无副作用）。
		return &workerpb.UploadFileResponse{Success: false, Error: "缺少首帧"}, nil
	}
	return &workerpb.UploadFileResponse{Success: true, BytesWritten: s.sent}, nil
}

func (f *fakeTransferWorker) UploadFile(_ context.Context, _ ...grpc.CallOption) (workerpb.WorkerService_UploadFileClient, error) {
	return &fakeTransferUploadStream{worker: f}, nil
}

func (f *fakeTransferWorker) ReadFile(_ context.Context, req *workerpb.ReadFileRequest, _ ...grpc.CallOption) (*workerpb.ReadFileResponse, error) {
	b, ok := f.files[req.Path]
	if !ok {
		return nil, io.EOF
	}
	return &workerpb.ReadFileResponse{Content: b}, nil
}

type fakeTransferDownloadStream struct {
	grpc.ClientStream
	chunks []*workerpb.DownloadFileChunk
	idx    int
}

func (s *fakeTransferDownloadStream) Recv() (*workerpb.DownloadFileChunk, error) {
	if s.idx >= len(s.chunks) {
		return nil, io.EOF
	}
	c := s.chunks[s.idx]
	s.idx++
	return c, nil
}

func (f *fakeTransferWorker) DownloadFile(_ context.Context, req *workerpb.DownloadFileRequest, _ ...grpc.CallOption) (workerpb.WorkerService_DownloadFileClient, error) {
	b := f.files[req.Path]
	return &fakeTransferDownloadStream{chunks: []*workerpb.DownloadFileChunk{
		{Content: b, TotalSize: int64(len(b))},
	}}, nil
}

// seedTransferAgent 建节点+实例，签发持 instance.content 的 V2 Token 并注入 fake Worker。
func seedTransferAgent(t *testing.T, db *gorm.DB, pool *cpgrpc.ClientPool, worker workerpb.WorkerServiceClient) (*service.AgentPrincipal, *model.Instance) {
	t.Helper()
	node := createTestNode(t, db)
	node.UUID = "node-agent-transfer"
	require.NoError(t, db.Save(node).Error)
	inst := &model.Instance{
		UUID: "inst-agent-transfer", NodeID: node.ID, Name: "transfer",
		Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal,
		ProcessType: model.ProcessTypeDirect, StartCommand: "noop",
		Status: model.InstanceStatusStopped, WorkDir: "/srv/transfer",
	}
	require.NoError(t, db.Create(inst).Error)
	pool.SetWorkerClientForTest(node.UUID, worker)

	agentSvc := service.NewAgentTokenService(db)
	_, plain, err := agentSvc.Issue(service.IssueAgentTokenRequest{
		Name:                 "transfer-agent",
		ScopedInstanceIDs:    []uint{inst.ID},
		PolicyVersion:        service.AgentPolicyVersionV2,
		CapabilitiesProvided: true,
		Capabilities:         []string{service.AgentCapabilityInstanceContent},
		CreatedBy:            1,
	})
	require.NoError(t, err)
	p, err := agentSvc.Authenticate(plain)
	require.NoError(t, err)
	return p, inst
}

// issueTestTicket 用与测试路由相同的密钥派生签发票据。
func issueTestTicket(t *testing.T, db *gorm.DB, p *service.AgentPrincipal, instanceID uint, direction, path string) (*service.AgentTransferTicketService, string) {
	t.Helper()
	agentSvc := service.NewAgentTokenService(db)
	tickets, err := service.NewAgentTransferTicketService(
		service.DeriveAgentTransferTicketSecret([]byte(testTransferMasterSecret)), agentSvc, nil)
	require.NoError(t, err)
	ticket, _, err := tickets.Issue(p, instanceID, direction, path)
	require.NoError(t, err)
	return tickets, ticket
}

func TestAgentTransfer_UploadStreamsToWorker(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	worker := &fakeTransferWorker{files: map[string][]byte{}}
	r := setupTransferTestRouter(t, db, pool)
	p, inst := seedTransferAgent(t, db, pool, worker)
	_, ticket := issueTestTicket(t, db, p, inst.ID, service.AgentTransferDirectionUpload, "plugins/a.jar")

	content := bytes.Repeat([]byte("jar-bytes"), 20000) // ~180KB，跨多分片
	req := httptest.NewRequest(http.MethodPut,
		"/api/v1/agent-transfer/upload?ticket="+url.QueryEscape(ticket), bytes.NewReader(content))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, content, worker.uploaded, "上传内容应逐字节一致")
}

func TestAgentTransfer_UploadSnapshotsBeforeOverwrite(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	worker := &fakeTransferWorker{files: map[string][]byte{"plugins/a.jar": []byte("old jar")}}
	r := setupTransferTestRouter(t, db, pool)
	p, inst := seedTransferAgent(t, db, pool, worker)
	_, ticket := issueTestTicket(t, db, p, inst.ID, service.AgentTransferDirectionUpload, "plugins/a.jar")

	req := httptest.NewRequest(http.MethodPut,
		"/api/v1/agent-transfer/upload?ticket="+url.QueryEscape(ticket), bytes.NewReader([]byte("new jar")))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var count int64
	require.NoError(t, db.Model(&model.FileVersion{}).
		Where("instance_id = ? AND file_path = ?", inst.ID, "plugins/a.jar").Count(&count).Error)
	assert.Equal(t, int64(1), count, "票据上传覆盖前应产生改前快照")
}

func TestAgentTransfer_DownloadStreams(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	worker := &fakeTransferWorker{files: map[string][]byte{"logs/latest.log": []byte("hello log")}}
	r := setupTransferTestRouter(t, db, pool)
	p, inst := seedTransferAgent(t, db, pool, worker)
	_, ticket := issueTestTicket(t, db, p, inst.ID, service.AgentTransferDirectionDownload, "logs/latest.log")

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/agent-transfer/download?ticket="+url.QueryEscape(ticket), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "hello log", w.Body.String())
}

func TestAgentTransfer_TicketRejections(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	worker := &fakeTransferWorker{files: map[string][]byte{}}
	r := setupTransferTestRouter(t, db, pool)
	p, inst := seedTransferAgent(t, db, pool, worker)

	doUpload := func(ticket string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut,
			"/api/v1/agent-transfer/upload?ticket="+url.QueryEscape(ticket), bytes.NewReader([]byte("x")))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// 缺票据
	assert.Equal(t, http.StatusForbidden, doUpload("").Code)
	// 伪造票据
	assert.Equal(t, http.StatusForbidden, doUpload("not-a-ticket").Code)

	// 一次性：复用被拒
	_, ticket := issueTestTicket(t, db, p, inst.ID, service.AgentTransferDirectionUpload, "plugins/a.jar")
	require.Equal(t, http.StatusOK, doUpload(ticket).Code)
	assert.Equal(t, http.StatusForbidden, doUpload(ticket).Code, "票据不得复用")

	// 方向绑定：下载票据不可用于上传端点
	_, dl := issueTestTicket(t, db, p, inst.ID, service.AgentTransferDirectionDownload, "plugins/a.jar")
	assert.Equal(t, http.StatusForbidden, doUpload(dl).Code, "下载票据不得用于上传")

	// 端点不接受任何路径/实例参数：额外 query 不改变落点
	_, ticket2 := issueTestTicket(t, db, p, inst.ID, service.AgentTransferDirectionUpload, "plugins/b.jar")
	req := httptest.NewRequest(http.MethodPut,
		"/api/v1/agent-transfer/upload?ticket="+url.QueryEscape(ticket2)+"&path=../../evil&id=99",
		bytes.NewReader([]byte("y")))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code, "额外参数应被忽略而非影响落点")
}

func TestAgentTransfer_RevokedTokenRejected(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	worker := &fakeTransferWorker{files: map[string][]byte{}}
	r := setupTransferTestRouter(t, db, pool)
	p, inst := seedTransferAgent(t, db, pool, worker)
	_, ticket := issueTestTicket(t, db, p, inst.ID, service.AgentTransferDirectionUpload, "plugins/a.jar")

	require.NoError(t, service.NewAgentTokenService(db).Revoke(p.TokenID))

	req := httptest.NewRequest(http.MethodPut,
		"/api/v1/agent-transfer/upload?ticket="+url.QueryEscape(ticket), bytes.NewReader([]byte("x")))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code, "Token 吊销后票据必须失效")
}

func TestAgentTransfer_NoAgentHeaderNeeded(t *testing.T) {
	// 票据即凭据：不带 Authorization 头也能消费（端点不在 AgentAuth 组）。
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	worker := &fakeTransferWorker{files: map[string][]byte{"a.txt": []byte("ok")}}
	r := setupTransferTestRouter(t, db, pool)
	p, inst := seedTransferAgent(t, db, pool, worker)
	_, ticket := issueTestTicket(t, db, p, inst.ID, service.AgentTransferDirectionDownload, "a.txt")

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/agent-transfer/download?ticket="+url.QueryEscape(ticket), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}
