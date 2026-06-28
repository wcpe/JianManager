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
	// ErrPullKeyNotRevealable 拉取密钥不可查看（无 KeyEnc：存量老哈希密钥，或创建时未配加密密钥）。
	// 供 reveal 端点映射 404 业务码 KEY_NOT_REVEALABLE（FR-192，见 ADR-044）。
	ErrPullKeyNotRevealable = errors.New("拉取密钥不可查看")
	// ErrPullKeyInvalid 拉取密钥鉴权失败（不存在/吊销/过期/频道不匹配）。
	// 供 FR-087 面向玩家端点映射 401/403；不区分具体原因以免泄露密钥状态。
	ErrPullKeyInvalid = errors.New("拉取密钥无效")
)

// pullKeyPlaintextPrefix 拉取密钥明文前缀，便于识别凭据来源（同构常见 SaaS token 前缀惯例）。
const pullKeyPlaintextPrefix = "jmck_"

// pullKeyPrefixLen 落库 KeyPrefix 截取的明文前缀长度（含 `jmck_`），仅供列表识别、不足以重建密钥。
const pullKeyPrefixLen = 9

// ClientChannelService 客户端分发频道与拉取密钥管理（FR-086，见 ADR-022）。
// 密钥鉴权（VerifyKey）只用 SHA-256 哈希比对；另存 AES-256-GCM 可逆加密副本供管理员查看明文（FR-192）。
// 鉴权（VerifyKey）供 FR-087 面向玩家的 manifest/制品端点消费。
type ClientChannelService struct {
	db *gorm.DB
	// encryptor 拉取密钥可逆加密器（FR-192，见 ADR-044）。nil=未配置加密：建/轮换不写 KeyEnc、
	// 查看返 ErrPullKeyNotRevealable（生产未注入 JIANMANAGER_CLIENT_KEY_ENC_SECRET 时优雅降级）。
	encryptor *KeyEncryptor
}

// NewClientChannelService 创建客户端分发频道服务。
// 加密器经 SetKeyEncryptor 注入（保持构造签名稳定，既有装配/测试零改动）。
func NewClientChannelService(db *gorm.DB) *ClientChannelService {
	return &ClientChannelService{db: db}
}

