package vlsup

import (
	"sync"
	"testing"
)

func TestBudgetSnapshotIsDeepReadOnlyActualReservationAndAdmission(t *testing.T) {
	c, err := NewBudgetController(RuntimeBudget{DiskBytes: 400, Namespaces: map[Namespace]NamespaceBudget{NamespaceHot: {RSSBytes: 100}}, Reservations: map[ReservationKind]int64{ReservationWAL: 40, ReservationStaging: 40, ReservationExport: 20}})
	if err != nil {
		t.Fatal(err)
	}
	initial := c.Snapshot()
	if initial.Allowed[WorkBackground] || !initial.Allowed[WorkWALDrain] || initial.Reasons[WorkBackground] == "" {
		t.Fatal("未知状态准入必须如实暴露")
	}
	c.Update(knownResourceSample())
	release, err := c.Reserve(ReservationStaging, 30)
	if err != nil {
		t.Fatal(err)
	}
	view := c.Snapshot()
	if view.TotalReservedBytes != 30 || view.Reserved[ReservationStaging] != 30 || !view.Allowed[WorkBackground] || view.Sample.SampledAt.IsZero() || view.TakenAt.IsZero() {
		t.Fatalf("不是实际占用/采样/许可快照: %+v", view)
	}
	view.Policy.Namespaces[NamespaceHot] = NamespaceBudget{RSSBytes: 1}
	view.Policy.Reservations[ReservationStaging] = 1
	view.Sample.Namespaces[NamespaceHot] = NamespaceSample{}
	view.Reserved[ReservationStaging] = 999
	view.Allowed[WorkBackground] = false
	check := c.Snapshot()
	if check.Policy.Namespaces[NamespaceHot].RSSBytes != 100 || check.Policy.Reservations[ReservationStaging] != 40 || !check.Sample.Namespaces[NamespaceHot].RSSKnown || check.Reserved[ReservationStaging] != 30 || !check.Allowed[WorkBackground] {
		t.Fatal("快照修改影响了控制器")
	}
	release()
	if c.Snapshot().TotalReservedBytes != 0 {
		t.Fatal("释放必须同步反映实际占用")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := c.Reserve(ReservationWAL, 1)
			_ = c.Snapshot()
			if e == nil {
				r()
			}
		}()
	}
	wg.Wait()
	if c.Snapshot().TotalReservedBytes != 0 {
		t.Fatal("并发快照不得改变预留")
	}
}
