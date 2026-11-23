package main

import (
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/platform/keyring"
)

// setTestConfigDir 把配置目录指向临时目录，避免测试污染真实 ~/.config。
func setTestConfigDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv(envURL, "")
	t.Setenv(envToken, "")
}

// TestParseGlobalArgs_Defaults 无 flag、无 env、无配置文件时用内置默认。
func TestParseGlobalArgs_Defaults(t *testing.T) {
	setTestConfigDir(t)
	cfg, rest, err := parseGlobalArgs([]string{"whoami"})
	require.NoError(t, err)
	assert.Equal(t, defaultURL, cfg.BaseURL)
	assert.Equal(t, "text", cfg.Output)
	assert.Empty(t, cfg.Token)
	assert.Equal(t, []string{"whoami"}, rest)
}

// TestParseGlobalArgs_FlagsOverrideEnv flag 优先于环境变量。
func TestParseGlobalArgs_FlagsOverrideEnv(t *testing.T) {
	setTestConfigDir(t)
	t.Setenv(envToken, "jmut_from_env")
	t.Setenv(envURL, "http://env.example:9090")

	cfg, rest, err := parseGlobalArgs([]string{
		"--token", "jmut_from_flag",
		"--url", "http://flag.example:8080/",
		"--output", "json",
		"list", "nodes",
	})
	require.NoError(t, err)
	assert.Equal(t, "jmut_from_flag", cfg.Token)
	assert.Equal(t, "http://flag.example:8080", cfg.BaseURL, "应去掉尾斜杠")
	assert.Equal(t, "json", cfg.Output)
	assert.Equal(t, []string{"list", "nodes"}, rest)
}

// TestParseGlobalArgs_EnvOnly 仅环境变量时采用 env。
func TestParseGlobalArgs_EnvOnly(t *testing.T) {
	setTestConfigDir(t)
	t.Setenv(envToken, "jmut_env_only")
	t.Setenv(envURL, "https://cp.example")

	cfg, rest, err := parseGlobalArgs([]string{"whoami"})
	require.NoError(t, err)
	assert.Equal(t, "jmut_env_only", cfg.Token)
	assert.Equal(t, "https://cp.example", cfg.BaseURL)
	assert.Equal(t, []string{"whoami"}, rest)
}

// TestParseGlobalArgs_EqualsForm 支持 --key=value 写法。
func TestParseGlobalArgs_EqualsForm(t *testing.T) {
	setTestConfigDir(t)
	cfg, rest, err := parseGlobalArgs([]string{
		"--token=jmut_eq",
		"--url=http://eq:1",
		"--output=json",
		"instance", "status", "1",
	})
	require.NoError(t, err)
	assert.Equal(t, "jmut_eq", cfg.Token)
	assert.Equal(t, "http://eq:1", cfg.BaseURL)
	assert.Equal(t, "json", cfg.Output)
	assert.Equal(t, []string{"instance", "status", "1"}, rest)
}

// TestParseGlobalArgs_SubcommandFlagsPassedThrough 子命令选项不被全局解析拦截。
func TestParseGlobalArgs_SubcommandFlagsPassedThrough(t *testing.T) {
	setTestConfigDir(t)
	_, rest, err := parseGlobalArgs([]string{"api", "POST", "/x", "--data", "@f.json"})
	require.NoError(t, err)
	assert.Equal(t, []string{"api", "POST", "/x", "--data", "@f.json"}, rest)
}