// SetKeyEncryptor 注入拉取密钥可逆加密器（FR-192，见 ADR-044）。
// 传 nil 即「未配置加密」降级语义。装配处按生产/开发态用 ResolveKeyEncryptor 解析后注入。
func (s *ClientChannelService) SetKeyEncryptor(enc *KeyEncryptor) {
	s.encryptor = enc
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

// CreateKey 创建拉取密钥（FR-192：密钥发出后永久使用，可填自定义明文值）。
//   - customValue 非空：用作密钥明文（管理员自控这把永久 key）；
//   - customValue 为空：生成随机明文（带 jmck_ 前缀）。
//
// 落库写 KeyHash（SHA-256，鉴权）+ KeyEnc（AES-GCM 可逆加密副本，配了加密器时）；明文随响应一次性返回，
// 此后可经 reveal 端点查看（有 KeyEnc 时）。expiresAt 为可选过期时间（nil=永不过期）。
func (s *ClientChannelService) CreateKey(channelID, name, customValue string, expiresAt *time.Time) (*model.ClientPullKey, string, error) {
	if _, err := s.findChannel(channelID); err != nil {
		return nil, "", err
	}
	if name == "" {
		return nil, "", fmt.Errorf("%w: 密钥名不能为空", ErrInvalidChannelID)
	}

	plaintext, hash, prefix, err := derivePullKey(customValue)
	if err != nil {
		return nil, "", err
	}
	// 可逆加密副本（FR-192）：配了加密器才写，未配（nil）则 enc 为空串、密钥仍正常可用、只是不可查看。
	enc, err := s.encryptor.Encrypt(plaintext)
	if err != nil {
		return nil, "", fmt.Errorf("加密拉取密钥失败: %w", err)
	}
	key := &model.ClientPullKey{
		ChannelID: channelID,
		Name:      name,
		KeyHash:   hash,
		KeyEnc:    enc,
		KeyPrefix: prefix,
		ExpiresAt: expiresAt,
	}
	if err := s.db.Create(key).Error; err != nil {
		// key_hash 唯一索引冲突：自定义值与既有密钥撞值（极罕见，自定义值才会发生）。
		return nil, "", fmt.Errorf("创建拉取密钥失败: %w", err)
	}
	return key, plaintext, nil
}

// UpdateKeyParams 编辑拉取密钥参数（FR-192）。名称必改；Value 非空时改密钥明文值。
type UpdateKeyParams struct {
	// Name 新名称（必填，非空）。
	Name string
	// Value 新密钥明文值（可空=不改值，只改名）。非空则重算 KeyHash + 重写 KeyEnc。
	Value string
}

// UpdateKey 编辑拉取密钥（FR-192：管理员手动设/改这把永久 key 的值与名称）。
//   - 仅改名（Value 为空）：更新 Name；
//   - 改值（Value 非空）：用新明文重算 KeyHash（鉴权随之切换到新值）+ 重写 KeyEnc（可查看）+ 更新 KeyPrefix。
//
// 返回 (密钥记录, 本次设置的明文, error)；改值时明文随响应回显供复制，未改值则明文为空。
// 注意：改值会使持旧值的已分发客户端失效——前端须强警告。不触碰吊销态（编辑非吊销/恢复语义）。
func (s *ClientChannelService) UpdateKey(channelID string, keyID uint, p UpdateKeyParams) (*model.ClientPullKey, string, error) {
	key, err := s.findKey(channelID, keyID)
	if err != nil {
		return nil, "", err
	}
	if p.Name == "" {
		return nil, "", fmt.Errorf("%w: 密钥名不能为空", ErrInvalidChannelID)
	}
	updates := map[string]any{"name": p.Name}
	var plaintext string
	if p.Value != "" {
		hash, prefix := deriveHashPrefix(p.Value)
		enc, eerr := s.encryptor.Encrypt(p.Value)
		if eerr != nil {
			return nil, "", fmt.Errorf("加密拉取密钥失败: %w", eerr)
		}
		updates["key_hash"] = hash
		updates["key_enc"] = enc
		updates["key_prefix"] = prefix
		plaintext = p.Value
		key.KeyHash = hash
		key.KeyEnc = enc
		key.KeyPrefix = prefix
	}
	if err := s.db.Model(key).Updates(updates).Error; err != nil {
		return nil, "", fmt.Errorf("更新拉取密钥失败: %w", err)
	}
	key.Name = p.Name
	key.Revealable = key.KeyEnc != ""
	return key, plaintext, nil
}

// RevealKey 返回拉取密钥明文（FR-192，见 ADR-044）：解密 KeyEnc 还原明文，供平台管理员查看 + 复制。
// 无 KeyEnc（存量老哈希密钥 / 创建时未配加密密钥）→ ErrPullKeyNotRevealable（哈希单向，救不回）。
// 鉴权不变（仍用 KeyHash 比对）；本方法只读、不刷新 last_used_at（查看非鉴权命中）。
func (s *ClientChannelService) RevealKey(channelID string, keyID uint) (string, error) {
	key, err := s.findKey(channelID, keyID)
	if err != nil {
		return "", err
	}
	if key.KeyEnc == "" {
		return "", ErrPullKeyNotRevealable
	}
	plaintext, err := s.encryptor.Decrypt(key.KeyEnc)
	if err != nil {
		// 加密器未配置（降级）或密文解不开（如换过 env 密钥）：对管理员均呈现「不可查看」。
		return "", ErrPullKeyNotRevealable
	}
	return plaintext, nil
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

// VerifyAnyKey 校验请求头明文密钥但不绑定具体频道：对明文做 SHA-256 → 按哈希查找 →
// 校验未吊销、未过期。供 FR-087 的制品端点（GET /client-artifacts/{sha256}，路径无频道段）消费。
// 制品内容寻址、跨频道共享，任一有效密钥即可授权路由；内容可信靠 manifest 签名而非密钥（ADR-022 §2）。
// 命中刷新 last_used_at（弱一致）。未命中/吊销/过期统一返回 ErrPullKeyInvalid（不泄露具体原因）。
func (s *ClientChannelService) VerifyAnyKey(plaintext string) (*model.ClientPullKey, error) {
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
	if key.Revoked {
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
// 派生 Revealable（= KeyEnc 非空，FR-192）供前端判定可否查看；KeyEnc/KeyHash 本身不序列化。
func (s *ClientChannelService) listKeys(channelID string) ([]model.ClientPullKey, error) {
	var keys []model.ClientPullKey
	if err := s.db.Where("channel_id = ?", channelID).Order("created_at DESC").Find(&keys).Error; err != nil {
		return nil, fmt.Errorf("查询拉取密钥失败: %w", err)
	}
	for i := range keys {
		keys[i].Revealable = keys[i].KeyEnc != ""
	}
	return keys, nil
}

// derivePullKey 据可选自定义值派生拉取密钥（FR-192）：
//   - customValue 非空：直接用作明文（管理员自定义这把永久 key）；
//   - customValue 为空：32 字节随机 → base64url 明文（带 jmck_ 前缀）。
//
// 返回 (明文, SHA-256 哈希, 前缀)。
func derivePullKey(customValue string) (plaintext, hash, prefix string, err error) {
	if customValue != "" {
		h, p := deriveHashPrefix(customValue)
		return customValue, h, p, nil
	}
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", "", fmt.Errorf("生成拉取密钥失败: %w", err)
	}
	plaintext = pullKeyPlaintextPrefix + base64.RawURLEncoding.EncodeToString(b)
	h, p := deriveHashPrefix(plaintext)
	return plaintext, h, p, nil
}

// deriveHashPrefix 由明文算 (SHA-256 哈希, 落库前缀)。前缀仅供列表识别、不足以重建密钥。
func deriveHashPrefix(plaintext string) (hash, prefix string) {
	hash = sha256Hex(plaintext)
	prefix = plaintext
	if len(prefix) > pullKeyPrefixLen {
		prefix = prefix[:pullKeyPrefixLen]
	}
	return hash, prefix
}

// sha256Hex 返回字符串的 SHA-256 十六进制小写。
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
