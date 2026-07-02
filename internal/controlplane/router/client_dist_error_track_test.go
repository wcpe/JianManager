package router

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

// setupClientDistRouterWithTracking 建一个含客户端分发消费端点的最小引擎（FR-249 追踪事件覆盖）。
// FR-256 起 manifest 不再签名，装配不再依赖签名器。返回引擎与追踪服务（供断言落库）。
func setupClientDistRouterWithTracking(t *testing.T, db *gorm.DB) (*gin.Engine, *service.ClientDistTrackingService) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	jwtCfg := config.JWTConfig{Secret: "test-secret-key-for-testing", AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour}

	root, err := dataroot.Init(filepath.Join(os.TempDir(), "jm-clientdist-errtrack-"+strconv.FormatInt(time.Now().UnixNano(), 10)))
	if err != nil {
		t.Fatalf("初始化数据根失败: %v", err)
	}
	assetSvc := service.NewAssetService(db, root)
	channelSvc := service.NewClientChannelService(db)
	versionSvc := service.NewClientVersionService(db, assetSvc, channelSvc)
	tracking := service.NewClientDistTrackingService(db)
	securitySvc := service.NewClientDistSecurityService(db, channelSvc, versionSvc)

	svcs := &Services{
		Auth:               service.NewAuthService(db, jwtCfg),
		Authz:              service.NewAuthzService(db),
		Audit:              service.NewAuditService(db),
		Asset:              assetSvc,
		ClientChannel:      channelSvc,
		ClientVersion:      versionSvc,
		ClientMachine:      service.NewClientMachineService(db),
		ClientDistTracking: tracking,
		ClientIPGuard:      service.NewClientIPGuardService(db),
		ClientTelemetry:    service.NewClientTelemetryService(db),
		ClientDistStats:    service.NewClientDistStatsService(db),
		ClientDistSecurity: securitySvc,
	}
	_ = cpgrpc.NewClientPool()
	return Setup(svcs, jwtCfg.Secret), tracking
}

// latestEvent 返回追踪服务里最新一条事件（created_at DESC 首行）；无则 t.Fatal。
func latestEvent(t *testing.T, tracking *service.ClientDistTrackingService, kind string) (status int, errCode string) {
	t.Helper()
	evs, err := tracking.QueryEvents(service.ClientDistEventFilter{Kind: kind, Limit: 1})
	if err != nil {
		t.Fatalf("检索事件失败: %v", err)
	}
	if len(evs) == 0 {
		t.Fatalf("期望记录 %s 事件，实际无", kind)
	}
	return evs[0].Status, evs[0].ErrCode
}

// TestClientDist_Manifest_AuthFailure_Recorded 鉴权失败(401)也记录追踪事件且带 INVALID_CLIENT_KEY（FR-249 核心：此前漏记）。
func TestClientDist_Manifest_AuthFailure_Recorded(t *testing.T) {
	db := setupTestDB(t)
	r, tracking := setupClientDistRouterWithTracking(t, db)
	token := getAdminToken(t, r)
	const channelID = "s1"
	_ = createChannelAndKey(t, r, token, channelID)

	// 无效密钥拉 manifest → 401，且必须落一条失败事件。
	req := httptest.NewRequest("GET", "/api/v1/client-channels/"+channelID+"/manifest", nil)
	req.Header.Set("X-Client-Key", "jmck_invalid")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无效密钥应 401，实际 %d", w.Code)
	}

	status, errCode := latestEvent(t, tracking, "manifest")
	if status != http.StatusUnauthorized {
		t.Errorf("事件 status 应 401，实际 %d", status)
	}
	if errCode != "INVALID_CLIENT_KEY" {
		t.Errorf("事件 errCode 应 INVALID_CLIENT_KEY，实际 %q", errCode)
	}
}

// TestClientDist_Manifest_NoVersion_Recorded 鉴权通过但无 latest → 404 记 NO_LATEST_VERSION（FR-249）。
func TestClientDist_Manifest_NoVersion_Recorded(t *testing.T) {
	db := setupTestDB(t)
	r, tracking := setupClientDistRouterWithTracking(t, db)
	token := getAdminToken(t, r)
	const channelID = "empty"
	key := createChannelAndKey(t, r, token, channelID)

	req := httptest.NewRequest("GET", "/api/v1/client-channels/"+channelID+"/manifest", nil)
	req.Header.Set("X-Client-Key", key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("无版本应 404，实际 %d", w.Code)
	}

	status, errCode := latestEvent(t, tracking, "manifest")
	if status != http.StatusNotFound {
		t.Errorf("事件 status 应 404，实际 %d", status)
	}
	if errCode != "NO_LATEST_VERSION" {
		t.Errorf("事件 errCode 应 NO_LATEST_VERSION，实际 %q", errCode)
	}
}

// TestClientDist_Artifact_AuthFailure_Recorded 制品鉴权失败(401)记 INVALID_CLIENT_KEY（FR-249）。
func TestClientDist_Artifact_AuthFailure_Recorded(t *testing.T) {
	db := setupTestDB(t)
	r, tracking := setupClientDistRouterWithTracking(t, db)

	// 无 key 取制品 → 401（此前鉴权失败漏记）。
	req := httptest.NewRequest("GET", "/api/v1/client-artifacts/"+sha256Hex2("whatever"), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无 key 取制品应 401，实际 %d", w.Code)
	}

	status, errCode := latestEvent(t, tracking, "artifact")
	if status != http.StatusUnauthorized {
		t.Errorf("制品事件 status 应 401，实际 %d", status)
	}
	if errCode != "INVALID_CLIENT_KEY" {
		t.Errorf("制品事件 errCode 应 INVALID_CLIENT_KEY，实际 %q", errCode)
	}
}

