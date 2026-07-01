package router

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// setupUpdaterConfigRouter 建一个含 jm-updater.json 生成端点的最小引擎（FR-253）。
func setupUpdaterConfigRouter(t *testing.T, db *gorm.DB, signer *service.ManifestSigner) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	jwtCfg := config.JWTConfig{Secret: "test-secret-key-for-testing", AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour}
	svcs := &Services{
		Auth:          service.NewAuthService(db, jwtCfg),
		Authz:         service.NewAuthzService(db),
		Audit:         service.NewAuditService(db),
		ClientChannel: service.NewClientChannelService(db),
		ClientSignKey: signer,
	}
	_ = cpgrpc.NewClientPool()
	return Setup(svcs, jwtCfg.Secret)
}

// TestGetUpdaterConfig_AdminReturnsConfigWithSignKey 平台管理员取 jm-updater.json →
// 200 + 含 signPublicKey / signKeyId / channel / endpoint（key 占位空串）。
func TestGetUpdaterConfig_AdminReturnsConfigWithSignKey(t *testing.T) {
	db := setupTestDB(t)
	keyPath := filepath.Join(t.TempDir(), "client-sign-key.pem")
	signer, err := service.LoadOrGenerateSigner(keyPath, service.DefaultSignKeyID)
	if err != nil {
		t.Fatalf("生成签名器失败: %v", err)
	}
	wantPub, err := signer.PublicKeySPKIBase64()
	if err != nil {
		t.Fatalf("导出公钥失败: %v", err)
	}
	// 建测试频道。
	if _, e := service.NewClientChannelService(db).CreateChannel("skyblock-s1", "空岛一服", ""); e != nil {
		t.Fatalf("创建测试频道失败: %v", e)
	}
	r := setupUpdaterConfigRouter(t, db, signer)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/client-channels/skyblock-s1/updater-config", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if resp["signPublicKey"] != wantPub {
		t.Fatalf("signPublicKey 应为签名器公钥 %q，实得 %v", wantPub, resp["signPublicKey"])
	}
	if resp["signKeyId"] != service.DefaultSignKeyID {
		t.Fatalf("signKeyId 应为 %q，实得 %v", service.DefaultSignKeyID, resp["signKeyId"])
	}
	if resp["channel"] != "skyblock-s1" {
		t.Fatalf("channel 应为 skyblock-s1，实得 %v", resp["channel"])
	}
	if resp["key"] != "" {
		t.Fatalf("key 应为空串（占位），实得 %v", resp["key"])
	}
	if resp["endpoint"] == nil || resp["endpoint"] == "" {
		t.Fatalf("endpoint 应非空（CP 公网基址），实得 %v", resp["endpoint"])
	}
}

// TestGetUpdaterConfig_ChannelNotFound 频道不存在 → 404。
func TestGetUpdaterConfig_ChannelNotFound(t *testing.T) {
	db := setupTestDB(t)
	keyPath := filepath.Join(t.TempDir(), "client-sign-key.pem")
	signer, err := service.LoadOrGenerateSigner(keyPath, service.DefaultSignKeyID)
	if err != nil {
		t.Fatalf("生成签名器失败: %v", err)
	}
	r := setupUpdaterConfigRouter(t, db, signer)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/client-channels/nonexistent/updater-config", nil, token)
	if w.Code != http.StatusNotFound {
		t.Fatalf("频道不存在应 404，实际 %d: %s", w.Code, w.Body.String())
	}
}

// TestGetUpdaterConfig_NonAdminForbidden 非平台管理员 → 403。
func TestGetUpdaterConfig_NonAdminForbidden(t *testing.T) {
	db := setupTestDB(t)
	keyPath := filepath.Join(t.TempDir(), "client-sign-key.pem")
	signer, err := service.LoadOrGenerateSigner(keyPath, service.DefaultSignKeyID)
	if err != nil {
		t.Fatalf("生成签名器失败: %v", err)
	}
	if _, e := service.NewClientChannelService(db).CreateChannel("skyblock-s1", "空岛一服", ""); e != nil {
		t.Fatalf("创建测试频道失败: %v", e)
	}
	r := setupUpdaterConfigRouter(t, db, signer)
	_ = getAdminToken(t, r) // 触发 setup 建库/首管理员
	memberToken := getMemberToken(t, r, "bob", "password123")

	w := makeRequest(r, "GET", "/api/v1/client-channels/skyblock-s1/updater-config", nil, memberToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("非管理员应 403，实际 %d: %s", w.Code, w.Body.String())
	}
}

// TestGetUpdaterConfig_NilSigner503 signer 为 nil（未配置）→ 503。
func TestGetUpdaterConfig_NilSigner503(t *testing.T) {
	db := setupTestDB(t)
	if _, e := service.NewClientChannelService(db).CreateChannel("skyblock-s1", "空岛一服", ""); e != nil {
		t.Fatalf("创建测试频道失败: %v", e)
	}
	r := setupUpdaterConfigRouter(t, db, nil)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/client-channels/skyblock-s1/updater-config", nil, token)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil signer 应 503，实际 %d: %s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if resp["error"] != "SIGN_KEY_NOT_CONFIGURED" {
		t.Fatalf("错误码应 SIGN_KEY_NOT_CONFIGURED，实得 %v", resp["error"])
	}
}
