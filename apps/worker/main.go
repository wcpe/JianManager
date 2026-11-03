package main

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof" // 冻结取证/堆剖面（P0 复盘 ✓，仅本机 ✓）
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"github.com/wcpe/JianManager/internal/platform/dataroot"
	"github.com/wcpe/JianManager/internal/platform/directprobe"
	"github.com/wcpe/JianManager/internal/platform/httpclient"
	"github.com/wcpe/JianManager/internal/version"
	workercfg "github.com/wcpe/JianManager/internal/worker"
	"github.com/wcpe/JianManager/internal/worker/artifactcache"
	"github.com/wcpe/JianManager/internal/worker/bot"
	"github.com/wcpe/JianManager/internal/worker/botdist"
	"github.com/wcpe/JianManager/internal/worker/crashreport"
	"github.com/wcpe/JianManager/internal/worker/daemon"
	"github.com/wcpe/JianManager/internal/worker/decompiler"
	wembed "github.com/wcpe/JianManager/internal/worker/embed"
	wgrpc "github.com/wcpe/JianManager/internal/worker/grpc"
	"github.com/wcpe/JianManager/internal/worker/heartbeat"
	jdks "github.com/wcpe/JianManager/internal/worker/jdk"
	"github.com/wcpe/JianManager/internal/worker/logs/archive"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/lifecycle"
	"github.com/wcpe/JianManager/internal/worker/logs/logassemble"
	"github.com/wcpe/JianManager/internal/worker/logs/logconf"
	"github.com/wcpe/JianManager/internal/worker/logs/logtail"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/query/grpcsvc"
	"github.com/wcpe/JianManager/internal/worker/logs/query/vlrange"
	"github.com/wcpe/JianManager/internal/worker/logs/retention"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
	"github.com/wcpe/JianManager/internal/worker/metrics"
	"github.com/wcpe/JianManager/internal/worker/orphanaudit"
	"github.com/wcpe/JianManager/internal/worker/pkgmgr"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/internal/worker/register"
	wruntime "github.com/wcpe/JianManager/internal/worker/runtime"
	"github.com/wcpe/JianManager/internal/worker/runtimescan"
	"github.com/wcpe/JianManager/internal/worker/setup"
	workerstorage "github.com/wcpe/JianManager/internal/worker/storage"
	"github.com/wcpe/JianManager/internal/worker/tunnel"
	"github.com/wcpe/JianManager/internal/worker/ws"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// crashSignal 归一化崩溃上报的信号名。
//
// docker 模式被 cgroup OOM killer 终止时，容器退出码恒为 137 而宿主侧没有信号——
// 此处补 "killed"（SIGKILL/137 口径），使 CP 侧崩溃归类能识别为 OOM，而不是退化成 unknown。
// 其余情况沿用进程/容器给出的信号名（daemon/direct 走 WaitStatus.Signal().String()）。
func crashSignal(info process.CrashInfo) string {
	if info.Signal != "" {
		return info.Signal
	}
	if info.OOMKilled {
		return "killed"
	}
	return ""
}

// main 是 Worker Node 入口。
// 若以 `daemon` 子命令模式启动（由 daemonStrategy spawn），则运行 wrapper 而非 Worker 主进程。
// 见 ADR-003: 守护进程 Wrapper 模式。
func main() {
	if version.Requested(os.Args[1:]) {
		fmt.Println(version.Version)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "daemon" {
		runDaemonWrapper()
		return
	}
	// `log-index-export` / `log-event-status` 两个只读导出子命令已随旧链路删除：
	// 它们读的是 ingest 的采集索引库（SQLite），新架构没有索引库。
	// **这是功能下线点**：`scripts/rollback-log-index.sh` 里的 `--export` 路径会退化为
	// 直接读 sqlite3（该脚本本身还有兜底分支）。
	runWorker()
}

/**
 * normalizeSubcommandArgs 归一化子命令的取值参数：把 `--k v` 与 `--k=v` 统一成 `--k=v`。
 *
 * 为什么必须归一化：解析器（如下层 ingest.parseEventStatusArgs）只认等号形式，而用法注释写的是
 * 空格形式 —— 按注释敲 `--source inst:1/stdout --from 100 --to 200` 会被**静默丢弃**，
 * 于是「看某源 100–200 区间」实际导出全源全区间，结论直接失真且毫无提示。
 * 同理，未知参数与缺值参数（末位 `--k`、下一参又是 `--x`）也一律报错：这是排障用的只读工具，
 * 宁可失败也不要静默换语义。
 */
func normalizeSubcommandArgs(args []string, known ...string) ([]string, error) {
	allowed := make(map[string]struct{}, len(known))
	for _, name := range known {
		allowed[name] = struct{}{}
	}
	usage := strings.Join(known, " / ")
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, hasValue := strings.Cut(arg, "=")
		if _, ok := allowed[name]; !ok {
			return nil, fmt.Errorf("未知参数 %q（可用参数：%s）", arg, usage)
		}
		if !hasValue {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("参数 %s 缺少取值", name)
			}
			value = args[i+1]
			i++
		}
		if value == "" {
			return nil, fmt.Errorf("参数 %s 的取值为空", name)
		}
		if strings.HasPrefix(value, "--") {
			return nil, fmt.Errorf("参数 %s 缺少取值（下一个参数是 %s）", name, value)
		}
		out = append(out, name+"="+value)
	}
	return out, nil
}

/** flagValue 取归一化参数里某个键的取值；重复出现即报错（避免「后者静默覆盖前者」）。 */
func flagValue(args []string, name string) (string, error) {
	found := ""
	count := 0
	for _, arg := range args {
		key, value, ok := strings.Cut(arg, "=")
		if !ok || key != name {
			continue
		}
		found = value
		count++
	}
	if count > 1 {
		return "", fmt.Errorf("参数 %s 重复出现 %d 次", name, count)
	}
	return found, nil
}

// runDaemonWrapper 以 wrapper 子进程模式运行。
// 配置通过环境变量 JM_DAEMON_WRAPPER_CONFIG 传递（JSON）。
func runDaemonWrapper() {
	// wrapper 必须在 Worker 重启后存活（ADR-003）：Worker 死后其建立的 stdout/stderr
	// 管道读端关闭，wrapper 下一次日志写会触发 SIGPIPE 而被默认终止。忽略之，改为 EPIPE 丢弃。
	daemon.IgnoreBrokenPipe()

	cfg, err := daemon.ParseWrapperConfigFromEnv()
	if err != nil {
		slog.Error("daemon wrapper 配置解析失败", "error", err)
		os.Exit(1)
	}
	if err := daemon.Run(cfg); err != nil {
		slog.Error("daemon wrapper 退出", "instanceId", cfg.InstanceUUID, "error", err)
		os.Exit(1)
	}
}

// configPathArg 返回命令行第 1 个位置参数当且仅当它是「现存的配置文件路径」（见 ADR-051）。
//
// 历史用法 `worker /path/worker.yml` 显式传配置文件仍支持；但 setup 的 `--xxx` flag 与不存在的
// 路径不当配置文件（否则会误判已配置而跳过 setup）。返回空串表示「未显式给配置文件」。
func configPathArg() string {
	if len(os.Args) <= 1 {
		return ""
	}
	first := os.Args[1]
	if strings.HasPrefix(first, "-") {
		return "" // setup flag，非配置文件
	}
	if st, err := os.Stat(first); err == nil && !st.IsDir() {
		return first // 现存文件，按显式配置文件处理
	}
	return ""
}

// setupArgs 返回传给 setup 解析的命令行参数（剥离程序名；首个位置参数若为配置文件路径也剥离）。
func setupArgs() []string {
	args := os.Args[1:]
	if len(args) > 0 && configPathArg() != "" {
		// 首参是显式配置文件时不会进 setup（已配置），此分支几乎不触达；防御性剥离。
		return args[1:]
	}
	return args
}

// isConfigured 报告 Worker 是否已配置（见 ADR-051）。
//
// 已配置 ⇔ 有 worker.yml/.yaml 配置文件 或 有 <data-dir>/etc/node-identity.json 身份文件。
// 二者任一存在即视为已配置（有 yml=写过配置；有身份=注册过）→ 跳过 setup。两者皆缺=全新机器=进 setup。
// data-dir 按 dataroot.Resolve 同优先级解析（--data-dir > 环境变量 > ./data），只解析路径不建目录。
func isConfigured() bool {
	if workercfg.WorkerConfigExists() {
		return true
	}
	// 身份文件路径据 data-dir 解析；--data-dir flag 与 JIANMANAGER_DATA_DIR 均纳入考量。
	override := ""
	for _, a := range parseDataDirArg(os.Args[1:]) {
		override = a
	}
	root, err := dataroot.Resolve(override)
	if err != nil {
		return false // 解析失败按未配置处理（让 setup 据 dataroot 重新解析并报清晰错误）
	}
	if _, err := os.Stat(register.IdentityPath(root.EtcDir())); err == nil {
		return true
	}
	return false
}

// parseDataDirArg 从命令行参数中提取 --data-dir 的值（支持 --data-dir v 与 --data-dir=v）。
// 返回 0 或 1 个元素（便于调用方取最后一个）。
func parseDataDirArg(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--data-dir" && i+1 < len(args) {
			out = append(out, args[i+1])
			i++
		} else if strings.HasPrefix(a, "--data-dir=") {
			out = append(out, strings.TrimPrefix(a, "--data-dir="))
		}
	}
	return out
}

// logStartupPhase 打一条启动阶段耗时日志（范围①）。
//
// 为什么默认开启而不藏在开关后面：启动期每次进程生命周期只跑一遍，没有噪声成本；
// 而运行期那套 `JIANMANAGER_STAGE_TIMING=1`（采集轮内计时）是高频路径，才需要开关。
// 现场出问题时，人最需要的是「上一次启动的日志里本来就有数据」。
func logStartupPhase(phase string, cost time.Duration) {
	slog.Info("启动阶段完成", "phase", phase, "elapsed_ms", cost.Milliseconds())
}

// warnIfStateTooLarge 启动期状态规模超阈告警（范围④）。
//
// 为什么要这条：旧事故（2026-09-28）里，1.2GB 状态把注册拖到 8m39s，而全程没有任何提示。
// 那时若有一条「状态偏大，启动会变慢」的 WARN，排查方向会完全不同。
//
// 判据用**索引库 + 事件段**的实际字节数，而不是行数：行数要开库才能数（那本身就要花时间），
// 而文件大小是 stat 一次即得，恰好在「加载之前」就能判断——这正是「启动前告警」的语义。
//
// 【修复：此前只扫了 var/log，漏掉 var/index】上面这句话一直是对的，但实现漏了一半：
// 只扫 `var/log`（事件段），没扫 `var/index`（状态索引库）——而后者才是慢加载的
// 主因承担者：`Store.load()` 对每张表做全表 SELECT + ORDER BY，成本随索引行数线性增长
// （实测 100k 行 = 453ms，见 stateindex/load_scaling_bench_test.go）。
// 只扫 log 目录会出现「索引库已涨到 GB 级、告警却一声不响」的假安全。
func warnIfStateTooLarge(root *dataroot.Root, thresholdMB int) {
	if thresholdMB <= 0 {
		return
	}

	// 两个目录分别统计，告警时一并给出，便于直接定位是哪一侧涨了。
	logBytes := dirSizeQuiet(root.LogDir())
	indexBytes := dirSizeQuiet(root.IndexDir())
	total := logBytes + indexBytes

	mb := total / (1 << 20)
	if mb < int64(thresholdMB) {
		return
	}
	slog.Warn("启动期状态数据规模偏大，加载与注册可能变慢",
		"state_size_mb", mb,
		"log_mb", logBytes/(1<<20),
		"index_mb", indexBytes/(1<<20),
		"threshold_mb", thresholdMB,
		"log_dir", root.LogDir(),
		"index_dir", root.IndexDir(),
		"suggestion", "加载成本主因是索引库行数（load 全表扫描），可降低采集源数/保留窗口；"+
			"若增长来自 var/log 则检查事件段是否异常膨胀；本项仅告警，不改变启动行为")
}

