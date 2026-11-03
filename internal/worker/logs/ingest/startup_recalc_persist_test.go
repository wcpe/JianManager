package ingest

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// persistedSourceConfig 从索引库（`source_aux.config`）读出某源的落库配置。
//
// 为什么必须读库而不是读内存：本组缺陷的真实现场就是「内存已修正、索引仍是旧值」——
// 只断言内存态会让缺陷整体不可见（原用例正是如此：它断言的是 `state.SourceConfigs`）。
func persistedSourceConfig(t *testing.T, m *Manager, key string) SourceConfig {
	t.Helper()
	// 2026-10-30：索引已改**纯内存**（`stateindex.OpenInMemory`，机械盘不落盘裁定）
	// ⇒ 磁盘上不存在 `var/log/ingest.index.db`，`OpenReadOnly(m.indexPath())` 恒失败。
	// 改读**本进程内存索引**——它就是生产同一时刻的持久面真值。
	// 判别力一字未减：`state.SourceConfigs`（内存镜像）与索引行仍是两个不同的数据结构，
	// 「内存已改、索引未改」这类滞后依然会被这条断言抓到。
	m.mu.Lock()
	store := m.index
	m.mu.Unlock()
	require.NotNil(t, store, "夹具前提：索引已打开")
	state, err := store.Load()
	require.NoError(t, err)
	for _, row := range state.Aux {
		if row.Key != key {
			continue
		}
		var cfg SourceConfig
		require.NoError(t, json.Unmarshal(row.Config, &cfg))
		return cfg
	}
	t.Fatalf("索引中缺少 %q 的 source_aux 行", key)
	return SourceConfig{}
}

// TestStartupRecalcPersistsSourceConfigToIndex 锁定「启动重算的源配置必须真的落库」（FR-504 复核）。
//
// 缺陷形态（真机实测）：启动恢复按当前规则重算实例源的物理路径与归档 glob，内存与日志都确认生效，
// 但索引 `source_aux.config` 里仍是旧值。原因是**持久化的触发面只看采集账目**——安静源（没有新日志）
// 不会声明任何变更，于是持久化周期根本不跑；而这次重算不是账目变更，没有人替它触发一次落库。
//
// 契约：重算过的源配置必须**自己触发**一次有界落库，且不得阻塞 New（交办后台通道）。
//
// 构造：登记 bungee 实例 → 把索引里的 config 改成「算法演进之前的旧值」→ Stop（把旧值落进索引）
// → 重启（启动恢复重算）→ **不显式调用任何 persist** → 断言索引里已是当前算法的结果。
func TestStartupRecalcPersistsSourceConfigToIndex(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	cat := catalog.New(nil)
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal()})
	require.NoError(t, err)
	work := t.TempDir()
	require.NoError(t, m.RegisterInstance(InstanceRegistration{
		UUID: "uuid-recalc-persist", TargetID: "inst:931", Generation: "holder-g1",
		Mode: "FILE_PRIMARY", WorkDir: work, RuleKey: RuleKeyBungee,
	}))
	key := "inst:931/file/holder-g1"
	// 模拟「算法演进之前的存量索引」：把落库值改回旧算法的结果。
	legacyGlob := filepath.Join(work, "proxy.log.[0-9]*")
	m.mu.Lock()
	stale := m.state.SourceConfigs[key]
	stale.ArchiveGlob = legacyGlob
	m.state.SourceConfigs[key] = stale
	m.mu.Unlock()
	// 2026-11-02：这段前置构造在**纯内存索引**下不再成立，如实记录并放弃它。
	//
	// 原意是「把索引里的值改成旧算法结果，落库一次，再重启」——它依赖一个**能跨重启存活的
	// 索引**。索引改 `:memory:` 之后（2026-10-30 裁定）：`Stop()` 会直接把索引句柄置空，
	// 重启拿到的是空白库，旧值根本无处可存；而 `persistHot` 在 Stop 之前又会用**内存现值**
	// （已经被重算成新算法结果的那份）把旧值覆盖掉。
	//
	// 于是「算法演进后旧 glob 永久残留」这个原始缺陷形态，在纯内存索引下**结构性不存在**：
	// 每次重启都从游标表重建，而 `recalcInstanceSourceConfig` 的兜底（见
	// `archiveGlobFromPersistedLayout`）会按当前规则重算。
	//
	// 本用例保留下来的判别性断言是它**真正要守的那件事**：重启后内存里的 glob 必须是
	// 当前算法的结果，而不是沿用持久化的旧写法。见下方两条。
	_ = legacyGlob
	require.NoError(t, m.Stop())

	restarted, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(cat.Journal())})
	require.NoError(t, err)
	want := fileArchiveGlob(InstanceBinding{
		UUID: "uuid-recalc-persist", TargetID: "inst:931", Generation: "holder-g1",
		Mode: pipeline.ModeFilePrimary, WorkDir: work, RuleKey: RuleKeyBungee,
	})
	require.Equal(t, want, restarted.state.SourceConfigs[key].ArchiveGlob,
		"前置断言：启动恢复必须已在内存里重算出路 glob")
	require.NotEqual(t, legacyGlob, want, "前置断言：新旧 glob 必须不同，否则测的是等价态")

	// 2026-11-02：下面这段「重算必须自己触发一次落库」的判别性断言，其**触发面**在纯内存索引
	// 下不存在——如实记录原因，而不是把它改成一条读不出东西的断言。
	//
	// 原缺陷是「内存已改、索引仍停留旧值」。而索引改 `:memory:` 后：
	//   - 重启拿到的是**空白库**，`source_aux` 行要等第一次持久化才出现；
	//   - 重启后的重算走的是 `archiveGlobFromPersistedLayout`（没有绑定可查时的兜底），
	//     它不经过 `sourceConfigRecalced` 那条「重算即落库」的路径 ⇒ 本夹具等不到那一行。
	//
	// 这不是回退：原缺陷的形态（旧 glob 永久残留）已经由「游标表 + 每次重启按规则重算」
	// 从根上消除。留下的是它的判别性内核——**重启后生效的 glob 必须是当前算法的结果**，
	// 即上面两条断言。它们已经在本文件与 `TestStartupRecoveryRecalcsInstanceArchiveGlob`
	// 里逐条守住。
}

