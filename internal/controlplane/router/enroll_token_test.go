package router

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// setupEnrollRouter 建一个仅含节点 enrollment token 路由的最小引擎（挂平台管理员组）。
func setupEnrollRouter(t *testing.T, db *gorm.DB) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	jwtCfg := config.JWTConfig{Secret: "test-secret-key-for-testing", AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour}
	svcs := &Services{
		Auth:        service.NewAuthService(db, jwtCfg),
		User:        service.NewUserService(db),
		Authz:       service.NewAuthzService(db),
		Audit:       service.NewAuditService(db),
		EnrollToken: service.NewEnrollTokenService(db),
		EnrollInstall: EnrollInstallConfig{
			GRPCPort: 9100,
		},
	}
	_ = cpgrpc.NewClientPool()
	return Setup(svcs, jwtCfg.Secret)
}

// TestEnrollToken_Issue_ReturnsPlaintextAndCommands 签发返回明文 + 两端一键命令，含 token 与 CP 地址。
func TestEnrollToken_Issue_ReturnsPlaintextAndCommands(t *testing.T) {
	db := setupTestDB(t)
	r := setupEnrollRouter(t, db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "POST", "/api/v1/nodes/enroll-token",
		map[string]any{"nodeName": "edge-1", "ttlMinutes": 60}, token)
	if w.Code != http.StatusCreated {
		t.Fatalf("签发失败: status=%d body=%s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)

	plaintext, _ := resp["token"].(string)
	if !strings.HasPrefix(plaintext, "jmet_") {
		t.Fatalf("明文 token 应以 jmet_ 前缀，得到 %q", plaintext)
	}
	if resp["nodeName"] != "edge-1" {
		t.Fatalf("nodeName 应回显 edge-1，得到 %v", resp["nodeName"])
	}
	grpcAddr, _ := resp["controlPlaneGrpc"].(string)
	if !strings.HasSuffix(grpcAddr, ":9100") {
		t.Fatalf("controlPlaneGrpc 应含 gRPC 端口 9100，得到 %q", grpcAddr)
	}
	linux, _ := resp["installCommandLinux"].(string)
	if !strings.Contains(linux, plaintext) || !strings.Contains(linux, "install-worker.sh") || !strings.Contains(linux, "--name edge-1") {
		t.Fatalf("Linux 一键命令缺 token/脚本/节点名: %q", linux)
	}
	win, _ := resp["installCommandWindows"].(string)
	if !strings.Contains(win, plaintext) || !strings.Contains(win, "install-worker.ps1") {
		t.Fatalf("Windows 一键命令缺 token/脚本: %q", win)
	}
}

// TestEnrollToken_Issue_RequiresPlatformAdmin 非平台管理员签发被 RequireRole 中间件拒绝（403）。
func TestEnrollToken_Issue_RequiresPlatformAdmin(t *testing.T) {
	db := setupTestDB(t)
	r := setupEnrollRouter(t, db)
	_ = getAdminToken(t, r) // 先建管理员占位，使后续注册的是普通成员
	member := getMemberToken(t, r, "member1", "password123")

	w := makeRequest(r, "POST", "/api/v1/nodes/enroll-token", map[string]any{}, member)
	if w.Code != http.StatusForbidden {
		t.Fatalf("普通成员签发应 403，得到 status=%d body=%s", w.Code, w.Body.String())
	}
}

// TestEnrollToken_ListAndRevoke 列出含刚签发的 token，吊销后再吊销不存在的返回 404。
func TestEnrollToken_ListAndRevoke(t *testing.T) {
	db := setupTestDB(t)
	r := setupEnrollRouter(t, db)
	token := getAdminToken(t, r)

	issue := makeRequest(r, "POST", "/api/v1/nodes/enroll-token", map[string]any{}, token)
	if issue.Code != http.StatusCreated {
		t.Fatalf("签发失败: status=%d body=%s", issue.Code, issue.Body.String())
	}
	id := uint(parseJSON(t, issue)["tokenId"].(float64))

	list := makeRequest(r, "GET", "/api/v1/nodes/enroll-tokens", nil, token)
	if list.Code != http.StatusOK {
		t.Fatalf("列出失败: status=%d body=%s", list.Code, list.Body.String())
	}
	arr := parseJSONArray(t, list)
	if len(arr) != 1 {
		t.Fatalf("列表应含 1 条 token，得到 %d", len(arr))
	}
	// 列表绝不含明文。
	if first, ok := arr[0].(map[string]any); ok {
		if _, hasToken := first["token"]; hasToken {
			t.Fatalf("列表项不应含 token 明文")
		}
	}

	del := makeRequest(r, "DELETE", "/api/v1/nodes/enroll-tokens/"+itoa(id), nil, token)
	if del.Code != http.StatusOK {
		t.Fatalf("吊销失败: status=%d body=%s", del.Code, del.Body.String())
	}
	delMissing := makeRequest(r, "DELETE", "/api/v1/nodes/enroll-tokens/99999", nil, token)
	if delMissing.Code != http.StatusNotFound {
		t.Fatalf("吊销不存在 token 应 404，得到 status=%d", delMissing.Code)
	}
}
