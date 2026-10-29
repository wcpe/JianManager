// Package logmetrics 采集日志采集平台的自身运行指标。
//
// ## 为什么单独成包，而不是直接塞进心跳
//
// 这里的多数读数要遍历全部采集源、stat 若干个文件、读 SQLite 采样环——都是**潜在的重活**。
// 心跳是 CP 判断节点与实例存活的依据，绝不能被观测拖慢（与 hostobs 同一取舍）。
// 因此形态固定为：**独立 goroutine 生产、心跳只读快照**。
//
// ## 缺失值的约定（本包最重要的语义）
//
// 拿不到读数时**绝不返回 0**：0 在运维上是「积压为零、一切正常」，而「拿不到」是
// 「不知道」——两者要做的处置完全相反。快照用 Valid / Available 表达可用性，
// 心跳侧据此**整组不上报**，CP 侧落 null 断点而不是落 0 值曲线。
//
// 恒为 0 的假指标比不实现更糟：它会让告警规则永远静默，而运维会误以为已有监控。
package logmetrics

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/ingest"
)

// Snapshot 是一次采集的完整读数。
//
// 每个字段都按「真实可采集」填充；采集不到的项目由 Available/Valid 统一表达，
// 不在这里逐字段编造零值。
type Snapshot struct {
	// Available 表示采集面本身可观测（采集运行时已装配且未处于起步态）。
	// false 时其余字段全部无意义，调用方不得上报。
	Available bool

	// StateFileSizeBytes 采集索引（含旧 state 文件迁移归档）在磁盘上的占用字节。
	StateFileSizeBytes int64
	// StateFileMeasured 表示 StateFileSizeBytes 是真实测量值（false=没测到，不是 0 字节）。
	StateFileMeasured bool
	// StateFileGrowthBytesPerSec 上述文件在采样窗口内的增速（字节/秒，可为负）。
	StateFileGrowthBytesPerSec float64
	// StateFileGrowthMeasured 表示增速有效（false=首拍尚无基线，或尺寸本身没测到）。
	StateFileGrowthMeasured bool
	// PersistWriteBytesPerSec 采集索引持久化的写入吞吐（字节/秒）。
	PersistWriteBytesPerSec float64

	// WALBacklogEntries 单源 WAL 最大积压条数（跨源取最大值，非求和）。
	WALBacklogEntries int64
	// WALPausedSeconds 上述积压最重源处于采集暂停状态的持续秒数（0=未暂停）。
	WALPausedSeconds int64

	// DeliveryFailureRate 投递失败率（0..100 百分比）。
	DeliveryFailureRate float64
	// DeliveryFailureClass 投递失败的主要分类（空=窗口内无失败）。
	DeliveryFailureClass string
	// DeliveryBatches 窗口内的投递批次总数（用于判断失败率的分母是否有效）。
	DeliveryBatches int64

	// VerifyBackoffCount 投影校验进入退避的累计次数（进程内单调递增）。
	VerifyBackoffCount int64
	// SlowSQLCount 慢查询累计次数（单次投影校验查询 ≥ 500ms）。
	SlowSQLCount int64
	// SlowSQLMaxMs 最慢单次查询耗时（毫秒）。
	SlowSQLMaxMs int64

	SampledAt time.Time
	// Error 是本次采集中途遇到的问题（部分字段可能仍有效），仅供日志排障。
	Error string
}

// Source 是本包对采集运行时的最小依赖面（便于测试注入，不绑定 *ingest.Manager）。
type Source interface {
	// LogMetricsSnapshot 返回采集面的原始读数。
	LogMetricsSnapshot() ingest.LogMetricsSnapshot
}

// Reader 只暴露已发布的快照（与 hostobs.Reader 同款：消费方不触发采集）。
type Reader interface {
	Snapshot() Snapshot
}

