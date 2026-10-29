package ledger

import "testing"

// ③ 附带修复：已解决缺口的裁剪。
//
// ## 为什么需要它
//
// `index_store.go` 的注释早已承诺「已解决缺口会被 trimResolvedGapsLocked 裁掉」，
// 但该函数**从未实现**——`gap` 表因此是索引里唯一无界增长的维度：
// 已补上的缺口永远留在库里，随运行时间线性膨胀。
//
// 后果是可观测的：`Store.Load()` 在启动时全表读 gap（实测每 1k 行约 5ms），
// 缺口堆积几十万行就意味着启动多花几秒——而这段时间**发生在注册之前**，
// 主控台看不到节点（的原始问题就是这个形态）。
//
// ## 判据为什么用「回收水位」而不是「是否 resolved」
//
// 只看 `Resolved` 在语义上不安全：一个缺口被标记解决，不代表它涉及的区间
// 已经从存储里回收。若在回收前就把缺口删掉，「这段日志缺失」的事实会消失，
// 而数据其实可能还没落盘——那会把「已知缺失」变成「静默丢失」。
//
// 只有当回收水位（Reclaim）已经越过缺口的 `EndPos`，即该区间的数据已确认无需再读，
// 删除缺口记录才是安全的。这与 `PruneDeliveryBatches` 用同一把尺子，
// 两者对「历史」的定义保持一致。

func mkGap(start, end uint64, resolved bool) Gap {
	return Gap{StartPos: start, EndPos: end, Reason: "APPEND_REJECTED", Resolved: resolved}
}

// TestPruneResolvedGaps_KeepsUnresolved 未解决的缺口永不裁剪——它们仍待补齐。
func TestPruneResolvedGaps_KeepsUnresolved(t *testing.T) {
	gaps := []Gap{
		mkGap(0, 100, false),
		mkGap(100, 200, false),
	}
	kept, dropped, err := PruneResolvedGaps(gaps, 1000, 1000, 0)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if dropped != 0 || len(kept) != 2 {
		t.Fatalf("未解决缺口必须全部保留，实际 dropped=%d kept=%d", dropped, len(kept))
	}
}

// TestPruneResolvedGaps_DropsResolvedBelowWatermark 已解决且水位越过者被裁。
func TestPruneResolvedGaps_DropsResolvedBelowWatermark(t *testing.T) {
	gaps := []Gap{
		mkGap(0, 100, true),   // 已解决，水位 500 越过 100 → 裁
		mkGap(100, 200, true), // 同上 → 裁
		mkGap(600, 700, true), // 已解决，但水位 500 未越过 700 → 留（可能还需读）
	}
	kept, dropped, err := PruneResolvedGaps(gaps, 500, 500, 0)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if dropped != 2 {
		t.Fatalf("应裁掉 2 条，实际 %d", dropped)
	}
	if len(kept) != 1 || kept[0].StartPos != 600 {
		t.Fatalf("应保留 600-700 那条，实际 %+v", kept)
	}
}

// TestPruneResolvedGaps_KeepsRecentAuditWindow 保留最近 N 条已解决缺口作审计尾窗。
//
// 为什么要留：缺口何时补上、怎么补的，是事后审计的凭据。全裁掉会让
// 「这个源历史上到底出过什么问题」无从回答。
func TestPruneResolvedGaps_KeepsRecentAuditWindow(t *testing.T) {
	gaps := []Gap{
		mkGap(0, 100, true),
		mkGap(100, 200, true),
		mkGap(200, 300, true),
		mkGap(300, 400, true),
	}
	// keepRecent=2：最后两条（200-300、300-400）无论如何保留
	kept, dropped, err := PruneResolvedGaps(gaps, 1000, 1000, 2)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if dropped != 2 {
		t.Fatalf("应裁掉前 2 条，实际 dropped=%d", dropped)
	}
	if len(kept) != 2 || kept[0].StartPos != 200 {
		t.Fatalf("应保留最后 2 条（从 200 起），实际 %+v", kept)
	}
}

