package service

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

// TerminalService 终端连接服务。
type TerminalService struct {
	db        *gorm.DB
	jwtSecret string
	baseURL   string // CP 自身地址（用于代理 WS URL）
}

// NewTerminalService 创建终端服务。
// baseURL 为 CP 的外部可访问地址（如 http://localhost:8080），用于构造代理 WS URL。
func NewTerminalService(db *gorm.DB, jwtSecret, baseURL string) *TerminalService {
	return &TerminalService{db: db, jwtSecret: jwtSecret, baseURL: baseURL}
}

// TerminalToken 终端连接 token 响应。
type TerminalToken struct {
	Token     string `json:"token"`
	WSURL     string `json:"wsUrl"`
	ExpiresIn int    `json:"expiresIn"`
}

// IssueToken 签发一次性终端连接 token（30s 有效期）。
// requestHost 为浏览器请求的 Host 头，secure 表示访问是否经 TLS（HTTPS 直连或反代标注），
// 二者共同决定 WS URL 的 host 与 ws/wss scheme；requestHost 空值回退到 baseURL。
func (s *TerminalService) IssueToken(instanceID uint, permission, requestHost string, secure bool) (*TerminalToken, error) {
	// 验证实例存在
	var instance model.Instance
	if err := s.db.First(&instance, instanceID).Error; err != nil {
		return nil, ErrInstanceNotFound
	}

	// 获取节点信息
	var node model.Node
	if err := s.db.First(&node, instance.NodeID).Error; err != nil {
		return nil, ErrNodeNotFound
	}

	// 签发 10min 有效期的 token。
	// 仅在 WS 握手时校验一次，连上后长期有效；前端按会话缓存复用同一 token，
	// 故 TTL 须明显大于前端缓存窗口，否则重开/重连会用到过期 token 致握手失败。
	now := time.Now()
	const terminalTokenTTL = 10 * time.Minute
	claims := jwt.MapClaims{
		"instanceId": instance.UUID,
		"permission": permission, // read 或 write
		"exp":        now.Add(terminalTokenTTL).Unix(),
		"iat":        now.Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString([]byte(s.jwtSecret))
	if err != nil {
		return nil, fmt.Errorf("签发终端 token 失败: %w", err)
	}

	// WS URL 指向 CP 代理端点（浏览器 → CP → Worker），按浏览器访问的 Host 与协议构造，
	// 支持生产环境非 localhost 访问；scheme 跟随访问协议，避免 HTTPS 页面连 ws 被浏览器按混合内容拦截。
	wsURL := buildTerminalWSURL(s.baseURL, requestHost, secure)

	return &TerminalToken{
		Token:     tokenStr,
		WSURL:     wsURL,
		ExpiresIn: int(terminalTokenTTL.Seconds()),
	}, nil
}

// buildTerminalWSURL 构造终端代理 WS URL。
// requestHost 非空时按其访问协议选择 scheme（secure → wss，否则 ws），避免 HTTPS 页面连 ws 被混合内容策略拦截；
// requestHost 为空时回退到配置的 baseURL（已含 scheme，不再追加）。
func buildTerminalWSURL(baseURL, requestHost string, secure bool) string {
	if requestHost == "" {
		return fmt.Sprintf("%s/ws/terminal", baseURL)
	}
	scheme := "ws"
	if secure {
		scheme = "wss"
	}
	return fmt.Sprintf("%s://%s/ws/terminal", scheme, requestHost)
}

// GetWorkerAddr 返回实例所在 Worker 的 WS 地址（供代理使用）。
func (s *TerminalService) GetWorkerAddr(instanceUUID string) (string, error) {
	var instance model.Instance
	if err := s.db.Where("uuid = ?", instanceUUID).First(&instance).Error; err != nil {
		return "", ErrInstanceNotFound
	}

	var node model.Node
	if err := s.db.First(&node, instance.NodeID).Error; err != nil {
		return "", ErrNodeNotFound
	}

	return fmt.Sprintf("ws://%s:%d/ws/terminal", node.Host, node.WSPort), nil
}
