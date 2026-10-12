package ingest

// 缺陷一回归：同名代次重复写（2026-10-10 生产实测）。
//
// ── 现场（只读取证，已核实）──────────────────────────────────────────────────────
// `inst:172/file` 三次写入的精确时序：
//
//	08:00:47.093 投影写入开始 gen=projection-1 events=60000
//	08:00:47.101 投影写入开始 gen=projection-1 events=60000   ← +8ms
//	08:00:47.102 投影写入开始 gen=projection-1 events=60000   ← +9ms
//	08:00:50.900 投影写入结束 gen=projection-1 verified=60000
//	08:00:50.942 投影写入结束 gen=projection-1 verified=60000
//	08:00:51.539 投影写入结束 gen=projection-1 verified=60000
//
// VL 侧铁证：同一 (源, 代次) = 60,000 唯一 event_id / 180,000 行（3×）。
// 该形态反复出现（08:07:29 / 08:10:33 / 08:51:29 / 08:54:30），非偶发。
//
// ── 机制 ───────────────────────────────────────────────────────────────────────
// 代次名原先在 `deliver` 里于**取 publishMu 之前**由 `nextFreeProjectionGeneration` 分配，
// 判据是「实探 VL 未占用」＋「缓存该名可用」。两条同源投递（三条 `DeliverPending` 面并发
// 入口：采集轮自愈面 `selfHealPausedSource`、兜底环候选投递步、兜底环投递欠账步）各自实探
// 「未占用」⇒ 双双算出**同一个名字**。
//
// ── 本文件守住什么 ─────────────────────────────────────────────────────────────
// ① 同源并发取名不再撞名（本用例的主断言，见下）；
// ② 释放后名字可被重新分配，但**必须重新实探**（不削弱既有重名防护）；
// ③ token 所有权：别人的预留不能被误清（否则撞名复现）；
// ④ 只读探测入口不登记预留（既有「同一状态重复询问得到同一结论」契约）。

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// TestConcurrentGenerationReservationNeverCollides 是缺陷一的主回归（可变异转红）。
//
// 形态：N 条 goroutine 同时对**同一个源**取名（正是现场三条同源投递并发的等价形态），
// 断言所有并发取得的代次名**两两不同**。
//
// 变异验证（实测）：把 `reserveProjectionGeneration` 换回
// `nextFreeProjectionGeneration`（即不做预留，等价于本修复前的实现），
// 全部 N 条会拿到同一个名字 ⇒ 本用例转红（见回复里的变异实测输出）。
func TestConcurrentGenerationReservationNeverCollides(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	source := SourceConfig{
		LogSourceID: "node:reserve-race", SourceGeneration: "g1",
		Path: root, Mode: pipeline.ModeFilePrimary,
		StorageNamespace: "node:reserve-race", UTCDay: runtimeTestUTCDay(),
	}
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{source}})
	require.NoError(t, err)
	defer func() { _ = m.Stop() }()

	const workers = 8
	names := make([]string, workers)
	tokens := make([]*generationReservation, workers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start // 同刻起跑：最大化「各自实探未占用」的重叠窗
			name, res := m.reserveProjectionGeneration(source, "", "test-worker")
			names[idx], tokens[idx] = name, res
		}(i)
	}
	close(start)
	wg.Wait()

	seen := map[string]int{}
	for _, name := range names {
		require.NotEmpty(t, name, "取名不得为空")
		seen[name]++
	}
	for name, count := range seen {
		require.Equal(t, 1, count,
			"同源并发取名不得撞名：%q 被 %d 条并发投递同时选中（现场形态 = 同代次写多份 ⇒ VL 3× 放大）",
			name, count)
	}
	// 预留令牌必须互不相同（所有权凭据的核心不变量）。
	byToken := map[uint64]bool{}
	for _, res := range tokens {
		if res == nil {
			continue
		}
		require.False(t, byToken[res.token], "预留令牌必须唯一（否则释放会误伤同伴）")
		byToken[res.token] = true
	}
}

