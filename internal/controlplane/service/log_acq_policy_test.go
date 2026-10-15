package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestLogAcqPolicyDefaults 验证未配置任何键时，日志采集策略回退到与
// worker.yml 基线一致的安全默认（FR-503）。
//
// 语义要点：规则引擎默认开、实时采集默认开、gz 导入默认关（保守）、
// 定时扫描默认关、路径覆盖为空。
func TestLogAcqPolicyDefaults(t *testing.T) {
	svc := NewSettingsService(newSettingsTestDB(t), testConfig())

	p := svc.LogAcqPolicy("")
	require.True(t, p.RulesEnabled, "规则引擎默认开启（与 FR-502 落地后行为一致）")
	require.True(t, p.RealtimeTail, "实时采集默认开启（应急开关默认不生效）")
	require.False(t, p.GzImportEnabled, "gz 导入默认关闭（保守：避免一次性导入大量历史）")
	require.Zero(t, p.GzScanInterval, "定时扫描默认关闭")
	require.Empty(t, p.PathOverrides, "默认无路径覆盖")
}

// TestLogAcqPolicyPlatformLevel 验证平台级键被读取。
func TestLogAcqPolicyPlatformLevel(t *testing.T) {
	svc := NewSettingsService(newSettingsTestDB(t), testConfig())

	require.NoError(t, svc.Update(map[string]string{
		SettingKeyLogAcqRulesEnabled:   "false",
		SettingKeyLogAcqRealtimeTail:   "false",
		SettingKeyLogAcqGzImport:       "true",
		SettingKeyLogAcqGzScanInterval: "30s",
	}))
	p := svc.LogAcqPolicy("")
	require.False(t, p.RulesEnabled)
	require.False(t, p.RealtimeTail)
	require.True(t, p.GzImportEnabled)
	require.Equal(t, "30s", p.GzScanInterval.String())
}

// TestLogAcqPolicyWorkerOverrideWins 验证 Worker 级覆盖优先于平台级（FR-503 的两层语义）。
//
// 这是本项的核心契约：同一键在两处都有值时，Worker 级必须胜出；
// 未被 Worker 覆盖的键仍继承平台级。
func TestLogAcqPolicyWorkerOverrideWins(t *testing.T) {
	svc := NewSettingsService(newSettingsTestDB(t), testConfig())

	// 平台级：实时采集关、gz 导入开。
	require.NoError(t, svc.Update(map[string]string{
		SettingKeyLogAcqRealtimeTail: "false",
		SettingKeyLogAcqGzImport:     "true",
	}))
	// Worker 级：只覆盖实时采集（打开），gz 导入不覆盖。
	require.NoError(t, svc.Update(map[string]string{
		NodeScopedSettingKey(SettingKeyLogAcqRealtimeTail, "node-abc"): "true",
	}))

	// 该 Worker：实时采集被覆盖为开，gz 导入继承平台级（开）。
	got := svc.LogAcqPolicy("node-abc")
	require.True(t, got.RealtimeTail, "Worker 级覆盖必须优先于平台级")
	require.True(t, got.GzImportEnabled, "未覆盖的键应继承平台级")

	// 另一 Worker：无覆盖，全用平台级（实时采集关）。
	other := svc.LogAcqPolicy("node-xyz")
	require.False(t, other.RealtimeTail, "无覆盖的 Worker 必须继承平台级")
	require.True(t, other.GzImportEnabled)
}

// TestLogAcqPolicyIsolatedBetweenWorkers 验证 Worker 级覆盖互不影响。
func TestLogAcqPolicyIsolatedBetweenWorkers(t *testing.T) {
	svc := NewSettingsService(newSettingsTestDB(t), testConfig())

	require.NoError(t, svc.Update(map[string]string{
		NodeScopedSettingKey(SettingKeyLogAcqRealtimeTail, "node-a"): "false",
		NodeScopedSettingKey(SettingKeyLogAcqRealtimeTail, "node-b"): "true",
	}))

	require.False(t, svc.LogAcqPolicy("node-a").RealtimeTail)
	require.True(t, svc.LogAcqPolicy("node-b").RealtimeTail)
	// 未配置的节点继承默认（开）。
	require.True(t, svc.LogAcqPolicy("node-c").RealtimeTail)
}

