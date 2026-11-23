package main

// 本文件提供「磁盘使用率」读数。
//
// # 为何单独一个文件
//
// 这个读数原本由 `ingest.DiskCapacityProvider` 提供（旧采集链路）。
// 删旧链路时发现：保留策略的磁盘触发只需要一个数字——「根目录用了百分之几」。
// 旧实现返回一整个 `acquire.CapacityBudget`（含 WAL 上限、降级/暂停阈值、
// 缺口上限……），那些对新采集器**全部无意义**（新架构没有 WAL、没有缺口记账）。
//
// 所以改成只回答那一个数字。保留策略的接口也相应收窄。

import (
	"github.com/shirou/gopsutil/v4/disk"
)

// newDiskUsageProvider 返回给定根所在文件系统的使用率（百分比）。
//
// # 为何要看「根所在文件系统」而不是硬编码路径
//
// 生产数据根可能在独立挂载点（如 `/home` 单独分区）。按数据根所在的文件系统算，
// 才能反映「日志真的会把哪块盘填满」。
//
// # 失败时的语义
//
// 返回 error 而不是 0：读不到磁盘水位时，保留策略应当**不因磁盘触发**（按年龄走），
// 而不是把 0 当成“盘很空”或 100 当成“盘满了”去做出错误的搬运决策。
func newDiskUsageProvider(root string) func() (float64, error) {
	return func() (float64, error) {
		usage, err := disk.Usage(root)
		if err != nil {
			return 0, err
		}
		return usage.UsedPercent, nil
	}
}
