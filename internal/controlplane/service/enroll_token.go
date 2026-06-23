package service

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

var (
	// ErrEnrollTokenNotFound enrollment token 不存在（列表/吊销路径用）。
	ErrEnrollTokenNotFound = errors.New("enrollment token 不存在")
	// ErrEnrollTokenInvalid enrollment token 校验失败（不存在/过期/已消费/已吊销）。
	// 注册路径统一返回此错误、不区分具体原因，避免泄露 token 状态。
	ErrEnrollTokenInvalid = errors.New("enrollment token 无效")
)

// enrollTokenPlaintextPrefix enrollment token 明文前缀，便于识别凭据来源（同构 FR-086 拉取密钥惯例）。
const enrollTokenPlaintextPrefix = "jmet_"

// enrollTokenPrefixLen 落库 TokenPrefix 截取的明文前缀长度（含 jmet_），仅供列表识别、不足以重建 token。
const enrollTokenPrefixLen = 9

// defaultEnrollTTLMinutes enrollment token 默认有效期（分钟）。
const defaultEnrollTTLMinutes = 30

// maxEnrollTTLMinutes enrollment token 有效期上限（分钟，1 天）。
const maxEnrollTTLMinutes = 1440

// EnrollTokenService 节点 enrollment token 管理（FR-080，见 ADR-020）。
//
// token 落库只存 SHA-256 哈希，明文仅签发时一次性返回、不可二次读取。
// 校验消费（ConsumeForNewNode）由 gRPC Register 在「新节点首次落库」时调用，
// 经条件 UPDATE 原子标记 Used 保证一次性（并发下仅一个调用成功）。
type EnrollTokenService struct {
	db *gorm.DB
}

// NewEnrollTokenService 创建 enrollment token 服务。
func NewEnrollTokenService(db *gorm.DB) *EnrollTokenService {
	return &EnrollTokenService{db: db}
}

// Issue 签发 enrollment token：生成随机明文 → 落库只存其 SHA-256 哈希 → 明文一次性返回。
// nodeName 为可选预设节点名；ttlMinutes<=0 取默认 30 分钟，超上限取上限。
// createdBy 为签发的平台管理员用户 ID（审计用）。返回 (记录, 明文, error)；明文不可二次读取。
func (s *EnrollTokenService) Issue(nodeName string, ttlMinutes int, createdBy uint) (*model.NodeEnrollToken, string, error) {
	if ttlMinutes <= 0 {
		ttlMinutes = defaultEnrollTTLMinutes
	}
	if ttlMinutes > maxEnrollTTLMinutes {
		ttlMinutes = maxEnrollTTLMinutes
	}

	plaintext, hash, prefix, err := generateEnrollToken()
	if err != nil {
		return nil, "", err
	}
	tok := &model.NodeEnrollToken{
		TokenHash:   hash,
		TokenPrefix: prefix,
		NodeName:    nodeName,
		ExpiresAt:   time.Now().Add(time.Duration(ttlMinutes) * time.Minute),
		CreatedBy:   createdBy,
	}
	if err := s.db.Create(tok).Error; err != nil {
		return nil, "", fmt.Errorf("签发 enrollment token 失败: %w", err)
	}
	return tok, plaintext, nil
}

// PeekForNewNode 仅校验 enrollment token 是否当前有效（存在 + 未过期 + 未消费 + 未吊销），不消费。
// 供 gRPC Register 在确认是「新节点」前先做合法性判断；真正消费在 ConsumeForNewNode（原子）。
// 校验失败统一返回 ErrEnrollTokenInvalid。
func (s *EnrollTokenService) PeekForNewNode(plaintext string) (*model.NodeEnrollToken, error) {
	if plaintext == "" {
		return nil, ErrEnrollTokenInvalid
	}
	hash := sha256Hex(plaintext)
	var tok model.NodeEnrollToken
	err := s.db.Where("token_hash = ?", hash).First(&tok).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrEnrollTokenInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("查询 enrollment token 失败: %w", err)
	}
	if tok.Used || tok.Revoked || !time.Now().Before(tok.ExpiresAt) {
		return nil, ErrEnrollTokenInvalid
	}
	return &tok, nil
}

// ConsumeForNewNode 校验并原子消费 enrollment token：经条件 UPDATE 仅当 (未消费 + 未吊销 + 未过期)
// 时把 used 置真并记 used_at/used_by_node。RowsAffected==1 才算消费成功（并发下仅一个调用成功，保证一次性）。
// 供 gRPC Register 在「新节点首次落库」前调用。校验/竞争失败统一返回 ErrEnrollTokenInvalid。
func (s *EnrollTokenService) ConsumeForNewNode(plaintext, nodeUUID string) error {
	if plaintext == "" {
		return ErrEnrollTokenInvalid
	}
	hash := sha256Hex(plaintext)
	now := time.Now()
	res := s.db.Model(&model.NodeEnrollToken{}).
		Where("token_hash = ? AND used = ? AND revoked = ? AND expires_at > ?", hash, false, false, now).
		Updates(map[string]any{
			"used":         true,
			"used_at":      &now,
			"used_by_node": nodeUUID,
		})
	if res.Error != nil {
		return fmt.Errorf("消费 enrollment token 失败: %w", res.Error)
	}
	if res.RowsAffected != 1 {
		return ErrEnrollTokenInvalid
	}
	return nil
}

// List 列出全部 enrollment token（仅元数据，无明文），按创建时间倒序。
func (s *EnrollTokenService) List() ([]model.NodeEnrollToken, error) {
	var tokens []model.NodeEnrollToken
	if err := s.db.Order("created_at DESC").Find(&tokens).Error; err != nil {
		return nil, fmt.Errorf("查询 enrollment token 失败: %w", err)
	}
	return tokens, nil
}

// Revoke 吊销 token（标记 revoked，立即校验失效）；不存在返回 ErrEnrollTokenNotFound。
// 已消费的 token 吊销为幂等无害操作（仍标记 revoked）。
func (s *EnrollTokenService) Revoke(id uint) error {
	var tok model.NodeEnrollToken
	err := s.db.First(&tok, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrEnrollTokenNotFound
	}
	if err != nil {
		return fmt.Errorf("查询 enrollment token 失败: %w", err)
	}
	if err := s.db.Model(&tok).Update("revoked", true).Error; err != nil {
		return fmt.Errorf("吊销 enrollment token 失败: %w", err)
	}
	return nil
}

// generateEnrollToken 生成 enrollment token：32 字节随机 → base64url 明文（带 jmet_ 前缀），
// 返回 (明文, SHA-256 哈希, 前缀)。
func generateEnrollToken() (plaintext, hash, prefix string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", "", fmt.Errorf("生成 enrollment token 失败: %w", err)
	}
	plaintext = enrollTokenPlaintextPrefix + base64.RawURLEncoding.EncodeToString(b)
	hash = sha256Hex(plaintext)
	prefix = plaintext
	if len(prefix) > enrollTokenPrefixLen {
		prefix = prefix[:enrollTokenPrefixLen]
	}
	return plaintext, hash, prefix, nil
}
