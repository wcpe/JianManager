package main

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/archive"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query/vlrange"
	"github.com/wcpe/JianManager/internal/worker/logs/retention"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// managedLogClients 在 supervisor 构造成功后即固定回环地址，不依赖首次 Start 结果。
// 客户端存在不代表分区已恢复；Catalog/projection 的可用性仍由发布和查询层判定。
//
// 2026-10-29：冷热双层已废除，只剩一个受管 VL ⇒ 不再有 Hot/Cold/Rehydrate 三个客户端。
type managedLogClients struct {
	Supervisor *vlsup.Supervisor
	Main       *vlsup.Client
	Ranges     *vlrange.Tiered
}

func newManagedLogClients(supervisor *vlsup.Supervisor) (*managedLogClients, error) {
	if supervisor == nil {
		return nil, fmt.Errorf("日志 supervisor 未装配")
	}
	client, err := supervisor.ClientFor(vlsup.NamespaceHot)
	if err != nil {
		return nil, err
	}
	rangeClient, err := vlrange.New(client)
	if err != nil {
		return nil, err
	}
	out := &managedLogClients{
		Supervisor: supervisor,
		Main:       client,
		Ranges:     &vlrange.Tiered{Main: rangeClient},
	}
	out.Ranges.Ready = out.ready
	return out, nil
}

func (c *managedLogClients) ready(ctx context.Context, owner catalog.Owner) error {
	if c == nil || c.Supervisor == nil {
		return fmt.Errorf("日志受管客户端未装配")
	}
	if !owner.Valid() {
		return fmt.Errorf("日志读取 owner %s 没有受管接收端", owner)
	}
	ns := vlsup.NamespaceHot
	status, err := c.Supervisor.Status(ns)
	if err != nil {
		return err
	}
	if status.State != vlsup.StateRunning {
		return fmt.Errorf("日志 %s 进程尚未运行（%s）", ns, status.State)
	}
	probeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := c.Supervisor.Health(probeCtx, ns); err != nil {
		return fmt.Errorf("日志 %s 传输未就绪: %w", ns, err)
	}
	return nil
}

func (c *managedLogClients) client(owner catalog.Owner) (*vlsup.Client, error) {
	if c == nil || c.Main == nil {
		return nil, fmt.Errorf("日志受管客户端未装配")
	}
	if !owner.Valid() {
		return nil, fmt.Errorf("日志 owner %s 需要显式恢复路径", owner)
	}
	return c.Main, nil
}

// managedLogWriteRoute 只做物理日路由预检；真正许可由 ingest 持有到写、校验和发布结束。
func managedLogWriteRoute(cat *catalog.Catalog, clients *managedLogClients, hot *vlsup.Client) func(ingest.SourceConfig) (*vlsup.Client, bool, error) {
	return func(source ingest.SourceConfig) (*vlsup.Client, bool, error) {
		if cat == nil {
			return nil, false, fmt.Errorf("日志 Catalog 不可用，不能猜测写入层")
		}
		target, err := cat.WriteAdmission(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay})
		if err != nil {
			return nil, true, err
		}
		if clients == nil {
			if target.Owner == catalog.OwnerHot && hot != nil {
				return hot, false, nil
			}
			return nil, false, fmt.Errorf("日志 owner %s 接收端未装配", target.Owner)
		}
		if err := clients.ready(context.Background(), target.Owner); err != nil {
			return nil, false, err
		}
		client, err := clients.client(target.Owner)
		return client, false, err
	}
}

// managedLogPermitRoute 只使用已获取许可的固定目标，后续冻结不能反向取消在途提交。
func managedLogPermitRoute(clients *managedLogClients, hot *vlsup.Client) func(ingest.SourceConfig, *catalog.WritePermit) (*vlsup.Client, error) {
	return func(_ ingest.SourceConfig, permit *catalog.WritePermit) (*vlsup.Client, error) {
		if permit == nil || (permit.Owner != catalog.OwnerHot && permit.Owner != catalog.OwnerCold) {
			return nil, fmt.Errorf("日志写入缺少有效 HOT/COLD 物理日许可")
		}
		if clients == nil {
			if permit.Owner == catalog.OwnerHot && hot != nil {
				return hot, nil
			}
			return nil, fmt.Errorf("日志许可 owner %s 接收端未装配", permit.Owner)
		}
		if err := clients.ready(context.Background(), permit.Owner); err != nil {
			return nil, err
		}
		return clients.client(permit.Owner)
	}
}

