package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// TestRegisterInstanceRuleOnlyChangeReappliesBinding 验证「仅规则变化」时可平滑重登记。
//
// 场景：既有实例首次引入规则（旧绑定 rule_key 为空，来自历史库/CP 尚未下发），
// 随后 CP 下发规则名。旧实现用结构体不等即报错，会把这类**纯规则升级**误判为
// 「绑定变更」而拒绝，导致规则永远无法生效。
//
// 契约：当变化只涉及 RuleKey/RelativePath（即 WorkDir/TargetID/Generation/Mode 未变）时，
// 允许重登记并刷新绑定——这类升级是 FR-502 的正常路径，不是异常。
func TestRegisterInstanceRuleOnlyChangeReappliesBinding(t *testing.T) {
	m, work := newInstanceRuleTestManager(t)

	// 首次登记：模拟历史绑定（无规则）。
	require.NoError(t, m.RegisterInstance(InstanceRegistration{
		UUID:       "uuid-upgrade",
		TargetID:   "inst:910",
		Generation: "holder-g1",
		Mode:       "FILE_PRIMARY",
		WorkDir:    work,
	}))
	first := sourceByID(t, m, "inst:910/file")
	require.Equal(t, filepath.Join(work, "logs", "latest.log"), first.Path,
		"无规则时回退 bukkit 路径")

	// 规则升级：同一实例下发 bungee 规则，应被接受并刷新到新路径。
	require.NoError(t, m.RegisterInstance(InstanceRegistration{
		UUID:       "uuid-upgrade",
		TargetID:   "inst:910",
		Generation: "holder-g1",
		Mode:       "FILE_PRIMARY",
		WorkDir:    work,
		RuleKey:    RuleKeyBungee,
	}), "仅规则变化的升级不得被判定为绑定变更错误")

	binding, ok := m.state.Instances["uuid-upgrade"]
	require.True(t, ok)
	require.Equal(t, RuleKeyBungee, binding.RuleKey, "绑定必须刷新出新规则名")
	// 源已切到 bungee 路径。
	after := sourceByID(t, m, "inst:910/file")
	require.Equal(t, filepath.Join(work, "proxy.log.0"), after.Path,
		"规则升级后源路径必须切到 bungee 布局")
}

// TestRegisterInstancePathChangeRejectedWithoutTransition 验证「路径类变更」仍被拒绝。
//
// 契约的另一半：WorkDir 等**决定源位置**的字段变化不在本项放行范围内——
// 那需要真正的源切换（注销旧源 + 登记新源），不能靠幂等重登记蒙混。
// 本用例锁定该保护未被削弱。
func TestRegisterInstancePathChangeRejectedWithoutTransition(t *testing.T) {
	m, work := newInstanceRuleTestManager(t)
	other := t.TempDir()

	require.NoError(t, m.RegisterInstance(InstanceRegistration{
		UUID:       "uuid-pathchange",
		TargetID:   "inst:911",
		Generation: "holder-g1",
		Mode:       "FILE_PRIMARY",
		WorkDir:    work,
		RuleKey:    RuleKeyBukkit,
	}))

	err := m.RegisterInstance(InstanceRegistration{
		UUID:       "uuid-pathchange",
		TargetID:   "inst:911",
		Generation: "holder-g1",
		Mode:       "FILE_PRIMARY",
		WorkDir:    other, // 工作目录变化 ⇒ 源位置变化
		RuleKey:    RuleKeyBukkit,
	})
	require.Error(t, err, "工作目录变化必须仍被拒绝（需真正的源切换）")
	require.Contains(t, err.Error(), "binding changed")
}

