package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newGuardDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ClientIPRule{}))
	return db
}

// TestClientIPGuard_DenyBlocks 黑名单：无规则全放行，加 deny 后命中 IP 拒、其它放行。
func TestClientIPGuard_DenyBlocks(t *testing.T) {
	g := NewClientIPGuardService(newGuardDB(t))
	require.True(t, g.Allowed("1.2.3.4"), "无规则应放行")

	_, err := g.AddRule("1.2.3.4", "deny", "abuse", 1)
	require.NoError(t, err)
	require.False(t, g.Allowed("1.2.3.4"), "deny 命中应拒")
	require.True(t, g.Allowed("5.6.7.8"), "未命中 deny 应放行")
}

// TestClientIPGuard_CIDRDeny CIDR 网段 deny。
func TestClientIPGuard_CIDRDeny(t *testing.T) {
	g := NewClientIPGuardService(newGuardDB(t))
	_, err := g.AddRule("10.0.0.0/8", "deny", "", 1)
	require.NoError(t, err)
	require.False(t, g.Allowed("10.1.2.3"))
	require.True(t, g.Allowed("11.0.0.1"))
}

// TestClientIPGuard_AllowlistMode 白名单模式：有 allow 规则则仅白名单内放行。
func TestClientIPGuard_AllowlistMode(t *testing.T) {
	g := NewClientIPGuardService(newGuardDB(t))
	_, err := g.AddRule("10.0.0.0/8", "allow", "", 1)
	require.NoError(t, err)
	require.True(t, g.Allowed("10.1.2.3"), "白名单内放行")
	require.False(t, g.Allowed("1.2.3.4"), "白名单模式下非白名单 IP 拒")
}

// TestClientIPGuard_DenyWinsInAllowlist 白名单模式下 deny 仍优先。
func TestClientIPGuard_DenyWinsInAllowlist(t *testing.T) {
	g := NewClientIPGuardService(newGuardDB(t))
	_, err := g.AddRule("10.0.0.0/8", "allow", "", 1)
	require.NoError(t, err)
	_, err = g.AddRule("10.0.0.5", "deny", "", 1)
	require.NoError(t, err)
	require.False(t, g.Allowed("10.0.0.5"), "deny 优先于 allow")
	require.True(t, g.Allowed("10.0.0.6"))
}

// TestClientIPGuard_AddRemoveReload 增删运行时即时生效。
func TestClientIPGuard_AddRemoveReload(t *testing.T) {
	g := NewClientIPGuardService(newGuardDB(t))
	r, err := g.AddRule("9.9.9.9", "deny", "", 1)
	require.NoError(t, err)
	require.False(t, g.Allowed("9.9.9.9"))
	require.NoError(t, g.RemoveRule(r.ID))
	require.True(t, g.Allowed("9.9.9.9"), "删除规则后恢复放行")
	require.ErrorIs(t, g.RemoveRule(99999), ErrIPRuleNotFound)
}

// TestClientIPGuard_InvalidRule 非法 CIDR/mode 拒绝。
func TestClientIPGuard_InvalidRule(t *testing.T) {
	g := NewClientIPGuardService(newGuardDB(t))
	_, err := g.AddRule("not-an-ip", "deny", "", 1)
	require.ErrorIs(t, err, ErrInvalidIPRule)
	_, err = g.AddRule("1.2.3.4", "bogus", "", 1)
	require.ErrorIs(t, err, ErrInvalidIPRule)
}
