package service

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

func jmpackSigner(t *testing.T) func([]byte) ([]byte, string) {
	t.Helper()
	s, err := NewManifestSigner(DevSignPrivateKeyPKCS8Base64, DefaultSignKeyID)
	require.NoError(t, err)
	return s.SignRaw
}

func jmpackPub(t *testing.T) func(string) ed25519.PublicKey {
	t.Helper()
	der, err := base64.StdEncoding.DecodeString(DevSignPublicKeySPKIBase64)
	require.NoError(t, err)
	pubAny, err := x509.ParsePKIXPublicKey(der)
	require.NoError(t, err)
	pub := pubAny.(ed25519.PublicKey)
	return func(keyID string) ed25519.PublicKey {
		if keyID == DefaultSignKeyID {
			return pub
		}
		return nil
	}
}

func jmpackInputs() []JmPackInput {
	a := []byte("AAA-mod-content")
	b := []byte("BBBB-config")
	return []JmPackInput{
		{Path: "mods/a.jar", SHA256: sha256Hex(string(a)), Size: int64(len(a)), Codec: "none", Data: a},
		{Path: "config/b.txt", SHA256: sha256Hex(string(b)), Size: int64(len(b)), Codec: "none", Data: b},
	}
}

// TestJmPack_RoundTrip 打包→验签解析往返，内容/元数据一致。
func TestJmPack_RoundTrip(t *testing.T) {
	packed, err := PackJmPack(jmpackInputs(), jmpackSigner(t))
	require.NoError(t, err)
	require.Equal(t, "JMPACK", string(packed[0:6]))

	out, err := ParseJmPack(packed, jmpackPub(t))
	require.NoError(t, err)
	require.Len(t, out, 2)
	require.Equal(t, "mods/a.jar", out[0].Path)
	require.Equal(t, []byte("AAA-mod-content"), out[0].Data)
	require.Equal(t, "none", out[0].Codec)
	require.Equal(t, "config/b.txt", out[1].Path)
	require.Equal(t, []byte("BBBB-config"), out[1].Data)
}

// TestJmPack_TamperRejected 篡改签名 → 验签失败。
func TestJmPack_TamperRejected(t *testing.T) {
	packed, err := PackJmPack(jmpackInputs(), jmpackSigner(t))
	require.NoError(t, err)
	packed[len(packed)-1] ^= 0xff // 翻转签名末字节。
	_, err = ParseJmPack(packed, jmpackPub(t))
	require.ErrorIs(t, err, ErrJmPackSignature)
}

// TestJmPack_BadMagicRejected 错误 magic → 格式非法。
func TestJmPack_BadMagicRejected(t *testing.T) {
	packed, err := PackJmPack(jmpackInputs(), jmpackSigner(t))
	require.NoError(t, err)
	packed[0] = 'X'
	_, err = ParseJmPack(packed, jmpackPub(t))
	require.ErrorIs(t, err, ErrJmPackFormat)
}

// TestJmPack_UnknownKeyIdRejected 未知 keyId → 拒。
func TestJmPack_UnknownKeyIdRejected(t *testing.T) {
	packed, err := PackJmPack(jmpackInputs(), jmpackSigner(t))
	require.NoError(t, err)
	none := func(string) ed25519.PublicKey { return nil }
	_, err = ParseJmPack(packed, none)
	require.ErrorIs(t, err, ErrJmPackSignature)
}

// TestJmPack_GoldenVectorEmit 产出固定输入的 .jmpack base64，供客户端 updater-core 跨语言兼容测试（JmPackTest）。
// 固定内容 + 内置 dev 签名密钥 → 客户端用内置 dev 公钥验签 + 解包应一致。
func TestJmPack_GoldenVectorEmit(t *testing.T) {
	content := []byte("jmpack-golden-content")
	files := []JmPackInput{
		{Path: "mods/hello.jar", SHA256: sha256Hex(string(content)), Size: int64(len(content)), Codec: "none", Data: content},
	}
	packed, err := PackJmPack(files, jmpackSigner(t))
	require.NoError(t, err)
	t.Logf("JMPACK_GOLDEN_BASE64=%s", base64.StdEncoding.EncodeToString(packed))
}
