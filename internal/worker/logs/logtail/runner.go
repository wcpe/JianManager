package logtail

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/tailer"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// Config 是新采集器的装配配置。
type Config struct {
	// Enabled 是总开关。false 时 Runner 不会被创建（走旧路径）。
	Enabled bool

	// Paths 是**额外的**采集 glob（通常留空，由绑定表推导）。
	//
	// # 为何通常不用配
	//
	// 采集路径由**已绑定实例的 WorkDir** 每轮现算（见 Bindings.PathsProvider）。
	// 这样新建实例的日志无需重启就能被采到，也不会因为“猜错一个 serversRoot”
	// 而静默采不到任何东西（真机实测：生产实例在 /home/wxys233/servers，
	// 而 dataRoot/var/servers 里 0 个实例目录）。
	//
	// 只有需要采**非实例**日志（节点自身日志等）时才需要在这里显式给出。
	Paths []string

	// Exclude 是排除规则。默认含 .gz（用户明确要求不采归档）。
	Exclude []string

	// ReadFromHead 决定首次发现的文件从哪里开始读。
	//
	// 生产取 true：能补上部署前的历史日志（用户需要这个）。
	ReadFromHead bool

	// RegistryPath 是位点表路径。
	//
	// 相对路径相对 Worker 数据根；建议 `var/log/tailer.registry.jsonl`。
	RegistryPath string

	// ScanInterval / ProgressInterval 留空时用 tailer 的默认值（10s / 2s）。
	ScanInterval     time.Duration
	ProgressInterval time.Duration

	// StdioRoot 是实例进程输出的落盘根目录（空 = 不桥接 stdio）。
	//
	// 它加入采集路径后，stdio 类实例的日志与文件日志走完全相同的后续链路。
	StdioRoot string

	// TimeZone 解释日志行内 `[HH:MM:SS]` 所用时区（空 = UTC）。
	//
	// 为什么必须有：MC 日志行只有时刻没有时区，而中文 locale 的 JVM 写的是本地时间。
	// 按 UTC 解释会让整源时间轴静默偏移（现场 +8 小时）——那正是旧链路缺陷 C。
	TimeZone *time.Location

	// Bindings 是预先打开的绑定表（nil = Start 自己按 RegistryPath 打开）。
	//
	// 给调用方留的入口：装配层需要在**启动前**就把已落盘的绑定灌进去，
	// 以免启动时那几秒的扫描把历史文件判成「无法识别源」而丢弃。
	Bindings *Bindings

	// HitsSource 提供「(源,天) 有数据」事实，用于**启动期重建** Catalog 分区。
	//
	// nil = 不做启动重建（历史天在查询侧仍不可查，但不静默——会在启动日志说明）。
	HitsSource HitsSource
	// CatalogRebuildDays 是启动重建的回溯天数（<=0 用默认 30）。
	CatalogRebuildDays int

	// Catalog 是日志分区目录（传 nil = 不补建分区）。
	//
	// **生产必须传**：查询入口是 `Planner.Plan` → `cat.Get(key)`，没有分区记录
	// ⇒ 该目标不进视图 ⇒ 用户看到「这天没日志」。而 VL 里明明有一堆行。
	Catalog *catalog.Catalog

	// Multiline 是多行规则（键 log_tailer.multiline.*；nil = 不合并）。
	Multiline *tailer.Multiline
	// MultilineTimeout 是 `multiline.timeout`（零值用 tailer 默认 5s）。
	MultilineTimeout time.Duration
	// IncludeGzip 报告是否导入 `.gz` 历史归档（实时采集仍不碰归档）。
	IncludeGzip bool

	// Charset 是节点级默认日志字符集（键 log_ingest.charset；空 = auto）。
	//
	// # 为何复用旧配置键
	//
	// 它表达的是「这台机器上的 JVM 用哪个编码写日志」，与用哪个采集器无关。
	// 新开一个键会让同一台机器上两套配置可以配得不一致。
	Charset string
}

// Runner 是运行中的新采集器（供 main.go 启动与停止）。
type Runner struct {
	collector *tailer.Collector
	sink      *Sink
	bindings  *Bindings
	stdio     *StdioBridge
	cancel    context.CancelFunc
	done      chan struct{}
}

// Bindings 返回绑定表（供登记路径写入）。
func (r *Runner) Bindings() *Bindings {
	if r == nil {
		return nil
	}
	return r.bindings
}

// Stdio 返回 stdio 桥接器（供进程输出回调写入）。
func (r *Runner) Stdio() *StdioBridge {
	if r == nil {
		return nil
	}
	return r.stdio
}

