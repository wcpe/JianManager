package metrics

import (
	"context"
	"log/slog"
	"runtime"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

// NodeMetrics 节点指标。
type NodeMetrics struct {
	CPUUsage float32
	// IOWait 是 CPU 处于 IO 等待的占比（0..1，FR-485）。
	// 只看 CPUUsage 会把 IO 瓶颈看成「CPU 不满」，本机事故中 iowait 曾达 90%。
	IOWait           float32
	MemoryUsage      float32
	DiskUsage        float32
	MemoryUsedMB     int64
	MemoryTotalMB    int64
	DiskUsedMB       int64
	DiskTotalMB      int64
	Goroutines       int
	NetworkBytesSent int64
	NetworkBytesRecv int64
}

// Collector 指标采集器。
type Collector struct {
	interval time.Duration
	stopCh   chan struct{}
}

// NewCollector 创建指标采集器。
func NewCollector(interval time.Duration) *Collector {
	return &Collector{
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

// IOWaitRatio 把 cpu.Times 各态归一为「IO 等待占比」（0..1）。
//
// 抽成纯函数以便单测：真实机器上 iowait 常在 0 附近，
// 直接对 Collect 做「大于零」断言会假红，而对本函数可用构造输入精确验证。
func IOWaitRatio(t cpu.TimesStat) float32 {
	total := t.User + t.System + t.Idle + t.Nice + t.Iowait + t.Irq + t.Softirq + t.Steal
	if total <= 0 {
		return 0
	}
	return float32(t.Iowait / total)
}

// Collect 采集当前节点指标。
func (c *Collector) Collect() NodeMetrics {
	ctx := context.Background()
	metrics := NodeMetrics{
		Goroutines: runtime.NumGoroutine(),
	}

	// CPU 使用率
	if percents, err := cpu.PercentWithContext(ctx, time.Second, false); err == nil && len(percents) > 0 {
		metrics.CPUUsage = float32(percents[0] / 100.0)
	}

	// IO 等待占比（FR-485）：取自 cpu.Times 的 Iowait，按各态总量归一。
	if times, err := cpu.TimesWithContext(ctx, false); err == nil && len(times) > 0 {
		metrics.IOWait = IOWaitRatio(times[0])
	}
	if times, err := cpu.TimesWithContext(ctx, false); err == nil && len(times) > 0 {
		t := times[0]
		total := t.User + t.System + t.Idle + t.Nice + t.Iowait + t.Irq + t.Softirq + t.Steal
		if total > 0 {
			metrics.IOWait = float32(t.Iowait / total)
		}
	}
	// 内存使用率
	if vmem, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		metrics.MemoryUsage = float32(vmem.UsedPercent / 100.0)
		metrics.MemoryUsedMB = int64(vmem.Used / 1024 / 1024)
		metrics.MemoryTotalMB = int64(vmem.Total / 1024 / 1024)
	}

	// 磁盘使用率
	if usage, err := disk.UsageWithContext(ctx, "/"); err == nil {
		metrics.DiskUsage = float32(usage.UsedPercent / 100.0)
		metrics.DiskUsedMB = int64(usage.Used / 1024 / 1024)
		metrics.DiskTotalMB = int64(usage.Total / 1024 / 1024)
	}

	// 网络 IO（所有网卡汇总）
	if counters, err := net.IOCountersWithContext(ctx, false); err == nil && len(counters) > 0 {
		metrics.NetworkBytesSent = int64(counters[0].BytesSent)
		metrics.NetworkBytesRecv = int64(counters[0].BytesRecv)
	}

	return metrics
}

// StartPeriodic 启动周期性采集。
func (c *Collector) StartPeriodic(callback func(NodeMetrics)) {
	go func() {
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()

		for {
			select {
			case <-c.stopCh:
				return
			case <-ticker.C:
				metrics := c.Collect()
				callback(metrics)
			}
		}
	}()

	slog.Info("指标采集器已启动", "interval", c.interval)
}

// Stop 停止周期性采集。
func (c *Collector) Stop() {
	close(c.stopCh)
	slog.Info("指标采集器已停止")
}
