package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseConfig_Flags(t *testing.T) {
	t.Setenv(envAgentToken, "")
	t.Setenv(envAgentCP, "")
	cfg, err := ParseConfig([]string{"--token", "jmat_abc", "--cp-url", "http://127.0.0.1:8080/"})
	require.NoError(t, err)
	assert.Equal(t, "jmat_abc", cfg.Token)
	assert.Equal(t, "http://127.0.0.1:8080", cfg.CPURL)
}

func TestParseConfig_Env(t *testing.T) {
	t.Setenv(envAgentToken, "jmat_from_env")
	t.Setenv(envAgentCP, "http://cp.example:9090")
	cfg, err := ParseConfig(nil)
	require.NoError(t, err)
	assert.Equal(t, "jmat_from_env", cfg.Token)
	assert.Equal(t, "http://cp.example:9090", cfg.CPURL)
}

func TestParseConfig_FlagOverridesEnv(t *testing.T) {
	t.Setenv(envAgentToken, "env_token")
	t.Setenv(envAgentCP, "http://env")
	cfg, err := ParseConfig([]string{"--token", "flag_token", "--cp-url", "http://flag"})
	require.NoError(t, err)
	assert.Equal(t, "flag_token", cfg.Token)
	assert.Equal(t, "http://flag", cfg.CPURL)
}

func TestParseConfig_MissingToken(t *testing.T) {
	t.Setenv(envAgentToken, "")
	t.Setenv(envAgentCP, "http://x")
	_, err := ParseConfig(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), envAgentToken)
}

func TestParseConfig_MissingCP(t *testing.T) {
	t.Setenv(envAgentToken, "jmat_x")
	t.Setenv(envAgentCP, "")
	// 清理可能残留的环境
	_ = os.Unsetenv(envAgentCP)
	_, err := ParseConfig([]string{"--token", "jmat_x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), envAgentCP)
}
