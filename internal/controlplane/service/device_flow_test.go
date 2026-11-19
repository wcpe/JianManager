package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// FR-562 / RFC 8628：设备授权状态机与一次性取用语义。

func newDeviceFlow(t *testing.T) (*DeviceFlowService, *UserTokenService, *model.User) {
	t.Helper()
	db := setupUserTokenDB(t)
	userTok := NewUserTokenService(db, nil)
	flow := NewDeviceFlowService(db, userTok)
	user := seedUser(t, db, "dev-user", model.RoleMember)
	return flow, userTok, user
}

// bypassPollInterval 清掉轮询间隔限制，让测试不必真的等待 5 秒。
func bypassPollInterval(f *DeviceFlowService, deviceCode string) {
	delete(f.pollGuard, deviceCode)
}

func TestDeviceFlow_FullHappyPath(t *testing.T) {
	flow, userTok, user := newDeviceFlow(t)

	start, err := flow.Start("laptop", "https://cp.example.com/device")
	require.NoError(t, err)
	assert.NotEmpty(t, start.DeviceCode)
	assert.NotEmpty(t, start.UserCode)
	assert.Equal(t, 600, start.ExpiresIn)
	assert.Equal(t, 5, start.Interval)
	assert.Contains(t, start.UserCode, "-", "用户码应带连字符便于输入")

	// 用户尚未确认 → pending。
	bypassPollInterval(flow, start.DeviceCode)
	_, err = flow.Poll(start.DeviceCode)
	assert.ErrorIs(t, err, ErrDeviceAuthorizationPending)

	// 浏览器确认。
	require.NoError(t, flow.Approve(start.UserCode, user.ID))

	// CLI 取回明文。
	bypassPollInterval(flow, start.DeviceCode)
	plaintext, err := flow.Poll(start.DeviceCode)
	require.NoError(t, err)
	assert.Contains(t, plaintext, UserTokenPrefix)

	// 明文可用。
	p, err := userTok.Authenticate(plaintext)
	require.NoError(t, err)
	assert.Equal(t, user.ID, p.UserID)

	// 只能取一次。
	bypassPollInterval(flow, start.DeviceCode)
	_, err = flow.Poll(start.DeviceCode)
	assert.ErrorIs(t, err, ErrDeviceCodeConsumed)
}

func TestDeviceFlow_SlowDown(t *testing.T) {
	flow, _, _ := newDeviceFlow(t)
	start, err := flow.Start("laptop", "https://cp/device")
	require.NoError(t, err)

	// 第一次轮询记录时间。
	_, err = flow.Poll(start.DeviceCode)
	assert.ErrorIs(t, err, ErrDeviceAuthorizationPending)

	// 立刻第二次 → slow_down。
	_, err = flow.Poll(start.DeviceCode)
	assert.ErrorIs(t, err, ErrDeviceSlowDown)
}

func TestDeviceFlow_Denied(t *testing.T) {
	flow, _, _ := newDeviceFlow(t)
	start, _ := flow.Start("", "https://cp/device")

	require.NoError(t, flow.Deny(start.UserCode))

	bypassPollInterval(flow, start.DeviceCode)
	_, err := flow.Poll(start.DeviceCode)
	assert.ErrorIs(t, err, ErrDeviceAccessDenied)
}

func TestDeviceFlow_Expired(t *testing.T) {
	db := setupUserTokenDB(t)
	userTok := NewUserTokenService(db, nil)
	flow := NewDeviceFlowService(db, userTok)

	start, _ := flow.Start("", "https://cp/device")
	// 把过期时间推到过去。
	require.NoError(t, db.Model(&model.DeviceCode{}).
		Where("device_code = ?", start.DeviceCode).
		Update("expires_at", time.Now().Add(-time.Minute)).Error)

	bypassPollInterval(flow, start.DeviceCode)
	_, err := flow.Poll(start.DeviceCode)
	assert.ErrorIs(t, err, ErrDeviceCodeExpired)
}

func TestDeviceFlow_ApproveExpiredFails(t *testing.T) {
	db := setupUserTokenDB(t)
	userTok := NewUserTokenService(db, nil)
	flow := NewDeviceFlowService(db, userTok)
	user := seedUser(t, db, "late", model.RoleMember)

	start, _ := flow.Start("", "https://cp/device")
	require.NoError(t, db.Model(&model.DeviceCode{}).
		Where("device_code = ?", start.DeviceCode).
		Update("expires_at", time.Now().Add(-time.Minute)).Error)

	err := flow.Approve(start.UserCode, user.ID)
	assert.ErrorIs(t, err, ErrDeviceCodeExpired)
}

func TestDeviceFlow_UnknownCode(t *testing.T) {
	flow, _, _ := newDeviceFlow(t)
	_, err := flow.Poll("nonexistent")
	assert.ErrorIs(t, err, ErrDeviceCodeNotFound)

	err = flow.Approve("ZZZZ-ZZZZ", 1)
	assert.ErrorIs(t, err, ErrDeviceCodeNotFound)
}

// TestDeviceFlow_UserCodeNormalization 用户输入不带连字符/小写也能识别。
func TestDeviceFlow_UserCodeNormalization(t *testing.T) {
	flow, _, user := newDeviceFlow(t)
	start, _ := flow.Start("", "https://cp/device")

	// 去掉连字符并转小写。
	raw := start.UserCode
	stripped := ""
	for _, r := range raw {
		if r != '-' {
			if r >= 'A' && r <= 'Z' {
				r = r - 'A' + 'a'
			}
			stripped += string(r)
		}
	}

	require.NoError(t, flow.Approve(stripped, user.ID), "归一化后应能识别")
}

// TestDeviceFlow_ApproveTwiceRejected 同一设备码不能确认两次。
func TestDeviceFlow_ApproveTwiceRejected(t *testing.T) {
	flow, _, user := newDeviceFlow(t)
	start, _ := flow.Start("", "https://cp/device")

	require.NoError(t, flow.Approve(start.UserCode, user.ID))
	err := flow.Approve(start.UserCode, user.ID)
	assert.ErrorIs(t, err, ErrDeviceCodeConsumed)
}

// TestDeviceFlow_IssuedTokenHasDeviceLabel 设备标识进入令牌名称，便于用户在列表识别。
func TestDeviceFlow_IssuedTokenHasDeviceLabel(t *testing.T) {
	flow, userTok, user := newDeviceFlow(t)
	start, _ := flow.Start("my-laptop", "https://cp/device")
	require.NoError(t, flow.Approve(start.UserCode, user.ID))

	tokens, err := userTok.List(user.ID)
	require.NoError(t, err)
	require.Len(t, tokens, 1)
	assert.Equal(t, "my-laptop", tokens[0].DeviceLabel)
	assert.Contains(t, tokens[0].Name, "my-laptop")
}

// TestUserCodeFormat 用户码格式：XXXX-XXXX，排除易混字符。
func TestUserCodeFormat(t *testing.T) {
	for i := 0; i < 50; i++ {
		code, err := randomUserCode()
		require.NoError(t, err)
		assert.Len(t, code, 9, "应为 4+1+4")
		assert.Equal(t, '-', rune(code[4]))
		for _, r := range code {
			if r == '-' {
				continue
			}
			assert.NotContains(t, "01OIL", string(r), "不应包含易混字符")
		}
	}
}
