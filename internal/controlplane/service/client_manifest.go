package service

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// 客户端分发 manifest 的类型、canonical JSON 序列化与 Ed25519 签名（FR-087，见 ADR-022、contract §2/§3）。
//
// 信任根 = manifest 的 Ed25519 签名；客户端 updater-core（Signatures.java / Json.java）内置公钥验签。
// 服务端签名与客户端验签必须对**同一份 canonical JSON 字节**计算，故此处的 canonical 规则与
// updater-core 的 Json.canonical 逐位对齐：键按码点升序、无多余空白、整数最简形式、非 ASCII 原样、
// 控制字符 \uXXXX 小写转义。任何偏差都会导致客户端验签失败。

var (
	// ErrSignKeyNotConfigured 未配置签名私钥（缺省也无内置开发私钥时）。
	ErrSignKeyNotConfigured = errors.New("客户端 manifest 签名私钥未配置")
	// ErrInvalidSignKey 签名私钥解析失败（非 base64 PKCS#8 Ed25519）。
	ErrInvalidSignKey = errors.New("客户端 manifest 签名私钥无效")
)

// manifestSchemaVersion 契约结构版本（contract §2 schemaVersion）。结构 break 时 +1。
const manifestSchemaVersion = 1

// ManifestArtifact manifest 文件的下载制品引用（内容寻址，contract §2 files[].artifact）。
type ManifestArtifact struct {
	// SHA256 制品（压缩后）自身 hash = 下载寻址 key = client-file 资产的 sha256。
	SHA256 string `json:"sha256"`
	// Size 制品（压缩后）字节数。
	Size int64 `json:"size"`
	// Codec 压缩算法："zstd" | "none"。
	Codec string `json:"codec"`
}

// ManifestFile manifest 单文件条目（contract §2 files[]）。
// sha256/md5/size 描述**解压后原始内容**（强校验/快筛）；artifact 描述下载制品（压缩态）。
type ManifestFile struct {
	// Path 相对 gameDir 的 POSIX 路径（统一 `/`，不得逃逸 `..`）。
	Path string `json:"path"`
	// SHA256 解压后原始内容 hash（信任校验，强）。
	SHA256 string `json:"sha256"`
	// MD5 解压后原始内容 md5（本地快筛，弱，不可作信任）。
	MD5 string `json:"md5"`
	// Size 解压后原始大小（字节）。
	Size int64 `json:"size"`
	// Sync 同步策略：strict=强制一致 | once=仅缺失时写 | ignore=不动。
	Sync string `json:"sync"`
	// Platform 平台门控：空=全平台 | windows | macos | linux。
	Platform string `json:"platform"`
	// Artifact 下载制品引用。
	Artifact ManifestArtifact `json:"artifact"`
}

// ValidSyncMode 报告 sync 取值是否合法（strict|once|ignore）。
func ValidSyncMode(s string) bool {
	switch s {
	case "strict", "once", "ignore":
		return true
	}
	return false
}

// ValidPlatform 报告 platform 取值是否合法（空=全平台，或 windows|macos|linux）。
func ValidPlatform(s string) bool {
	switch s {
	case "", "windows", "macos", "linux":
		return true
	}
	return false
}

// ManifestAgentArtifact 自更新段单平台制品（contract §2 agent.core.platforms[os].artifact）。
type ManifestAgentArtifact struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Codec  string `json:"codec"`
}

// ManifestWedge 楔子版本段（信息性；楔子随基础包、不自更新）。
type ManifestWedge struct {
	Version int `json:"version"`
}

// ManifestCore updater-core 自更新段（FR-091 消费）：版本 + 各平台制品。
type ManifestCore struct {
	Version   int                              `json:"version"`
	Platforms map[string]ManifestAgentArtifact `json:"platforms"`
}

// ManifestAgent 楔子 + updater-core 自更新段（contract §2 agent）。
type ManifestAgent struct {
	Wedge *ManifestWedge `json:"wedge,omitempty"`
	Core  *ManifestCore  `json:"core,omitempty"`
}

// ManifestSig manifest 签名段（contract §2 sig，§3 范围）。
type ManifestSig struct {
	Alg   string `json:"alg"`
	KeyID string `json:"keyId"`
	Value string `json:"value"`
}

// SignedManifest 完整签名 manifest（contract §2）。
//
// 关键：HTTP 响应 JSON 与签名 canonical JSON 必须是**同一棵逻辑树**——否则客户端对响应 JSON
// 重新 canonical 验签会与服务端签名输入不一致而失败。故 MarshalJSON 与签名都走 manifestToTree
// 单一真源（如全平台文件 platform 统一为 JSON null，而非 Go 零值 ""）。
type SignedManifest struct {
	SchemaVersion int            `json:"schemaVersion"`
	Channel       string         `json:"channel"`
	Version       int            `json:"version"`
	IssuedAt      string         `json:"issuedAt"`
	ManagedDirs   []string       `json:"managedDirs"`
	Files         []ManifestFile `json:"files"`
	Agent         *ManifestAgent `json:"agent,omitempty"`
	Sig           *ManifestSig   `json:"sig,omitempty"`
}

