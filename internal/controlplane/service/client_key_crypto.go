package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 拉取密钥可逆加密（FR-192，见 ADR-044）。
//
// 拉取密钥半公开（随整包分发必泄露、非信任根，ADR-022 决策①），其鉴权仍只用 KeyHash 比对，
// 行为不变；另存一份 AES-256-GCM 可逆加密副本（KeyEnc）供平台管理员事后查看明文 + 复制，
// 与其「半公开、非信任根」的真实信任级一致（防投毒全靠 manifest 签名，本能力不触碰签名信任根）。
//
// 对称密钥来源三轨（FR-263，优先级 env 注入 > 生产自动生成 > dev 回退）：
//   - env 注入（JIANMANAGER_CLIENT_KEY_ENC_SECRET，32 字节 base64）：优先，注入态不生成/不持久化；
//   - 生产未注入：自动生成 32 字节随机密钥并持久化到 <dataRoot>/etc/client-key-enc.key（0600，原子
//     rename），跨重启用同一密钥——零配置即可用、密钥始终可查看（同构 FR-248 签名密钥先例）；
//   - dev_mode 未注入：回退内置 dev 密钥（源码公开，仅零配置开发用）。
//
// 自动生成/读取失败时优雅降级返回 nil（不崩，密钥不可查看但其余功能正常，与 ADR-038 降级哲学一致）；
// 注入了非法 env 密钥仍快失败（配错即让运维修正）。

// keyEncSecretLen 对称加密密钥字节数（AES-256 → 32 字节）。
const keyEncSecretLen = 32

// 拉取密钥加密器来源标识（ResolveKeyEncryptor 返回的 source 值，FR-263）。
const (
	// KeyEncSourceEnv env 注入（JIANMANAGER_CLIENT_KEY_ENC_SECRET 非空）。
	KeyEncSourceEnv = "env"
	// KeyEncSourceGenerated 生产态自动生成并持久化到数据根文件。
	KeyEncSourceGenerated = "generated"
	// KeyEncSourceDev dev_mode 回退内置开发密钥。
	KeyEncSourceDev = "dev"
)

// keyEncFileName 持久化加密密钥的文件名（置于数据根 etc/ 下）。
const keyEncFileName = "client-key-enc.key"

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

// ResolveKeyEncryptor 按三轨裁决拉取密钥加密器来源（FR-192/FR-263，见 ADR-044，优雅降级）。
//
//   - env 注入（secretB64 非空）：解析为 32 字节 AES 密钥构造加密器；非法即 ErrInvalidKeyEncSecret
//     （配错快失败，让运维即时修正）。source=KeyEncSourceEnv，不生成/不持久化。
//   - 未注入 + devMode=true：回退内置 dev 密钥（仅零配置开发），source=KeyEncSourceDev。
//   - 未注入 + devMode=false：读 keyFilePath，存在→解析；不存在→生成 32 字节随机密钥→base64→写文件
//     （0600，原子 rename）→构造加密器，source=KeyEncSourceGenerated。
//   - 自动生成/读取失败 → 返回 (nil, "", nil) 优雅降级（不崩，密钥不可查看但其余功能正常）。
//
// keyFilePath 为持久化文件绝对路径（一般由 dataroot.Root.Abs("etc/client-key-enc.key") 提供，
// etc/ 目录由 dataroot.EnsureLayout 保证存在）。
func ResolveKeyEncryptor(secretB64 string, devMode bool, keyFilePath string) (enc *KeyEncryptor, source string, err error) {
	if strings.TrimSpace(secretB64) != "" {
		e, perr := newKeyEncryptor(secretB64)
		if perr != nil {
			return nil, "", perr
		}
		return e, KeyEncSourceEnv, nil
	}
	if devMode {
		e, derr := newKeyEncryptor(DevKeyEncSecretBase64)
		if derr != nil {
			// 内置常量理论上不会失败；保险起见降级而非 fatal。
			return nil, "", nil
		}
		return e, KeyEncSourceDev, nil
	}
	// 生产态：自动生成或读取持久化密钥。任何步骤失败 → 降级返回 nil（不崩）。
	e, gerr := loadOrGenerateKeyEncryptor(keyFilePath)
	if gerr != nil {
		return nil, "", nil
	}
	return e, KeyEncSourceGenerated, nil
}

// loadOrGenerateKeyEncryptor 读取或生成并持久化拉取密钥加密密钥（FR-263）。
// keyFilePath 存在 → 读取并解析（跨重启用同一密钥）；不存在 → 生成 32 字节随机密钥 → base64 →
// 原子写文件（0600）→ 构造加密器。返回错误由调用方据降级语义处理。
func loadOrGenerateKeyEncryptor(keyFilePath string) (*KeyEncryptor, error) {
	if data, rerr := os.ReadFile(keyFilePath); rerr == nil {
		// 文件存在：解析已有密钥（跨重启稳定）。
		secret := strings.TrimSpace(string(data))
		if secret != "" {
			return newKeyEncryptor(secret)
		}
		// 文件存在但为空：当作损坏，落到生成路径。
	} else if !os.IsNotExist(rerr) {
		return nil, fmt.Errorf("读取加密密钥文件失败: %w", rerr)
	}
	// 文件不存在（或为空）→ 生成新密钥并持久化。
	b := make([]byte, keyEncSecretLen)
	if _, gerr := rand.Read(b); gerr != nil {
		return nil, fmt.Errorf("生成加密密钥失败: %w", gerr)
	}
	secret := base64.StdEncoding.EncodeToString(b)
	if werr := writeKeyFileAtomic(keyFilePath, []byte(secret)); werr != nil {
		return nil, werr
	}
	return newKeyEncryptor(secret)
}

// writeKeyFileAtomic 以 0600 权限原子写文件：先写临时文件再 rename，防半写脏文件。
func writeKeyFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建密钥文件目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".client-key-enc.*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	// 失败路径清理临时文件（成功 rename 后临时文件已不存在，Remove 无副作用）。
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("设置文件权限失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("原子重命名失败: %w", err)
	}
	return nil
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
