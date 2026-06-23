package service

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// sampleSignedManifest 构造一份固定样例 manifest（contract §2），供 canonical/签名/结构断言复用。
func sampleSignedManifest() *SignedManifest {
	return &SignedManifest{
		SchemaVersion: 1,
		Channel:       "skyblock-s1",
		Version:       42,
		IssuedAt:      "2026-06-23T10:00:00Z",
		ManagedDirs:   []string{"mods", "config"},
		Files: []ManifestFile{
			{
				Path: "mods/foo.jar", SHA256: "ab12", MD5: "cd34", Size: 123456,
				Sync: "strict", Platform: "",
				Artifact: ManifestArtifact{SHA256: "ef56", Size: 45678, Codec: "zstd"},
			},
			{
				Path: "config/opt.txt", SHA256: "9988", MD5: "7766", Size: 12,
				Sync: "once", Platform: "windows",
				Artifact: ManifestArtifact{SHA256: "aa00", Size: 20, Codec: "none"},
			},
		},
		Agent: &ManifestAgent{
			Wedge: &ManifestWedge{Version: 3},
			Core: &ManifestCore{
				Version: 5,
				Platforms: map[string]ManifestAgentArtifact{
					"windows": {SHA256: "c1", Size: 100, Codec: "zstd"},
				},
			},
		},
	}
}

// TestCanonicalJSON_MatchesContractRules 固化 canonical JSON 规则：键码点升序、无空白、整数最简、
// null 平台、嵌套对象有序。此串即客户端 updater-core Json.canonical 对同一对象树的输出（逐位对齐）。
func TestCanonicalJSON_MatchesContractRules(t *testing.T) {
	m := sampleSignedManifest()
	got := string(SigningBytes(m))

	// 期望：去 sig，所有对象键码点升序递归排序、无空白、整数最简、全平台 platform=null。
	// 顶层键序 agent<channel<files<issuedAt<managedDirs<schemaVersion<version；
	// files[] 内键序 artifact<md5<path<platform<sha256<size<sync；
	// artifact 内键序 codec<sha256<size。managedDirs/files 数组顺序保持原序（不排序）。
	want := `{` +
		`"agent":{"core":{"platforms":{"windows":{"artifact":{"codec":"zstd","sha256":"c1","size":100}}},"version":5},"wedge":{"version":3}},` +
		`"channel":"skyblock-s1",` +
		`"files":[` +
		`{"artifact":{"codec":"zstd","sha256":"ef56","size":45678},"md5":"cd34","path":"mods/foo.jar","platform":null,"sha256":"ab12","size":123456,"sync":"strict"},` +
		`{"artifact":{"codec":"none","sha256":"aa00","size":20},"md5":"7766","path":"config/opt.txt","platform":"windows","sha256":"9988","size":12,"sync":"once"}` +
		`],` +
		`"issuedAt":"2026-06-23T10:00:00Z",` +
		`"managedDirs":["mods","config"],` +
		`"schemaVersion":1,` +
		`"version":42` +
		`}`
	require.Equal(t, want, got, "canonical JSON 必须与契约规则逐位一致（客户端据此验签）")
}

// TestSign_VerifiableWithEd25519PublicKey 用 Go ed25519.Verify 校验签名——
// Go 与 Java 的 Ed25519 同为 RFC 8032 PureEdDSA（64 字节签名、X.509 SPKI 公钥），
// Go 验签通过即等价于客户端 Signatures.verify 通过（无 JDK15+ 时的等价证明）。
func TestSign_VerifiableWithEd25519PublicKey(t *testing.T) {
	signer, err := NewManifestSigner(DevSignPrivateKeyPKCS8Base64, DefaultSignKeyID)
	require.NoError(t, err)

	m := sampleSignedManifest()
	require.NoError(t, signer.Sign(m))

	require.NotNil(t, m.Sig)
	require.Equal(t, "Ed25519", m.Sig.Alg)
	require.Equal(t, "k1", m.Sig.KeyID)

	// 从内置开发公钥（回填客户端的同值）解出公钥，验签。
	pubDER, err := base64.StdEncoding.DecodeString(DevSignPublicKeySPKIBase64)
	require.NoError(t, err)
	pubAny, err := x509.ParsePKIXPublicKey(pubDER)
	require.NoError(t, err)
	pub := pubAny.(ed25519.PublicKey)

	sigBytes, err := base64.StdEncoding.DecodeString(m.Sig.Value)
	require.NoError(t, err)
	require.True(t, ed25519.Verify(pub, SigningBytes(m), sigBytes),
		"签名必须可被内置公钥验证（等价于客户端验签通过）")

	// 公钥与私钥成对：签名器导出的 SPKI 应等于固化常量。
	exported, err := signer.PublicKeySPKIBase64()
	require.NoError(t, err)
	require.Equal(t, DevSignPublicKeySPKIBase64, exported, "导出公钥须与内置常量一致")
}

