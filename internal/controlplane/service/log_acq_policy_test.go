package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestLogAcqPolicyDefaults 验证未配置任何键时，日志采集策略回退到与
// worker.yml 基线一致的安全默认（FR-503）。
//
// 语义要点：规则引擎默认开、实时采集默认开、gz 导入**总开关默认开**、
// 定时扫描周期默认空（二者组合 ⇒ 默认行为仍是不扫描）、路径覆盖为空。
func TestLogAcqPolicyDefaults(t *testing.T) {
	svc := NewSettingsService(newSettingsTestDB(t), testConfig())

	p := svc.LogAcqPolicy("")
	require.True(t, p.RulesEnabled, "规则引擎默认开启（与 FR-502 落地后行为一致）")
	require.True(t, p.RealtimeTail, "实时采集默认开启（应急开关默认不生效）")
	// 总开关默认开 + 周期默认空 ⇒ 默认仍是「不扫描」；总开关开着才能让
	// worker.yml 的 log_ingest.archive_scan_interval 基线回落可达（否则既有部署升级后静默失去定时扫描）。
	require.True(t, p.GzImportEnabled, "gz 导入总开关默认开启")
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
		SettingKeyLogAcqPathOverrides: `{"inst:1":"custom/a.log","inst:2":"logs/proxy.log.0"}`,
	}))
	p := svc.LogAcqPolicy("")
	require.Equal(t, "custom/a.log", p.PathOverrides["inst:1"])
	require.Equal(t, "logs/proxy.log.0", p.PathOverrides["inst:2"])
}