// MarshalJSON 从 manifestToTree 生成响应 JSON，确保与签名输入同源（见 SignedManifest 文档）。
// 输出非 canonical（键序由 encoding/json 决定），但**值与结构**与签名树一致，客户端重新 canonical 后
// 得到的字节与服务端签名输入逐位相同。
func (m *SignedManifest) MarshalJSON() ([]byte, error) {
	return json.Marshal(manifestToTree(m))
}

// ManifestSigner 用 Ed25519 私钥对 manifest 签名（contract §3）。
// keyID 标识公钥版本（轮换用，主 k1 + 备 k2…，ADR-022 决策 8）；客户端按 sig.keyId 选内置公钥验签。
type ManifestSigner struct {
	priv  ed25519.PrivateKey
	keyID string
}

// NewManifestSigner 用 base64(PKCS#8 DER) 私钥与 keyID 构造签名器。
// privKeyB64 为空返回 ErrSignKeyNotConfigured；解析失败返回 ErrInvalidSignKey。
func NewManifestSigner(privKeyB64, keyID string) (*ManifestSigner, error) {
	privKeyB64 = strings.TrimSpace(privKeyB64)
	if privKeyB64 == "" {
		return nil, ErrSignKeyNotConfigured
	}
	der, err := base64.StdEncoding.DecodeString(privKeyB64)
	if err != nil {
		return nil, fmt.Errorf("%w: base64 解码失败: %v", ErrInvalidSignKey, err)
	}
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("%w: PKCS#8 解析失败: %v", ErrInvalidSignKey, err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%w: 私钥非 Ed25519", ErrInvalidSignKey)
	}
	if keyID == "" {
		keyID = DefaultSignKeyID
	}
	return &ManifestSigner{priv: priv, keyID: keyID}, nil
}

// KeyID 返回签名器使用的公钥版本标识。
func (s *ManifestSigner) KeyID() string { return s.keyID }

// SignRaw 对任意原始字节做 Ed25519 签名，返回 (签名, keyId)。供 .jmpack 容器签名复用同一信任根（FR-097）。
func (s *ManifestSigner) SignRaw(msg []byte) ([]byte, string) {
	return ed25519.Sign(s.priv, msg), s.keyID
}

// PublicKeySPKIBase64 返回公钥 X.509 SubjectPublicKeyInfo DER 的 base64（与客户端内置公钥对照用）。
func (s *ManifestSigner) PublicKeySPKIBase64() (string, error) {
	pub := s.priv.Public().(ed25519.PublicKey)
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("编码公钥失败: %w", err)
	}
	return base64.StdEncoding.EncodeToString(der), nil
}

// Sign 对 manifest 去 sig 后的 canonical JSON 做 Ed25519 签名，回填 m.Sig（contract §3）。
// 签名覆盖 version + 文件全集 → 攻击者无法改版本号/文件而保持签名有效。
func (s *ManifestSigner) Sign(m *SignedManifest) error {
	m.Sig = nil
	tree := manifestToTree(m)
	canon := canonicalJSON(tree)
	sig := ed25519.Sign(s.priv, []byte(canon))
	m.Sig = &ManifestSig{
		Alg:   "Ed25519",
		KeyID: s.keyID,
		Value: base64.StdEncoding.EncodeToString(sig),
	}
	return nil
}

// SigningBytes 返回 manifest 去 sig 后的 canonical JSON 字节（测试/对照用，等于 Sign 的签名输入）。
func SigningBytes(m *SignedManifest) []byte {
	cp := *m
	cp.Sig = nil
	return []byte(canonicalJSON(manifestToTree(&cp)))
}

// manifestToTree 把 SignedManifest 转为有序无关的原生对象树（map/slice/string/int64/nil），
// 供 canonicalJSON 递归排序序列化。键名与 contract §2 字段名严格一致。
// 注意：与客户端 Manifest.parse 后再 canonical 的结构对齐——客户端解析后 raw 树即 JSON 原样字段，
// 故此处必须包含 manifest 输出 JSON 的**全部字段**（含 issuedAt、agent 等），且类型一致（整数→int64）。
func manifestToTree(m *SignedManifest) map[string]any {
	root := map[string]any{
		"schemaVersion": int64(m.SchemaVersion),
		"channel":       m.Channel,
		"version":       int64(m.Version),
		"issuedAt":      m.IssuedAt,
		"managedDirs":   stringsToTree(m.ManagedDirs),
		"files":         filesToTree(m.Files),
	}
	if m.Agent != nil {
		root["agent"] = agentToTree(m.Agent)
	}
	if m.Sig != nil {
		root["sig"] = map[string]any{
			"alg":   m.Sig.Alg,
			"keyId": m.Sig.KeyID,
			"value": m.Sig.Value,
		}
	}
	return root
}

