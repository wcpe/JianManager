package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/httpclient"
)

// fixedGlobalProxy 返回固定全局默认代理，模拟 settings.EffectiveProxy。
func fixedGlobalProxy(url, noProxy string) func() httpclient.Config {
	return func() httpclient.Config { return httpclient.Config{URL: url, NoProxy: noProxy} }
}

// TestNodeProxy_InheritUsesGlobalDefault 节点 inherit 时期望代理 = 全局默认。
func TestNodeProxy_InheritUsesGlobalDefault(t *testing.T) {
	db := newNodeTestDB(t)
	svc := NewNodeProxyService(db, fixedGlobalProxy("http://global:7890", "localhost"))
	node := newTestNode(t, db, "n1") // 默认 inherit

	eff := svc.EffectiveNodeProxy(node)
	require.Equal(t, "http://global:7890", eff.URL)
	require.Equal(t, "localhost", eff.NoProxy)
	// generation 应与全局默认一致（同一份配置）。
	require.Equal(t, httpclient.ProxyGeneration(httpclient.Config{URL: "http://global:7890", NoProxy: "localhost"}),
		svc.NodeProxyGeneration(node))
}

// TestNodeProxy_CustomOverridesGlobal 节点 custom 时期望代理 = 节点自定义（压过全局）。
func TestNodeProxy_CustomOverridesGlobal(t *testing.T) {
	db := newNodeTestDB(t)
	svc := NewNodeProxyService(db, fixedGlobalProxy("http://global:7890", ""))
	node := newTestNode(t, db, "n1")

	updated, err := svc.UpdateNodeProxy(node.ID, "custom", "socks5://10.0.0.1:1080", "192.168.0.0/16")
	require.NoError(t, err)
	require.Equal(t, "custom", updated.ProxyMode)

	eff := svc.EffectiveNodeProxy(updated)
	require.Equal(t, "socks5://10.0.0.1:1080", eff.URL)
	require.Equal(t, "192.168.0.0/16", eff.NoProxy)
}

// TestNodeProxy_BackToInherit 节点改回 inherit 后恢复用全局默认。
func TestNodeProxy_BackToInherit(t *testing.T) {
	db := newNodeTestDB(t)
	svc := NewNodeProxyService(db, fixedGlobalProxy("http://global:7890", ""))
	node := newTestNode(t, db, "n1")

	_, err := svc.UpdateNodeProxy(node.ID, "custom", "http://node:8080", "")
	require.NoError(t, err)
	updated, err := svc.UpdateNodeProxy(node.ID, "inherit", "", "")
	require.NoError(t, err)

	eff := svc.EffectiveNodeProxy(updated)
	require.Equal(t, "http://global:7890", eff.URL)
}

// TestNodeProxy_ValidatesCustomURL custom 模式非法 URL 被拒、不落库。
func TestNodeProxy_ValidatesCustomURL(t *testing.T) {
	db := newNodeTestDB(t)
	svc := NewNodeProxyService(db, fixedGlobalProxy("", ""))
	node := newTestNode(t, db, "n1")

	_, err := svc.UpdateNodeProxy(node.ID, "custom", "ftp://bad:1", "")
	require.ErrorIs(t, err, ErrSettingValueInvalid)

	// custom 但空 URL 也非法（custom 必须给地址，否则该用 inherit）。
	_, err = svc.UpdateNodeProxy(node.ID, "custom", "", "")
	require.ErrorIs(t, err, ErrSettingValueInvalid)

	var fromDB model.Node
	require.NoError(t, db.First(&fromDB, node.ID).Error)
	require.Equal(t, "inherit", fromDB.ProxyMode) // 未被改动
}

// TestNodeProxy_RejectsUnknownMode 非法 mode 被拒。
func TestNodeProxy_RejectsUnknownMode(t *testing.T) {
	db := newNodeTestDB(t)
	svc := NewNodeProxyService(db, fixedGlobalProxy("", ""))
	node := newTestNode(t, db, "n1")

	_, err := svc.UpdateNodeProxy(node.ID, "bogus", "", "")
	require.ErrorIs(t, err, ErrSettingValueInvalid)
}

// TestNodeProxy_InheritClearsCustomFields 改回 inherit 时清空残留的 custom 字段（避免脏数据下发）。
func TestNodeProxy_InheritClearsCustomFields(t *testing.T) {
	db := newNodeTestDB(t)
	svc := NewNodeProxyService(db, fixedGlobalProxy("", ""))
	node := newTestNode(t, db, "n1")

	_, err := svc.UpdateNodeProxy(node.ID, "custom", "http://node:8080", "localhost")
	require.NoError(t, err)
	_, err = svc.UpdateNodeProxy(node.ID, "inherit", "", "")
	require.NoError(t, err)

	var fromDB model.Node
	require.NoError(t, db.First(&fromDB, node.ID).Error)
	require.Empty(t, fromDB.ProxyURL)
	require.Empty(t, fromDB.ProxyNoProxy)
}

// TestNodeProxy_ViewSanitizes 对外视图脱敏 custom 代理 URL 的凭据。
func TestNodeProxy_ViewSanitizes(t *testing.T) {
	db := newNodeTestDB(t)
	svc := NewNodeProxyService(db, fixedGlobalProxy("http://user:secret@global:7890", ""))
	node := newTestNode(t, db, "n1")
	_, err := svc.UpdateNodeProxy(node.ID, "custom", "http://nu:np@node:8080", "")
	require.NoError(t, err)

	view, err := svc.NodeProxyView(node.ID)
	require.NoError(t, err)
	require.Equal(t, "custom", view.Mode)
	require.NotContains(t, view.URL, "np")            // 节点 URL 脱敏
	require.Contains(t, view.URL, "node:8080")
	require.NotContains(t, view.EffectiveURL, "np")   // 生效 URL 脱敏
	require.NotContains(t, view.GlobalDefaultURL, "secret") // 全局默认脱敏
}