// dirSizeQuiet 统计目录下所有普通文件的总字节数；目录不存在或读取失败返回 0（不报错）。
func dirSizeQuiet(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, statErr := d.Info(); statErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func runWorker() {
	// 启动分段计时。放在最前面，第一个 Mark 才有意义。
	startup := newStartupTimer()

	// Windows 服务默认 cwd=System32，会把 setup 写出的 worker.yml 落到错误位置且重启找不到（FIX-2/3）。
	// 以服务身份运行时先把工作目录切到 exe 所在的安装目录（与 Linux systemd WorkingDirectory 对齐）。
	chdirToExeDirIfService()

	// 加载配置：worker.yml + JIANMANAGER_ 环境变量覆盖（见 ADR-020）。
	// 配置可选参数从命令行第 1 个参数取（如 `worker /path/worker.yml`），缺省自动查找。
	// 第 1 个参数仅当是「现存的配置文件路径」时才当配置文件；setup 的 --xxx flag 不算（见下自检）。
	cfgPath := configPathArg()

	// 未配置自检（，见 ADR-051）：无 worker.yml/.yaml 且无 etc/node-identity.json → 进 setup。
	// setup 采集入参（TTY 交互 / 无 TTY 参数+env）→ 写 worker.yml + 首注册 + 持久化身份 → 转 run。
	// 已配置（有 yml / 有身份 / 显式传配置文件路径）→ 跳过 setup，走现有 run（现状零变化）。
	var setupResult *setup.Result
	if cfgPath == "" && !isConfigured() {
		res, err := setup.Run(context.Background(), ".", setup.Options{
			Args:  setupArgs(),
			IsTTY: setup.IsStdinTTY(),
		})
		if err != nil {
			slog.Error("节点上线 setup 失败", "error", err)
			os.Exit(1)
		}
		setupResult = res
		slog.Info("节点 setup 完成，转入正常运行", "nodeUUID", setupResult.Identity.NodeUUID)
	}

	var cfg *workercfg.Config
	if setupResult != nil {
		// setup 已在内存构造配置（含刚写出的 worker.yml 内容），直接复用，不重读文件。
		cfg = setupResult.Config
	} else {
		loaded, err := workercfg.Load(cfgPath)
		if err != nil {
			slog.Error("加载 Worker 配置失败", "error", err)
			os.Exit(1)
		}
		cfg = loaded
	}

	// 2026-10-04（候选 2 测量）：按需开启互斥锁争用采样 —— m.mu 是全局热锁（代码侧 61 个调用点、
	// pprof 点名 14 个等待者），"谁持锁最久/谁被等最多"必须先有数据再谈收窄。
	// 0 = 关闭（Go 默认 fraction=0，也是本项默认）。
	//
	// GO-7：此前该旋钮只认环境变量 JM_MUTEX_PROFILE_FRACTION，绕过项目配置体系 ⇒ 运维无从从
	// 配置面发现它、也无法纳入版本化管理。现以 `log.mutex_profile_fraction` 为**正典**；
	// 环境变量保留为「配置加载失败时的兜底」（例如要诊断的正是启动早期），设置时打 Warn 提示
	// 运维改走配置面。1 = 全采（诊断窗口），数值越大采样越稀（如 5 = 1/5）。
	mutexFraction := cfg.Log.MutexProfileFraction
	if v := os.Getenv("JM_MUTEX_PROFILE_FRACTION"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			if mutexFraction == 0 {
				mutexFraction = n
			}
			slog.Warn("JM_MUTEX_PROFILE_FRACTION 是配置面外的兜底旋钮，请改用 log.mutex_profile_fraction",
				"env", n, "configured", cfg.Log.MutexProfileFraction)
		} else {
			slog.Warn("JM_MUTEX_PROFILE_FRACTION 非法，忽略", "value", v)
		}
	}
	if mutexFraction > 0 {
		runtime.SetMutexProfileFraction(mutexFraction)
		slog.Info("互斥锁争用采样已配置", "fraction", mutexFraction)
	}
	// 日志等级/格式装配（此前**完全没有装配点**：Worker 从不调用 slog.SetDefault，
	// 于是 log.level / log.format 两个配置项形同不存在——写 debug 不产生任何 DEBUG 输出，
	// 写 error 也压不住 INFO。现场表现是「改了配置但日志没变」，极难归因。
	//
	// 口径（勿夸大能力）：等级**只在启动期按配置装配一次**，进程内没有任何可达入口能再改它
	// ⇒ 现场改级别必须改配置并**重启 Worker**。用 LevelVar 承载该等级，只是把它绑到 handler
	// 上（等价于固定等级），将来若真接出运行期调级入口（信号/心跳策略），这里就是接线点。
	logLevelVar := new(slog.LevelVar)
	logLevel, logLevelErr := cfg.LogLevel()
	if logLevelErr != nil {
		slog.Error("日志等级配置非法，拒绝启动", "error", logLevelErr)
		os.Exit(1)
	}
	logLevelVar.Set(logLevel)
	logFormat, logFormatErr := cfg.LogFormat()
	if logFormatErr != nil {
		slog.Error("日志格式配置非法，拒绝启动", "error", logFormatErr)
		os.Exit(1)
	}
	// 日志落地：stderr 之外再写一份自轮转文件（见 process_log.go 的说明）。
	// stderr 保留，因此前台/容器/CI 场景不变；文件供面板诊断与运维 grep。
	//
	// cfg.Log 传入轮转与保留策略（大小/份数/天数/压缩）：零值回退内置默认，
	// 因此老配置不需要改。
	installProcessLogger(cfg.DataDir, logLevelVar, logFormat, cfg.Log)
	// 诊断接口需要知道日志文件在哪：注入路径提供者（见 internal/worker/grpc/self_log.go）。
	// 为空时 PullSelfLog 返回结构化错误而不是空成功。
	wgrpc.SetSelfLogPathProvider(ProcessLogPath)
	slog.Info("Worker 日志已按配置装配", "level", logLevel.String(), "format", logFormat,
		"note", "等级在启动期生效，调整需重启进程")

	// 启动自证（2026-10-02 真机复验）：把「实际读到的配置文件」与关键采集口径一次打清楚。
	// 现场教训：`log_ingest.time_zone: local` 配上仍按 UTC 解释日志——可能是读的不是你改的文件，
	// 也可能是 local 在 Worker 进程内恰好解析成 UTC（容器/systemd 常见）。这一行让两者当场可分。
	configSource := cfg.ConfigPath()
	if configSource == "" && setupResult != nil {
		// setup 刚写出 worker.yml 并在内存里构造配置，不重读文件：此处不能显示成「无配置文件」。
		configSource = "<setup 本次写出并在内存中构造>"
	}
	slog.Info("Worker 配置已加载",
		"configPath", configSource,
		"logIngestTimeZone", cfg.IngestDefaultTimeZone(),
		"logIngestCharset", cfg.IngestDefaultCharset())
	startup.Mark("config")

	// 出站 HTTP 客户端持有者（见 ADR-037/043）：所有出站下载（自更新/JDK/CFR/服务端 jar）
	// 经此进程级代理 client。proxy.url 留空=直连（沿用环境变量代理）。非法代理 URL 启动即 fail-fast。
	// 持有者可运行时重建：CP 经心跳下发节点期望代理后即时生效（custom→节点值 / inherit→全局默认）。
	// 启动按 worker.yml/env 基线构造；CP 下发为空时回退此基线。
	outboundProvider, err := httpclient.NewProvider(cfg.Proxy)
	if err != nil {
		slog.Error("初始化出站代理客户端失败", "proxy", httpclient.Sanitize(cfg.Proxy.URL), "error", err)
		os.Exit(1)
	}
	if cfg.Proxy.URL != "" {
		slog.Info("出站代理已启用", "proxy", httpclient.Sanitize(cfg.Proxy.URL), "noProxy", cfg.Proxy.NoProxy)
	}

	nodeName := cfg.Name
	wsPort := cfg.WS.Port
	host := cfg.Host // 留空自动检测本机 IP
	wsTokenSecret := ""

	// 节点 UUID 在首次注册后由 CP 签发并落本地身份文件复用；启动时先占位。
	nodeUUID := "local-dev"

	// 解析并初始化项目自包含数据根（配置 data_dir，缺省 ./data，可经 JIANMANAGER_DATA_DIR 覆盖）。
	// 运行态数据全部收口到此根，整体可迁移。参见 ADR-010。
	root, err := dataroot.Init(cfg.DataDir)
	if err != nil {
		slog.Error("初始化数据根失败", "error", err)
		os.Exit(1)
	}

	// 本地身份提前加载（见 ADR-061）：WS 令牌密钥初始值优先取身份文件持久化值
	// （上次 CP 下发），回退 worker.yml jwt_secret（旧 CP/首装兼容）——须在 WS 服务器构造前
	// 就绪，消除「注册完成前用旧密钥校验」窗口。注册流程复用本次加载结果，不二次读文件。
	etcDir := root.EtcDir()
	localIdentity, idErr := register.LoadIdentity(etcDir)
	if idErr != nil {
		slog.Error("读取本地节点身份失败", "error", idErr)
		os.Exit(1)
	}
	if setupResult != nil {
		// setup 刚完成首注册并持久化身份（含 CP 下发的 WS 令牌密钥），直接复用不重读。
		localIdentity = setupResult.Identity
	}
	if localIdentity != nil && localIdentity.WSTokenSecret != "" {
		wsTokenSecret = localIdentity.WSTokenSecret
	}

	// 服务器工作目录根：默认数据根下 var/servers；配置 servers_dir 显式覆盖（兼容旧部署）。
	serversDir := root.ServersDir()
	if cfg.ServersDir != "" {
		serversDir = cfg.ServersDir
	}

	slog.Info("Worker Node 启动", "name", nodeName, "wsPort", wsPort, "dataDir", root.Base(), "serversDir", serversDir)
	startup.Mark("bootstrap")

	// 启后自检：本 Worker 所在 systemd 单元的 KillMode 是否会让「重启单元」连坐杀掉全部游戏服
	// （见 daemon.WarnIfUnitKillModeUnprotected：该属性是部署期属性，手工 systemd-run / 手写单元
	// 的部署会漏掉，落到默认 control-group——2026-10-09 真机正是如此）。只告警、不阻断启动，
	// 任何查询失败（非 systemd 环境、systemctl 缺失、超时）均静默跳过。
	daemon.WarnIfUnitKillModeUnprotected()

	// 启前回收本节点 vlsup 孤儿（P0 复盘 2026-10-03 ✓）：worker 被强杀后其 vlsup 子进程成孤儿、
	// 继续占用 hot/cold 端口 ⇒ 新 worker 报 `hot exited immediately after start`（现场 ✓，只能靠
	// RangeClient 接旧实例 ⇒ 采集面降级 ✗）。判据＝**本 data 根**（cmdline 含根 ＋ exe 在根下，两道都过 ✓）
	// ⇒ 不误杀他进程 ✓；回收本身有界（每个孤儿 TERM→5s→KILL ✓）；非 Linux（无 /proc）仅 Debug 降级 ✓。
	if reaped, err := vlsup.ReapOrphans(root.Base(), "startup", logReapedVLOrphan); err != nil {
		slog.Debug("vlsup 孤儿回收不可用（非致命）", "error", err)
	} else if len(reaped) > 0 {
		slog.Warn("启前 vlsup 孤儿回收完成", "count", len(reaped), "pids", reaped)
	}
	// 维护自身 PID 记录（与 CP 的 cp-lifecycle.sh 同口径）：生产曾遗留停在多日前的 worker.pid，
	// 按它识别进程会认错；此处启动即原子覆盖，写失败仅告警、不影响启动。
	if err := writeWorkerPIDRecord(root.Base(), wsPort, cfgPath); err != nil {
		slog.Warn("写 worker.pid 记录失败（不影响运行）", "error", err)
	}
	// 初始化进程管理器
	manager := process.NewManager(serversDir)
	// procManager 与 manager 同一实例，只是给它一个**不会被日志采集代码块遮蔽**的名字。
	//
	// 为什么不直接用 manager：下方日志采集段会写 `manager, ingestErr = newIngest()`，
	// 把同名变量改成 `*ingest.Manager`。那个遮蔽是既有的、改动面很大的，而吸存需要的是
	// 进程管理器（要拿实例的 WorkDir），两类 Manager 类型名相近但不能互换——
	// 留着同名只用一次容易在下一个人改代码时静默传错。
	procManager := manager
	// 接管重试窗口（①）：Worker 重启后对存活 wrapper 的 reconnect 重试序列（默认 ≈127s），
	// 覆盖分钟级瞬时故障；必须在下方 RecoverDaemonInstances 之前装配，否则该轮接管仍用默认序列。
	manager.SetRecoverRetryBackoff(cfg.Recover.RetryBackoffSequence())
	// 启动内存闸：可用内存塞不下待启实例即拒绝启动，防节点 OOM 失联。
	manager.SetMemGuard(process.MemGuardConfig{
		ReserveMB: cfg.MemoryGuard.ReserveMB,
		Disabled:  cfg.MemoryGuard.Disabled,
	})

	// 初始化 JDK 管理器：托管根 <dataRoot>/opt/jdks；可选追加系统 JDK 探测目录。
	// 参见 ADR-010：JDK 从旧的 <serversDir>/jdks 迁移到 opt/jdks。
	var systemJDKDirs []string
	if v := os.Getenv("JIANMANAGER_JDK_SYSTEM_DIRS"); v != "" {
		for _, d := range strings.Split(v, string(os.PathListSeparator)) {
			d = strings.TrimSpace(d)
			if d != "" {
				systemJDKDirs = append(systemJDKDirs, d)
			}
		}
	}
	var jdkMgr *jdks.Manager
	if os.Getenv("JIANMANAGER_DISABLE_JDK") != "1" {
		jdkMgr = jdks.NewManager(root.JDKsDir(), systemJDKDirs)
		// JDK 下载经进程级出站持有者：CP 下发代理改动运行时即时生效。
		jdkMgr.SetHTTPClientProvider(outboundProvider.Client)
		slog.Info("JDK manager enabled", "rootDir", root.JDKsDir())
	} else {
		slog.Info("JDK manager disabled by JIANMANAGER_DISABLE_JDK=1")
	}
	// 孤儿处置审计回调（456）：误杀拦截/孤儿处置既落 Worker 结构化日志（不静默），
	// 又经 Worker→CP 出站信道上报落 CP 审计库（spec §2.2）。
	//
	// 装配顺序（N2）：必须在 RecoverDaemonInstances **之前**。接管兜底的处置/误杀拦截动作
	// 就发生在启动恢复期，若回调晚装配，这些真孤儿处置只落 slog、进不了 CP 审计库（reviewer 无从查证）。
	cpAddr := cfg.ControlPlane
	if cpAddr == "" {
		cpAddr = "localhost:9100"
	}
	orphanAuditReporter := orphanaudit.New(cpAddr)
	// 进程退出前释放复用的上报连接与派发 goroutine（N4）。
	defer orphanAuditReporter.Close()
	// 身份注入同样提前：本地身份文件已在启动早期加载（重注册路径），此处先注入，使**启动期**恢复
	// 的处置也能凭既有身份上报。首装尚无身份时上报安全丢弃；注册成功后于下方再注入一次覆盖运行期。
	if localIdentity != nil {
		orphanAuditReporter.SetIdentity(localIdentity.NodeUUID, localIdentity.NodeSecret)
	}
	manager.SetOrphanAuditHandler(func(action, targetID, detail string, success bool, errMsg string) {
		slog.Warn("孤儿处置审计", "action", action, "target", targetID, "detail", detail, "success", success, "error", errMsg)
		orphanAuditReporter.Report(orphanaudit.Event{
			Action: action, TargetID: targetID, Detail: detail, Success: success, ErrMsg: errMsg,
		})
	})

	// 这是 ADR-003「平台重启不杀游戏服」的关键路径。
	//
	// 本函数只同步完成「快速首拨」（每条 PID 记录拨一次），首拨失败的记录转后台按
	// recover.retry_backoff 序列继续重试（最长 ≈127s）。故下面的运行期孤儿扫描不会被接管重试
	// 拖住——旧实现把重试串行摊在启动主流程上，一条不可达记录即把扫描器推迟 127s（N 条线性累加），
	// 启动窗口内完全没有扫描能力。接管重试与扫描并行的并发隔离见 process.takeoverProtects。
	recovered, recoverErr := manager.RecoverDaemonInstances()
	if recoverErr != nil {
		slog.Warn("恢复 daemon 实例失败", "error", recoverErr)
	}
	if recovered > 0 {
		slog.Info("已恢复 daemon 实例连接", "count", recovered)
	}
	// 就地收养的恢复（与 daemon 恢复同一阶段）。
	//
	// 这些进程不是平台 spawn 的（tmux/systemd 起的服），没有 wrapper 可重连；
	// 它们的“活着的证据”只有当初落盘的那条收养记录。不恢复的话，
	// Worker 每次重启都让一批在跑的服变回 STOPPED——面板与磁盘事实分家。
	if adopted, cleaned := manager.RestoreAdoptedRuntimes(); adopted > 0 || cleaned > 0 {
		slog.Info("就地收养状态已恢复", "restored", adopted, "cleaned", cleaned)
	}
	phaseDone(startup.PhaseProcessRecover)

	// 运行期周期孤儿扫描：随 Worker 常驻，持续兜底 wrapper 死/Java 活、direct 孤儿、
	// docker 残留，按策略处置（默认 warn 只告警，可配 auto 自动清理）。补齐「仅启动时清理」的缺口。
	if !cfg.OrphanScan.Disabled {
		policy := process.NormalizeOrphanDisposePolicy(cfg.OrphanScan.DisposePolicy)
		orphanScanner := process.NewOrphanScanner(manager, cfg.OrphanScan.ScanInterval(), policy)
		// 未纳管活进程的自动收养开关（默认开；此处接线使 orphan_scan.auto_adopt 生效）。
		orphanScanner.SetAutoAdopt(cfg.OrphanScan.AutoAdoptEnabled())
		scannerCtx, cancelScanner := context.WithCancel(context.Background())
		defer cancelScanner()
		orphanScanner.Start(scannerCtx)
	}

	// 运行期实例健康巡检与自愈：存活 ∧ 响应双维识别假死，受控自愈（warn/restart）
	// 与崩溃熔断（窗口内重启超限即停自动重启）。本地配置 worker.health_scan.*；运行期阈值/动作
	// 可经心跳响应下发覆盖（见下方 heartbeat.SetHealthPolicyApplier）。
	healthScanner := process.NewHealthScanner(manager, !cfg.HealthScan.Disabled, cfg.HealthScan.ScanInterval(), cfg.HealthScan.HealthPolicy())
	if !cfg.HealthScan.Disabled {
		healthCtx, cancelHealth := context.WithCancel(context.Background())
		defer cancelHealth()
		healthScanner.Start(healthCtx)
	}

	// 一个 CPU 采样所有者发布窗口快照，周期采集、RPC 与心跳只读取同一结果。
	cpuSampler := metrics.NewCPUSampler(30 * time.Second)
	cpuSampler.Start(context.Background())
	defer cpuSampler.Stop()
	collector := metrics.NewCollector(30*time.Second, cpuSampler)
	collector.StartPeriodic(func(m metrics.NodeMetrics) {
		slog.Debug("指标采集",
			"cpu", fmt.Sprintf("%.1f%%", m.CPUUsage*100),
			"memory", fmt.Sprintf("%.1f%%", m.MemoryUsage*100),
			"goroutines", m.Goroutines,
		)
	})
	defer collector.Stop()

	workerServer := wgrpc.NewServer(manager, nodeUUID, collector, jdkMgr, root)
	// T11：装配 Worker 日志查询栈（Catalog+Planner+grpcsvc）。
	// 配置了健康的 localhost VL 时接入真实 RangeClient；否则保留显式 LOG_UNSUPPORTED。
	var rangeClient query.RangeClient
	var vlHTTPClient *vlsup.Client
	var vlSupervisor *vlsup.Supervisor
	var managedClients *managedLogClients
	budgetConfig, budgetErr := cfg.LogVL.RuntimeBudget()
	if budgetErr != nil {
		slog.Error("日志资源预算配置非法", "error", budgetErr)
		return
	}
	resourceBudget, budgetErr := vlsup.NewBudgetController(budgetConfig)
	if budgetErr != nil {
		slog.Error("日志资源预算控制器创建失败", "error", budgetErr)
		return
	}
	logRuntimeCtx, cancelLogRuntime := context.WithCancel(context.Background())
	defer cancelLogRuntime()
	// 运行期自愈看护的取消函数（缺陷修复：VL 运行期崩溃后无自愈）。
	// 必须在 StopAll 之前调用：先停止看护，再停进程，避免"刚停就被看护判成崩溃又拉起"。
	var cancelVLGuardian context.CancelFunc
	managedBinary, managedSHA := cfg.LogVL.BinaryPath, cfg.LogVL.AssetSHA256
	var packageErr error
	if strings.TrimSpace(cfg.LogVL.PackagePath) != "" {
		managedBinary, packageErr = vlsup.InstallApprovedPackage(context.Background(), cfg.LogVL.PackagePath,
			root.Abs("var/log/vl-assets"), runtime.GOOS, runtime.GOARCH)
		if packageErr != nil {
			slog.Error("VictoriaLogs 受管资产安装失败", "error", packageErr)
		} else {
			managedSHA = vlsup.CurrentApprovedExeSHA256()
			slog.Info("VictoriaLogs 受管资产已校验安装", "tag", vlsup.AssetTag, "sha256", managedSHA)
		}
	}
	if packageErr == nil && strings.TrimSpace(managedBinary) != "" {
		dataRoot := cfg.LogVL.DataRoot
		if dataRoot == "" {
			dataRoot = root.Abs("var/log/vl")
		}
		vlSupervisor, err = vlsup.New(vlsup.Options{
			BinaryPath: managedBinary, AssetSHA256: managedSHA,
			DataRoot: dataRoot, RetentionPeriod: cfg.LogVL.RetentionPeriod,
			// 删除三闸保持默认关闭；原生整日 TTL 仍独立运行，不提供永久副本保证。
			DeleteEnabled: cfg.LogVL.DeleteEnabled,
			// 磁盘占用上限（2026-10-29）：与 retention_period 同时生效，谁先到谁触发。
			// 默认 0 = 不限制（保持改动前行为）；生产建议显式配 max_disk_space_bytes。
			MaxDiskSpaceUsageBytes: cfg.LogVL.MaxDiskSpaceBytes,
			MaxDiskUsagePercent:    cfg.LogVL.MaxDiskUsagePercent,
			AuthUsername:           cfg.LogVL.Username, AuthPassword: cfg.LogVL.Password,
			Ports: map[vlsup.Namespace]int{
				vlsup.NamespaceHot: cfg.LogVL.HotPort,
			},
			NamespaceBudgets: budgetConfig.Namespaces,
			// 新键已归并旧 cache/Go 软限制；RSS 另由唯一控制器执行真实准入。
			MemoryLimitBytes: cfg.LogVL.MemoryLimitBytes,
			// 官方推荐按可用内存百分比自适应；显式 hot_cache_bytes 时由
			// EffectiveCacheBytes 走 allowedBytes，此处仅作默认路径。
			MemoryAllowedPercent: cfg.LogVL.MemoryAllowedPercent,
			// 流标签（2026-10-31）：默认开，取值由架构裁定（`_stream_fields=log_source_id,stream`）。
			// 写入客户端一律经 supervisor.ClientFor 得到，所以开关必须传到这里。
			DisableStreamFields: cfg.LogVL.DisableStreamFields,
		})
		if err != nil {
			slog.Error("VictoriaLogs supervisor 创建失败", "error", err)
		} else {
			// 先装配所有固定地址，首次关闭或失败不影响后续运行期启动的业务接线。
			if clients, clientErr := newManagedLogClients(vlSupervisor); clientErr != nil {
				slog.Error("VictoriaLogs 固定客户端装配失败", "error", clientErr)
			} else {
				managedClients = clients
				vlHTTPClient = clients.Main
				rangeClient = clients.Ranges
			}
			if err = vlSupervisor.Start(context.Background(), vlsup.NamespaceHot); err != nil {
				slog.Error("VictoriaLogs 启动失败，保留运行期恢复接线", "error", err)
			}
			// 运行期自愈看护：过去只在启动路径做一次孤儿回收，VL 运行期崩溃（OOM、段损坏）后
			// 账面只会被读状态时改成 FAILED，进程不会自己起来 ⇒ 采集/查询长期降级，直到人工
			// 重启 Worker。这里启动守护循环：只重启「此前健康（稳定存活超过窗口）、现已退出」
			// 的实例；指数退避 + 连续重启上限，达上限熔断并告警；与显式 Start/Stop 互斥。
			//
			// 默认开启的理由：判据保守（存活窗口 + 真实退出）、失败有上限（默认 5 次后停手）、
			// 且不改变任何显式操作语义；关闭用 JIANMANAGER_DISABLE_LOG_VL_GUARDIAN=1
			// （与 JIANMANAGER_DISABLE_JDK 同口径，避免为开关改配置结构）。
			if os.Getenv("JIANMANAGER_DISABLE_LOG_VL_GUARDIAN") != "1" {
				guardCtx, cancelGuard := context.WithCancel(context.Background())
				cancelVLGuardian = cancelGuard
				// 兜底：任何提前返回/致命退出路径都不会留下看护 goroutine。
				defer cancelGuard()
				if guardErr := vlSupervisor.StartGuardian(guardCtx, vlsup.GuardianOptions{}); guardErr != nil {
					slog.Warn("VictoriaLogs 运行期看护启动失败（不影响既有行为）", "error", guardErr)
				}
			}
			// 受管 VL 启动是异步的：接线 ingest/查询前先等 HOT 就绪，避免启动期写连接被拒
			// 导致采集运行时创建失败（重启时序 / Runbook C）。
			hotReadyCtx, cancelHotReady := context.WithTimeout(context.Background(), 15*time.Second)
			if waitErr := vlSupervisor.WaitHealthy(hotReadyCtx, vlsup.NamespaceHot, 15*time.Second); waitErr != nil {
				slog.Warn("VictoriaLogs HOT 未在超时内就绪，采集/查询将保持降级", "error", waitErr)
			}
			cancelHotReady()
			if managedClients != nil {
				slog.Info("VictoriaLogs 三层固定客户端已装配；进程健康与 Catalog/投影就绪分别判定")
			}
		}
	}
	if rangeClient == nil && strings.TrimSpace(cfg.LogQuery.VLURL) != "" {
		vlClient, vlErr := vlsup.NewClient(vlsup.ClientOptions{
			BaseURL:  cfg.LogQuery.VLURL,
			Username: cfg.LogQuery.Username,
			Password: cfg.LogQuery.Password,
			// 外部回环 VL 同时兼任写入端（未配受管 binary 时 vlHTTPClient 就是它），
			// 因此流标签开关必须与受管路径同口径，否则两种部署的写入形态会不一致。
			StreamFields:        cfg.LogVL.StreamFields,
			DisableStreamFields: cfg.LogVL.DisableStreamFields,
		})
		if vlErr != nil {
			slog.Warn("VictoriaLogs 查询客户端配置无效，保持 LOG_UNSUPPORTED", "error", vlErr)
		} else {
			healthCtx, cancelHealth := context.WithTimeout(context.Background(), 3*time.Second)
			healthErr := vlClient.Health(healthCtx)
			cancelHealth()
			if healthErr != nil {
				slog.Warn("VictoriaLogs 未就绪，保持 LOG_UNSUPPORTED", "error", healthErr)
			} else if client, clientErr := vlrange.New(vlClient); clientErr != nil {
				slog.Warn("VictoriaLogs RangeClient 创建失败，保持 LOG_UNSUPPORTED", "error", clientErr)
			} else {
				rangeClient = client
				vlHTTPClient = vlClient
				slog.Info("VictoriaLogs RangeClient 已接线", "baseURL", vlClient.BaseURL())
			}
		}
	}
	// VL 流标签生效形态（启动时打一次）。
	//
	// 为什么值得占一行启动日志：流是**分区键**，启用它会在 VL 里留下永久的 `_stream` 形态；
	// 而切换点之前写入的历史数据 `_stream` 是空串，不会被自动迁移。运维将来一定会问
	// 「为什么查询里有的日志没有 stream 标签？」，没有这条线索就只能当故障排查。
	// 它只出现一次（不是每轮采集都打），不存在噪声问题。
	logStreamFieldsState(cfg.LogVL.DisableStreamFields, cfg.LogVL.StreamFields)

	logBudgetRoots := []string{root.Abs("var/log")}
	if strings.TrimSpace(cfg.LogVL.DataRoot) != "" {
		logBudgetRoots = append(logBudgetRoots, cfg.LogVL.DataRoot)
	}
	// 先获得有界采样，昂贵后台工作才有准入依据；之后只更新同一控制器。
	externalHot := vlSupervisor == nil && vlHTTPClient != nil
	resourceBudget.Update(sampleLogResources(vlSupervisor, logBudgetRoots, externalHot))
	slog.Info("日志资源预算准入已装配；零全局上界只计量，Go 软限制不是 RSS 硬限",
		"diskBudgetBytes", budgetConfig.DiskBytes, "rssBudgetBytes", budgetConfig.RSSBytes,
		"namespaceBudgets", budgetConfig.Namespaces, "reservationBudgets", budgetConfig.Reservations)
	go sampleLogBudget(logRuntimeCtx, vlSupervisor, resourceBudget, logBudgetRoots, externalHot)
	var logJournal catalog.Journal
	// logJournalCloser 负责关停时把 journal 最后一攒批记录落盘（nil = 纯内存形态，无可关闭）。
	var logJournalCloser func() error
	// 日志容量读数（磁盘使用率 / WAL 上限）的**唯一**构造点：采集侧容量门禁与
	// 保留策略的磁盘触发共用同一份提供者。两者各采一次磁盘迟早会给出不同结论，
	// 而现场只会表现成「有时降级有时不降级」，极难归因。
	if notice := cfg.WALBudgetNotice(); notice != "" {
		slog.Warn(notice)
	}
	// 未闭合多行缓冲超时与整节点解算预算都已随旧链路删除：
	// 前者是旧 normalize 的多行缓冲概念（新采集器在 tailer 层用 multiline.timeout 处理），
	// 后者服务的是旧链路的缺口解算（新架构没有缺口记账）。
	// 磁盘使用率读数（保留策略的磁盘触发用）。
	//
	// 原由 `ingest.DiskCapacityProvider` 提供（返回整个旧链路的 CapacityBudget）。
	// 保留策略只用到「用了百分之几」这一个数字，所以这里只提供那个数字——
	// 为一个字段传一整个已废弃的模型会让两边的概念永久绑在一起。
	logCapacityProvider := newDiskUsageProvider(root.Base())
	// 保留策略驱动器（G6）。声明在这里（而非 lifecycle 块内）是因为它要在采集运行时
	// 启动之后才有意义：搬运的是已经落库的分区，采集还没起来时搬没有意义。
	var logRetentionDriver *retention.Driver
	// 归档对象自身的回收（的最后一块）：把「归档目的地里的副本」按保留期清掉。
	// 声明在驱动器之外，是因为它由两个独立开关共同决定（保留策略总开关 + 本项开关），
	// 且必须与 /metrics 的读数出口共用同一个实例。
	var logArchivePurge *retention.ArchiveObjectReclaimer
	// 日志 Catalog journal：**纯内存**（2026-11-05 架构决策 ADR-102）。
	//
	// # 历史沿革（读到这里的人需要知道为什么绕了一圈）
	//
	// 2026-10-30 裁定纯内存，理由是「分区记录是可随时重建的派生状态」。
	// 2026-11-02 简化投递路径后那前提失效（记录改由 `ensureCatalogPartition` 只在
	// 投递成功时补建），于是 2026-11-05 早上改成 JSONL 落盘来保住历史天。
	//
	// 但落盘只保得住「写进去过的」：生产 journal 只写到 10-30，11-01/11-02 从没写入
	// ⇒ 那两天照样查不到。于是又加了「启动期从 VL 反推 30 天」的第三层补救。
	//
	// **三层补救的共同病根是「我们维护了一份 VL 事实的副本」**。
	//
	// # 现在为什么又回到纯内存
	//
	// 分区目录改由 VictoriaLogs 的 `hits` 一次请求重建（实测 428 个 (源,天) / 206ms）。
	// 副本被「直接问真源」取代后，落盘这层就没有存在理由了：
	//
	//   - 它维护的副本已被 hits 取代；
	//   - 它顺带承担的「租约旁路文件」已明确降级为可用性辅助（视图 TTL 到期即失效，
	//     重启后旧视图 ID 本就不该复活）；
	//   - `ReplayJournal` 只读内存 entries，不依赖落盘。
	//
	// 代价：重启后约 30s 窗口内（hits 重建启动前）历史天报「无分区记录」。
	// 这是**短暂降级**，换掉了「副本可能长期与真源不一致」这个更危险的风险。
	logJournal, logJournalCloser = openLogCatalogJournal(cfg, root, logJournal)
	if logJournalCloser != nil {
		defer func() {
			if closeErr := logJournalCloser(); closeErr != nil {
				slog.Warn("日志 Catalog journal 退出落盘失败（最后一批分区记录可能未持久）", "error", closeErr)
			}
		}()
	}
	logStack := logassemble.Build(logJournal, rangeClient, version.Version)
	logStack.LogRPC.SetResourceBudget(resourceBudget)
	defer func() {
		if closeErr := logStack.LogRPC.Close(); closeErr != nil {
			slog.Warn("日志查询视图退出清理失败", "error", closeErr)
		}
	}()
	logStack.LogRPC.SetRuntimeCatalog(logStack.Catalog)
	// 先完成 Catalog/journal 逻辑恢复，再开放 Log RPC；物理目录残留
	// 不能替代 owner/generation 权威。不可查询范围保持 recovery-required，
	// 不阻塞 Worker 其它实例服务。
	for key, recovery := range logStack.Catalog.StartingRecover() {
		if !recovery.Queryable {
			slog.Warn("日志分区启动恢复未完成，查询范围降级", "partition", key.String(), "reasons", recovery.PartialReasons)
		}
	}
	// 投影 query_generation 与权威 generation 的一致性自愈（2026-10-21 生产既成损害）。
	//
	// 为什么要做：`resetNamespaceConflictCounts` 的旧实现用无 CAS 的 AppendJournal 做派生
	// 计数对齐，把 Generation 自增 1 却不动 PublishedProjection.QueryGeneration，造出一批
	// `generation=2 ∧ query_generation=1` 的分区（生产 inst:320 实测 28 个，覆盖 09-20～10-19
	// 的活跃日期）。这类分区**永远无法再发布**（`PublishProjection` 一进门就
	// `projection and authority do not match`），采集侧的补投/重建因此恒失败。
	//
	// 为什么放在这里：必须在 journal 逻辑恢复完成后（否则读到的是中间态）、在采集/查询
	// 对外服务之前（否则首轮发布仍会撞前置校验）。修复本身是带 CAS 的单条提交，幂等，
	// 并发下失败即放弃、下轮再来 —— 不阻塞启动，也不假装成功。
	if repaired, failed, needsManual := repairProjectionQueryGenerations(logStack.Catalog); repaired > 0 || failed > 0 || len(needsManual) > 0 {
		if repaired > 0 || failed > 0 {
			slog.Warn("投影代次一致性自愈完成（历史缺陷的既成损害修复）",
				"repaired", repaired, "failed", failed)
		}
		// 需人工处置的分区**单独**一条 WARN（2026-10-21 复审修正）：这些分区既没被修好、
		// 也没返回 error，若与上面那条汇总混在一起或干脆不报，运维看到的就是「自愈完成」，
		// 而实际上它们永久不可发布（PublishProjection 恒拒），且没有任何别的入口能发现。
		if len(needsManual) > 0 {
			sample := needsManual
			if len(sample) > maxNeedsManualPartitionsLogged {
				sample = sample[:maxNeedsManualPartitionsLogged]
			}
			slog.Warn("投影代次一致性自愈：存在需人工处置的分区（未自动修复，也不会自愈）",
				"count", len(needsManual), "partitions", sample,
				"truncated", len(needsManual) > len(sample))
		}
	}
	// 已归档但热层尚未回收的分区（回收闭环的可观测面）。
	//
	// 为什么会存在这种状态：权威态先改 ARCHIVED、再删热层。若进程恰在两步之间退出，
	// 就会留下一份「数据安全、盘没省下来」的残留副本。这是**安全方向**的不一致
	// （数据多一份而不是少一份），但必须有人知道——否则它永远不会被清理。
	// 这里只报出来，不自动重试：自动删除必须重新过三闸与 Catalog 复核，
	// 由保留策略的下一轮驱动承担（那是唯一持有完整判据的地方）。
	if pending := logStack.Catalog.PendingArchiveReclaims(); len(pending) > 0 {
		sample := pending
		if len(sample) > maxNeedsManualPartitionsLogged {
			sample = sample[:maxNeedsManualPartitionsLogged]
		}
		keys := make([]string, 0, len(sample))
		for _, key := range sample {
			keys = append(keys, key.String())
		}
		slog.Warn("存在已归档但热层未回收的分区（数据安全、磁盘未释放；下一轮保留策略会按三闸重试）",
			"count", len(pending), "partitions", keys, "truncated", len(pending) > len(sample))
	}
	if vlHTTPClient != nil && vlSupervisor != nil {
		hotCfg, hotErr := vlSupervisor.Config(vlsup.NamespaceHot)
		if hotErr != nil {
			slog.Error("日志 Lifecycle namespace 配置不可用", "error", hotErr)
		} else if physical, lifecycleErr := lifecycle.NewVLPartitionOps(vlHTTPClient,
			hotCfg.StorageDataPath, logStack.Catalog); lifecycleErr != nil {
			slog.Error("日志 Lifecycle 物理适配器创建失败", "error", lifecycleErr)
		} else {
			physical.ResourceBudget = resourceBudget
			// 2026-10-29：不再构造 DayManager / CanonicalDayInput —— 它们只服务冷层搬迁，
			// 而搬迁已随冷热分层一起删除（见 lifecycle 包注释）。
			// 保留策略驱动器（G6）：到期分区要么导出独立副本、要么如实落 blocked 保留原物。
			//
			// 2026-10-29：冷热双层已废除 ⇒ **不再装配 mover**（第三个参数传 nil）。
			// `retention.Policy.PlanArchive` 的动作序会自动落到：
			//   配了导出 → archive_copy；否则 → blocked（保留原物，不删）。
			// 这正是单 VL 想要的语义，不需要改 Policy 本身。
			if retentionPolicy, policyErr := cfg.RetentionPolicy(); policyErr != nil {
				slog.Error("日志保留策略非法，保留驱动未启用", "error", policyErr)
			} else if retentionPolicy.Enabled {
				logRetentionDriver = retention.NewDriver(retentionPolicy,
					newCatalogPartitionLister(logStack.Catalog),
					nil,
					newDiskPercentReader(logCapacityProvider))
				// 导出归档（，硬规则的次选路径）：搬不了冷层时导出为独立副本。
				//
				// 默认关闭（`log_retention.archive_copy.enabled=false`）。这里**不做**任何
				// 默认兜底目录：没有目的地就不装出口，计划阶段于是不会产出 archive_copy，
				// 到期分区继续落 blocked（保留原物）——与本次改动之前的行为完全一致。
				if retentionPolicy.ArchiveCopy.Enabled {
					// 目的地的三种形态（local / s3 / minio）都在 newArchiveCopyProvider 里构造，
					// 失败一律**只降级导出器**、不让 Worker 启动失败：拿不到目的地时正确的行为
					// 是「不出副本、保留原物」，而不是「整个节点起不来」。方向与本次改动之前
					// 「开关开了但没目的地」那条分支完全一致（blocked + 保留原物）。
					provider, providerErr := newArchiveCopyProvider(cfg.LogRetention.ArchiveCopy)
					if providerErr != nil {
						slog.Error("导出归档目的地构造失败，归档出口未装配（到期分区将继续保留原物）",
							"provider", cfg.LogRetention.ArchiveCopy.ArchiveProviderKind(), "error", providerErr)
					} else {
						// 导出器住在 logs/archive（manifest/Provider 契约的家），
						// 但「冻结快照怎么取、能读哪里」属于物理布局，仍由 lifecycle 提供，
						// 在这里注入——依赖方向因此保持单向：lifecycle 不认识 archive。
						exporter := archive.NewArchiveCopyExporter(logStack.Catalog, physical.ExportSnapshot, provider)
						logRetentionDriver.Executor().SetArchiveExporter(retention.ArchiveExporterFunc(
							func(ctx context.Context, namespace, day string) (retention.ArchiveExportResult, error) {
								sum, err := exporter.Export(ctx, namespace, day)
								if err != nil {
									return retention.ArchiveExportResult{}, err
								}
								return retention.ArchiveExportResult{
									ObjectCount:      sum.ObjectCount,
									SourceBytes:      sum.SourceBytes,
									StoredBytes:      sum.StoredBytes,
									Location:         sum.Location,
									ProviderKind:     sum.ProviderKind,
									ObjectPrefix:     sum.ObjectPrefix,
									ManifestChecksum: sum.ManifestChecksum,
									CoverageComplete: sum.CoverageComplete,
								}, nil
							}))
						// 「已归档」权威态迁移出口 + 热层回收出口（回收热层闭环）。
						//
						// 三个出口必须成套装配才有回收：导出证明「副本可信」、authority 证明
						// 「Catalog 已承认正本在归档」、remover 才允许真的删。缺 authority 时
						// 执行器会拒绝删除（否则查询面会指向一个不存在的目录），这是硬判据。
						//
						// 三闸（discard + sweep.vl_sweep + log_vl.delete_enabled）仍然默认全关：
						// 装配这几个出口**不等于**允许删，删不删由 Policy.DeleteGatesOpen 决定。
						logRetentionDriver.Executor().SetArchiveAuthority(newCatalogArchiveAuthority(logStack.Catalog))
						logRetentionDriver.Executor().SetRemover(retention.RemoverFunc(physical.RemoveArchivedPartition))
						if ok, reason := logRetentionDriver.Executor().ArchiveReadyForReclaim(); !ok {
							slog.Info("日志归档热层回收未就绪（导出仍会执行，原物保留）", "reason", reason)
						} else {
							slog.Warn("日志归档热层回收已就绪：三闸全开时将删除已归档分区的热层数据",
								"provider", provider.Kind())
						}
						// 日志里只打 kind 与「本地根目录」；s3/minio 的 endpoint/bucket 也不打
						// 是刻意的：现场日志常被导出传阅，少一个泄漏面。
						slog.Info("日志导出归档已装配（搬不了冷层时导出为独立副本；回收热层仍需删除三闸全开）",
							"provider", provider.Kind(), "root", strings.TrimSpace(cfg.LogRetention.ArchiveCopy.Root))
					}
				}
				// 归档对象自身的回收：把归档目的地里**已过保留期**的副本删掉。
				//
				// 装配条件是「本项生效 **且** 有归档根」，**不**要求 archive_copy.enabled：
				// 「停止写新归档，但仍要清理旧归档」是一个真实且合理的运维动作
				// （例如归档盘快满了、或已经决定不再新增归档）。若把它绑在导出开关上，
				// 那个动作就只能靠「继续导出 + 同时清理」来实现——反而多写数据。
				// 注意**反方向不成立**：开了导出不等于授权清除（删除比写入危险得多），
				// 清除仍要过同一套三闸。
				//
				// 三个出口必须成套：lister 提供候选、authority 提供「已清除」权威态、
				// store 才是真正删除对象的那一步。任一缺失都让 reclaimer 为空，
				// 于是启动日志会明确写出「未装配」——而不是一个永远不动的计数器。
				if retentionPolicy.ArchiveObjectRetention.Active() {
					// 目的地与导出侧**同源**：用同一个构造器（newArchiveCopyProvider）
					// 按 `archive_copy.provider` 选 local / s3 / minio。
					//
					// 此前这里硬编码 `archive.NewLocalArchive(root)`，于是配了 s3 导出
					// 的人在回收侧拿不到 provider，只能如实拒绝——「导出已能写 s3、却永远
					// 不会清理 s3」就是这么来的。
					//
					// 注意：这里**不复用**导出器那个 provider 实例（两者不同 goroutine，
					// 各自持有自己的连接状态更安全），但必须是**同一个目的地配置**。
					provider, providerErr := newArchiveCopyProvider(cfg.LogRetention.ArchiveCopy)
					if providerErr != nil {
						slog.Warn("归档对象回收目的地构造失败，回收未装配（归档副本将继续累积）",
							"provider", cfg.LogRetention.ArchiveCopy.ArchiveProviderKind(), "error", providerErr)
					} else {
						// root 只对本地目录有意义（清空目录骨架）；对象存储传空串。
						root := ""
						if provider.Kind() == archive.KindLocal {
							root = strings.TrimSpace(cfg.LogRetention.ArchiveCopy.Root)
						}
						logArchivePurge = newArchiveObjectReclaimer(retentionPolicy, logStack.Catalog, provider, root)
						if logArchivePurge == nil {
							slog.Warn("归档对象回收已启用但出口装配失败（将在 /metrics 与下方日志中体现为零行动）",
								"provider", provider.Kind())
						} else if ok, reason := logArchivePurge.Ready(); !ok {
							slog.Warn("归档对象回收出口不完整，本轮不会删除任何对象", "reason", reason)
						} else if open, gateReason := retentionPolicy.DeleteGatesOpen(); !open {
							slog.Info("归档对象回收已装配但删除三闸未全开（一个对象都不会删）", "reason", gateReason)
						} else {
							slog.Warn("归档对象回收已就绪：三闸全开时将按保留期删除归档目的地里的副本",
								"provider", provider.Kind(), "root", root,
								"retention", retentionPolicy.ArchiveObjectRetention.Retention.String(),
								"archiveCopyEnabled", retentionPolicy.ArchiveCopy.Enabled)
						}
					}
				}
				slog.Info("日志保留策略已装配（默认只搬运不删除）", retentionPolicyLogFields(retentionPolicy)...)
			}
			// 2026-10-29：分区下沉（hot→cold）能力已随冷热双层一起删除。
			// 这里**显式**装一个明确拒绝的出口，而不是不装配：
			// 不装配时 RPC 返回「log partition migration is not configured」，
			// 运维会以为是漏配了某项；装配一个说出真实原因的拒绝，才能让人知道
			// 「这个动作在新架构里不存在了」，而不是去翻配置找开关。
			logStack.LogRPC.SetPartitionMigrator(grpcsvc.PartitionMigrateFunc(func(ctx context.Context, namespace, day, target string) error {
				return fmt.Errorf("分区下沉已废弃：单个 VictoriaLogs 实例既是热层也是长期层，"+
					"没有第二层可搬（storage_namespace=%s utc_day=%s target_tier=%s）", namespace, day, target)
			}))
		}
	}
	if vlSupervisor != nil {
		logStack.LogRPC.SetRuntimeController(vlSupervisor)
		if cpHost, _, splitErr := net.SplitHostPort(cfg.ControlPlane); splitErr == nil {
			logStack.LogRPC.SetRuntimeInstaller(grpcsvc.RuntimeInstallFunc(func(ctx context.Context, packageURL, packageSHA string) error {
				path, err := vlsup.InstallApprovedURL(ctx, packageURL, packageSHA, root.Abs("var/log/vl-assets"),
					cpHost, runtime.GOOS, runtime.GOARCH)
				if err != nil {
					return err
				}
				return vlSupervisor.ReplaceBinary(ctx, path, vlsup.CurrentApprovedExeSHA256())
			}))
		} else {
			slog.Error("日志资产 CP 主机无效，禁用远程安装", "error", splitErr)
		}
	}
	workerServer.SetLogQueryService(logStack.LogRPC)
	// 归档 Registry/Rehydrate 进入同一 Worker 查询面。凭据只来自
	// 环境覆盖的配置对象，不写入日志或诊断响应。
	var archiveRegistry *archive.Registry
	if cfg.LogArchive.Enabled {
		var provider archive.Provider
		archiveCfg := cfg.LogArchive
		archiveStorageCfg := workerstorage.Config{
			Type:     workerstorage.TypeS3,
			Endpoint: archiveCfg.Endpoint, Bucket: archiveCfg.Bucket, Region: archiveCfg.Region,
			Prefix: archiveCfg.Prefix, AccessKey: archiveCfg.AccessKey, SecretKey: archiveCfg.SecretKey,
		}
		if os.Getenv("JIANMANAGER_LOG_ARCHIVE_PROBE") == "1" && strings.EqualFold(archiveCfg.Provider, "s3") {
			if probeErr := workerstorage.Probe(context.Background(), archiveStorageCfg); probeErr != nil {
				slog.Error("日志归档对象存储探测失败", "error", probeErr)
			} else {
				slog.Info("日志归档对象存储探测通过", "provider", archiveCfg.Provider)
			}
		}
		switch strings.ToLower(strings.TrimSpace(archiveCfg.Provider)) {
		case "s3", "minio":
			remote, archiveErr := archive.NewRemoteS3Provider(archiveStorageCfg)
			if archiveErr != nil {
				slog.Error("日志归档 S3 Provider 初始化失败", "error", archiveErr)
			} else {
				provider = remote
			}
		default:
			provider = archive.NewLocalArchive(root.Abs("var/log/archive"))
		}
		if provider != nil {
			registry := archive.NewRegistry(provider)
			// 归档 manifest 记录受管 VL 的真实构建标识（资产审批），而非占位。
			if vlsup.AssetBuildID != "" {
				registry.SetEngineVersion("victorialogs/" + vlsup.AssetBuildID)
			}
			archiveRegistry = registry
			rehydrate := archive.NewRehydrateManager(archive.RehydrateOptions{
				DefaultTimeout: 5 * time.Minute, DefaultLeaseTTL: 5 * time.Minute,
			})
			// rehydrate 实例直接交给 query backend（下方 backend）；旧链路的 RecoveryHold 回调已删。
			backend := archive.NewQueryBackend(registry, rehydrate)
			backend.SetResourceBudget(archive.RehydrateResourceBudget{
				Admit: func(ctx context.Context) error {
					if err := ctx.Err(); err != nil {
						return err
					}
					return resourceBudget.Admit(vlsup.WorkBackground)
				},
				ReserveStaging: func(ctx context.Context, bytes int64) (func(), error) {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					return resourceBudget.Reserve(vlsup.ReservationStaging, bytes)
				},
			})
			if vlHTTPClient != nil {
				backend.SetPublisher(readyRehydratePublisher{clients: managedClients,
					// Retention 与主实例同源：传进去让 rehydrate 能当场认出「这个分区日已超出
					// 目标 VL 保留期、注定写不进去」，而不是写一遍 200、verify 查不到、再报一个
					// 看似瞬时的失败。
					//
					// 2026-10-29：回灌目标从 Rehydrate 层改为主实例 —— 已没有第二个实例，
					// 而归档还原的语义是「把归档副本重新变成可查」，落在主实例上正是它该做的。
					publisher: &archive.VLRehydratePublisher{Catalog: logStack.Catalog, VL: vlHTTPClient, Retention: resolveHotRetention(cfg)}})
			}
			logStack.SetArchiveBackend(backend)
			slog.Info("日志归档运行时已装配", "provider", provider.Kind())
		}
	}
	// 新采集器（tailer/logtail）。旧链路已删除，不存在「两者互斥」的问题。
	var logTailRunner *logtail.Runner
	// /504 的采集策略（含定时归档导入扫描周期）已随旧链路删除：
	// 新采集器不读这些策略——路径来自 CP 下发的实例登记，gzip 由 `log_tailer.include_gzip` 决定。
	// 新采集器（tailer/logtail，阶段二并行接入）：开关打开时走它，**不构造** ingest.Manager。
	//
	// # 为何必须互斥
	//
	// 两套采集器会读同一批文件（旧链路按 log_sources / 登记源读，新采集器按 glob 读），
	// 同一行日志进 VL 两次。event_id 相同不保证 VL 去重（那只是我们的内容指纹字段），
	// 所以必须从装配层就保证只开一套。
	if cfg.LogTailer.Enabled {
		scanIv, progressIv, ivErr := cfg.LogTailerIntervals()
		if ivErr != nil {
			// 已在 config.Load 拦过一次；这里兜底不静默。
			slog.Error("log_tailer 采集周期非法，新采集器不启动", "error", ivErr)
			register.SetLogIngestHealth(false, ivErr.Error())
		} else {
			// 实例日志的采集路径**不在这里推导**：它由绑定表（CP 下发的���例登记）每轮现算。
			// 在这里猜一个 serversRoot 推 glob 是错的——真机实测生产实例位于
			// /home/user/servers，而 dataRoot/var/servers 里 0 个实例目录，
			// 按后者扫就是“静默采不到任何东西”。
			paths := cfg.LogTailer.Paths
			// 时区：与旧链路共用同一个节点级默认（键 log_ingest.time_zone）。
			// 为什么复用而不是新开一个键：它表达的是「这台机器上的 JVM 写的是哪个时区的时间」，
			// 与用哪个采集器无关；两个键会立刻出现在同一部署上配得不一致。
			loc, tzErr := logconf.ParseTimeZone(cfg.IngestDefaultTimeZone())
			if tzErr != nil {
				slog.Error("日志时区非法，新采集器按 UTC 启动（时间轴可能整体偏移）", "error", tzErr)
				loc = nil
			}
			// 多行规则（键 log_tailer.multiline.*）：非法值已在 config.Load 拦过，
			// 这里再取一次是为了拿到编译好的规则对象。
			multilineRule, multilineTimeout, mlErr := cfg.MultilineSettings()
			if mlErr != nil {
				slog.Error("多行规则非法，本次按「不合并」启动（堆栈会被拆成多行）", "error", mlErr)
				multilineRule, multilineTimeout = nil, 0
			}
			if multilineRule != nil {
				// 打**生效**的规则，不是原始配置值。
				//
				// 默认开启后，没配 pattern 的部署里 `cfg.LogTailer.Multiline.Pattern` 是空串，
				// 而实际跑的是 DefaultMultilinePattern。照原样打会把"已启用"和"pattern 为空"
				// 并排打在同一行，读日志的人会以为规则没生效（2026-11-07 部署时就这样误解过）。
				// 规则来自编译后的对象，它才是真源。
				slog.Info("日志多行合并已启用（对齐 Filebeat parsers.multiline）",
					"pattern", multilineRule.Pattern(),
					"negate", multilineRule.Negate(),
					"match", multilineRule.Match(),
					"maxLines", multilineRule.MaxLines(),
					"source", multilineRuleSource(cfg))
			}
			// stdio 落盘目录：必须放在数据根下（随数据根一起迁移/清理），
			// 且必须在采集 glob 覆盖范围内——它自己就是一条采集路径。
			stdioRoot := root.LogDir()
			if stdioRoot != "" {
				stdioRoot = filepath.Join(stdioRoot, "stdio")
			}
			runner, startErr := logtail.Start(context.Background(), logtail.Config{
				Enabled:          true,
				Paths:            paths,
				Exclude:          cfg.LogTailer.Exclude,
				ReadFromHead:     cfg.LogTailer.ReadFromHead,
				RegistryPath:     logtail.ResolveRegistryPath(root.Base(), cfg.LogTailer.RegistryPath),
				ScanInterval:     scanIv,
				ProgressInterval: progressIv,
				StdioRoot:        stdioRoot,
				TimeZone:         loc,
				Charset:          cfg.IngestDefaultCharset(),
				Multiline:        multilineRule,
				MultilineTimeout: multilineTimeout,
				IncludeGzip:      cfg.LogTailer.IncludeGzip,
				Catalog:          logStack.Catalog,
				// 启动期分区重建：向 VL 问「哪些 (源,天) 有数据」，把「VL 有行但 Catalog 没记录」
				// 的天补回来。
				//
				// # 为何必须接
				//
				// Catalog journal 自 42a1b728 起是**纯内存**的（有意取舍：避免副本与真源不一致）。
				// 代价是重启后分区目录从空开始——设计上应由 hits 重建补齐。
				// 但两者都不接就会出现双重真空：重启后既无 journal、也不重建，
				// **历史天在查询侧永久变成「不存在」**（VL 里行还在，没人知道去查）。
				// 2026-11-07 部署时实测确认了这个状态（日志：「未接 hits 数据源」）。
				HitsSource: hitsSourceAdapter(vlHTTPClient),
			}, vlHTTPClient, nil)
			if startErr != nil {
				slog.Error("新日志采集器启动失败（本次启动不采集，需告警介入）", "error", startErr)
				register.SetLogIngestHealth(false, startErr.Error())
			} else {
				logTailRunner = runner
				register.SetLogIngestHealth(true, "")
				// 登记入口：CP 下发的实例日志绑定写进新采集器的绑定表。
				// 只有这里知道 `inst:<id>` 与工作目录的对应关系。
				workerServer.SetInstanceLogCollector(logTailCollector{runner: runner})
				defer runner.Stop()
				// 周期性采集读数：新采集器的典型故障是「静默无数据」（路径推导错、
				// 绑定没到达、VL 拒收），三者都不报错。没有读数就只能猜。
				tailStatsCtx, cancelTailStats := context.WithCancel(context.Background())
				defer cancelTailStats()
				go sampleLogTailStats(tailStatsCtx, runner, time.Minute)
				stats := runner.Stats()
				// 与采集器**无关**的生命周期驱动器（保留策略 / 归档回收 / 实例 gz 吸存）。
				// 它们原本住在旧采集块内；不迁到这里就会随旧链路一起消失，
				// 表现为“数据到期不淘汰、归档副本不回收”——静默且极难归因。
				logLifecycle{
					retentionDriver: logRetentionDriver,
					archivePurge:    logArchivePurge,
					archiveRegistry: archiveRegistry,
					procManager:     procManager,
					adoptCfg:        cfg.LogArchive.InstanceGzAdopt.Normalized(),
				}.run(context.Background())
				slog.Info("新日志采集器已接管采集（旧 ingest 链路本次不构造）",
					"lines", stats.Lines, "written", stats.Written)
				startup.Mark("logTailer")
			}
		}
	}
	// 旧采集链路已删除（阶段三）：采集只走 tailer/logtail。
	// 回退开关 `log_tailer.enabled: false` 随之失效——旧实现不在本版本里。
	if rangeClient == nil {
		slog.Info("Worker 日志查询面已装配", "buildId", version.Version, "rangeClient", "unimplemented-stub")
	}
	// Worker 升级二进制下载与服务端 jar 下载经进程级出站持有者：
	// CP 下发代理改动运行时即时生效。
	workerServer.SetHTTPClientProvider(outboundProvider.Client)
	// 非 JDK 运行时安装管理器（首批 Node.js）：托管根 <dataRoot>/opt/runtimes，
	// 下载经进程级出站持有者（代理改动运行时即时生效，同 JDK）。
	runtimeMgr := wruntime.NewManager(root.RuntimesDir())
	runtimeMgr.SetHTTPClientProvider(outboundProvider.Client)
	workerServer.SetRuntimeManager(runtimeMgr)
	// 运行时扫描器（节点运行时库）：ScanRuntimes 按常见安装路径发现 jdk/nodejs 候选；
	// JDK 托管根与运行时托管根传入，其下候选标 already_registered。
	var runtimeManagedRoots []string
	if jdkMgr != nil {
		runtimeManagedRoots = append(runtimeManagedRoots, jdkMgr.RootDir())
	}
	runtimeManagedRoots = append(runtimeManagedRoots, runtimeMgr.RootDir())
	runtimeScanner := runtimescan.New(runtimeManagedRoots)
	// ×胶合：托管安装根不在默认 node 路径表内，动态追加两种布局
	// （linux tar.gz 解出 bin/node；windows zip 顶层 node.exe），使一键安装的托管 Node
	// 能被扫描发现并被 Bot spawn 解析器选中。
	runtimeScanner.AddNodeGlobs(
		filepath.Join(runtimeMgr.RootDir(), "nodejs-*", "bin", "node"),
		filepath.Join(runtimeMgr.RootDir(), "nodejs-*", "*", "bin", "node"),
		filepath.Join(runtimeMgr.RootDir(), "nodejs-*", "node.exe"),
		filepath.Join(runtimeMgr.RootDir(), "nodejs-*", "*", "node.exe"),
	)
	workerServer.SetRuntimeScanner(runtimeScanner)
	// 包管理器：托管 .npmrc 与 corepack 激活落 opt/runtimes（与 nodejs-* 平级）。
	workerServer.SetPkgManager(pkgmgr.NewManager(runtimeMgr.RootDir()))
	// 全文搜索追加忽略规则（worker.yml search.ignore，叠加内置默认集）。
	workerServer.SetSearchIgnore(cfg.Search.Ignore)
	// 节点制品缓存：按 sha256 缓存下载过的核心 jar（var/artifact-cache），建实例命中即秒拷免重下。
	// 容量上限来自 worker.yml artifact_cache.max_bytes（0=不限），可经 CP 端点运行时下发覆盖。
	artifactCache := artifactcache.New(root.ArtifactCacheDir())
	artifactCache.SetCap(cfg.ArtifactCache.MaxBytes)
	workerServer.SetArtifactCache(artifactCache)
	slog.Info("节点制品缓存已启用", "dir", root.ArtifactCacheDir(), "maxBytes", cfg.ArtifactCache.MaxBytes)

	// Bot 管理器：按需 spawn bot-worker(Node) 子进程，经 stdin/stdout IPC 管理 Mineflayer Bot。
	// 入口脚本解析顺序（见 ADR-072迁移见 ADR-064）：
	// JIANMANAGER_BOT_WORKER_PATH 显式覆盖 > 数据根自愈物化副本（注册后拉取，见下方
	// botdist.Ensure）> apps/bot-worker/dist/index.js（仓库内直跑新布局）> 旧相对路径
	// bot-worker/dist/index.js（存量部署把 bot-worker 平铺在安装目录，不因升级断 Bot 能力）。
	// NodeResolver 对显式/托管/PATH 全部真实探测，强制 >=22.13.0；托管候选按完整版本排序。
	botWorkerPath := os.Getenv("JIANMANAGER_BOT_WORKER_PATH")
	botWorkerPathPinned := botWorkerPath != ""
	if botWorkerPath == "" {
		botWorkerPath = filepath.Join("apps", "bot-worker", "dist", "index.js")
		if _, statErr := os.Stat(botWorkerPath); statErr != nil {
			legacy := filepath.Join("bot-worker", "dist", "index.js")
			if _, legacyErr := os.Stat(legacy); legacyErr == nil {
				botWorkerPath = legacy
			}
		}
	}
	// mineflayer 等运行时依赖不随 dist 分发。NODE_PATH 只兜底 CJS；ESM 由受控 dist
	// 同级 node_modules 链接承载，且每次 spawn 前按新旧根完整性刷新后再预检。
	globalNM := botdist.GlobalNodeModulesCandidates(runtimeMgr.RootDir())
	managedBotWorkerDir := root.BotWorkerDir()
	// 308：扫描只提供候选路径，NodeResolver 会逐个真实探测并强制 >=22.13.0。
	//
	// 分片（shards）：单 Node 进程承载 bot 有硬上限——实测 272 bot 时主线程 99.9% CPU、
	// RSS 9.2GB，事件循环排不上 10s 心跳，stdout 停更致控制面判定容量快照过期。
	// 故按 shards 起多个 bot-worker 进程，每片承载 ceil(max_bots/shards)。
	// shards<=1 时行为与旧版单进程完全一致（可随时回退）。
	newBotWorkerMgr := func(perShardMax int) *bot.Manager {
		return bot.NewManager(bot.ManagerConfig{
			BotWorkerPath: botWorkerPath,
			NodeResolver: bot.NewNodeResolver("", func() []runtimescan.Candidate {
				return runtimeScanner.Scan([]string{runtimescan.TypeNodeJS})
			}),
			ExtraEnv: append([]string{botdist.NodePathEnv(globalNM)}, cfg.BotWorker.ApplyEnvForShard(perShardMax)...),
			PrepareSpawn: func(distDir string) error {
				if filepath.Clean(distDir) != filepath.Clean(managedBotWorkerDir) {
					return nil
				}
				return botdist.RefreshNodeModulesLink(distDir, runtimeMgr.RootDir())
			},
			DepsPrecheck: botdist.CheckDeps,
		})
	}
	shardCfg := cfg.BotWorker.Normalize()
	botMgr := bot.NewShardedManager(bot.ShardedManagerConfig{
		Shards:   shardCfg.Shards,
		MaxBots:  shardCfg.MaxBots,
		NewShard: func(_ int, perShardMax int) *bot.Manager { return newBotWorkerMgr(perShardMax) },
	})
	defer botMgr.Stop()
	if n := botMgr.ShardCount(); n > 1 {
		slog.Info("Bot Worker 已启用多进程分片",
			"shards", n, "totalMaxBots", shardCfg.MaxBots, "perShardMaxBots", (shardCfg.MaxBots+n-1)/n)
	}
	workerServer.SetBotManager(botMgr)

	// 反编译器（见 ADR-018）：解析 CFR jar（配置路径>内嵌>数据根缓存>按需下载 sha256 pin），
	// 缓存落数据根 cache/tools；反编译经实例/系统 JDK 受控调起 CFR，只读+超时+体积上限+失败降级。
	decompProvider := decompiler.NewProvider(decompiler.Config{
		ConfigPath:    cfg.Decompiler.CFRPath,
		CacheDir:      filepath.Join(root.CacheDir(), "tools"),
		Embedded:      wembed.CFRJar,
		AllowDownload: cfg.Decompiler.AllowDownload,
		// CFR 按需下载经进程级出站持有者：CP 下发代理改动运行时即时生效。
		HTTPClientProvider: outboundProvider.Client,
	})
	workerServer.SetDecompiler(decompProvider)

	// 终端会话隧道桥（M2，见 ADR-066）：TerminalSession 回环拨本机 WS 终端服务，
	// 令牌校验/会话层单一真源仍是 ws.TerminalServer。
	workerServer.SetTerminalWSAddr(fmt.Sprintf("ws://127.0.0.1:%d/ws/terminal", wsPort))

	// WS 服务在节点完成注册并取得非空专用密钥后才监听，避免以空密钥暴露入口。
	terminalServer := ws.NewTerminalServer(wsTokenSecret)

	// 桥接进程输出：一份给 WebSocket 终端（交互），一份给 StreamInstanceEvents 事件流（CP 采集落库）。
	// 两条路径相互独立，从同一份进程输出分流，互不阻塞。
	manager.SetOutputHandler(func(instanceID string, stream string, data []byte) {
		text := string(data)
		terminalServer.Broadcast(instanceID, stream, text)
		workerServer.EmitOutput(instanceID, stream, text)
		if logTailRunner != nil {
			// 新采集器的 stdio 入口：落成按天文件交给 tailer 采。
			//
			// FILE_PRIMARY（Minecraft Java）实例自己写 latest.log，进程输出与它是同一份，
			// 这里直接返回——否则同一条日志会在 VL 里出现两次。分流在 logTailCollector 里。
			if err := (logTailCollector{runner: logTailRunner}).AppendInstanceOutput(instanceID, stream, data); err != nil {
				slog.Error("实例进程输出落盘失败", "instanceId", instanceID, "stream", stream, "error", err)
			}
		}
	})

	// 缺陷修复：实例每次开始新一轮运行即重置其终端环形缓冲，使崩溃快照截取的
	// 尾部输出只含本次运行——否则上一轮的崩溃文本会残留并被分类器按优先级取走，把本次崩溃误判成
	// 上一轮的原因（真机实测：注入端口占用却因残留 OutOfMemoryError 行被判 oom）。
	manager.SetInstanceStartHandler(func(instanceID string) {
		terminalServer.ResetBuffer(instanceID)
	})

	// 桥接终端输入到进程 stdin
	terminalServer.SetStdinHandler(func(instanceID, data string) {
		if err := manager.SendCommand(instanceID, data); err != nil {
			slog.Warn("终端输入发送失败", "instanceId", instanceID, "error", err)
		}
	})

	// 插件桥服务端（ServerProbe 反向 WS见 ADR-016）：与终端 WS 并列、同一监听端口。
	// 探针主动连入 /ws/plugin-bridge，事件经 gRPC StreamPluginEvents 冒泡到 CP；token 校验使用 CP 下发的专用 WS 密钥。
	pluginBridge := ws.NewPluginBridgeServer(wsTokenSecret)
	// 托管模式（ServerProbe PR#64）：探针在零监听承诺下不再开 /metrics，
	// 指标改经桥周期推 metric_snapshot。这里把推送帧接进心跳采集使用的同一份存储。
	//
	// 不接的后果是真机实测过的：桥连上了、config_push 也生效，但面板上
	// TPS / MSPT / 玩家数 / 堆内存永远是 0——托管了个“看不到”的服。
	pluginBridge.SetMetricSnapshotHandler(func(f ws.MetricSnapshotFrame) {
		snap, ok := metrics.ProbeSnapshotFromPushed(f.Metrics)
		if !ok {
			slog.Debug("托管指标帧解析失败，丢弃本帧", "instanceId", f.InstanceUUID, "ts", f.TS)
			return
		}
		heartbeat.PushedSnapshotStore().Store(f.InstanceUUID, snap)
	})
	workerServer.SetPluginBridge(pluginBridge)

	wsMux := http.NewServeMux()
	// 冻结取证（P0 复盘 2026-10-03 ✓）：pprof 挂在**同一个 127.0.0.1 绑定的 mux** 上 ⇒ **不新增暴露面** ✓。
	// 用途：① 冻结瞬间取 goroutine 栈（谁持 m.mu、卡在哪行、是否在 GC ✓）；② 堆剖面（稳态/空闲内存增长取证 ✓）。
	// 注意：该面**无令牌**（与终端 WS 不同 ✗）⇒ 仅本机回环可达 ✓；如需更强，后续加令牌门（改进项 ✓）。
	wsMux.HandleFunc("/debug/pprof/", pprof.Index)
	wsMux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	wsMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	wsMux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	wsMux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	wsMux.HandleFunc("/ws/terminal", terminalServer.Handler())
	wsMux.HandleFunc("/ws/plugin-bridge", pluginBridge.Handler())
	// 日志保留策略的读数端点（Prometheus 文本格式）。
	// 挂载位置与 pprof 同口径：同一个 **127.0.0.1 绑定**的 mux ⇒ **不新增暴露面** ✓
	// （wsAddr 由 localWSAddr 固定回环，见下）。要跨机抓取请走 SSH 隧道或节点上的本地
	// exporter，别把这个 mux 绑到 0.0.0.0——它没有令牌门，与 pprof 一样只靠回环隔离。
	// 取舍说明：读数只有 8 个标量，故手写文本格式而不引 prometheus/client_golang（见
	// retention.MetricsHandler 的注释）；logRetentionDriver 可能为 nil（保留策略未启用），
	// 处理器照样输出全零序列 + jm_log_retention_driver_enabled 0，抓取侧不必写特殊分支。
	// 事件段回收读数同理由 logIngest 提供：它可能为 nil（采集运行时创建失败），此时
	// 回收序列输出 0、jm_log_eventstore_prune_enabled 0，同样是「全零而非缺序列」。
	//
	// 这里必须先判 nil 再作接口实参：logIngest 是 *ingest.Manager，直接把 nil 指针塞进
	// 接口会得到**非 nil 的接口值**（带类型的 nil），MetricsHandler 里的 `prune != nil`
	// 会因此为真，随后调用方法解引用 nil 指针 —— 表现为「采集没起来时抓 /metrics 直接 panic」。
	// 事件段已整体删除（2026-11-03）：不再有段回收读数可上报。
	var pruneSource retention.EventStorePruneSource
	// 归档对象回收读数：同一个「带类型的 nil」陷阱——logArchivePurge 是具体指针类型，
	// 未启用时直接作接口实参会得到非 nil 接口值，`purge != nil` 为真，
	// 随后调用方法解引用 nil 指针（本方法对 nil 安全，但别指望下一个实现也安全）。
	var purgeSource retention.ArchivePurgeSource
	if logArchivePurge != nil {
		purgeSource = logArchivePurge
	}
	// 索引库 SQL 慢查询读数随旧链路删除：数据源是 ingest.Manager 的索引库，
	// 新架构没有索引库。此处显式传 nil，让 /metrics 输出「全零序列」而不是缺序列。
	var indexSQLSource retention.IndexSQLSource
	// 投递批次与索引 SQL 指标随旧链路删除（数据源是 ingest.Manager）。
	// 保留策略的三条读数（prune/purge）照常输出。
	wsMux.HandleFunc("/metrics", retention.MetricsHandler(logRetentionDriver, pruneSource, purgeSource, indexSQLSource))
	// 运行期日志等级调整（GET 读 / POST 改）。
	// 同样挂在 127.0.0.1 回环 mux 上，不新增暴露面；只调等级不改格式、不持久化，
	// 重启后回到 log.level 配置值（见 retention.LogLevelController 的边界说明）。
	// 这里正是 logLevelVar 注释里预留的接线点：此前它是 runWorker 的局部量、无任何可达入口。
	wsMux.HandleFunc("/debug/log-level", retention.NewLogLevelController(logLevelVar).Handler())
	// 的 HTTP 日志推送端点（/insert/jsonline）已随旧链路删除。
	//
	// 它依赖 `PUSH_ONLY` 源与 `Pipeline.Ingest` 的 WAL 攒批/背压语义——新采集器的模型里
	// 没有「推送源」这个概念（只有「扫描文件」）。**这是功能下线点**：原本靠 HTTP 推日志
	// 进来的外部系统需要改为「落盘 + 采集」。

	wsAddr := localWSAddr(wsPort)
	wsServer := &http.Server{Addr: wsAddr, Handler: wsMux}

	// 注册到 Control Plane（见 ADR-020）。
	// Control Plane 未启动时 Worker 不退出，按指数退避重试直到注册成功。
	// 崩溃快照上报：进程非正常退出时，把策略捕获的退出码/信号/时长 +
	// 终端环形缓冲的尾部输出（最后 200 行 / 64KB）组装为快照，异步经 gRPC 上报 CP
	// 持久化。上报失败（网络 / 老 CP Unimplemented）记日志丢弃，不阻塞状态机。
	crashReporter := crashreport.New(cpAddr)
	manager.SetCrashHandler(func(instanceID string, info process.CrashInfo) {
		crashReporter.Report(crashreport.Snapshot{
			InstanceUUID: instanceID,
			OccurredAt:   info.OccurredAt,
			ExitCode:     info.ExitCode,
			// docker 容器被 cgroup OOM killer 杀掉时，宿主侧拿不到信号，
			// 用 "killed" 语义补齐（对齐 SIGKILL/137 口径），使 CP 的根因归类能判 oom；
			// 非 OOM 情况沿用进程/容器给出的信号名。
			Signal:     crashSignal(info),
			DurationMs: info.DurationMs,
			TailOutput: crashreport.Tail(terminalServer.BufferedOutput(instanceID), crashreport.DefaultTailLines, crashreport.DefaultTailBytes),
		})
	})

	var regResult *register.Result
	// identityForPersist 是当前节点身份的内存副本（心跳下发 WS 令牌密钥轮换时补写身份文件用）。
	var identityForPersist *register.Identity

	if setupResult != nil {
		// 本次启动由 setup 完成首注册并已持久化身份（见 ADR-051）：
		// 直接复用其换得的身份转 run，不再二次注册、不重复消费一次性 token。
		regResult = &register.Result{
			NodeUUID:      setupResult.Identity.NodeUUID,
			NodeSecret:    setupResult.Identity.NodeSecret,
			WSTokenSecret: setupResult.Identity.WSTokenSecret,
		}
		identityForPersist = setupResult.Identity
		nodeUUID = regResult.NodeUUID
		slog.Info("沿用 setup 首注册身份转入运行", "nodeUUID", nodeUUID)
	} else {
		// 优先复用启动早期加载的本地身份（重注册，不带 token，不重复消费一次性 token）；
		// 无身份文件则为首次安装，必须携带 enrollment token 首注册。
		identity := localIdentity

		regCfg := register.Config{
			ControlPlaneAddr: cpAddr,
			NodeName:         nodeName,
			WsPort:           wsPort,
			GrpcPort:         0,
			Host:             host,
		}
		if identity != nil {
			// 重注册：沿用既有身份的节点名，并经 metadata 出示 node_uuid + node_secret，
			// CP 据此按 UUID 匹配既有节点（而非可重复的 name），杜绝重名覆写（见 ADR-039）。
			regCfg.NodeName = identity.NodeName
			regCfg.NodeUUID = identity.NodeUUID
			regCfg.NodeSecret = identity.NodeSecret
			slog.Info("发现本地节点身份，复用既有身份重注册", "nodeUUID", identity.NodeUUID, "name", identity.NodeName)
		} else {
			// 首次注册：携带一次性 enrollment token。缺 token 直接退出（避免无效注册无限重试刷日志）。
			if cfg.EnrollToken == "" {
				slog.Error("首次注册缺少 enrollment token：请在面板「添加节点」生成一键命令，" +
					"或经 JIANMANAGER_ENROLL_TOKEN 提供。已有节点请确认本地身份文件 etc/node-identity.json 是否存在")
				botMgr.Stop()
				//nolint:gocritic // 此处已显式停止 Bot 管理器；进程必须以失败状态退出。
				os.Exit(1)
			}
			regCfg.EnrollToken = cfg.EnrollToken
		}

		res, err := register.RegisterWithRetry(context.Background(), regCfg, 2*time.Second, 60*time.Second)
		if err != nil {
			slog.Error("注册到 Control Plane 失败", "error", err)
			os.Exit(1)
		}
		regResult = res
		nodeUUID = regResult.NodeUUID
		slog.Info("已注册到 Control Plane", "nodeUUID", nodeUUID)

		// 首次注册成功后持久化身份（含 node_secret，0600），重启复用、不重复消费 token。
		// CP 下发的 WS 令牌密钥一并持久化（见 ADR-061）。
		if identity == nil {
			fresh := &register.Identity{
				NodeUUID:      regResult.NodeUUID,
				NodeSecret:    regResult.NodeSecret,
				NodeName:      regCfg.NodeName,
				WSTokenSecret: regResult.WSTokenSecret,
			}
			if err := register.SaveIdentity(etcDir, fresh); err != nil {
				// 持久化失败不致命（本次仍在线），但重启会因无身份且 token 已失效而首注册失败，需告警。
				slog.Warn("持久化节点身份失败，重启可能需重新签发 enrollment token", "error", err)
			}
			if cfg.EnrollTokenFile != "" {
				if err := os.Remove(cfg.EnrollTokenFile); err != nil && !os.IsNotExist(err) {
					slog.Warn("删除已消费 enrollment token 文件失败", "error", err)
				}
			}
			identityForPersist = fresh
		} else {
			if regResult.WSTokenSecret != "" && regResult.WSTokenSecret != identity.WSTokenSecret {
				// 重注册带来新 WS 令牌密钥（存量节点升级 / CP 轮换后的主路径）：补写身份文件。
				identity.WSTokenSecret = regResult.WSTokenSecret
				if err := register.SaveIdentity(etcDir, identity); err != nil {
					slog.Warn("持久化 WS 令牌密钥失败（内存已生效，重启后经注册自愈）", "error", err)
				}
			}
			identityForPersist = identity
		}
	}

	// 节点身份就绪后崩溃快照上报可用：两条注册路径（setup 首注册 / 常规注册）在此汇合。
	crashReporter.SetIdentity(nodeUUID, regResult.NodeSecret)
	// 孤儿处置审计上报（456）同样就绪：此后运行期扫描/接管兜底的处置动作可落 CP 审计库。
	orphanAuditReporter.SetIdentity(nodeUUID, regResult.NodeSecret)

	// bot-worker dist 自愈下发（见 ADR-072）：注册成功即持身份从 CP 拉取内嵌归档，
	// 物化到数据根后切换 bot 入口路径。显式 JIANMANAGER_BOT_WORKER_PATH 时尊重覆盖不自愈；
	// CP 未内嵌/拉取失败回退本地已有（物化副本或旧相对路径），只告警不阻断启动。
	if !botWorkerPathPinned {
		ensureCtx, cancelEnsure := context.WithTimeout(context.Background(), 60*time.Second)
		entry, err := botdist.Ensure(ensureCtx, botdist.Options{
			CPAddr:            cpAddr,
			NodeUUID:          nodeUUID,
			NodeSecret:        regResult.NodeSecret,
			Dir:               root.BotWorkerDir(),
			GlobalNodeModules: botdist.GlobalNodeModulesDir(runtimeMgr.RootDir()),
		})
		cancelEnsure()
		if err != nil {
			slog.Warn("bot-worker 自愈下发未成，沿用现有入口路径", "error", err, "fallback", botWorkerPath)
		} else {
			botMgr.SetBotWorkerPath(entry)
			slog.Info("bot-worker dist 已就绪", "entry", entry)
		}
	}

	// CP 下发的 WS 令牌密钥热应用（见 ADR-061）：注册响应非空即应用到终端/插件桥。
	// 旧 CP 未下发密钥时仅可使用此前由 CP 持久化的密钥，绝不回退 worker.yml 的 jwt_secret。
	currentWSSecret := wsTokenSecret
	if regResult.WSTokenSecret != "" {
		terminalServer.SetJWTSecret(regResult.WSTokenSecret)
		pluginBridge.SetJWTSecret(regResult.WSTokenSecret)
		currentWSSecret = regResult.WSTokenSecret
	}
	if currentWSSecret == "" {
		slog.Error("Control Plane 未下发 WS 令牌密钥，拒绝启动 WS 服务")
		os.Exit(1)
	}
	go func() {
		slog.Info("WebSocket 服务器就绪", "addr", wsAddr)
		startup.Mark("wsReady")
		// 所有关键阶段都已就绪，这时输出分段耗时最有意义。
		startup.Report()
		if err := wsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			// WS 监听失败是**致命**的：本节点已无法被 CP 接入终端与插件桥；
			// 更关键的是，它通常意味着**另一个 Worker 实例正占着这个端口**。
			//
			// 历史上的错（2026-11-07 实测）：这里只打 ERROR 就返回，**主进程照常运行**。
			// 于是两个 Worker 同时活着、同时采集同一批文件：
			//   · 两份数据写进 VictoriaLogs（重复）
			//   · 两边 registry 互相覆盖（各自以为自己读到不同位置）
			// 而 registry 只有进程内锁、没有文件锁，两个进程谁也不知道对方存在。
			//
			// 一个节点只应有一个 Worker（启动脚本 / systemd unit 都是这个模型）。
			// 所以这里必须退出，而不是降级继续跑：后者把"启动失败"变成"静默损坏"。
			slog.Error("WebSocket 服务器退出，进程终止（一个节点只允许一个 Worker；"+
				"端口被占通常意味着已有另一个实例在运行）", "addr", wsAddr, "error", err)
			os.Exit(1)
		}
	}()

	// 启动心跳上报（携带注册获得的 node_secret 供 Control Plane 鉴权）。
	// 节拍取共享契约值 directprobe.HeartbeatInterval（ADR-013 的 30s）：单拍采集预算的上界正是
	// 由它反推（见 internal/platform/directprobe），两处必须同源，否则「预算 < 节拍」的护栏失效。
	hb := heartbeat.New(cpAddr, nodeUUID, regResult.NodeSecret, directprobe.HeartbeatInterval, manager)
	// 心跳消费同一窗口，不重新读取累计 CPU 计数或推进采样基线。
	hb.SetCPUReader(cpuSampler)
	// 受管运行时快照随反向隧道 Heartbeat 上报；只读取既有 Worker/Bot Worker，不会拉起 Bot 子进程。
	hb.SetManagedRuntimeProvider(workerServer)
	// 日志存储层（VL）磁盘占用读数随心跳上报（2026-10-29）：
	//
	// 配了磁盘上限后，VL 超限会**丢弃最旧的按日分区**；而它把磁盘写满时会进入
	// **只读模式、连丢弃都做不了**。两种情形默认全静 ⇒ 必须进告警链路。
	//
	// 未启用受管 VL（vlSupervisor==nil）时不注入：心跳不带这四个字段，
	// CP 按「无读数」处理，不会误报成「磁盘 0 字节」。
	if vlSupervisor != nil {
		hb.SetVLDiskProvider(vlDiskProviderAdapter{sup: vlSupervisor})
	}
	// 运行中长任务进度随心跳上报（，见 ADR-040）：心跳读 Worker gRPC Server 的内存任务表。
	hb.SetTaskProvider(workerServer)
	// CP 经心跳响应下发节点期望出站代理（见 ADR-043）：generation 变化时重建出站持有者，
	// 注入到各下载点（JDK/CFR/自更新/服务端 jar）即时生效；下发为空回退本地 worker.yml/env。
	hb.SetProxyRebuilder(func(c httpclient.Config) error {
		if err := outboundProvider.Rebuild(c); err != nil {
			slog.Warn("据心跳下发重建出站代理失败", "proxy", httpclient.Sanitize(c.URL), "error", err)
			return err
		}
		slog.Info("出站代理已据 CP 下发运行时更新", "proxy", httpclient.Sanitize(c.URL), "noProxy", c.NoProxy)
		return nil
	})
	// CP 经心跳下发 WS 令牌密钥（见 ADR-061）：值变化即热更新终端/插件桥校验并补写
	// 身份文件——CP 轮换密钥后 Worker 不重启自愈。心跳单 goroutine 应用，无并发写身份文件。
	hb.SetWSSecretApplier(currentWSSecret, func(secret string) error {
		terminalServer.SetJWTSecret(secret)
		pluginBridge.SetJWTSecret(secret)
		if identityForPersist == nil {
			return nil // 理论不达：注册成功必有身份；无身份时仅内存生效
		}
		identityForPersist.WSTokenSecret = secret
		return register.SaveIdentity(etcDir, identityForPersist)
	})
	// CP 经心跳响应下发实例健康巡检策略：阈值/动作/探针类型/熔断阈值写入巡检器生效值，
	// 无需重启即生效；本地开关（worker.health_scan_disabled）为硬关，CP 无法远程开启。
	hb.SetHealthPolicyApplier(func(p process.HealthPolicy) {
		healthScanner.SetPolicy(p)
	})
	// 日志采集配置：先加载本地持久化快照——CP 不可达时也能沿用上次生效值
	// （离线保持），并作为首拍去重的基准。文件缺失/损坏不阻断启动：采集配置问题
	// /504 的面板热更新链路已随旧链路删除：它的消费方是 ingest.Manager 的
	// 采集策略与定时扫描，而新采集器不读这些策略（路径来自 CP 下发的实例登记）。
	// **这是功能下线点**：面板上「采集策略 / 归档导入扫描周期」改了不会再产生效果。
	hb.Start()
	defer hb.Stop()

	// 常驻反向隧道：把同一 workerServer 实现挂到隧道上，Worker 不监听 CP 直拨 gRPC 端口。
	tunnelRunner := tunnel.New(cpAddr, nodeUUID, regResult.NodeSecret, func(reg grpc.ServiceRegistrar) {
		workerpb.RegisterWorkerServiceServer(reg, workerServer)
	})
	tunnelRunner.Start()
	defer tunnelRunner.Stop()

	// 等待信号
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh

	slog.Info("收到退出信号，正在关闭", "signal", sig)
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if err := wsServer.Shutdown(shutdownCtx); err != nil {
		slog.Warn("WS 服务关闭超时", "error", err)
	}
	manager.StopAll()
	cancelLogRuntime()
	if err := logStack.LogRPC.Close(); err != nil {
		slog.Warn("日志查询视图关闭失败", "error", err)
	}
	// 先停看护再停 VL：反过来的话，停止过程中进程退出会被看护判成崩溃并尝试拉起
	// （epoch 已挡住，但显式取消更直白，也不留残余 goroutine）。
	if cancelVLGuardian != nil {
		cancelVLGuardian()
	}
	// 受管 VL 必须随 Worker 一起回收：否则它们会成为孤儿并继续占用 hot/cold/rehydrate
	// 端口，导致下次启动的新 VL 因端口被占而立即退出（真机事故），采集面长期降级。
	if vlSupervisor != nil {
		vlSupervisor.StopAll(shutdownCtx)
	}
	// 日志文件放在最后关：上面每一条 Warn/Info 还要落到它里；
	// Close 会等后台 gzip 收尾，避免刚 rename 的轮转档卡在未压缩状态。
	closeProcessLogger()
	slog.Info("Worker Node 已停止")
}

