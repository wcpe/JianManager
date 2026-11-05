package ingest

// VerifyBudgetSnapshot 的等待仅来自许可闸，不包含HTTP往返；短锁内读取一致快照。
type VerifyBudgetSnapshot struct {
	Acquired        uint64
	Completed       uint64
	Rejected        uint64
	WaitCount       uint64
	WaitNanoseconds int64
	Waiting         int
	InFlight        int
	MaxInFlight     int
	MaxPerSecond    int
}

func (m *Manager) VerifyBudgetStats() VerifyBudgetSnapshot {
	if m == nil || m.verifyLimiter == nil {
		return VerifyBudgetSnapshot{}
	}
	l := m.verifyLimiter
	l.mu.Lock()
	defer l.mu.Unlock()
	return VerifyBudgetSnapshot{Acquired: l.acquired, Completed: l.completed, Rejected: l.rejected, WaitCount: l.waitCount, WaitNanoseconds: l.waitNanoseconds, Waiting: l.waiting, InFlight: l.inFlight, MaxInFlight: l.maxInFlight, MaxPerSecond: l.rate}
}
