package vlsup

import "time"

// BudgetSnapshot 只读暴露同进程实际计量，不把未配置上界或WAL排空许可冒充预留批准。
// 所有map均为深拷贝，调用方改动快照不会修改控制器、上限或采样。
type BudgetSnapshot struct {
	Policy             RuntimeBudget
	Sample             ResourceSample
	Reserved           map[ReservationKind]int64
	TotalReservedBytes int64
	Allowed            map[WorkClass]bool
	Reasons            map[WorkClass]string
	TakenAt            time.Time
}

// Snapshot 在常量规模短锁内复制3个namespace/3类预留，绝不采磁盘或发HTTP。
func (c *BudgetController) Snapshot() BudgetSnapshot {
	out := BudgetSnapshot{TakenAt: time.Now(), Reserved: map[ReservationKind]int64{}, Allowed: map[WorkClass]bool{}, Reasons: map[WorkClass]string{}}
	if c == nil {
		for _, work := range []WorkClass{WorkForeground, WorkBackground, WorkWALDrain} {
			out.Allowed[work] = true
		}
		return out
	}
	// 锁序仅controller.mu；最多3个namespace与3项预留，不执行外部回调/IO。
	c.mu.Lock()
	defer c.mu.Unlock()
	out.Policy = c.policy
	out.Policy.Namespaces = map[Namespace]NamespaceBudget{}
	out.Policy.Reservations = map[ReservationKind]int64{}
	for ns, b := range c.policy.Namespaces {
		out.Policy.Namespaces[ns] = b
	}
	for kind, n := range c.policy.Reservations {
		out.Policy.Reservations[kind] = n
	}
	out.Sample = c.sample
	out.Sample.Namespaces = map[Namespace]NamespaceSample{}
	for ns, v := range c.sample.Namespaces {
		out.Sample.Namespaces[ns] = v
	}
	for _, kind := range []ReservationKind{ReservationWAL, ReservationStaging, ReservationExport} {
		out.Reserved[kind] = c.reserved[kind]
		out.TotalReservedBytes += c.reserved[kind]
	}
	for _, work := range []WorkClass{WorkForeground, WorkBackground, WorkWALDrain} {
		err := c.admissionLocked(work)
		out.Allowed[work] = err == nil
		if err != nil {
			out.Reasons[work] = err.Error()
		}
	}
	return out
}
