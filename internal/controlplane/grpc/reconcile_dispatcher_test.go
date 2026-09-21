package grpc

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestEvidenceReconcileDispatcher_PerNodeSingleFlight FR-456 F10：同节点在飞时的重复派发被跳过，
// 不同节点互不影响。
func TestEvidenceReconcileDispatcher_PerNodeSingleFlight(t *testing.T) {
	d := NewEvidenceReconcileDispatcher()
	release := make(chan struct{})
	done := make(chan struct{}, 4)
	var calls int32
	task := func() {
		atomic.AddInt32(&calls, 1)
		done <- struct{}{}
		<-release
	}

	d.Dispatch("n1", task) // 执行
	<-done
	d.Dispatch("n1", task) // 同节点在飞 → 跳过
	d.Dispatch("n2", task) // 不同节点 → 执行
	<-done

	require.Equal(t, int32(2), atomic.LoadInt32(&calls), "同节点单飞，异节点不受影响")
	close(release)

	// 在飞清空后同节点可再次派发。
	require.Eventually(t, func() bool {
		d.mu.Lock()
		busy := len(d.inflight) > 0
		d.mu.Unlock()
		return !busy
	}, time.Second, 5*time.Millisecond)
}

// TestEvidenceReconcileDispatcher_Noop 空节点/空任务忽略。
func TestEvidenceReconcileDispatcher_Noop(t *testing.T) {
	d := NewEvidenceReconcileDispatcher()
	d.Dispatch("", func() { t.Fatal("空节点不应执行") })
	d.Dispatch("n", nil)
	var nilD *evidenceReconcileDispatcher
	nilD.Dispatch("n", func() { t.Fatal("nil 派发器不应执行") })
	// 给潜在 goroutine 一点时间触发（若误执行会 Fatal）。
	time.Sleep(20 * time.Millisecond)
}
