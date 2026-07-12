package router

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
	"github.com/wcpe/JianManager/proto/workerpb"
)

type fakePluginBatchWorker struct {
	workerpb.WorkerServiceClient
	files  map[string]struct{}
	writes []string
}

func (f *fakePluginBatchWorker) ListFiles(_ context.Context, in *workerpb.ListFilesRequest, _ ...grpc.CallOption) (*workerpb.ListFilesResponse, error) {
	resp := &workerpb.ListFilesResponse{}
	prefix := in.Path + "/"
	for path := range f.files {
		if strings.HasPrefix(path, prefix) {
			resp.Files = append(resp.Files, &workerpb.FileInfo{Name: strings.TrimPrefix(path, prefix)})
		}
	}
	return resp, nil
}

func (f *fakePluginBatchWorker) WriteFile(_ context.Context, in *workerpb.WriteFileRequest, _ ...grpc.CallOption) (*workerpb.WriteFileResponse, error) {
	f.writes = append(f.writes, in.Path)
	f.files[in.Path] = struct{}{}
	return &workerpb.WriteFileResponse{Success: true}, nil
}

// UploadFile 模拟老 Worker（Unimplemented）：部署回退 WriteFile，既有 writes 断言不变。
func (f *fakePluginBatchWorker) UploadFile(_ context.Context, _ ...grpc.CallOption) (workerpb.WorkerService_UploadFileClient, error) {
	return nil, status.Error(codes.Unimplemented, "unknown method UploadFile")
}

func TestPluginBatchDeploy_Route(t *testing.T) {
	db := setupTestDB(t)
	root, err := dataroot.Init(filepath.Join(t.TempDir(), "data"))
	require.NoError(t, err)
	assetSvc := service.NewAssetService(db, root)
	asset, err := assetSvc.Ingest(strings.NewReader("jar-bytes"), service.IngestParams{
		Type:     model.AssetTypePlugin,
		Filename: "EssentialsX.jar",
	})
	require.NoError(t, err)
	node := model.Node{UUID: "node-plugin", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&node).Error)
	inst := model.Instance{UUID: "inst-plugin", NodeID: node.ID, Name: "srv", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, StartCommand: "x", WorkDir: "/srv/srv"}
	require.NoError(t, db.Create(&inst).Error)

	pool := cpgrpc.NewClientPool()
	fake := &fakePluginBatchWorker{files: map[string]struct{}{}}
	pool.SetWorkerClientForTest(node.UUID, fake)
	handler := NewPluginHandler(service.NewPluginService(db, pool, assetSvc), service.NewAuthzService(db), service.NewAuditService(db))

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxAccess, &service.UserAccess{UserID: 1, IsPlatformAdmin: true})
		c.Set(middleware.CtxUserID, uint(1))
		c.Next()
	})
	handler.RegisterRoutes(r.Group("/api/v1"))

	w := makeRequest(r, "POST", "/api/v1/plugins/batch-deploy", map[string]any{
		"assetIds": []uint{asset.ID},
		"target":   map[string]any{"ids": []uint{inst.ID}},
	}, "")

	require.Equal(t, 200, w.Code, w.Body.String())
	resp := parseJSON(t, w)
	require.Equal(t, float64(1), resp["succeeded"])
	require.Equal(t, []string{"plugins/EssentialsX.jar"}, fake.writes)
}

func TestPluginUpload_RouteRejectsExistingFileWithoutOverwrite(t *testing.T) {
	db := setupTestDB(t)
	node := model.Node{UUID: "node-plugin-upload", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&node).Error)
	inst := model.Instance{UUID: "inst-plugin-upload", NodeID: node.ID, Name: "srv", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, StartCommand: "x", WorkDir: "/srv/srv"}
	require.NoError(t, db.Create(&inst).Error)

	pool := cpgrpc.NewClientPool()
	fake := &fakePluginBatchWorker{files: map[string]struct{}{"plugins/EssentialsX.jar": {}}}
	pool.SetWorkerClientForTest(node.UUID, fake)
	handler := NewPluginHandler(service.NewPluginService(db, pool, nil), service.NewAuthzService(db), service.NewAuditService(db))

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxAccess, &service.UserAccess{UserID: 1, IsPlatformAdmin: true})
		c.Set(middleware.CtxUserID, uint(1))
		c.Next()
	})
	handler.RegisterRoutes(r.Group("/api/v1"))

	body := bytes.NewBuffer(nil)
	writer := multipart.NewWriter(body)
	require.NoError(t, writer.WriteField("dir", "plugins"))
	part, err := writer.CreateFormFile("file", "EssentialsX.jar")
	require.NoError(t, err)
	_, err = part.Write([]byte("jar-bytes"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/instances/%d/plugins", inst.ID), body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	resp := parseJSON(t, w)
	require.Equal(t, "FILE_EXISTS", resp["error"])
	require.Empty(t, fake.writes)
}

func TestPluginUpload_RouteAllowsOverwriteExistingFile(t *testing.T) {
	db := setupTestDB(t)
	node := model.Node{UUID: "node-plugin-upload-overwrite", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&node).Error)
	inst := model.Instance{UUID: "inst-plugin-upload-overwrite", NodeID: node.ID, Name: "srv", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, StartCommand: "x", WorkDir: "/srv/srv"}
	require.NoError(t, db.Create(&inst).Error)

	pool := cpgrpc.NewClientPool()
	fake := &fakePluginBatchWorker{files: map[string]struct{}{"plugins/EssentialsX.jar": {}}}
	pool.SetWorkerClientForTest(node.UUID, fake)
	handler := NewPluginHandler(service.NewPluginService(db, pool, nil), service.NewAuthzService(db), service.NewAuditService(db))

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(middleware.CtxAccess, &service.UserAccess{UserID: 1, IsPlatformAdmin: true})
		c.Set(middleware.CtxUserID, uint(1))
		c.Next()
	})
	handler.RegisterRoutes(r.Group("/api/v1"))

	body := bytes.NewBuffer(nil)
	writer := multipart.NewWriter(body)
	require.NoError(t, writer.WriteField("dir", "plugins"))
	require.NoError(t, writer.WriteField("overwrite", "true"))
	part, err := writer.CreateFormFile("file", "EssentialsX.jar")
	require.NoError(t, err)
	_, err = part.Write([]byte("jar-bytes"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/instances/%d/plugins", inst.ID), body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.Equal(t, []string{"plugins/EssentialsX.jar"}, fake.writes)
}