// localWSAddr 返回仅供本机终端回环桥与本机探针使用的 WebSocket 监听地址。
func localWSAddr(port int) string {
	return fmt.Sprintf("127.0.0.1:%d", port)
}

// repairProjectionQueryGenerations 扫描 Catalog 并把「投影 query_generation 落后于权威
// generation」的分区对齐，返回（修复数, 失败数, 需人工处置的分区清单）。
//
// 背景与修法见 catalog.RepairProjectionQueryGeneration 的注释；这里只负责遍历与汇总。
//
// 第三个返回值是 2026-10-21 复审补上的可观测性缺口：该自愈对三种形态**不修也不报错**
// （分区没有已发布投影 / 投影代次反向矛盾 / 投影目录与权威目录不一致）。早前调用方只在
// 「修复数或失败数 > 0」时打日志 ⇒ 这些分区既不计数也不留痕，运维完全看不到「还有 N 个
// 待人工」。它们必须被汇总出来单独打一条 WARN，否则沉默的「不修」等于假装健康。
//
// 为什么逐条独立处理、失败不中断：单个分区的 CAS 冲突（并发发布抢先生效）只应影响它自己，
// 不该让其余分区也修不上。失败计数会由调用方以 WARN 打出，不静默。
//
// 为什么用 `Keys()` 而不是另建索引：分区数量级是「节点数 × 天数」（生产 283 个），
// 启动扫一遍是 O(分区数) 的内存读，与既有 `StartingRecover()` 同档，无需额外索引。
func repairProjectionQueryGenerations(cat *catalog.Catalog) (repaired, failed int, needsManual []string) {
	if cat == nil {
		return 0, 0, nil
	}
	for _, key := range cat.Keys() {
		ok, manual, err := cat.RepairProjectionQueryGeneration(key)
		if err != nil {
			failed++
			slog.Warn("投影代次一致性自愈失败（该分区待下一轮或人工处理）",
				"partition", key.String(), "error", err)
			continue
		}
		if ok {
			repaired++
			slog.Info("投影代次一致性已修复", "partition", key.String(),
				"detail", "query_generation 对齐到权威 generation，该分区恢复可发布")
			continue
		}
		if manual != "" {
			needsManual = append(needsManual, key.String()+": "+manual)
		}
	}
	return repaired, failed, needsManual
}

