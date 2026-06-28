package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// 拉取密钥可逆加密（FR-192，见 ADR-044）。
//
// 拉取密钥半公开（随整包分发必泄露、非信任根，ADR-022 决策①），其鉴权仍只用 KeyHash 比对，
// 行为不变；另存一份 AES-256-GCM 可逆加密副本（KeyEnc）供平台管理员事后查看明文 + 复制，
// 与其「半公开、非信任根」的真实信任级一致（防投毒全靠 manifest 签名，本能力不触碰签名信任根）。
//
// 对称密钥经 env JIANMANAGER_CLIENT_KEY_ENC_SECRET 注入（32 字节 base64、不入库，同构签名私钥惯例）。
// 未配置时优雅降级（与 ADR-038 降级哲学一致）：dev_mode 回退内置 dev 密钥；生产未配则不写 KeyEnc、
// 查看返「不可查看」——绝不阻断建密钥（拉取密钥半公开、非信任根，缺加密密钥降级为不可查看可接受）。

// keyEncSecretLen 对称加密密钥字节数（AES-256 → 32 字节）。
const keyEncSecretLen = 32

// DevKeyEncSecretBase64 内置 dev 用 AES-256 密钥（32 字节，base64）。
// 仅 dev_mode=true 且未注入 env 密钥时零配置回退；源码公开，明示不得用于生产。
const DevKeyEncSecretBase64 = "ZGV2LW9ubHkta2V5LWZvci1jbGllbnQtcHVsbC1lbmM="

var (
	// ErrInvalidKeyEncSecret 注入了非法的拉取密钥加密密钥（非 base64 或长度不是 32 字节）。
	ErrInvalidKeyEncSecret = errors.New("拉取密钥加密密钥非法（须为 32 字节 base64）")
	// ErrKeyEncNotConfigured 未配置拉取密钥加密（生产态未注入 JIANMANAGER_CLIENT_KEY_ENC_SECRET）。
	ErrKeyEncNotConfigured = errors.New("拉取密钥加密未配置")
)

// KeyEncryptor 拉取密钥的 AES-256-GCM 可逆加密器（FR-192，见 ADR-044）。
// nil 值表示「未配置加密」：Encrypt 返回空串（调用方据此不写 KeyEnc），Decrypt 返回 ErrKeyEncNotConfigured。
type KeyEncryptor struct {
	gcm cipher.AEAD
}

// ResolveKeyEncryptor 按生产/开发态裁决拉取密钥加密器来源（FR-192，见 ADR-044，优雅降级）。
//
//   - 注入了密钥（secretB64 非空）：解析为 32 字节 AES 密钥构造加密器；非法即 ErrInvalidKeyEncSecret（配错快失败）。
//   - 未注入 + devMode=true：回退内置 dev 密钥（仅零配置开发），usedDevFallback=true 供上层告警。
//   - 未注入 + devMode=false：返回 (nil, false, nil)——优雅降级，不写 KeyEnc、不阻断建密钥。
func ResolveKeyEncryptor(secretB64 string, devMode bool) (enc *KeyEncryptor, usedDevFallback bool, err error) {
	if strings.TrimSpace(secretB64) == "" {
		if !devMode {
			// 生产未注入：优雅降级（密钥仍可创建/鉴权，只是不可查看）。
			return nil, false, nil
		}
		e, derr := newKeyEncryptor(DevKeyEncSecretBase64)
		if derr != nil {
			return nil, false, derr
		}
		return e, true, nil
	}
	e, perr := newKeyEncryptor(secretB64)
	if perr != nil {
		return nil, false, perr
	}
	return e, false, nil
}

// newKeyEncryptor 用 base64 编码的 32 字节密钥构造 AES-256-GCM 加密器。
func newKeyEncryptor(secretB64 string) (*KeyEncryptor, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(secretB64))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidKeyEncSecret, err)
	}
	if len(raw) != keyEncSecretLen {
		return nil, fmt.Errorf("%w: 实际 %d 字节", ErrInvalidKeyEncSecret, len(raw))
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidKeyEncSecret, err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidKeyEncSecret, err)
	}
	return &KeyEncryptor{gcm: gcm}, nil
}

// Encrypt 用 AES-256-GCM 加密明文，返回 base64(nonce ‖ ciphertext+tag)。
// nil 接收者（未配置）返回空串、无错——调用方据此跳过写 KeyEnc。
func (e *KeyEncryptor) Encrypt(plaintext string) (string, error) {
	if e == nil {
		return "", nil
	}
	nonce := make([]byte, e.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("生成 nonce 失败: %w", err)
	}
	// Seal 把密文+认证标签追加到 nonce 后，整体 base64 落库。
	sealed := e.gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt 解密 base64(nonce ‖ ciphertext+tag) 还原明文。
// nil 接收者（未配置）返回 ErrKeyEncNotConfigured；密文损坏/被篡改/密钥不符返回 GCM 认证错误。
func (e *KeyEncryptor) Decrypt(encoded string) (string, error) {
	if e == nil {
		return "", ErrKeyEncNotConfigured
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("解析密文失败: %w", err)
	}
	ns := e.gcm.NonceSize()
	if len(raw) < ns {
		return "", fmt.Errorf("密文长度不足")
	}
	nonce, ct := raw[:ns], raw[ns:]
	plain, err := e.gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("解密失败: %w", err)
	}
	return string(plain), nil
}