func managedLogReadRoute(clients *managedLogClients, hot *vlsup.Client) func(ingest.SourceConfig, *catalog.Record) (*vlsup.Client, error) {
	return func(_ ingest.SourceConfig, record *catalog.Record) (*vlsup.Client, error) {
		if record == nil || record.PublishedProjection == nil {
			return nil, fmt.Errorf("日志没有已发布的权威投影")
		}
		if clients == nil {
			if record.Owner == catalog.OwnerHot && hot != nil {
				return hot, nil
			}
			return nil, fmt.Errorf("日志 owner %s 读取端未装配", record.Owner)
		}
		if err := clients.ready(context.Background(), record.Owner); err != nil {
			return nil, err
		}
		return clients.client(record.Owner)
	}
}

// readyRetentionMover 不修改开关；冷层停止或传输失效时保留原物，后续同一驱动可恢复。
func readyRetentionMover(mover retention.Mover, clients *managedLogClients, budget *vlsup.BudgetController) retention.Mover {
	return retention.MoverFunc(func(ctx context.Context, namespace, day string) (string, error) {
		if budget != nil {
			if err := budget.Admit(vlsup.WorkBackground); err != nil {
				return "", err
			}
		}
		for _, owner := range []catalog.Owner{catalog.OwnerHot, catalog.OwnerCold} {
			if err := clients.ready(ctx, owner); err != nil {
				return "", err
			}
		}
		return mover.MoveToCold(ctx, namespace, day)
	})
}

// readyRehydratePublisher 保留固定 publisher，动态启动或同端口重启不需要替换裸指针。
type readyRehydratePublisher struct {
	clients   *managedLogClients
	publisher archive.RehydratePublisher
}

func (p readyRehydratePublisher) Publish(ctx context.Context, key archive.PartitionKey, events []archive.ArchivedEvent, taskID string) error {
	if err := p.clients.ready(ctx, catalog.OwnerArchive); err != nil {
		return err
	}
	return p.publisher.Publish(ctx, key, events, taskID)
}

func (p readyRehydratePublisher) CanReuseRehydrate(ctx context.Context, key archive.PartitionKey, taskID string) bool {
	if err := p.clients.ready(ctx, catalog.OwnerArchive); err != nil {
		return false
	}
	if checker, ok := p.publisher.(archive.RehydrateReuseChecker); ok {
		return checker.CanReuseRehydrate(ctx, key, taskID)
	}
	return false
}

func (p readyRehydratePublisher) ReportFailure(key archive.PartitionKey, reason string) error {
	if reporter, ok := p.publisher.(archive.RehydrateFailureReporter); ok {
		return reporter.ReportFailure(key, reason)
	}
	return nil
}

// dayMigrationInputBridge 只发布已构造的采集运行时，避免迁移早于 canonical 存储恢复。
type dayMigrationInputBridge struct {
	manager atomic.Pointer[ingest.Manager]
}

func (b *dayMigrationInputBridge) input(record *catalog.Record) ([]logtypes.Event, error) {
	manager := b.manager.Load()
	if manager == nil {
		return nil, fmt.Errorf("日志迁移 canonical 输入尚未恢复，稍后重试")
	}
	return manager.CanonicalDayMigrationInput(record)
}

// logStreamFieldsState 在启动时输出一次 VL 流标签的生效形态。
//
// 为什么需要这条日志（而不是“让运维自己 curl 看”）：
//   - 流是**分区键**，启用它会在 VL 里留下永久的 `_stream` 形态；
//   - 切换点**之前**写入的历史数据 `_stream` 是空串，VL 不会自动迁移（只追加）；
//   - 于是同一个 VL 里会同时存在两类流，按 `_stream` 分组的查询在切换点会看到断崖。
//
// 运维将来一定会问「为什么查询里有的日志没有 stream 标签？」——不给这条线索，
// 他们只会把它当故障排查（“流标签没生效？”），而实际上那是历史数据。
// 它只在启动时打一次，不在采集热路径上，不存在噪声问题。
//
// disabled 为真时打一条对照日志，让「为什么没有流标签」的另一种答案也留在日志里：
// 那是配置显式关掉的，不是没接上。
func logStreamFieldsState(disabled bool, override string) {
	if disabled {
		slog.Info("VL 流标签已关闭（log_vl.disable_stream_fields）；写入请求不带任何 query 参数，所有日志落在同一个 stream",
			"stream_fields", "",
			"hint", "新写入与历史数据的分区形态一致，但查询无法按源/通道收敛；如需开启请移除 disable_stream_fields")
		return
	}
	fields := strings.TrimSpace(override)
	if fields == "" {
		fields = vlsup.DefaultStreamFields
	}
	slog.Info("VL 流标签已启用；本次启动之前写入的日志 _stream 为空，不会自动迁移",
		"stream_fields", fields,
		"hint", "按 _stream 分组的查询在切换点会看到两类；旧数据随 retention 自然淘汰")
}