// maxNeedsManualPartitionsLogged 限制「需人工」清单在**单条日志行**里的条数。
//
// 汇总条数（count）永远是准确的，被截断的只是逐条列举：分区数量级是「节点数 × 天数」
// （生产 283 个），若全部属于需人工形态，一整行几百个键会让这条日志本身失去可读性。
const maxNeedsManualPartitionsLogged = 20

// logIndexPersistWindow 把 stateindex 采样环换算成「本窗口的增量」。
//
// 为什么不能拿「环和」相减当增量：采样环只保留最近 4096 次（提交单元）持久化，而采集循环每 250 ms 就可能触发
// 一次 persist，真机 60 源实测约 2.5 次/s（环覆盖 ≈205 s），环在启动几分钟后就会写满。写满后
// 「环和」是滑动窗口和，相邻两次读数相减得到的是「本窗口新增 − 同期被挤出的旧样本」——稳态下
// 两者近似相等，差值趋近 0，会把整整一分钟的写入量抹掉；环未满时「环和」又等于自启动累计值，
// 直接当成窗口量上报，就是把「开服至今的写入」当成「一分钟的写入」（实测：删除因此被读成
// 写入的 9 倍，而同口径真值约为删除 ≈ 0.5 × 写入）。两种形态都不是窗口增量。
//
// 所以这里按「样本身份」做差：窗口基准是一个时间游标，即上一次读数时环内最新样本的 StartedAt，
// 每次只累计 StartedAt 严格晚于游标的样本。不管环满没满、有没有挤出旧样本，累计到的都恰好是
// 「两次读数之间新增的那几次持久化」——这才叫窗口增量。
//
// 前提：单个窗口内新增的样本数少于环容量（4096，见 stateindex.sampleRing）。否则最早的那几次
// 新增样本已被挤出环外，读数只能是下界。生产上采集循环节拍 250 ms、采样节拍 1 分钟，单窗口最多
// 约 240 次持久化周期；切分（P0）后一次周期会产生多个**提交单元**采样，60 源 × 60 行/s
// 实测约 2 单元/周期 ⇒ 单窗口约 400 个样本，仍远小于 4096（环容量正是为此从 512 提到 4096）。

