package service

import (
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

// randSecretB64 生成一把合法的 32 字节 base64 加密密钥（测试用）。
func randSecretB64(t *testing.T) string {
	t.Helper()
	b := make([]byte, keyEncSecretLen)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(b)
}

func TestKeyEncryptor_RoundTrip(t *testing.T) {
	enc, dev, err := ResolveKeyEncryptor(randSecretB64(t), false)
	require.NoError(t, err)
	require.False(t, dev)
	require.NotNil(t, enc)

	plain := "jmck_abcdefg1234567890_round_trip"
	ct, err := enc.Encrypt(plain)
	require.NoError(t, err)
	require.NotEmpty(t, ct)
	// 密文不得是明文（已加密）。
	require.NotContains(t, ct, plain)

	// 每次加密 nonce 随机：同明文两次密文不同（语义安全）。
	ct2, err := enc.Encrypt(plain)
	require.NoError(t, err)
	require.NotEqual(t, ct, ct2)

	got, err := enc.Decrypt(ct)
	require.NoError(t, err)
	require.Equal(t, plain, got)
	got2, err := enc.Decrypt(ct2)
	require.NoError(t, err)
	require.Equal(t, plain, got2)
}

func TestKeyEncryptor_DecryptRejectsTampered(t *testing.T) {
	enc, _, err := ResolveKeyEncryptor(randSecretB64(t), false)
	require.NoError(t, err)

	ct, err := enc.Encrypt("jmck_secret")
	require.NoError(t, err)

	// 篡改密文（翻转最后一个 base64 字符）→ GCM 认证失败。
	raw, err := base64.StdEncoding.DecodeString(ct)
	require.NoError(t, err)
	raw[len(raw)-1] ^= 0xFF
	tampered := base64.StdEncoding.EncodeToString(raw)

	_, err = enc.Decrypt(tampered)
	require.Error(t, err)
}

func TestKeyEncryptor_DecryptWrongKeyFails(t *testing.T) {
	enc1, _, err := ResolveKeyEncryptor(randSecretB64(t), false)
	require.NoError(t, err)
	enc2, _, err := ResolveKeyEncryptor(randSecretB64(t), false)
	require.NoError(t, err)

	ct, err := enc1.Encrypt("jmck_secret")
	require.NoError(t, err)

	// 换一把密钥解不开（轮换 env 密钥后旧密文不可读，spec §6 风险）。
	_, err = enc2.Decrypt(ct)
	require.Error(t, err)
}

func TestResolveKeyEncryptor_DevFallbackWhenUnset(t *testing.T) {
	// dev_mode=true 且未注入 → 回退内置 dev 密钥，可用。
	enc, dev, err := ResolveKeyEncryptor("", true)
	require.NoError(t, err)
	require.True(t, dev)
	require.NotNil(t, enc)

	ct, err := enc.Encrypt("jmck_dev")
	require.NoError(t, err)
	got, err := enc.Decrypt(ct)
	require.NoError(t, err)
	require.Equal(t, "jmck_dev", got)
}

func TestResolveKeyEncryptor_ProdUnsetDegradesToNil(t *testing.T) {
	// 生产态未注入 → 优雅降级：返回 nil encryptor、不报错、不阻断（不写 KeyEnc）。
	enc, dev, err := ResolveKeyEncryptor("", false)
	require.NoError(t, err)
	require.False(t, dev)
	require.Nil(t, enc)
}

func TestResolveKeyEncryptor_InvalidSecretRejected(t *testing.T) {
	// 注入了非法密钥（非 base64 / 长度不对）→ 报错（想用却配错，快失败让运维修正）。
	_, _, err := ResolveKeyEncryptor("not-base64-!!!", false)
	require.Error(t, err)

	// 长度不足 32 字节也拒绝。
	short := base64.StdEncoding.EncodeToString([]byte("too-short"))
	_, _, err = ResolveKeyEncryptor(short, false)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidKeyEncSecret)
}

func TestKeyEncryptor_NilSafe(t *testing.T) {
	// nil encryptor 表示「未配置」：Encrypt 返回空串无错（调用方据此不写 KeyEnc），Decrypt 报未配置。
	var enc *KeyEncryptor
	ct, err := enc.Encrypt("x")
	require.NoError(t, err)
	require.Empty(t, ct)

	_, err = enc.Decrypt("anything")
	require.ErrorIs(t, err, ErrKeyEncNotConfigured)
}

func TestDevKeyEncSecret_IsValidBase64Len(t *testing.T) {
	// 内置 dev 密钥常量必须是合法的 32 字节 base64（否则 dev 回退会崩）。
	raw, err := base64.StdEncoding.DecodeString(DevKeyEncSecretBase64)
	require.NoError(t, err)
	require.Len(t, raw, keyEncSecretLen)
}
