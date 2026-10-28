package startup

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// TestTrackerRecordsPhaseCosts 守住「阶段耗时被真实记录」。
func TestTrackerRecordsPhaseCosts(t *testing.T) {
	tr := New()
	tr.Begin(PhaseConfig)
	time.Sleep(20 * time.Millisecond)
	d := tr.Done(PhaseConfig)

	if d < 15*time.Millisecond {
		t.Fatalf("阶段耗时应 ≥15ms，实际 %v", d)
	}
	if got := tr.PhaseCost(PhaseConfig); got != d {
		t.Fatalf("PhaseCost 与 Done 返回值不一致：%v vs %v", got, d)
	}
	if tr.PhaseCost(PhaseDataRoot) != 0 {
		t.Fatal("未记录阶段应返回 0，不能凭空产生耗时")
	}
}

// TestDoneWithoutBeginIsIgnored 守住「未 Begin 就 Done 不产生伪数据」。
//
// 为什么重要：启动链路有分支（如 setup 已构造配置则跳过配置加载）。
// 若 Done 对未 Begin 的阶段也记一笔，汇总里会出现 0ms 的假阶段，
// 让「阶段缺失」看起来像「阶段很快」。
func TestDoneWithoutBeginIsIgnored(t *testing.T) {
	tr := New()
	if d := tr.Done(PhaseRegister); d != 0 {
		t.Fatalf("未 Begin 的 Done 应返回 0，实际 %v", d)
	}
	if n := len(tr.Snapshot().Costs); n != 0 {
		t.Fatalf("未 Begin 的 Done 不应进入快照，实际有 %d 条", n)
	}
}

// TestTrackWrapsFunc 守住便捷包装。
func TestTrackWrapsFunc(t *testing.T) {
	tr := New()
	ran := false
	d := tr.Track(PhaseHeartbeat, func() {
		ran = true
		time.Sleep(10 * time.Millisecond)
	})
	if !ran {
		t.Fatal("Track 必须执行传入函数")
	}
	if d < 5*time.Millisecond {
		t.Fatalf("Track 应记录真实耗时，实际 %v", d)
	}
}

// TestSnapshotOrderAndSlowest 守住发生顺序与最慢阶段判定。
func TestSnapshotOrderAndSlowest(t *testing.T) {
	tr := New()
	// 故意让后发生的阶段最慢，验证 Slowest 不是「第一个」而是「最慢的」。
	tr.Track(PhaseConfig, func() { time.Sleep(5 * time.Millisecond) })
	tr.Track(PhaseRegister, func() { time.Sleep(30 * time.Millisecond) })
	tr.Track(PhaseHeartbeat, func() { time.Sleep(5 * time.Millisecond) })

	snap := tr.Snapshot()
	if len(snap.Costs) != 3 {
		t.Fatalf("应记录 3 个阶段，实际 %d", len(snap.Costs))
	}
	// 按发生顺序
	want := []Phase{PhaseConfig, PhaseRegister, PhaseHeartbeat}
	for i, w := range want {
		if snap.Costs[i].Phase != w {
			t.Fatalf("第 %d 个阶段应为 %s，实际 %s", i, w, snap.Costs[i].Phase)
		}
	}
	if snap.Slowest != PhaseRegister {
		t.Fatalf("最慢阶段应为 register，实际 %s", snap.Slowest)
	}
	if snap.Tracked < 35*time.Millisecond {
		t.Fatalf("已记录总耗时应 ≥35ms，实际 %v", snap.Tracked)
	}
}

// TestSortedSummariesDescending 守住「最慢的排最前」。
func TestSortedSummariesDescending(t *testing.T) {
	tr := New()
	tr.Track(PhaseConfig, func() { time.Sleep(2 * time.Millisecond) })
	tr.Track(PhaseIngestStateLoad, func() { time.Sleep(25 * time.Millisecond) })

	items := tr.Snapshot().SortedSummaries()
	if len(items) != 2 {
		t.Fatalf("应有 2 条汇总，实际 %d", len(items))
	}
	if !strings.HasPrefix(items[0], string(PhaseIngestStateLoad)) {
		t.Fatalf("最慢阶段应排首位，实际首位是 %q", items[0])
	}
}

// TestConcurrentAccessIsSafe 守住并发安全（range② 的上报会从另一 goroutine 读快照）。
//
// 配合 -race 才有意义：go test -race ./internal/worker/startup/
func TestConcurrentAccessIsSafe(t *testing.T) {
	tr := New()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = tr.Snapshot()
				_ = tr.Elapsed()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 50; j++ {
			tr.Track(PhaseConfig, func() {})
		}
	}()
	wg.Wait()
}

// TestNilTrackerIsSafe 守住零值可用：装配层可能没注入 tracker（如测试构造）。
//
// 若 nil 不安全，所有既有测试构造点都要改，等于为了新特性破坏旧结构。
func TestNilTrackerIsSafe(t *testing.T) {
	var tr *Tracker
	tr.Begin(PhaseConfig)
	if d := tr.Done(PhaseConfig); d != 0 {
		t.Fatalf("nil tracker 的 Done 应返回 0，实际 %v", d)
	}
	if tr.Elapsed() != 0 {
		t.Fatal("nil tracker 的 Elapsed 应为 0")
	}
	if snap := tr.Snapshot(); len(snap.Costs) != 0 {
		t.Fatal("nil tracker 的快照应为空")
	}
	if tr.PhaseCost(PhaseConfig) != 0 {
		t.Fatal("nil tracker 的 PhaseCost 应为 0")
	}
}

// TestAllPhasesStable 守住阶段全集不被意外增删（它是对外契约的一部分）。
func TestAllPhasesStable(t *testing.T) {
	got := AllPhases()
	want := []Phase{
		"config", "dataroot", "identity", "process_recover",
		"ingest_index_open", "ingest_state_load", "register", "heartbeat",
	}
	if len(got) != len(want) {
		t.Fatalf("阶段数应为 %d，实际 %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 个阶段应为 %s，实际 %s", i, want[i], got[i])
		}
	}
	// 副本语义：改动返回值不应影响内部
	got[0] = "mutated"
	if AllPhases()[0] != PhaseConfig {
		t.Fatal("AllPhases 必须返回副本，调用方改动不能污染内部状态")
	}
}
