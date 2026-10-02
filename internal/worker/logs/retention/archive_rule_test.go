package retention

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// 本文件守住硬规则「删前归档」（archive-before-delete）：
//
//	保留期到期的数据**绝不裸删**。默认动作是把日分区搬运到冷层（复用既有
//	ROUTING_FROZEN→…→CLEANED 状态机），搬运成功且校验通过后才允许 HOT 侧回收；
//	搬运失败 = 保留原物 + 告警；直接删除只在配置里**显式** discard: true 时可达。
//
// 这四条用例对应用户指定的四条验收，每条都刻意断言「没有被删」，而不只是「搬了」。

// fakeMover 记录搬运调用，可注入失败。
type fakeMover struct {
	calls     []string
	failWith  error
	targetDir string
}

func (m *fakeMover) MoveToCold(_ context.Context, ns, day string) (string, error) {
	m.calls = append(m.calls, ns+"/"+day)
	if m.failWith != nil {
		return "", m.failWith
	}
	dir := m.targetDir
	if dir == "" {
		dir = ns + "-" + day
	}
	return dir, nil
}

// 归档夹具：以基准日 2026-10-02、热层窗口 30d 为口径。
//
//	2026-09-01 → 次日 +30d = 2026-10-02 ⇒ 恰好到点（边界）
//	2026-09-30 → 次日 +30d = 2026-10-31 ⇒ 未到点
//	2026-06-01 → 远早于窗口 ⇒ 早已到点
func archiveFixture() []PartitionRef {
	return []PartitionRef{
		{StorageNamespace: "inst:147", UTCDay: "20260703", Owner: "hot"}, // 恰好到点
		{StorageNamespace: "inst:147", UTCDay: "20260930", Owner: "hot"}, // 未到点
		{StorageNamespace: "node:1", UTCDay: "20260601", Owner: "hot"},   // 早已到点
	}
}

// 回归①：搬运失败 ⇒ 数据仍在 + 有告警，且**不触达任何删除路径**。
func TestArchiveRule1_MoveFailureKeepsOriginalAndAlerts(t *testing.T) {
	p := enabledPolicy()
	hv := time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)
	mv := &fakeMover{failWith: errors.New("冷层不可达（模拟）")}
	ex := NewArchiveExecutor(p, mv)

	out := ex.Execute(context.Background(), hv, archiveFixture())
	if len(mv.calls) == 0 {
		t.Fatal("前置条件：夹具必须确实尝试过搬运")
	}

	var moved, kept int
	for _, o := range out {
		switch o.Result {
		case ArchiveMoved:
			moved++
		case ArchiveKeptOriginal:
			kept++
			if o.Err == nil {
				t.Errorf("保留原物必须带原因（告警的落点）：%s", o.Target)
			}
			if o.TargetDirID != "" {
				t.Errorf("搬运失败不得声称有目标目录：%s", o.Target)
			}
		}
	}
	if moved != 0 {
		t.Fatalf("搬运全部失败时不得有任何 Moved，得到 %d", moved)
	}
	if kept == 0 {
		t.Fatal("搬运失败必须落到「保留原物」，不得静默")
	}
	st := ex.Stats()
	if st.KeptOriginal != int64(kept) {
		t.Errorf("观测面必须记下保留原物件数：期望 %d 得到 %d", kept, st.KeptOriginal)
	}
	if st.LastError == "" || !strings.Contains(st.LastError, "保留原物") {
		t.Errorf("最近告警必须说明「已保留原物」，得到 %q", st.LastError)
	}
	if st.Discarded != 0 {
		t.Fatal("搬运失败路径绝不允许出现删除")
	}
}

// 回归②：达到保留期且有 COLD 路径 ⇒ **必走搬运而非删除**。
func TestArchiveRule2_ExpiredDataTakesMoveNotDelete(t *testing.T) {
	p := enabledPolicy()
	hv := time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)
	mv := &fakeMover{}
	ex := NewArchiveExecutor(p, mv)

	// 同时挂一个「删除器」并把执行闸打开：若实现真去删，本用例会立刻发现。
	fd := &fakeDeleter{}
	sw := NewSweeper(p, fd) // 注意：p.Discard 为 false

	out := ex.Execute(context.Background(), hv, archiveFixture())
	var moved int
	for _, o := range out {
		if o.Result == ArchiveMoved {
			moved++
			if o.Target.Action != ActionMoveToCold {
				t.Errorf("到期数据的默认动作必须是搬运，得到 %s", o.Target.Action)
			}
		}
		if o.Result == ArchiveDiscarded {
			t.Fatal("未显式 discard 时绝不允许出现删除结果")
		}
	}
	if moved == 0 {
		t.Fatal("有 COLD 路径且已过保留期，必须走搬运")
	}
	if _, err := sw.Sweep(context.Background(), hv, []string{"inst:147"}, DefaultScanLevels()); err != nil {
		t.Fatalf("扫描不该报错：%v", err)
	}
	if len(fd.calls) != 0 {
		t.Fatalf("硬规则：有搬运路径时必须搬运而非删除，但删除接口被调用了 %d 次", len(fd.calls))
	}
}

