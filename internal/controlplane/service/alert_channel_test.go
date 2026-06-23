package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestAlertChannel_CRUD(t *testing.T) {
	db := newAlertTestDB(t)
	svc := NewAlertChannelService(db)

	// 创建（dingtalk，URL 经 ${ENV} 引用）。
	ch, err := svc.Create(ChannelRequest{
		Name:   "ops-dingtalk",
		Type:   model.ChannelTypeDingtalk,
		Config: ChannelConfig{URL: "${JM_DING_URL}"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, ch.UUID)
	assert.True(t, ch.Enabled)
	assert.Contains(t, ch.Config, "JM_DING_URL")

	// 明文 URL 被拒。
	_, err = svc.Create(ChannelRequest{Name: "bad", Type: model.ChannelTypeDingtalk,
		Config: ChannelConfig{URL: "https://oapi.dingtalk.com/robot/send?access_token=x"}})
	require.ErrorIs(t, err, ErrCredentialNotEnvRef)

	// 列表。
	list, err := svc.List()
	require.NoError(t, err)
	require.Len(t, list, 1)

	// 更新为禁用。
	off := false
	updated, err := svc.Update(ch.ID, ChannelRequest{Name: "ops-dingtalk", Type: model.ChannelTypeDingtalk,
		Enabled: &off, Config: ChannelConfig{URL: "${JM_DING_URL}"}})
	require.NoError(t, err)
	assert.False(t, updated.Enabled)

	// 删除。
	require.NoError(t, svc.Delete(ch.ID))
	_, err = svc.Get(ch.ID)
	require.ErrorIs(t, err, ErrAlertChannelNotFound)
}

func TestAlertChannel_CreateDisabledPersists(t *testing.T) {
	db := newAlertTestDB(t)
	svc := NewAlertChannelService(db)
	off := false
	ch, err := svc.Create(ChannelRequest{Name: "off", Type: model.ChannelTypeInApp, Enabled: &off})
	require.NoError(t, err)
	// 重新读取确认禁用真正落库（GORM default:true 回写修正）。
	got, err := svc.Get(ch.ID)
	require.NoError(t, err)
	assert.False(t, got.Enabled)
}

func TestAlertChannel_DeleteRejectedWhenReferenced(t *testing.T) {
	db := newAlertTestDB(t)
	svc := NewAlertChannelService(db)
	ch, err := svc.Create(ChannelRequest{Name: "wh", Type: model.ChannelTypeWebhook,
		Config: ChannelConfig{URL: "${JM_WH}"}})
	require.NoError(t, err)

	// 一条规则引用该通道。
	rule := &model.AlertRule{Name: "r", TargetType: "node", ChannelIDs: "[" + itoa(ch.ID) + "]"}
	require.NoError(t, db.Create(rule).Error)

	err = svc.Delete(ch.ID)
	require.ErrorIs(t, err, ErrAlertChannelInUse)
}
