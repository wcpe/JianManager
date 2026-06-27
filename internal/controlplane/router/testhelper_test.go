package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	"github.com/wcpe/JianManager/internal/controlplane/database"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

// setupTestDB 创建临时 SQLite 数据库并运行自动迁移。
// 通过 t.Cleanup 确保测试结束时关闭数据库连接。
func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	tmpDir := t.TempDir()
	db, err := gorm.Open(sqlite.Open(tmpDir+"/test.db"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := database.AutoMigrate(db); err != nil {
		t.Fatalf("自动迁移失败: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	})
	return db
}

// setupTestRouter 创建配置好所有服务的测试路由引擎。
func setupTestRouter(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	jwtCfg := config.JWTConfig{
		Secret:     "test-secret-key-for-testing",
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 7 * 24 * time.Hour,
	}
	groupSvc := service.NewGroupService(db)
	pool := cpgrpc.NewClientPool()
	authzSvc := service.NewAuthzService(db)
	fileSvc := service.NewFileService(db, pool)
	fileVersionSvc := service.NewFileVersionService(db, pool, service.DefaultFileVersionConfig())
	configSvc := service.NewConfigService(db, pool)
	nodeSvc := service.NewNodeService(db)
	instanceSvc := service.NewInstanceService(db, groupSvc, pool)
	// 测试环境无 Worker 连接：禁用后台异步委托。否则 Stop/Start 等同步转换后，
	// delegateToWorker 会因节点不可达把状态异步覆盖为 CRASHED，且可能在用例结束
	// 关闭 DB 后仍写库——这正是 TestNode_Drain_StopsRunning 整包跑偶发失败的根因。
	instanceSvc.Shutdown()
	// 回填实例服务，供节点排空（drain）测试复用实例停止逻辑（FR-048）。
	nodeSvc.SetInstanceService(instanceSvc)
	// 制品库需要数据根；测试用临时根，进程退出后由 OS 回收。
	root, _ := dataroot.Init(filepath.Join(os.TempDir(), "jm-test-"+strconv.FormatInt(time.Now().UnixNano(), 10)))
	svcs := &Services{
		Auth:          service.NewAuthService(db, jwtCfg),
		User:          service.NewUserService(db),
		Group:         groupSvc,
		Node:          nodeSvc,
		NodeRepair:    service.NewNodeRepairService(db),
		Instance:      instanceSvc,
		InstanceBatch: service.NewInstanceBatchService(db, pool),
		InstanceGroup: service.NewInstanceGroupService(db),
		NodeRuntime:   service.NewNodeRuntimeService(db, pool),
		ProbeUpdate:   service.NewProbeUpdateService(db, pool, service.NewPluginBridgeService(jwtCfg.Secret)),
		Terminal:      service.NewTerminalService(db, jwtCfg.Secret, "ws://localhost:8080"),
		File:          fileSvc,
		FileVersion:   fileVersionSvc,
		Config:        configSvc,
		Bot:           service.NewBotService(db, pool),
		Alert:         service.NewAlertService(db),
		AlertChannel:  service.NewAlertChannelService(db),
		Schedule:      service.NewScheduleService(db),
		Backup:        service.NewBackupService(db, pool),
		Template:      service.NewTemplateService(db),
		Audit:         service.NewAuditService(db),
		Authz:         authzSvc,
		Business:      service.NewBusinessService(db, pool),
		Asset:         service.NewAssetService(db, root),
		Storage:       service.NewStorageService(db, root),
		Log:           service.NewLogService(db, root, config.LogStoreConfig{Enabled: true, PersistPlatform: true}),
		Metric:        service.NewMetricService(db),
		DBBrowse:      service.NewDBBrowseService(db),
		SelfUpdate:    service.NewSelfUpdateService(db, pool, service.SelfUpdateConfig{}, root),
		ServerState:   service.NewServerStateService(db, pool),
	}
	return Setup(svcs, jwtCfg.Secret)
}

// getAdminToken 通过 setup 流程创建管理员并返回 access token。
func getAdminToken(t *testing.T, r *gin.Engine) string {
	t.Helper()

	body := `{"username":"admin","password":"password123"}`
	req := httptest.NewRequest("POST", "/api/v1/setup", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("创建管理员失败: status=%d, body=%s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	token, ok := resp["accessToken"].(string)
	if !ok || token == "" {
		t.Fatalf("响应中缺少 accessToken: %v", resp)
	}
	return token
}

// getMemberToken 注册一个普通成员并返回 access token。
func getMemberToken(t *testing.T, r *gin.Engine, username, password string) string {
	t.Helper()

	regBody, _ := json.Marshal(map[string]string{
		"username": username,
		"password": password,
	})
	req := httptest.NewRequest("POST", "/api/v1/auth/register", bytes.NewBuffer(regBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("注册用户失败: status=%d, body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewBuffer(regBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("登录失败: status=%d, body=%s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析登录响应失败: %v", err)
	}

	token, ok := resp["accessToken"].(string)
	if !ok || token == "" {
		t.Fatalf("登录响应中缺少 accessToken: %v", resp)
	}
	return token
}

// makeRequest 发送 HTTP 请求并返回响应。
func makeRequest(r *gin.Engine, method, path string, body interface{}, token string) *httptest.ResponseRecorder {
	var reqBody *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		reqBody = bytes.NewBuffer(b)
	} else {
		reqBody = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, reqBody)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// createTestNode 直接向数据库插入测试节点（节点注册走 gRPC，测试中直接写库）。
func createTestNode(t *testing.T, db *gorm.DB) *model.Node {
	t.Helper()
	node := &model.Node{
		Name:     "test-node",
		Host:     "127.0.0.1",
		GRPCPort: 9100,
		WSPort:   9101,
		Secret:   "test-node-secret",
		Status:   model.NodeStatusOnline,
	}
	if err := db.Create(node).Error; err != nil {
		t.Fatalf("创建测试节点失败: %v", err)
	}
	return node
}

// createTestNodeWithSuffix 创建带名称后缀的测试节点。
func createTestNodeWithSuffix(t *testing.T, db *gorm.DB, name string) *model.Node {
	t.Helper()
	node := &model.Node{
		Name:     name,
		Host:     "127.0.0.1",
		GRPCPort: 9100,
		WSPort:   9101,
		Secret:   "test-node-secret-" + name,
		Status:   model.NodeStatusOnline,
	}
	if err := db.Create(node).Error; err != nil {
		t.Fatalf("创建测试节点失败: %v", err)
	}
	return node
}

// parseJSON 解析响应 body 为 map。
func parseJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析 JSON 失败: %v\nbody: %s", err, w.Body.String())
	}
	return resp
}

// parseJSONArray 解析响应 body 为 slice。
func parseJSONArray(t *testing.T, w *httptest.ResponseRecorder) []interface{} {
	t.Helper()
	var resp []interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析 JSON 数组失败: %v\nbody: %s", err, w.Body.String())
	}
	return resp
}

// itoa uint 转字符串。
func itoa(n uint) string {
	return fmt.Sprintf("%d", n)
}
