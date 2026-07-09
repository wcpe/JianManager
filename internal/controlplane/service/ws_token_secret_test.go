package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

// wsSecretFilePath 在临时数据根下拼出 etc/ws-token-secret.key 路径（FR-275）。
func wsSecretFilePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "etc", wsTokenSecretFileName)
}

func TestResolveWSTokenSecret_ExplicitWins(t *testing.T) {
	// 显式配置非空 → 优先于 dev 回退与自动生成，不生成/不写文件（过渡逃生口，见 ADR-061 决策 2.1）。
	path := wsSecretFilePath(t)
	secret, src, err := ResolveWSTokenSecret("  my-explicit-secret  ", true, path)
	require.NoError(t, err)
	require.Equal(t, WSTokenSecretSourceExplicit, src)
	require.Equal(t, "my-explicit-secret", secret)

	_, statErr := os.Stat(path)
	require.True(t, os.IsNotExist(statErr))
}

func TestResolveWSTokenSecret_DevFallback(t *testing.T) {
	// dev_mode 未配 → 回退固定开发值（与 CP/Worker 现状默认一致，保 dev 零配置连续性），不写文件。
	path := wsSecretFilePath(t)
	secret, src, err := ResolveWSTokenSecret("", true, path)
	require.NoError(t, err)
	require.Equal(t, WSTokenSecretSourceDev, src)
	require.Equal(t, DevWSTokenSecret, secret)

	_, statErr := os.Stat(path)
	require.True(t, os.IsNotExist(statErr))
}

func TestDevWSTokenSecret_MatchesLegacyDefault(t *testing.T) {
	// dev 回退值必须与既有两端默认（dev-secret-change-me）一致：
	// 存量 dev 部署与探针长期 token 才不因升级失效（ADR-061 决策 2.3）。
	require.Equal(t, "dev-secret-change-me", DevWSTokenSecret)
}

func TestResolveWSTokenSecret_AutogenPersistsAndStable(t *testing.T) {
	// 生产未配 → 自动生成并持久化；同一文件再次解析返回同一密钥（跨重启稳定）。
	path := wsSecretFilePath(t)
	secret1, src1, err := ResolveWSTokenSecret("", false, path)
	require.NoError(t, err)
	require.Equal(t, WSTokenSecretSourceGenerated, src1)
	require.NotEmpty(t, secret1)

	// 文件已落盘且内容即密钥。注：0600 在 Windows 语义有限，不跨平台硬断言权限值（同 FR-263 测试）。
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, secret1, string(data))

	secret2, src2, err := ResolveWSTokenSecret("", false, path)
	require.NoError(t, err)
	require.Equal(t, WSTokenSecretSourceGenerated, src2)
	require.Equal(t, secret1, secret2)
}

func TestResolveWSTokenSecret_AutogenRandomPerDeploy(t *testing.T) {
	// 不同数据根各自生成独立随机密钥（每部署一把），且不得等于 dev 回退值。
	s1, _, err := ResolveWSTokenSecret("", false, wsSecretFilePath(t))
	require.NoError(t, err)
	s2, _, err := ResolveWSTokenSecret("", false, wsSecretFilePath(t))
	require.NoError(t, err)
	require.NotEqual(t, s1, s2)
	require.NotEqual(t, DevWSTokenSecret, s1)
}

func TestResolveWSTokenSecret_EmptyPersistedFileFailsClosed(t *testing.T) {
	// 已存在但为空的密钥文件 → fail-fast 且不覆盖现场（ADR-061 决策 2：不静默轮换掩盖问题）。
	path := wsSecretFilePath(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("   \n"), 0o600))

	secret, src, err := ResolveWSTokenSecret("", false, path)
	require.Error(t, err)
	require.Empty(t, secret)
	require.Empty(t, src)
}

// TestWSTokenSecret_IsolatedFromUserSessionSecret 密钥隔离契约（FR-275 验收 #2，见 ADR-061 决策 1）：
// 终端令牌用注入的专用 WS 密钥签发——jwt.secret 校验必须不过（轮换 jwt.secret 不影响终端；
// 反向亦然：Worker 仅持 WS 密钥，伪造不了会话类令牌）。签发点与 jwt.secret 的分离由
// cmd/control-plane/main.go 装配保证（TerminalService/TerminalProxy/PluginBridgeService
// 传 wsTokenSecret，AuthService/路由中间件传 cfg.JWT.Secret），本测试钉住签发侧契约。
func TestWSTokenSecret_IsolatedFromUserSessionSecret(t *testing.T) {
	db, instanceID := newTerminalTestDB(t, "ws://127.0.0.1:1")
	svc := NewTerminalService(db, "dedicated-ws-secret", "ws://fallback.invalid")
	tok, err := svc.IssueToken(instanceID, "read", "", false)
	require.NoError(t, err)

	parseWith := func(secret string) error {
		_, perr := jwt.Parse(tok.Token, func(t *jwt.Token) (interface{}, error) { return []byte(secret), nil })
		return perr
	}
	require.NoError(t, parseWith("dedicated-ws-secret"), "专用 WS 密钥应能校验终端令牌")
	require.Error(t, parseWith("user-session-jwt-secret"), "jwt.secret 不得能校验终端令牌（密钥隔离）")
}

func TestResolveWSTokenSecret_PersistFailureReturnsError(t *testing.T) {
	// 生成后持久化失败 → 返回错误（装配层 fail-fast，绝不回退 jwt.secret）。
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))
	badPath := filepath.Join(blocker, "etc", wsTokenSecretFileName)

	secret, src, err := ResolveWSTokenSecret("", false, badPath)
	require.Error(t, err)
	require.Empty(t, secret)
	require.Empty(t, src)
}
