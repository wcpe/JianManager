package ingest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// newInstanceRuleTestManager 构造一个用于规则路径断言的 Manager，
// 返回 manager 与一个独立的实例工作目录。
func newInstanceRuleTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	vl, _ := newProjectionVL(t)
	root := t.TempDir()
	cat := catalog.New(nil)
	m, err := newTestManager(t, Options{Root: root, VL: vl, Catalog: cat, Journal: cat.Journal()})
	require.NoError(t, err)
	return m, t.TempDir()
}

// sourceByID 取出指定 LogSourceID 已登记的源配置（同一实例只有一个 FILE_PRIMARY 源）。
func sourceByID(t *testing.T, m *Manager, logSourceID string) SourceConfig {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sc := range m.state.SourceConfigs {
		if sc.LogSourceID == logSourceID {
			return sc
		}
	}
	t.Fatalf("未找到已登记的源 %q", logSourceID)
	return SourceConfig{}
}

// TestRegisterInstanceBungeeRuleUsesRootProxyLog 验证 bungee 规则下
// 实例的活动日志被解析到工作目录根的 proxy.log.0，而非 logs/latest.log。
//
// 这是 FR-502 的核心补齐：BungeeCord 的日志在根目录，历史上的硬编码路径
// 永远发现不了它（生产实测 bungee-main 在 VL 中 0 条）。
func TestRegisterInstanceBungeeRuleUsesRootProxyLog(t *testing.T) {
	m, work := newInstanceRuleTestManager(t)

	require.NoError(t, m.RegisterInstance(InstanceRegistration{
		UUID:       "uuid-bungee",
		TargetID:   "inst:900",
		Generation: "holder-g1",
		Mode:       "FILE_PRIMARY",
		WorkDir:    work,
		RuleKey:    RuleKeyBungee,
	}))

	sc := sourceByID(t, m, "inst:900/file")
	require.Equal(t, filepath.Join(work, "proxy.log.0"), sc.Path,
		"bungee 规则的活动日志应为工作目录根的 proxy.log.0")
	require.Equal(t, filepath.Join(work, "proxy.log.[1-9]*"), sc.ArchiveGlob,
		"bungee 规则的归档 glob 应为收窄后的 proxy.log.[1-9]*（纯文本轮转可导入，"+
			"见 TestBungeeRuleImportsPlainTextRotations）")
}

