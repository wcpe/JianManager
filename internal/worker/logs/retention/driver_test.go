package retention

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 本文件守住 G6：冷层搬运**驱动器**。没有它，HOT 永不自动转 COLD——
// plan/executor/Mover 全都对，但没有东西周期性地把它们串起来。

// fakeLister 提供候选分区。
type fakeLister struct {
	parts []PartitionRef
	err   error
}

func (f *fakeLister) ListPartitions() ([]PartitionRef, error) { return f.parts, f.err }

var driverNow = time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)

// driverFixture 以热层窗口 30d、基准日 2026-10-02 为口径：
//
//	2026-08-01 → 次日 +30d = 2026-09-01 ⇒ 已到点（年龄 31d）
//	2026-09-30 → 次日 +30d = 2026-10-31 ⇒ 未到点（年龄 2d）
//	2026-09-24 → 次日 +30d = 2026-10-25 ⇒ 未到点，但年龄恰好 7d（磁盘触发可提前搬）
func driverFixture() []PartitionRef {
	return []PartitionRef{
		{StorageNamespace: "inst:147", UTCDay: "20260801", Owner: "hot"},
		{StorageNamespace: "inst:147", UTCDay: "20260930", Owner: "hot"},
		{StorageNamespace: "inst:147", UTCDay: "20260924", Owner: "hot"},
	}
}

func newTestDriver(t *testing.T, mover Mover, disk DiskReader, parts []PartitionRef) *Driver {
	t.Helper()
	p := enabledPolicy()
	return NewDriver(p, &fakeLister{parts: parts}, mover, disk)
}

// 回归①：**到点必搬** —— 这是驱动器的存在理由。
func TestDriverMovesDuePartitions(t *testing.T) {
	mv := &fakeMover{}
	d := newTestDriver(t, mv, nil, driverFixture())

	res, err := d.Step(context.Background(), driverNow)
	if err != nil {
		t.Fatalf("单轮驱动失败：%v", err)
	}
	if res.Listed != 3 {
		t.Fatalf("应列出 3 个分区，得到 %d", res.Listed)
	}
	if res.Moved != 1 {
		t.Fatalf("恰好 1 个分区已到点，应搬运 1 个，得到 %d（分区结果 %+v）", res.Moved, res.Outcomes)
	}
	if len(mv.calls) != 1 || mv.calls[0] != "inst:147/20260801" {
		t.Fatalf("搬错了分区：%v", mv.calls)
	}
	if res.NotDue != 2 {
		t.Fatalf("其余 2 个未到点，应记 not_due，得到 %d", res.NotDue)
	}
	// 到点的那个必须带年龄触发标记，未到点的不带。
	for _, o := range res.Outcomes {
		if o.Target.UTCDay == "20260801" && o.Target.TriggerReason != TriggerAge {
			t.Errorf("到点分区应记年龄触发，得到 %q", o.Target.TriggerReason)
		}
		if o.Target.UTCDay != "20260801" && o.Target.TriggerReason != "" {
			t.Errorf("未到点分区不应有触发原因，得到 %q", o.Target.TriggerReason)
		}
	}
}

// 回归②：**搬运失败必须留存 + 告警**，且绝不删。
func TestDriverKeepsOriginalOnMoveFailure(t *testing.T) {
	mv := &fakeMover{failWith: errors.New("冷层不可达（模拟）")}
	d := newTestDriver(t, mv, nil, driverFixture())

	res, err := d.Step(context.Background(), driverNow)
	if err != nil {
		t.Fatalf("单轮驱动失败：%v", err)
	}
	if res.Moved != 0 {
		t.Fatalf("搬运失败不得有任何 Moved，得到 %d", res.Moved)
	}
	if res.KeptOriginal != 1 {
		t.Fatalf("搬运失败必须记「保留原物」，得到 %d", res.KeptOriginal)
	}
	if res.Discarded != 0 {
		t.Fatal("搬运失败路径绝不允许出现删除")
	}
	st := d.Executor().Stats()
	if st.LastError == "" {
		t.Fatal("失败必须留下告警（观测面的最近错误）")
	}
	if st.KeptOriginal != 1 {
		t.Fatalf("累计读数应记下保留原物，得到 %d", st.KeptOriginal)
	}
}

