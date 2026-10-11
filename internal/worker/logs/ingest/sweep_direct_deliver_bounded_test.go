package ingest

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// TestDirectDeliverStepIsBoundedAndClaimed（INGEST-12 红证）：
// 兜底环里「破环直投」步此前是同窗口唯一**裸调用**的一步——相邻的滚动步与投递步都经
// `runSweepStepBounded` + 同一张在飞表收口，唯独它直通。其内部是 `Ledger.Get` 深克隆 +
// `WAL.Snapshot` 全量拷贝 + 同步 VL 写，于是既不占预算也不进在飞计数，全落在 ticker
// 单线程上，与「每轮有界」的自述直接矛盾。
//
// **必须真调生产入口 `maybeResumePausedSources()`**（2026-10-12 终审订正）：
// 本用例此前的形态是在测试内**手写复刻**认领 + `runSweepStepBounded` 的调用点，只断言
// 测试自己写的闭包有界——那锁的是测试自己，生产代码还原成裸调用它察觉不到（实测：
// 把 runtime.go 的破环直投整块还原为裸调用，旧形态用例仍 PASS）。现改为真调生产入口。
//
// 判别原理：`sweepStepHold` 的注入点在 `runSweepStepBounded` **内部**，因此只有真正走
// 有界执行器的步才会被它按住。夹具让该源「有可查配置、未暂停」，于是本函数里
// **唯一**可能命中 `sweepStepHold` 的就是破环直投步（同窗口的滚动步要求 reclaim<delivery、
// 投递步要求 delivery<read，本夹具两者皆不满足；后续 autoResolve/DeliverPending/
// EvaluateResume 三步都在 `entry.AcquirePaused` 的 continue 之后 ⇒ 不可达）。
//
// 转红方式（实测）：把 runtime.go 破环直投步整块还原为裸调用（不经
// `runSweepStepBounded`、不认领）⇒ 该步不再触碰 `sweepStepHold` ⇒ 断言①「钩子必须被观察到」
// 立即红；去掉外层 `tryClaimReplayInflight` ⇒ 断言③「在飞必须归零」红。
func TestDirectDeliverStepIsBoundedAndClaimed(t *testing.T) {
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	src := SourceConfig{
		LogSourceID: "inst:direct-bounded/file", SourceGeneration: "g1",
		Path: filepath.Join(root, "b.log"), Mode: pipeline.ModeFilePrimary,
		StorageNamespace: "inst:direct-bounded", UTCDay: runtimeTestUTCDay(),
	}
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.WriteFile(src.Path, []byte("[12:00:01] [Server thread/INFO]: seed\n"), 0o644))

	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: catalog.New(catalog.NewMemJournal()),
		Journal: nil, Sources: []SourceConfig{src},
	})
	require.NoError(t, err)

	// 前置：源**不暂停**（跳过 entry.AcquirePaused 之后的全部步骤）；水位保持全零
	// （不触发滚动步与投递步）⇒ 破环直投是唯一可能走有界执行器的步。

	// 钩子：观察「是否有步进入了有界执行器」，并把它按住到有界预算耗尽。
	observed := make(chan struct{}, 8)
	hold := make(chan struct{})
	m.sweepStepHold = func() {
		select {
		case observed <- struct{}{}:
		default:
		}
		select {
		case <-hold:
		case <-time.After(3 * time.Second): // 兜底：永不被卡死（测试自身有界 ✓）
		}
	}
	m.sweepStepBudget = 10 * time.Millisecond

	// 真调生产入口。
	_, _ = m.maybeResumePausedSources()

	// ① 破环直投步必须进入有界执行器（这正是旧形态的裸调用所缺失的）。
	select {
	case <-observed:
	case <-time.After(2 * time.Second):
		close(hold)
		t.Fatal("破环直投步未进入 runSweepStepBounded（裸调用形态：既不占预算也不进在飞计数 ⇒ 会钉住 ticker 线程）")
	}

	// ② 探针必须已落计数与耗时（旧形态连读数都没有 ⇒ 兜底环诊断里不可见）。
	require.GreaterOrEqual(t, m.sweepDirectDeliverProbes.Load(), int64(1),
		"破环直投步必须可观测（探针是该步在兜底环诊断里的唯一读数）")
	require.Positive(t, m.nsRollingDirectDeliver.Load(),
		"该步必须记录耗时（否则无法判「它是否又在拖慢整轮」）")

	// ③ 在飞认领必须由该步自己释放（defer m.finishScheduledReplay）——
	// 否则该 key 被永久钉死，同源后续排空全部让路（自锁）。
	close(hold)
	require.Eventually(t, func() bool { return m.sweepInflight.Load() == 0 }, 5*time.Second, 20*time.Millisecond,
		"放行后在飞必须归零（否则该步把 key 钉死 ⇒ 下轮全自锁）")
}