// TestLogAcqPolicyPathOverridesRejectsEscapingPaths 验证路径覆盖的越界路径被写入侧拒绝。
//
// 口径（2026-10-19 用户裁定「限定在工作目录内」）：覆盖值必须是**相对实例工作目录**的路径。
// 放行绝对路径时，持有 settings.write 的人可把任意实例的日志源指向宿主机任意可读文件
// （/etc/shadow 之类），Worker 会照读并写入 VL——CP 与 Worker 分属不同信任域，Worker 不应
// 无条件信任下发来的物理路径。`..` 逃逸同理。
func TestLogAcqPolicyPathOverridesRejectsEscapingPaths(t *testing.T) {
	cases := []struct {
		name string
		val  string
	}{
		{"绝对路径", `{"inst:1":"/etc/shadow"}`},
		{"用 .. 逃出工作目录", `{"inst:1":"../../etc/shadow"}`},
		{"清洗后仍逃出", `{"inst:1":"logs/../../../etc/shadow"}`},
		{"键不是 inst: 形态", `{"node:1":"logs/a.log"}`},
		{"空值", `{"inst:1":""}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewSettingsService(newSettingsTestDB(t), testConfig())
			err := svc.Update(map[string]string{SettingKeyLogAcqPathOverrides: tc.val})
			require.ErrorIs(t, err, ErrSettingValueInvalid, "越界路径应被写入校验拒绝")
			require.Empty(t, svc.EffectiveValue(SettingKeyLogAcqPathOverrides), "被拒绝的值不应落库")
		})
	}

	// 对照：工作目录内的相对路径必须放行，否则这条约束就把正常用法一起禁掉了。
	svc := NewSettingsService(newSettingsTestDB(t), testConfig())
	require.NoError(t, svc.Update(map[string]string{
		SettingKeyLogAcqPathOverrides: `{"inst:1":"logs/latest.log","inst:2":"sub/dir/a.log"}`,
	}))
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
// 只对「确有节点级消费方」的键开放节点级覆盖，即日志采集的 5 个键（它们经
// LogAcqPolicy(nodeUUID) 按节点合并后下发，节点级覆盖真会生效）。
// 其余键套 `@node:` 后缀必须被拒绝——否则会落一条「写入 200 但永不生效」的键，
// 属于最难排查的一类静默配置错误。
func TestLogAcqPolicyWorkerScopeWhitelist(t *testing.T) {
	svc := NewSettingsService(newSettingsTestDB(t), testConfig())

	// 放行：日志采集的 5 个键（心跳按节点下发，确有节点级消费方）。
	require.NoError(t, svc.Update(map[string]string{
		NodeScopedSettingKey(SettingKeyLogAcqRulesEnabled, "n1"):   "false",
		NodeScopedSettingKey(SettingKeyLogAcqRealtimeTail, "n1"):   "false",
		NodeScopedSettingKey(SettingKeyLogAcqGzImport, "n1"):       "true",
		NodeScopedSettingKey(SettingKeyLogAcqGzScanInterval, "n1"): "30s",
		NodeScopedSettingKey(SettingKeyLogAcqPathOverrides, "n1"):  `{"inst:1":"custom/a.log"}`,
	}), "日志采集键应支持节点级覆盖")

	// 拒绝：health.* / direct_probe.* 虽随心跳下发，但其解析器（HealthScanPolicy /
	// DirectProbeTimeouts）没有节点形参，全仓没有节点级消费方——放行等于落一条永不生效
	// 且不产生审计 action 的静默键（静默 + 无痕），必须写入时报错。
	for _, key := range []string{
		SettingKeyHealthScanInterval, SettingKeyHealthScanEnabled, SettingKeyHealthAction,
		SettingKeyHealthSelfHealMaxRestarts,
		SettingKeyDirectProbeSLPTimeout, SettingKeyDirectProbeQueryTimeout,
	} {
		err := svc.Update(map[string]string{NodeScopedSettingKey(key, "n1"): "60s"})
		require.ErrorIs(t, err, ErrSettingKeyNotWritable, "%s 无节点级消费方，应拒绝", key)
	}

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

// TestLogAcqPolicyNodeEmptyOverrideIsExplicitDisable 验证「节点级空串覆盖 = 显式关闭」，
// 而不是回落继承平台值（FR-503）。
//
// 空串是合法值：gz_scan_interval 空 = 关闭定时扫描、path_overrides 空 = 无覆盖；
// 前端节点面板把非布尔项渲染成普通输入框，**清空输入即写入空串**，故这条路径经界面可达。
// 历史缺陷：心跳按「值非空」判定覆盖、视图按「键存在」判定，于是界面显示「本节点已覆盖为
// 空/已关闭」，而该节点仍按平台周期（如 1h）在扫描，且运维**无法对单节点关闭定时扫描**——
// 这是最难排查的一类静默配置错误。
//
// 转红方式：把 mergeLogAcqValues 的 ok 判定改回 `!= ""`——本用例在「空串必须表达关闭」
// 与「视图/心跳自洽」两处同时变红。
func TestLogAcqPolicyNodeEmptyOverrideIsExplicitDisable(t *testing.T) {
	svc := NewSettingsService(newSettingsTestDB(t), testConfig())

	// 平台级：开 gz 导入、每 1h 扫描、带路径覆盖（缓存未命中/绝对路径场景）。
	require.NoError(t, svc.Update(map[string]string{
		SettingKeyLogAcqGzImport:       "true",
		SettingKeyLogAcqGzScanInterval: "1h",
		SettingKeyLogAcqPathOverrides:  `{"inst:1":"custom/a.log"}`,
	}))

	const node = "node-empty-override"
	// 节点级：显式清空扫描周期与路径覆盖 = 本节点关闭定时扫描、无路径覆盖。
	require.NoError(t, svc.Update(map[string]string{
		NodeScopedSettingKey(SettingKeyLogAcqGzScanInterval, node): "",
		NodeScopedSettingKey(SettingKeyLogAcqPathOverrides, node):  "",
	}))

	policy := svc.LogAcqPolicy(node)
	require.Zero(t, policy.GzScanInterval, "节点级空串必须表达「关闭定时扫描」，不得继承平台 1h")
	require.Empty(t, policy.PathOverrides, "节点级空串必须表达「无路径覆盖」，不得继承平台覆盖")
	require.True(t, policy.GzImportEnabled, "未被节点覆盖的键仍继承平台级")

	// 其它节点不受影响：仍按平台值。
	other := svc.LogAcqPolicy("node-other")
	require.Equal(t, time.Hour, other.GzScanInterval)
	require.Equal(t, "custom/a.log", other.PathOverrides["inst:1"])

	// 视图与心跳必须自洽：承认「已覆盖」，且生效值就是空（= 与下发结果一致）。
	view, err := svc.LogAcqSettingsForNode(node)
	require.NoError(t, err)
	item := logAcqSettingItemByKey(t, view, SettingKeyLogAcqGzScanInterval)
	require.True(t, item.Overridden, "空串覆盖仍是「已覆盖」")
	require.Equal(t, "", item.Override)
	require.Equal(t, "", item.Effective, "生效值必须等于心跳合并结果（空 = 关闭）")
	require.Equal(t, "1h", item.Platform, "平台列展示平台级值，不受节点覆盖影响")
	require.Equal(t, "", item.Default)
}

// logAcqSettingItemByKey 从设置视图里取一项（不存在则直接判失败）。
func logAcqSettingItemByKey(t *testing.T, view LogAcqSettingsView, key string) LogAcqSettingItem {
	t.Helper()
	for _, item := range view.Items {
		if item.Key == key {
			return item
		}
	}
	t.Fatalf("视图缺少配置项 %s", key)
	return LogAcqSettingItem{}
}
