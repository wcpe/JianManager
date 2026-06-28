package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// newClientChannelSvcWithEnc 建一个配了加密器的频道服务（FR-192）。
func newClientChannelSvcWithEnc(t *testing.T) (*ClientChannelService, *KeyEncryptor) {
	t.Helper()
	svc := newClientChannelSvc(t)
	enc, _, err := ResolveKeyEncryptor(randSecretB64(t), false)
	require.NoError(t, err)
	require.NotNil(t, enc)
	svc.SetKeyEncryptor(enc)
	return svc, enc
}

func TestCreateKey_WritesKeyEncWhenEncryptorSet(t *testing.T) {
	svc, enc := newClientChannelSvcWithEnc(t)
	_, err := svc.CreateChannel("skyblock-s1", "空岛一服", "")
	require.NoError(t, err)

	key, plaintext, err := svc.CreateKey("skyblock-s1", "正式包", nil)
	require.NoError(t, err)

	// 落库 KeyEnc 非空，且解密回得到原明文（鉴权仍只用 KeyHash，行为不变）。
	require.NotEmpty(t, key.KeyEnc)
	require.Equal(t, sha256hexStr(plaintext), key.KeyHash)
	got, err := enc.Decrypt(key.KeyEnc)
	require.NoError(t, err)
	require.Equal(t, plaintext, got)

	// RevealKey 经服务返回明文。
	revealed, err := svc.RevealKey("skyblock-s1", key.ID)
	require.NoError(t, err)
	require.Equal(t, plaintext, revealed)
}

func TestRotateKey_RewritesKeyEnc(t *testing.T) {
	svc, _ := newClientChannelSvcWithEnc(t)
	_, err := svc.CreateChannel("skyblock-s1", "空岛一服", "")
	require.NoError(t, err)
	key, _, err := svc.CreateKey("skyblock-s1", "正式包", nil)
	require.NoError(t, err)

	before, err := svc.RevealKey("skyblock-s1", key.ID)
	require.NoError(t, err)

	_, newPlain, err := svc.RotateKey("skyblock-s1", key.ID)
	require.NoError(t, err)

	// 轮换后 RevealKey 返回新明文（旧明文已被覆盖）。
	after, err := svc.RevealKey("skyblock-s1", key.ID)
	require.NoError(t, err)
	require.Equal(t, newPlain, after)
	require.NotEqual(t, before, after)
}

func TestRevealKey_NotRevealableWhenNoEnc(t *testing.T) {
	// 未配加密器（生产态降级）：建密钥不写 KeyEnc，Reveal 返回 ErrPullKeyNotRevealable。
	svc := newClientChannelSvc(t) // 无 encryptor
	_, err := svc.CreateChannel("skyblock-s1", "空岛一服", "")
	require.NoError(t, err)
	key, _, err := svc.CreateKey("skyblock-s1", "正式包", nil)
	require.NoError(t, err)
	require.Empty(t, key.KeyEnc)

	_, err = svc.RevealKey("skyblock-s1", key.ID)
	require.ErrorIs(t, err, ErrPullKeyNotRevealable)
}

func TestRevealKey_OldHashOnlyKeyNotRevealable(t *testing.T) {
	// 存量老密钥（只有 KeyHash、KeyEnc 空）即便此刻已配加密器也不可查（哈希单向救不回）。
	svc, _ := newClientChannelSvcWithEnc(t)
	_, err := svc.CreateChannel("skyblock-s1", "空岛一服", "")
	require.NoError(t, err)
	key, _, err := svc.CreateKey("skyblock-s1", "正式包", nil)
	require.NoError(t, err)

	// 模拟存量：清空 KeyEnc。
	require.NoError(t, svc.db.Model(key).Update("key_enc", "").Error)

	_, err = svc.RevealKey("skyblock-s1", key.ID)
	require.ErrorIs(t, err, ErrPullKeyNotRevealable)
}

func TestRevealKey_ChannelAndKeyNotFound(t *testing.T) {
	svc, _ := newClientChannelSvcWithEnc(t)
	// 频道不存在。
	_, err := svc.RevealKey("nope", 1)
	require.ErrorIs(t, err, ErrChannelNotFound)

	_, err = svc.CreateChannel("skyblock-s1", "空岛一服", "")
	require.NoError(t, err)
	// 密钥不存在。
	_, err = svc.RevealKey("skyblock-s1", 9999)
	require.ErrorIs(t, err, ErrPullKeyNotFound)
}
