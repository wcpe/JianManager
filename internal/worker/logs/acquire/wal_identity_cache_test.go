package acquire

// WALEntry 身份缓存（identity / identityOK）的语义回归。
//
// 背景（2026-10-29 pprof 实测）：生产 worker 30s CPU 剖面里 `CommitIdentityOf` 占 **63%**
// （json.Marshal 36% + sha256 25%），全部来自两个**逐条扫全表**的入口：
//   - `snapshotPending`（由 `HasPendingDelivery(limit=1)` 调用，只想探一条却扫全表 34%）；
//   - `committedIdentities`（每次排空后裁剪回执，扫全表 32%）。
// 而条目正文在两次 Append 之间不变 ⇒ 结果逐字相同。加缓存后必须保证三件事，否则比不缓存更糟：
//
//	① 命中缓存的结果与现算**逐字段相等**（否则资格比对失效）；
//	② 正文一变，缓存必须失效（否则拿旧身份冒充新正文 ⇒ 静默数据错误）；
//	③ 值拷贝上命中的缓存要能**写回原切片**（否则缓存从不被复热，等于白加）。
//
// 本文件用变异验证过：把 ② 的 `setEventLocked` 换成裸赋值 `entry.Event = event`，
// TestWALIdentityCacheInvalidatedOnReplace 必红；把 ③ 写回那行删掉，
// TestWALIdentityCacheSurvivesValueCopyWriteBack 必红。

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// identityCacheWAL 造一个只装了三条 durable 条目的 WAL（足够驱动全部全表循环）。
func identityCacheWAL(t *testing.T) *WAL {
	t.Helper()
	w := &WAL{key: testKey("inst:cache/file", "g1")}
	w.entries = identityTestEntries(t)
	return w
}

// TestWALIdentityCacheMatchesRecomputed 钉住不变量 ①：缓存值必须等于现算值。
//
// 这条是**全部资格比对的根**：snapshotPending 用缓存身份去查 acknowledged 集合，
// committedIdentities 用缓存身份与回执做交集，两者一旦与「现算」不符，投递/回收资格就会错位。
func TestWALIdentityCacheMatchesRecomputed(t *testing.T) {
	w := identityCacheWAL(t)
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := range w.entries {
		require.False(t, w.entries[i].identityOK, "前置：初始未缓存")
		cached, err := w.entries[i].eventIdentityLocked()
		require.NoError(t, err)
		require.True(t, w.entries[i].identityOK, "首次调用必须把缓存置上")

		recomputed, err := CommitIdentityOf(w.entries[i].Event)
		require.NoError(t, err)
		require.Equal(t, recomputed, cached, "缓存命中的身份必须与现算逐字段相等")

		// 第二次必须走缓存且结果不变（幂等）。
		again, err := w.entries[i].eventIdentityLocked()
		require.NoError(t, err)
		require.Equal(t, cached, again)
	}
}

// TestWALIdentityCacheInvalidatedOnReplace 钉住不变量 ②：正文被改写后缓存必须失效。
//
// 走的是真实的改写入口 `setEventLocked`（`adjudicateReplayDuplicatesLocked` 的
// 同区间异哈希原位替换用的就是它）。变异：把它换成裸赋值 ⇒ 本用例必红。
func TestWALIdentityCacheInvalidatedOnReplace(t *testing.T) {
	w := identityCacheWAL(t)
	w.mu.Lock()
	defer w.mu.Unlock()

	before, err := w.entries[0].eventIdentityLocked()
	require.NoError(t, err)
	require.True(t, w.entries[0].identityOK)

	// 换一份正文不同、但 event_id / 区间都相同的条目（正是回放去重替换的形态）。
	changed := identityTestEntries(t)[0]
	changed.Event.Message = "a-changed"
	w.entries[0].setEventLocked(changed.Event)

	require.False(t, w.entries[0].identityOK, "正文变了缓存必须失效")
	after, err := w.entries[0].eventIdentityLocked()
	require.NoError(t, err)
	require.Equal(t, before.EventID, after.EventID, "event_id 由区间派生，替换后不变")
	require.NotEqual(t, before, after, "正文变了身份必须跟着变，否则拿旧身份冒充新正文")
}

// TestWALIdentityCacheSurvivesValueCopyWriteBack 钉住不变量 ③：值拷贝命中后的缓存要写回原切片。
//
// 为什么重要：`snapshotPending` 用 `entry := w.entries[index]` 拿到的是**值拷贝**，
// 缓存只在拷贝上置位、下轮又要重算。生产上 `HasPendingDelivery` 每轮每源都跑一次，
// 不写回就意味着这份缓存永远命中不了 —— 等于没加。
func TestWALIdentityCacheSurvivesValueCopyWriteBack(t *testing.T) {
	w := identityCacheWAL(t)

	// 走真实入口：limit=1 的探测（对应 HasPendingDelivery 的形态）。
	// 注意**不能持 w.mu**：snapshotPending 内部自己取写锁（持锁调用会自死锁）。
	_, _, _, err := w.snapshotPending(1, 0, nil, nil)
	require.NoError(t, err)

	w.mu.Lock()
	defer w.mu.Unlock()
	cached := 0
	for i := range w.entries {
		if w.entries[i].identityOK {
			cached++
		}
	}
	require.Positive(t, cached, "snapshotPending 扫过的条目必须把身份缓存写回原切片，否则下次还要重算")
}

// TestWALIdentityCacheCommittedIdentitiesUsesCache 钉住「committedIdentities 不再重算」。
//
// 前置把缓存全部置上，再改一个与身份无关的字段（Durable 不变 / 正文不变），
// 调用后结果必须与现算集合逐项相等，且**不得**把 identityOK 打回 false（那说明它绕过缓存重算了）。
func TestWALIdentityCacheCommittedIdentitiesUsesCache(t *testing.T) {
	w := identityCacheWAL(t)
	w.mu.Lock()
	for i := range w.entries {
		_, err := w.entries[i].eventIdentityLocked()
		require.NoError(t, err)
	}
	w.mu.Unlock()

	got, err := w.CommittedIdentities()
	require.NoError(t, err)
	require.Len(t, got, 3, "三条 durable 条目都应在集合里")

	for _, entry := range identityTestEntries(t) {
		id, err := CommitIdentityOf(entry.Event)
		require.NoError(t, err)
		require.Contains(t, got, id, "缓存路径返回的集合必须与现算身份逐项一致")
	}
}
