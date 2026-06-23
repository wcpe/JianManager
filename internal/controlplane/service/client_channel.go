package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

var (
	// ErrInvalidChannelID 频道 slug 非法（不满足 `^[a-z0-9][a-z0-9-]{1,63}$`）。
	ErrInvalidChannelID = errors.New("非法的频道标识")
	// ErrChannelExists 频道 channelId 已存在。
	ErrChannelExists = errors.New("频道已存在")
	// ErrChannelNotFound 频道不存在。
	ErrChannelNotFound = errors.New("频道不存在")
	// ErrPullKeyNotFound 拉取密钥不存在（或不属于该频道）。
	ErrPullKeyNotFound = errors.New("拉取密钥不存在")
	// ErrPullKeyInvalid 拉取密钥鉴权失败（不存在/吊销/过期/频道不匹配）。
	// 供 FR-087 面向玩家端点映射 401/403；不区分具体原因以免泄露密钥状态。
	ErrPullKeyInvalid = errors.New("拉取密钥无效")
)

// pullKeyPlaintextPrefix 拉取密钥明文前缀，便于识别凭据来源（同构常见 SaaS token 前缀惯例）。
const pullKeyPlaintextPrefix = "jmck_"

// pullKeyPrefixLen 落库 KeyPrefix 截取的明文前缀长度（含 `jmck_`），仅供列表识别、不足以重建密钥。
const pullKeyPrefixLen = 9

// ClientChannelService 客户端分发频道与拉取密钥管理（FR-086，见 ADR-022）。
// 密钥落库只存 SHA-256 哈希，明文仅创建/轮换时一次性返回、不可二次读取。
// 鉴权（VerifyKey）供 FR-087 面向玩家的 manifest/制品端点消费。
type ClientChannelService struct {
	db *gorm.DB
}

// NewClientChannelService 创建客户端分发频道服务。
func NewClientChannelService(db *gorm.DB) *ClientChannelService {
	return &ClientChannelService{db: db}
}

// ChannelSummary 频道列表项（含密钥数量，便于管理台一览）。
type ChannelSummary struct {
	model.ClientChannel
	// KeyCount 频道下拉取密钥数量（含已吊销）。
	KeyCount int64 `json:"keyCount"`
}

// ChannelDetail 频道详情（含密钥元数据列表，无明文）。
type ChannelDetail struct {
	model.ClientChannel
	// Keys 密钥元数据列表（KeyHash 不序列化，无明文）。
	Keys []model.ClientPullKey `json:"keys"`
}

// ListChannels 列出全部分发频道（含密钥计数）。
func (s *ClientChannelService) ListChannels() ([]ChannelSummary, error) {
	var channels []model.ClientChannel
	if err := s.db.Order("created_at DESC").Find(&channels).Error; err != nil {
		return nil, fmt.Errorf("查询频道失败: %w", err)
	}
	out := make([]ChannelSummary, 0, len(channels))
	for _, ch := range channels {
		var cnt int64
		s.db.Model(&model.ClientPullKey{}).Where("channel_id = ?", ch.ChannelID).Count(&cnt)
		out = append(out, ChannelSummary{ClientChannel: ch, KeyCount: cnt})
	}
	return out, nil
}

// CreateChannel 创建分发频道（channelId 为 slug，全局唯一）。
func (s *ClientChannelService) CreateChannel(channelID, name, description string) (*model.ClientChannel, error) {
	if !model.ValidChannelID(channelID) {
		return nil, ErrInvalidChannelID
	}
	if name == "" {
		return nil, fmt.Errorf("%w: 名称不能为空", ErrInvalidChannelID)
	}

	var existing model.ClientChannel
	err := s.db.Where("channel_id = ?", channelID).First(&existing).Error
	if err == nil {
		return nil, ErrChannelExists
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("查询频道失败: %w", err)
	}

	ch := &model.ClientChannel{ChannelID: channelID, Name: name, Description: description}
	if err := s.db.Create(ch).Error; err != nil {
		return nil, fmt.Errorf("创建频道失败: %w", err)
	}
	return ch, nil
}

