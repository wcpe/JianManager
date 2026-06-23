package service

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newClientChannelSvc(t *testing.T) *ClientChannelService {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ClientChannel{}, &model.ClientPullKey{}))
	return NewClientChannelService(db)
}

func sha256hexStr(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestCreateChannel_ValidatesSlugAndUniqueness(t *testing.T) {
	svc := newClientChannelSvc(t)

	ch, err := svc.CreateChannel("skyblock-s1", "空岛一服", "desc")
	require.NoError(t, err)
	require.Equal(t, "skyblock-s1", ch.ChannelID)
	require.Equal(t, 0, ch.CurrentVersion)

	// 非法 slug 拒绝。
	_, err = svc.CreateChannel("Bad_Slug", "x", "")
	require.ErrorIs(t, err, ErrInvalidChannelID)

	// 重复 channelId 拒绝。
	_, err = svc.CreateChannel("skyblock-s1", "重复", "")
	require.ErrorIs(t, err, ErrChannelExists)
}

func TestCreateKey_StoresOnlyHashAndReturnsPlaintextOnce(t *testing.T) {
	svc := newClientChannelSvc(t)
	_, err := svc.CreateChannel("skyblock-s1", "空岛一服", "")
	require.NoError(t, err)

	key, plaintext, err := svc.CreateKey("skyblock-s1", "正式包", nil)
	require.NoError(t, err)

	// 明文非空、带约定前缀，且与前缀字段一致。
	require.NotEmpty(t, plaintext)
	require.True(t, strings.HasPrefix(plaintext, "jmck_"))
	require.True(t, strings.HasPrefix(plaintext, key.KeyPrefix))

	// 落库只存哈希，且哈希 = SHA-256(明文)；库内绝不含明文。
	require.Equal(t, sha256hexStr(plaintext), key.KeyHash)
	require.NotContains(t, key.KeyHash, plaintext)

	// 二次读取（列表/详情）不返回明文：JSON 序列化里 KeyHash 打了 json:"-"，且无明文字段。
	keys, err := svc.ListKeys("skyblock-s1")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.Equal(t, key.ID, keys[0].ID)
	require.Equal(t, key.KeyPrefix, keys[0].KeyPrefix)
}

func TestVerifyKey_HappyPath(t *testing.T) {
	svc := newClientChannelSvc(t)
	_, err := svc.CreateChannel("skyblock-s1", "空岛一服", "")
	require.NoError(t, err)
	_, plaintext, err := svc.CreateKey("skyblock-s1", "正式包", nil)
	require.NoError(t, err)

	got, err := svc.VerifyKey("skyblock-s1", plaintext)
	require.NoError(t, err)
	require.Equal(t, "skyblock-s1", got.ChannelID)

	// last_used_at 被刷新。
	keys, err := svc.ListKeys("skyblock-s1")
	require.NoError(t, err)
	require.NotNil(t, keys[0].LastUsedAt)
}

func TestVerifyKey_WrongKeyAndWrongChannel(t *testing.T) {
	svc := newClientChannelSvc(t)
	_, err := svc.CreateChannel("skyblock-s1", "空岛一服", "")
	require.NoError(t, err)
	_, err = svc.CreateChannel("other", "另一服", "")
	require.NoError(t, err)
	_, plaintext, err := svc.CreateKey("skyblock-s1", "正式包", nil)
	require.NoError(t, err)

	// 错误明文拒绝。
	_, err = svc.VerifyKey("skyblock-s1", "jmck_wrongwrongwrong")
	require.ErrorIs(t, err, ErrPullKeyInvalid)

	// 正确明文但频道不匹配（密钥属于 skyblock-s1，拿去 other 用）→ 拒绝。
	_, err = svc.VerifyKey("other", plaintext)
	require.ErrorIs(t, err, ErrPullKeyInvalid)
}

func TestRevokeKey_InvalidatesVerification(t *testing.T) {
	svc := newClientChannelSvc(t)
	_, err := svc.CreateChannel("skyblock-s1", "空岛一服", "")
	require.NoError(t, err)
	key, plaintext, err := svc.CreateKey("skyblock-s1", "正式包", nil)
	require.NoError(t, err)

	// 吊销前可用。
	_, err = svc.VerifyKey("skyblock-s1", plaintext)
	require.NoError(t, err)

	require.NoError(t, svc.RevokeKey("skyblock-s1", key.ID))

	// 吊销后立即失效。
	_, err = svc.VerifyKey("skyblock-s1", plaintext)
	require.ErrorIs(t, err, ErrPullKeyInvalid)

	// 记录仍在（保留 + revoked 标记 + revokedAt）。
	keys, err := svc.ListKeys("skyblock-s1")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.True(t, keys[0].Revoked)
	require.NotNil(t, keys[0].RevokedAt)
}

func TestRotateKey_NewPlaintextOldInvalidatedSameRecord(t *testing.T) {
	svc := newClientChannelSvc(t)
	_, err := svc.CreateChannel("skyblock-s1", "空岛一服", "")
	require.NoError(t, err)
	key, oldPlain, err := svc.CreateKey("skyblock-s1", "正式包", nil)
	require.NoError(t, err)

	rotated, newPlain, err := svc.RotateKey("skyblock-s1", key.ID)
	require.NoError(t, err)

	// 同一条记录（id 不变），明文已变。
	require.Equal(t, key.ID, rotated.ID)
	require.NotEqual(t, oldPlain, newPlain)
	require.Equal(t, sha256hexStr(newPlain), rotated.KeyHash)

	// 旧明文失效，新明文可用。
	_, err = svc.VerifyKey("skyblock-s1", oldPlain)
	require.ErrorIs(t, err, ErrPullKeyInvalid)
	_, err = svc.VerifyKey("skyblock-s1", newPlain)
	require.NoError(t, err)

	// 列表仍只有一条（轮换不新增记录）。
	keys, err := svc.ListKeys("skyblock-s1")
	require.NoError(t, err)
	require.Len(t, keys, 1)
}

func TestVerifyKey_Expired(t *testing.T) {
	svc := newClientChannelSvc(t)
	_, err := svc.CreateChannel("skyblock-s1", "空岛一服", "")
	require.NoError(t, err)
	past := time.Now().Add(-time.Hour)
	_, plaintext, err := svc.CreateKey("skyblock-s1", "临时", &past)
	require.NoError(t, err)

	_, err = svc.VerifyKey("skyblock-s1", plaintext)
	require.ErrorIs(t, err, ErrPullKeyInvalid)
}

func TestDeleteChannel_CascadesKeys(t *testing.T) {
	svc := newClientChannelSvc(t)
	_, err := svc.CreateChannel("skyblock-s1", "空岛一服", "")
	require.NoError(t, err)
	_, _, err = svc.CreateKey("skyblock-s1", "正式包", nil)
	require.NoError(t, err)

	require.NoError(t, svc.DeleteChannel("skyblock-s1"))

	_, err = svc.GetChannel("skyblock-s1")
	require.ErrorIs(t, err, ErrChannelNotFound)

	// 频道删除后其密钥不再可列。
	_, err = svc.ListKeys("skyblock-s1")
	require.ErrorIs(t, err, ErrChannelNotFound)
}

func TestUpdateChannel(t *testing.T) {
	svc := newClientChannelSvc(t)
	_, err := svc.CreateChannel("skyblock-s1", "旧名", "旧描述")
	require.NoError(t, err)

	ch, err := svc.UpdateChannel("skyblock-s1", "新名", "新描述")
	require.NoError(t, err)
	require.Equal(t, "新名", ch.Name)
	require.Equal(t, "新描述", ch.Description)
	// channelId 不可改。
	require.Equal(t, "skyblock-s1", ch.ChannelID)
}