// logIndexPersistDelta 是一个窗口内的写入增量，与同一条日志里的 P50/P95/Max 同窗口。
// catalogBackfillStartDelay 是启动期分区补建的**让位窗口**。
//
// 为何需要：补建要知道「本节点负责哪些 namespace」，而那来自**已登记的源**。
// 采集运行时（manager.Start）与源登记都在异步路径上，立即开跑只能看到空源表。
//
// 取值权衡：太长会推迟历史天恢复可查的时间，太短会让首轮白跑（白跑不会错，
// 只是浪费一次查询）。30s 覆盖了常规启动的登记耗时（实例发现 + 游标恢复）。
// 即使没赶上，补建也就在那一轮返回零计数，不会假装成功。
const catalogBackfillStartDelay = 30 * time.Second

// 日志分区 Catalog 的 journal 使用**纯内存**（2026-11-05 架构决策，ADR-102）。
//
// # 为什么不再落盘
//
// 分区目录（「哪些源在哪些天有数据」）此前靠三层补救去近似一个 VL 已经知道的事实：
//
//	第 1 层  journal 纯内存      → 重启后历史天全丢
//	第 2 层  改成 JSONL 落盘     → 与 2026-10-30「机械盘不落盘」裁定冲突
//	第 3 层  启动期从 VL 反推    → 每源一次查询、窗口易漏
//
// 现在由 `hits` 在启动期一次请求重建完整分区目录（实测 428 个 (源,天) / 206ms），
// 落盘这一层就失去了存在理由——它维护的副本已经被「直接问 VL」取代。
//
// # 落盘原本承担的另外两项职责，逐项核过
//
//  1. **租约旁路文件**（`<journal>.leases`）：`leaseDeltaLog` 的注释已明确它降级为
//     「可用性辅助」——租约是短生命周期运行时状态，视图 TTL 到期就失效，进程重启后
//     旧视图 ID 本来就不该复活。丢了它唯一后果是「旧视图失效得早一点」，不影响正确性。
//  2. **StartingRecover 的 journal 重放**：`ReplayJournal` 只读传入的 entries 切片，
//     而 entries 来自内存 journal；落盘与否不影响恢复语义。
//
// # 代价（诚实说明）
//
// 重启后分区目录从空开始，由 hits 重建补齐（异步，约 30s 后启动）。重建完成前查询侧
// 对历史天报「无分区记录」——这与改造前的落盘形态相比是**短暂降级**，但换掉了
// 「副本可能与真源不一致」这个长期风险（journal 只写到 10-30 那类事故不会再发生）。
func openLogCatalogJournal(cfg *workercfg.Config, root *dataroot.Root, fallback catalog.Journal) (catalog.Journal, func() error) {
	if cfg != nil && cfg.LogCatalog.JournalDisabled() {
		slog.Info("日志 Catalog journal 为纯内存（log_catalog.journal_enabled=false）")
	}
	return fallback, nil
}

