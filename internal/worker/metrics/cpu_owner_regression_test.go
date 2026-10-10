package metrics

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/stretchr/testify/require"
)

// 长期空闲后的突发等待应按当前窗口计算，不得被开机以来累计值稀释。
func TestCPUOwnerWindowNotHistoricalAverage(t *testing.T) {
	calls := 0
	now := time.Unix(100, 0)
	current := cpu.TimesStat{Idle: 100000}
	s := NewCPUSamplerWithReader(time.Second, func(_ context.Context, perCPU bool) ([]cpu.TimesStat, error) {
		require.False(t, perCPU)
		calls++
		return []cpu.TimesStat{current}, nil
	}, func() time.Time { return now })
	c := NewCollector(time.Second, s)
	s.Sample(context.Background())
	require.False(t, s.Snapshot().Valid)
	current = cpu.TimesStat{Idle: 100010, Iowait: 90}
	now = now.Add(time.Second)
	s.Sample(context.Background())
	got := c.Collect()
	require.InDelta(t, .9, got.IOWait, 1e-6)
	require.Zero(t, got.CPUUsage, "idle 与 iowait 均不得计入 busy")
	require.Equal(t, 2, calls, "两窗口只能各读取一次")

	// 等待消退后必须立即恢复为当前窗口的零，而不是历史累计平均。
	current.Idle += 100
	now = now.Add(time.Second)
	s.Sample(context.Background())
	require.True(t, s.Snapshot().Valid)
	require.Zero(t, c.Collect().IOWait)
	require.Equal(t, 3, calls)
}

// 周期采集、心跳和实时 RPC 的读取不能触发三套 CPU 采样。
func TestCPUOwnerConsumersDoNotResample(t *testing.T) {
	calls := 0
	now := time.Unix(100, 0)
	current := cpu.TimesStat{Idle: 100000}
	s := NewCPUSamplerWithReader(time.Second, func(context.Context, bool) ([]cpu.TimesStat, error) {
		calls++
		return []cpu.TimesStat{current}, nil
	}, func() time.Time { return now })
	c := NewCollector(time.Second, s)
	s.Sample(context.Background())
	current.Iowait += 90
	current.Idle += 10
	now = now.Add(time.Second)
	s.Sample(context.Background())
	before := s.Snapshot()
	calls = 0
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Collect()
		}()
	}
	wg.Wait()
	require.Zero(t, calls, "消费者只能读已发布快照，不得读取累计计数")
	require.Equal(t, before, s.Snapshot())
	for range 3 {
		s.Sample(context.Background())
	}
	require.Zero(t, calls, "同窗口重复驱动 owner 也不能二次读取")
}

func TestCPUOwnerInvalidWindowsAndRecovery(t *testing.T) {
	cases := []struct {
		name    string
		current cpu.TimesStat
		err     error
		empty   bool
		want    string
		rewarm  bool
	}{
		{name: "零增量", current: cpu.TimesStat{Idle: 100, Iowait: 10}, want: "无有效增量"},
		{name: "总计数回退", current: cpu.TimesStat{Idle: 1}, want: "回退"},
		{name: "等待回退但总量增加", current: cpu.TimesStat{Idle: 200, Iowait: 9}, want: "回退"},
		{name: "读取失败", err: errors.New("测试读取失败"), want: "读取失败", rewarm: true},
		{name: "空样本", empty: true, want: "缺失或无效", rewarm: true},
		{name: "非数", current: cpu.TimesStat{Idle: math.NaN()}, want: "缺失或无效", rewarm: true},
		{name: "无穷大", current: cpu.TimesStat{Idle: math.Inf(1)}, want: "缺失或无效", rewarm: true},
		{name: "负计数", current: cpu.TimesStat{Idle: -1}, want: "缺失或无效", rewarm: true},
		{name: "总量溢出", current: cpu.TimesStat{Idle: math.MaxFloat64, User: math.MaxFloat64, Iowait: 10}, want: "无有效增量", rewarm: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Unix(100, 0)
			current := cpu.TimesStat{Idle: 100, Iowait: 10}
			var err error
			empty := false
			s := NewCPUSamplerWithReader(time.Second, func(context.Context, bool) ([]cpu.TimesStat, error) {
				if empty {
					return nil, err
				}
				return []cpu.TimesStat{current}, err
			}, func() time.Time { return now })
			s.Sample(context.Background())
			current, err, empty = tt.current, tt.err, tt.empty
			now = now.Add(time.Second)
			s.Sample(context.Background())
			invalid := s.Snapshot()
			require.False(t, invalid.Valid)
			require.Contains(t, invalid.Error, tt.want)
			require.Zero(t, invalid.IOWait)
			require.Zero(t, invalid.CPUUsage)
			// 失败后必须重新建立基线，不跨缺测窗口借用旧计数。
			if tt.rewarm {
				current, err, empty = cpu.TimesStat{Idle: 500, Iowait: 20}, nil, false
				now = now.Add(time.Second)
				s.Sample(context.Background())
				require.False(t, s.Snapshot().Valid)
			}
			current.Idle += 10
			current.Iowait += 90
			now = now.Add(time.Second)
			s.Sample(context.Background())
			require.True(t, s.Snapshot().Valid, s.Snapshot().Error)
			require.InDelta(t, .9, s.Snapshot().IOWait, 1e-6)
		})
	}
}

