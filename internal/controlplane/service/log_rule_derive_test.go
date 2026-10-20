package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestDeriveLogRuleKey 验证由实例的 role + 核心类型（coreKind）+ launch_spec.coreJar
// 推导日志采集规则名（FR-502 §3.1）。推导结果经 proto 下发给 Worker，决定活动日志的物理路径规则。
//
// 前两组用例（coreKind 为空）锁住**历史数据**的兼容口径：老实例的 launch_spec 没有
// coreKind，只能按 jar 名匹配，行为必须与本项落地前逐字一致。
func TestDeriveLogRuleKey(t *testing.T) {
	cases := []struct {
		name     string
		role     string
		coreKind string
		coreJar  string
		want     string
	}{
		// 代理系（历史数据：无 coreKind，按核心 jar 名区分 Bungee / Waterfall / Velocity）
		{"bungee 代理", "proxy", "", "BungeeCord.jar", "bungee"},
		{"bungee 代理（版本化文件名）", "proxy", "", "BungeeCord-1.20.jar", "bungee"},
		{"waterfall 代理", "proxy", "", "Waterfall.jar", "waterfall"},
		{"velocity 代理", "proxy", "", "velocity-3.3.0-SNAPSHOT-123.jar", "velocity"},
		{"代理但 jar 未识别", "proxy", "", "some-proxy.jar", "bukkit"},
		{"代理且 coreJar 为空", "proxy", "", "", "bukkit"},

		// 代理系（一键搭建：jar 名恒为 server.jar，只有 coreKind 能表达核心类型）
		{"bungee 代理（平台命名）", "proxy", "bungeecord", "server.jar", "bungee"},
		{"bungee 代理（coreKind 简写）", "proxy", "bungee", "server.jar", "bungee"},
		{"waterfall 代理（平台命名）", "proxy", "waterfall", "server.jar", "waterfall"},
		{"velocity 代理（平台命名）", "proxy", "velocity", "server.jar", "velocity"},
		{"coreKind 大小写与空白不敏感", "proxy", "  Velocity ", "server.jar", "velocity"},
		{"coreKind 未知时回退 jar 名", "proxy", "custom-proxy", "Waterfall.jar", "waterfall"},
		{"coreKind 未知且 jar 名未识别", "proxy", "custom-proxy", "server.jar", "bukkit"},

		// 后端：Paper / Spigot / Bukkit 一律 bukkit（coreKind 也不改变结论）
		{"paper 后端", "backend", "", "paper-1.20.1-196.jar", "bukkit"},
		{"spigot 后端", "backend", "", "spigot-1.20.1.jar", "bukkit"},
		{"后端且 coreJar 为空", "backend", "", "", "bukkit"},
		{"paper 后端（平台命名）", "backend", "paper", "server.jar", "bukkit"},
		{"spongeforge 后端（平台命名）", "backend", "spongeforge", "server.jar", "bukkit"},
		// 后端身份优先于核心类型：即便核心类型像代理也不特化（防误判）。
		{"后端带代理核心类型仍回退 bukkit", "backend", "bungeecord", "server.jar", "bukkit"},

		// 其他角色：回退 bukkit（不影响非 MC 实例）
		{"通用实例", "universal", "", "", "bukkit"},
		{"beacon 实例", "beacon", "", "", "bukkit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := deriveLogRuleKey(c.role, c.coreKind, c.coreJar)
			require.Equal(t, c.want, got,
				"role=%q coreKind=%q coreJar=%q 应推导为 %q", c.role, c.coreKind, c.coreJar, c.want)
		})
	}
}

// TestDeriveLogRuleKeyCaseInsensitive 验证识别不受大小写影响
// （不同核心发行物的文件名大小写并不统一）。
func TestDeriveLogRuleKeyCaseInsensitive(t *testing.T) {
	cases := []struct {
		coreJar string
		want    string
	}{
		{"bungeecord.jar", "bungee"},
		{"BUNGEECORD.JAR", "bungee"},
		{"waterfall-1.20.jar", "waterfall"},
		{"Velocity.jar", "velocity"},
	}
	for _, c := range cases {
		got := deriveLogRuleKey("proxy", "", c.coreJar)
		require.Equal(t, c.want, got, "coreJar=%q 应推导为 %q", c.coreJar, c.want)
	}
}

