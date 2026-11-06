package logtail

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/tailer"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// Config 是新采集器的装配配置。
type Config struct {
	// Enabled 是总开关。false 时 Runner 不会被创建（走旧路径）。
	Enabled bool

	// Paths 是采集 glob 列表。
	//
	// 典型值（生产）：
	//
	//	/home/wxys233/servers/*/logs/*.log     ← bukkit/login/lobby
	//	/home/wxys233/servers/*/proxy.log.[0-9]*  ← bungee
	//
	// 已排除的扩展（.gz/.lck/.zip/.tmp）写在 Exclude。
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
	if len(cfg.Paths) == 0 && strings.TrimSpace(cfg.StdioRoot) == "" {
		return nil, fmt.Errorf("logtail: 未配置采集路径")
	}

	// stdio 桥接：把进程输出（STDIO_PRIMARY 类实例的唯一日志来源）落成文件，
	// 再交给同一个 tailer 采——避免“管道直写 VL”与“文件采集”两套字段/去重口径分叉。
	stdio := NewStdioBridge(cfg.StdioRoot)
	paths := append([]string{}, cfg.Paths...)
	paths = append(paths, stdio.Paths()...)
	if len(paths) == 0 {
		return nil, fmt.Errorf("logtail: 未配置采集路径")
	}

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
	collectorCfg := tailer.CollectorConfig{
		ID:               "instance-logs",
		Paths:            paths,
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

// DefaultPaths 返回生产的默认采集路径（基于服务器根目录）。
//
// 与旧实现 `policy_rules.go` 的规则表同口径：
//   - bukkit/waterfall/velocity: `<workdir>/logs/latest.log`
//   - bungee: `<workdir>/proxy.log.0`
//
// 用两个 glob 覆盖两种形态，并依赖 Exclude 排掉归档。
func DefaultPaths(serversRoot string) []string {
	root := strings.TrimSpace(serversRoot)
	if root == "" {
		return nil
	}
	return []string{
		filepath.Join(root, "*", "logs", "*.log"), // bukkit / login / lobby
		filepath.Join(root, "*", "proxy.log.*"),   // bungee
	}
}

// EnsureDir 建目录（测试与部署辅助）。
func EnsureDir(path string) error {
	d := filepath.Dir(path)
	if d == "" || d == "." {
		return nil
	}
	return os.MkdirAll(d, 0o755)
}