// TestLogAcqPolicyPathOverridesJSON 验证路径覆盖映射的解析（FR-502 §3.4）。
func TestLogAcqPolicyPathOverridesJSON(t *testing.T) {
	svc := NewSettingsService(newSettingsTestDB(t), testConfig())

	require.NoError(t, svc.Update(map[string]string{
		SettingKeyLogAcqPathOverrides: `{"inst:1":"custom/a.log","inst:2":"/abs/b.log"}`,
	}))
	p := svc.LogAcqPolicy("")
	require.Equal(t, "custom/a.log", p.PathOverrides["inst:1"])
	require.Equal(t, "/abs/b.log", p.PathOverrides["inst:2"])
}

// TestLogAcqPolicyPathOverridesInvalidJSONIsSafe 验证非法路径覆盖 JSON 不致命（两道防线）。
//
// 第一道在**写入侧**：Update 直接拒绝非法 JSON，不落库——用户在设置页当场看到错误，
// 而不是写进去、静默不生效。
// 第二道在**读取侧**：即便库里已存在脏值（历史遗留、人工改库、旧版本写入），
// LogAcqPolicy 也回退为空覆盖并继续下发其余有效配置——配置问题绝不能导致心跳下发失败
// （那会让节点被判离线）。
func TestLogAcqPolicyPathOverridesInvalidJSONIsSafe(t *testing.T) {
	db := newSettingsTestDB(t)
	svc := NewSettingsService(db, testConfig())

	// 第一道：写入侧拒绝，且不落库。
	err := svc.Update(map[string]string{SettingKeyLogAcqPathOverrides: `{not-json`})
	require.ErrorIs(t, err, ErrSettingValueInvalid, "非法 JSON 应被写入校验拒绝")
	require.Empty(t, svc.EffectiveValue(SettingKeyLogAcqPathOverrides), "被拒绝的值不应落库")

	// 第二道：绕过写入服务注入脏值（模拟历史遗留），读取侧仍安全。
	require.NoError(t, db.Save(&model.PlatformSetting{
		Key: SettingKeyLogAcqPathOverrides, Value: `{not-json`,
	}).Error)

	p := svc.LogAcqPolicy("")
	require.Empty(t, p.PathOverrides, "非法 JSON 应回退为空覆盖，而非让下发失败")
	// 其余键仍正常（证明策略整体未被非法值拖垮）。
	require.True(t, p.RealtimeTail)
}

// TestLogAcqPolicyWorkerScopeWhitelist 验证节点级覆盖键的白名单边界（FR-503）。
//
// 只对「经心跳按拍下发」的键开放节点级覆盖：log.acq.* / health.* / direct_probe.*。
// 其余键套 `@node:` 后缀必须被拒绝——否则会落一条永不生效的键（无消费方），
// 属于最难排查的一类静默配置错误。
func TestLogAcqPolicyWorkerScopeWhitelist(t *testing.T) {
	svc := NewSettingsService(newSettingsTestDB(t), testConfig())

	// 放行：三类心跳下发键。
	require.NoError(t, svc.Update(map[string]string{
		NodeScopedSettingKey(SettingKeyLogAcqRulesEnabled, "n1"):    "false",
		NodeScopedSettingKey(SettingKeyHealthScanInterval, "n1"):    "60s",
		NodeScopedSettingKey(SettingKeyDirectProbeSLPTimeout, "n1"): "5s",
	}), "心跳下发类键应支持节点级覆盖")

	// 拒绝：不经心跳下发的键（无节点级消费方，写了也不会生效）。
	err := svc.Update(map[string]string{
		NodeScopedSettingKey(SettingKeyLogLevel, "n1"): "debug",
	})
	require.ErrorIs(t, err, ErrSettingKeyNotWritable, "log.level 无节点级消费方，应拒绝")

	// 拒绝：残缺后缀（`x@node:`）——不能退化成「配了平台级」。
	err = svc.Update(map[string]string{
		SettingKeyLogAcqRealtimeTail + nodeScopedSettingSuffix: "false",
	})
	require.ErrorIs(t, err, ErrSettingKeyNotWritable, "残缺 @node: 后缀应拒绝")

	// 拒绝：节点级键的值语义校验与平台级同口径（后缀不改变校验）。
	err = svc.Update(map[string]string{
		NodeScopedSettingKey(SettingKeyLogAcqGzScanInterval, "n1"): "0",
	})
	require.ErrorIs(t, err, ErrSettingValueInvalid, "节点级键也须过值校验（0 不是合法周期）")
}