// TestRegisterInstanceBungeeRuleImportsPlainTextRotations 验证 bungee 源**能**采集纯文本轮转历史，
// 且不留下不可自愈的缺口（本能力的端到端行为级回归）。
//
// 为什么必须行为级：旧用例只断言归档 glob 的字符串形态，而缺陷的后果在账本里——
// 修复前 `proxy.log.1` 会被登记为 kind=gzip 段、记一条 `ARCHIVE_GZIP_CORRUPT` 缺口
// （该原因不可自动消解 ⇒ 源一旦被暂停即永久停采），于是当时的结论是「glob 必须为空」。
// 本用例直接断言新口径的三件事：
//   - 轮转正文**确实**进了事件段（能力生效，不是又被跳过）；
//   - 分段形态是 rotated_text 而**不是** gzip（形态判定按文件头，登记如实）；
//   - 全程零缺口（尤其不得有 ARCHIVE_GZIP_CORRUPT）——这是「源不会被永久停采」的判据。
//
// 同时覆盖退化形态：`proxy.log.1.lck`（java.util.logging 的 0 字节锁文件，会被 glob 匹配）
// 必须被处置为「空归档」而不是「损坏的 gzip 归档」。
func TestRegisterInstanceBungeeRuleImportsPlainTextRotations(t *testing.T) {
	m, work := newInstanceRuleTestManager(t)
	// BungeeCord 的轮转产物：纯文本，数字后缀，位于工作目录根。
	// 活动日志写两行（末行会被事件边界留作悬挂），使首行本轮就被闭合、可被断言。
	require.NoError(t, os.WriteFile(filepath.Join(work, "proxy.log.0"),
		[]byte("[12:00:02] [INFO] live line\n[12:00:03] [INFO] still pending at file tail\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(work, "proxy.log.1"), []byte("[12:00:00] [INFO] rotated plain text one\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(work, "proxy.log.2"), []byte("[12:00:01] [INFO] rotated plain text two\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(work, "proxy.log.1.lck"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(work, "proxy.log.0.lck"), nil, 0o600))

	require.NoError(t, m.RegisterInstance(InstanceRegistration{
		UUID: "uuid-bungee-archives", TargetID: "inst:920", Generation: "holder-g1",
		Mode: "FILE_PRIMARY", WorkDir: work, RuleKey: RuleKeyBungee,
	}))
	m.pollOnce()

	key := ledger.SourceKey{LogSourceID: "inst:920/file", SourceGeneration: "holder-g1"}
	m.mu.Lock()
	pipe := m.pipes[key.String()]
	m.mu.Unlock()
	require.NotNil(t, pipe)
	entry := pipe.Ledger().Get(key)
	require.NotNil(t, entry)
	kindByPath := map[string]ledger.SegmentKind{}
	for _, seg := range entry.Segments {
		kindByPath[filepath.Base(seg.Path)] = seg.Kind
		require.NotEqual(t, ledger.SegmentGzip, seg.Kind,
			"纯文本轮转文件不得被登记为 gzip 归档段（path=%s）", seg.Path)
		require.Empty(t, seg.ImportError,
			"纯文本轮转文件的导入不得留下失败登记（path=%s）", seg.Path)
	}
	require.Equal(t, ledger.SegmentRotatedText, kindByPath["proxy.log.1"],
		"轮转文件必须被登记为 rotated_text（形态按文件头判定）")
	require.Equal(t, ledger.SegmentRotatedText, kindByPath["proxy.log.2"])
	require.Equal(t, ledger.SegmentRotatedText, kindByPath["proxy.log.1.lck"],
		"0 字节锁文件会被 glob 匹配，必须按「空归档」处置而不是「损坏的 gzip」")
	require.NotContains(t, kindByPath, "proxy.log.0.lck",
		"当前活跃日志的锁文件不属轮转历史（glob 已收窄排除）")
	for _, gap := range entry.Gaps {
		require.NotEqual(t, ledger.GapReasonArchiveGzipCorrupt, gap.Reason,
			"不得为纯文本轮转文件产生不可自愈的 ARCHIVE_GZIP_CORRUPT 缺口（detail=%s）", gap.Detail)
	}
	require.Empty(t, entry.Gaps, "纯文本轮转的常规路径不得产生任何缺口")

	// 当前活跃日志与轮转历史都必须进事件段。
	text := eventMessageText(t, m, key.String())
	require.Contains(t, text, "live line", "bungee 的活动日志必须仍被采集")
	require.Contains(t, text, "rotated plain text one", "轮转历史必须被导入（本能力的核心）")
	require.Contains(t, text, "rotated plain text two")
}

// TestRegisterInstanceBukkitRuleKeepsHistoricalPaths 验证 bukkit 规则
// （以及空规则名的回退）保持历史路径逐字不变——FR-502 的向后兼容判据。
func TestRegisterInstanceBukkitRuleKeepsHistoricalPaths(t *testing.T) {
	for _, ruleKey := range []string{RuleKeyBukkit, ""} {
		name := ruleKey
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			m, work := newInstanceRuleTestManager(t)

			require.NoError(t, m.RegisterInstance(InstanceRegistration{
				UUID:       "uuid-bukkit",
				TargetID:   "inst:901",
				Generation: "holder-g1",
				Mode:       "FILE_PRIMARY",
				WorkDir:    work,
				RuleKey:    ruleKey,
			}))

			sc := sourceByID(t, m, "inst:901/file")
			require.Equal(t, filepath.Join(work, "logs", "latest.log"), sc.Path,
				"bukkit 规则/空规则的路径必须与历史逐字一致")
			require.Equal(t, filepath.Join(work, "logs", "*.gz"), sc.ArchiveGlob)
		})
	}
}

// TestRegisterInstanceRelativePathOverridesRule 验证实例级路径覆盖优先于规则默认。
func TestRegisterInstanceRelativePathOverridesRule(t *testing.T) {
	m, work := newInstanceRuleTestManager(t)

	require.NoError(t, m.RegisterInstance(InstanceRegistration{
		UUID:         "uuid-override",
		TargetID:     "inst:902",
		Generation:   "holder-g1",
		Mode:         "FILE_PRIMARY",
		WorkDir:      work,
		RuleKey:      RuleKeyBungee,   // 规则默认 proxy.log.0
		RelativePath: "custom/my.log", // 覆盖为自定义路径
	}))

	sc := sourceByID(t, m, "inst:902/file")
	require.Equal(t, filepath.Join(work, "custom", "my.log"), sc.Path,
		"实例级相对路径覆盖应优先于规则默认值")
}

// TestRegisterInstanceVelocityRuleUsesLogsSubdir 验证 velocity 规则的路径。
func TestRegisterInstanceVelocityRuleUsesLogsSubdir(t *testing.T) {
	m, work := newInstanceRuleTestManager(t)

	require.NoError(t, m.RegisterInstance(InstanceRegistration{
		UUID:       "uuid-velocity",
		TargetID:   "inst:903",
		Generation: "holder-g1",
		Mode:       "FILE_PRIMARY",
		WorkDir:    work,
		RuleKey:    RuleKeyVelocity,
	}))

	sc := sourceByID(t, m, "inst:903/file")
	require.Equal(t, filepath.Join(work, "logs", "velocity.log"), sc.Path)
	require.Equal(t, filepath.Join(work, "logs", "*.gz"), sc.ArchiveGlob)
}

// TestRegisterInstanceAbsolutePathOverride 验证覆盖路径为绝对路径时原样使用
// （不拼到工作目录之下）。filepath.Join 不会因第二参数是绝对路径而丢弃前者，
// 必须显式判断——此测试即锁定该行为。
func TestRegisterInstanceAbsolutePathOverride(t *testing.T) {
	m, work := newInstanceRuleTestManager(t)
	abs := filepath.Join(t.TempDir(), "elsewhere", "server.log")

	require.NoError(t, m.RegisterInstance(InstanceRegistration{
		UUID:         "uuid-abs",
		TargetID:     "inst:905",
		Generation:   "holder-g1",
		Mode:         "FILE_PRIMARY",
		WorkDir:      work,
		RuleKey:      RuleKeyBungee,
		RelativePath: abs,
	}))

	sc := sourceByID(t, m, "inst:905/file")
	require.Equal(t, abs, sc.Path,
		"绝对路径覆盖必须原样使用，不得被拼到工作目录之下")
	require.NotContains(t, sc.Path, work,
		"绝对路径覆盖的结果不应包含实例工作目录前缀")
}

// TestInstanceRuleBindingSurvivesRestart 验证规则名与路径覆盖在 Worker 重启后仍生效。
//
// 这是 FR-502 的关键回归：新字段若不随 binding 持久化，重启后规则丢失，
// 非 bukkit 布局的实例（如 BungeeCord）会退回 logs/latest.log 而重新采集不到日志
// ——即「修复只在重启前有效」。本用例锁定持久化往返（写库 → 读库还原）。
func TestInstanceRuleBindingSurvivesRestart(t *testing.T) {
	vl, _ := newProjectionVL(t)
	root := t.TempDir()
	cat := catalog.New(nil)
	m, err := newTestManager(t, Options{Root: root, VL: vl, Catalog: cat, Journal: cat.Journal()})
	require.NoError(t, err)
	work := t.TempDir()

	// 登记是同步落库的（FR-499：读写 state.Instances + 幂等建管道 + persist）。
	require.NoError(t, m.RegisterInstance(InstanceRegistration{
		UUID:         "uuid-restart-bungee",
		TargetID:     "inst:906",
		Generation:   "holder-g1",
		Mode:         "FILE_PRIMARY",
		WorkDir:      work,
		RuleKey:      RuleKeyBungee,
		RelativePath: "custom/bungee.log",
	}))
	require.NoError(t, m.Stop())

	// 重启读回：规则名与路径覆盖必须随绑定往返。
	restarted, err := newTestManager(t, Options{Root: root, VL: vl, Catalog: catalog.New(cat.Journal())})
	require.NoError(t, err)
	binding, ok := restarted.state.Instances["uuid-restart-bungee"]
	require.True(t, ok, "重启后应还原实例绑定")
	require.Equal(t, RuleKeyBungee, binding.RuleKey,
		"规则名必须随 binding 持久化——否则重启后 BungeeCord 采集退回错误路径")
	require.Equal(t, "custom/bungee.log", binding.RelativePath,
		"实例级路径覆盖必须随 binding 持久化")
}

// TestRegisterInstanceRuleDoesNotAffectSourceIdentity 验证规则只影响物理路径发现，
// 不影响源标识（FR-502 §3.5）：sourceID 与 storage namespace 保持原样。
func TestRegisterInstanceRuleDoesNotAffectSourceIdentity(t *testing.T) {
	m, work := newInstanceRuleTestManager(t)

	require.NoError(t, m.RegisterInstance(InstanceRegistration{
		UUID:       "uuid-identity",
		TargetID:   "inst:904",
		Generation: "holder-g1",
		Mode:       "FILE_PRIMARY",
		WorkDir:    work,
		RuleKey:    RuleKeyBungee,
	}))

	sc := sourceByID(t, m, "inst:904/file")
	require.Equal(t, "inst:904/file", sc.LogSourceID,
		"源标识不得随规则变化（仍是 <targetID>/file）")
	require.Equal(t, "inst:904", sc.StorageNamespace,
		"storage namespace 不得随规则变化")
}

// 变异验证记录（2026-10-15 复核「glob 置空」；2026-10-16 纯文本轮转导入上线后**该结论已反转**）：
//
//   A2 —— 当时把 bungee 规则的 ArchiveGlob 从空改回 `proxy.log.[1-9]*`：
//     旧用例 `TestRegisterInstanceBungeeRuleDoesNotImportPlainTextRotations` → FAIL
//     （"纯文本轮转文件不得被登记为 gzip 归档段"）。
//     注意：那次转红证明的是「**当时**没有任何纯文本导入实现」——匹配到的文件只会以
//     ARCHIVE_GZIP_CORRUPT 失败（不可自动消解 ⇒ 源暂停即永久停采），故当时如实置空。
//
//   本项（纯文本轮转归档导入）落地后，判据从「有没有实现」变成「形态是否如实登记」，
//   同一个 glob 现在是**正确**的：纯文本轮转走纯文本实现并登记 kind=rotated_text。
//   反向变异见 acquire/text_rotated_archive_test.go 与 pipeline/text_archive_import_test.go
//   的「转红方式」注释（形态分流回退、新增 kind 不参与归档类判定）。

// TestBungeeTextRotationIsNotRediscoveredAsPending 验证纯文本轮转归档一旦导入，就不再被判为
// 「待导入」（新增归档形态必须参与归档类判定）。
//
// 不改判定读侧的后果（真机可达）：每轮采集都把它当待导入重新发现，且 `SetRotateTarget` 会持续
// 指向**已导入**的归档，使 tailer 的轮转目标落到历史文件上——该归档会被反复重试、永远无法重新
// 标记为已导入。
func TestBungeeTextRotationIsNotRediscoveredAsPending(t *testing.T) {
	m, work := newInstanceRuleTestManager(t)
	require.NoError(t, os.WriteFile(filepath.Join(work, "proxy.log.1"),
		[]byte("[10:00:00] [Proxy thread/INFO]: rotated one\n[10:00:01] [Proxy thread/INFO]: rotated two\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(work, "proxy.log.0"),
		[]byte("[11:00:00] [Proxy thread/INFO]: live line\n"), 0o600))

	require.NoError(t, m.RegisterInstance(InstanceRegistration{
		UUID: "uuid-text-rot", TargetID: "inst:930", Generation: "holder-g1",
		Mode: "FILE_PRIMARY", WorkDir: work, RuleKey: RuleKeyBungee,
	}))

	// 首轮：该纯文本轮转归档应被发现并导入（走 ImportArchivesNow 的「发现 + 待导入」口径）。
	scanned, imported, err := m.ImportArchivesNow("", "test-operator")
	require.NoError(t, err)
	require.Equal(t, 1, scanned, "首轮应发现 1 份待导入归档（纯文本轮转）")
	require.Equal(t, 1, imported, "纯文本轮转归档必须能被导入")

	// 次轮：同一归档已记「已导入」⇒ 不得再被判为待导入（否则每轮重发现，且轮转目标被钉在历史文件上）。
	scannedAgain, _, err := m.ImportArchivesNow("", "test-operator")
	require.NoError(t, err)
	require.Zero(t, scannedAgain, "已导入的纯文本轮转归档不得被反复判为待导入")
}
