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
	if !waitReadLoopsBounded(&wg, 2*time.Second) {
		t.Fatal("应在界内归零 ⇒ true")
	}
}

func TestWaitReadLoopsBounded_False(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1) // 故意不 Done ⇒ 必须在界内返回 false（不无界 ✗）
	start := time.Now()
	if waitReadLoopsBounded(&wg, 50*time.Millisecond) {
		t.Fatal("不应归零 ⇒ false")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("有界等待超界：%v", d)
	}
	wg.Done()
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
		if !waitReadLoopsBounded(&d.readWg, 2*time.Second) {
			t.Fatalf("第 %d 轮：关 conn 后旧环未在界内退出 ⇒ ② 不成立", round)
		}
		_ = client.Close()
	}
}
