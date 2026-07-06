package router

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/internal/version"
)

// setupEnrollRouter 建一个仅含节点 enrollment token 路由的最小引擎（挂平台管理员组）。
func setupEnrollRouter(t *testing.T, db *gorm.DB) *gin.Engine {
	return setupEnrollRouterWithInstall(t, db, EnrollInstallConfig{GRPCPort: 9100})
}

// setupEnrollRouterWithInstall 同 setupEnrollRouter，但允许注入自定义一键安装配置（如 BinaryURL）。
func setupEnrollRouterWithInstall(t *testing.T, db *gorm.DB, install EnrollInstallConfig) *gin.Engine {
	return setupEnrollRouterWithInstallAndSelfUpdate(t, db, install, nil)
}

func setupEnrollRouterWithInstallAndSelfUpdate(t *testing.T, db *gorm.DB, install EnrollInstallConfig, selfUpdate *service.SelfUpdateService) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	jwtCfg := config.JWTConfig{Secret: "test-secret-key-for-testing", AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour}
	svcs := &Services{
		Auth:          service.NewAuthService(db, jwtCfg),
		User:          service.NewUserService(db),
		Authz:         service.NewAuthzService(db),
		Audit:         service.NewAuditService(db),
		EnrollToken:   service.NewEnrollTokenService(db),
		EnrollInstall: install,
		SelfUpdate:    selfUpdate,
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
	// scriptBaseUrl 回显 CP 托管脚本的基址，供前端拼「手动安装步骤」兜底命令；一键命令的脚本 URL 应以它打头。
	scriptBase, _ := resp["scriptBaseUrl"].(string)
	if scriptBase == "" {
		t.Fatalf("响应应含 scriptBaseUrl，得到空")
	}
	linux, _ := resp["installCommandLinux"].(string)
	if !strings.Contains(linux, plaintext) || !strings.Contains(linux, scriptBase+"/install-worker.sh") || !strings.Contains(linux, "--name edge-1") {
		t.Fatalf("Linux 一键命令缺 token/脚本(基址)/节点名: %q", linux)
	}
	win, _ := resp["installCommandWindows"].(string)
	if !strings.Contains(win, plaintext) || !strings.Contains(win, "install-worker.ps1") {
		t.Fatalf("Windows 一键命令缺 token/脚本: %q", win)
	}
}

// TestBuildWindowsInstallCommand_PipesContentNotObject 回归 FIX-1（节点上线真机打通）：
// Windows 一键命令必须把脚本「内容字符串」喂给 iex（iex (iwr ...).Content），
// 不得把 iwr 的「响应对象」直接管道给 iex（iwr ... | iex）。
// 后者经 BasicHtmlWebResponseObject 串化会损坏 UTF-8（CJK）正文，PowerShell 7.6 解析在
// 含中文的字符串插值行（install-worker.ps1:71 "windows/$arch，安装目录: ..."）报
// 「Variable reference is not valid ':'」，函数从未定义 → Install-JianManagerWorker 不识别。
// 真机 PS 7.6 复现：对象管道 THREW，.Content 管道/求值正常定义函数。
func TestBuildWindowsInstallCommand_PipesContentNotObject(t *testing.T) {
	cmd := buildWindowsInstallCommand("https://cp.example.com", "cp.example.com:9100", "jmet_test", "edge-1", "")
	if strings.Contains(cmd, ".ps1 -UseBasicParsing | iex") || strings.Contains(cmd, ".ps1 | iex") {
		t.Fatalf("FIX-1 回归：一键命令把 iwr 响应对象直接管道给 iex（串化损坏 UTF-8 正文致解析失败）: %q", cmd)
	}
	if !strings.Contains(cmd, ").Content") {
		t.Fatalf("FIX-1：一键命令应把脚本内容字符串喂给 iex（iex (iwr ...).Content）: %q", cmd)
	}
	if !strings.Contains(cmd, "Install-JianManagerWorker -ControlPlane ") {
		t.Fatalf("一键命令应在定义后调用 Install-JianManagerWorker: %q", cmd)
	}
}