// TestDeriveLogRuleKeyWaterfallBeforeBungee 验证 Waterfall 不被误判为 Bungee。
//
// Waterfall 是 BungeeCord 的分支，其 jar 内部可能含 "Bungee" 字样，
// 但文件名以 Waterfall 标识自身——匹配必须优先 Waterfall。
func TestDeriveLogRuleKeyWaterfallBeforeBungee(t *testing.T) {
	got := deriveLogRuleKey("proxy", "", "Waterfall-BungeeCord-based.jar")
	require.Equal(t, "waterfall", got, "Waterfall 的识别必须优先于 Bungee")
}

// TestLogRuleKeyForInstancePrefersCoreKind 钉住「一键搭建的代理」这条主路径：
// 落盘名固定为 server.jar（见 provisionCoreJar），规则只能由 launch_spec.coreKind 推出。
//
// 转红方式：把 logRuleKeyForInstance 改回只读 spec.CoreJar——第一条用例立刻
// 从 bungee 变 bukkit（这正是审查发现的 FR-502 在生产主路径上静默失效）。
func TestLogRuleKeyForInstancePrefersCoreKind(t *testing.T) {
	svc := &InstanceService{}

	// 一键搭建的 BungeeCord 代理：jar 名不含 bungee 字样。
	provisioned := &model.Instance{
		Role:       model.InstanceRoleProxy,
		LaunchSpec: `{"memoryMb":1024,"jvmArgs":[],"coreJar":"server.jar","coreKind":"bungeecord","omitNogui":true}`,
	}
	require.Equal(t, "bungee", svc.logRuleKeyForInstance(provisioned),
		"一键搭建的代理必须靠 coreKind 推出规则（jar 名为 server.jar）")

	// 历史数据：无 coreKind，靠 jar 名匹配（行为不变）。
	legacy := &model.Instance{Role: model.InstanceRoleProxy, LaunchSpec: `{"coreJar":"BungeeCord.jar"}`}
	require.Equal(t, "bungee", svc.logRuleKeyForInstance(legacy), "历史数据须回退 jar 名匹配")

	// launch_spec 缺失/损坏：回退 bukkit，且不得因它阻断注册。
	require.Equal(t, "bukkit", svc.logRuleKeyForInstance(&model.Instance{Role: model.InstanceRoleProxy}))
	require.Equal(t, "bukkit", svc.logRuleKeyForInstance(&model.Instance{
		Role: model.InstanceRoleProxy, LaunchSpec: "{not-json",
	}))
}

