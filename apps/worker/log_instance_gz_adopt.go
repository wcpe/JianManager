package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/archive"
	"github.com/wcpe/JianManager/internal/worker/process"
)

// 本文件是 ADR-098 §5「实例本地 logs/*.log.gz 归档吸存」的生产接线。
//
// 规格原文（那四条不是建议，是约束）：
//  1. 以**文件身份**、**原样（压缩态）**吸存到冷层；
//  2. **只归档文件，不解析、不入事件流**——它不是 canonical 事件的第二份副本，而是原始证据；
//  3. **幂等去重由系统负责**（同一文件重复吸存不产生第二份）；去重判据不得依赖
//     「最后一次吸存时间」这类可变量；
//  4. **本地 gz 一律不动**：不在 Worker 侧删除或搬走实例本地的 .gz。
//
// 为什么第 4 条关键：这些 .gz 是轮转后的原始证据，也是用户明确要求保留的最后备份。
// 任何「吸存成功后删本地」的优化都越过了本 ADR 的边界，即便它看上去很省空间。
//
// 去重靠 `Registry.RegisterRaw` 自带的 object_id（content sha256 + size）幂等：
// 同一份字节第二次登记会命中 `AlreadyRegistered` 而不写第二份对象。
// 这条判据只用「内容 + 大小」，**不碰时间**，满足规格要求。

// instanceGzAdopter 把实例日志目录里的 .gz 原样吸存到项目归档。
type instanceGzAdopter struct {
	registry *archive.Registry
	// instanceDirs 返回「实例 UUID → work_dir」的快照；每轮重新拉，以便容纳新实例。
	instanceDirs func() map[string]string
	// now 可注入，便于测试固定日期。
	now func() time.Time
}

// adoptStats 是一轮的读数。
//
type adoptStats struct {
	// Scanned 是候选 .gz 文件数（已去重前）。
	Scanned int
	// Registered 是首次登记成功的文件数。
	Registered int
	// AlreadyRegistered 是命中幂等去重的文件数。
	AlreadyRegistered int
	// Failed 是登记失败的文件数（不阻断其它文件）。
	Failed int
	// LocalDeleted 永远为 0——见文件头第 4 条约束；这个字段存在只是为了把
	// 「平台不删实例本地 gz」变成可观测事实，能直接从读数里读出。
	LocalDeleted int
}

// AdoptOnce 扫一轮所有实例的 logs/*.log.gz 并吸存。
//
// 「分区键」取 (storage_namespace=inst:<uuid>, UTCDay=文件 mtime 的 UTC 日期, generation=1)：
// 用 mtime 而不是文件名里的日期，是因为文件名格式由各服务端的轮转策略决定（Paper/Bungee/
// 自定义都可能不同），从文件名解析日期是不可靠的；mtime 则是文件系统给的、对所有服务端一致。
// 代价是「文件被 touch 过就会落到另一天」，但那只会导致**多一份副本**（object_id 仍能去重），
// 不会丢数据——方向是安全的。
func (a *instanceGzAdopter) AdoptOnce(ctx context.Context) (adoptStats, error) {
	var stats adoptStats
	if a == nil || a.registry == nil || a.instanceDirs == nil {
		return stats, nil
	}
	dirs := a.instanceDirs()
	uuids := make([]string, 0, len(dirs))
	for id := range dirs {
		uuids = append(uuids, id)
	}
	sort.Strings(uuids) // 稳定顺序，便于复现与日志比对

	var firstErr error
	for _, uuid := range uuids {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		workDir := strings.TrimSpace(dirs[uuid])
		if workDir == "" {
			continue
		}
		logsDir := filepath.Join(workDir, "logs")
		entries, err := os.ReadDir(logsDir)
		if err != nil {
			// 目录不存在是常态（实例可能还没轮转过），不当错误；其它错误记账但不阻断。
			if !os.IsNotExist(err) {
				stats.Failed++
				if firstErr == nil {
					firstErr = fmt.Errorf("读 %s 失败: %w", logsDir, err)
				}
			}
			continue
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return stats, err
			}
			if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".gz") {
				continue
			}
			stats.Scanned++
			stats2, err := a.adoptFile(ctx, uuid, logsDir, entry.Name())
			if stats2 {
				stats.Registered++
			} else if err == nil {
				stats.AlreadyRegistered++
			}
			if err != nil {
				stats.Failed++
				if firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return stats, firstErr
}

// adoptFile 吸存单个文件。返回 registered=true 表示首次登记。
func (a *instanceGzAdopter) adoptFile(ctx context.Context, uuid, logsDir, name string) (bool, error) {
	path := filepath.Join(logsDir, name)
	info, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("stat %s 失败: %w", path, err)
	}
	mtime := info.ModTime().UTC()
	day := mtime.Format("2006-01-02")
	key := archive.PartitionKey{
		StorageNamespace: "inst:" + uuid,
		UTCDay:           day,
		Generation:       1,
	}
	// RelPath 保持文件名本身：这样「同一文件名换了日期目录」仍然撞 object_id 去重，
	// 而人看归档目录时能直接认出这是哪个文件。
	result, err := a.registry.RegisterRaw(ctx, key, archive.RawSource{
		SourcePath:  path,
		Origin:      archive.OriginInstanceGz,
		RelPath:     "raw/instance-gz/" + name,
		LogSourceID: "inst:" + uuid + "/file",
	})
	if err != nil {
		return false, fmt.Errorf("吸存 %s 失败: %w", path, err)
	}
	// 文件保持不动（ADR-098 §5 第 4 条）。这里显式不调用任何删除/移动。
	return result != nil && !result.AlreadyRegistered, nil
}

// newInstanceGzAdopter 构造吸存器。
//
// manager 可以为 nil（例如单元测试或未启用实例管理）；此时 instanceDirs 返回空，
// 一轮会扫到 0 个文件而不是 panic。
func newInstanceGzAdopter(registry *archive.Registry, manager *process.Manager) *instanceGzAdopter {
	if registry == nil {
		return nil
	}
	return &instanceGzAdopter{
		registry: registry,
		instanceDirs: func() map[string]string {
			out := make(map[string]string)
			if manager == nil {
				return out
			}
			for _, uuid := range manager.ListInstances() {
				// ListInstances 只返回 UUID（它只遍历 map 的键）；WorkDir 要逐个经
				// GetInstance 取——不能在循环里直接读内部 map（未导出且需持锁）。
				inst, ok := manager.GetInstance(uuid)
				if !ok || inst == nil {
					continue
				}
				if strings.TrimSpace(inst.WorkDir) == "" {
					continue
				}
				out[uuid] = inst.WorkDir
			}
			return out
		},
		now: func() time.Time { return time.Now().UTC() },
	}
}

// logAdoptStats 把一轮读数打成一行，便于现场对照「扫了多少 / 存了多少 / 跳了多少」。
func logAdoptStats(stats adoptStats, elapsed time.Duration, err error) {
	fields := []any{
		"scanned", stats.Scanned,
		"registered", stats.Registered,
		"alreadyRegistered", stats.AlreadyRegistered,
		"failed", stats.Failed,
		// 恒为 0：把「平台不删实例本地 gz」读出来，而不是只写在注释里。
		"localDeleted", stats.LocalDeleted,
		"elapsedMs", elapsed.Milliseconds(),
	}
	if err != nil {
		fields = append(fields, "error", err)
		slog.Warn("实例 gz 吸存本轮有失败（不阻断，下轮重试）", fields...)
		return
	}
	if stats.Scanned == 0 {
		return // 一轮没事可做就不刷日志
	}
	slog.Info("实例 gz 吸存完成", fields...)
}