// Start 创建并启动新采集器。
//
// # 与旧采集器的关系
//
// 两者**互斥**：本函数只在开关打开时被调用，且不向旧 `ingest.Manager` 注册任何源。
// 同时跑两套会重复采集（同一文件被两个 harvester 读两遍），所以装配层必须保证只开一套。
//
// # 为什么用 background context
//
// 采集器是长驻服务，生命周期由 Stop 控制，不跟随某个请求。
// 但 ctx 仍要传递（便于测试注入取消）。
func Start(ctx context.Context, cfg Config, vl *vlsup.Client, resolver SourceResolver) (*Runner, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if vl == nil {
		return nil, fmt.Errorf("logtail: VL 客户端未装配")
	}
	// 路径来源有三处：调用方显式给的、stdio 桥接目录、以及**绑定表推导的实例目录**（动态）。
	// 所以这里不要求 Paths 非空——生产就是“静态一个都不配，全靠绑定表”的形态。

	// stdio 桥接：把进程输出（STDIO_PRIMARY 类实例的唯一日志来源）落成文件，
	// 再交给同一个 tailer 采——避免“管道直写 VL”与“文件采集”两套字段/去重口径分叉。
	stdio := NewStdioBridge(cfg.StdioRoot)
	// 静态路径可以一个都没有（生产就是这种情况：全靠绑定表推导），
	// 所以这里不要求 len(paths)>0——真正的“到底有哪些文件”由每轮扫描决定。
	// 没有任何绑定 + 没配 paths 时扫描结果恒为空，但这**不是错误**：
	// 新装的节点在 CP 下发第一个实例前本来就没有日志可采。

	reg, err := tailer.OpenRegistry(cfg.RegistryPath)
	if err != nil {
		return nil, fmt.Errorf("logtail: 打开位点表: %w", err)
	}
	// 绑定表与采集器本身解耦：调用方既可传自己的（重启后立即能定位源），
	// 也可不传（纯内存，仅测试用）。
	bindings := cfg.Bindings
	if bindings == nil {
		bindings, err = OpenBindings(BindingsPathFor(cfg.RegistryPath), stdio.Root())
		if err != nil {
			return nil, fmt.Errorf("logtail: 打开绑定表: %w", err)
		}
	}
	if resolver == nil {
		resolver = bindings.Resolver()
	}
	if resolver == nil {
		return nil, fmt.Errorf("logtail: 源解析器未装配")
	}

	sink := NewSink(vl, resolver, cfg.TimeZone)
	sink.SetCharset(cfg.Charset)
	// 分区补建：没有它，采集照常、VL 有数据，但查询侧全报「这天没日志」。
	ensurer := NewPartitionEnsurer(cfg.Catalog)
	sink.SetPartitionEnsurer(ensurer)
	// 静态路径：stdio 目录 + 调用方显式给的额外路径。
	paths := append([]string{}, cfg.Paths...)
	paths = append(paths, stdio.Paths()...)

	collectorCfg := tailer.CollectorConfig{
		ID:    "instance-logs",
		Paths: paths,
		// 动态采集路径：每轮扫描现取「当前已绑定实例的 WorkDir」× 两种日志布局。
		// 不这样做的话，新建实例的日志要等到 Worker 重启才被采——而 Worker
		// 可能几个月不重启，那段时间的日志就静默地不存在。
		ExtraPaths:       bindings.PathsProvider(),
		Multiline:        cfg.Multiline,
		MultilineTimeout: cfg.MultilineTimeout,
		IncludeGzip:      cfg.IncludeGzip,
		Exclude:          cfg.Exclude,
		ScanInterval:     cfg.ScanInterval,
		ProgressInterval: cfg.ProgressInterval,
		ReadFromHead:     cfg.ReadFromHead,
		RegistryPath:     cfg.RegistryPath,
	}
	if collectorCfg.HasRecursiveGlob() {
		slog.Warn("logtail: 采集路径含 `**`（本实现不支持递归 glob），该 pattern 会被当字面量处理",
			"paths", cfg.Paths)
	}
	c := tailer.NewCollector(collectorCfg, reg, sink)
	c.SetGapReporter(sink.RecordGap)
	// 「源可识别」闸门：resolve 不出来的文件**不建 harvester**。
	//
	// 不加这道闸时，典型故障是：Worker 刚重启、绑定表里还没有某个实例（或首次部署），
	// 而 ReadFromHead 会把整个 latest.log 读一遍、逐行因「无法识别源」被丢——
	// 但位点已推到 EOF，那段历史**永久不采**。用户看不到报错，只会发现“这几天没日志”。
	//
	// 加上后这些文件被原地跳过（每路径告警一次），绑定到达后的下一轮扫描就正常采。
	c.SetHarvestable(func(path string) bool {
		id, _ := resolver.Resolve(path)
		return id != ""
	})

	runCtx, cancel := context.WithCancel(ctx)
	r := &Runner{collector: c, sink: sink, bindings: bindings, stdio: stdio,
		cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		defer func() {
			if p := recover(); p != nil {
				slog.Error("logtail: 采集循环 panic", "panic", p)
			}
		}()
		if err := c.Run(runCtx); err != nil && runCtx.Err() == nil {
			slog.Error("logtail: 采集循环退出", "error", err)
		}
	}()

	// 启动重建：补上「投递当时没建」的历史分区（停机期间 / 功能上线前的数据）。
	// 必须异步且不拒启——它是补救动作，失败不该拖坠 Worker。
	if cfg.HitsSource != nil && cfg.Catalog != nil {
		RunCatalogRebuildOnStartup(runCtx, cfg.Catalog, ensurer, bindings, cfg.HitsSource,
			CatalogRebuildOptions{Days: cfg.CatalogRebuildDays})
	} else if cfg.Catalog != nil {
		slog.Info("未接 hits 数据源：跳过启动期 Catalog 分区重建（历史天在查询侧仍不可查）")
	}

	scanIv, progressIv := collectorCfg.EffectiveIntervals()
	slog.Info("日志采集器已启动（tailer 模式：对齐 Fluent Bit / Filebeat）",
		"paths", paths,
		"exclude", collectorCfg.EffectiveExclude(),
		"readFromHead", cfg.ReadFromHead,
		"registry", cfg.RegistryPath,
		"bindings", bindings.Path(),
		"knownBindings", bindings.Len(),
		"stdioRoot", stdio.Root(),
		"timeZone", func() string {
			if cfg.TimeZone == nil {
				return time.UTC.String()
			}
			return cfg.TimeZone.String()
		}(),
		"scanInterval", scanIv,
		"progressInterval", progressIv)
	return r, nil
}

