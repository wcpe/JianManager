package metrics

import (
	"context"
	"log/slog"
	"runtime"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
)

// NodeMetrics 节点指标。
type NodeMetrics struct {
	CPUUsage     float32
	MemoryUsage  float32
	DiskUsage    float32
	MemoryUsedMB int64
	MemoryTotalMB int64
	DiskUsedMB   int64
	DiskTotalMB  int64
	Goroutines   int
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
