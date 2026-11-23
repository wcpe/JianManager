package config

// 受管 VL 磁盘占用上限的配置面回归（2026-10-29 用户要求「加磁盘上限」）。
//
// 背景与语义见 internal/worker/logs/vlsup/disk_limit_test.go 的文件头。
// 本文件只管**配置层**：默认值、互斥、越界三条。
//
// 与 vlsup 那侧的分工（刻意分开，不重复）：
//   - vlsup：BuildArgs 真的把参数拼进命令行了吗？InstanceConfig.Validate 拒了吗？
//   - 本文件：log_vl.* 键能读进来吗？Load 会在启动时报错吗？
//
// 转红方式：从 Load 里删掉 `cfg.LogVL.ValidateDiskLimit()` 调用 ⇒
// TestDiskLimitBothSetRejectedAtLoad 立红。

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// 默认不限制：与改动前行为逐字一致（不猜“聪明”的默认值）。
func TestDiskLimitDefaultsToUnlimited(t *testing.T) {
	cfg, err := Load(t.TempDir() + "/nonexistent.yaml")
	require.NoError(t, err)
	require.Zero(t, cfg.LogVL.MaxDiskSpaceBytes, "默认 0 = 不限制")
	require.Zero(t, cfg.LogVL.MaxDiskUsagePercent)
	require.NoError(t, cfg.LogVL.ValidateDiskLimit())
}

// 三个合法形态：只配字节 / 只配百分比 / 都不配。
func TestDiskLimitLegalShapes(t *testing.T) {
	cfg, err := Load(t.TempDir() + "/nonexistent.yaml")
	require.NoError(t, err)

	onlyBytes := cfg.LogVL
	onlyBytes.MaxDiskSpaceBytes = 20 << 30
	require.NoError(t, onlyBytes.ValidateDiskLimit(), "只配字节上限应通过")

	onlyPercent := cfg.LogVL
	onlyPercent.MaxDiskUsagePercent = 85
	require.NoError(t, onlyPercent.ValidateDiskLimit(), "只配百分比上限应通过")
}

// 两者同时配 ⇒ 拒绝。
//
// 这条不是“我们发明的规矩”：真实 VL 二进制对同配的反应是
// `fatal: -retention.maxDiskSpaceUsageBytes and -retention.maxDiskUsagePercent
// cannot be set simultaneously`（2026-10-29 用 v1.52.0 实跑确认）。
// 我们在启动前拦，是为了给出中文原因与键名，而不是让运维去看 VL 的 fatal。
func TestDiskLimitBothSetRejected(t *testing.T) {
	cfg, err := Load(t.TempDir() + "/nonexistent.yaml")
	require.NoError(t, err)
	cfg.LogVL.MaxDiskSpaceBytes = 20 << 30
	cfg.LogVL.MaxDiskUsagePercent = 85
	require.Error(t, cfg.LogVL.ValidateDiskLimit(), "两者互斥必须拒绝（VL 官方也是 fatal）")
}

// 越界值 ⇒ 拒绝。
func TestDiskLimitRejectsOutOfRange(t *testing.T) {
	cfg, err := Load(t.TempDir() + "/nonexistent.yaml")
	require.NoError(t, err)

	neg := cfg.LogVL
	neg.MaxDiskSpaceBytes = -1
	require.Error(t, neg.ValidateDiskLimit(), "负字节无意义")

	over := cfg.LogVL
	over.MaxDiskUsagePercent = 101
	require.Error(t, over.ValidateDiskLimit(), "百分比上限 100")

	negPct := cfg.LogVL
	negPct.MaxDiskUsagePercent = -1
	require.Error(t, negPct.ValidateDiskLimit())
}

// 从 yml 真的读得进来（键名拼错会被 viper 静默忽略，这条守的就是键名）。
func TestDiskLimitKeyBinding(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yml")
	require.NoError(t, os.WriteFile(path, []byte(
		"log_vl:\n  max_disk_space_bytes: 21474836480\n"), 0o600))
	cfg, err := Load(path)
	require.NoError(t, err)
	require.EqualValues(t, 21474836480, cfg.LogVL.MaxDiskSpaceBytes,
		"log_vl.max_disk_space_bytes 键必须真的绑上（否则运维配了也不生效）")
	require.NoError(t, cfg.LogVL.ValidateDiskLimit())
}

// Load 路径上的拦截：两个都配时启动阶段必须直接报错，不能等到 VL 子进程 fatal。
func TestDiskLimitBothSetRejectedAtLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yml")
	require.NoError(t, os.WriteFile(path, []byte(
		"log_vl:\n  max_disk_space_bytes: 21474836480\n  max_disk_usage_percent: 85\n"), 0o600))
	_, err := Load(path)
	require.Error(t, err, "Load 就必须拒绝，而不是把错误留给 VL 子进程")
	require.Contains(t, err.Error(), "max_disk_space_bytes", "错误必须点名是哪个键")
}
