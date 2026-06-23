package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// EnrollInstallConfig 拼装一键安装命令所需的对外地址（FR-080，见 ADR-020）。
// 全部可选：留空字段在签发时按请求 Host 推断（适配同机/反代部署），显式配置可覆盖。
type EnrollInstallConfig struct {
	// AdvertiseGRPC 对外公布的 CP gRPC 地址（host:port）。留空则由请求 Host + GRPCPort 推断。
	AdvertiseGRPC string
	// GRPCPort CP gRPC 端口，用于在 AdvertiseGRPC 留空时与请求 Host 拼装。
	GRPCPort int
	// ScriptBaseURL 安装脚本下载基址（scheme://host）。留空则用请求 scheme://Host。
	ScriptBaseURL string
	// BinaryURL Worker 二进制下载地址（可选）。非空则并入一键命令的 --download-url。
	BinaryURL string
}

// EnrollTokenHandler 节点 enrollment token 路由（FR-080，见 ADR-020）。
// 限平台管理员（挂在 admin 组）；签发/吊销写审计，detail 绝不含 token 明文。
type EnrollTokenHandler struct {
	svc     *service.EnrollTokenService
	audit   *service.AuditService
	install EnrollInstallConfig
}

// NewEnrollTokenHandler 创建 enrollment token 路由处理器。
func NewEnrollTokenHandler(svc *service.EnrollTokenService, audit *service.AuditService, install EnrollInstallConfig) *EnrollTokenHandler {
	return &EnrollTokenHandler{svc: svc, audit: audit, install: install}
}

// issueEnrollTokenRequest 签发请求体（全部可选）。
type issueEnrollTokenRequest struct {
	// NodeName 预设节点名；留空则注册时以 Worker 上报名生效。
	NodeName string `json:"nodeName"`
	// TTLMinutes token 有效期（分钟），<=0 取默认 30，超上限取上限（service 内收敛）。
	TTLMinutes int `json:"ttlMinutes"`
}

// Issue POST /nodes/enroll-token — 签发一次性、限时的 enrollment token，返回明文 + 一键安装命令。
func (h *EnrollTokenHandler) Issue(c *gin.Context) {
	var req issueEnrollTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// 请求体可选：解析失败（如空 body）按全默认处理，不报错。
		req = issueEnrollTokenRequest{}
	}

	uid := h.currentUserID(c)
	tok, plaintext, err := h.svc.Issue(strings.TrimSpace(req.NodeName), req.TTLMinutes, uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "签发 enrollment token 失败"})
		return
	}

	grpcAddr := h.resolveGRPCAddr(c)
	scriptBase := h.resolveScriptBase(c)

	h.recordAudit(c, "node.enroll_token.create", map[string]any{
		"tokenId":     tok.ID,
		"tokenPrefix": tok.TokenPrefix,
		"nodeName":    tok.NodeName,
		"expiresAt":   tok.ExpiresAt,
	})

	c.JSON(http.StatusCreated, gin.H{
		"token":                 plaintext, // 明文，仅此次返回、不可二次读取
		"tokenId":               tok.ID,
		"tokenPrefix":           tok.TokenPrefix,
		"expiresAt":             tok.ExpiresAt,
		"nodeName":              tok.NodeName,
		"controlPlaneGrpc":      grpcAddr,
		"installCommandLinux":   buildLinuxInstallCommand(scriptBase, grpcAddr, plaintext, tok.NodeName, h.install.BinaryURL),
		"installCommandWindows": buildWindowsInstallCommand(scriptBase, grpcAddr, plaintext, tok.NodeName, h.install.BinaryURL),
	})
}

// List GET /nodes/enroll-tokens — 列出 token 元数据（无明文）。
func (h *EnrollTokenHandler) List(c *gin.Context) {
	tokens, err := h.svc.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "查询 enrollment token 失败"})
		return
	}
	c.JSON(http.StatusOK, tokens)
}