// 回归③：已在冷层 / 在途迁移的分区不得重复驱动。
//
// 在途这一条尤其要紧：驱动器周期跑，若不看在途标记就反复下发，
// 第二轮会对同一分区再次 BeginMigration，Catalog 状态机会报错并污染状态。
func TestDriverSkipsColdAndInFlight(t *testing.T) {
	mv := &fakeMover{}
	d := newTestDriver(t, mv, nil, []PartitionRef{
		{StorageNamespace: "inst:147", UTCDay: "20260801", Owner: OwnerColdName},
		{StorageNamespace: "inst:147", UTCDay: "20260801", Owner: "hot", InFlight: true},
		{StorageNamespace: "inst:147", UTCDay: "20260802", Owner: "hot"},
	})

	res, err := d.Step(context.Background(), driverNow)
	if err != nil {
		t.Fatalf("单轮驱动失败：%v", err)
	}
	if res.Skipped != 2 {
		t.Fatalf("应在过滤阶段跳过 2 个（已在冷层 + 在途），得到 %d", res.Skipped)
	}
	if res.Moved != 1 {
		t.Fatalf("只剩 1 个可搬，得到 %d", res.Moved)
	}
	for _, call := range mv.calls {
		if call != "inst:147/20260802" {
			t.Fatalf("不得驱动已在冷层/在途的分区，却搬了 %s", call)
		}
	}
}

// 回归④：磁盘水位触发 —— 「年龄 + 磁盘水位取先到」。
func TestDriverDiskWatermarkTriggersEarlyMove(t *testing.T) {
	p := enabledPolicy()
	p.HotRetention = 30 * 24 * time.Hour
	p.Trigger = Trigger{DiskPercent: 80, MinAge: 7 * 24 * time.Hour}

	cases := []struct {
		name        string
		diskPercent float64
		wantMoved   int
		why         string
	}{
		{"盘不吃紧：只按年龄", 50, 1, "未达水位不得放宽口径"},
		{"盘吃紧：提前搬过下限的分区", 85, 2, "20260925 年龄 7d，可提前搬；20260930 只 2d，仍不搬"},
	}
	for _, c := range cases {
		mv := &fakeMover{}
		d := NewDriver(p, &fakeLister{parts: driverFixture()}, mv,
			func() (float64, error) { return c.diskPercent, nil })
		res, err := d.Step(context.Background(), driverNow)
		if err != nil {
			t.Fatalf("%s：驱动失败 %v", c.name, err)
		}
		if res.Moved != c.wantMoved {
			t.Fatalf("%s：应搬 %d 个，得到 %d（%s）\n%+v", c.name, c.wantMoved, res.Moved, c.why, res.Outcomes)
		}
	}
}

// 回归④b：磁盘触发**必须**有最小年龄下限——盘再满也不搬刚写进来的分区。
func TestDriverDiskTriggerRespectsMinAge(t *testing.T) {
	p := enabledPolicy()
	p.HotRetention = 30 * 24 * time.Hour
	p.Trigger = Trigger{DiskPercent: 80, MinAge: 7 * 24 * time.Hour}

	mv := &fakeMover{}
	d := NewDriver(p, &fakeLister{parts: []PartitionRef{
		// 年龄 2 天：盘 100% 满也不该动它——它可能正在被写入。
		{StorageNamespace: "inst:147", UTCDay: "20260930", Owner: "hot"},
	}}, mv, func() (float64, error) { return 100, nil })

	res, err := d.Step(context.Background(), driverNow)
	if err != nil {
		t.Fatalf("驱动失败：%v", err)
	}
	if res.Moved != 0 || len(mv.calls) != 0 {
		t.Fatalf("磁盘触发必须尊重最小年龄下限，却搬了 %v", mv.calls)
	}
	if res.NotDue != 1 {
		t.Fatalf("应记 not_due，得到 %d", res.NotDue)
	}
}