// TestParseGlobalArgs_UnknownFlagRejected 未知选项报错。
func TestParseGlobalArgs_UnknownFlagRejected(t *testing.T) {
	setTestConfigDir(t)
	_, _, err := parseGlobalArgs([]string{"--bogus", "whoami"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "未知选项")
}

// TestParseGlobalArgs_InvalidOutput 非法 --output 报错。
func TestParseGlobalArgs_InvalidOutput(t *testing.T) {
	setTestConfigDir(t)
	_, _, err := parseGlobalArgs([]string{"--output", "yaml", "whoami"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "text 或 json")
}

// TestConfigSetURL_And_List set-url 写入并成为默认，list 能读出。
func TestConfigSetURL_And_List(t *testing.T) {
	setTestConfigDir(t)
	require.NoError(t, configSetURL("https://cp.example.com"))

	cf, err := loadConfigFile()
	require.NoError(t, err)
	require.Contains(t, cf.Hosts, "cp.example.com")
	assert.Equal(t, "https://cp.example.com", cf.Hosts["cp.example.com"].URL)
	assert.Equal(t, "cp.example.com", cf.Default)
}

// TestConfigUse 切换默认 host。
func TestConfigUse(t *testing.T) {
	setTestConfigDir(t)
	require.NoError(t, configSetURL("https://a.example"))
	require.NoError(t, configSetURL("https://b.example"))

	cf, _ := loadConfigFile()
	assert.Equal(t, "b.example", cf.Default, "后设置的成为默认")

	require.NoError(t, configUse("a.example"))
	cf, _ = loadConfigFile()
	assert.Equal(t, "a.example", cf.Default)
}

// TestConfigUse_Unknown 切换不存在的主机报错。
func TestConfigUse_Unknown(t *testing.T) {
	setTestConfigDir(t)
	err := configUse("nope.example")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "未找到主机")
}

// TestConfigRemove 删除主机并从默认中摘除。
func TestConfigRemove(t *testing.T) {
	setTestConfigDir(t)
	require.NoError(t, configSetURL("https://a.example"))
	require.NoError(t, configSetURL("https://b.example"))

	require.NoError(t, configRemove("b.example"))
	cf, _ := loadConfigFile()
	assert.NotContains(t, cf.Hosts, "b.example")
	assert.Equal(t, "a.example", cf.Default, "应顺延到剩余主机")
}

// TestHostKeyOf 从 URL 提取 host 键。
func TestHostKeyOf(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://cp.example.com", "cp.example.com"},
		{"https://cp.example.com/", "cp.example.com"},
		{"http://127.0.0.1:8080", "127.0.0.1:8080"},
		{"cp.example.com", "cp.example.com"},
	}
	for _, tc := range cases {
		got, err := hostKeyOf(tc.in)
		require.NoError(t, err, tc.in)
		assert.Equal(t, tc.want, got, tc.in)
	}

	_, err := hostKeyOf("")
	assert.Error(t, err)
}

// TestNormalizeURL 补 scheme、去尾斜杠。
func TestNormalizeURL(t *testing.T) {
	assert.Equal(t, "https://a.example", normalizeURL("a.example"))
	assert.Equal(t, "https://a.example", normalizeURL("https://a.example/"))
	assert.Equal(t, "http://a.example", normalizeURL("http://a.example"))
	assert.Equal(t, "", normalizeURL(""))
}

// TestRequireLogin_Empty 未登录时给出可操作提示。
func TestRequireLogin_Empty(t *testing.T) {
	err := requireLogin(Config{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), envToken, "提示应指出 JM_TOKEN 旁路")
}

// TestRequireURL_Empty 缺基址时报错。
func TestRequireURL_Empty(t *testing.T) {
	err := requireURL(Config{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), envURL)
}

// TestAddQuery 解析 key=value，允许同名多值。
func TestAddQuery(t *testing.T) {
	q := url.Values{}
	for _, pair := range []string{"a=1", "a=2", "b=3"} {
		require.NoError(t, addQuery(q, pair))
	}
	assert.Equal(t, []string{"1", "2"}, q["a"])
	assert.Equal(t, []string{"3"}, q["b"])

	err := addQuery(q, "noequals")
	assert.Error(t, err, "缺等号应报错")
}

// TestExplainKeyringError 钥匙串不可用时给出 JM_TOKEN 提示。
func TestExplainKeyringError(t *testing.T) {
	assert.Empty(t, explainKeyringError(nil))
	msg := explainKeyringError(keyring.ErrUnavailable)
	assert.Contains(t, msg, "JM_TOKEN")
}

// TestDeviceLabel 设备标识包含主机名与 OS。
func TestDeviceLabel(t *testing.T) {
	label := deviceLabel()
	assert.NotEmpty(t, label)
	assert.Contains(t, label, "(")
}

// TestTokenPrefixOf 前缀截取。
func TestTokenPrefixOf(t *testing.T) {
	assert.Equal(t, "jmut_abcd", tokenPrefixOf("jmut_abcdefghij"))
	assert.Equal(t, "short", tokenPrefixOf("short"), "短令牌原样返回")
}

// TestConfigPathHonorsXDG XDG_CONFIG_HOME 生效。
func TestConfigPathHonorsXDG(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path, err := configPath()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, filepath.FromSlash(configFileName)), path)
}

// TestSaveConfigFile_Permissions 配置文件为 0600。
func TestSaveConfigFile_Permissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不适用 POSIX 权限位")
	}
	setTestConfigDir(t)
	require.NoError(t, configSetURL("https://a.example"))
	path, err := configPath()
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}
