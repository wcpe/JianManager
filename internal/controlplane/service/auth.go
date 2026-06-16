package service

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/config"
	"github.com/wxys233/JianManager/internal/controlplane/model"
)

var (
	ErrUserExists         = errors.New("用户名已存在")
	ErrInvalidCreds       = errors.New("用户名或密码错误")
	ErrInvalidToken       = errors.New("无效的 token")
	ErrUserDisabled       = errors.New("用户已被禁用")
	ErrAdminAlreadyExists = errors.New("管理员已存在")
	ErrUserNotFound       = errors.New("用户不存在")
)

// AuthService 认证服务。
type AuthService struct {
	db  *gorm.DB
	cfg config.JWTConfig
}

// NewAuthService 创建认证服务。
func NewAuthService(db *gorm.DB, cfg config.JWTConfig) *AuthService {
	return &AuthService{db: db, cfg: cfg}
}

// TokenPair access + refresh token 对。
type TokenPair struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int    `json:"expiresIn"`
}

// Claims JWT 声明。
type Claims struct {
	UserID   uint           `json:"userId"`
	Username string         `json:"username"`
	Role     model.UserRole `json:"role"`
	jwt.RegisteredClaims
}

// Register 用户注册。
func (s *AuthService) Register(username, password string) (*model.User, error) {
	// 检查用户名是否已存在
	var count int64
	s.db.Model(&model.User{}).Where("username = ?", username).Count(&count)
	if count > 0 {
		return nil, ErrUserExists
	}

	// bcrypt 加密密码
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("加密密码失败: %w", err)
	}

	user := &model.User{
		Username: username,
		Password: string(hashed),
		Role:     model.RoleMember,
		Status:   model.UserStatusActive,
	}

	if err := s.db.Create(user).Error; err != nil {
		return nil, fmt.Errorf("创建用户失败: %w", err)
	}

	return user, nil
}

// Login 用户登录。
func (s *AuthService) Login(username, password string) (*TokenPair, error) {
	var user model.User
	if err := s.db.Where("username = ?", username).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidCreds
		}
		return nil, fmt.Errorf("查询用户失败: %w", err)
	}

	if user.Status == model.UserStatusDisabled {
		return nil, ErrUserDisabled
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
		return nil, ErrInvalidCreds
	}

	return s.generateTokenPair(&user)
}

// RefreshToken 刷新 access token。
func (s *AuthService) RefreshToken(refreshToken string) (*TokenPair, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(refreshToken, claims, func(t *jwt.Token) (interface{}, error) {
		return []byte(s.cfg.Secret), nil
	})
	if err != nil || !token.Valid {
		return nil, ErrInvalidToken
	}

	// 从数据库验证用户仍然存在且启用
	var user model.User
	if err := s.db.First(&user, claims.UserID).Error; err != nil {
		return nil, ErrInvalidToken
	}
	if user.Status == model.UserStatusDisabled {
		return nil, ErrUserDisabled
	}

	return s.generateTokenPair(&user)
}

// generateTokenPair 生成 access + refresh token 对。
func (s *AuthService) generateTokenPair(user *model.User) (*TokenPair, error) {
	now := time.Now()

	// Access Token
	accessClaims := &Claims{
		UserID:   user.ID,
		Username: user.Username,
		Role:     user.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(s.cfg.AccessTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
	}
	accessToken := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessStr, err := accessToken.SignedString([]byte(s.cfg.Secret))
	if err != nil {
		return nil, fmt.Errorf("签名 access token 失败: %w", err)
	}

	// Refresh Token
	refreshClaims := &Claims{
		UserID: user.ID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(s.cfg.RefreshTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
	}
	refreshToken := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims)
	refreshStr, err := refreshToken.SignedString([]byte(s.cfg.Secret))
	if err != nil {
		return nil, fmt.Errorf("签名 refresh token 失败: %w", err)
	}

	return &TokenPair{
		AccessToken:  accessStr,
		RefreshToken: refreshStr,
		ExpiresIn:    int(s.cfg.AccessTTL.Seconds()),
	}, nil
}

// SetupRequired 检查系统是否需要初始化（是否存在平台管理员）。
func (s *AuthService) SetupRequired() (bool, error) {
	var count int64
	if err := s.db.Model(&model.User{}).Where("role = ?", model.RolePlatformAdmin).Count(&count).Error; err != nil {
		return false, fmt.Errorf("查询管理员数量失败: %w", err)
	}
	return count == 0, nil
}

// SetupAdmin 创建初始管理员并返回 Token。仅当无管理员时可用。
func (s *AuthService) SetupAdmin(username, password string) (*TokenPair, error) {
	// 幂等检查：管理员已存在则拒绝
	var count int64
	if err := s.db.Model(&model.User{}).Where("role = ?", model.RolePlatformAdmin).Count(&count).Error; err != nil {
		return nil, fmt.Errorf("查询管理员数量失败: %w", err)
	}
	if count > 0 {
		return nil, ErrAdminAlreadyExists
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("加密密码失败: %w", err)
	}

	user := &model.User{
		Username: username,
		Password: string(hashed),
		Role:     model.RolePlatformAdmin,
		Status:   model.UserStatusActive,
	}

	if err := s.db.Create(user).Error; err != nil {
		return nil, fmt.Errorf("创建管理员失败: %w", err)
	}

	return s.generateTokenPair(user)
}