func TestCPUOwnerBusyAndGuestAccounting(t *testing.T) {
	now := time.Unix(100, 0)
	current := cpu.TimesStat{User: 1000, Idle: 1000, Guest: 500, GuestNice: 100}
	s := NewCPUSamplerWithReader(time.Second, func(context.Context, bool) ([]cpu.TimesStat, error) {
		return []cpu.TimesStat{current}, nil
	}, func() time.Time { return now })
	s.Sample(context.Background())
	current = cpu.TimesStat{User: 1010, System: 5, Nice: 5, Irq: 5, Softirq: 5, Steal: 10, Idle: 1020, Iowait: 40, Guest: 510, GuestNice: 105}
	now = now.Add(time.Second)
	s.Sample(context.Background())
	require.InDelta(t, .4, s.Snapshot().CPUUsage, 1e-6)
	require.InDelta(t, .4, s.Snapshot().IOWait, 1e-6)
}

func TestCPUOwnerWindowJitterAndClockRollback(t *testing.T) {
	now := time.Unix(100, 0)
	calls := 0
	s := NewCPUSamplerWithReader(time.Second, func(context.Context, bool) ([]cpu.TimesStat, error) {
		calls++
		return []cpu.TimesStat{{Idle: float64(calls)}}, nil
	}, func() time.Time { return now })
	s.Sample(context.Background())
	now = now.Add(time.Second + time.Millisecond)
	s.Sample(context.Background())
	now = now.Add(time.Second - time.Microsecond)
	s.Sample(context.Background())
	require.Equal(t, 3, calls, "微小调度抖动不得漏一拍")
	now = now.Add(-time.Minute)
	s.Sample(context.Background())
	require.Contains(t, s.Snapshot().Error, "时钟回退")
	require.False(t, s.Snapshot().Valid)
	now = now.Add(time.Second)
	s.Sample(context.Background())
	require.Contains(t, s.Snapshot().Error, "预热")
	now = now.Add(time.Second)
	s.Sample(context.Background())
	require.True(t, s.Snapshot().Valid)
}

func TestCPUOwnerSnapshotDoesNotBlockOnSystemRead(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s := NewCPUSamplerWithReader(time.Second, func(context.Context, bool) ([]cpu.TimesStat, error) {
		close(entered)
		<-release
		return []cpu.TimesStat{{Idle: 100}}, nil
	}, time.Now)
	go func() { defer close(done); s.Sample(context.Background()) }()
	<-entered
	read := make(chan CPUSnapshot, 1)
	go func() { read <- s.Snapshot() }()
	select {
	case snapshot := <-read:
		require.False(t, snapshot.Valid)
	case <-time.After(time.Second):
		t.Error("只读消费者被系统计数读取阻塞")
	}
	close(release)
	<-done
}

