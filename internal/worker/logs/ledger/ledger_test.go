package ledger

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func keyOf(id, gen string) SourceKey {
	return SourceKey{LogSourceID: id, SourceGeneration: gen}
}

func TestResolveGapsKeepsAuditAndRequiresFullCoverageBeforeResume(t *testing.T) {
	ledger := New()
	key := SourceKey{LogSourceID: "source", SourceGeneration: "g1"}
	ledger.Ensure(key, logtypes.SourceIdentity{LogSourceID: "source", SourceGeneration: "g1"})
	require.NoError(t, ledger.PauseAcquire(key, "disk full"))
	require.NoError(t, ledger.RecordGap(key, 0, 10, "PAUSED", "disk full"))
	require.NoError(t, ledger.RecordGap(key, 10, 20, "DELIVERY_ERROR", "unverified"))
	resolved, err := ledger.ResolveGapsThrough(key, 10, "projection verified")
	require.NoError(t, err)
	require.Equal(t, 1, resolved)
	require.Equal(t, 1, ledger.UnresolvedGapCount(key))
	require.Error(t, ledger.ResumeAcquire(key))
	resolved, err = ledger.ResolveGapsThrough(key, 20, "projection verified")
	require.NoError(t, err)
	require.Equal(t, 1, resolved)
	require.NoError(t, ledger.ResumeAcquire(key))
	entry := ledger.Get(key)
	require.False(t, entry.AcquirePaused)
	require.Len(t, entry.Gaps, 2)
	for _, gap := range entry.Gaps {
		require.True(t, gap.Resolved)
		require.Equal(t, "projection verified", gap.Resolution)
	}
}

func TestLedgerEnsureAndWatermarks(t *testing.T) {
	led := New()
	key := keyOf("s1", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "s1", SourceGeneration: "g1", ParserVersion: "p1"})

	if err := led.AdvanceRead(key, 10); err != nil {
		t.Fatal(err)
	}
	// read 可回退。
	if err := led.AdvanceRead(key, 4); err != nil {
		t.Fatal(err)
	}
	if err := led.AdvanceDurable(key, 20); err != nil {
		t.Fatal(err)
	}
	if err := led.AdvanceDurable(key, 10); err == nil {
		t.Fatal("durable must be monotonic")
	}
	ent := led.Get(key)
	if ent.Positions.Read != 4 || ent.Positions.Durable != 20 {
		t.Fatalf("positions=%+v", ent.Positions)
	}
}

func TestLedgerRotationKeepsGeneration(t *testing.T) {
	led := New()
	key := keyOf("src", "g7")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "src", SourceGeneration: "g7"})
	_ = led.RegisterSegment(key, Segment{Path: "latest.log", Kind: SegmentLive})
	if err := led.LinkRotation(key, "latest.log", "latest.log.1.gz", 100); err != nil {
		t.Fatal(err)
	}
	link, ok := led.RotationLinked(key, "latest.log.1.gz")
	if !ok {
		t.Fatal("expected rotation linked")
	}
	if link.Generation != "g7" {
		t.Fatalf("generation must carry over, got %s", link.Generation)
	}
	if link.EndPosFrom != 100 {
		t.Fatalf("end pos from want 100 got %d", link.EndPosFrom)
	}
}

func TestLedgerGapAndPause(t *testing.T) {
	led := New()
	key := keyOf("g", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "g", SourceGeneration: "g1"})
	if err := led.RecordGap(key, 0, 50, "CAPACITY", "wal full"); err != nil {
		t.Fatal(err)
	}
	if err := led.PauseAcquire(key, "budget"); err != nil {
		t.Fatal(err)
	}
	ent := led.Get(key)
	if !ent.AcquirePaused || len(ent.Gaps) != 1 || ent.ErrorCount != 1 {
		t.Fatalf("gap/pause not recorded: %+v", ent)
	}
	if err := led.ResumeAcquire(key); err == nil {
		t.Fatal("unresolved gap must block resume")
	}
	if _, err := led.ResolveGapsThrough(key, 50, "operator verified projection"); err != nil {
		t.Fatal(err)
	}
	if err := led.ResumeAcquire(key); err != nil {
		t.Fatal(err)
	}
	if led.Get(key).AcquirePaused {
		t.Fatal("should resume")
	}
}

