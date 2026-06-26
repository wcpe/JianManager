package service

import (
	"errors"
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
//     （见 ResolveManifestSigner），并写入 configs/control-plane.yaml 注释示例。
//
// 生产部署（dev_mode=false）**必须**经 JIANMANAGER_CLIENT_SIGN_PRIVKEY 注入独立私钥，并把对应公钥
// 回填 Signatures.production()（随基础包分发）；**未注入即 fail-closed 拒绝启动**（ResolveManifestSigner），
// 绝不沿用此开发密钥（私钥已在源码中公开，否则可被伪造投毒）。

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