// Stop 优雅停止采集器（冲刷残留行 + 位点落盘）。
func (r *Runner) Stop() {
	if r == nil {
		return
	}
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(10 * time.Second):
		slog.Warn("logtail: 采集器停止超时")
	}
	// stdio 句柄必须关：它们不属于采集线，但同样需要在退出时释放。
	if err := r.stdio.Close(); err != nil {
		slog.Warn("logtail: 关闭 stdio 落盘句柄失败", "error", err)
	}
	if err := r.bindings.Flush(); err != nil {
		slog.Warn("logtail: 关停时绑定表落盘失败", "error", err)
	}
}

// Stats 返回采集统计（供 /metrics）。
func (r *Runner) Stats() Stats {
	if r == nil {
		return Stats{}
	}
	return r.sink.Snapshot()
}

// Harvesters 返回当前活跃的 harvester 数量。
func (r *Runner) Harvesters() int {
	if r == nil {
		return 0
	}
	return r.collector.Harvesters()
}

// ResolveRegistryPath 把配置里的相对路径解析成绝对路径。
//
// 与旧 cursor 表同一口径：相对路径相对**Worker 数据根**（不是进程 cwd），
// 因为运维写 `var/log/tailer.registry.jsonl` 时想的是“放在 Worker 自己的数据目录里”，
// 而 cwd 会随 systemd 的 WorkingDirectory 变化。
func ResolveRegistryPath(base, configured string) string {
	p := strings.TrimSpace(configured)
	if p == "" {
		p = "var/log/tailer.registry.jsonl"
	}
	if filepath.IsAbs(p) {
		return p
	}
	// 显式相对（./ 或 ../）相对 cwd，其余相对数据根——与旧实现同口径。
	if strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../") || p == "." || p == ".." {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return p
	}
	return filepath.Join(base, p)
}

// DefaultPaths 返回按**实例工作目录**构造的采集 glob（不再是“猜一个 root”）。
//
// # 为何不能用一个 serversRoot 推导
//
// 真机实测：生产实例的 WorkDir 由 CP 逐个下发，可以是任意绝对路径
// （现场是 `/home/wxys233/servers/*`，而 `<dataRoot>/var/servers` 里只有
// `.pid`/`.sock`，0 个实例目录）。用一个 root 推 glob 就得到 0 个匹配——
// 采集“静默无数据”，而日志里只有一句“已启动”。
//
// 故改为：每个已绑定实例的 WorkDir × 规则表里的两种布局。
//
// 取三种可能：
//   - `<workDir>/logs/*.log`   bukkit / waterfall / velocity（含 latest.log 与轮转出的日期.log）
//   - `<workDir>/proxy.log.*`  bungee（proxy.log.0 与 proxy.log.N）
func DefaultPathsFor(workDirs []string) []string {
	// 同一 workDir 只产生两条 pattern，去重后返回（重复 pattern 会白白多扫一遍）。
	seen := make(map[string]struct{}, len(workDirs)*2)
	out := make([]string, 0, len(workDirs)*2)
	add := func(p string) {
		if _, dup := seen[p]; dup {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	for _, wd := range workDirs {
		root := strings.TrimSpace(wd)
		if root == "" {
			continue
		}
		add(filepath.Join(root, "logs", "*.log"))
		add(filepath.Join(root, "proxy.log.*"))
	}
	return out
}

// EnsureDir 建目录（测试与部署辅助）。
func EnsureDir(path string) error {
	d := filepath.Dir(path)
	if d == "" || d == "." {
		return nil
	}
	return os.MkdirAll(d, 0o755)
}