// 回归③：未显式 discard ⇒ 任何配置组合下都不裸删。
func TestArchiveRule3_NeverNakedDeletesWithoutExplicitDiscard(t *testing.T) {
	hv := time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)
	fd := &fakeDeleter{}

	// 穷举「除 discard 之外」的开关组合：Enabled / VLSweep / 有无搬运路径。
	for _, enabled := range []bool{false, true} {
		for _, vlSweep := range []bool{false, true} {
			for _, hasMover := range []bool{false, true} {
				p := DefaultPolicy()
				p.Enabled = enabled
				p.Sweep.VLSweep = vlSweep
				p.Discard = false // 唯一被固定的变量
				var mv Mover
				if hasMover {
					mv = &fakeMover{}
				}
				fd.calls = nil

				sw := NewSweeper(p, fd)
				if _, err := sw.Sweep(context.Background(), hv, []string{"inst:147"}, DefaultScanLevels()); err != nil {
					t.Fatalf("扫描不该报错：%v", err)
				}
				if len(fd.calls) != 0 {
					t.Fatalf("enabled=%v vlSweep=%v hasMover=%v：未显式 discard 时删除接口被调用了 %d 次",
						enabled, vlSweep, hasMover, len(fd.calls))
				}

				ex := NewArchiveExecutor(p, mv)
				for _, o := range ex.Execute(context.Background(), hv, archiveFixture()) {
					if o.Result == ArchiveDiscarded {
						t.Fatalf("enabled=%v vlSweep=%v hasMover=%v：未显式 discard 时出现了删除结果",
							enabled, vlSweep, hasMover)
					}
					if o.Target.Action == ActionDiscard {
						t.Fatalf("enabled=%v vlSweep=%v hasMover=%v：计划里出现了 discard 动作",
							enabled, vlSweep, hasMover)
					}
				}
			}
		}
	}
}

// 回归③b：无搬运路径且未显式 discard ⇒ blocked（保留原物 + 告警），不是删。
func TestArchiveRule3b_NoPathMeansBlockedNotDelete(t *testing.T) {
	p := enabledPolicy()
	hv := time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)
	ex := NewArchiveExecutor(p, nil) // 没有冷层路径

	out := ex.Execute(context.Background(), hv, archiveFixture())
	if len(out) == 0 {
		t.Fatal("前置条件：夹具应产出计划")
	}
	var blocked, due int
	for _, o := range out {
		// 计划层面：没有搬运路径 ⇒ 动作一律是 blocked（这对未到点的分区同样成立，
		// 因为它回答的是「到了点会怎么做」，观测面据此提前暴露「没有归档路径」）。
		if o.Target.Action != ActionBlocked {
			t.Errorf("无搬运路径时应标 blocked，得到 %s", o.Target.Action)
		}
		if !o.Target.Due {
			if o.Result != ArchiveNotDue {
				t.Errorf("未到点的分区应记 not_due，得到 %s", o.Result)
			}
			continue
		}
		due++
		blocked++
		if o.Result != ArchiveKeptOriginal {
			t.Errorf("blocked 必须落到「保留原物」，得到 %s", o.Result)
		}
		if o.Err == nil || !strings.Contains(o.Err.Error(), "删前归档") {
			t.Errorf("blocked 的原因应点明硬规则，得到 %v", o.Err)
		}
	}
	if due == 0 {
		t.Fatal("前置条件：夹具必须有已到点的分区，否则本用例是空跑")
	}
	if got := ex.Stats().KeptOriginal; got != int64(blocked) {
		t.Errorf("观测面必须记下 blocked 件数：期望 %d 得到 %d", blocked, got)
	}
	if ex.Stats().Discarded != 0 {
		t.Error("blocked 路径绝不允许出现删除")
	}
}

// 回归④：搬运后仍要能找回原数据 ⇒ 执行结果必须给出**真实的承接物**（目标目录 ID），
// 且原物在搬运失败/未到点时保持不动。
//
// 说明（如实登记边界）：完整的 Rehydrate 回灌链路由既有 lifecycle 状态机负责，
// 其真机验收见 `docs/specs/worker-log-lifecycle/spec.md` 与
// `worker-log-platform-contract/acceptance-record.md` §2（Runbook B：FROZEN→…→CLEANED 全链、
// 数据物理迁移、HOT 空/COLD 持有、逐状态崩溃恢复视图）。本包**复用**该状态机，
// 不重新实现搬运，因此这里断言的是「保留策略正确地把动作交给了状态机、
// 并拿到了可审计的承接物」，而不是重测状态机本身。
func TestArchiveRule4_MoveYieldsAuditableTargetAndLeavesUndueData(t *testing.T) {
	p := enabledPolicy()
	hv := time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)
	mv := &fakeMover{}
	ex := NewArchiveExecutor(p, mv)

	out := ex.Execute(context.Background(), hv, archiveFixture())
	byPart := map[string]ArchiveOutcome{}
	for _, o := range out {
		byPart[o.Target.StorageNamespace+"/"+o.Target.UTCDay] = o
	}

	moved := byPart["inst:147/20260703"]
	if moved.Result != ArchiveMoved {
		t.Fatalf("已过期的分区应搬运成功，得到 %s（%v）", moved.Result, moved.Err)
	}
	if moved.TargetDirID == "" {
		t.Fatal("搬运成功必须给出目标目录 ID（审计「去哪」，也是 Rehydrate 回灌的依据）")
	}
	if moved.At.IsZero() {
		t.Fatal("执行时刻必须记录（G10 的「何时」）")
	}

	// 未到点的分区必须保持不动：既没搬也没删。
	recent := byPart["inst:147/20260930"]
	if recent.Result != ArchiveNotDue {
		t.Fatalf("未到保留期的分区必须保持不动，得到 %s", recent.Result)
	}
	for _, call := range mv.calls {
		if call == "inst:147/20260930" {
			t.Error("未到点的分区不得搬运")
		}
	}

	st := ex.Stats()
	if st.Moved != int64(countResult(out, ArchiveMoved)) {
		t.Errorf("观测面 Moved 计数与结果不一致")
	}
	if st.Discarded != 0 {
		t.Error("本用例全程不应出现删除")
	}
}

