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
	"github.com/wcpe/JianManager/internal/worker/logs/lifecycle"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/query/vlrange"
	"github.com/wcpe/JianManager/internal/worker/logs/retention"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// managedLogClients 在 supervisor 构造成功后即固定全部回环地址，不依赖首次 Start 结果。
// 客户端存在不代表分区已恢复；Catalog/projection 的可用性仍由发布和查询层判定。
type managedLogClients struct {
	Supervisor *vlsup.Supervisor
	Hot        *vlsup.Client
	Cold       *vlsup.Client
	Rehydrate  *vlsup.Client
	Ranges     *vlrange.Tiered
}

func newManagedLogClients(supervisor *vlsup.Supervisor) (*managedLogClients, error) {
	if supervisor == nil {
		return nil, fmt.Errorf("日志 supervisor 未装配")
	}
	out := &managedLogClients{Supervisor: supervisor, Ranges: &vlrange.Tiered{}}
	for _, ns := range []vlsup.Namespace{vlsup.NamespaceHot, vlsup.NamespaceCold, vlsup.NamespaceRehydrate} {
		client, err := supervisor.ClientFor(ns)
		if err != nil {
			return nil, err
		}
		rangeClient, err := vlrange.New(client)
		if err != nil {
			return nil, err
		}
		switch ns {
		case vlsup.NamespaceHot:
			out.Hot, out.Ranges.Hot = client, rangeClient
		case vlsup.NamespaceCold:
			out.Cold, out.Ranges.Cold = client, rangeClient
		case vlsup.NamespaceRehydrate:
			out.Rehydrate, out.Ranges.Rehydrate = client, rangeClient
		}
	}
	out.Ranges.Ready = out.ready
	return out, nil
}

func (c *managedLogClients) ready(ctx context.Context, owner catalog.Owner) error {
	if c == nil || c.Supervisor == nil {
		return fmt.Errorf("日志受管客户端未装配")
	}
	var ns vlsup.Namespace
	switch owner {
	case catalog.OwnerHot:
		ns = vlsup.NamespaceHot
	case catalog.OwnerCold:
		ns = vlsup.NamespaceCold
	case catalog.OwnerArchive:
		ns = vlsup.NamespaceRehydrate
	default:
		return fmt.Errorf("日志读取 owner %s 没有受管接收端", owner)
	}
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
	if c == nil {
		return nil, fmt.Errorf("日志受管客户端未装配")
	}
	switch owner {
	case catalog.OwnerHot:
		return c.Hot, nil
	case catalog.OwnerCold:
		return c.Cold, nil
	case catalog.OwnerArchive:
		return c.Rehydrate, nil
	default:
		return nil, fmt.Errorf("日志 owner %s 需要显式恢复路径", owner)
	}
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

// logPhysicalDayRecovery 只续跑已持久化的未完成日，不创建新迁移或改变保留策略开关。
// 单协程每轮最多四日，失败日也推进轮转游标，避免一个旧故障饿死其余恢复责任。
type logPhysicalDayRecovery struct {
	cat    *catalog.Catalog
	resume func(string) error
	ready  func(context.Context) error
	budget interface{ Admit(vlsup.WorkClass) error }
	after  string
}

func newLogPhysicalDayRecovery(manager *lifecycle.DayManager, cat *catalog.Catalog, clients *managedLogClients, budget *vlsup.BudgetController) *logPhysicalDayRecovery {
	if manager == nil || cat == nil {
		return nil
	}
	return &logPhysicalDayRecovery{cat: cat, resume: manager.Resume, budget: budget,
		ready: func(ctx context.Context) error {
			for _, owner := range []catalog.Owner{catalog.OwnerHot, catalog.OwnerCold} {
				if err := clients.ready(ctx, owner); err != nil {
					return err
				}
			}
			return nil
		}}
}

func (recovery *logPhysicalDayRecovery) step(ctx context.Context) {
	if recovery == nil || ctx.Err() != nil {
		return
	}
	ordered := recovery.cat.PendingPhysicalDays()
	if len(ordered) == 0 {
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	if err := recovery.budget.Admit(vlsup.WorkBackground); err != nil {
		slog.Warn("日志未完成迁移恢复等待资源预算", "error", err)
		return
	}
	if err := recovery.ready(ctx); err != nil {
		slog.Warn("日志未完成迁移恢复等待接收端就绪", "error", err)
		return
	}
	start := sort.SearchStrings(ordered, recovery.after)
	for start < len(ordered) && ordered[start] <= recovery.after {
		start++
	}
	for offset := 0; offset < len(ordered) && offset < 4; offset++ {
		if ctx.Err() != nil || time.Now().After(deadline) {
			return
		}
		if err := recovery.budget.Admit(vlsup.WorkBackground); err != nil {
			return
		}
		day := ordered[(start+offset)%len(ordered)]
		recovery.after = day
		if err := recovery.resume(day); err != nil {
			slog.Warn("日志物理日迁移恢复未完成，保留原物待重试", "day", day, "error", err)
		}
	}
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
		for _, ns := range []vlsup.Namespace{vlsup.NamespaceHot, vlsup.NamespaceCold, vlsup.NamespaceRehydrate} {
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