// 回归④c：磁盘读数拿不到 ⇒ 退回纯年龄口径，绝不猜「盘满了」。
func TestDriverDiskReadFailureFallsBackToAgeOnly(t *testing.T) {
	p := enabledPolicy()
	p.Trigger = Trigger{DiskPercent: 80, MinAge: 7 * 24 * time.Hour}
	mv := &fakeMover{}
	d := NewDriver(p, &fakeLister{parts: driverFixture()}, mv,
		func() (float64, error) { return 0, errors.New("statfs 失败（模拟）") })

	res, err := d.Step(context.Background(), driverNow)
	if err != nil {
		t.Fatalf("读数失败不该让整轮失败：%v", err)
	}
	if res.Moved != 1 {
		t.Fatalf("读数失败时应退回纯年龄口径（只搬 1 个），得到 %d", res.Moved)
	}
}

// 回归⑤：未启用策略 / 无分区时必须是彻底的空操作。
func TestDriverNoopWhenDisabledOrEmpty(t *testing.T) {
	mv := &fakeMover{}

	disabled := NewDriver(DefaultPolicy(), &fakeLister{parts: driverFixture()}, mv, nil)
	res, err := disabled.Step(context.Background(), driverNow)
	if err != nil || res.Listed != 0 || len(mv.calls) != 0 {
		t.Fatalf("未启用时应完全不动：listed=%d calls=%d err=%v", res.Listed, len(mv.calls), err)
	}

	empty := newTestDriver(t, mv, nil, nil)
	res, err = empty.Step(context.Background(), driverNow)
	if err != nil || res.Listed != 0 || len(mv.calls) != 0 {
		t.Fatalf("无分区时应完全不动：listed=%d calls=%d err=%v", res.Listed, len(mv.calls), err)
	}
}

// 回归⑥：列举失败必须如实报错（使观测面能区分「没有分区」与「读不出来」）。
func TestDriverListFailureSurfaces(t *testing.T) {
	p := enabledPolicy()
	d := NewDriver(p, &fakeLister{err: errors.New("catalog 不可读（模拟）")}, &fakeMover{}, nil)
	if _, err := d.Step(context.Background(), driverNow); err == nil {
		t.Fatal("列举失败必须报错，不得静默当成「没有分区」")
	}
}

// 回归⑦：无搬运路径时驱动器不得删——回落 blocked + 保留原物。
func TestDriverWithoutMoverBlocksInsteadOfDeleting(t *testing.T) {
	p := enabledPolicy()
	d := NewDriver(p, &fakeLister{parts: driverFixture()}, nil, nil) // 无 Mover

	res, err := d.Step(context.Background(), driverNow)
	if err != nil {
		t.Fatalf("驱动失败：%v", err)
	}
	if res.Moved != 0 || res.Discarded != 0 {
		t.Fatalf("无搬运路径时应 blocked，得到 moved=%d discarded=%d", res.Moved, res.Discarded)
	}
	if res.KeptOriginal != 1 {
		t.Fatalf("到点分区应保留原物，得到 %d", res.KeptOriginal)
	}
	if d.Executor().Stats().Discarded != 0 {
		t.Fatal("无搬运路径时绝不允许删除")
	}
}

// TestDriverRunStopsOnContextCancel 确认周期循环会随 ctx 结束退出（首轮立即执行）。
func TestDriverRunStopsOnContextCancel(t *testing.T) {
	p := enabledPolicy()
	p.Sweep.Interval = time.Hour // 长间隔：确保只跑首轮
	mv := &fakeMover{}
	d := NewDriver(p, &fakeLister{parts: driverFixture()}, mv, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Run(ctx)
		close(done)
	}()
	// 首轮必须立即执行（启动时正是「停机期间攒下的过期分区」最需要处理的时候）。
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run 未随 context 取消退出")
	}
	if len(mv.calls) == 0 {
		t.Fatal("Run 应至少执行一轮（首轮立即执行，不等第一个 tick）")
	}
}
