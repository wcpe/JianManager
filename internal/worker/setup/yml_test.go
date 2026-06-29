package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	workercfg "github.com/wcpe/JianManager/internal/worker"
)

// TestWriteWorkerYML_Fields 写出字段正确、token 不在文件中，且能被 config.Load 正常解析回。
func TestWriteWorkerYML_Fields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yml")

	in := &Inputs{
		ControlPlane: "cp-host:9100",
		EnrollToken:  "jmet_should_not_appear",
		NodeName:     "edge-1",
		GRPCPort:     19101,
		WSPort:       19102,
		DataDir:      filepath.Join(dir, "mydata"),
	}
	require.NoError(t, WriteWorkerYML(path, in))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(raw)
	assert.NotContains(t, text, "jmet_should_not_appear", "enrollment token 绝不写入")

	// 用 config.Load 回读，验证字段映射正确（与 viper 解析链一致）。
	cfg, err := workercfg.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "edge-1", cfg.Name)
	assert.Equal(t, "cp-host:9100", cfg.ControlPlane)
	assert.Equal(t, 19101, cfg.GRPC.Port)
	assert.Equal(t, 19102, cfg.WS.Port)
	assert.Equal(t, filepath.Join(dir, "mydata"), cfg.DataDir)
	assert.Equal(t, "info", cfg.Log.Level)
	assert.Equal(t, "json", cfg.Log.Format)
	assert.Empty(t, cfg.EnrollToken, "token 不从 yaml 读出")
}

// TestWriteWorkerYML_OmitsEmptyDataDir data_dir 留空时不写入该字段（缺省 = ./data）。
func TestWriteWorkerYML_OmitsEmptyDataDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yml")
	in := &Inputs{
		ControlPlane: "cp:9100",
		NodeName:     "n",
		GRPCPort:     9101,
		WSPort:       9102,
		DataDir:      "",
	}
	require.NoError(t, WriteWorkerYML(path, in))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	// 解析为通用 map，确认没有 data_dir 键。
	var m map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &m))
	_, hasDataDir := m["data_dir"]
	assert.False(t, hasDataDir, "data_dir 留空时不写入")
	assert.Equal(t, "cp:9100", m["control_plane"])
}

// TestWriteWorkerYML_EmptyNameFallsBack 节点名留空时回退 node-<hostname>（非空）。
func TestWriteWorkerYML_EmptyNameFallsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yml")
	in := &Inputs{ControlPlane: "cp:9100", NodeName: "", GRPCPort: 9101, WSPort: 9102}
	require.NoError(t, WriteWorkerYML(path, in))

	cfg, err := workercfg.Load(path)
	require.NoError(t, err)
	assert.NotEmpty(t, cfg.Name, "节点名留空应回退非空默认")
}

// TestWriteWorkerYML_Atomic 写出是有效 YAML（原子写无半截内容）。
func TestWriteWorkerYML_Atomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yml")
	require.NoError(t, WriteWorkerYML(path, &Inputs{ControlPlane: "cp:9100", NodeName: "n", GRPCPort: 9101, WSPort: 9102}))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var m map[string]any
	assert.NoError(t, yaml.Unmarshal(raw, &m), "写出应为合法 YAML")

	// 临时文件已清理。
	_, statErr := os.Stat(path + ".tmp")
	assert.True(t, os.IsNotExist(statErr), "临时文件应已 rename 清理")
}
