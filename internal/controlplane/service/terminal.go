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
}

// NewTerminalService 创建终端服务。
func NewTerminalService(db *gorm.DB, jwtSecret string) *TerminalService {
	return &TerminalService{db: db, jwtSecret: jwtSecret}
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

	wsURL := fmt.Sprintf("ws://%s:%d/ws/terminal?token=%s", node.Host, node.WSPort, tokenStr)

	return &TerminalToken{
		Token:     tokenStr,
		WSURL:     wsURL,
		ExpiresIn: 30,
	}, nil
}