// TestGenerationReservationSurvivesForeignReleaseAttempts 守住 token 所有权：
// 「别人的预留不得被误清」——没有这条，一条迟到的旧调用就能把正在写入的同伴的预留删掉，
// 撞名随即复现（这正是本缺陷的机制本体）。
func TestGenerationReservationSurvivesForeignReleaseAttempts(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	source := SourceConfig{
		LogSourceID: "node:reserve-owner", SourceGeneration: "g1",
		Path: root, Mode: pipeline.ModeFilePrimary,
		StorageNamespace: "node:reserve-owner", UTCDay: runtimeTestUTCDay(),
	}
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{source}})
	require.NoError(t, err)
	defer func() { _ = m.Stop() }()

	name, res := m.reserveProjectionGeneration(source, "", "first")
	require.NotNil(t, res, "首次取名应当拿到预留（VL 探测可用）")
	key := source.LogSourceID + "/" + source.SourceGeneration

	// 伪造一个「同一个名字、不同 token」的句柄：模拟一条迟到调用试图释放同伴的预留。
	m.releaseProjectionGeneration(&generationReservation{key: key, generation: name, token: res.token + 1000})
	require.False(t, m.generationReservableForTest(key, name),
		"异 token 的释放必须无效：该名字仍应被视为已被预留")

	// 真正的主人释放后，名字才回到可分配状态。
	m.releaseProjectionGeneration(res)
	require.True(t, m.generationReservableForTest(key, name),
		"主人释放后该名字应不再被视为已预留")
}

// TestGenerationReleaseForcesReprobe 守住「释放不等于可复用」：
// 释放必须同时让该名字的「未占用」缓存失效，使下一次取名**重新实探 VL**。
//
// 为什么这是红线：真实写入失败可能已在 VL 留下同名行；若释放后仍凭旧缓存判「未占用」，
// 就等于把「同名旧行被读成本次写入 → 校验永不通过 → 采集停摆」（2026-10-01 生产事故）
// 重新引进来。变异：让 `releaseProjectionGeneration` 不删 generationProbeCache ⇒ 本用例转红。
func TestGenerationReleaseForcesReprobe(t *testing.T) {
	client, fixture := newProjectionVL(t)
	root := t.TempDir()
	source := SourceConfig{
		LogSourceID: "node:reserve-reprobe", SourceGeneration: "g1",
		Path: root, Mode: pipeline.ModeFilePrimary,
		StorageNamespace: "node:reserve-reprobe", UTCDay: runtimeTestUTCDay(),
	}
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{source}})
	require.NoError(t, err)
	defer func() { _ = m.Stop() }()

	name, res := m.reserveProjectionGeneration(source, "projection-7", "first")
	require.NotNil(t, res)
	require.Equal(t, "projection-8", name)
	probesAfterFirst := fixture.probeCount(name)
	require.Equal(t, 1, probesAfterFirst, "首次取名必须实探一次")

	m.releaseProjectionGeneration(res)

	// 释放后重新取名：必须重新实探（缓存已失效），而不是凭缓存直接回答。
	again, res2 := m.reserveProjectionGeneration(source, "projection-7", "second")
	require.NotNil(t, res2)
	require.Equal(t, name, again, "VL 中该名字仍未占用 ⇒ 应得到同一个名字")
	require.Greater(t, fixture.probeCount(again), probesAfterFirst,
		"释放后重新取名必须重新实探 VL（不得凭已失效的缓存判未占用）")
	m.releaseProjectionGeneration(res2)
}

// TestProbeOnlyEntryDoesNotReserve 守住只读探测入口的契约：
// `nextFreeProjectionGenerationWithClient` 必须**不登记预留**，故「同一状态重复询问
// 必须得到同一结论」仍然成立（既有回归 TestProjectionGenerationProbeIsCachedAndInvalidatedOnWrite
// 从另一侧守同一件事）。
//
// 变异：让探测入口也走 reserve=true ⇒ 第二次询问会把自己刚登记的条目当"被占用"而进位 ⇒ 转红。
func TestProbeOnlyEntryDoesNotReserve(t *testing.T) {
	client, _ := newProjectionVL(t)
	root := t.TempDir()
	source := SourceConfig{
		LogSourceID: "node:probe-only", SourceGeneration: "g1",
		Path: root, Mode: pipeline.ModeFilePrimary,
		StorageNamespace: "node:probe-only", UTCDay: runtimeTestUTCDay(),
	}
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{source}})
	require.NoError(t, err)
	defer func() { _ = m.Stop() }()

	first := m.nextFreeProjectionGenerationWithClient(client, source, "projection-7")
	second := m.nextFreeProjectionGenerationWithClient(client, source, "projection-7")
	require.Equal(t, first, second,
		"只读探测入口不得登记预留：同一状态重复询问必须得到同一结论")
	key := source.LogSourceID + "/" + source.SourceGeneration
	require.True(t, m.generationReservableForTest(key, first),
		"只读探测入口不得留下预留（否则该名字会被无关调用钉死）")
}
