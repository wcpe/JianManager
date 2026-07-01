package service

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 客户端 manifest 签名密钥常量（FR-087，见 ADR-022 决策 2/8）。
//
// 信任根 = manifest 的 Ed25519 签名；客户端 updater-core 内置**公钥**验签。
// 私钥**服务端持有、env 注入不入库**（contract §3、config-files 规范：敏感信息不硬编码生产值）。
//
// 下方固化的是一对**仅供开发环境**的密钥：
//   - DevSignPublicKeySPKIBase64 同时回填到客户端 updater-core 的
//     Signatures.production()（keyId=k1），使开发期两线可端到端验签；
//   - DevSignPrivateKeyPKCS8Base64 仅在 dev_mode=true 且未注入私钥时作零配置回退
//     （见 ResolveManifestSigner），并写入 configs/control-plane.yml 注释示例。
//
// 生产部署（dev_mode=false）**必须**经 JIANMANAGER_CLIENT_SIGN_PRIVKEY 注入独立私钥，并把对应公钥
// 回填 Signatures.production()（随基础包分发）；**未注入即降级**为客户端 OTA 不可用、配错（注入无效/
// 开发密钥）则拒绝启动（见 StartableWithoutSigner），绝不沿用此开发密钥对外签名（私钥已在源码中公开，否则可被伪造投毒）。

// DefaultSignKeyID 默认签名公钥版本标识（主公钥）。轮换时新增 k2… 并更新客户端内置集。
const DefaultSignKeyID = "k1"

// DevSignPublicKeySPKIBase64 开发用 Ed25519 公钥（X.509 SubjectPublicKeyInfo DER, base64）。
// 与 DevSignPrivateKeyPKCS8Base64 成对；同值回填 updater-core Signatures.production() 的 k1。
const DevSignPublicKeySPKIBase64 = "MCowBQYDK2VwAyEAsO7B/k+2++wQtN/L0jpCXCjsGnYV5Sx2eyCk0pDzV0Y="

// DevSignPrivateKeyPKCS8Base64 开发用 Ed25519 私钥（PKCS#8 DER, base64）。
// 仅用于零配置开发环境；生产经 env 覆盖。明示公开——不得用于生产。
const DevSignPrivateKeyPKCS8Base64 = "MC4CAQAwBQYDK2VwBCIEIMomw76hnk28SRyhq6JL3IDN7DMThLnBqdc5Lf4TrDzg"

var (
	// ErrSignKeyRequiredInProd 生产态（dev_mode=false）未注入签名私钥时拒绝回退内置开发密钥（FR-087，见 ADR-022）。
	ErrSignKeyRequiredInProd = errors.New("生产态（dev_mode=false）必须经 JIANMANAGER_CLIENT_SIGN_PRIVKEY 注入客户端 manifest 签名私钥")
	// ErrDevSignKeyInProd 生产态显式注入了源码公开的内置开发密钥时拒绝（同属可被伪造的投毒面）。
	ErrDevSignKeyInProd = errors.New("生产态（dev_mode=false）拒绝使用内置开发签名密钥（源码已公开），必须注入独立私钥")
)

// 签名密钥来源标识（FR-248，见 ADR-052）：面板据此展示公钥来源徽章。
const (
	// SignKeySourceEnv 来自 env 注入的生产私钥（JIANMANAGER_CLIENT_SIGN_PRIVKEY）。
	SignKeySourceEnv = "env"
	// SignKeySourceGenerated 生产态未注入时自动生成并持久化到数据根的密钥。
	SignKeySourceGenerated = "generated"
	// SignKeySourceDev 开发态未注入时回退的源码内置开发密钥（仅 dev_mode）。
	SignKeySourceDev = "dev"
)

// StartableWithoutSigner 报告签名器初始化错误是否应「降级启动」而非阻断整个 CP。
//
// 语义变更（FR-248，见 ADR-052）：ResolveManifestSignerWithAutogen 装配路径下，「生产态未注入私钥」
// 不再返回 ErrSignKeyRequiredInProd（改为自动生成密钥启用签名），故本判定在新路径下**不再命中**。
// 保留 ErrSignKeyRequiredInProd 与本函数供既有 ResolveManifestSigner 及其测试/兼容使用：
// 仅「生产态未注入私钥」(ErrSignKeyRequiredInProd) 属未配置——降级为客户端 OTA 分发/签名功能不可用
// （消费服务持 nil signer 时返回 ErrSignKeyNotConfigured，绝不回退源码公开的开发密钥对外签名，
// 守住 ADR-022 安全核心），其余 CP 功能照常启动。
//
// 注入了无效私钥或误用源码公开的开发密钥（ErrInvalidSignKey / ErrDevSignKeyInProd）属「想用却配错」，
// 应 fail-fast 让运维即时修正，故返回 false（不降级）。
func StartableWithoutSigner(err error) bool {
	return errors.Is(err, ErrSignKeyRequiredInProd)
}

