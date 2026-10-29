package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 导出物化预算配置（导出闸）的默认值与校验回归。
//
// 缺陷背景：上限原是编译期常量，前端导出又不传 limit，导致数据超 10000 行的
// 用户没有任何途径拿到导出文件。修复把上限做成配置项，但**默认值必须不变**——
// 否则所有既有部署的导出行为会在升级瞬间改变。

func TestValidateLogExportConfig(t *testing.T) {
	cases := []struct {
		name    string
		cfg     LogExportConfig
		wantErr string // 期望错误包含的子串；空表示期望通过
	}{
		{name: "零值（未配置）放行，回退默认", cfg: LogExportConfig{}},
		{name: "显式默认值放行", cfg: LogExportConfig{MaxRows: DefaultLogExportRows, MaxBytes: DefaultLogExportBytes}},
		{name: "调大放行", cfg: LogExportConfig{MaxRows: 1_000_000, MaxBytes: 1 << 32}},
		{name: "只配行数放行", cfg: LogExportConfig{MaxRows: 50_000}},
		{name: "负数拒绝", cfg: LogExportConfig{MaxRows: -1}, wantErr: "不能为负数"},
		{name: "过小拒绝", cfg: LogExportConfig{MaxRows: 50}, wantErr: "过小"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLogExportConfig(tc.cfg)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// Load 的默认值必须与 logcoord 的兜底常量一致：零配置部署行为不变。
func TestLoadLogExportDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "control-plane.yml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("server:\n  port: 8080\n"), 0o600))

	cfg, err := Load(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, 10_000, cfg.LogExport.MaxRows, "默认行上限必须保持既有常量值")
	assert.Equal(t, uint64(64<<20), cfg.LogExport.MaxBytes, "默认字节上限必须保持既有常量值")
	assert.Equal(t, DefaultLogExportRows, cfg.LogExport.MaxRows)
	assert.Equal(t, uint64(DefaultLogExportBytes), cfg.LogExport.MaxBytes)
}

// 配置文件显式调大必须被读到（否则「可配置」是空话）。
func TestLoadLogExportFromFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "control-plane.yml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(
		"log_export:\n  max_rows: 200000\n  max_bytes: 1073741824\n"), 0o600))

	cfg, err := Load(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, 200_000, cfg.LogExport.MaxRows)
	assert.Equal(t, uint64(1<<30), cfg.LogExport.MaxBytes)
}