// Revoke DELETE /nodes/enroll-tokens/:id — 吊销未消费的 token。
func (h *EnrollTokenHandler) Revoke(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		return
	}
	if err := h.svc.Revoke(id); err != nil {
		if errors.Is(err, service.ErrEnrollTokenNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "ENROLL_TOKEN_NOT_FOUND", "message": "enrollment token 不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR", "message": "吊销 enrollment token 失败"})
		return
	}
	h.recordAudit(c, "node.enroll_token.revoke", map[string]any{"tokenId": id})
	c.JSON(http.StatusOK, gin.H{"message": "已吊销"})
}

// resolveGRPCAddr 解析对外公布的 CP gRPC 地址：优先显式配置，否则按请求 Host 主机名 + GRPCPort 推断。
func (h *EnrollTokenHandler) resolveGRPCAddr(c *gin.Context) string {
	if h.install.AdvertiseGRPC != "" {
		return h.install.AdvertiseGRPC
	}
	host := requestHostname(c)
	port := h.install.GRPCPort
	if port <= 0 {
		port = 9100
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// resolveScriptBase 解析安装脚本下载基址：优先显式配置，否则用请求的 scheme://Host。
func (h *EnrollTokenHandler) resolveScriptBase(c *gin.Context) string {
	if h.install.ScriptBaseURL != "" {
		return strings.TrimRight(h.install.ScriptBaseURL, "/")
	}
	scheme := "http"
	if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s", scheme, c.Request.Host)
}

// currentUserID 取当前登录用户 ID（审计用）；缺失返回 0。
func (h *EnrollTokenHandler) currentUserID(c *gin.Context) uint {
	v, _ := c.Get(middleware.CtxUserID)
	id, _ := v.(uint)
	return id
}

// recordAudit 记录 enrollment token 破坏性操作审计（detail 仅含可公开元数据，绝不含明文）。
func (h *EnrollTokenHandler) recordAudit(c *gin.Context, action string, detail map[string]any) {
	if h.audit == nil {
		return
	}
	raw, _ := json.Marshal(detail)
	_ = h.audit.Record(h.currentUserID(c), action, "node_enroll_token", "", string(raw), c.ClientIP())
}

// RegisterRoutes 注册 enrollment token 路由（挂在平台管理员组下，无需再判 IsPlatformAdmin）。
func (h *EnrollTokenHandler) RegisterRoutes(rg *gin.RouterGroup) {
	nodes := rg.Group("/nodes")
	{
		nodes.POST("/enroll-token", h.Issue)
		nodes.GET("/enroll-tokens", h.List)
		nodes.DELETE("/enroll-tokens/:id", h.Revoke)
	}
}

// requestHostname 取请求 Host 的主机名（剥离端口）。
func requestHostname(c *gin.Context) string {
	host := c.Request.Host
	if i := strings.LastIndex(host, ":"); i > 0 {
		// 仅在确为 host:port（非 IPv6 字面量裸写）时剥离端口。
		if !strings.Contains(host[:i], "]") && !strings.Contains(host[:i], ":") {
			host = host[:i]
		}
	}
	if host == "" {
		host = "localhost"
	}
	return host
}

// buildLinuxInstallCommand 拼 Linux/macOS 一键安装命令（curl 拉脚本 | sh）。
func buildLinuxInstallCommand(scriptBase, grpcAddr, token, nodeName, binaryURL string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "curl -fsSL %s/install-worker.sh | sh -s -- --control-plane %s --token %s", scriptBase, grpcAddr, token)
	if nodeName != "" {
		fmt.Fprintf(&b, " --name %s", nodeName)
	}
	if binaryURL != "" {
		fmt.Fprintf(&b, " --download-url %s", binaryURL)
	}
	return b.String()
}

// buildWindowsInstallCommand 拼 Windows 一键安装命令（iwr 拉脚本 | iex 后调函数）。
func buildWindowsInstallCommand(scriptBase, grpcAddr, token, nodeName, binaryURL string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "iwr %s/install-worker.ps1 -UseBasicParsing | iex; Install-JianManagerWorker -ControlPlane %s -Token %s", scriptBase, grpcAddr, token)
	if nodeName != "" {
		fmt.Fprintf(&b, " -Name %s", nodeName)
	}
	if binaryURL != "" {
		fmt.Fprintf(&b, " -DownloadUrl %s", binaryURL)
	}
	return b.String()
}