func TestCPUOwnerReadFailureClearsPublishedSample(t *testing.T) {
	now := time.Unix(100, 0)
	current := cpu.TimesStat{Idle: 100000}
	var readErr error
	calls := 0
	s := NewCPUSamplerWithReader(time.Second, func(context.Context, bool) ([]cpu.TimesStat, error) {
		calls++
		return []cpu.TimesStat{current}, readErr
	}, func() time.Time { return now })
	s.Sample(context.Background())
	now = now.Add(time.Second)
	current.Iowait, current.Idle = 90, 100010
	s.Sample(context.Background())
	require.InDelta(t, .9, s.Snapshot().IOWait, 1e-6)
	readErr = errors.New("测试读取失败")
	now = now.Add(time.Second)
	s.Sample(context.Background())
	s.Sample(context.Background())
	require.Equal(t, 3, calls, "失败窗口同样只读一次")
	require.False(t, s.Snapshot().Valid)
	require.Zero(t, s.Snapshot().IOWait, "读取失败不能沿用历史成功快照伪装为当前窗口")
	require.Zero(t, NewCollector(time.Second, s).Collect().IOWait)
}

func TestCPUOwnerIndependentBaselinesAndConcurrentSamples(t *testing.T) {
	now := time.Unix(100, 0)
	left, right := cpu.TimesStat{Idle: 100000}, cpu.TimesStat{User: 5000}
	leftReads, rightReads := 0, 0
	a := NewCPUSamplerWithReader(time.Second, func(context.Context, bool) ([]cpu.TimesStat, error) {
		leftReads++
		return []cpu.TimesStat{left}, nil
	}, func() time.Time { return now })
	b := NewCPUSamplerWithReader(time.Second, func(context.Context, bool) ([]cpu.TimesStat, error) {
		rightReads++
		return []cpu.TimesStat{right}, nil
	}, func() time.Time { return now })
	a.Sample(context.Background())
	b.Sample(context.Background())
	now = now.Add(time.Second)
	left.Idle, left.Iowait, right.User = 100010, 90, 5100
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(2)
		go func() { defer wg.Done(); a.Sample(context.Background()) }()
		go func() { defer wg.Done(); b.Sample(context.Background()) }()
	}
	wg.Wait()
	require.Equal(t, 2, leftReads)
	require.Equal(t, 2, rightReads)
	require.InDelta(t, .9, a.Snapshot().IOWait, 1e-6)
	require.Zero(t, a.Snapshot().CPUUsage)
	require.Equal(t, float32(1), b.Snapshot().CPUUsage)
	require.Zero(t, b.Snapshot().IOWait, "独立 owner 不得共享全局 previous")
	copy := a.Snapshot()
	copy.IOWait = 0
	require.Zero(t, copy.IOWait)
	require.InDelta(t, .9, a.Snapshot().IOWait, 1e-6, "消费者持有的是发布值的拷贝")
}

func TestCPUOwnerStopCancelsActiveRead(t *testing.T) {
	entered, startDone := make(chan struct{}), make(chan struct{})
	s := NewCPUSamplerWithReader(time.Hour, func(ctx context.Context, _ bool) ([]cpu.TimesStat, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}, time.Now)
	go func() { s.Start(context.Background()); close(startDone) }()
	<-entered
	s.Stop()
	<-startDone
	require.False(t, s.Snapshot().Valid)
	require.Contains(t, s.Snapshot().Error, "读取失败")
}

func TestCPUOwnerStartStopIdempotent(t *testing.T) {
	calls := 0
	s := NewCPUSamplerWithReader(time.Hour, func(context.Context, bool) ([]cpu.TimesStat, error) {
		calls++
		return []cpu.TimesStat{{Idle: 100}}, nil
	}, time.Now)
	s.Start(context.Background())
	s.Start(context.Background())
	s.Stop()
	s.Stop()
	require.Equal(t, 1, calls)

	stopped := NewCPUSamplerWithReader(time.Hour, func(context.Context, bool) ([]cpu.TimesStat, error) {
		t.Fatal("停止后不得再读取系统")
		return nil, nil
	}, time.Now)
	stopped.Stop()
	stopped.Start(context.Background())
	stopped.Stop()
}