// Collector 周期采集日志平台指标。
type Collector struct {
	interval time.Duration
	source   func() (Source, error)
	// root 是 Worker 数据根目录，用于定位索引与旧 state 文件。
	root string
	// statFile 允许测试注入（CI 上未必能造出真实大文件）。
	statFile func(name string) (int64, error)
	now      func() time.Time

	mu   sync.RWMutex
	snap Snapshot

	// 上一拍的累计值，用于差分推导速率（与 CP 侧 node_net_*_rate 同口径）。
	prevSizeBytes int64
	prevAt        time.Time
	prevValid     bool

	prevPersistBytes int64
	prevPersistAt    time.Time
	prevPersistSet   bool
}

// Options 采集器配置（可缺省）。
type Options struct {
	// Interval 采集间隔；≤0 取默认 30s（与心跳节拍对齐）。
	// 更密没有意义——读数每 30s 才被消费一次。
	Interval time.Duration
	// Root Worker 数据根目录（用于定位 var/log 下的索引与旧 state 文件）。
	Root string
}

const defaultInterval = 30 * time.Second

// New 构造采集器。source 为 nil 时快照恒为 Available=false
// （表示「本节点没有采集面」，不是「采集面健康读数为 0」）。
func New(opts Options, source func() (Source, error)) *Collector {
	interval := opts.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	return &Collector{
		interval: interval,
		source:   source,
		root:     opts.Root,
		statFile: func(name string) (int64, error) {
			info, err := os.Stat(name)
			if err != nil {
				return 0, err
			}
			return info.Size(), nil
		},
		now: func() time.Time { return time.Now() },
	}
}

// Snapshot 返回最近一次采集结果（心跳消费入口）。
func (c *Collector) Snapshot() Snapshot {
	if c == nil {
		return Snapshot{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snap
}

// Start 启动周期采集（首拍立即采集，不等第一个 tick）。
//
// 立即采一拍的理由：Worker 启动后 CP 就该看到读数；等 30s 才出现会让
// 「刚重启完到底健不健康」在这段时间内无从判断。
func (c *Collector) Start(ctx context.Context) {
	if c == nil {
		return
	}
	go func() {
		c.sampleOnce()
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.sampleOnce()
			}
		}
	}()
}

// sampleOnce 采一拍并发布快照。
func (c *Collector) sampleOnce() {
	snap := Snapshot{SampledAt: c.now()}
	now := snap.SampledAt

	src, err := c.sourceOrNil()
	if err != nil || src == nil {
		// 采集运行时未装配：Available=false，其余字段留零但**不被上报**。
		if err != nil {
			snap.Error = err.Error()
		}
		c.publish(snap)
		return
	}

	raw := src.LogMetricsSnapshot()
	snap.Available = raw.Available
	if !raw.Available {
		c.publish(snap)
		return
	}

	// 1) 索引/state 文件尺寸：真实 stat。
	//
	// 拿不到（数据根未配置 / 一个文件都不存在）时**不填**，由 StateFileMeasured 表达：
	// 上位据此不上报尺寸与增速，而不是上报 0。上报 0 会在面板上画出「索引被清空了」
	// 这条虚假好转曲线，恰好掩盖「采集面根本没起来」这一真实故障。
	size, sizeOK := c.measureStateFiles()
	snap.StateFileMeasured = sizeOK
	if sizeOK {
		snap.StateFileSizeBytes = size
		// 增速须有上一拍才能差分；首拍只建立基线（速率保持 0 且不参与判定）。
		if c.prevValid && c.prevAt.Before(now) {
			dt := now.Sub(c.prevAt).Seconds()
			if dt > 0 {
				snap.StateFileGrowthBytesPerSec = float64(size-c.prevSizeBytes) / dt
				snap.StateFileGrowthMeasured = true
			}
		}
		c.prevSizeBytes = size
		c.prevAt = now
		c.prevValid = true
	}

	// 2) 持久化写入吞吐：由累计写入字节差分推导（口径与网络速率一致）。
	if c.prevPersistSet {
		dt := now.Sub(c.prevPersistAt).Seconds()
		if dt > 0 {
			delta := raw.PersistBytesTotal - c.prevPersistBytes
			if delta >= 0 {
				snap.PersistWriteBytesPerSec = float64(delta) / dt
			}
			// delta < 0 表示计数器重置（索引重建）：本拍不出速率，仅重建基线。
		}
	}
	c.prevPersistBytes = raw.PersistBytesTotal
	c.prevPersistAt = now
	c.prevPersistSet = true

	// 3) 单源 WAL 最大积压与暂停时长。
	snap.WALBacklogEntries = raw.WALMaxBacklogEntries
	snap.WALPausedSeconds = raw.WALMaxPausedSeconds

	// 4) 投递失败率与主要分类（按窗口聚合，窗口由采样侧维护）。
	snap.DeliveryBatches = raw.DeliveryBatches
	snap.DeliveryFailureRate = failureRate(raw.DeliveryFailures, raw.DeliveryBatches)
	snap.DeliveryFailureClass = raw.DominantFailureClass

	// 5) 校验退避与慢查询（累计值，单调递增）。
	snap.VerifyBackoffCount = raw.VerifyBackoffCount
	snap.SlowSQLCount = raw.SlowSQLCount
	snap.SlowSQLMaxMs = raw.SlowSQLMaxMs

	c.publish(snap)
}