// TestProvisionedLaunchSpecCarriesCoreKind 验证一键搭建（后端 + 代理）的 spec 组装：
// coreKind 必须来自已解析的核心，且**不参与落盘文件名与启动命令**。
func TestProvisionedLaunchSpecCarriesCoreKind(t *testing.T) {
	// 后端：核心类型 paper，落盘名仍是统一的 server.jar。
	backendSpec, err := launchSpecForProvision(ProvisionServerRequest{MemoryMb: 2048, JvmArgs: []string{"-XX:+UseG1GC"}},
		&CoreInfo{Type: "paper", MCVersion: "1.20.1"}, "linux")
	require.NoError(t, err)
	require.Equal(t, provisionCoreJar, backendSpec.CoreJar)
	require.Equal(t, "paper", backendSpec.CoreKind)
	backendCmd, err := deriveStartCommand(&backendSpec)
	require.NoError(t, err)
	require.Equal(t, "java -Xms2048M -Xmx2048M -XX:+UseG1GC -jar server.jar nogui", backendCmd)

	// 代理：核心类型 bungeecord，规则必须推出 bungee（这就是本波要修的生产缺陷）。
	proxySpec := launchSpecForProxy(ProvisionProxyRequest{MemoryMb: 1024, ProxyType: "bungeecord"},
		&CoreInfo{Type: "bungeecord"})
	require.Equal(t, provisionCoreJar, proxySpec.CoreJar)
	require.Equal(t, "bungeecord", proxySpec.CoreKind)
	proxyCmd, err := deriveStartCommand(&proxySpec)
	require.NoError(t, err)
	require.Equal(t, "java -Xms1024M -Xmx1024M -jar server.jar", proxyCmd,
		"代理不接受 nogui；coreKind 不得进入启动命令")

	// coreKind 只是元数据：把它清空后启动命令必须逐字相同（不参与派生）。
	withoutKind := proxySpec
	withoutKind.CoreKind = ""
	noKindCmd, err := deriveStartCommand(&withoutKind)
	require.NoError(t, err)
	require.Equal(t, proxyCmd, noKindCmd)

	// 端到端：组装出的 spec 经 JSON 落库后再推导（与实例注册路径同口径）。
	restored := roundTripLaunchSpec(t, proxySpec)
	require.Equal(t, "bungee", deriveLogRuleKey(string(model.InstanceRoleProxy), restored.CoreKind, restored.CoreJar),
		"一键搭建的代理经落库往返后仍须推出 bungee 规则")
}

// TestBackfillLaunchSpecCoreKind 验证重建路径为历史实例补齐核心类型（不覆盖其它字段）。
func TestBackfillLaunchSpecCoreKind(t *testing.T) {
	db := newSettingsTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Instance{}))

	inst := &model.Instance{
		NodeID: 1, Name: "bungee-legacy", Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleProxy, ProcessType: model.ProcessTypeDaemon,
		Status: model.InstanceStatusDamaged, ServerPort: 25565,
		LaunchSpec: `{"memoryMb":1024,"jvmArgs":["-XX:+UseG1GC"],"coreJar":"server.jar","omitNogui":true}`,
	}
	require.NoError(t, db.Create(inst).Error)

	require.NoError(t, backfillLaunchSpecCoreKind(db, inst, &CoreInfo{Type: "bungeecord"}))

	var reloaded model.Instance
	require.NoError(t, db.First(&reloaded, inst.ID).Error)
	spec, err := parseLaunchSpec(reloaded.LaunchSpec)
	require.NoError(t, err)
	require.Equal(t, "bungeecord", spec.CoreKind)
	require.Equal(t, 1024, spec.MemoryMb, "补齐不得改动其余字段")
	require.Equal(t, []string{"-XX:+UseG1GC"}, spec.JvmArgs)
	require.True(t, spec.OmitNogui)

	// 已有 coreKind 时不得被重建参数改写（避免把运维纠正过的信息覆盖掉）。
	require.NoError(t, backfillLaunchSpecCoreKind(db, &reloaded, &CoreInfo{Type: "velocity"}))
	var again model.Instance
	require.NoError(t, db.First(&again, inst.ID).Error)
	specAgain, err := parseLaunchSpec(again.LaunchSpec)
	require.NoError(t, err)
	require.Equal(t, "bungeecord", specAgain.CoreKind)

	// core 为空（解析结果缺失）时不写任何内容。
	blank := &model.Instance{NodeID: 1, Name: "blank", LaunchSpec: `{"coreJar":"server.jar"}`}
	require.NoError(t, backfillLaunchSpecCoreKind(db, blank, nil))
	require.Equal(t, `{"coreJar":"server.jar"}`, blank.LaunchSpec)
}

// roundTripLaunchSpec 把 spec 序列化后再解析，模拟实例落库/读取的往返。
func roundTripLaunchSpec(t *testing.T, spec LaunchSpec) *LaunchSpec {
	t.Helper()
	raw, err := json.Marshal(spec)
	require.NoError(t, err)
	parsed, err := parseLaunchSpec(string(raw))
	require.NoError(t, err)
	require.NotNil(t, parsed)
	return parsed
}