// GetChannel 取频道详情（含密钥元数据列表）。
func (s *ClientChannelService) GetChannel(channelID string) (*ChannelDetail, error) {
	ch, err := s.findChannel(channelID)
	if err != nil {
		return nil, err
	}
	keys, err := s.listKeys(channelID)
	if err != nil {
		return nil, err
	}
	return &ChannelDetail{ClientChannel: *ch, Keys: keys}, nil
}

// UpdateChannel 更新频道展示名/描述（channelId 不可改）。
func (s *ClientChannelService) UpdateChannel(channelID, name, description string) (*model.ClientChannel, error) {
	ch, err := s.findChannel(channelID)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, fmt.Errorf("%w: 名称不能为空", ErrInvalidChannelID)
	}
	ch.Name = name
	ch.Description = description
	if err := s.db.Model(ch).Updates(map[string]any{"name": name, "description": description}).Error; err != nil {
		return nil, fmt.Errorf("更新频道失败: %w", err)
	}
	return ch, nil
}

// DeleteChannel 删除频道及其全部拉取密钥（应用层级联：DB 无外键约束）。
func (s *ClientChannelService) DeleteChannel(channelID string) error {
	ch, err := s.findChannel(channelID)
	if err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		// 硬删密钥（凭据不保留软删，避免 key_hash 唯一索引与历史记录冲突）。
		if err := tx.Where("channel_id = ?", channelID).Delete(&model.ClientPullKey{}).Error; err != nil {
			return fmt.Errorf("删除频道密钥失败: %w", err)
		}
		if err := tx.Delete(ch).Error; err != nil {
			return fmt.Errorf("删除频道失败: %w", err)
		}
		return nil
	})
}

// ListKeys 列出频道下拉取密钥（仅元数据，无明文；频道不存在返回 ErrChannelNotFound）。
func (s *ClientChannelService) ListKeys(channelID string) ([]model.ClientPullKey, error) {
	if _, err := s.findChannel(channelID); err != nil {
		return nil, err
	}
	return s.listKeys(channelID)
}

// CreateKey 创建拉取密钥：生成随机明文 → 落库只存其 SHA-256 哈希 → 明文一次性返回。
// expiresAt 为可选过期时间（nil=永不过期）。返回 (密钥记录, 明文, error)；明文不可二次读取。
func (s *ClientChannelService) CreateKey(channelID, name string, expiresAt *time.Time) (*model.ClientPullKey, string, error) {
	if _, err := s.findChannel(channelID); err != nil {
		return nil, "", err
	}
	if name == "" {
		return nil, "", fmt.Errorf("%w: 密钥名不能为空", ErrInvalidChannelID)
	}

	plaintext, hash, prefix, err := generatePullKey()
	if err != nil {
		return nil, "", err
	}
	key := &model.ClientPullKey{
		ChannelID: channelID,
		Name:      name,
		KeyHash:   hash,
		KeyPrefix: prefix,
		ExpiresAt: expiresAt,
	}
	if err := s.db.Create(key).Error; err != nil {
		return nil, "", fmt.Errorf("创建拉取密钥失败: %w", err)
	}
	return key, plaintext, nil
}

// RotateKey 轮换密钥：生成新明文替换哈希（同一条记录），旧明文立即失效；新明文一次性返回。
// 轮换同时清除吊销态（轮换语义为「换一把新的接着用」）。
func (s *ClientChannelService) RotateKey(channelID string, keyID uint) (*model.ClientPullKey, string, error) {
	key, err := s.findKey(channelID, keyID)
	if err != nil {
		return nil, "", err
	}
	plaintext, hash, prefix, err := generatePullKey()
	if err != nil {
		return nil, "", err
	}
	updates := map[string]any{
		"key_hash":   hash,
		"key_prefix": prefix,
		"revoked":    false,
		"revoked_at": nil,
	}
	if err := s.db.Model(key).Updates(updates).Error; err != nil {
		return nil, "", fmt.Errorf("轮换拉取密钥失败: %w", err)
	}
	key.KeyHash = hash
	key.KeyPrefix = prefix
	key.Revoked = false
	key.RevokedAt = nil
	return key, plaintext, nil
}

