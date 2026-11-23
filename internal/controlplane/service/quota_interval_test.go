package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestP3Fix_QuotaIntervalNotClampedByProbeTimeout 复现「配额巡检周期被 MC 直探超时上界钳制」。
//
// 缺陷现场（真机验收 2026-09-23 发现）：
//   - `quota.enforce_interval` 默认值为 `60s`，但 CP 启动日志打印 `"interval":"10s"`；
//   - 根因：`quota_enforce.go` 的 `interval()` 用了 `parseDurationOr`——那是**专为
//     MC 直探超时**设计的解析器，末尾会 `directprobe.NormalizeTimeout` 把值钳到
//     `MaxTimeout = 10s`。于是**任何 >10s 的巡检周期都被静默钳成 10s**。
//   - 后果：spec §2.3 明言「周期下限与规模有关，64 服时应为 120s」，该配置**完全无法生效**；
//     且 K×interval 判定窗口被压缩（默认 K=5 时 5min → 50s），stop 档误停风险回升。
//   - 同 package 的 `health_policy.go` 已有正确范式 `parseDurationDefault`（不钳制），
//     其注释还明确点出「巡检周期/熔断窗口不受 MC 直探超时上界约束」——证明作者知道该区别，
//     仅配额巡检这处漏改。
//
// 本用例断言「配置的巡检周期原样生效」——修复前必失败（60s/120s 都被钳成 10s）。
func TestP3Fix_QuotaIntervalNotClampedByProbeTimeout(t *testing.T) {
	db := quotaTestDB(t)
	e, _ := newEnforcer(t, db, &stubQuotaMetrics{})

	// ① 显式配置 120s（spec §2.3 对 64 服的推荐值）必须原样生效。
	e.SetSettingsReader(stubSettings{SettingKeyQuotaEnforceInterval: "120s"})
	assert.Equal(t, 120*time.Second, e.interval(),
		"配置 120s 必须原样生效（修复前被钳成 10s，使 64 服规模无法按 spec 调参）")

	// ② 默认值 60s 同样不得被钳（它就已超过 10s 上界）。
	e.SetSettingsReader(stubSettings{SettingKeyQuotaEnforceInterval: "60s"})
	assert.Equal(t, 60*time.Second, e.interval(), "配置 60s 必须原样生效")

	// ③ 小周期原样生效（钳制只影响上界，但一并锁定语义）。
	e.SetSettingsReader(stubSettings{SettingKeyQuotaEnforceInterval: "5s"})
	assert.Equal(t, 5*time.Second, e.interval(), "配置 5s 必须原样生效")

	// ④ 非法/非正/空值回退内置默认（既有语义，不得回归）。
	for _, bad := range []string{"not-a-duration", "-1s", "0s", ""} {
		e.SetSettingsReader(stubSettings{SettingKeyQuotaEnforceInterval: bad})
		assert.Equal(t, quotaDefaultInterval, e.interval(), "非法值 %q 应回退默认", bad)
	}

	// ⑤ 无设置读取器时用内置默认。
	e.SetSettingsReader(nil)
	assert.Equal(t, quotaDefaultInterval, e.interval(), "无读取器时用内置默认")
}

// TestP3Fix_ProbeTimeoutStillClamped 对照：MC 直探超时**必须继续**受上界钳制
// （那是钳制存在的本意——下发给 Worker 的直探超时不能超过单轮预算）。
//
// 本用例防「为修巡检周期而顺手删掉钳制」，那会让直探超时失去上界保护。
func TestP3Fix_ProbeTimeoutStillClamped(t *testing.T) {
	db := newSettingsTestDB(t)
	svc := NewSettingsService(db, testConfig())

	// 默认（未覆盖）即 3s，且在 10s 上界内。
	slp, query := svc.DirectProbeTimeouts()
	assert.Equal(t, query, slp, "两来源同口径")
	assert.Greater(t, slp, time.Duration(0), "默认超时为正")
	assert.LessOrEqual(t, slp, 10*time.Second, "默认超时不超过单轮预算上界")

	// 直接验证钳制函数语义：parseDurationOr 对超大值必须钳到 MaxTimeout。
	assert.Equal(t, 10*time.Second, parseDurationOr("45s", defaultDirectProbeTimeout),
		"parseDurationOr 对上界的钳制是其本意，必须保留")
	assert.Equal(t, 5*time.Second, parseDurationOr("5s", defaultDirectProbeTimeout),
		"界内值原样返回")

	// 而巡检周期走的 parseDurationDefault **不钳制**——这正是两者的分工。
	assert.Equal(t, 120*time.Second, parseDurationDefault("120s", quotaDefaultInterval),
		"巡检周期等非直探配置必须走不钳制的解析器")
}

