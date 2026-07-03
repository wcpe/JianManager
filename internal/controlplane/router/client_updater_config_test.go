package router

import (
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// setupUpdaterConfigRouter 建一个含 jm-updater.json 生成端点的最小引擎（FR-253）。
// FR-256 起该端点不再依赖签名器（验签已去）。
func setupUpdaterConfigRouter(t *testing.T, db *gorm.DB) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	jwtCfg := config.JWTConfig{Secret: "test-secret-key-for-testing", AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour}
	svcs := &Services{
		Auth:          service.NewAuthService(db, jwtCfg),
		Authz:         service.NewAuthzService(db),
		Audit:         service.NewAuditService(db),
		ClientChannel: service.NewClientChannelService(db),
	}
	_ = cpgrpc.NewClientPool()
	return Setup(svcs, jwtCfg.Secret)
}

// TestGetUpdaterConfig_AdminReturnsConfig 平台管理员取 jm-updater.json →
// 200 + 含 channel / endpoint / key。FR-256 起不再返回 signPublicKey/signKeyId。
func TestGetUpdaterConfig_AdminReturnsConfig(t *testing.T) {
	db := setupTestDB(t)
	// 建测试频道。
	if _, e := service.NewClientChannelService(db).CreateChannel("skyblock-s1", "空岛一服", ""); e != nil {
		t.Fatalf("创建测试频道失败: %v", e)
	}
	r := setupUpdaterConfigRouter(t, db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/client-channels/skyblock-s1/updater-config?key=jmck_real", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if resp["channel"] != "skyblock-s1" {
		t.Fatalf("channel 应为 skyblock-s1，实得 %v", resp["channel"])
	}
	if resp["key"] != "jmck_real" {
		t.Fatalf("key 应为传入的真实拉取密钥，实得 %v", resp["key"])
	}
	if resp["endpoint"] == nil || resp["endpoint"] == "" {
		t.Fatalf("endpoint 应非空（CP 公网基址），实得 %v", resp["endpoint"])
	}
	if _, ok := resp["coreEndpoint"]; ok {
		t.Fatalf("jm-updater.json 不应再含 coreEndpoint，楔子应由 endpoint 自动拼接")
	}
	// FR-256 起 jm-updater.json 不再含签名公钥字段。
	if _, ok := resp["signPublicKey"]; ok {
		t.Fatalf("jm-updater.json 不应再含 signPublicKey（验签已去）")
	}
	if _, ok := resp["signKeyId"]; ok {
		t.Fatalf("jm-updater.json 不应再含 signKeyId（验签已去）")
	}
}

// TestGetUpdaterConfig_KeyRequired 缺少真实拉取密钥时拒绝生成配置，避免下发空 key。
func TestGetUpdaterConfig_KeyRequired(t *testing.T) {
	db := setupTestDB(t)
	if _, e := service.NewClientChannelService(db).CreateChannel("skyblock-s1", "空岛一服", ""); e != nil {
		t.Fatalf("创建测试频道失败: %v", e)
	}
	r := setupUpdaterConfigRouter(t, db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/client-channels/skyblock-s1/updater-config", nil, token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺 key 应 400，实际 %d: %s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if resp["error"] != "CLIENT_KEY_REQUIRED" {
		t.Fatalf("错误码应 CLIENT_KEY_REQUIRED，实得 %v", resp["error"])
	}
}

// TestGetUpdaterConfig_ChannelNotFound 频道不存在 → 404。
func TestGetUpdaterConfig_ChannelNotFound(t *testing.T) {
	db := setupTestDB(t)
	r := setupUpdaterConfigRouter(t, db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/client-channels/nonexistent/updater-config", nil, token)
	if w.Code != http.StatusNotFound {
		t.Fatalf("频道不存在应 404，实际 %d: %s", w.Code, w.Body.String())
	}
}

// TestGetUpdaterConfig_NonAdminForbidden 非平台管理员 → 403。
func TestGetUpdaterConfig_NonAdminForbidden(t *testing.T) {
	db := setupTestDB(t)
	if _, e := service.NewClientChannelService(db).CreateChannel("skyblock-s1", "空岛一服", ""); e != nil {
		t.Fatalf("创建测试频道失败: %v", e)
	}
	r := setupUpdaterConfigRouter(t, db)
	_ = getAdminToken(t, r) // 触发 setup 建库/首管理员
	memberToken := getMemberToken(t, r, "bob", "password123")

	w := makeRequest(r, "GET", "/api/v1/client-channels/skyblock-s1/updater-config", nil, memberToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("非管理员应 403，实际 %d: %s", w.Code, w.Body.String())
	}
}
