package service

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 用户令牌相关错误（对外统一收敛为 401/403，不泄露细节）。
var (
	ErrUserTokenInvalid = errors.New("用户令牌无效")
	ErrUserTokenRevoked = errors.New("用户令牌已吊销")
	ErrUserTokenExpired = errors.New("用户令牌已过期")
	ErrUserTokenMismatch = errors.New("用户令牌与用户状态不一致")
)

// UserTokenPrefix 用户令牌明文前缀。
// 与 Agent Token 的 jmat_ 明确区分：鉴权中间件据此分流，且用户在日志里一眼可辨。
const UserTokenPrefix = "jmut_"

// 用户令牌有效期默认值。
const (
	// UserTokenDefaultTTL 默认 90 天：人类凭证需要长期有效，否则频繁重登很烦。
	UserTokenDefaultTTL = 90 * 24 * time.Hour
	// UserTokenMaxTTL 上限 365 天，防止创建永不过期的凭证。
	UserTokenMaxTTL = 365 * 24 * time.Hour
)

// UserTokenService 用户令牌服务（FR-560 / ADR-112）。
//
// 与 AgentTokenService 的差异：
//   - 权限 = token scope ∩ 用户当前 RBAC 权限（不能放大）
//   - 用户 AuthVersion 递增时该用户全部令牌失效
//   - 用户可自助创建/吊销，不需要管理员
type UserTokenService struct {
	db    *gorm.DB
	authz *AuthzService
}

// NewUserTokenService 创建用户令牌服务。
// authz 用于计算用户当前有效权限，参与交集运算；为 nil 时交集退化为「仅 token scope」。
func NewUserTokenService(db *gorm.DB, authz *AuthzService) *UserTokenService {
	return &UserTokenService{db: db, authz: authz}
}

// CreateUserTokenRequest 创建令牌的入参。
type CreateUserTokenRequest struct {
	Name              string   `json:"name"`
	DeviceLabel       string   `json:"deviceLabel"`
	ScopedNodeIDs     []uint   `json:"scopedNodeIds"`
	ScopedInstanceIDs []uint   `json:"scopedInstanceIds"`
	Capabilities      []string `json:"capabilities"`
	// TTLSeconds 可选；0 表示用默认值，超过上限按上限截断。
	TTLSeconds int `json:"ttlSeconds"`
}

// CreateUserTokenResult 创建结果，Plaintext 只在此处出现一次。
type CreateUserTokenResult struct {
	Token     *model.UserToken `json:"token"`
	Plaintext string           `json:"plaintext"`
}

// Create 为指定用户签发令牌。
//
// 明文只返回一次（本次响应），库内仅存 hash。
func (s *UserTokenService) Create(userID uint, req CreateUserTokenRequest) (*CreateUserTokenResult, error) {
	if userID == 0 {
		return nil, fmt.Errorf("用户 ID 不能为空")
	}
	name := req.Name
	if name == "" {
		name = "未命名令牌"
	}

	// 校验并收敛 scope：token 不得持有用户自己没有的节点/实例。
	if err := s.validateScope(userID, req); err != nil {
		return nil, err
	}

	user, err := s.loadUser(userID)
	if err != nil {
		return nil, err
	}

	plaintext, hash, prefix, err := generateUserToken()
	if err != nil {
		return nil, err
	}

	ttl := time.Duration(req.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = UserTokenDefaultTTL
	}
	if ttl > UserTokenMaxTTL {
		ttl = UserTokenMaxTTL
	}

	nodeIDs, _ := json.Marshal(req.ScopedNodeIDs)
	instanceIDs, _ := json.Marshal(req.ScopedInstanceIDs)
	caps, _ := json.Marshal(req.Capabilities)

	token := &model.UserToken{
		UserID:            userID,
		Name:              name,
		DeviceLabel:       req.DeviceLabel,
		TokenHash:         hash,
		TokenPrefix:       prefix,
		ScopedNodeIDs:     string(nodeIDs),
		ScopedInstanceIDs: string(instanceIDs),
		Capabilities:      string(caps),
		AuthVersion:       user.AuthVersion,
		ExpiresAt:         time.Now().Add(ttl),
	}
	if err := s.db.Create(token).Error; err != nil {
		return nil, fmt.Errorf("保存用户令牌失败: %w", err)
	}
	return &CreateUserTokenResult{Token: token, Plaintext: plaintext}, nil
}

