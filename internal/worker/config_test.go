package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoad_Defaults 零配置（无文件、无 env）时加载合理默认值（FR-080，见 ADR-020）。
func TestLoad_Defaults(t *testing.T) {
	// 指向不存在的文件，强制走默认值（ReadInConfig 容错）。
	cfg, err := Load(t.TempDir() + "/nonexistent.yaml")
	require.NoError(t, err)
	assert.Equal(t, "node-01", cfg.Name)
	assert.Equal(t, "localhost:9100", cfg.ControlPlane)
	assert.Equal(t, 9101, cfg.GRPC.Port)
	assert.Equal(t, 9102, cfg.WS.Port)
	assert.Empty(t, cfg.EnrollToken, "enroll token 默认空，仅经 env/命令行注入")
}

// TestLoad_EnrollTokenFromEnv enrollment token 经 JIANMANAGER_ENROLL_TOKEN 注入、不从 yaml 读取。
func TestLoad_EnrollTokenFromEnv(t *testing.T) {
	t.Setenv("JIANMANAGER_ENROLL_TOKEN", "jmet_abc123")
	cfg, err := Load(t.TempDir() + "/nonexistent.yaml")
	require.NoError(t, err)
	assert.Equal(t, "jmet_abc123", cfg.EnrollToken)
}

// TestLoad_EnvOverridesDefaults JIANMANAGER_ 前缀环境变量按路径覆盖配置（含历史别名）。
func TestLoad_EnvOverridesDefaults(t *testing.T) {
	t.Setenv("JIANMANAGER_NODE_NAME", "edge-42")
	t.Setenv("JIANMANAGER_CONTROL_PLANE_GRPC", "cp.example.com:9100")
	t.Setenv("JIANMANAGER_GRPC_PORT", "19101")
	cfg, err := Load(t.TempDir() + "/nonexistent.yaml")
	require.NoError(t, err)
	assert.Equal(t, "edge-42", cfg.Name)
	assert.Equal(t, "cp.example.com:9100", cfg.ControlPlane)
	assert.Equal(t, 19101, cfg.GRPC.Port)
}

// TestLoad_ArtifactCacheCap 节点制品缓存上限默认 0（不限），可经 env 覆盖（FR-178）。
func TestLoad_ArtifactCacheCap(t *testing.T) {
	cfg, err := Load(t.TempDir() + "/nonexistent.yaml")
	require.NoError(t, err)
	assert.Equal(t, int64(0), cfg.ArtifactCache.MaxBytes, "默认不限")

	t.Setenv("JIANMANAGER_ARTIFACT_CACHE_MAX_BYTES", "1073741824")
	cfg2, err := Load(t.TempDir() + "/nonexistent.yaml")
	require.NoError(t, err)
	assert.Equal(t, int64(1073741824), cfg2.ArtifactCache.MaxBytes)
}
