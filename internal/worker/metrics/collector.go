package metrics

import (
	"context"
	"log/slog"
	"math"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

// NodeMetrics 节点指标。
type NodeMetrics struct {
	CPUUsage float32
	// IOWait 是 CPU 处于 IO 等待的占比（0..1）。
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
	interval  time.Duration
	stopCh    chan struct{}
	cpuReader CPUReader
	startOnce sync.Once
	stopOnce  sync.Once
}

// NewCollector 创建指标采集器；可选 reader 由 Worker 主进程注入唯一 owner。
// 未注入时 CPU 标量以零降级，不能隐式另建采样者或使用累计历史平均。
func NewCollector(interval time.Duration, readers ...CPUReader) *Collector {
	if interval <= 0 {
		interval = time.Second
	}
	c := &Collector{interval: interval, stopCh: make(chan struct{})}
	if len(readers) > 0 {
		c.cpuReader = readers[0]
	}
	return c
}

// IOWaitRatio 归一化窗口增量为 IO 等待占比（0..1），不得传入开机累计计数。
func IOWaitRatio(t cpu.TimesStat) float32 {
	total := cpuTotal(t)
	if !validCPUTimes(t) || total <= 0 || math.IsNaN(total) || math.IsInf(total, 0) {
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

	// CPU 只读唯一 owner 的同窗快照；采集器及 RPC 不推进累计计数基线。
	if c.cpuReader != nil {
		snapshot := c.cpuReader.Snapshot()
		if snapshot.Valid {
			metrics.CPUUsage, metrics.IOWait = snapshot.CPUUsage, snapshot.IOWait
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
	c.startOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(c.interval)
			defer ticker.Stop()
			for {
				select {
				case <-c.stopCh:
					return
				case <-ticker.C:
					m := c.Collect()
					if callback != nil {
						callback(m)
					}
				}
			}
		}()
		slog.Info("指标采集器已启动", "interval", c.interval)
	})
}

// Stop 停止周期性采集。
func (c *Collector) Stop() {
	c.stopOnce.Do(func() {
		close(c.stopCh)
		slog.Info("指标采集器已停止")
	})
}