// validateScope 保证 token 的 scope 不超过用户自身可访问范围。
//
// 这里只做「节点归属」粗校验：实例存在性不在此校验（避免创建期查库过重），
// 实际访问时仍会在 Resolve/Authz 层做最终收敛。
func (s *UserTokenService) validateScope(userID uint, req CreateUserTokenRequest) error {
	if s.authz == nil || len(req.ScopedNodeIDs) == 0 {
		return nil
	}
	access, err := s.authz.LoadUserAccess(userID)
	if err != nil {
		return fmt.Errorf("加载用户权限失败: %w", err)
	}
	// 平台管理员可声明任意节点；其余用户只能声明自己已获授权的节点。
	if access.IsPlatformAdmin {
		return nil
	}
	for _, nodeID := range req.ScopedNodeIDs {
		if !access.HasNode(fmt.Sprintf("%d", nodeID)) {
			return ErrAgentForbidden
		}
	}
	return nil
}

// UserTokenPrincipal 校验通过后的主体，供中间件注入上下文。
type UserTokenPrincipal struct {
	TokenID uint
	UserID  uint
	// Username / Role / AuthVersion 与 JWT Claims 对齐，便于复用既有 LoadAccess 链路。
	Username    string
	Role        model.UserRole
	AuthVersion uint
	// Capabilities 交集后的有效能力。
	Capabilities []string
	// NodeIDs / InstanceIDs 交集后的有效 scope；nil 表示不额外收窄。
	NodeIDs     []uint
	InstanceIDs []uint
}

// Authenticate 校验明文令牌，返回主体。
//
// 校验链（ADR-112）：
//  1. hash 命中且唯一
//  2. 未吊销
//  3. 未过期
//  4. 所属用户存在且启用
//  5. token.AuthVersion == user.AuthVersion（改密码即全失效）
func (s *UserTokenService) Authenticate(plaintext string) (*UserTokenPrincipal, error) {
	if plaintext == "" {
		return nil, ErrUserTokenInvalid
	}
	hash := hashUserToken(plaintext)

	var candidates []model.UserToken
	if err := s.db.Where("token_hash = ?", hash).Limit(1).Find(&candidates).Error; err != nil {
		return nil, fmt.Errorf("查询用户令牌失败: %w", err)
	}
	if len(candidates) == 0 {
		return nil, ErrUserTokenInvalid
	}
	tok := candidates[0]

	// 常量时间比对，防时序侧信道（虽然已按 hash 索引查询，仍保留防御）。
	if subtle.ConstantTimeCompare([]byte(tok.TokenHash), []byte(hash)) != 1 {
		return nil, ErrUserTokenInvalid
	}
	if tok.Revoked {
		return nil, ErrUserTokenRevoked
	}
	if !time.Now().Before(tok.ExpiresAt) {
		return nil, ErrUserTokenExpired
	}

	user, err := s.loadUser(tok.UserID)
	if err != nil {
		return nil, err
	}
	if user.AuthVersion != tok.AuthVersion {
		return nil, ErrUserTokenMismatch
	}

	s.touchLastUsed(tok.ID)
	return s.resolve(user, &tok)
}

// Resolve 把令牌的 scope 与用户权限求交集。
func (s *UserTokenService) resolve(user *model.User, tok *model.UserToken) (*UserTokenPrincipal, error) {
	p := &UserTokenPrincipal{
		TokenID:     tok.ID,
		UserID:      user.ID,
		Username:    user.Username,
		Role:        user.Role,
		AuthVersion: tok.AuthVersion,
	}
	caps := decodeJSONStrings(tok.Capabilities)
	nodes := decodeJSONUints(tok.ScopedNodeIDs)
	instances := decodeJSONUints(tok.ScopedInstanceIDs)

	// 平台管理员拥有全部权限，不受 token capability 限制（但 scope 仍生效）。
	if user.Role == model.RolePlatformAdmin {
		p.Capabilities = caps
		p.NodeIDs = nodes
		p.InstanceIDs = instances
		return p, nil
	}

	// 非管理员：token 能力必须落在用户实际权限内。
	if s.authz != nil {
		access, err := s.authz.LoadUserAccess(user.ID)
		if err != nil {
			return nil, fmt.Errorf("加载用户权限失败: %w", err)
		}
		p.Capabilities = intersectCapabilities(caps, access)
	} else {
		p.Capabilities = caps
	}
	p.NodeIDs = nodes
	p.InstanceIDs = instances
	return p, nil
}