// logReapedVLOrphan 仅输出归属核对所需元数据，绝不接收原始命令行或鉴权配置。
func logReapedVLOrphan(orphan vlsup.OrphanMatch) {
	slog.Warn("已回收本节点 vlsup 孤儿（启前）", "pid", orphan.PID, "namespace", orphan.Namespace, "exe", orphan.Exe)
}

// logDiskBytes 有界统计日志根；缺失根是已知零，读错或预算耗尽必须保留 unknown。
func logDiskBytes(roots []string, maxEntries int, maxDuration time.Duration) (int64, bool) {
	deadline := time.Now().Add(maxDuration)
	var total int64
	visited := 0
	var normalized, unique []string
	for _, root := range roots {
		absolute, err := filepath.Abs(root)
		if err != nil {
			return 0, false
		}
		if real, err := filepath.EvalSymlinks(absolute); err == nil {
			absolute = real
		} else if !os.IsNotExist(err) {
			return 0, false
		}
		normalized = append(normalized, absolute)
	}
	// 父目录先统计，避免配置把子根放在前面时重复计量同一批字节。
	sort.Slice(normalized, func(i, j int) bool { return len(normalized[i]) < len(normalized[j]) })
	for _, absolute := range normalized {
		contained := false
		for _, previous := range unique {
			relative, err := filepath.Rel(previous, absolute)
			if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				contained = true
				break
			}
		}
		if contained {
			continue
		}
		unique = append(unique, absolute)
		if err := filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				if path == absolute && os.IsNotExist(err) {
					return nil
				}
				return err
			}
			visited++
			if visited > maxEntries || time.Now().After(deadline) {
				return fmt.Errorf("日志目录采样超出有限预算")
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("日志目录含未纳入采样根的符号链接")
			}
			if entry.Type().IsRegular() {
				info, err := entry.Info()
				if err != nil {
					return err
				}
				if info.Size() < 0 || info.Size() > int64(^uint64(0)>>1)-total {
					return fmt.Errorf("日志目录大小溢出")
				}
				total += info.Size()
			}
			return nil
		}); err != nil {
			return 0, false
		}
	}
	return total, true
}

func sampleLogResources(supervisor *vlsup.Supervisor, roots []string, externalHot bool) vlsup.ResourceSample {
	sample := vlsup.ResourceSample{Namespaces: map[vlsup.Namespace]vlsup.NamespaceSample{}}
	if supervisor != nil {
		sample = supervisor.SampleResources(os.Getpid())
	} else {
		for _, ns := range []vlsup.Namespace{vlsup.NamespaceHot} {
			sample.Namespaces[ns] = vlsup.NamespaceSample{RSSKnown: true}
		}
		if externalHot {
			// 外部回环实例没有受管 PID 证据，不能伪装成未运行的已知零。
			sample.Namespaces[vlsup.NamespaceHot] = vlsup.NamespaceSample{}
		}
		if rss, err := vlsup.SampleProcessRSSBytes(os.Getpid()); err == nil {
			sample.WorkerRSSBytes, sample.WorkerRSSKnown = rss, true
		}
	}
	sample.DiskUsedBytes, sample.DiskBytesKnown = logDiskBytes(roots, 100000, 100*time.Millisecond)
	sample.DiskKnown = true
	sample.DiskUsagePercent = 0
	for _, root := range roots {
		path := root
		for {
			if _, err := os.Stat(path); err == nil {
				break
			} else if !os.IsNotExist(err) || filepath.Dir(path) == path {
				sample.DiskKnown = false
				break
			}
			path = filepath.Dir(path)
		}
		percent, err := vlsup.SampleDiskUsagePercent(path)
		if err != nil {
			sample.DiskKnown = false
		} else if percent > sample.DiskUsagePercent {
			sample.DiskUsagePercent = percent
		}
	}
	return sample
}