// TestPruneResolvedGaps_ZeroWatermarkIsNoop 水位为 0 表示「还没回收过」，一律不动。
//
// 这与 PruneDeliveryBatches 的约定一致：没有回收水位就等于没有安全边界，
// 此时裁剪任何东西都可能删掉仍需读取的区间。
func TestPruneResolvedGaps_ZeroWatermarkIsNoop(t *testing.T) {
	gaps := []Gap{mkGap(0, 100, true)}
	kept, dropped, err := PruneResolvedGaps(gaps, 0, 0, 0)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if dropped != 0 || len(kept) != 1 {
		t.Fatalf("水位 0 时不应裁剪，实际 dropped=%d kept=%d", dropped, len(kept))
	}
}

// TestPruneResolvedGaps_EmptyInput 空输入安全，且返回非 nil（形态可比）。
func TestPruneResolvedGaps_EmptyInput(t *testing.T) {
	kept, dropped, err := PruneResolvedGaps(nil, 1000, 1000, 0)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if dropped != 0 {
		t.Fatalf("空输入不应有 dropped，实际 %d", dropped)
	}
	if kept == nil {
		t.Fatal("空输入应返回非 nil 空切片（与 normalize 形态一致，便于可比）")
	}
}

// TestPruneResolvedGaps_PreservesOrder 裁剪保持原有顺序。
//
// gap 的顺序承载语义（按位置升序），乱序会让「缺口链」的判读错位。
func TestPruneResolvedGaps_PreservesOrder(t *testing.T) {
	gaps := []Gap{
		mkGap(0, 100, true),
		mkGap(100, 200, false),
		mkGap(200, 300, true),
		mkGap(300, 400, false),
	}
	kept, _, err := PruneResolvedGaps(gaps, 1000, 1000, 0)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(kept) != 2 {
		t.Fatalf("应保留 2 条未解决，实际 %d", len(kept))
	}
	if kept[0].StartPos != 100 || kept[1].StartPos != 300 {
		t.Fatalf("顺序应保持 100,300，实际 %+v", kept)
	}
}

// TestPruneResolvedGaps_DeliveryCapsReclaim 判据取 min(Reclaim, Delivery)。
//
// 这条守的是本轮实现中最容易搞错的地方：`Reclaim` 会越过 `Delivery`
// （凭据放行 / 责任已转移两个分支，见 delivery_batch_prune.go 的 LEDGER-1 论证）。
// 若直接按 Reclaim 裁剪，会删掉 EndPos 落在 (Delivery, Reclaim] 的缺口——
// 而 runtime.go:9894 的「重读治愈」正需要这些候选（判据是 EndPos > Delivery）。
// 把它们裁掉 = 静默放弃一条自愈路径，且不报错。
func TestPruneResolvedGaps_DeliveryCapsReclaim(t *testing.T) {
	gaps := []Gap{
		mkGap(0, 100, true),   // EndPos 100 ≤ delivery 200 → 可裁
		mkGap(200, 300, true), // delivery 200 < EndPos 300 ≤ reclaim 900 → **必须留**
	}
	// reclaim 大幅越过 delivery，模拟凭据放行分支
	kept, dropped, err := PruneResolvedGaps(gaps, 900, 200, 0)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if dropped != 1 {
		t.Fatalf("只应裁 1 条（EndPos ≤ delivery 的那条），实际 %d", dropped)
	}
	if len(kept) != 1 || kept[0].StartPos != 200 {
		t.Fatalf("必须保留 200-300 那条（重读治愈仍需它），实际 %+v", kept)
	}
}

// TestPruneResolvedGaps_ZeroDeliveryIsNoop Delivery 为 0 时不做裁剪。
//
// 与「watermark 为 0」同理：还没有推进过就没有安全边界。
// 单独覆盖 Delivery，是因为取 min 后它可能先于 Reclaim 归零。
func TestPruneResolvedGaps_ZeroDeliveryIsNoop(t *testing.T) {
	gaps := []Gap{mkGap(0, 100, true)}
	kept, dropped, err := PruneResolvedGaps(gaps, 1000, 0, 0)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if dropped != 0 || len(kept) != 1 {
		t.Fatalf("delivery=0 时不应裁剪，实际 dropped=%d kept=%d", dropped, len(kept))
	}
}