// resolveHotRetention 把 `log_vl.retention_period` 解析成热层保留期时长。
//
// 为什么抽成包级函数（2026-10-28）：这个值现在有两处消费者——ingest 侧的「热层可发布窗口」，
// 以及归档还原（Rehydrate）侧「这个分区日是否注定写不进去」的判定。两处必须用**同一口径**，
// 否则会出现「ingest 认为超窗口、rehydrate 认为没超」这种自相矛盾。
//
// 解析失败返回 0（= 不启用过滤，保持改动前行为）：与 ingest 侧 hot_retention_window.go
// 同一取舍——没配置就把数据全过滤掉，比不修更糟。
func resolveHotRetention(cfg *workercfg.Config) time.Duration {
	raw := strings.TrimSpace(cfg.LogVL.RetentionPeriod)
	if raw == "" {
		raw = vlsup.DefaultRetentionPeriod
	}
	period, err := retention.ParseRuntimeTTL(raw)
	if err != nil {
		slog.Warn("热层 retention 解析失败：不启用「热层可发布窗口」过滤（行为与改动前一致）",
			"retentionPeriod", raw, "error", err)
		return 0
	}
	slog.Info("热层可发布窗口已启用：超出热层保留期的 UTC 天不再重建、也不算对账缺失天",
		"retentionPeriod", raw, "hotRetention", period)
	return period
}