// TestEnrollToken_Issue_EmitsDownloadURLWhenBinaryURLConfigured 配置 BinaryURL 时一键命令带显式覆盖源。
// 一键命令仍应保留 --download-url / -DownloadUrl 覆盖能力，便于私有源或内网源接管。
func TestEnrollToken_Issue_EmitsDownloadURLWhenBinaryURLConfigured(t *testing.T) {
	const releaseBase = "https://github.com/wcpe/jianmanager/releases/latest/download"
	db := setupTestDB(t)
	r := setupEnrollRouterWithInstall(t, db, EnrollInstallConfig{GRPCPort: 9100, BinaryURL: releaseBase})
	token := getAdminToken(t, r)

	w := makeRequest(r, "POST", "/api/v1/nodes/enroll-token", map[string]any{}, token)
	if w.Code != http.StatusCreated {
		t.Fatalf("签发失败: status=%d body=%s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)

	linux, _ := resp["installCommandLinux"].(string)
	if !strings.Contains(linux, "--download-url '"+releaseBase+"'") {
		t.Fatalf("Linux 一键命令应带 --download-url %s，得到 %q", releaseBase, linux)
	}
	win, _ := resp["installCommandWindows"].(string)
	if !strings.Contains(win, "-DownloadUrl '"+releaseBase+"'") {
		t.Fatalf("Windows 一键命令应带 -DownloadUrl %s，得到 %q", releaseBase, win)
	}
}

// TestEnrollToken_Issue_DefaultsToCPLocalWorkerAssetURL 覆盖 FR-190：未显式配置 BinaryURL 时，
// 添加节点的一键命令默认走 CP-local Worker 资产下载 URL，而不是公网 release 源。
func TestEnrollToken_Issue_DefaultsToCPLocalWorkerAssetURL(t *testing.T) {
	db := setupTestDB(t)
	selfUpdate := service.NewSelfUpdateService(db, cpgrpc.NewClientPool(), service.SelfUpdateConfig{}, newRouterTestRoot(t))
	r := setupEnrollRouterWithInstallAndSelfUpdate(t, db, EnrollInstallConfig{GRPCPort: 9100}, selfUpdate)
	token := getAdminToken(t, r)

	w := makeRequest(r, "POST", "/api/v1/nodes/enroll-token", map[string]any{}, token)
	if w.Code != http.StatusCreated {
		t.Fatalf("签发失败: status=%d body=%s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	scriptBase, _ := resp["scriptBaseUrl"].(string)

	linux, _ := resp["installCommandLinux"].(string)
	linuxURL := extractDownloadURL(t, linux, "--download-url ")
	wantTemplatePrefix := scriptBase + "/worker-assets/" + version.Version + "/{os}/{arch}/worker?token="
	if !strings.HasPrefix(linuxURL, wantTemplatePrefix) {
		t.Fatalf("Linux 一键命令应默认 CP-local 下载 URL，得到 %q", linux)
	}
	linuxToken := extractWorkerAssetToken(t, linuxURL)
	if _, err := selfUpdate.ValidateWorkerAssetToken(linuxToken, service.WorkerAssetTokenScope{
		Version: version.Version,
		OS:      "linux",
		Arch:    "amd64",
		Purpose: service.WorkerAssetPurposeInstall,
	}); err != nil {
		t.Fatalf("Linux 下载 token 范围不正确: %v", err)
	}

	win, _ := resp["installCommandWindows"].(string)
	winURL := extractDownloadURL(t, win, "-DownloadUrl ")
	if !strings.HasPrefix(winURL, wantTemplatePrefix) {
		t.Fatalf("Windows 一键命令应默认 CP-local 下载 URL，得到 %q", win)
	}
	winToken := extractWorkerAssetToken(t, winURL)
	if _, err := selfUpdate.ValidateWorkerAssetToken(winToken, service.WorkerAssetTokenScope{
		Version: version.Version,
		OS:      "windows",
		Arch:    "amd64",
		Purpose: service.WorkerAssetPurposeInstall,
	}); err != nil {
		t.Fatalf("Windows 下载 token 范围不正确: %v", err)
	}

	if strings.Contains(linux, "github.com/wcpe") || strings.Contains(win, "github.com/wcpe") {
		t.Fatalf("默认一键命令不应再指向公网 release 源: linux=%q windows=%q", linux, win)
	}
}

func extractDownloadURL(t *testing.T, command, marker string) string {
	t.Helper()
	idx := strings.Index(command, marker)
	if idx < 0 {
		t.Fatalf("命令缺下载参数 %q: %q", marker, command)
	}
	raw := command[idx+len(marker):]
	if strings.HasPrefix(raw, "'") {
		raw = strings.TrimPrefix(raw, "'")
		if end := strings.Index(raw, "'"); end >= 0 {
			return raw[:end]
		}
		t.Fatalf("下载 URL 缺少结束引号: %q", command)
	}
	if end := strings.Index(raw, " "); end >= 0 {
		raw = raw[:end]
	}
	return raw
}

func extractWorkerAssetToken(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("下载 URL 无法解析: %v", err)
	}
	token := u.Query().Get("token")
	if token == "" {
		t.Fatalf("下载 URL 缺 token query: %s", rawURL)
	}
	return token
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