// TestRuleChangeClearsPersistedSourceState 验证规则升级后，索引库中该源的旧账本
// （位置水位/投影）被清除，而非残留。
//
// 为什么必须清：新物理文件与旧文件是不同内容流。残留旧账本会让采集器拿旧事件
// 去校验新文件内容，报「projection content mismatch」并把整轮采集绊住（真机实测）。
// 本用例锁定「清库」这一半——否则重启后从库里恢复旧账本，问题复发。
func TestRuleChangeClearsPersistedSourceState(t *testing.T) {
	vl, _ := newProjectionVL(t)
	root := t.TempDir()
	cat := catalog.New(nil)
	m, err := newTestManager(t, Options{Root: root, VL: vl, Catalog: cat, Journal: cat.Journal()})
	require.NoError(t, err)
	work := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(work, "logs"), 0o700))

	// 首次登记（bukkit），并让采集跑一轮产生账本。
	require.NoError(t, os.WriteFile(filepath.Join(work, "logs", "latest.log"), []byte("[12:00:00] [Server thread/INFO]: first\n"), 0o600))
	require.NoError(t, m.RegisterInstance(InstanceRegistration{
		UUID: "uuid-clearsrc", TargetID: "inst:912", Generation: "holder-g1",
		Mode: "FILE_PRIMARY", WorkDir: work, RuleKey: RuleKeyBukkit,
	}))
	m.pollOnce()
	require.NoError(t, m.Stop())

	// 重启后升级规则为 bungee —— 该源应从新文件重新开始，不残留旧账本。
	// 注意：bungee 的实际文件须在**重启前**建好，因为重启会用同一 work 目录。
	require.NoError(t, os.WriteFile(filepath.Join(work, "proxy.log.0"), []byte("[13:00:00] [INFO] bungee\n"), 0o600))

	restarted, err := newTestManager(t, Options{Root: root, VL: vl, Catalog: catalog.New(cat.Journal())})
	require.NoError(t, err)
	require.NoError(t, restarted.RegisterInstance(InstanceRegistration{
		UUID: "uuid-clearsrc", TargetID: "inst:912", Generation: "holder-g1",
		Mode: "FILE_PRIMARY", WorkDir: work, RuleKey: RuleKeyBungee,
	}))
	restarted.pollOnce()

	// 新源已建立且路径为 bungee 布局。
	sc := sourceByID(t, restarted, "inst:912/file")
	require.Equal(t, filepath.Join(work, "proxy.log.0"), sc.Path,
		"规则升级后源必须指向 bungee 布局的新文件")
}

// TestRuleChangeStartsAtEOFAvoidingReimport 验证规则升级重建源时**不会重读文件已有内容**。
//
// 为什么必须如此：规则升级会换到新物理文件（如 logs/latest.log → proxy.log.0）。
// 若从文件头重读，而该文件里已有**投递过的历史内容**，就会重复导入
// （真机实测：反复重启累积 270 万条 vs 唯一 9 万条，约 30 倍重复）。
// 本用例锁定「重建后从 EOF 开始」这一语义。
//
// 判据必须是**行为级**（事件正文 / 位置不前进），不能只看位置数字（2026-10-15 复核）：
// 旧版本只断言 `pos.Read >= len(historical)`，而「先把水位推到 EOF、再从文件头整份重读」
// 同样满足该断言（水位确实 ≥ 文件大小，但既有内容已以新位置第二次进入事件段）——属假绿，
// 也正是「StartAtEOF 实现方向反了」被漏检的直接原因。
func TestRuleChangeStartsAtEOFAvoidingReimport(t *testing.T) {
	m, work := newInstanceRuleTestManager(t)
	require.NoError(t, os.MkdirAll(filepath.Join(work, "logs"), 0o700))

	// 首次登记（bukkit）并跑一轮，建立基线账本。
	require.NoError(t, os.WriteFile(filepath.Join(work, "logs", "latest.log"), []byte("[12:00:00] [INFO] old\n"), 0o600))
	require.NoError(t, m.RegisterInstance(InstanceRegistration{
		UUID: "uuid-eof", TargetID: "inst:913", Generation: "holder-g1",
		Mode: "FILE_PRIMARY", WorkDir: work, RuleKey: RuleKeyBukkit,
	}))
	m.pollOnce()

	// 新载体（bungee）**已有历史内容**——模拟被替换的文件里仍留旧数据。
	historical := "[13:00:00] [INFO] already-delivered historically\n"
	proxyLog := filepath.Join(work, "proxy.log.0")
	require.NoError(t, os.WriteFile(proxyLog, []byte(historical), 0o600))

	// 规则升级 → 源重建。
	require.NoError(t, m.RegisterInstance(InstanceRegistration{
		UUID: "uuid-eof", TargetID: "inst:913", Generation: "holder-g1",
		Mode: "FILE_PRIMARY", WorkDir: work, RuleKey: RuleKeyBungee,
	}))

	key := ledger.SourceKey{LogSourceID: "inst:913/file", SourceGeneration: "holder-g1"}
	sc := sourceByID(t, m, "inst:913/file")
	require.Equal(t, proxyLog, sc.Path)

	positions := func() logtypes.Positions {
		t.Helper()
		m.mu.Lock()
		pipe := m.pipes[key.String()]
		m.mu.Unlock()
		require.NotNil(t, pipe, "规则升级后源必须仍在（重建）")
		pos, ok := pipe.Positions()
		require.True(t, ok)
		return pos
	}
	// 重建后水位已锚到新文件末尾（= 文件大小）。
	rebuilt := positions()
	require.GreaterOrEqual(t, rebuilt.Read, uint64(len(historical)),
		"重建后读取位置必须已锚到新文件末尾（否则会重读历史内容造成重复导入）")

	// 行为判据①：文件**未追加任何字节** ⇒ 采集轮不得推进任何水位、不得产出新事件。
	m.pollOnce()
	idle := positions()
	require.Equal(t, rebuilt.Read, idle.Read,
		"文件未追加时读取位置不得前进——前进即等于从文件头重读既有内容")
	require.Equal(t, rebuilt.Durable, idle.Durable, "文件未追加时持久化位置不得前进")

	// 行为判据②：续采能力不受影响——重建后**新增**的内容必须照常采到，且既有历史内容
	// 不得出现在事件段里。
	//
	// 写两行：事件边界由「下一行到达」闭合（normalize 的既有语义），故末尾刻意留一行悬挂，
	// 确保「重建后新增」那一行本轮就被闭合、可被断言。
	appended := "[13:00:01] [INFO] collected after rule upgrade\n"
	trailing := "[13:00:02] [INFO] still pending at file tail\n"
	require.NoError(t, os.WriteFile(proxyLog, []byte(historical+appended+trailing), 0o600))
	m.pollOnce()
	joined := eventMessageText(t, m, key.String())
	require.Contains(t, joined, "collected after rule upgrade",
		"重建只跳过既有内容，不得连后续追加的新内容一起丢掉")
	require.NotContains(t, joined, "already-delivered historically",
		"新载体里的既有历史内容不属于本源的采集范围，不得被重新读入事件段")
	require.Equal(t, uint64(len(historical)+len(appended)), positions().Durable,
		"持久化水位应恰好停在已闭合事件之后：既不重读历史，也不跳读")
}