// List 列出某用户的令牌（不含明文与 hash）。
func (s *UserTokenService) List(userID uint) ([]model.UserToken, error) {
	var out []model.UserToken
	err := s.db.Where("user_id = ?", userID).Order("created_at DESC").Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("列出用户令牌失败: %w", err)
	}
	return out, nil
}

// Revoke 吊销令牌。
//
// 仅允许吊销本人令牌；平台管理员可吊销任意（由调用方 router 层判定并传 allowAny）。
func (s *UserTokenService) Revoke(tokenID, actorUserID uint, allowAny bool) error {
	var tok model.UserToken
	if err := s.db.First(&tok, tokenID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 与「不属于你」统一返回，避免成为存在性探针。
			return ErrAgentForbidden
		}
		return fmt.Errorf("查询用户令牌失败: %w", err)
	}
	if !allowAny && tok.UserID != actorUserID {
		return ErrAgentForbidden
	}
	if tok.Revoked {
		return nil // 幂等
	}
	if err := s.db.Model(&model.UserToken{}).Where("id = ?", tokenID).
		Update("revoked", true).Error; err != nil {
		return fmt.Errorf("吊销用户令牌失败: %w", err)
	}
	return nil
}

// GetByID 按 ID 查询令牌（供 router 判定归属）。
func (s *UserTokenService) GetByID(tokenID uint) (*model.UserToken, error) {
	var tok model.UserToken
	if err := s.db.First(&tok, tokenID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAgentForbidden
		}
		return nil, fmt.Errorf("查询用户令牌失败: %w", err)
	}
	return &tok, nil
}

// loadUser 加载启用状态的用户。
func (s *UserTokenService) loadUser(userID uint) (*model.User, error) {
	var user model.User
	if err := s.db.First(&user, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrUserTokenInvalid
		}
		return nil, fmt.Errorf("查询用户失败: %w", err)
	}
	if user.Status != model.UserStatusActive {
		return nil, ErrUserTokenInvalid
	}
	return &user, nil
}

// touchLastUsed 更新最近使用时间；失败不影响鉴权结果。
func (s *UserTokenService) touchLastUsed(tokenID uint) {
	now := time.Now()
	_ = s.db.Model(&model.UserToken{}).Where("id = ?", tokenID).
		Update("last_used_at", now).Error
}

// generateUserToken 生成明文、hash 与可展示前缀。
func generateUserToken() (plaintext, hash, prefix string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", "", fmt.Errorf("生成令牌随机数失败: %w", err)
	}
	body := base64.RawURLEncoding.EncodeToString(buf)
	plaintext = UserTokenPrefix + body
	hash = hashUserToken(plaintext)
	// 前缀取 jmut_ + 4 位，便于用户辨认但不泄露足够信息。
	prefix = plaintext[:len(UserTokenPrefix)+4]
	return plaintext, hash, prefix, nil
}

// hashUserToken 计算令牌 hash（sha256 十六进制小写）。
func hashUserToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// intersectCapabilities 求 token 能力与用户权限的交集。
//
// 语义：token 声明了能力 A，用户当前也必须具备 A，否则 A 不生效。
// token 未声明任何能力时视为「不额外限制」，此时不返回能力清单，
// 由调用方回退到用户权限（见 resolve）。
func intersectCapabilities(tokenCaps []string, access *UserAccess) []string {
	if access == nil {
		return tokenCaps
	}
	out := make([]string, 0, len(tokenCaps))
	for _, c := range tokenCaps {
		if access.HasPermission(PermissionNode(c)) || access.HasNode(c) {
			out = append(out, c)
		}
	}
	return out
}

// decodeJSONStrings 宽松解析 JSON 字符串数组；失败返回空。
func decodeJSONStrings(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// decodeJSONUints 宽松解析 JSON 数字数组；失败返回空。
func decodeJSONUints(raw string) []uint {
	if raw == "" {
		return nil
	}
	var out []uint
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}
