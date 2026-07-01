package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// FR-248（见 ADR-052）：签名私钥来源三轨（env 优先 → 生产未注入自动生成持久化 → dev 回退内置开发密钥）
// 与 LoadOrGenerateSigner 的持久化/重载稳定性覆盖。测试先行，锁定行为契约。

// TestLoadOrGenerateSigner_GeneratesAndPersists 首次调用生成 Ed25519 并写 PKCS#8 PEM 文件（0600），
// 返回可用签名器；文件应真实落盘。
func TestLoadOrGenerateSigner_GeneratesAndPersists(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "client-sign-key.pem")

	signer, err := LoadOrGenerateSigner(keyPath, DefaultSignKeyID)
	require.NoError(t, err)
	require.NotNil(t, signer)
	require.Equal(t, DefaultSignKeyID, signer.KeyID())

	// 文件应已生成，且非源码公开的开发密钥（每部署独立）。
	info, statErr := os.Stat(keyPath)
	require.NoError(t, statErr)
	require.Greater(t, info.Size(), int64(0))

	pub, perr := signer.PublicKeySPKIBase64()
	require.NoError(t, perr)
	require.NotEqual(t, DevSignPublicKeySPKIBase64, pub)
}

// TestLoadOrGenerateSigner_ReloadStable 已存在密钥文件时应加载既有私钥，
// 跨调用（模拟重启）派生同一公钥——保证已分发客户端验签不因重启失效。
func TestLoadOrGenerateSigner_ReloadStable(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "client-sign-key.pem")

	first, err := LoadOrGenerateSigner(keyPath, DefaultSignKeyID)
	require.NoError(t, err)
	pub1, err := first.PublicKeySPKIBase64()
	require.NoError(t, err)

	// 记录首次写入的原始字节，二次调用不应重写文件。
	raw1, err := os.ReadFile(keyPath)
	require.NoError(t, err)

	second, err := LoadOrGenerateSigner(keyPath, DefaultSignKeyID)
	require.NoError(t, err)
	pub2, err := second.PublicKeySPKIBase64()
	require.NoError(t, err)

	require.Equal(t, pub1, pub2, "重载后公钥必须不变（同一密钥）")

	raw2, err := os.ReadFile(keyPath)
	require.NoError(t, err)
	require.Equal(t, raw1, raw2, "已存在密钥时不应重写文件")
}

// TestLoadOrGenerateSigner_PersistFailReturnsError 持久化目标不可写（父路径是文件）时返回错误，
// 不静默吞掉——信任根必须可用，配错快失败。
func TestLoadOrGenerateSigner_PersistFailReturnsError(t *testing.T) {
	// 用一个已存在的普通文件充当「父目录」，其下写文件必失败。
	notADir := filepath.Join(t.TempDir(), "iamafile")
	require.NoError(t, os.WriteFile(notADir, []byte("x"), 0o600))
	keyPath := filepath.Join(notADir, "client-sign-key.pem")

	signer, err := LoadOrGenerateSigner(keyPath, DefaultSignKeyID)
	require.Error(t, err)
	require.Nil(t, signer)
}

// TestResolveManifestSignerWithAutogen_EnvTakesPrecedence env 注入非空时用注入私钥、来源 env，
// 且绝不生成/写文件（keyPath 指向的文件不应被创建）。
func TestResolveManifestSignerWithAutogen_EnvTakesPrecedence(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "should-not-exist.pem")
	prod := freshProdSignKeyB64(t)

	signer, source, err := ResolveManifestSignerWithAutogen(prod, "k1", false, keyPath)
	require.NoError(t, err)
	require.NotNil(t, signer)
	require.Equal(t, "env", source)
	require.Equal(t, "k1", signer.KeyID())

	_, statErr := os.Stat(keyPath)
	require.True(t, os.IsNotExist(statErr), "env 注入态绝不应生成密钥文件")
}

// TestResolveManifestSignerWithAutogen_ProdAutogen 生产态未注入 → 自动生成持久化，来源 generated，
// 再次解析（模拟重启）得同一公钥（稳定）。取代 ADR-038 的「未注入即降级」。
func TestResolveManifestSignerWithAutogen_ProdAutogen(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "client-sign-key.pem")

	signer, source, err := ResolveManifestSignerWithAutogen("", "", false, keyPath)
	require.NoError(t, err)
	require.NotNil(t, signer)
	require.Equal(t, "generated", source)

	_, statErr := os.Stat(keyPath)
	require.NoError(t, statErr, "生产未注入应自动生成密钥文件")

	pub1, err := signer.PublicKeySPKIBase64()
	require.NoError(t, err)
	require.NotEqual(t, DevSignPublicKeySPKIBase64, pub1, "自动生成密钥不得等于源码公开的开发密钥")

	// 模拟重启：同 keyPath 再解析一次，公钥不变、来源仍 generated。
	signer2, source2, err := ResolveManifestSignerWithAutogen("", "", false, keyPath)
	require.NoError(t, err)
	require.Equal(t, "generated", source2)
	pub2, err := signer2.PublicKeySPKIBase64()
	require.NoError(t, err)
	require.Equal(t, pub1, pub2, "重启后公钥必须不变")
}

// TestResolveManifestSignerWithAutogen_DevFallback 开发态未注入维持回退内置开发密钥，来源 dev，
// 不生成文件（保开发端到端验签，公钥已回填 updater-core）。
func TestResolveManifestSignerWithAutogen_DevFallback(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "should-not-exist.pem")

	signer, source, err := ResolveManifestSignerWithAutogen("", "", true, keyPath)
	require.NoError(t, err)
	require.NotNil(t, signer)
	require.Equal(t, "dev", source)

	pub, err := signer.PublicKeySPKIBase64()
	require.NoError(t, err)
	require.Equal(t, DevSignPublicKeySPKIBase64, pub)

	_, statErr := os.Stat(keyPath)
	require.True(t, os.IsNotExist(statErr), "dev 回退态不应生成密钥文件")
}

// TestResolveManifestSignerWithAutogen_ProdDevKeyInjectedRejected 生产态显式注入源码公开的开发密钥
// 仍被拒（ErrDevSignKeyInProd 防线保留），不因引入自动生成而松动。
func TestResolveManifestSignerWithAutogen_ProdDevKeyInjectedRejected(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "should-not-exist.pem")

	signer, source, err := ResolveManifestSignerWithAutogen(DevSignPrivateKeyPKCS8Base64, DefaultSignKeyID, false, keyPath)
	require.ErrorIs(t, err, ErrDevSignKeyInProd)
	require.Nil(t, signer)
	require.Empty(t, source)

	_, statErr := os.Stat(keyPath)
	require.True(t, os.IsNotExist(statErr), "被拒时不应生成密钥文件")
}

// TestResolveManifestSignerWithAutogen_InvalidEnvKeyPropagates 注入非法私钥透传解析错误，
// 不静默回退到自动生成（配错快失败）。
func TestResolveManifestSignerWithAutogen_InvalidEnvKeyPropagates(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "should-not-exist.pem")

	signer, source, err := ResolveManifestSignerWithAutogen("not-base64-!!!", "k1", false, keyPath)
	require.ErrorIs(t, err, ErrInvalidSignKey)
	require.Nil(t, signer)
	require.Empty(t, source)

	_, statErr := os.Stat(keyPath)
	require.True(t, os.IsNotExist(statErr), "注入非法私钥不应触发自动生成")
}