// TestTryReclaimTargetTable 固定新语义下 reclaim 的**上界推导**本身：
//
//	reclaim = min(REQUEST_DONE 连续覆盖末端, max(Read, Durable))
//
// 它取代了旧的「恢复段状态机门禁表」（STAGED/DURABLE_VERIFIED 阻断、WAL_RESPONSIBILITY_TRANSFERRED
// 放行、hold 阻断）。那套门禁 2026-10-29 随受管恢复分段机制一并删除（机制失去对象，
// 见 tryReclaimLocked 的注释）。原表验证的**关切**是「reclaim 只在证据充分时放行，
// 且放行到具体位置」；新表把同一关切表达为三个真实输入（认证覆盖 / 读入量 / 耐久量）
// 与一个具体位置输出 —— 每一行都断言推进到**哪个字节**，而不是只断言“成功/失败”。
//
// 分工（避免与 reclaim_evidence_test.go 重复）：证据轴（UNKNOWN 不算证据、真实空洞阻断、
// 1 字节分隔符容差、读入量夹取）由 reclaim_evidence_test.go 逐条钉死；**本表只负责它没盖的**——
// 两个下界的 max 语义（Durable 单独立得住 / Read 单独立得住）与「完全无证据」的空操作。
//
// 转红方式（实测）：把 tryReclaimLocked 的 `max(e.Positions.Read, e.Positions.Durable)`
// 改成只用 Read ⇒「Durable 单独立得住上界」一行立即红（Read=0 ∧ Durable=200 会被误判成
// 「无事可做」，而实际上确有 200 字节可回收）；把 `target <= Reclaim` 提前返回删掉并改成
// 总是推进 ⇒「完全无证据」一行立即红。
func TestTryReclaimTargetTable(t *testing.T) {
	tests := []struct {
		name          string
		read, durable uint64
		batches       []DeliveryBatch
		wantReclaim   uint64
	}{
		{
			// AdvanceDurable 只推 Durable、不动 Read ⇒ Read=0 ∧ Durable=200 是合法状态；
			// 只用 Read 当上界会误判成「无事可做」（这是 tryReclaimLocked 注释里点名的那一条）。
			name: "Durable 单独立得住上界（Read 滞后）", read: 0, durable: 200,
			batches:     []DeliveryBatch{batchOf(0, 200)},
			wantReclaim: 200,
		},
		{
			name: "Read 单独立得住上界（Durable 滞后）", read: 200, durable: 0,
			batches:     []DeliveryBatch{batchOf(0, 200)},
			wantReclaim: 200,
		},
		{
			// 无事可做返回 (当前Reclaim, nil)：旧实现此时报 ErrReclaimNoSegment，
			// 让调用方对空区间反复补段（生产 inst:317 自旋 34 次/分）。
			name: "完全没有证据：无事可做，不报错", read: 200, durable: 200,
			batches:     nil,
			wantReclaim: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			led := New()
			key := keyOf("r", "g1")
			led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "r", SourceGeneration: "g1"})
			require.NoError(t, led.AdvanceRead(key, tt.read))
			require.NoError(t, led.AdvanceDurable(key, tt.durable))
			for _, b := range tt.batches {
				require.NoError(t, led.RecordDelivery(key, b.Start, b.End, b.State))
			}
			pos, err := led.TryReclaim(key)
			require.NoError(t, err, "新语义下「无事可做」返回 (当前Reclaim, nil)，不再有 ErrReclaimNoSegment")
			require.Equal(t, tt.wantReclaim, pos)
			require.Equal(t, tt.wantReclaim, led.Get(key).Positions.Reclaim)
		})
	}
}