// TestP3Fix_LatestProcessSampleScanDoesNotFail 复现「心跳兜底采样因 SQLite 时间扫描失败而完全失效」。
//
// 缺陷现场（真机验收 2026-09-23 发现）：CP 日志每轮巡检都打
//
//	配额巡检：实例采样失败 … error="sql: Scan error on column index 0,
//	name \"MAX(sampled_at)\": unsupported Scan, storing driver.Value type string into type *time.Time"
//
// `Select("MAX(sampled_at)").Scan(&latest)` 把聚合结果扫进**裸 `time.Time`**，
// 而 SQLite 驱动对 datetime 列返回 string，无法直接扫入 `*time.Time`。
// 后果：`LatestProcessSample` 恒返回 err → `sampleInstance` 的「心跳兜底」这条
// **设计好的降级路径完全不可用**（节点离线时本可用心跳样本兜底 CPU 判定），
// 且把「预期内无数据」误报成「采样失败」，污染 `failed` 计数与运维日志。
//
// 正确范式在同批次的 `crash_correlation.go`：扫进**模型**（GORM 走时间字段解析）
// 而非裸 `time.Time`。
func TestP3Fix_LatestProcessSampleScanDoesNotFail(t *testing.T) {
	db := quotaTestDB(t)
	inst := makeQuotaInstance(t, db, "hb", 0, 0, 0, 0)
	src := NewMetricQuotaSource(db, NewMetricService(db), nil)

	now := time.Now().UTC().Truncate(time.Second)

	// ① 窗口内无样本：必须返回零值样本且 **err == nil**（「无数据」不是「采样失败」）。
	sample, err := src.LatestProcessSample(inst.UUID, now.Add(-time.Hour))
	require.NoError(t, err, "窗口内无样本时不得报错（原实现因扫描失败恒报错）")
	require.NotNil(t, sample)
	assert.Zero(t, sample.CPUPercent)
	assert.Zero(t, sample.RSSBytes)

	// ② 有样本：必须按「最大 sampled_at 的一拍」求和（多个进程共享同一拍）。
	require.NoError(t, db.Create(&model.ProcessMetricSnapshot{
		NodeUUID: "n", InstanceUUID: inst.UUID, PID: 1, Name: "p1",
		CPUPercent: 30, RSSBytes: 100 << 20, SampledAt: now,
	}).Error)
	require.NoError(t, db.Create(&model.ProcessMetricSnapshot{
		NodeUUID: "n", InstanceUUID: inst.UUID, PID: 2, Name: "p2",
		CPUPercent: 20, RSSBytes: 50 << 20, SampledAt: now, // 同拍
	}).Error)
	// 更早的一拍不应被计入（否则把不同时刻的占用叠加成虚高值）。
	require.NoError(t, db.Create(&model.ProcessMetricSnapshot{
		NodeUUID: "n", InstanceUUID: inst.UUID, PID: 3, Name: "p3",
		CPUPercent: 99, RSSBytes: 900 << 20, SampledAt: now.Add(-30 * time.Minute),
	}).Error)

	sample2, err := src.LatestProcessSample(inst.UUID, now.Add(-time.Hour))
	require.NoError(t, err, "有样本时必须采样成功（这是心跳兜底路径的可用性前提）")
	require.NotNil(t, sample2)
	assert.InDelta(t, 50.0, sample2.CPUPercent, 0.001, "同拍 CPU 求和（30+20），不含更早那拍")
	assert.EqualValues(t, 150<<20, sample2.RSSBytes, "同拍 RSS 求和（100+50 MiB），不含更早那拍")
}
