// 本测试验证 P0 修法 ② 的两条不变量（注释为中文 ✓）：
//
//	① 有界等待原语：readWg 归零 ⇒ true ✓；超时 ⇒ false（**不无界** ✗）✓；
//	② "关旧 conn ⇒ 旧环必退"：对 net.Pipe 起的 readLoop，关掉对端/本端后，循环在界内退出（readWg 归零 ✓）。
package process

import (
	"net"
	"sync"
	"testing"
	"time"
)

func TestWaitReadLoopsBounded_True(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { time.Sleep(10 * time.Millisecond); wg.Done() }()
	if !(&daemonStrategy{}).waitReadLoopsBounded(&wg, 2*time.Second) {
		t.Fatal("应在界内归零 ⇒ true")
	}
}

// TestWaitReadLoopsBounded_False 钉住「超时后**不遗留阻塞 Wait**」这条不变量（M1，2026-10-12）。
//
// 语义已随修复变化：此前超时即返回（false），但遗留的 `wg.Wait()` 会与调用方随后的
// `Add`（起新读环）相撞，触发 Go 运行时 panic（`WaitGroup is reused before previous Wait
// has returned`），且 panic 在旁路 goroutine 内、调用方 recover 兜不住 ⇒ 终止整个 Worker。
// 修复后超时分支**继续等到归零**才返回，故本用例断言两件事：
//   - 返回 false（确实超时了）；
//   - 返回时计数已归零 ⇒ 后续 Add 安全（这是消除 panic 的充要条件）。
func TestWaitReadLoopsBounded_False(t *testing.T) {
	// 确定性判据（M1，2026-10-12）：**旧环未退出时，本方法不得返回**。
	//
	// 为什么不能用"是否 panic"作判据：Go 的 panic 需要 Add 恰好落在「Done 释放 sema」
	// 与「Wait 执行检查」之间的纳秒窗口，无法确定性触发（我实测 300 轮才偶发）。
	// 而"不得返回"是该不变量**等价且确定**的表述：只要返回时 Wait 已结束，
	// 后续 Add 就必然安全；反之返回时 Wait 仍在飞，Add 就有机会撞上它。
	//
	// 转红方式（实测）：把超时分支改回「直接 return false」（遗留阻塞 Wait）⇒
	// 本用例在第一个 select 处立即红。
	var wg sync.WaitGroup
	wg.Add(1)
	// 故意不 Done：模拟"读环被楔住"（读环会同步调 onOutput → WS 广播 / 落盘，任一处长阻塞都满足）。

	resCh := make(chan bool, 1)
	go func() { resCh <- (&daemonStrategy{}).waitReadLoopsBounded(&wg, 20*time.Millisecond) }()

	// 预算已过（20ms），但旧环仍在飞 ⇒ 修复后**不得返回**。
	select {
	case <-resCh:
		t.Fatal("旧环未退出时不得返回：遗留的阻塞 Wait 会与后续 Add 相撞，" +
			"触发 `WaitGroup is reused before previous Wait has returned` panic（在旁路 goroutine 内，无法 recover）")
	case <-time.After(100 * time.Millisecond):
		// 仍未返回 ✓
	}

	// 释放旧环（生产对应"强关 oldConn ⇒ 读环退出"）⇒ 现在才应返回 false。
	wg.Done()
	select {
	case got := <-resCh:
		if got {
			t.Fatal("旧环是被释放的、非预算内归零 ⇒ 必须返回 false")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("旧环退出后应立即返回（不得无界等待）")
	}

	// 返回后同一 WaitGroup 必须可安全复用（这是消除 panic 的充要条件）。
	wg.Add(1)
	go wg.Done()
	wg.Wait()
}

// TestReadLoopExitsOnConnClose_20Rounds 等价于"≥20 次重连"的停止面：每轮起 readLoop 后关 conn ⇒ 界内退出 ✓。
func TestReadLoopExitsOnConnClose_20Rounds(t *testing.T) {
	mgr := &Manager{}
	for round := 0; round < 20; round++ {
		server, client := net.Pipe()
		d := &daemonStrategy{mgr: mgr}
		d.readWg.Add(1)
		go d.readLoop(server)
		time.Sleep(2 * time.Millisecond) // 让循环进入 Read ✓
		_ = server.Close()               // 停止信号：关旧 conn（读必失败 ⇒ 旧环退 ✓）
		if !d.waitReadLoopsBounded(&d.readWg, 2*time.Second) {
			t.Fatalf("第 %d 轮：关 conn 后旧环未在界内退出 ⇒ ② 不成立", round)
		}
		_ = client.Close()
	}
}
