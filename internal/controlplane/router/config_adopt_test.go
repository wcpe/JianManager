package router

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// errAdoptTestMissing 模拟「文件不存在」的假 Worker 错误。
var errAdoptTestMissing = errors.New("读取配置失败: no such file or directory")

// 配置纳管路由端到端测试（FR-533）。
//
// 这里走 HTTP 层，验证三件在本层才成立的事：
//  1. 路由可达 —— adopt / adopt/preview / health 是静态段，不能被 /config-files/:id 通配吃掉；
//  2. 预演端点强制 dryRun —— 请求体写 dryRun=false 也必须零写入；
//  3. 错误映射 —— 请求级校验错误返回 400 而非 422。

// adoptFakeWorker 是只实现配置读取相关方法的假 Worker。
type adoptFakeWorker struct {
	workerpb.WorkerServiceClient
	// files 工作目录根下的「path → 内容」。
	files map[string]string
}

func (f *adoptFakeWorker) ListFiles(ctx context.Context, in *workerpb.ListFilesRequest, opts ...grpc.CallOption) (*workerpb.ListFilesResponse, error) {
	// Discover 是广度优先逐目录列取：这里只实现根目录（""），够本用例验证路由链路。
	if in.Path != "" {
		return &workerpb.ListFilesResponse{}, nil
	}
	out := make([]*workerpb.FileInfo, 0, len(f.files))
	for p := range f.files {
		out = append(out, &workerpb.FileInfo{Name: p, IsDir: false})
	}
	return &workerpb.ListFilesResponse{Files: out}, nil
}

func (f *adoptFakeWorker) ReadConfig(ctx context.Context, in *workerpb.ReadConfigRequest, opts ...grpc.CallOption) (*workerpb.ReadConfigResponse, error) {
	content, ok := f.files[in.Path]
	if !ok {
		return nil, errAdoptTestMissing
	}
	return &workerpb.ReadConfigResponse{Path: in.Path, Format: "properties", Content: content}, nil
}

// TestConfigFiles_AdoptEndpoints 覆盖 adopt / adopt/preview / health 的 HTTP 链路。
func TestConfigFiles_AdoptEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	node := createTestNode(t, db)
	inst := model.Instance{
		Name: "adopt-srv", UUID: "uuid-adopt-srv", NodeID: node.ID,
		Type: model.InstanceTypeMinecraftJava, Role: model.InstanceRoleBackend,
		ProcessType: model.ProcessTypeDaemon, StartCommand: "java -jar server.jar",
		WorkDir: t.TempDir(), Status: model.InstanceStatusRunning,
	}
	require.NoError(t, db.Create(&inst).Error)

	// 注入假 Worker：磁盘上有一份带注释的 properties。
	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest(node.UUID, &adoptFakeWorker{files: map[string]string{
		"server.properties": "# 主端口\nserver-port=25565\nmotd=hello\n",
	}})
	r := setupTestRouterWithPool(db, pool)
	adminTok := getAdminToken(t, r)

	reqBody := map[string]any{
		"files":          []map[string]any{{"instanceId": inst.ID, "path": "server.properties"}},
		"createBaseline": true,
	}

	// ① 预演：请求体 dryRun=false 也必须被强制成 dryRun，零写入。
	w := makeRequest(r, http.MethodPost, "/api/v1/config-files/adopt/preview", reqBody, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	preview := parseJSON(t, w)
	require.Equal(t, true, preview["dryRun"], "预览端点必须强制 dryRun")
	summary, ok := preview["summary"].(map[string]any)
	require.True(t, ok)
	require.EqualValues(t, 1, summary["created"])
	require.EqualValues(t, 0, summary["failed"])

	// 预演不产生 config_file：列表应为空。
	w = makeRequest(r, http.MethodGet, "/api/v1/config-files", nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	list := parseJSON(t, w)
	files, _ := list["files"].([]any)
	require.Empty(t, files, "预演不得创建配置文件")

	// ② 实跑：真的落库。
	w = makeRequest(r, http.MethodPost, "/api/v1/config-files/adopt", reqBody, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	done := parseJSON(t, w)
	require.Equal(t, false, done["dryRun"])
	results, ok := done["results"].([]any)
	require.True(t, ok)
	require.Len(t, results, 1)
	first := results[0].(map[string]any)
	require.Equal(t, "created", first["status"])
	require.Equal(t, "ADOPTED", first["code"])
	configFileID := uint(first["configFileId"].(float64))
	require.NotZero(t, configFileID)

	// 纳管后能经既有 /config-files/:id/effective 预览（验收 10 的 HTTP 面）。
	w = makeRequest(r, http.MethodGet,
		"/api/v1/config-files/"+itoa(configFileID)+"/effective?instanceId="+itoa(inst.ID), nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	eff := parseJSON(t, w)
	require.Equal(t, "hello", tracedValueOf(t, eff, "motd"))

	// ③ 重复纳管 → ALREADY_ADOPTED 且不覆盖。
	w = makeRequest(r, http.MethodPost, "/api/v1/config-files/adopt", reqBody, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	again := parseJSON(t, w)
	againResults := again["results"].([]any)
	require.Equal(t, "already_adopted", againResults[0].(map[string]any)["status"])
	require.Equal(t, "ALREADY_ADOPTED", againResults[0].(map[string]any)["code"])
	require.Equal(t, "hello", tracedValueOf(t, eff, "motd"), "重复纳管不得改动已纳管内容")

	// ④ health：静态段可达，只报告事实。
	w = makeRequest(r, http.MethodGet, "/api/v1/config-files/health", nil, adminTok)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	health := parseJSON(t, w)
	require.EqualValues(t, 1, health["configFileCount"])
	require.EqualValues(t, 1, health["layerVersionCount"])
	notice, _ := health["notice"].(string)
	require.Contains(t, notice, "无法自动检测")
}

// TestConfigFiles_AdoptBadRequest 覆盖请求级校验走 400（而不是逐文件 422）。
func TestConfigFiles_AdoptBadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	r := setupTestRouter(db)
	adminTok := getAdminToken(t, r)

	// 既无 files 也无 target。
	w := makeRequest(r, http.MethodPost, "/api/v1/config-files/adopt", map[string]any{}, adminTok)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Equal(t, "FILES_REQUIRED", parseJSON(t, w)["error"])

	// 有 target 但没有 pathPatterns。
	w = makeRequest(r, http.MethodPost, "/api/v1/config-files/adopt", map[string]any{
		"target": map[string]any{"groupIds": []uint{1}, "includeDescendants": true},
	}, adminTok)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Equal(t, "PATTERN_REQUIRED", parseJSON(t, w)["error"])

	// target 维度全空：属「未指定目标」的非法请求，与 BusinessBatchErrorCode 同口径归 INVALID_REQUEST
	// （TARGET_EMPTY 留给「目标已指定但解析后为空」）。
	w = makeRequest(r, http.MethodPost, "/api/v1/config-files/adopt", map[string]any{
		"target":       map[string]any{},
		"pathPatterns": []string{"*.yml"},
	}, adminTok)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Equal(t, "INVALID_REQUEST", parseJSON(t, w)["error"])
}

// tracedValueOf 取 effective 响应里某键的溯源值（字符串化）。
func tracedValueOf(t *testing.T, eff map[string]any, key string) string {
	t.Helper()
	traced, ok := eff["traced"].(map[string]any)
	require.True(t, ok, "effective 响应应含 traced")
	entry, ok := traced[key].(map[string]any)
	require.True(t, ok, "traced 里应有键 %s", key)
	v, ok := entry["value"].(string)
	require.True(t, ok, "值应为字符串")
	return v
}
