package router

import (
	"net/http"
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

// setupSignKeyRouter 建一个仅含签名公钥端点的最小引擎（FR-248）。
// signer 为 nil 模拟未配置（理论上仅生成失败已 fatal，端点仍应稳妥返回 503）。
func setupSignKeyRouter(t *testing.T, db *gorm.DB, signer *service.ManifestSigner, source string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	jwtCfg := config.JWTConfig{Secret: "test-secret-key-for-testing", AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour}
	svcs := &Services{
		Auth:              service.NewAuthService(db, jwtCfg),
		Authz:             service.NewAuthzService(db),
		Audit:             service.NewAuditService(db),
		ClientSignKey:     signer,
		ClientSignKeySrc:  source,
	}
	_ = cpgrpc.NewClientPool()
	return Setup(svcs, jwtCfg.Secret)
}

// TestGetSignKey_AdminReturnsPublicKey 平台管理员取签名公钥 → 200 + {publicKey,keyId,source}。
func TestGetSignKey_AdminReturnsPublicKey(t *testing.T) {
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
	r := setupSignKeyRouter(t, db, signer, service.SignKeySourceGenerated)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/client-dist/sign-key", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("管理员取公钥应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if resp["publicKey"] != wantPub {
		t.Fatalf("公钥不符：期望 %q 实际 %v", wantPub, resp["publicKey"])
	}
	if resp["keyId"] != service.DefaultSignKeyID {
		t.Fatalf("keyId 应为 %q，实际 %v", service.DefaultSignKeyID, resp["keyId"])
	}
	if resp["source"] != service.SignKeySourceGenerated {
		t.Fatalf("source 应为 generated，实际 %v", resp["source"])
	}
}

// TestGetSignKey_NonAdminForbidden 非平台管理员取公钥 → 403（早于业务逻辑）。
func TestGetSignKey_NonAdminForbidden(t *testing.T) {
	db := setupTestDB(t)
	keyPath := filepath.Join(t.TempDir(), "client-sign-key.pem")
	signer, err := service.LoadOrGenerateSigner(keyPath, service.DefaultSignKeyID)
	if err != nil {
		t.Fatalf("生成签名器失败: %v", err)
	}
	r := setupSignKeyRouter(t, db, signer, service.SignKeySourceGenerated)
	_ = getAdminToken(t, r) // 触发 setup 建库/首管理员
	memberToken := getMemberToken(t, r, "bob", "password123")

	w := makeRequest(r, "GET", "/api/v1/client-dist/sign-key", nil, memberToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("非管理员取公钥应 403，实际 %d: %s", w.Code, w.Body.String())
	}
}

// TestGetSignKey_NilSignerServiceUnavailable signer 为 nil（未配置）→ 503 SIGN_KEY_NOT_CONFIGURED。
func TestGetSignKey_NilSignerServiceUnavailable(t *testing.T) {
	db := setupTestDB(t)
	r := setupSignKeyRouter(t, db, nil, "")
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/client-dist/sign-key", nil, token)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil signer 应 503，实际 %d: %s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if resp["error"] != "SIGN_KEY_NOT_CONFIGURED" {
		t.Fatalf("错误码应 SIGN_KEY_NOT_CONFIGURED，实得 %v", resp["error"])
	}
}

// 防未用 import（dataroot/os/strconv 供潜在扩展）。
var (
	_ = dataroot.Init
	_ = os.Stat
	_ = strconv.Itoa
)
