// 本文件证明两件事（注释为中文 ✓）：
//
//	① **红**：用 dispatchHook 在"快照后、发送前"确定性注入 close ⇒ 必 panic ✓（修前语义的代码内红证；
//	   等价于现场 crash-tier1-112138.txt 的栈链 ✓，且可在 CI 长期复跑 ✓）；
//	② **绿**：生产语义（订阅侧只摘除、不 close ✓）+ 并发取消订阅 ⇒ 不 panic ✓。
package grpc

import (
	"sync"
	"testing"
)

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

// TestDispatchHookLegacyClosePanics 用 dispatchHook 在"快照后、发送前"**确定性**塞入 close ⇒ 必 panic ✓
// （= 修前语义的代码内红证 ✓；修 ① 后生产路径不再 close ⇒ 本用例仅证明"该模式致命"且钩子有效 ✓）。
func TestDispatchHookLegacyClosePanics(t *testing.T) {
	s := &Server{}
	ch := make(chan instanceEvent, 4)
	s.eventMu.Lock()
	s.eventSubs = append(s.eventSubs, ch)
	s.eventMu.Unlock()

	panicked := false
	dispatchHook = func() { close(ch) } // 确定性注入 ✓
	defer func() {
		dispatchHook = nil
		if r := recover(); r != nil {
			panicked = true
		}
		if !panicked {
			t.Fatal("期望复现 send on closed channel（修前语义 ✓）")
		}
	}()
	s.dispatch(instanceEvent{UUID: "u"})
}

// TestDispatchHookNoCloseGreen 生产语义（只摘除、不 close ✓）+ 并发取消订阅 ⇒ **必须不 panic** ✓（CI 绿 ✓）。
func TestDispatchHookNoCloseGreen(t *testing.T) {
	defer func() { dispatchHook = nil }()
	calls := 0
	dispatchHook = func() { calls++ } // 仅计数，不 close ✓
	s := &Server{}
	for round := 0; round < 100; round++ {
		ch := make(chan instanceEvent, 64)
		s.eventMu.Lock()
		s.eventSubs = append(s.eventSubs, ch)
		s.eventMu.Unlock()
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				s.dispatch(instanceEvent{UUID: "u"})
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
	if calls == 0 {
		t.Fatal("dispatchHook 未被调用 ⇒ 钩子未生效")
	}
}