// TestReclaimUnknownIsAStallPointNotADeadEnd 钉住 UNKNOWN 区间的**可恢复性**：
// 它只是回收的停滞点，重投成功（补上 REQUEST_DONE）后回收必须能继续推进。
//
// 与 reclaim_evidence_test.go 的 TestReclaimDoesNotCrossUnknownDelivery 分工：
// 那条只断言「不得越过」，若实现把「遇 UNKNOWN 就永久停止」写死（比如把它当成硬边界），
// 那条仍然是绿的——而现场就是「源永久停在 100，重投成功也不恢复」。
//
// 转红方式（实测）：把 contiguousCertifiedEnd 的 `if r.To <= pos { continue }` 改成
// 「遇到非 REQUEST_DONE 区间就 return pos 且不再重新计算」之类的一次性缓存，本用例后半立即红。
func TestReclaimUnknownIsAStallPointNotADeadEnd(t *testing.T) {
	led := New()
	key := keyOf("ack-lost", "g1")
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "ack-lost", SourceGeneration: "g1"})
	require.NoError(t, led.AdvanceRead(key, 300))
	require.NoError(t, led.AdvanceDurable(key, 300))
	require.NoError(t, led.RecordDelivery(key, 0, 100, logtypes.DeliveryRequestDone))
	// 响应丢失：请求发出去了，但不知道落没落库。
	require.NoError(t, led.RecordDelivery(key, 100, 200, logtypes.DeliveryUnknown))
	require.NoError(t, led.RecordDelivery(key, 200, 300, logtypes.DeliveryRequestDone))

	e := led.Get(key)
	require.Equal(t, uint64(300), e.Positions.Delivery, "Delivery 含 UNKNOWN ⇒ 连续前缀照常推进到 300")

	pos, err := led.TryReclaim(key)
	require.NoError(t, err)
	require.Equal(t, uint64(100), pos, "reclaim 只认 REQUEST_DONE ⇒ 停在 UNKNOWN 之前")
	require.Equal(t, uint64(100), led.Get(key).Positions.Reclaim)

	// 重投成功（UNKNOWN 区间补上 REQUEST_DONE）后，认证覆盖才接上，reclaim 继续推进。
	require.NoError(t, led.RecordDelivery(key, 100, 200, logtypes.DeliveryRequestDone))
	pos, err = led.TryReclaim(key)
	require.NoError(t, err)
	require.Equal(t, uint64(300), pos, "补上 ACK 后认证覆盖连续 ⇒ 推进到读入量，不是永久死路")
}

// FR-496 加固：账本修订号必须在**每一个**写路径上推进——上层「只持久化变更源」依赖它是
// 可靠判据，漏推进一处就会把该源的更新静默漏落库。
//
// 转红说明：删掉某个写方法里的 touch（或把推进点从 require 挪走），本用例即红——
// 对应源修订号不变，上层的增量持久化便跳过该源。
func TestLedgerRevisionAdvancesOnEveryMutation(t *testing.T) {
	key := keyOf("rev", "g1")
	led := New()
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "rev", SourceGeneration: "g1"})

	type mutation struct {
		name string
		run  func()
	}
	segment := Segment{Path: "/data/rev.log", Kind: SegmentLive, StartPos: 0, EndPos: 100}
	mutations := []mutation{
		{"AdvanceRead", func() { _ = led.AdvanceRead(key, 10) }},
		{"AdvanceDurable", func() { _ = led.AdvanceDurable(key, 10) }},
		{"RecordDelivery", func() { _ = led.RecordDelivery(key, 0, 10, logtypes.DeliveryRequestDone) }},
		{"RegisterSegment", func() { _ = led.RegisterSegment(key, segment) }},
		{"LinkRotation", func() { _ = led.LinkRotation(key, "/data/rev.log", "/data/rev.1.log", 100) }},
		{"RecordGap", func() { _ = led.RecordGap(key, 0, 10, "TEST", "detail") }},
		{"PauseAcquire", func() { _ = led.PauseAcquire(key, "disk full") }},
		{"ResumeAcquire", func() { _ = led.ResumeAcquire(key) }},
		{"TryReclaim", func() { _, _ = led.TryReclaim(key) }},
		{"IncrementIngestSeq", func() { _, _ = led.IncrementIngestSeq(key) }},
	}
	// 让 TryReclaim 有可推进的靶子：认证覆盖 + 读入量都在 100（旧实现靠「注册恢复段 + 推进状态」，
	// 2026-10-29 那套机制删除后，靶子就是两个真实水位）。
	require.NoError(t, led.AdvanceRead(key, 100))
	require.NoError(t, led.AdvanceDurable(key, 100))
	require.NoError(t, led.RecordDelivery(key, 0, 100, logtypes.DeliveryRequestDone))

	for index, mutation := range mutations {
		before := led.Revision(key)
		mutation.run()
		after := led.Revision(key)
		if after <= before {
			t.Fatalf("写方法 %q 之后修订号未推进（before=%d after=%d）", mutation.name, before, after)
		}
		_ = index
	}
	// Snapshot / Revision 是只读操作，不得推进修订号。
	before := led.Revision(key)
	_ = led.Snapshot()
	_ = led.Revision(key)
	if got := led.Revision(key); got != before {
		t.Fatalf("只读操作改变了修订号：%d → %d", before, got)
	}
	// 新鲜账本不存在的源修订号为 0。
	if got := led.Revision(keyOf("absent", "g1")); got != 0 {
		t.Fatalf("未注册源修订号应为 0，实测 %d", got)
	}
}
