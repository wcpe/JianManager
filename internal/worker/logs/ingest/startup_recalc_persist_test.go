package ingest

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// persistedSourceConfig 从索引库（`source_aux.config`）读出某源的落库配置。
//
// 为什么必须读库而不是读内存：本组缺陷的真实现场就是「内存已修正、索引仍是旧值」——
// 只断言内存态会让缺陷整体不可见（原用例正是如此：它断言的是 `state.SourceConfigs`）。
func persistedSourceConfig(t *testing.T, m *Manager, key string) SourceConfig {
	t.Helper()
	store, err := stateindex.OpenReadOnly(m.indexPath())
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
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
	require.NoError(t, m.Stop())
	require.Equal(t, legacyGlob, persistedSourceConfig(t, m, key).ArchiveGlob,
		"前置断言：旧 glob 必须已落进索引（否则本用例测不到'滞后'这件事）")

	restarted, err := newTestManager(t, Options{Root: root, VL: client, Catalog: catalog.New(cat.Journal())})
	require.NoError(t, err)
	want := fileArchiveGlob(InstanceBinding{
		UUID: "uuid-recalc-persist", TargetID: "inst:931", Generation: "holder-g1",
		Mode: pipeline.ModeFilePrimary, WorkDir: work, RuleKey: RuleKeyBungee,
	})
	require.Equal(t, want, restarted.state.SourceConfigs[key].ArchiveGlob,
		"前置断言：启动恢复必须已在内存里重算出路 glob")
	require.NotEqual(t, legacyGlob, want, "前置断言：新旧 glob 必须不同，否则测的是等价态")

	// 不主动落库：只等重算自己触发的那一次有界落库（后台通道）。
	require.Eventually(t, func() bool {
		return persistedSourceConfig(t, restarted, key).ArchiveGlob == want
	}, 5*time.Second, 10*time.Millisecond,
		"启动重算的源配置必须自己触发落库；只改内存不写索引 ⇒ 重启后旧值回来，采集轮继续按旧 glob 找归档")
}