// 回归④b：日期不可解析的分区不猜、不动手（误搬一个还在写的分区代价远大于多占盘）。
func TestArchiveSkipsUnparseableDay(t *testing.T) {
	p := enabledPolicy()
	hv := time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)
	mv := &fakeMover{}
	ex := NewArchiveExecutor(p, mv)

	out := ex.Execute(context.Background(), hv, []PartitionRef{
		{StorageNamespace: "inst:147", UTCDay: "not-a-day"},
		{StorageNamespace: "", UTCDay: "20260703"},
	})
	if len(out) != 0 {
		t.Fatalf("无法解析的分区不得产出计划，得到 %d 条", len(out))
	}
	if len(mv.calls) != 0 {
		t.Fatal("不得对无法解析的分区动手")
	}
}

// TestArchiveExplicitDiscardIsReachable 是硬规则的**正向对照**：
// 显式 discard 之后确实可达删除，否则「硬规则」会退化成「永远不删」，
// 那样第 3 条验收会在一个恒真的分支上通过（用例失去意义）。
func TestArchiveExplicitDiscardIsReachable(t *testing.T) {
	p := discardPolicy()
	hv := time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)

	ex := NewArchiveExecutor(p, nil) // 即使没有搬运路径，显式放弃也应可达删除
	out := ex.Execute(context.Background(), hv, archiveFixture())
	var discarded int
	for _, o := range out {
		if o.Result == ArchiveDiscarded {
			discarded++
		}
	}
	if discarded == 0 {
		t.Fatal("显式 discard 后删除路径必须可达——否则硬规则退化成「永远不删」，第 3 条验收将恒真")
	}
	if got := ex.Stats().Discarded; got != int64(discarded) {
		t.Errorf("观测面 Discarded 计数应为 %d，得到 %d", discarded, got)
	}

	fd := &fakeDeleter{}
	sw := NewSweeper(p, fd)
	res, err := sw.Sweep(context.Background(), hv, []string{"inst:147"}, DefaultScanLevels())
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	if res.Submitted == 0 || len(fd.calls) == 0 {
		t.Fatal("两道闸都开时删除路径必须真的可用")
	}
}

// TestArchivePrefersMoveEvenWhenDiscardIsExplicit 钉住「首选/次选/仅当」的**次序**。
//
// 这是硬规则里最容易写反的一处：把 discard 判在前面，语义就变成
// 「只要曾经显式开启过删除，默认动作就永久变成删除」——而规则的意图恰恰相反：
// **能搬就搬，搬不了才谈放弃**。本用例此前缺失，是变异实验（N13）暴露出来的。
func TestArchivePrefersMoveEvenWhenDiscardIsExplicit(t *testing.T) {
	p := discardPolicy() // discard=true 且执行闸已开
	hv := time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)
	mv := &fakeMover{}

	plan := p.PlanArchive(hv, archiveFixture(), true)
	if len(plan) == 0 {
		t.Fatal("前置条件：夹具应产出计划")
	}
	for _, tg := range plan {
		if tg.Action != ActionMoveToCold {
			t.Errorf("冷层路径可用时，即使显式 discard 也应优先搬运，得到 %s（%s）", tg.Action, tg)
		}
	}

	ex := NewArchiveExecutor(p, mv)
	out := ex.Execute(context.Background(), hv, archiveFixture())
	for _, o := range out {
		if o.Result == ArchiveDiscarded {
			t.Fatalf("冷层路径可用时不得删除：%s", o.Target)
		}
	}
	if ex.Stats().Moved == 0 {
		t.Fatal("应发生搬运")
	}
	if ex.Stats().Discarded != 0 {
		t.Fatal("搬运可用时不得出现任何删除")
	}
}

func countResult(out []ArchiveOutcome, result string) int {
	n := 0
	for _, o := range out {
		if o.Result == result {
			n++
		}
	}
	return n
}