// RevokeKey 吊销密钥（保留记录、标记 revoked + revokedAt，立即鉴权失效）。
func (s *ClientChannelService) RevokeKey(channelID string, keyID uint) error {
	key, err := s.findKey(channelID, keyID)
	if err != nil {
		return err
	}
	now := time.Now()
	if err := s.db.Model(key).Updates(map[string]any{"revoked": true, "revoked_at": &now}).Error; err != nil {
		return fmt.Errorf("吊销拉取密钥失败: %w", err)
	}
	return nil
}

// VerifyKey 校验请求头明文密钥：对明文做 SHA-256 → 按哈希查找 → 校验频道匹配、未吊销、未过期。
// 命中则刷新 last_used_at（弱一致，失败不阻断）。供 FR-087 面向玩家端点消费。
// 未命中/吊销/过期/频道不匹配统一返回 ErrPullKeyInvalid（不泄露具体原因）。
func (s *ClientChannelService) VerifyKey(channelID, plaintext string) (*model.ClientPullKey, error) {
	if plaintext == "" {
		return nil, ErrPullKeyInvalid
	}
	hash := sha256Hex(plaintext)

	var key model.ClientPullKey
	err := s.db.Where("key_hash = ?", hash).First(&key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrPullKeyInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("查询拉取密钥失败: %w", err)
	}
	if key.ChannelID != channelID || key.Revoked {
		return nil, ErrPullKeyInvalid
	}
	if key.ExpiresAt != nil && !time.Now().Before(*key.ExpiresAt) {
		return nil, ErrPullKeyInvalid
	}

	now := time.Now()
	s.db.Model(&key).Update("last_used_at", &now)
	key.LastUsedAt = &now
	return &key, nil
}

// findChannel 按 channelId 查频道，不存在返回 ErrChannelNotFound。
func (s *ClientChannelService) findChannel(channelID string) (*model.ClientChannel, error) {
	var ch model.ClientChannel
	err := s.db.Where("channel_id = ?", channelID).First(&ch).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrChannelNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询频道失败: %w", err)
	}
	return &ch, nil
}

// findKey 查指定频道下的密钥，校验归属；不存在返回 ErrPullKeyNotFound（频道不存在返回 ErrChannelNotFound）。
func (s *ClientChannelService) findKey(channelID string, keyID uint) (*model.ClientPullKey, error) {
	if _, err := s.findChannel(channelID); err != nil {
		return nil, err
	}
	var key model.ClientPullKey
	err := s.db.Where("id = ? AND channel_id = ?", keyID, channelID).First(&key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrPullKeyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询拉取密钥失败: %w", err)
	}
	return &key, nil
}

// listKeys 内部列密钥（不校验频道存在），按创建时间倒序。
func (s *ClientChannelService) listKeys(channelID string) ([]model.ClientPullKey, error) {
	var keys []model.ClientPullKey
	if err := s.db.Where("channel_id = ?", channelID).Order("created_at DESC").Find(&keys).Error; err != nil {
		return nil, fmt.Errorf("查询拉取密钥失败: %w", err)
	}
	return keys, nil
}

// generatePullKey 生成拉取密钥：32 字节随机 → base64url 明文（带 jmck_ 前缀），返回 (明文, SHA-256 哈希, 前缀)。
func generatePullKey() (plaintext, hash, prefix string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", "", fmt.Errorf("生成拉取密钥失败: %w", err)
	}
	plaintext = pullKeyPlaintextPrefix + base64.RawURLEncoding.EncodeToString(b)
	hash = sha256Hex(plaintext)
	prefix = plaintext
	if len(prefix) > pullKeyPrefixLen {
		prefix = prefix[:pullKeyPrefixLen]
	}
	return plaintext, hash, prefix, nil
}

// sha256Hex 返回字符串的 SHA-256 十六进制小写。
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
