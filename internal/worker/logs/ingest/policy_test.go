package ingest

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// FR-503 日志采集策略：默认值安全、幂等、持久化往返、以及登记路径的策略收敛。

// TestAcqPolicyZeroValueManagerIsSafe 验证「从未下发/加载」时按默认策略取值。
//
// 零值 Manager（未设置过策略）绝不能表达成「规则关闭」——那会让所有实例退回
// 历史硬编码路径，BungeeCord 之类立刻采集不到日志。这是本项最危险的误读方向。
func TestAcqPolicyZeroValueManagerIsSafe(t *testing.T) {
	var m Manager
	got := m.AcqPolicySnapshot()

	require.True(t, got.RulesEnabled, "未配置时必须按默认：规则引擎开")
	require.True(t, got.RealtimeTail, "未配置时必须按默认：实时采集开")
	require.True(t, got.GzImportEnabled,
		"未配置时必须按默认：gz 导入总开关开——关掉它会让 worker.yml 的 archive_scan_interval 基线被静默短路")
	require.Zero(t, got.GzScanInterval, "默认未配周期 ⇒ 折算为「未配置」，是否扫描由 worker.yml 基线决定")
}

// TestSetAcqPolicyIdempotent 验证同值重复设置不产生变更（心跳每拍调用）。
func TestSetAcqPolicyIdempotent(t *testing.T) {
	m, _ := newInstanceRuleTestManager(t)
	p := DefaultAcqPolicy()
	p.RealtimeTail = false

	require.True(t, m.SetAcqPolicy(p), "首次设置应报告变化")
	require.False(t, m.SetAcqPolicy(p), "同值重复设置不应报告变化")
	require.False(t, m.SetAcqPolicy(cloneAcqPolicy(p)), "等价副本也不应报告变化")

	p.PathOverrides = map[string]string{"inst:1": "a.log"}
	require.True(t, m.SetAcqPolicy(p), "路径覆盖变化应报告变化")
	require.False(t, m.SetAcqPolicy(p), "同值重复设置不应报告变化")
}

// TestAcqPolicySnapshotIsCopy 验证快照是副本：调用方改返回值不影响内部生效值。
func TestAcqPolicySnapshotIsCopy(t *testing.T) {
	m, _ := newInstanceRuleTestManager(t)
	require.True(t, m.SetAcqPolicy(AcqPolicy{
		RulesEnabled:  true,
		PathOverrides: map[string]string{"inst:1": "a.log"},
	}))

	got := m.AcqPolicySnapshot()
	got.PathOverrides["inst:1"] = "mutated.log"
	got.PathOverrides["inst:2"] = "added.log"

	again := m.AcqPolicySnapshot()
	require.Equal(t, "a.log", again.PathOverrides["inst:1"], "改快照不应影响内部状态")
	require.NotContains(t, again.PathOverrides, "inst:2")
}

// TestSetAcqPolicyNormalizesNegativeInterval 验证负数周期归一为关闭（=0），
// 与写入侧「留空/负数 = 关闭」的口径一致。
func TestSetAcqPolicyNormalizesNegativeInterval(t *testing.T) {
	m, _ := newInstanceRuleTestManager(t)
	require.True(t, m.SetAcqPolicy(AcqPolicy{RulesEnabled: true, GzScanInterval: -time.Minute}))
	require.Zero(t, m.AcqPolicySnapshot().GzScanInterval)
}

// TestAcqPolicyPersistenceRoundTrip 验证持久化往返（Worker 重启后配置不丢）。
func TestAcqPolicyPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := AcqPolicy{
		RulesEnabled:    false,
		RealtimeTail:    false,
		GzImportEnabled: true,
		GzScanInterval:  90 * time.Minute,
		PathOverrides:   map[string]string{"inst:7": "custom/x.log", "inst:8": "/abs/y.log"},
	}
	require.NoError(t, SaveAcqPolicy(dir, want))

	got, ok, err := LoadAcqPolicy(dir)
	require.NoError(t, err)
	require.True(t, ok, "文件存在时应报告已加载")
	require.True(t, AcqPolicyEqual(want, got), "往返后策略必须逐字段一致（含 duration 与覆盖映射）")
}

// TestLoadAcqPolicyMissingFileIsNotAnError 验证首次启动（无快照）不报错，返回默认。
func TestLoadAcqPolicyMissingFileIsNotAnError(t *testing.T) {
	got, ok, err := LoadAcqPolicy(t.TempDir())
	require.NoError(t, err, "文件不存在不是错误（首次启动的正常路径）")
	require.False(t, ok)
	require.True(t, AcqPolicyEqual(DefaultAcqPolicy(), got))
}

// TestLoadAcqPolicyCorruptFileReturnsErrorAndFallback 验证损坏文件：返回错误供调用方告警，
// 同时给出默认策略（调用方据此继续启动，绝不阻断——配置问题不该让节点离线）。
func TestLoadAcqPolicyCorruptFileReturnsErrorAndFallback(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, acqPolicyFileName), []byte("{not-json"), 0o644))

	got, ok, err := LoadAcqPolicy(dir)
	require.Error(t, err, "损坏快照应报告错误（调用方据此告警）")
	require.False(t, ok)
	require.True(t, AcqPolicyEqual(DefaultAcqPolicy(), got), "损坏时必须回退默认而非零值（零值=关闭规则）")
}

