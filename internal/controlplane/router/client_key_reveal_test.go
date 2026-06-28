package router

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

// setupRevealRouter 建一个含客户端分发路由的最小引擎，按 withEnc 决定频道服务是否配可逆加密器（FR-192）。
// 返回引擎与配了（或没配）加密器的频道服务（供直接建带 KeyEnc 的密钥）。
func setupRevealRouter(t *testing.T, db *gorm.DB, withEnc bool) (*gin.Engine, *service.ClientChannelService) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	jwtCfg := config.JWTConfig{Secret: "test-secret-key-for-testing", AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour}

	root, err := dataroot.Init(filepath.Join(os.TempDir(), "jm-reveal-test-"+strconv.FormatInt(time.Now().UnixNano(), 10)))
	if err != nil {
		t.Fatalf("初始化数据根失败: %v", err)
	}
	channelSvc := service.NewClientChannelService(db)
	if withEnc {
		enc, _, err := service.ResolveKeyEncryptor("", true) // dev 回退一把可用密钥
		if err != nil || enc == nil {
			t.Fatalf("构造加密器失败: enc=%v err=%v", enc, err)
		}
		channelSvc.SetKeyEncryptor(enc)
	}

	svcs := &Services{
		Auth:          service.NewAuthService(db, jwtCfg),
		Authz:         service.NewAuthzService(db),
		Audit:         service.NewAuditService(db),
		Asset:         service.NewAssetService(db, root),
		ClientChannel: channelSvc,
	}
	_ = cpgrpc.NewClientPool()
	return Setup(svcs, jwtCfg.Secret), channelSvc
}

// TestRevealKey_AdminGetsPlaintext 平台管理员对可查看密钥取明文（200 + key），并写审计（detail 无明文）。
func TestRevealKey_AdminGetsPlaintext(t *testing.T) {
	db := setupTestDB(t)
	r, svc := setupRevealRouter(t, db, true)
	token := getAdminToken(t, r)

	if w := makeRequest(r, "POST", "/api/v1/client-channels",
		map[string]string{"channelId": "skyblock-s1", "name": "测试频道"}, token); w.Code != http.StatusCreated {
		t.Fatalf("建频道失败: %d %s", w.Code, w.Body.String())
	}
	// 经配了加密器的服务直接建密钥，拿到一次性明文（待与 reveal 结果比对）。
	key, plaintext, err := svc.CreateKey("skyblock-s1", "正式包", "", nil)
	if err != nil {
		t.Fatalf("建密钥失败: %v", err)
	}

	w := makeRequest(r, "GET", "/api/v1/client-channels/skyblock-s1/keys/"+itoa(key.ID)+"/reveal", nil, token)
	if w.Code != http.StatusOK {
		t.Fatalf("管理员 reveal 应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if resp["key"] != plaintext {
		t.Fatalf("reveal 明文不符：期望 %q 实际 %v", plaintext, resp["key"])
	}

	// 审计：记录 client_key.reveal 且 detail 绝不含明文。
	var cnt int64
	db.Table("audit_logs").Where("action = ?", "client_key.reveal").Count(&cnt)
	if cnt != 1 {
		t.Fatalf("应有 1 条 client_key.reveal 审计，实际 %d", cnt)
	}
	var detail string
	db.Table("audit_logs").Where("action = ?", "client_key.reveal").Select("detail").Scan(&detail)
	if strings.Contains(detail, plaintext) {
		t.Fatalf("审计 detail 绝不应含明文，实得: %s", detail)
	}
}

// TestRevealKey_NotRevealableWhenNoEnc 未配加密器（生产降级）的密钥不可查 → 404 KEY_NOT_REVEALABLE。
func TestRevealKey_NotRevealableWhenNoEnc(t *testing.T) {
	db := setupTestDB(t)
	r, svc := setupRevealRouter(t, db, false) // 频道服务无加密器：建出的密钥无 KeyEnc
	token := getAdminToken(t, r)

	if w := makeRequest(r, "POST", "/api/v1/client-channels",
		map[string]string{"channelId": "skyblock-s1", "name": "测试频道"}, token); w.Code != http.StatusCreated {
		t.Fatalf("建频道失败: %d %s", w.Code, w.Body.String())
	}
	key, _, err := svc.CreateKey("skyblock-s1", "正式包", "", nil)
	if err != nil {
		t.Fatalf("建密钥失败: %v", err)
	}

	w := makeRequest(r, "GET", "/api/v1/client-channels/skyblock-s1/keys/"+itoa(key.ID)+"/reveal", nil, token)
	if w.Code != http.StatusNotFound {
		t.Fatalf("无 KeyEnc 应 404，实际 %d: %s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if resp["error"] != "KEY_NOT_REVEALABLE" {
		t.Fatalf("错误码应 KEY_NOT_REVEALABLE，实得 %v", resp["error"])
	}
}

// TestRevealKey_NonAdminForbidden 非平台管理员 reveal → 403（早于业务逻辑）。
func TestRevealKey_NonAdminForbidden(t *testing.T) {
	db := setupTestDB(t)
	r, svc := setupRevealRouter(t, db, true)
	adminToken := getAdminToken(t, r)
	if w := makeRequest(r, "POST", "/api/v1/client-channels",
		map[string]string{"channelId": "skyblock-s1", "name": "测试频道"}, adminToken); w.Code != http.StatusCreated {
		t.Fatalf("建频道失败: %d %s", w.Code, w.Body.String())
	}
	key, _, err := svc.CreateKey("skyblock-s1", "正式包", "", nil)
	if err != nil {
		t.Fatalf("建密钥失败: %v", err)
	}

	memberToken := getMemberToken(t, r, "alice", "password123")
	w := makeRequest(r, "GET", "/api/v1/client-channels/skyblock-s1/keys/"+itoa(key.ID)+"/reveal", nil, memberToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("非管理员 reveal 应 403，实际 %d: %s", w.Code, w.Body.String())
	}
}