// ResolveManifestSigner 按生产/开发态裁决客户端 manifest 签名密钥来源（fail-closed，FR-087、ADR-022）。
//
//   - 注入了私钥（privKeyB64 非空）：用注入私钥构造签名器（keyID 空回退 DefaultSignKeyID）。
//   - 未注入 + devMode=true：回退内置开发密钥（仅零配置开发），usedDevFallback=true 供上层告警。
//   - 未注入 + devMode=false：返回 ErrSignKeyRequiredInProd（拒绝用源码公开的开发密钥对外签名）。
//
// 返回 usedDevFallback 标记是否走了开发密钥零配置回退（仅 devMode 路径）。
func ResolveManifestSigner(privKeyB64, keyID string, devMode bool) (signer *ManifestSigner, usedDevFallback bool, err error) {
	if strings.TrimSpace(privKeyB64) == "" {
		// 未注入私钥：生产态拒绝（绝不回退到源码公开的开发密钥对外签名），仅开发态零配置回退。
		if !devMode {
			return nil, false, ErrSignKeyRequiredInProd
		}
		s, e := NewManifestSigner(DevSignPrivateKeyPKCS8Base64, DefaultSignKeyID)
		if e != nil {
			return nil, false, e
		}
		return s, true, nil
	}

	// 注入了私钥：先解析，解析失败透传（不静默回退）。
	s, e := NewManifestSigner(privKeyB64, keyID)
	if e != nil {
		return nil, false, e
	}
	// 生产态额外防线：即便显式注入，也拒绝源码公开的内置开发密钥（按解出公钥识别，防再编码绕过）。
	if !devMode {
		pub, perr := s.PublicKeySPKIBase64()
		if perr != nil {
			return nil, false, perr
		}
		if pub == DevSignPublicKeySPKIBase64 {
			return nil, false, ErrDevSignKeyInProd
		}
	}
	return s, false, nil
}

// LoadOrGenerateSigner 从 keyPath 加载 Ed25519 签名私钥，缺失则生成并持久化（FR-248，见 ADR-052）。
//
//   - keyPath 存在：读 PEM → 解 PKCS#8 DER → 构造签名器（跨重启用同一密钥，公钥不变）。
//   - keyPath 不存在：ed25519.GenerateKey → PKCS#8 DER → PEM 编码 → 先写临时文件（0600）再原子 rename
//     防半写 → 构造签名器。父目录应由 dataroot 保证存在（<root>/etc）。
//
// 生成/持久化失败返回错误（调用方据此 fatal——信任根必须可用，与「配错快失败」一致）。
// 每部署独立密钥、私钥 0600 且永不出服务端；公钥本就公开（客户端内置验签）。
func LoadOrGenerateSigner(keyPath, keyID string) (*ManifestSigner, error) {
	if strings.TrimSpace(keyID) == "" {
		keyID = DefaultSignKeyID
	}

	if raw, err := os.ReadFile(keyPath); err == nil {
		block, _ := pem.Decode(raw)
		if block == nil {
			return nil, fmt.Errorf("%w: 签名密钥文件 %s 非合法 PEM", ErrInvalidSignKey, keyPath)
		}
		der := block.Bytes
		return NewManifestSigner(base64.StdEncoding.EncodeToString(der), keyID)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("读取签名密钥文件 %s 失败: %w", keyPath, err)
	}

	// 不存在：生成 + 持久化。
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成 Ed25519 签名密钥失败: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("编码签名私钥 PKCS#8 失败: %w", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := writeFileAtomic(keyPath, pemBytes, 0o600); err != nil {
		return nil, fmt.Errorf("持久化签名密钥到 %s 失败: %w", keyPath, err)
	}
	return NewManifestSigner(base64.StdEncoding.EncodeToString(der), keyID)
}

// writeFileAtomic 原子写文件：先写同目录临时文件（perm），再 rename 覆盖目标，避免半写残留。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// 失败路径清理临时文件（成功 rename 后 tmpName 已不存在，Remove 返回的 not-exist 忽略）。
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// CreateTemp 默认 0600；显式 Chmod 以贯彻调用方要求的权限（Windows 语义有限，惯例写法）。
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// ResolveManifestSignerWithAutogen 按三轨裁决客户端 manifest 签名密钥来源（FR-248，见 ADR-052）。
//
// 优先级由高到低：
//   - env 注入非空（privKeyB64）：走 ResolveManifestSigner（含生产拒源码开发密钥 ErrDevSignKeyInProd
//     防线），来源 SignKeySourceEnv；注入态**不生成、不持久化**。
//   - 未注入 + devMode=true：回退内置开发密钥（公钥已回填 updater-core，保开发端到端验签），
//     来源 SignKeySourceDev；不写文件。
//   - 未注入 + devMode=false（生产）：LoadOrGenerateSigner(keyPath) 自动生成并持久化，
//     来源 SignKeySourceGenerated（取代 ADR-038 的「未注入即降级」）。
//
// 生成/持久化失败或注入私钥非法均返回错误（调用方据此 fatal——信任根必须可用，配错快失败）。
func ResolveManifestSignerWithAutogen(privKeyB64, keyID string, devMode bool, keyPath string) (signer *ManifestSigner, source string, err error) {
	if strings.TrimSpace(privKeyB64) != "" {
		s, _, e := ResolveManifestSigner(privKeyB64, keyID, devMode)
		if e != nil {
			return nil, "", e
		}
		return s, SignKeySourceEnv, nil
	}

	if devMode {
		s, e := NewManifestSigner(DevSignPrivateKeyPKCS8Base64, DefaultSignKeyID)
		if e != nil {
			return nil, "", e
		}
		return s, SignKeySourceDev, nil
	}

	s, e := LoadOrGenerateSigner(keyPath, keyID)
	if e != nil {
		return nil, "", e
	}
	return s, SignKeySourceGenerated, nil
}