// TestLoadAcqPolicyInvalidScanIntervalFallsBackToOff 验证单字段损坏不拖垮整份策略。
func TestLoadAcqPolicyInvalidScanIntervalFallsBackToOff(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, acqPolicyFileName),
		[]byte(`{"rules_enabled":true,"realtime_tail":false,"gz_import_enabled":true,"gz_scan_interval":"bogus"}`), 0o644))

	got, ok, err := LoadAcqPolicy(dir)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, got.RulesEnabled, "其余字段应正常还原")
	require.False(t, got.RealtimeTail)
	require.True(t, got.GzImportEnabled)
	require.Zero(t, got.GzScanInterval, "非法周期按关闭处理，且不丢弃整份策略")
}

// TestRegisterInstancePolicyRulesDisabledFallsBackToHistoricalLayout 验证规则引擎关闭的语义：
// 忽略 CP 下发的规则名，回退历史默认布局（bukkit）。
//
// 这是「应急回退档」：规则表本身出问题时，一键回到引入规则前的行为。
func TestRegisterInstancePolicyRulesDisabledFallsBackToHistoricalLayout(t *testing.T) {
	m, work := newInstanceRuleTestManager(t)
	require.True(t, m.SetAcqPolicy(AcqPolicy{RulesEnabled: false, RealtimeTail: true}))

	require.NoError(t, m.RegisterInstance(InstanceRegistration{
		UUID:       "uuid-rules-off",
		TargetID:   "inst:910",
		Generation: "holder-g1",
		Mode:       "FILE_PRIMARY",
		WorkDir:    work,
		RuleKey:    RuleKeyBungee, // 会被策略忽略
	}))

	sc := sourceByID(t, m, "inst:910/file")
	require.Equal(t, filepath.Join(work, "logs", "latest.log"), sc.Path,
		"规则引擎关闭时应回退历史默认布局，而不是继续按 bungee 规则解析")
}

// TestRegisterInstancePolicyPathOverrideWins 验证平台侧路径覆盖优先于 CP 下发的相对路径。
//
// 覆盖档顺序（高 → 低）：策略 path_overrides > CP 下发的 relative_path > 规则默认。
func TestRegisterInstancePolicyPathOverrideWins(t *testing.T) {
	m, work := newInstanceRuleTestManager(t)
	require.True(t, m.SetAcqPolicy(AcqPolicy{
		RulesEnabled:  true,
		PathOverrides: map[string]string{"inst:911": "policy/override.log"},
	}))

	require.NoError(t, m.RegisterInstance(InstanceRegistration{
		UUID:         "uuid-policy-override",
		TargetID:     "inst:911",
		Generation:   "holder-g1",
		Mode:         "FILE_PRIMARY",
		WorkDir:      work,
		RuleKey:      RuleKeyBungee,
		RelativePath: "cp/derived.log", // 应被策略覆盖压过
	}))

	sc := sourceByID(t, m, "inst:911/file")
	require.Equal(t, filepath.Join(work, "policy", "override.log"), sc.Path,
		"平台配置的实例路径覆盖必须优先于 CP 下发的相对路径")
}

// TestRegisterInstancePolicyChangeDoesNotMoveExistingSource 验证策略变更**不**影响已在采实例。
//
// 这是 spec §3.2 的硬约束：运行中切换路径会导致账本断裂，必须避免。
// 策略只影响后续登记/重登记的实例参数。
func TestRegisterInstancePolicyChangeDoesNotMoveExistingSource(t *testing.T) {
	m, work := newInstanceRuleTestManager(t)

	require.NoError(t, m.RegisterInstance(InstanceRegistration{
		UUID:       "uuid-existing",
		TargetID:   "inst:912",
		Generation: "holder-g1",
		Mode:       "FILE_PRIMARY",
		WorkDir:    work,
		RuleKey:    RuleKeyBungee,
	}))
	before := sourceByID(t, m, "inst:912/file").Path
	require.Equal(t, filepath.Join(work, "proxy.log.0"), before)

	// 之后任意改策略：关规则、加覆盖——都不应动已在采实例的源路径。
	require.True(t, m.SetAcqPolicy(AcqPolicy{
		RulesEnabled:  false,
		PathOverrides: map[string]string{"inst:912": "moved/elsewhere.log"},
	}))

	after := sourceByID(t, m, "inst:912/file").Path
	require.Equal(t, before, after,
		"策略变更不得改变已在采实例的源路径（避免账本断裂）；只对后续登记生效")
}

// TestRegisterInstancePathOverrideOnlyForMatchingTarget 验证覆盖按 targetID 精确匹配，
// 不会误伤其它实例。
func TestRegisterInstancePathOverrideOnlyForMatchingTarget(t *testing.T) {
	m, work := newInstanceRuleTestManager(t)
	require.True(t, m.SetAcqPolicy(AcqPolicy{
		RulesEnabled:  true,
		PathOverrides: map[string]string{"inst:913": "only/this.log"},
	}))

	require.NoError(t, m.RegisterInstance(InstanceRegistration{
		UUID: "uuid-a", TargetID: "inst:913", Generation: "holder-g1",
		Mode: "FILE_PRIMARY", WorkDir: work, RuleKey: RuleKeyBukkit,
	}))
	require.NoError(t, m.RegisterInstance(InstanceRegistration{
		UUID: "uuid-b", TargetID: "inst:914", Generation: "holder-g1",
		Mode: "FILE_PRIMARY", WorkDir: work, RuleKey: RuleKeyBukkit,
	}))

	require.Equal(t, filepath.Join(work, "only", "this.log"),
		sourceByID(t, m, "inst:913/file").Path, "命中的 targetID 应使用覆盖路径")
	require.Equal(t, filepath.Join(work, "logs", "latest.log"),
		sourceByID(t, m, "inst:914/file").Path, "未命中的 targetID 应保持规则默认")
}
