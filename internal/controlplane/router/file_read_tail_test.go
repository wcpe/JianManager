package router

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakeTailReadWorker 记录调用方传入的 tail_bytes，并回写 truncated/total_size，
// 用来确认 CP 确实把尾部窗口透传下去、且把结果如实回告给客户端。
type fakeTailReadWorker struct {
	workerpb.WorkerServiceClient
	lastTailBytes int64
	content       []byte
	wroteTrunc    bool
	totalSize     int64
}

func (f *fakeTailReadWorker) ReadFile(_ context.Context, req *workerpb.ReadFileRequest, _ ...grpc.CallOption) (*workerpb.ReadFileResponse, error) {
	f.lastTailBytes = req.TailBytes
	if req.Path != "logs/latest.log" {
		return nil, fmt.Errorf("读取文件失败: 文件不存在")
	}
	return &workerpb.ReadFileResponse{
		Content:   f.content,
		Truncated: f.wroteTrunc,
		TotalSize: f.totalSize,
	}, nil
}

// setupTailReadTest 建路由 + 在线节点 + 实例 + 注入 fake Worker。
func setupTailReadTest(t *testing.T, worker workerpb.WorkerServiceClient) (r *gin.Engine, token string, instID uint) {
	t.Helper()
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r = setupTestRouterWithPool(db, pool)
	token = getAdminToken(t, r)
	node := createTestNode(t, db)
	pool.SetWorkerClientForTest(node.UUID, worker)
	inst := &model.Instance{
		NodeID:       node.ID,
		Name:         "tail-read",
		Type:         model.InstanceTypeGeneric,
		Role:         model.InstanceRoleUniversal,
		ProcessType:  model.ProcessTypeDirect,
		StartCommand: "noop",
		Status:       model.InstanceStatusStopped,
		WorkDir:      "/srv/tail",
	}
	require.NoError(t, db.Create(inst).Error)
	return r, token, inst.ID
}

// TestFileRead_TailWindowPassedThrough 带 tailBytes 时 CP 必须把窗口透传给 Worker，
// 并在响应头回告「仅尾部 + 文件总大小」，供前端提示而不能当完整正文展示。
func TestFileRead_TailWindowPassedThrough(t *testing.T) {
	const total = int64(1782579) // 1.7MB，即用户报告的那份日志量级
	worker := &fakeTailReadWorker{
		content:    []byte("last line of the log\n"),
		wroteTrunc: true,
		totalSize:  total,
	}
	r, token, instID := setupTailReadTest(t, worker)

	w := makeRequest(r, http.MethodGet,
		"/api/v1/instances/"+itoa(instID)+"/files/read?path=logs/latest.log&tailBytes=262144", nil, token)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, int64(262144), worker.lastTailBytes, "CP 必须把尾部窗口原样透传给 Worker")
	require.Equal(t, "last line of the log\n", w.Body.String(), "正文必须是纯文本，不得包成 JSON")
	require.Equal(t, "1", w.Header().Get("X-File-Truncated"), "必须回告内容仅为尾部")
	require.Equal(t, strconv.FormatInt(total, 10), w.Header().Get("X-File-Total-Size"))
}

// TestFileRead_NoTailBytesReadsWholeFile 不传 tailBytes 时保持既有全量语义（既有调用方零影响）。
func TestFileRead_NoTailBytesReadsWholeFile(t *testing.T) {
	content := []byte("server-port=25565\n")
	worker := &fakeTailReadWorker{content: content, totalSize: int64(len(content))}
	r, token, instID := setupTailReadTest(t, worker)

	w := makeRequest(r, http.MethodGet,
		"/api/v1/instances/"+itoa(instID)+"/files/read?path=logs/latest.log", nil, token)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, int64(0), worker.lastTailBytes, "不传 tailBytes 应读全量")
	require.Equal(t, string(content), w.Body.String())
	require.Empty(t, w.Header().Get("X-File-Truncated"), "完整正文不得标截断")
}

// TestFileRead_InvalidTailBytes 非法 tailBytes 明确报错，不静默当全量。
func TestFileRead_InvalidTailBytes(t *testing.T) {
	for _, raw := range []string{"abc", "-1"} {
		worker := &fakeTailReadWorker{content: []byte("x")}
		r, token, instID := setupTailReadTest(t, worker)

		w := makeRequest(r, http.MethodGet,
			"/api/v1/instances/"+itoa(instID)+"/files/read?path=logs/latest.log&tailBytes="+raw, nil, token)

		require.Equal(t, http.StatusBadRequest, w.Code, "tailBytes=%s 应报参数错误: %s", raw, w.Body.String())
		require.Equal(t, int64(0), worker.lastTailBytes, "参数非法时不得发请求给 Worker")
	}
}
