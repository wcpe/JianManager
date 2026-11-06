package main

// 本文件放「与采集器无关」的日志生命周期驱动器启动。
//
// # 为何要抽出来
//
// 这些驱动器（保留策略、归档对象回收、实例 gz 吸存）原本写在**旧采集链路的
// 装配块内**。而它们与“用哪个采集器读文件”完全无关——只依赖 Catalog / archive /
// retention，那三者在两条链路下都在。
//
// 如果不抽出来，删旧采集块时它们会跟着一起消失（数据到期不淘汰、归档副本不回收）。
// 抽成函数后新旧两条路径共用，删除时只需改调用位置。

import (
	"context"
	"log/slog"

	workercfg "github.com/wcpe/JianManager/internal/worker"
	"github.com/wcpe/JianManager/internal/worker/logs/archive"
	"github.com/wcpe/JianManager/internal/worker/logs/retention"
	"github.com/wcpe/JianManager/internal/worker/process"
)

// logLifecycle 是启动日志生命周期驱动器所需的依赖。
//
// 用结构体传参而不是一长串形参：这些字段来源分散（配置、装配期构造、条件构造），
// 位置参数很容易传错位。
type logLifecycle struct {
	retentionDriver *retention.Driver
	archivePurge    *retention.ArchiveObjectReclaimer
	archiveRegistry *archive.Registry
	procManager     *process.Manager
	adoptCfg        workercfg.LogArchiveInstanceGzAdoptConfig
}

// runLogLifecycle 在给定 ctx 下启动所有生命周期驱动器。
//
// # 为何共用采集器的 ctx
//
// 它们与采集共用同一生命周期：Worker 退出时全部停止。另起 ctx 会让停机时序
// 更难推理（部分驱动器还在跑时采集已停）。
func (l logLifecycle) run(ctx context.Context) {
	// 保留策略驱动器：首轮立即执行（启动时正是「停机期间攒下的过期分区」最需要处理的时刻），
	// 之后每天在 log_retention.trigger.sweep_at_hour 指定的本地整点跑一轮（默认凌晨 3 点）。
	if l.retentionDriver != nil {
		go l.retentionDriver.Run(ctx)
		slog.Info("日志冷热分层驱动器已启动", "schedule", l.retentionDriver.DescribeSchedule())
	}
	// 归档对象回收：同一 ctx、同一「首轮立即执行 + 每日固定时刻」口径。
	// 候选只会是「已归档 + 热层已回收 + 已过期 + 三闸全开」的分区，代价有界。
	if l.archivePurge != nil {
		purgeDriver := retention.NewArchivePurgeDriver(l.archivePurge)
		go purgeDriver.Run(ctx)
		slog.Info("归档对象回收驱动器已启动", "schedule", purgeDriver.DescribeSchedule())
	}
	// 实例本地 gz 吸存（ADR-098 §5）：把各实例 logs/*.log.gz 以**文件身份原样**
	// 吸存到归档（与 Deep Archive 同一个 provider）。
	//
	// 三条边界（写在这里以免日后被当成优化点）：
	//   - 只归档文件，不下探解析、不入事件流（它不是 canonical 事件的副本，是原始证据）；
	//   - **不删不搬实例本地 .gz**（用户明确要留作最后备份）；
	//   - 幂等靠 object_id（内容 sha256 + 大小），重跑不产生第二份。
	if l.archiveRegistry == nil {
		slog.Info("实例 gz 吸存未装配：本次没有可用的归档目的地（log_archive）")
		return
	}
	logAdoptSchedule(l.adoptCfg.Enabled, l.adoptCfg.SweepAtHour)
	if !l.adoptCfg.Enabled {
		return
	}
	adopter := newInstanceGzAdopter(l.archiveRegistry, l.procManager)
	if adopter == nil {
		return
	}
	go runInstanceGzAdopt(ctx, adopter, l.adoptCfg.SweepAtHour)
}