// vlDiskProviderAdapter 把 vlsup.Supervisor 的磁盘读数适配成 heartbeat.VLDiskProvider。
//
// 为什么要这么一层：vlsup 不 import heartbeat（heartbeat 已经 import ingest/metrics，
// 反向依赖会成环），所以两边各自定义同形结构体，由装配层在这里做一次转换。
type vlDiskProviderAdapter struct {
	sup *vlsup.Supervisor
}

func (a vlDiskProviderAdapter) VLDiskSnapshot() heartbeat.VLDiskReading {
	r := a.sup.VLDiskSnapshot()
	return heartbeat.VLDiskReading{
		UsedBytes:        r.UsedBytes,
		LimitBytes:       r.LimitBytes,
		LimitPercent:     r.LimitPercent,
		Partitions:       r.Partitions,
		RowsDroppedTotal: r.RowsDroppedTotal,
	}
}

// sampleLogBudget 更新真实准入所使用的唯一预算控制器，采样失败保持未知而非伪造零。
//
// 注意：这是 **VL 资源预算**的采样（Worker RSS / 日志盘用量 / 预留字节），
// 与采集器无关——它随 VL 一起存在，不随旧采集链路删除。
func sampleLogBudget(ctx context.Context, sup *vlsup.Supervisor, budget *vlsup.BudgetController, roots []string, externalHot bool) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	last := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sample := sampleLogResources(sup, roots, externalHot)
			budget.Update(sample)
			foregroundErr, backgroundErr := budget.Admit(vlsup.WorkForeground), budget.Admit(vlsup.WorkBackground)
			state := fmt.Sprint(foregroundErr, ";", backgroundErr)
			snapshot := budget.Snapshot()
			slog.Debug("日志资源预算采样", "workerRssBytes", sample.WorkerRSSBytes,
				"workerRssKnown", sample.WorkerRSSKnown, "namespaceRss", sample.Namespaces,
				"diskUsagePercent", sample.DiskUsagePercent, "diskKnown", sample.DiskKnown,
				"logDiskBytes", sample.DiskUsedBytes, "logDiskBytesKnown", sample.DiskBytesKnown,
				"reservedBytes", snapshot.TotalReservedBytes,
				"reservedWalBytes", snapshot.Reserved[vlsup.ReservationWAL],
				"reservedStagingBytes", snapshot.Reserved[vlsup.ReservationStaging],
				"reservedExportBytes", snapshot.Reserved[vlsup.ReservationExport])
			if state != last {
				if foregroundErr != nil || backgroundErr != nil {
					slog.Warn("日志资源预算准入受限；WAL 仍允许有界排空", "foreground", foregroundErr, "background", backgroundErr)
				} else {
					slog.Info("日志资源预算准入正常")
				}
				last = state
			}
		}
	}
}

