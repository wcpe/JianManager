package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoad_LogTailerDefaultsEnabled 锤死新采集器**默认开启**。
//
// # 为何要钉住
//
// 这个默认值就是「采集走哪条链路」的开关。改回 false 不会有任何测试报错，
// 但线上会静默退回旧链路——而旧链路是阶段三要拆掉的目标。
// 一个不报错的回退，比一个报错的错误更难发现。
//
// 转红方式：把 SetDefault("log_tailer.enabled", ...) 改回 false。
func TestLoad_LogTailerDefaultsEnabled(t *testing.T) {
	cfg, err := Load(t.TempDir() + "/nonexistent.yaml")
	require.NoError(t, err)
	assert.True(t, cfg.LogTailer.Enabled, "新采集器应当默认开启（2026-11-06 切换）")
	assert.True(t, cfg.LogTailer.ReadFromHead, "默认从头读，才能补上部署前的历史日志")
	assert.Empty(t, cfg.LogTailer.Paths, "默认不配静态路径——实例路径由绑定表推导")
	assert.Empty(t, cfg.LogTailer.Exclude, "空 = 用实现包默认（*.gz 等）")
	assert.Empty(t, cfg.LogTailer.RegistryPath, "空 = var/log/tailer.registry.jsonl")
}

// TestLoad_LogTailerRollbackSwitch 验证回退开关可用（enabled: false 走旧链路）。
//
// 阶段三删旧代码前，这是唯一的生产级回退手段，必须能配。
func TestLoad_LogTailerRollbackSwitch(t *testing.T) {
	t.Setenv("JIANMANAGER_LOG_TAILER_ENABLED", "false")
	cfg, err := Load(t.TempDir() + "/nonexistent.yaml")
	require.NoError(t, err)
	assert.False(t, cfg.LogTailer.Enabled)
}

// TestLogTailerIntervalsParsing 空串走实现包默认（返回 0），非法值启动即拒。
//
// # 为何非法值必须拒
//
// 采集周期配错会让「实时性」静默变形（比如把 2s 误写成 2m，日志看起来“半天不上来”），
// 而这件事只有在用户投诉时才会被发现，排查方向会被完全带偏。
func TestLogTailerIntervalsParsing(t *testing.T) {
	scan, progress, err := (&Config{}).LogTailerIntervals()
	require.NoError(t, err)
	assert.Zero(t, scan, "空串应返回 0（交给实现包默认）")
	assert.Zero(t, progress)

	cfg := &Config{LogTailer: LogTailerConfig{ScanInterval: "5s", ProgressInterval: "500ms"}}
	scan, progress, err = cfg.LogTailerIntervals()
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, scan)
	assert.Equal(t, 500*time.Millisecond, progress)

	for _, bad := range []string{"bogus", "0", "-1s"} {
		cfg := &Config{LogTailer: LogTailerConfig{ScanInterval: bad}}
		_, _, err := cfg.LogTailerIntervals()
		assert.Error(t, err, "非法周期 %q 应当启动即拒", bad)
	}
}
