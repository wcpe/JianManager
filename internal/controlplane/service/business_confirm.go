package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// 业务批量二次确认令牌（FR-535 §4）。
//
// 【为什么要绑快照 hash 而不是「用户点过一次确认」的布尔标记】后者是会话状态，容易被绕过；
// 只有把「确认时看到的实例集合」签进令牌，才能在执行时判断目标是否漂移（§4.2）。
//
// 令牌是服务端签名的 opaque 字符串，用「payload.signature」两段式便于无状态校验：
// 任何 CP 实例拿到同一密钥都能独立验签，不需要共享内存状态。
//
// 【正文里还有目标选择条件（Target）】这不是冗余——契约的 BatchExecuteRequest **不携带
// target**（§7.2 的请求体只有 token + domain + action + payload + requestId + reason），
// 执行端必须能从令牌里还原出「当初是怎么选的目标」才能重新解析并比对 hash。
// 令牌是签名的，正文不可篡改，因此可以把它当作可信输入使用。

const (
	// businessConfirmTokenTTL 令牌有效期（规格默认 5 分钟）。
	businessConfirmTokenTTL = 5 * time.Minute
	// businessConfirmTokenDomain 令牌密钥的域分离标签（避免与其他 HMAC 用途共用密钥）。
	businessConfirmTokenDomain = "jianmanager/business/batch-confirm-token/v1"
	// businessConfirmTokenPrefix 令牌可见前缀，便于日志与人工辨识（非安全边界）。
	businessConfirmTokenPrefix = "bpt_"
	// businessConfirmTokenVersion 令牌格式版本，为将来演进留位。
	businessConfirmTokenVersion = 1
)

// 令牌校验失败的稳定错误（对应 §8 错误码表）。
var (
	// ErrBusinessTokenExpired 令牌已过期（409 TOKEN_EXPIRED）。
	ErrBusinessTokenExpired = errors.New("确认令牌已过期，请重新预览")
	// ErrBusinessTokenMismatch 令牌不属于当前调用者（403 TOKEN_MISMATCH）。
	ErrBusinessTokenMismatch = errors.New("确认令牌不属于当前调用者")
	// ErrBusinessTokenInvalid 令牌格式/签名无效（按 409 处理：不可采信，要求重新预览）。
	ErrBusinessTokenInvalid = errors.New("确认令牌无效，请重新预览")
	// ErrBusinessTargetChanged 目标集合与令牌绑定不一致（409 TARGET_CHANGED）。
	ErrBusinessTargetChanged = errors.New("目标集合已变化，请重新预览")
	// ErrBusinessConfirmationRequired 高风险但未提供令牌（409 CONFIRMATION_REQUIRED）。
	ErrBusinessConfirmationRequired = errors.New("高风险批量操作需要二次确认")
	// ErrBusinessReasonRequired critical 动作未填 reason（400 REASON_REQUIRED）。
	ErrBusinessReasonRequired = errors.New("DESTRUCTIVE 动作必须填写操作原因")
	// ErrBusinessTargetEmpty 解析后无实例（400 TARGET_EMPTY）。
	ErrBusinessTargetEmpty = errors.New("目标解析后没有可执行的实例")
	// ErrBusinessIdempotencyKeyReused 同 requestId 但 payload 不同（409 IDEMPOTENCY_KEY_REUSED）。
	ErrBusinessIdempotencyKeyReused = errors.New("requestId 已用于另一组不同的请求参数")
)

// DeriveBusinessConfirmTokenSecret 从服务端主密钥域分离派生确认令牌专用密钥。
//
// 域分离的意义：同一把主密钥派生出的不同用途子密钥互不可替代——即使某处泄漏了一个
// 用途的密钥，也不能据此伪造其他用途的签名。
func DeriveBusinessConfirmTokenSecret(master []byte) []byte {
	if len(master) == 0 {
		return nil
	}
	mac := hmac.New(sha256.New, master)
	_, _ = mac.Write([]byte(businessConfirmTokenDomain))
	return mac.Sum(nil)
}

// BusinessConfirmClaims 是令牌正文（签名保护，不加密）。
//
// 正文里**不含 payload 业务内容**：令牌只回答「谁、在什么目标集合、什么风险等级、什么时候」，
// 不携带任何业务数据（ADR-106：JM 不持有业务语义）。
type BusinessConfirmClaims struct {
	Version int `json:"v"`
	// TokenID 令牌子标识，便于审计串联同一次确认的签发与使用。
	TokenID string `json:"jti"`
	// IssuerKey 签发者身份键（user:<id> / agent:<id>），用于拦截跨账号使用。
	IssuerKey string `json:"iss"`
	// Target 签发时的目标选择条件（执行端据此重新解析）。
	Target BusinessTarget `json:"tgt"`
	// InstancesHash 当时解析出的实例集合稳定指纹。
	InstancesHash string `json:"h"`
	// RiskLevel 签发时的风险等级，防止「预览时 high，执行时降级绕过」。
	RiskLevel string `json:"r"`
	// IssuedAtUnix / ExpiresAtUnix 签发与过期时间（秒级：避免平台间时钟精度差异）。
	IssuedAtUnix  int64 `json:"iat"`
	ExpiresAtUnix int64 `json:"exp"`
}

// ExpiresAt 返回过期时刻。
func (c *BusinessConfirmClaims) ExpiresAt() time.Time {
	if c == nil {
		return time.Time{}
	}
	return time.Unix(c.ExpiresAtUnix, 0).UTC()
}