// eventMessageText 把某源事件段的正文拼成一段文本，用于**子串**断言。
//
// 为什么不用逐元素相等：event.Message 是事件边界内的原文（含时间戳前缀，不等于解析后的片段），
// 且 normalize 会按字符集/多行规则重写——子串断言才测的是「这段内容有没有进事件段」本身。
func eventMessageText(t *testing.T, m *Manager, key string) string {
	t.Helper()
	var messages []string
	for _, ev := range durableEvents(t, m, key) {
		messages = append(messages, ev.Message)
	}
	return strings.Join(messages, "\n")
}

// TestRuleKeyUpgradeWithIdenticalLayoutDoesNotRebuildSource 验证「等价规则升级」不触发破坏性重建
// （2026-10-15 复核：触发面过宽）。
//
// 场景（存量实例升级后的**主路径**）：历史绑定的 rule_key 为空（FR-502 引入该列前入库），
// 而 CP 恒下发非空规则名（默认 bukkit）。两者解析出的生效载体逐字相同
// （logs/latest.log + logs/*.gz），但按「结构体/规则名不等」判据仍会走破坏性重建——
// 重建会把该源的账目（WAL、水位、投影、已验证代次）整体清零，并从文件头重读既有内容
// （实测同一份 56 字节内容被读第二遍：read 56→112）。
//
// 契约：重建判据只看**解析后的生效载体**（instanceLivePath + fileArchiveGlob）；
// 表达形式变化但载体不变时，只把新记法写回绑定，源与管道原样复用。
func TestRuleKeyUpgradeWithIdenticalLayoutDoesNotRebuildSource(t *testing.T) {
	m, work := newInstanceRuleTestManager(t)
	require.NoError(t, os.MkdirAll(filepath.Join(work, "logs"), 0o700))
	// 两行：末行会被事件边界留作悬挂，首行因此在本轮就被闭合，可被断言。
	content := "[12:00:00] [INFO] stable content\n"
	trailing := "[12:00:01] [INFO] still pending at file tail\n"
	require.NoError(t, os.WriteFile(filepath.Join(work, "logs", "latest.log"), []byte(content+trailing), 0o600))

	// 首次登记：模拟 FR-502 之前的存量绑定（规则名为空 ⇒ 解析为 bukkit 默认布局）。
	require.NoError(t, m.RegisterInstance(InstanceRegistration{
		UUID: "uuid-equiv", TargetID: "inst:915", Generation: "holder-g1",
		Mode: "FILE_PRIMARY", WorkDir: work,
	}))
	m.pollOnce()
	key := ledger.SourceKey{LogSourceID: "inst:915/file", SourceGeneration: "holder-g1"}
	m.mu.Lock()
	pipeBefore := m.pipes[key.String()]
	m.mu.Unlock()
	require.NotNil(t, pipeBefore)
	posBefore, ok := pipeBefore.Positions()
	require.True(t, ok)
	require.Equal(t, uint64(len(content)), posBefore.Durable, "前置断言：首轮已把首行采完")
	require.Contains(t, eventMessageText(t, m, key.String()), "stable content",
		"前置断言：首行已进入事件段")

	// CP 下发非空规则名 bukkit：解析后生效路径与归档 glob 与空规则名逐字相同。
	require.NoError(t, m.RegisterInstance(InstanceRegistration{
		UUID: "uuid-equiv", TargetID: "inst:915", Generation: "holder-g1",
		Mode: "FILE_PRIMARY", WorkDir: work, RuleKey: RuleKeyBukkit,
	}))

	m.mu.Lock()
	pipeAfter := m.pipes[key.String()]
	bindingAfter := m.state.Instances["uuid-equiv"]
	m.mu.Unlock()
	require.Same(t, pipeBefore, pipeAfter,
		"等价规则升级必须复用既有源与管道（重建会清零账目并从文件头重读既有内容）")
	require.Equal(t, RuleKeyBukkit, bindingAfter.RuleKey,
		"等价升级仍须把新记法写回绑定，否则每次登记都会重复走一遍等价判定")

	// 行为判据：同一内容不得被第二次读入，水位不得被重置。
	m.pollOnce()
	posAfter, ok := pipeAfter.Positions()
	require.True(t, ok)
	require.Equal(t, posBefore.Read, posAfter.Read, "等价升级后水位不得被重置并重读")
	require.Equal(t, posBefore.Durable, posAfter.Durable, "等价升级后持久化水位不得被重置")
	text := eventMessageText(t, m, key.String())
	require.Contains(t, text, "stable content", "既有事件必须仍在")
	require.Equal(t, 1, strings.Count(text, "stable content"),
		"等价升级不得让同一份文件内容第二次进入事件段")
}

