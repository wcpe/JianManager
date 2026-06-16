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
func (s *TerminalService) IssueToken(instanceID uint, permission string) (*TerminalToken, error) {
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

	// 签发 30s 有效期的 token
	now := time.Now()
	claims := jwt.MapClaims{
		"instanceId": instance.UUID,
		"permission": permission, // read 或 write
		"exp":        now.Add(30 * time.Second).Unix(),
		"iat":        now.Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString([]byte(s.jwtSecret))
	if err != nil {
		return nil, fmt.Errorf("签发终端 token 失败: %w", err)
	}

	// WS URL 指向 CP 代理端点（浏览器 → CP → Worker）
	// 不含 token 参数，由前端拼接
	wsURL := fmt.Sprintf("%s/ws/terminal", s.baseURL)

	return &TerminalToken{
		Token:     tokenStr,
		WSURL:     wsURL,
		ExpiresIn: 30,
	}, nil
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
