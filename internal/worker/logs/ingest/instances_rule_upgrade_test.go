package ingest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
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
	historical := []byte("[13:00:00] [INFO] already-delivered historically\n")
	require.NoError(t, os.WriteFile(filepath.Join(work, "proxy.log.0"), historical, 0o600))

	// 规则升级 → 源重建。
	require.NoError(t, m.RegisterInstance(InstanceRegistration{
		UUID: "uuid-eof", TargetID: "inst:913", Generation: "holder-g1",
		Mode: "FILE_PRIMARY", WorkDir: work, RuleKey: RuleKeyBungee,
	}))

	// 判据：重建后账本的读取位置必须已锚到新文件末尾（= 文件大小），
	// 而不是从 0 开始——后者会把文件里的历史内容重投一遍。
	key := ledger.SourceKey{LogSourceID: "inst:913/file", SourceGeneration: "holder-g1"}
	sc := sourceByID(t, m, "inst:913/file")
	require.Equal(t, filepath.Join(work, "proxy.log.0"), sc.Path)

	var pos logtypes.Positions
	m.mu.Lock()
	if pipe, ok := m.pipes["inst:913/file/holder-g1"]; ok {
		if p, ok := pipe.Ledger().Positions(key); ok {
			pos = p
		}
	}
	m.mu.Unlock()
	require.GreaterOrEqual(t, pos.Read, uint64(len(historical)),
		"重建后读取位置必须已锚到新文件末尾（否则会重读历史内容造成重复导入）")
}
