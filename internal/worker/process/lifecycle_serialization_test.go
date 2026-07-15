package process

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type blockingLifecycleStrategy struct {
	mu          sync.Mutex
	state       InstanceState
	stopStarted chan struct{}
	stopRelease chan struct{}
	startOnce   sync.Once
	releaseOnce sync.Once
	concurrent  bool
}

func newBlockingLifecycleStrategy() *blockingLifecycleStrategy {
	return &blockingLifecycleStrategy{
		state:       StateRunning,
		stopStarted: make(chan struct{}),
		stopRelease: make(chan struct{}),
	}
}

func (s *blockingLifecycleStrategy) Start(context.Context) error {
	s.mu.Lock()
	s.state = StateRunning
	s.mu.Unlock()
	return nil
}

func (s *blockingLifecycleStrategy) Stop() error {
	s.mu.Lock()
	s.state = StateStopping
	s.mu.Unlock()
	s.startOnce.Do(func() { close(s.stopStarted) })
	<-s.stopRelease
	s.mu.Lock()
	s.state = StateStopped
	s.mu.Unlock()
	return nil
}

func (s *blockingLifecycleStrategy) Kill() error {
	s.markConcurrentWithStop()
	s.mu.Lock()
	s.state = StateStopped
	s.mu.Unlock()
	return nil
}

func (s *blockingLifecycleStrategy) SendCommand(string) error { return nil }

func (s *blockingLifecycleStrategy) State() InstanceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *blockingLifecycleStrategy) Close() error {
	s.markConcurrentWithStop()
	return nil
}

func (s *blockingLifecycleStrategy) GetPID() int { return 0 }

func (s *blockingLifecycleStrategy) markConcurrentWithStop() {
	s.mu.Lock()
	if s.state == StateStopping {
		s.concurrent = true
	}
	s.mu.Unlock()
}

func (s *blockingLifecycleStrategy) releaseStop() {
	s.releaseOnce.Do(func() { close(s.stopRelease) })
}

func (s *blockingLifecycleStrategy) hadConcurrentCall() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.concurrent
}

func TestManager_RestartWaitsForStopAndKeepsNewProcessRunning(t *testing.T) {
	m := NewManager(t.TempDir())
	const uuid = "restart-serialized"
	require.NoError(t, createDirect(m, uuid, "并发重启实例", keepAliveCmd(), t.TempDir()))

	oldStrategy := installRunningStrategy(t, m, uuid)
	stopDone := runLifecycleOperation(func() error { return m.Stop(uuid) })
	waitForSignal(t, oldStrategy.stopStarted, "Stop 未进入阻塞点")

	restartStarted := make(chan struct{})
	restartDone := make(chan error, 1)
	go func() {
		close(restartStarted)
		restartDone <- m.Restart(uuid)
	}()
	waitForSignal(t, restartStarted, "Restart 未开始调用")
	assertStillBlocked(t, restartDone, "Restart 必须等待同实例 Stop 完成")
	require.False(t, oldStrategy.hadConcurrentCall(), "旧策略 Stop 阻塞期间不得并发 Kill/Close")

	oldStrategy.releaseStop()
	require.NoError(t, waitForResult(t, stopDone, "Stop 完成超时"))
	require.NoError(t, waitForResult(t, restartDone, "Restart 完成超时"))
	require.Equal(t, StateRunning, mustGetState(t, m, uuid))

	m.mu.RLock()
	newStrategy := m.instances[uuid].strategy
	m.mu.RUnlock()
	require.NotSame(t, oldStrategy, newStrategy, "Restart 必须启动新策略")
	require.False(t, oldStrategy.hadConcurrentCall(), "旧策略生命周期方法不得并发执行")
	t.Cleanup(func() { _ = m.Kill(uuid) })
}

func TestManager_RemoveWaitsForStop(t *testing.T) {
	m := NewManager(t.TempDir())
	const uuid = "remove-serialized"
	require.NoError(t, createDirect(m, uuid, "并发删除实例", keepAliveCmd(), t.TempDir()))

	strategy := installRunningStrategy(t, m, uuid)
	stopDone := runLifecycleOperation(func() error { return m.Stop(uuid) })
	waitForSignal(t, strategy.stopStarted, "Stop 未进入阻塞点")

	removeDone := runLifecycleOperation(func() error { return m.Remove(uuid) })
	assertStillBlocked(t, removeDone, "Remove 必须等待同实例 Stop 完成")
	require.False(t, strategy.hadConcurrentCall(), "Remove 不得与旧策略 Stop 并发 Kill/Close")

	strategy.releaseStop()
	require.NoError(t, waitForResult(t, stopDone, "Stop 完成超时"))
	require.NoError(t, waitForResult(t, removeDone, "Remove 完成超时"))
	_, exists := m.GetInstance(uuid)
	require.False(t, exists)
	require.False(t, strategy.hadConcurrentCall(), "Remove 应在 Stop 完成后再清理旧策略")
}

func TestManager_LifecycleOperationsRemainParallelAcrossInstances(t *testing.T) {
	m := NewManager(t.TempDir())
	require.NoError(t, createDirect(m, "instance-a", "实例 A", keepAliveCmd(), t.TempDir()))
	require.NoError(t, createDirect(m, "instance-b", "实例 B", keepAliveCmd(), t.TempDir()))

	strategyA := installRunningStrategy(t, m, "instance-a")
	installRunningStrategy(t, m, "instance-b")
	stopDone := runLifecycleOperation(func() error { return m.Stop("instance-a") })
	waitForSignal(t, strategyA.stopStarted, "实例 A 的 Stop 未进入阻塞点")

	killDone := runLifecycleOperation(func() error { return m.Kill("instance-b") })
	require.NoError(t, waitForResult(t, killDone, "实例 B 的 Kill 不应被实例 A 阻塞"))

	strategyA.releaseStop()
	require.NoError(t, waitForResult(t, stopDone, "实例 A 的 Stop 完成超时"))
}

func installRunningStrategy(t *testing.T, m *Manager, uuid string) *blockingLifecycleStrategy {
	t.Helper()
	strategy := newBlockingLifecycleStrategy()
	t.Cleanup(strategy.releaseStop)
	m.mu.Lock()
	inst := m.instances[uuid]
	inst.strategy = strategy
	inst.State = StateRunning
	m.mu.Unlock()
	return strategy
}

func runLifecycleOperation(operation func() error) <-chan error {
	done := make(chan error, 1)
	go func() { done <- operation() }()
	return done
}

func waitForSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal(message)
	}
}

func assertStillBlocked(t *testing.T, done <-chan error, message string) {
	t.Helper()
	select {
	case err := <-done:
		require.NoError(t, err)
		t.Fatal(message)
	case <-time.After(100 * time.Millisecond):
	}
}

func waitForResult(t *testing.T, done <-chan error, message string) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal(message)
		return nil
	}
}
