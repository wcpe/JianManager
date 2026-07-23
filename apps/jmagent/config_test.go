package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseArgs_Defaults 无 flag、无 env 时 CP 默认本地 8080，output=text。
func TestParseArgs_Defaults(t *testing.T) {
	t.Setenv(envAgentToken, "")
	t.Setenv(envAgentCP, "")

	cfg, rest, err := parseArgs([]string{"whoami"})
	require.NoError(t, err)
	assert.Equal(t, defaultCPURL, cfg.CPURL)
	assert.Equal(t, "text", cfg.Output)
	assert.Empty(t, cfg.Token)
	assert.Equal(t, []string{"whoami"}, rest)
}

// TestParseArgs_EnvAndFlags flag 覆盖 env；token/cp-url 优先级正确。
func TestParseArgs_EnvAndFlags(t *testing.T) {
	t.Setenv(envAgentToken, "jmat_from_env")
	t.Setenv(envAgentCP, "http://env.example:9090")

	cfg, rest, err := parseArgs([]string{
		"--token", "jmat_from_flag",
		"--cp-url", "http://flag.example:8080/",
		"--output", "json",
		"list", "nodes",
	})
	require.NoError(t, err)
	assert.Equal(t, "jmat_from_flag", cfg.Token)
	assert.Equal(t, "http://flag.example:8080", cfg.CPURL) // 去尾斜杠
	assert.Equal(t, "json", cfg.Output)
	assert.Equal(t, []string{"list", "nodes"}, rest)
}

// TestParseArgs_EnvOnly 仅 env 时采用环境变量。
func TestParseArgs_EnvOnly(t *testing.T) {
	t.Setenv(envAgentToken, "jmat_env_only")
	t.Setenv(envAgentCP, "https://cp.example")

	cfg, rest, err := parseArgs([]string{"whoami"})
	require.NoError(t, err)
	assert.Equal(t, "jmat_env_only", cfg.Token)
	assert.Equal(t, "https://cp.example", cfg.CPURL)
	assert.Equal(t, []string{"whoami"}, rest)
}

// TestParseArgs_EqualsForm 支持 --token= / --cp-url= / --output= 写法。
func TestParseArgs_EqualsForm(t *testing.T) {
	t.Setenv(envAgentToken, "")
	t.Setenv(envAgentCP, "")

	cfg, rest, err := parseArgs([]string{
		"--token=jmat_eq",
		"--cp-url=http://eq:1",
		"--output=json",
		"instance", "status", "1",
	})
	require.NoError(t, err)
	assert.Equal(t, "jmat_eq", cfg.Token)
	assert.Equal(t, "http://eq:1", cfg.CPURL)
	assert.Equal(t, "json", cfg.Output)
	assert.Equal(t, []string{"instance", "status", "1"}, rest)
}

// TestParseArgs_GlobalFlagsAfterSubcommand 全局选项可出现在子命令后。
func TestParseArgs_GlobalFlagsAfterSubcommand(t *testing.T) {
	t.Setenv(envAgentToken, "")
	t.Setenv(envAgentCP, "")

	cfg, rest, err := parseArgs([]string{"whoami", "--token", "jmat_x", "--output", "json"})
	require.NoError(t, err)
	assert.Equal(t, "jmat_x", cfg.Token)
	assert.Equal(t, "json", cfg.Output)
	assert.Equal(t, []string{"whoami"}, rest)
}

// TestParseArgs_InvalidOutput 非法 --output 报错。
func TestParseArgs_InvalidOutput(t *testing.T) {
	_, _, err := parseArgs([]string{"--output", "yaml", "whoami"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "text 或 json")
}

// TestRequireToken_Empty 缺 token 时给出中文提示。
func TestRequireToken_Empty(t *testing.T) {
	err := requireToken(Config{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), envAgentToken)
}