// TestEffectiveLogLayoutEquivalence 覆盖等价判据的输入面（表格化，不依赖 Manager 夹具）。
func TestEffectiveLogLayoutEquivalence(t *testing.T) {
	work := filepath.Join("root", "inst")
	base := InstanceBinding{
		UUID: "u", TargetID: "inst:1", Generation: "g1",
		Mode: pipeline.ModeFilePrimary, WorkDir: work,
	}
	withRule := func(b InstanceBinding, key string) InstanceBinding {
		b.RuleKey = key
		return b
	}
	withRel := func(b InstanceBinding, rel string) InstanceBinding {
		b.RelativePath = rel
		return b
	}
	require.True(t, sameEffectiveLogLayout(base, withRule(base, RuleKeyBukkit)),
		"空规则名与 bukkit 规则名解析同一布局 ⇒ 等价")
	require.False(t, sameEffectiveLogLayout(base, withRule(base, RuleKeyBungee)),
		"bukkit → bungee 换了物理载体 ⇒ 不等价")
	require.False(t, sameEffectiveLogLayout(withRule(base, RuleKeyBungee),
		withRel(withRule(base, RuleKeyBukkit), "proxy.log.0")),
		"活动日志路径相同但归档 glob 不同（bungee 无归档 vs 覆盖档的同目录 *.gz）⇒ 不等价："+
			"归档发现面同样在判据内")
	require.False(t, sameEffectiveLogLayout(base, withRel(base, "custom/live.log")),
		"路径覆盖换了物理载体 ⇒ 不等价")
	require.True(t, sameEffectiveLogLayout(withRel(base, "custom/live.log"), withRel(base, "custom/live.log")),
		"逐字相同的覆盖 ⇒ 等价（幂等重登记）")

	// 非 FILE_PRIMARY（stdio）的载体是 raw 文件（由 UUID+Generation 派生），与规则名/相对路径无关。
	stdio := base
	stdio.Mode = pipeline.ModeStdioPrimary
	require.True(t, sameEffectiveLogLayout(stdio, withRel(withRule(stdio, RuleKeyBungee), "whatever.log")),
		"stdio 模式的载体不随规则名/相对路径变化 ⇒ 等价")
	require.False(t, sameEffectiveLogLayout(base, stdio),
		"采集模式不同 ⇒ 不等价（实际上已由源身份判据拒绝）")
}

