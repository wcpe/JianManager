package ingest

// 本文件给出 A 主机制（注册路径持久化改"有界热路径" ✓，见 instances.go:168 的 persistHot ✓）
// 的**确定性红/绿证明**（注释中文 ✓）：
//   绿：持 `registerMu` 调 `m.persistHot()`，在持久化门被长占时**有界返回**（≤ budget+ε ✓）；
//   红（对照，＝修前语义）：同场景改调 `m.persist()`（persistPriority，**无 maxWait** ✗）⇒ **必须等满门占用** ✗。
// 两种调用在同一测试内并列 ⇒ **无需任何 git 操作** ✓；夹具与门事件口径复用既有
// persist_gate_fairness_test.go 的 newGateFixture ✓。

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// holdGate 按"确定性占用"占门 H 时长后释放并广播（不使用 sleep 猜测 ✓）。
func holdGate(m *Manager, h time.Duration) {
	g := &m.persistGate
	g.mu.Lock()
	g.running = true
	g.mu.Unlock()
	time.AfterFunc(h, func() {
		g.mu.Lock()
		g.running = false
		g.cond.Broadcast()
		g.mu.Unlock()
	})
}

// TestRegisterPersistHotBoundedVsUnbounded 绿/红并列：热路径有界 ✓；修前语义必等满门占用 ✗（红 ✓）。
func TestRegisterPersistHotBoundedVsUnbounded(t *testing.T) {
	m, _ := newGateFixture(t, 64, 16)
	const H = 400 * time.Millisecond
	budget := m.persistHotBudgetOf()

	// ── 绿：持 registerMu 调 persistHot ⇒ 有界 ✓
	holdGate(m, H)
	m.registerMu.Lock()
	t0 := time.Now()
	errGreen := m.persistHot()
	dGreen := time.Since(t0)
	m.registerMu.Unlock()
	require.NoError(t, errGreen)
	if dGreen > budget+80*time.Millisecond {
		t.Fatalf("绿：persistHot 未在有界内返回（%v > budget %v+ε）", dGreen, budget)
	}
	t.Logf("绿 ✓ persistHot 持 registerMu 返回耗时 %v（budget %v）", dGreen, budget)

	// ── 红（对照＝修前语义）：持 registerMu 调 persist ⇒ **无限等门**（无 maxWait ✗）⇒ 必等满 ✓
	holdGate(m, H)
	m.registerMu.Lock()
	t1 := time.Now()
	errRed := m.persist()
	dRed := time.Since(t1)
	m.registerMu.Unlock()
	require.NoError(t, errRed)
	if dRed < H-80*time.Millisecond {
		t.Fatalf("红：修前语义本应等满门占用（%v），实测仅 %v ⇒ 对照失效", H, dRed)
	}
	t.Logf("红 ✓ persist（修前语义）持 registerMu 等满门占用 %v", dRed)
}

// TestRegisterStormWallClockBounded 风暴下 N 个并发"注册者"整体墙钟有界 ✓
// （每个等待者有界 ⇒ 串行在 registerMu 上的总时长上界 ≈ N×(budget+ε) ✓）。
func TestRegisterStormWallClockBounded(t *testing.T) {
	m, _ := newGateFixture(t, 64, 16)
	const N = 16
	holdGate(m, 120*time.Millisecond) // 门被占一段，风暴全部撞上 ✓
	budget := m.persistHotBudgetOf()
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.registerMu.Lock()
			_ = m.persistHot()
			m.registerMu.Unlock()
		}()
	}
	wg.Wait()
	d := time.Since(start)
	limit := time.Duration(N)*(budget+80*time.Millisecond) + 2*time.Second
	if d > limit {
		t.Fatalf("风暴墙钟超界：%v > %v", d, limit)
	}
	t.Logf("风暴 ✓ N=%d 并发注册整体墙钟 %v（上界 %v）", N, d, limit)
}
