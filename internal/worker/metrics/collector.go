package metrics

import (
	"log/slog"
	"os"
	"runtime"
	"time"
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
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	metrics := NodeMetrics{
		MemoryUsedMB:  int64(m.Sys / 1024 / 1024),
		MemoryTotalMB: int64(m.Sys / 1024 / 1024), // 简化：使用 sys 作为总量
		Goroutines:    runtime.NumGoroutine(),
	}

	// 内存使用率
	if metrics.MemoryTotalMB > 0 {
		metrics.MemoryUsage = float32(m.Alloc) / float32(m.Sys)
	}

	// CPU 使用率（简化：基于 Goroutine 数估算）
	numCPU := runtime.NumCPU()
	metrics.CPUUsage = float32(runtime.NumGoroutine()) / float32(numCPU*100)
	if metrics.CPUUsage > 1.0 {
		metrics.CPUUsage = 1.0
	}

	// 磁盘使用率（简化）
	cwd, err := os.Getwd()
	if err == nil {
		usage := getDiskUsage(cwd)
		metrics.DiskUsage = usage
	}

	return metrics
}

// getDiskUsage 获取磁盘使用率（简化实现）。
func getDiskUsage(path string) float32 {
	// 在实际实现中应使用 syscall 获取磁盘信息
	// 这里返回估算值
	_ = path
	return 0.0
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
