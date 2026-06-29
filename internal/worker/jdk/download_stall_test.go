package jdk

import (
	"context"
	"testing"
	"time"
)

// TestWatchStall_CancelsOnNoProgress 无字节进展达 stall 时长 → 看门狗 cancel（FIX-4：判卡死）。
// 复现原「15min 总超时把慢但在进展的下载也掐断」改为「仅无进展才中断」的核心逻辑。
func TestWatchStall_CancelsOnNoProgress(t *testing.T) {
	pw := &progressWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go watchStall(cancel, pw, 60*time.Millisecond, 15*time.Millisecond, done)

	select {
	case <-ctx.Done():
		// 预期：无字节进展超过 stall → 取消。
	case <-time.After(2 * time.Second):
		t.Fatal("无字节进展超过 stall 仍未取消")
	}
}

// TestWatchStall_NoCancelWhileProgressing 持续有字节进展（远超 stall 总时长）→ 不取消（慢但在进展不掐断）。
func TestWatchStall_NoCancelWhileProgressing(t *testing.T) {
	pw := &progressWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go watchStall(cancel, pw, 80*time.Millisecond, 15*time.Millisecond, done)

	// 每 10ms 进展一次，持续 300ms（>> stall 80ms）：每个 tick 都见进展 → idle 永不累积到 stall。
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		pw.written.Add(1024)
		time.Sleep(10 * time.Millisecond)
	}
	close(done)

	select {
	case <-ctx.Done():
		t.Fatal("持续进展中不应被取消")
	default:
	}
}