// hitsSourceAdapter 把 vlsup.Client 的 hits 能力适配成 logtail.HitsSource。
//
// # 为何需要这一层
//
// 两个包各自定义了形状相同的 SourceDay（vlsup 是「VL 客户端」，logtail 是「采集器」，
// 互不依赖是对的）。装配层做一次转换，比让其中一个包 import 另一个更轻。
// 这与旧链路 HitsSourceFunc 是同一模式。
//
// # 为何 nil 客户端返回 nil
//
// nil HitsSource 在 logtail 里有明确语义：**不做启动重建**，并在启动日志里说明
// （「未接 hits 数据源」）。宁可如实说「没接」，也不要给一个恒返回空的结果——
// 那样日志会显示「重建完成，补齐 0 天」，把一个缺失的能力伪装成正常结果。
func hitsSourceAdapter(c *vlsup.Client) logtail.HitsSource {
	if c == nil {
		return nil
	}
	return func(ctx context.Context, start, end time.Time) ([]logtail.SourceDay, error) {
		days, err := c.HitsBySourceDay(ctx, start, end)
		if err != nil {
			return nil, err
		}
		out := make([]logtail.SourceDay, 0, len(days))
		for _, d := range days {
			out = append(out, logtail.SourceDay{
				LogSourceID: d.LogSourceID,
				UTCDay:      d.UTCDay,
				Weight:      d.Weight,
			})
		}
		return out, nil
	}
}

// multilineRuleSource 说明生效的多行规则来自哪里（默认 vs 显式配置）。
//
// 它只服务启动日志：让读日志的人一眼知道"这条规则是我配的，还是用的默认"，
// 不必回头看 worker.yml 才知道。
func multilineRuleSource(cfg *workercfg.Config) string {
	if cfg != nil && strings.TrimSpace(cfg.LogTailer.Multiline.Pattern) != "" {
		return "配置显式指定"
	}
	return "默认规则（log_tailer.multiline.pattern 留空）"
}
