// 本测试证明：派发快照与"取消订阅"并发时，**close(ch) 会招致 send on closed channel**（修前红 ✗）；
// 修法（订阅侧只摘除、不 close ✓）后同形并发不再 panic（绿 ✓）。注释为中文 ✓。
package grpc

import (
	"sync"
	"sync/atomic"
	"testing"
)

// TestDispatchConcurrentUnsubscribeLegacyClose 以**修前语义**（取消订阅时 close ✓）复现竞争窗口 ⇒ 期望 panic。
func TestDispatchConcurrentUnsubscribeLegacyClose(t *testing.T) {
	for round := 0; round < 200; round++ {
		s := &Server{}
		ch := make(chan instanceEvent, 1)
		s.eventMu.Lock()
		s.eventSubs = append(s.eventSubs, ch)
		s.eventMu.Unlock()

		var wg sync.WaitGroup
		panicked := int32(0)
		stopDrain := make(chan struct{})
		wg.Add(3)
		go func() { // **排空读者**：真订阅者的行为 ✓（否则 select/default 会掩蔽竞争 ✗）
			defer wg.Done()
			for {
				select {
				case <-stopDrain:
					return
				case _, ok := <-ch:
					if !ok {
						return // 通道被 close（修前语义 ✓）⇒ 排空者随即退出 ⇒ 无自锁 ✓
					}
				}
			}
		}()
		go func() { // 派发者：快照 → 解锁 → 发送（server.go:220-229 同形 ✓）
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					atomic.StoreInt32(&panicked, 1) // 捕获 ⇒ 由断言判定"修前必红" ✓
				}
			}()
			for i := 0; i < 5000; i++ {
				s.dispatch(instanceEvent{UUID: "u"})
			}
		}()
		go func() { // 取消订阅者：摘除后 close（**修前**语义 ✓）
			defer wg.Done()
			s.eventMu.Lock()
			for i, sub := range s.eventSubs {
				if sub == ch {
					s.eventSubs = append(s.eventSubs[:i], s.eventSubs[i+1:]...)
					break
				}
			}
			s.eventMu.Unlock()
			close(ch)
		}()
		wg.Wait()
		close(stopDrain)
		if atomic.LoadInt32(&panicked) == 1 {
			t.Fatalf("第 %d 轮复现 send on closed channel（修前语义必红 ✓）", round)
		}
	}
}

// TestDispatchConcurrentUnsubscribeNoClose 以**修后语义**（只摘除、不 close ✓）同形并发 ⇒ 必须无 panic（绿 ✓）。
func TestDispatchConcurrentUnsubscribeNoClose(t *testing.T) {
	s := &Server{}
	for round := 0; round < 200; round++ {
		ch := make(chan instanceEvent, 64)
		s.eventMu.Lock()
		s.eventSubs = append(s.eventSubs, ch)
		s.eventMu.Unlock()

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				s.dispatch(instanceEvent{UUID: "u"}) // 无 recover：真 panic 就红 ✓
			}
		}()
		go func() {
			defer wg.Done()
			s.eventMu.Lock()
			for i, sub := range s.eventSubs {
				if sub == ch {
					s.eventSubs = append(s.eventSubs[:i], s.eventSubs[i+1:]...)
					break
				}
			}
			s.eventMu.Unlock()
		}()
		wg.Wait()
	}
}