// BusinessConfirmTokenSigner 签发与校验确认令牌。
type BusinessConfirmTokenSigner struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// NewBusinessConfirmTokenSigner 创建令牌签名器。secret 为空返回 error（绝不允许无密钥签发）。
func NewBusinessConfirmTokenSigner(secret []byte) (*BusinessConfirmTokenSigner, error) {
	if len(secret) == 0 {
		return nil, fmt.Errorf("确认令牌签名密钥不能为空")
	}
	return &BusinessConfirmTokenSigner{
		secret: append([]byte(nil), secret...),
		ttl:    businessConfirmTokenTTL,
		now:    time.Now,
	}, nil
}

// SetClock 覆盖时钟（测试用）。
func (s *BusinessConfirmTokenSigner) SetClock(now func() time.Time) {
	if s == nil || now == nil {
		return
	}
	s.now = now
}

// SetTTL 覆盖有效期（测试用）。
func (s *BusinessConfirmTokenSigner) SetTTL(ttl time.Duration) {
	if s == nil || ttl <= 0 {
		return
	}
	s.ttl = ttl
}

// TTL 返回当前有效期。
func (s *BusinessConfirmTokenSigner) TTL() time.Duration {
	if s == nil {
		return businessConfirmTokenTTL
	}
	return s.ttl
}

// Issue 签发确认令牌，返回令牌串与过期时间。
//
// instancesHash 为空或 riskLevel 为空视为调用方错误（签一个不绑任何东西的令牌毫无意义）。
func (s *BusinessConfirmTokenSigner) Issue(issuerKey string, target BusinessTarget, instancesHash, riskLevel string) (string, time.Time, error) {
	if s == nil {
		return "", time.Time{}, fmt.Errorf("确认令牌签名器未初始化")
	}
	if instancesHash == "" {
		return "", time.Time{}, fmt.Errorf("签发确认令牌需要实例集指纹")
	}
	if riskLevel == "" {
		return "", time.Time{}, fmt.Errorf("签发确认令牌需要风险等级")
	}
	now := s.now().UTC()
	expiresAt := now.Add(s.ttl)
	claims := BusinessConfirmClaims{
		Version:       businessConfirmTokenVersion,
		TokenID:       uuid.NewString(),
		IssuerKey:     issuerKey,
		Target:        target,
		InstancesHash: instancesHash,
		RiskLevel:     riskLevel,
		IssuedAtUnix:  now.Unix(),
		ExpiresAtUnix: expiresAt.Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("序列化确认令牌失败: %w", err)
	}
	token := businessConfirmTokenPrefix +
		base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(s.sign(payload))
	return token, expiresAt, nil
}

// BusinessConfirmExpectation 是校验时的期望值（由调用方在**重新解析目标后**构造）。
type BusinessConfirmExpectation struct {
	// IssuerKey 当前调用者身份键；为空表示不校验归属（内部调用场景）。
	IssuerKey string
	// InstancesHash 本次重新解析得到的实例集指纹；为空表示本次不做目标漂移检查。
	InstancesHash string
}

// Verify 校验令牌并返回其正文。
//
// 校验顺序刻意固定：格式/签名 → 归属 → 过期 → 目标漂移。
// 先把「这不是我们的令牌」「这不是你的令牌」判掉，再谈时效与目标，
// 这样错误码语义才准确（过期令牌不该因为目标恰好也变了而被报成 TARGET_CHANGED）。
func (s *BusinessConfirmTokenSigner) Verify(token string, expect BusinessConfirmExpectation) (*BusinessConfirmClaims, error) {
	if s == nil {
		return nil, ErrBusinessTokenInvalid
	}
	claims, err := s.parseAndAuthenticate(token)
	if err != nil {
		return nil, err
	}
	if expect.IssuerKey != "" && claims.IssuerKey != expect.IssuerKey {
		return nil, ErrBusinessTokenMismatch
	}
	if !s.now().UTC().Before(time.Unix(claims.ExpiresAtUnix, 0)) {
		return nil, ErrBusinessTokenExpired
	}
	if expect.InstancesHash != "" && !hmac.Equal([]byte(claims.InstancesHash), []byte(expect.InstancesHash)) {
		return nil, ErrBusinessTargetChanged
	}
	return claims, nil
}

// parseAndAuthenticate 拆包、验签、解正文。
//
// 所有格式类失败都归为 ErrBusinessTokenInvalid：对调用方而言「这个令牌不可采信」的
// 处置动作与「过期」完全一致——重新预览。
func (s *BusinessConfirmTokenSigner) parseAndAuthenticate(token string) (*BusinessConfirmClaims, error) {
	if len(token) <= len(businessConfirmTokenPrefix) {
		return nil, ErrBusinessTokenInvalid
	}
	body := token[len(businessConfirmTokenPrefix):]
	sep := -1
	for i := 0; i < len(body); i++ {
		if body[i] == '.' {
			sep = i
			break
		}
	}
	if sep <= 0 || sep == len(body)-1 {
		return nil, ErrBusinessTokenInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(body[:sep])
	if err != nil {
		return nil, ErrBusinessTokenInvalid
	}
	signature, err := base64.RawURLEncoding.DecodeString(body[sep+1:])
	if err != nil || !hmac.Equal(signature, s.sign(payload)) {
		return nil, ErrBusinessTokenInvalid
	}
	var claims BusinessConfirmClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, ErrBusinessTokenInvalid
	}
	if claims.Version != businessConfirmTokenVersion {
		return nil, ErrBusinessTokenInvalid
	}
	return &claims, nil
}

// sign 计算 HMAC-SHA256 签名。
func (s *BusinessConfirmTokenSigner) sign(payload []byte) []byte {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}
