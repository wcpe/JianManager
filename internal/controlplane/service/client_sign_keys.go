package service

// 客户端 manifest 签名密钥常量（FR-087，见 ADR-022 决策 2/8）。
//
// 信任根 = manifest 的 Ed25519 签名；客户端 updater-core 内置**公钥**验签。
// 私钥**服务端持有、env 注入不入库**（contract §3、config-files 规范：敏感信息不硬编码生产值）。
//
// 下方固化的是一对**仅供开发/默认环境**的密钥：
//   - DevSignPublicKeySPKIBase64 同时回填到客户端 updater-core 的
//     Signatures.production()（keyId=k1），使开发期两线可端到端验签；
//   - DevSignPrivateKeyPKCS8Base64 作为零配置默认私钥（env 未设时使用），并写入
//     configs/control-plane.yaml 注释示例。
//
// 生产部署**必须**经 JIANMANAGER_CLIENT_SIGN_PRIVKEY 注入独立私钥，并把对应公钥回填
// Signatures.production()（随基础包分发），切勿沿用此开发密钥（私钥已在源码中公开）。

// DefaultSignKeyID 默认签名公钥版本标识（主公钥）。轮换时新增 k2… 并更新客户端内置集。
const DefaultSignKeyID = "k1"

// DevSignPublicKeySPKIBase64 开发用 Ed25519 公钥（X.509 SubjectPublicKeyInfo DER, base64）。
// 与 DevSignPrivateKeyPKCS8Base64 成对；同值回填 updater-core Signatures.production() 的 k1。
const DevSignPublicKeySPKIBase64 = "MCowBQYDK2VwAyEAsO7B/k+2++wQtN/L0jpCXCjsGnYV5Sx2eyCk0pDzV0Y="

// DevSignPrivateKeyPKCS8Base64 开发用 Ed25519 私钥（PKCS#8 DER, base64）。
// 仅用于零配置开发环境；生产经 env 覆盖。明示公开——不得用于生产。
const DevSignPrivateKeyPKCS8Base64 = "MC4CAQAwBQYDK2VwBCIEIMomw76hnk28SRyhq6JL3IDN7DMThLnBqdc5Lf4TrDzg"