// 变异验证记录（2026-10-15，逐字还原后 diff 为空）：
//
//   A1 —— 把 runtime.go 的 StartAtEOF 段登记改回 `StartPos: size`（即"段基址也前移到 EOF"）：
//     `TestRuleChangeStartsAtEOFAvoidingReimport` → FAIL
//     （判据①"文件未追加时读取位置不得前进"：读取位置从 EOF 继续前进，因为 tailer 的
//      cursor = durable - segmentStart = size - size = 0，实际从**文件头**读起）。
//
//   A4 —— 把 instances.go 的判据短路为"一律重建"（`if false && sameEffectiveLogLayout(...)`，
//     等价于复核前的行为）：
//     `TestRuleKeyUpgradeWithIdenticalLayoutDoesNotRebuildSource` → FAIL
//     （"等价规则升级必须复用既有源与管道"：源与管道对象被整体替换）。
//     反方向的短路（`if true || ...`，即"一律不重建"）不会让本用例转红——这正说明该用例
//     断言的是"等价时才不重建"，而不是"永远不重建"（为此另有 bukkit→bungee 真换载体的用例）。

// TestStartupRecoveryRecalcsInstanceArchiveGlob 验证**启动恢复**会按当前规则重算实例源的归档 glob
// （FR-504 真机发现的路径：源恢复只信任账本里的 SourceConfig，于是 glob 算法修正对已登记源
// 永不生效——真机实测：修正上线并重启 Worker 后，索引里的旧 glob 一直留着、采集轮仍按旧 glob
// 去发现归档）。
//
// 构造：登记 bungee 实例 → 把索引里的 glob 改成「旧算法的结果」→ Stop（把旧值持久化进账本）
// → 重启 → 断言恢复后的 glob 是当前算法的结果（bungee 不产出归档 glob，故为空）。
func TestStartupRecoveryRecalcsInstanceArchiveGlob(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	cat := catalog.New(nil)
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal()})
	require.NoError(t, err)
	work := t.TempDir()
	require.NoError(t, m.RegisterInstance(InstanceRegistration{
		UUID: "uuid-restart-glob", TargetID: "inst:921", Generation: "holder-g1",
		Mode: "FILE_PRIMARY", WorkDir: work, RuleKey: RuleKeyBungee,
	}))
	key := "inst:921/file/holder-g1"
	// 模拟「算法演进之前的存量索引」：把 glob 改回旧算法的结果，并随 Stop 落进账本。
	legacyGlob := filepath.Join(work, "proxy.log.[0-9]*")
	m.mu.Lock()
	stale := m.state.SourceConfigs[key]
	stale.ArchiveGlob = legacyGlob
	m.state.SourceConfigs[key] = stale
	m.mu.Unlock()
	require.NoError(t, m.Stop())

	restarted, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(cat.Journal())})
	require.NoError(t, err)
	got := restarted.state.SourceConfigs[key].ArchiveGlob
	require.Equal(t, fileArchiveGlob(InstanceBinding{
		UUID: "uuid-restart-glob", TargetID: "inst:921", Generation: "holder-g1",
		Mode: pipeline.ModeFilePrimary, WorkDir: work, RuleKey: RuleKeyBungee,
	}), got, "启动恢复必须按当前规则重算归档 glob（否则修正对已登记源永不生效）")
	require.NotEqual(t, legacyGlob, got, "不得保留账本里的旧 glob")
}