// TestSign_CoversVersionAndFiles 防降级/防篡改：改 version 或文件后，原签名对新 canonical 必失效。
func TestSign_CoversVersionAndFiles(t *testing.T) {
	signer, err := NewManifestSigner(DevSignPrivateKeyPKCS8Base64, DefaultSignKeyID)
	require.NoError(t, err)

	m := sampleSignedManifest()
	require.NoError(t, signer.Sign(m))
	origSig := m.Sig.Value

	pubDER, _ := base64.StdEncoding.DecodeString(DevSignPublicKeySPKIBase64)
	pubAny, _ := x509.ParsePKIXPublicKey(pubDER)
	pub := pubAny.(ed25519.PublicKey)
	sigBytes, _ := base64.StdEncoding.DecodeString(origSig)

	// 篡改 version：用原签名对新 canonical 验签必败。
	tampered := sampleSignedManifest()
	tampered.Version = 99
	require.False(t, ed25519.Verify(pub, SigningBytes(tampered), sigBytes),
		"改 version 后原签名必失效（契约 §3 覆盖 version）")

	// 篡改文件路径：同理失败。
	tampered2 := sampleSignedManifest()
	tampered2.Files[0].Path = "mods/evil.jar"
	require.False(t, ed25519.Verify(pub, SigningBytes(tampered2), sigBytes),
		"改文件路径后原签名必失效")
}

// TestSignedManifest_JSONStructureMatchesContract 断言序列化 JSON 含 contract §2 全部字段与结构，
// 可被客户端 Manifest.parse 解析（字段名/嵌套/类型对齐）。
func TestSignedManifest_JSONStructureMatchesContract(t *testing.T) {
	signer, err := NewManifestSigner(DevSignPrivateKeyPKCS8Base64, DefaultSignKeyID)
	require.NoError(t, err)
	m := sampleSignedManifest()
	require.NoError(t, signer.Sign(m))

	raw, err := json.Marshal(m)
	require.NoError(t, err)

	var obj map[string]any
	require.NoError(t, json.Unmarshal(raw, &obj))

	// 顶层字段（Manifest.parse 读取的键）。
	require.EqualValues(t, 1, obj["schemaVersion"])
	require.Equal(t, "skyblock-s1", obj["channel"])
	require.EqualValues(t, 42, obj["version"])
	require.Contains(t, obj, "issuedAt")
	require.ElementsMatch(t, []any{"mods", "config"}, obj["managedDirs"])

	files := obj["files"].([]any)
	require.Len(t, files, 2)
	f0 := files[0].(map[string]any)
	for _, k := range []string{"path", "sha256", "md5", "size", "sync", "platform", "artifact"} {
		require.Contains(t, f0, k, "files[] 须含契约字段 %s", k)
	}
	require.Nil(t, f0["platform"], "全平台文件 platform 须为 null")
	art := f0["artifact"].(map[string]any)
	for _, k := range []string{"sha256", "size", "codec"} {
		require.Contains(t, art, k, "artifact 须含契约字段 %s", k)
	}

	// 签名段。
	sig := obj["sig"].(map[string]any)
	require.Equal(t, "Ed25519", sig["alg"])
	require.Equal(t, "k1", sig["keyId"])
	require.NotEmpty(t, sig["value"])

	// 自更新段。
	agent := obj["agent"].(map[string]any)
	core := agent["core"].(map[string]any)
	require.EqualValues(t, 5, core["version"])
	platforms := core["platforms"].(map[string]any)
	require.Contains(t, platforms, "windows")
}

// TestNewManifestSigner_Errors 私钥缺失/非法的错误路径。
func TestNewManifestSigner_Errors(t *testing.T) {
	_, err := NewManifestSigner("", "k1")
	require.ErrorIs(t, err, ErrSignKeyNotConfigured)

	_, err = NewManifestSigner("not-base64-!!!", "k1")
	require.ErrorIs(t, err, ErrInvalidSignKey)

	_, err = NewManifestSigner(base64.StdEncoding.EncodeToString([]byte("garbage")), "k1")
	require.ErrorIs(t, err, ErrInvalidSignKey)
}

// 防止 strings 包未用（保留以备扩展断言）。
var _ = strings.TrimSpace