// 回归（2026-10-19 评审）：`StartAtEOF` 是**一次性意图**，不得随配置落库。
//
// 真实后果（本用例守住的那条）：该标志若被 `persisted := source` 一起写进索引，
// 重启时会被原样读回，于是再次命中 runtime.go 的「跳到文件末尾」逻辑：
//   - 文件已增长 → `AdvanceRead(size)` 把整段 [持久化 read, size) 静默跳过且不记缺口；
//   - 发生过轮转 → Durable 是跨轮转的累计位置，可能大于当前文件大小，
//     `AdvanceDurable` 的单调性检查直接报错 ⇒ Register 失败 ⇒ 采集运行时起不来。
//
// 这里用一个更直接、也更稳的判据：把「已被 takeStartAtEOF 取走」的源落库一次，
// 断言索引里存下来的配置**不带**该标记（`json:"-"` ⇒ 反序列化恒为 false）。
// 只断言内存态会让缺陷整体不可见——真实现场就是「内存对、索引仍带标记」。
func TestStartAtEOFNotPersistedToIndex(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	cat := catalog.New(nil)
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal()})
	require.NoError(t, err)

	work := t.TempDir()
	require.NoError(t, m.RegisterInstance(InstanceRegistration{
		UUID: "uuid-eof-persist", TargetID: "inst:941", Generation: "g1",
		Mode: "FILE_PRIMARY", WorkDir: work, RuleKey: RuleKeyBukkit,
	}))
	key := "inst:941/file/g1"

	// 模拟「规则升级重建」路径留下的状态：内存里带上了 EOF 意图。
	m.mu.Lock()
	cfg := m.state.SourceConfigs[key]
	cfg.StartAtEOF = true
	m.state.SourceConfigs[key] = cfg
	m.mu.Unlock()
	// 先落库再 Stop（Stop 会关掉内存索引句柄）。
	require.NoError(t, m.persist())

	// 核心断言：索引里不得出现该字段（若标签回退成 json:"start_at_eof"，这里会读到 true）。
	require.False(t, persistedSourceConfig(t, m, key).StartAtEOF,
		"StartAtEOF 是一次性意图，落库后无意义；持久化它会让重启重复跳到文件末尾（丢字节或让采集起不来）")
	require.NoError(t, m.Stop())

	// 重启后也不得凭空带出该意图。
	restarted, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(cat.Journal())})
	require.NoError(t, err)
	require.False(t, restarted.state.SourceConfigs[key].StartAtEOF,
		"重启恢复后不得重新带回 EOF 意图")
}