// sourceOrNil 取采集面；未装配时返回 nil（不是错误）。
//
// 注意 Go 的「非 nil 接口装 nil 指针」陷阱：`var m *ingest.Manager; return m, nil`
// 返回的接口**非 nil**，直接调用其方法会空指针崩溃。生产里 main 注入的是
// `func() (Source, error) { return manager, nil }`，而 manager 在创建失败的分支里
// 正是 nil——这条路径必须显式挡掉，否则采集器一启动就 panic（观测模块打死 Worker）。
func (c *Collector) sourceOrNil() (Source, error) {
	if c.source == nil {
		return nil, nil
	}
	src, err := c.source()
	if err != nil || src == nil {
		return nil, err
	}
	if isNilSource(src) {
		return nil, nil
	}
	return src, nil
}

// measureStateFiles 汇总采集索引相关文件的占用字节。
//
// 口径刻意包含旧 ingest.state.json 及其 .migrated 归档：事故形态里增长的正是这个面，
// 迁移完成后它们仍留在盘上（供回滚与排障），若不计入就会出现「文件已 1.2 GB
// 但平台显示 0」的观测盲区。
func (c *Collector) measureStateFiles() (int64, bool) {
	if c.root == "" {
		return 0, false
	}
	dir := filepath.Join(c.root, "var", "log")
	names := []string{
		"ingest.index.db",
		"ingest.index.db-wal",
		"ingest.index.db-shm",
		"ingest.state.json",
	}
	var total int64
	seenAny := false
	for _, name := range names {
		if size, err := c.statFile(filepath.Join(dir, name)); err == nil {
			total += size
			seenAny = true
		}
	}
	// 旧 state 的一次性迁移归档（ingest.state.json.migrated 或带时间戳后缀）：
	// 目录列举失败不影响其余读数，只是少算归档体积。
	if entries, err := os.ReadDir(dir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if hasPrefix(entry.Name(), "ingest.state.json.migrated") {
				if info, ierr := entry.Info(); ierr == nil {
					total += info.Size()
					seenAny = true
				}
			}
		}
	}
	return total, seenAny
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// failureRate 计算失败率百分比；分母无效时返回 0（由 DeliveryBatches 区分「无活动」）。
func failureRate(failures, batches int64) float64 {
	if batches <= 0 {
		return 0
	}
	if failures < 0 {
		failures = 0
	}
	if failures > batches {
		failures = batches
	}
	return float64(failures) / float64(batches) * 100
}

func (c *Collector) publish(snap Snapshot) {
	c.mu.Lock()
	c.snap = snap
	c.mu.Unlock()
}

// isNilSource 判断接口背后是否为 nil 指针。
//
// 为什么需要它：Go 里把 nil 具体指针装进接口后，`src == nil` 为 false，
// 随后调用其方法即空指针崩溃。生产注入点传的正是可能为 nil 的 manager，
// 这条路径必须在调用任何方法之前挡掉——观测模块把 Worker 打死是最坏的故障形态。
func isNilSource(src Source) bool {
	value := reflect.ValueOf(src)
	switch value.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return value.IsNil()
	}
	return false
}