func stringsToTree(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func filesToTree(files []ManifestFile) []any {
	out := make([]any, len(files))
	for i, f := range files {
		out[i] = map[string]any{
			"path":     f.Path,
			"sha256":   f.SHA256,
			"md5":      f.MD5,
			"size":     f.Size,
			"sync":     f.Sync,
			"platform": platformValue(f.Platform),
			"artifact": map[string]any{
				"sha256": f.Artifact.SHA256,
				"size":   f.Artifact.Size,
				"codec":  f.Artifact.Codec,
			},
		}
	}
	return out
}

func agentToTree(a *ManifestAgent) map[string]any {
	out := map[string]any{}
	if a.Wedge != nil {
		out["wedge"] = map[string]any{"version": int64(a.Wedge.Version)}
	}
	if a.Core != nil {
		platforms := map[string]any{}
		for os, art := range a.Core.Platforms {
			platforms[os] = map[string]any{
				"artifact": map[string]any{
					"sha256": art.SHA256,
					"size":   art.Size,
					"codec":  art.Codec,
				},
			}
		}
		out["core"] = map[string]any{
			"version":   int64(a.Core.Version),
			"platforms": platforms,
		}
	}
	return out
}

// platformValue 把空字符串平台映射为 JSON null（contract §2：null=全平台）。
func platformValue(p string) any {
	if p == "" {
		return nil
	}
	return p
}

// canonicalJSON 把原生对象树序列化为 canonical JSON（与 updater-core Json.canonical 逐位对齐）：
//   - 对象键按 UTF-16 码元升序（ASCII 键等价于字节序）递归排序；
//   - 无多余空白；分隔符 `:` 与 `,`；
//   - 整数最简形式（不带小数点/指数）；
//   - 字符串转义 " \ \n \r \t \b \f，控制字符 <0x20 转 \uXXXX（小写），其余原样（含非 ASCII）。
//
// 仅支持 nil/bool/string/int/int64/[]any/map[string]any（manifest 用到的类型全集）。
func canonicalJSON(value any) string {
	var sb strings.Builder
	writeCanonical(value, &sb)
	return sb.String()
}

func writeCanonical(value any, sb *strings.Builder) {
	switch v := value.(type) {
	case nil:
		sb.WriteString("null")
	case bool:
		if v {
			sb.WriteString("true")
		} else {
			sb.WriteString("false")
		}
	case string:
		writeCanonicalString(v, sb)
	case int:
		sb.WriteString(strconv.FormatInt(int64(v), 10))
	case int64:
		sb.WriteString(strconv.FormatInt(v, 10))
	case []any:
		sb.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				sb.WriteByte(',')
			}
			writeCanonical(item, sb)
		}
		sb.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		// Java TreeMap 用 String.compareTo（UTF-16 码元序）；ASCII 键等价于 Go 字节序排序。
		sort.Strings(keys)
		sb.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				sb.WriteByte(',')
			}
			writeCanonicalString(k, sb)
			sb.WriteByte(':')
			writeCanonical(v[k], sb)
		}
		sb.WriteByte('}')
	default:
		// manifest 构造受控，不应到达；保底 panic 暴露编程错误而非静默产出错签名。
		panic(fmt.Sprintf("canonicalJSON 不支持的类型: %T", value))
	}
}

// writeCanonicalString 按 updater-core Json.writeString 的规则转义字符串。
// 按 rune 遍历：转义集合与 Java 逐 char 等价（被转义字符均为 BMP 单码元）；
// 控制字符 <0x20 转 \uXXXX 小写；其余原样写出（BMP 与非 BMP 的 UTF-8 与 Java
// getBytes(UTF_8) 一致，非 BMP 字符不会是控制字符，故按 rune 处理与按 char 等价）。
// manifest 字段值全为 ASCII（路径/hex/base64/slug/字面量/时间戳），非 BMP 实际不会出现。
func writeCanonicalString(s string, sb *strings.Builder) {
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString("\\\"")
		case '\\':
			sb.WriteString("\\\\")
		case '\n':
			sb.WriteString("\\n")
		case '\r':
			sb.WriteString("\\r")
		case '\t':
			sb.WriteString("\\t")
		case '\b':
			sb.WriteString("\\b")
		case '\f':
			sb.WriteString("\\f")
		default:
			if r < 0x20 {
				fmt.Fprintf(sb, "\\u%04x", r)
			} else {
				sb.WriteRune(r)
			}
		}
	}
	sb.WriteByte('"')
}
