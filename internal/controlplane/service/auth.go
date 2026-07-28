package service

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

var (
	ErrUserExists         = errors.New("用户名已存在")
	ErrInvalidCreds       = errors.New("用户名或密码错误")
	ErrInvalidToken       = errors.New("无效的 token")
	ErrUserDisabled       = errors.New("用户已被禁用")
	ErrAdminAlreadyExists = errors.New("管理员已存在")
	ErrUserNotFound       = errors.New("用户不存在")
	ErrUserInvalid        = errors.New("用户角色或状态非法")
)

const (
	// TokenTypeAccess 标识仅可用于访问受保护 API 的 token。
	TokenTypeAccess = "access"
	// TokenTypeRefresh 标识仅可用于刷新会话的 token。
	TokenTypeRefresh = "refresh"
	authSetupLockKey = "initial-admin"
)

// AuthService 认证服务。
type AuthService struct {
	db           *gorm.DB
	cfg          config.JWTConfig
	passwordCost int
}

// NewAuthService 创建认证服务。
func NewAuthService(db *gorm.DB, cfg config.JWTConfig) *AuthService {
	return &AuthService{db: db, cfg: cfg, passwordCost: bcrypt.DefaultCost}
}

// UserService 创建共享认证数据库的用户服务，供完整路由装配缺少显式用户服务时兜底。
func (s *AuthService) UserService() *UserService { return NewUserService(s.db) }

// SetPasswordCostForTest 设置测试用 bcrypt 成本，生产装配不得调用。
func (s *AuthService) SetPasswordCostForTest(cost int) {
	s.passwordCost = cost
}

// TokenPair access + refresh token 对。
type TokenPair struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int    `json:"expiresIn"`
}

// Claims JWT 声明。
type Claims struct {
	UserID      uint           `json:"userId"`
	Username    string         `json:"username"`
	Role        model.UserRole `json:"role"`
	TokenType   string         `json:"tokenType"`
	AuthVersion uint           `json:"authVersion"`
	jwt.RegisteredClaims
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
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || !token.Valid || claims.TokenType != TokenTypeRefresh {
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
	if user.AuthVersion != claims.AuthVersion {
		return nil, ErrInvalidToken
	}

	return s.generateTokenPair(&user)
}

// generateTokenPair 生成 access + refresh token 对。
func (s *AuthService) generateTokenPair(user *model.User) (*TokenPair, error) {
	now := time.Now()

	// Access Token
	accessClaims := &Claims{
		UserID:      user.ID,
		Username:    user.Username,
		Role:        user.Role,
		TokenType:   TokenTypeAccess,
		AuthVersion: user.AuthVersion,
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
		UserID:      user.ID,
		TokenType:   TokenTypeRefresh,
		AuthVersion: user.AuthVersion,
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
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), s.passwordCost)
	if err != nil {
		return nil, fmt.Errorf("加密密码失败: %w", err)
	}

	user := &model.User{
		Username: username,
		Password: string(hashed),
		Role:     model.RolePlatformAdmin,
		Status:   model.UserStatusActive,
	}

	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.User{}).Where("role = ?", model.RolePlatformAdmin).Count(&count).Error; err != nil {
			return fmt.Errorf("查询管理员数量失败: %w", err)
		}
		if count > 0 {
			return ErrAdminAlreadyExists
		}
		lockResult := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.AuthSetupLock{Key: authSetupLockKey})
		if lockResult.Error != nil {
			return fmt.Errorf("获取初始化锁失败: %w", lockResult.Error)
		}
		if lockResult.RowsAffected == 0 {
			return ErrAdminAlreadyExists
		}
		if err := tx.Create(user).Error; err != nil {
			return fmt.Errorf("创建管理员失败: %w", err)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	return s.generateTokenPair(user)
}