// TestClientDist_Artifact_NotFound_Recorded 鉴权通过但制品不存在 → 404 记 ARTIFACT_NOT_FOUND（FR-249）。
func TestClientDist_Artifact_NotFound_Recorded(t *testing.T) {
	db := setupTestDB(t)
	r, tracking := setupClientDistRouterWithTracking(t, db)
	token := getAdminToken(t, r)
	const channelID = "s1"
	key := createChannelAndKey(t, r, token, channelID)

	req := httptest.NewRequest("GET", "/api/v1/client-artifacts/"+sha256Hex2("nope"), nil)
	req.Header.Set("X-Client-Key", key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("不存在制品应 404，实际 %d", w.Code)
	}

	status, errCode := latestEvent(t, tracking, "artifact")
	if status != http.StatusNotFound {
		t.Errorf("制品事件 status 应 404，实际 %d", status)
	}
	if errCode != "ARTIFACT_NOT_FOUND" {
		t.Errorf("制品事件 errCode 应 ARTIFACT_NOT_FOUND，实际 %q", errCode)
	}
}

// TestClientDist_Manifest_Success_NoErrCode 成功拉取事件 errCode 为空（不回归 FR-093）。
func TestClientDist_Manifest_Success_NoErrCode(t *testing.T) {
	db := setupTestDB(t)
	r, tracking := setupClientDistRouterWithTracking(t, db)
	token := getAdminToken(t, r)
	const channelID = "s1"
	key := createChannelAndKey(t, r, token, channelID)
	artSha, artSize := uploadClientFile(t, r, token, channelID, []byte("x"))
	pubBody := map[string]any{
		"managedDirs": []string{"mods"},
		"files": []map[string]any{{
			"path": "mods/a.jar", "sha256": sha256Hex2("a"), "md5": "m", "size": 1,
			"sync": "strict", "platform": "",
			"artifact": map[string]any{"sha256": artSha, "size": artSize, "codec": "zstd"},
		}},
	}
	if w := makeRequest(r, "POST", "/api/v1/client-channels/"+channelID+"/versions", pubBody, token); w.Code != http.StatusCreated {
		t.Fatalf("发布版本失败: %d %s", w.Code, w.Body.String())
	}

	req := httptest.NewRequest("GET", "/api/v1/client-channels/"+channelID+"/manifest", nil)
	req.Header.Set("X-Client-Key", key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("拉取应 200，实际 %d body=%s", w.Code, w.Body.String())
	}

	status, errCode := latestEvent(t, tracking, "manifest")
	if status != http.StatusOK {
		t.Errorf("成功事件 status 应 200，实际 %d", status)
	}
	if errCode != "" {
		t.Errorf("成功事件 errCode 应为空，实际 %q", errCode)
	}
}

// TestClientDist_ListEvents_OutcomeFilter ListEvents 读 outcome/errCode query 参数并透传（FR-249）。
func TestClientDist_ListEvents_OutcomeFilter(t *testing.T) {
	db := setupTestDB(t)
	r, tracking := setupClientDistRouterWithTracking(t, db)
	token := getAdminToken(t, r)

	// 直接落两条：一成功一失败。
	if err := tracking.Record(service.ClientDistEventInput{ChannelID: "s1", Kind: "manifest", Version: 1, Status: 200}); err != nil {
		t.Fatalf("写成功事件失败: %v", err)
	}
	if err := tracking.Record(service.ClientDistEventInput{ChannelID: "s1", Kind: "manifest", Status: 401, ErrCode: "INVALID_CLIENT_KEY"}); err != nil {
		t.Fatalf("写失败事件失败: %v", err)
	}

	// outcome=failure 只返失败。
	w := makeRequest(r, "GET", "/api/v1/client-dist/events?outcome=failure", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("检索应 200，实际 %d", w.Code)
	}
	arr := parseJSONArray(t, w)
	if len(arr) != 1 {
		t.Fatalf("outcome=failure 应返 1 条，实际 %d", len(arr))
	}
	row := arr[0].(map[string]any)
	if row["errCode"] != "INVALID_CLIENT_KEY" {
		t.Errorf("失败事件 errCode 应回 INVALID_CLIENT_KEY，实际 %v", row["errCode"])
	}

	// outcome=success 只返成功。
	w = makeRequest(r, "GET", "/api/v1/client-dist/events?outcome=success", nil, token)
	arr = parseJSONArray(t, w)
	if len(arr) != 1 {
		t.Fatalf("outcome=success 应返 1 条，实际 %d", len(arr))
	}
	if s, _ := arr[0].(map[string]any)["status"].(float64); int(s) != http.StatusOK {
		t.Errorf("成功事件 status 应 200，实际 %v", arr[0].(map[string]any)["status"])
	}

	// errCode 精确筛。
	w = makeRequest(r, "GET", "/api/v1/client-dist/events?errCode=INVALID_CLIENT_KEY", nil, token)
	arr = parseJSONArray(t, w)
	if len(arr) != 1 {
		t.Errorf("errCode 精确筛应返 1 条，实际 %d", len(arr))
	}
}
