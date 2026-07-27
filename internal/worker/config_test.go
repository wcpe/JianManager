package config

import (
	"os"
	"path/filepath"
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
	cfg, err := Load(t.TempDir() + "/nonexistent.yaml")
	require.NoError(t, err)
	assert.Equal(t, "edge-42", cfg.Name)
	assert.Equal(t, "cp.example.com:9100", cfg.ControlPlane)
}

// TestLoad_IgnoresLegacyGRPCPort 旧 grpc.port 与环境变量仅兼容读取，不能改变 Worker 行为。
func TestLoad_IgnoresLegacyGRPCPort(t *testing.T) {
	path := t.TempDir() + "/worker.yml"
	require.NoError(t, os.WriteFile(path, []byte("grpc:\n  port: 19101\nws:\n  port: 19102\n"), 0o644))
	t.Setenv("JIANMANAGER_GRPC_PORT", "19103")

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, 19102, cfg.WS.Port)
}

// TestLoad_ControlPlaneEnvAlias 旧键 JIANMANAGER_CONTROL_PLANE 与正式键等价（FR-354）。
func TestLoad_ControlPlaneEnvAlias(t *testing.T) {
	t.Setenv("JIANMANAGER_CONTROL_PLANE", "legacy-cp:9100")
	cfg, err := Load(t.TempDir() + "/nonexistent.yaml")
	require.NoError(t, err)
	assert.Equal(t, "legacy-cp:9100", cfg.ControlPlane)
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

// TestFindConfigFile_YmlPreferred .yml 优先于 .yaml，皆无返回空（FR-224 / FR-222 自检复用）。
func TestFindConfigFile_YmlPreferred(t *testing.T) {
	dir := t.TempDir()
	assert.Empty(t, FindConfigFile("worker", dir), "皆无返回空")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "worker.yaml"), []byte("name: a\n"), 0o644))
	assert.Equal(t, filepath.Join(dir, "worker.yaml"), FindConfigFile("worker", dir), "仅 .yaml 时命中 .yaml")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "worker.yml"), []byte("name: b\n"), 0o644))
	assert.Equal(t, filepath.Join(dir, "worker.yml"), FindConfigFile("worker", dir), ".yml 优先")
}

// TestWorkerConfigExists_FindsConfigBesideExecutable 配置在「二进制旁」而非 cwd 时也应判已配置（FIX-3）。
// 复现 Windows 服务/从别处启动：cwd 无 worker.yml，但 exe 目录有 → 应找到（否则误判未配置而重跑 setup）。
func TestWorkerConfigExists_FindsConfigBesideExecutable(t *testing.T) {
	cwd := t.TempDir()
	exeDir := t.TempDir()
	wd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(wd) })
	require.NoError(t, os.Chdir(cwd)) // cwd 无配置

	orig := executableDir
	executableDir = func() string { return exeDir }
	t.Cleanup(func() { executableDir = orig })

	assert.False(t, WorkerConfigExists(), "cwd 与 exe 目录皆无配置")

	require.NoError(t, os.WriteFile(filepath.Join(exeDir, "worker.yml"), []byte("name: beside\n"), 0o644))
	assert.True(t, WorkerConfigExists(), "exe 目录有 worker.yml → 应判已配置（FIX-3）")
}

// TestWorkerConfigExists 工作目录有/无 worker 配置文件时正确报告（FR-222 未配置自检的一半）。
func TestWorkerConfigExists(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(wd) })
	require.NoError(t, os.Chdir(dir))

	assert.False(t, WorkerConfigExists(), "干净目录无配置文件")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "worker.yml"), []byte("name: x\n"), 0o644))
	assert.True(t, WorkerConfigExists(), "有 worker.yml 即已配置")
}
