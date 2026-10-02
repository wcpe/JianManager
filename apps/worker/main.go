package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
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
	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/archive"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/lifecycle"
	"github.com/wcpe/JianManager/internal/worker/logs/logassemble"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
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

// crashSignal 归一化崩溃上报的信号名（FR-467）。
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
	// 采集索引只读导出（FR-496 spec §2.3）：`worker log-index-export [--data-dir DIR] [--out FILE]`。
	// 用于排障与回滚前的快照，不改动索引库或任何状态。
	if len(os.Args) > 1 && os.Args[1] == "log-index-export" {
		runLogIndexExport(os.Args[2:])
		return
	}
	// 事件级状态派生态视图（后续项③）：`worker log-event-status [--data-dir DIR] [--source ID[/GEN]]
	// [--from N --to M | --at RFC3339]`。只读索引库（+ 可选事件段），不改动任何状态。
	if len(os.Args) > 1 && os.Args[1] == "log-event-status" {
		runLogEventStatus(os.Args[2:])
		return
	}
	runWorker()
}

// runLogEventStatus 输出“某源某区间/某时间点当前处于哪一态”的派生态视图（JSON 到 stdout）。
//
// 只读：索引库不存在或不可读时报错退出（退出码 1），不创建、不修改任何文件。
func runLogEventStatus(args []string) {
	override := ""
	for _, value := range parseDataDirArg(args) {
		override = value
	}
	dataRoot, err := dataroot.Resolve(override)
	if err != nil {
		fmt.Fprintf(os.Stderr, "解析数据目录失败: %v\n", err)
		os.Exit(1)
	}
	if err := ingest.ExportEventStatus(dataRoot.Base(), args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "导出事件级状态失败: %v\n", err)
		os.Exit(1)
	}
}

// runLogIndexExport 把采集索引（SQLite）导出为旧 ingest.state.json 结构的 JSON。
//
// 只读：库不存在或不可读时报错退出（退出码 1），不创建、不修改任何文件。
func runLogIndexExport(args []string) {
	override := ""
	for _, value := range parseDataDirArg(args) {
		override = value
	}
	dataRoot, err := dataroot.Resolve(override)
	if err != nil {
		fmt.Fprintf(os.Stderr, "解析数据目录失败: %v\n", err)
		os.Exit(1)
	}
	root := dataRoot.Base()
	out := os.Stdout
	for i := 0; i < len(args); i++ {
		value := ""
		switch {
		case args[i] == "--out" && i+1 < len(args):
			value = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--out="):
			value = strings.TrimPrefix(args[i], "--out=")
		default:
			continue
		}
		file, err := os.OpenFile(value, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "打开导出文件失败: %v\n", err)
			os.Exit(1)
		}
		defer func() { _ = file.Close() }()
		out = file
	}
	if err := ingest.ExportIndexJSON(root, out); err != nil {
		fmt.Fprintf(os.Stderr, "导出采集索引失败: %v\n", err)
		os.Exit(1)
	}
	if out != os.Stdout {
		fmt.Fprintln(os.Stderr, "采集索引已导出")
	}
}

// runDaemonWrapper 以 wrapper 子进程模式运行。
// 配置通过环境变量 JM_DAEMON_WRAPPER_CONFIG 传递（JSON）。
func runDaemonWrapper() {
	// wrapper 必须在 Worker 重启后存活（ADR-003、FR-341）：Worker 死后其建立的 stdout/stderr
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

// configPathArg 返回命令行第 1 个位置参数当且仅当它是「现存的配置文件路径」（FR-222，见 ADR-051）。
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

// isConfigured 报告 Worker 是否已配置（FR-222，见 ADR-051）。
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

func runWorker() {
	// Windows 服务默认 cwd=System32，会把 setup 写出的 worker.yml 落到错误位置且重启找不到（FIX-2/3）。
	// 以服务身份运行时先把工作目录切到 exe 所在的安装目录（与 Linux systemd WorkingDirectory 对齐）。
	chdirToExeDirIfService()

	// 加载配置：worker.yml + JIANMANAGER_ 环境变量覆盖（FR-080，见 ADR-020）。
	// 配置可选参数从命令行第 1 个参数取（如 `worker /path/worker.yml`），缺省自动查找。
	// 第 1 个参数仅当是「现存的配置文件路径」时才当配置文件；setup 的 --xxx flag 不算（见下自检）。
	cfgPath := configPathArg()

	// 未配置自检（FR-222，见 ADR-051）：无 worker.yml/.yaml 且无 etc/node-identity.json → 进 setup。
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

	// 日志等级/格式装配（此前**完全没有装配点**：Worker 从不调用 slog.SetDefault，
	// 于是 log.level / log.format 两个配置项形同不存在——写 debug 不产生任何 DEBUG 输出，
	// 写 error 也压不住 INFO。现场表现是「改了配置但日志没变」，极难归因。
	//
	// 用 LevelVar 而不是固定等级：G10 要求「运行期可动态调等级」（平时压到 info 降开销、
	// 排障时临时开 debug），而 LevelVar 正是该能力的接线点。
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
	logOpts := &slog.HandlerOptions{Level: logLevelVar}
	if logFormat == "json" {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, logOpts)))
	} else {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, logOpts)))
	}
	slog.Info("Worker 日志已按配置装配", "level", logLevel.String(), "format", logFormat,
		"note", "等级可经 logLevelVar 运行期调整（G10）")

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

	// 出站 HTTP 客户端持有者（FR-174/FR-185，见 ADR-037/043）：所有出站下载（自更新/JDK/CFR/服务端 jar）
	// 经此进程级代理 client。proxy.url 留空=直连（沿用环境变量代理）。非法代理 URL 启动即 fail-fast。
	// 持有者可运行时重建：CP 经心跳下发节点期望代理后即时生效（custom→节点值 / inherit→全局默认，FR-185）。
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

	// 节点 UUID 在首次注册后由 CP 签发并落本地身份文件复用（FR-080）；启动时先占位。
	nodeUUID := "local-dev"

	// 解析并初始化项目自包含数据根（配置 data_dir，缺省 ./data，可经 JIANMANAGER_DATA_DIR 覆盖）。
	// 运行态数据全部收口到此根，整体可迁移。参见 ADR-010。
	root, err := dataroot.Init(cfg.DataDir)
	if err != nil {
		slog.Error("初始化数据根失败", "error", err)
		os.Exit(1)
	}

	// 本地身份提前加载（FR-275，见 ADR-061）：WS 令牌密钥初始值优先取身份文件持久化值
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
	// 维护自身 PID 记录（与 CP 的 cp-lifecycle.sh 同口径）：生产曾遗留停在多日前的 worker.pid，
	// 按它识别进程会认错；此处启动即原子覆盖，写失败仅告警、不影响启动。
	if err := writeWorkerPIDRecord(root.Base(), wsPort, cfgPath); err != nil {
		slog.Warn("写 worker.pid 记录失败（不影响运行）", "error", err)
	}
	// 初始化进程管理器
	manager := process.NewManager(serversDir)
	// 接管重试窗口（FR-455①）：Worker 重启后对存活 wrapper 的 reconnect 重试序列（默认 ≈127s），
	// 覆盖分钟级瞬时故障；必须在下方 RecoverDaemonInstances 之前装配，否则该轮接管仍用默认序列。
	manager.SetRecoverRetryBackoff(cfg.Recover.RetryBackoffSequence())
	// 启动内存闸（FR-317）：可用内存塞不下待启实例即拒绝启动，防节点 OOM 失联。
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
		// JDK 下载经进程级出站持有者（FR-174/FR-185）：CP 下发代理改动运行时即时生效。
		jdkMgr.SetHTTPClientProvider(outboundProvider.Client)
		slog.Info("JDK manager enabled", "rootDir", root.JDKsDir())
	} else {
		slog.Info("JDK manager disabled by JIANMANAGER_DISABLE_JDK=1")
	}
	// 孤儿处置审计回调（FR-455/456）：误杀拦截/孤儿处置既落 Worker 结构化日志（不静默），
	// 又经 Worker→CP 出站信道上报落 CP 审计库（spec §2.2）。
	//
	// 装配顺序（FR-456 N2）：必须在 RecoverDaemonInstances **之前**。接管兜底的处置/误杀拦截动作
	// 就发生在启动恢复期，若回调晚装配，这些真孤儿处置只落 slog、进不了 CP 审计库（reviewer 无从查证）。
	cpAddr := cfg.ControlPlane
	if cpAddr == "" {
		cpAddr = "localhost:9100"
	}
	orphanAuditReporter := orphanaudit.New(cpAddr)
	// 进程退出前释放复用的上报连接与派发 goroutine（FR-456 N4）。
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
	recovered, recoverErr := manager.RecoverDaemonInstances()
	if recoverErr != nil {
		slog.Warn("恢复 daemon 实例失败", "error", recoverErr)
	}
	if recovered > 0 {
		slog.Info("已恢复 daemon 实例连接", "count", recovered)
	}

	// 运行期周期孤儿扫描（FR-456）：随 Worker 常驻，持续兜底 wrapper 死/Java 活、direct 孤儿、
	// docker 残留，按策略处置（默认 warn 只告警，可配 auto 自动清理）。补齐「仅启动时清理」的缺口。
	if !cfg.OrphanScan.Disabled {
		policy := process.NormalizeOrphanDisposePolicy(cfg.OrphanScan.DisposePolicy)
		orphanScanner := process.NewOrphanScanner(manager, cfg.OrphanScan.ScanInterval(), policy)
		// FR-497：未纳管活进程的自动收养开关（默认开；此处接线使 orphan_scan.auto_adopt 生效）。
		orphanScanner.SetAutoAdopt(cfg.OrphanScan.AutoAdoptEnabled())
		scannerCtx, cancelScanner := context.WithCancel(context.Background())
		defer cancelScanner()
		orphanScanner.Start(scannerCtx)
	}

	// 运行期实例健康巡检与自愈（FR-459）：存活 ∧ 响应双维识别假死，受控自愈（warn/restart）
	// 与崩溃熔断（窗口内重启超限即停自动重启）。本地配置 worker.health_scan.*；运行期阈值/动作
	// 可经心跳响应下发覆盖（见下方 heartbeat.SetHealthPolicyApplier）。
	healthScanner := process.NewHealthScanner(manager, !cfg.HealthScan.Disabled, cfg.HealthScan.ScanInterval(), cfg.HealthScan.HealthPolicy())
	if !cfg.HealthScan.Disabled {
		healthCtx, cancelHealth := context.WithCancel(context.Background())
		defer cancelHealth()
		healthScanner.Start(healthCtx)
	}

	// 初始化指标采集器
	collector := metrics.NewCollector(30 * time.Second)
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
	var coldVLClient *vlsup.Client
	var rehydrateVLClient *vlsup.Client
	var vlSupervisor *vlsup.Supervisor
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
			RetentionByNamespace: map[vlsup.Namespace]string{
				// 冷层 retention 是「冷留存多久」的唯一执行者（用户口径：730d，所有级别统一）。
				vlsup.NamespaceCold: cfg.LogVL.ColdRetentionPeriod,
			},
			AuthUsername: cfg.LogVL.Username, AuthPassword: cfg.LogVL.Password,
			Ports: map[vlsup.Namespace]int{
				vlsup.NamespaceHot:       cfg.LogVL.HotPort,
				vlsup.NamespaceCold:      cfg.LogVL.ColdPort,
				vlsup.NamespaceRehydrate: cfg.LogVL.RehydratePort,
			},
			MemoryAllowedBytes: map[vlsup.Namespace]int64{vlsup.NamespaceHot: cfg.LogVL.HotCacheBytes},
			// 受管 VL 进程 Go 软内存上限（契约 §6.6 RSS 预算）。
			MemoryLimitBytes: cfg.LogVL.MemoryLimitBytes,
		})
		if err != nil {
			slog.Error("VictoriaLogs supervisor 创建失败", "error", err)
		} else if err = vlSupervisor.Start(context.Background(), vlsup.NamespaceHot); err != nil {
			slog.Error("VictoriaLogs HOT 启动失败", "error", err)
		} else {
			if cfg.LogVL.StartCold {
				if err = vlSupervisor.Start(context.Background(), vlsup.NamespaceCold); err != nil {
					slog.Error("VictoriaLogs COLD 启动失败", "error", err)
				}
			}
			if cfg.LogVL.StartRehydrate {
				if err = vlSupervisor.Start(context.Background(), vlsup.NamespaceRehydrate); err != nil {
					slog.Error("VictoriaLogs Rehydrate 启动失败", "error", err)
				}
			}
			// FR-476 / 契约 §6.6：周期采样 VL RSS 与数据盘预算并暴露降级。
			go sampleLogBudget(vlSupervisor)
			// 受管 VL 启动是异步的：接线 ingest/查询前先等 HOT 就绪，避免启动期写连接被拒
			// 导致采集运行时创建失败（重启时序 / Runbook C）。
			hotReadyCtx, cancelHotReady := context.WithTimeout(context.Background(), 15*time.Second)
			if waitErr := vlSupervisor.WaitHealthy(hotReadyCtx, vlsup.NamespaceHot, 15*time.Second); waitErr != nil {
				slog.Warn("VictoriaLogs HOT 未在超时内就绪，采集/查询将保持降级", "error", waitErr)
			}
			cancelHotReady()
			if vlClient, clientErr := vlSupervisor.ClientFor(vlsup.NamespaceHot); clientErr == nil {
				vlHTTPClient = vlClient
				hotRange, _ := vlrange.New(vlClient)
				tiered := &vlrange.Tiered{Hot: hotRange}
				for _, ns := range []vlsup.Namespace{vlsup.NamespaceCold, vlsup.NamespaceRehydrate} {
					status, statusErr := vlSupervisor.Status(ns)
					if statusErr != nil || status.State != vlsup.StateRunning {
						continue
					}
					client, clientErr := vlSupervisor.ClientFor(ns)
					if clientErr != nil {
						continue
					}
					rangeClient, rangeErr := vlrange.New(client)
					if rangeErr != nil {
						continue
					}
					if ns == vlsup.NamespaceCold {
						tiered.Cold = rangeClient
						coldVLClient = client
					} else {
						tiered.Rehydrate = rangeClient
						rehydrateVLClient = client
					}
				}
				rangeClient = tiered
				slog.Info("VictoriaLogs supervisor RangeClient 已接线", "baseURL", vlClient.BaseURL())
			}
		}
	}
	if rangeClient == nil && strings.TrimSpace(cfg.LogQuery.VLURL) != "" {
		vlClient, vlErr := vlsup.NewClient(vlsup.ClientOptions{
			BaseURL:  cfg.LogQuery.VLURL,
			Username: cfg.LogQuery.Username,
			Password: cfg.LogQuery.Password,
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
	var logJournal catalog.Journal
	// 日志容量读数（磁盘使用率 / WAL 上限）的**唯一**构造点：采集侧容量门禁与
	// 保留策略的磁盘触发共用同一份提供者。两者各采一次磁盘迟早会给出不同结论，
	// 而现场只会表现成「有时降级有时不降级」，极难归因。
	if notice := cfg.WALBudgetNotice(); notice != "" {
		slog.Warn(notice)
	}
	// 静默源的未闭合缓冲超时（缺陷 B）：非法值在 config.Load 阶段已被拒，
	// 此处是防御性兜底——退回默认而不是中断装配（这一层没有可返回的错误通道）。
	unclosedTimeout, unclosedErr := cfg.IngestMultilineUnclosedTimeout()
	if unclosedErr != nil {
		slog.Warn("log_ingest.multiline_unclosed_timeout 无法解析，退回默认", "error", unclosedErr)
		unclosedTimeout = ingest.DefaultMultilineUnclosedTimeout()
	}
	// 整节点解算（POST /nodes/:id/log-runtime/ingest/resolve-gaps 无 storageNamespace 路径）的
	// 单次调用预算（键 log_ingest.resolve_gaps_max_sources / resolve_gaps_max_duration）。
	// 2026-10-02 压测现场：该路径此前整段持采集轮锁且无上界，一次调用让整节点采集停摆 30 分钟。
	// 锁纪律已在 ingest 侧修正；这条接线补上「有界」。非法值在 config.Load 阶段已被拒，
	// 此处同 unclosedTimeout 做防御性兜底（本层没有可返回的错误通道）。
	resolveGapsBudget, resolveGapsErr := cfg.IngestResolveGapsBudget()
	if resolveGapsErr != nil {
		slog.Warn("log_ingest.resolve_gaps_* 无法解析，退回默认预算", "error", resolveGapsErr)
		resolveGapsBudget = nil
	}
	logCapacityProvider := ingest.DiskCapacityProvider(root.Base(), acquire.CapacityBudget{
		MaxWALBytes: cfg.LogCapacity.MaxWALBytes, MaxGaps: cfg.LogCapacity.MaxGaps,
		DegradedAtPercent: cfg.LogCapacity.DegradedAtPercent, PauseAtPercent: cfg.LogCapacity.PauseAtPercent,
	})
	// 保留策略驱动器（G6）。声明在这里（而非 lifecycle 块内）是因为它要在采集运行时
	// 启动之后才有意义：搬运的是已经落库的分区，采集还没起来时搬没有意义。
	var logRetentionDriver *retention.Driver
	if store, journalErr := catalog.NewJSONLFileStore(root.Abs("var/log/catalog.journal.jsonl")); journalErr != nil {
		slog.Error("日志 Catalog journal 打开失败，查询面保持无权威分区", "error", journalErr)
	} else if journal, journalErr := catalog.NewJournalWithStore(store); journalErr != nil {
		slog.Error("日志 Catalog journal 恢复失败，查询面保持无权威分区", "error", journalErr)
	} else {
		logJournal = journal
		slog.Info("日志 Catalog journal 已恢复", "path", root.Abs("var/log/catalog.journal.jsonl"), "entries", journal.LastSeq())
	}
	logStack := logassemble.Build(logJournal, rangeClient, version.Version)
	logStack.LogRPC.SetRuntimeCatalog(logStack.Catalog)
	// FR-477：先完成 Catalog/journal 逻辑恢复，再开放 Log RPC；物理目录残留
	// 不能替代 owner/generation 权威。不可查询范围保持 recovery-required，
	// 不阻塞 Worker 其它实例服务。
	for key, recovery := range logStack.Catalog.StartingRecover() {
		if !recovery.Queryable {
			slog.Warn("日志分区启动恢复未完成，查询范围降级", "partition", key.String(), "reasons", recovery.PartialReasons)
		}
	}
	if vlHTTPClient != nil && coldVLClient != nil && vlSupervisor != nil {
		hotCfg, hotErr := vlSupervisor.Config(vlsup.NamespaceHot)
		coldCfg, coldErr := vlSupervisor.Config(vlsup.NamespaceCold)
		if hotErr != nil || coldErr != nil {
			slog.Error("日志 Lifecycle namespace 配置不可用", "hotError", hotErr, "coldError", coldErr)
		} else if physical, lifecycleErr := lifecycle.NewVLPartitionOps(vlHTTPClient, coldVLClient,
			hotCfg.StorageDataPath, coldCfg.StorageDataPath, logStack.Catalog); lifecycleErr != nil {
			slog.Error("日志 Lifecycle 物理适配器创建失败", "error", lifecycleErr)
		} else {
			dayManager := lifecycle.NewDayManager(logStack.Catalog, physical.Ops(), physical)
			// 保留策略驱动器（G6）：把 catalog 的日分区按「年龄 + 磁盘水位取先到」搬到冷层。
			// 没有它，HOT 永不自动转 COLD——plan/executor/Mover 都对，但没人周期性把它们串起来。
			if retentionPolicy, policyErr := cfg.RetentionPolicy(); policyErr != nil {
				slog.Error("日志保留策略非法，冷热分层驱动未启用", "error", policyErr)
			} else if retentionPolicy.Enabled {
				logRetentionDriver = retention.NewDriver(retentionPolicy,
					newCatalogPartitionLister(logStack.Catalog),
					newDayManagerMover(dayManager, logStack.Catalog),
					newDiskPercentReader(logCapacityProvider))
				slog.Info("日志保留策略已装配（默认只搬运不删除）", retentionPolicyLogFields(retentionPolicy)...)
			}
			resumedDays := make(map[string]bool)
			for _, key := range logStack.Catalog.Keys() {
				rec, ok := logStack.Catalog.Get(key)
				if !ok || rec.MigrationState == "" || rec.MigrationState == catalog.StateCleaned ||
					!rec.TargetOwner.Valid() || rec.TargetDirID == "" || rec.MigrationFromDirID == "" {
					continue
				}
				if resumedDays[key.UTCDay] {
					continue
				}
				resumedDays[key.UTCDay] = true
				if resumeErr := dayManager.Resume(key.UTCDay); resumeErr != nil {
					slog.Error("日志分区迁移启动恢复失败", "partition", key.String(), "error", resumeErr)
				}
			}
			logStack.LogRPC.SetPartitionMigrator(grpcsvc.PartitionMigrateFunc(func(ctx context.Context, namespace, day, target string) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if target != "cold" {
					return fmt.Errorf("unsupported lifecycle target %s", target)
				}
				key := catalog.PartitionKey{StorageNamespace: namespace, UTCDay: day}
				rec, ok := logStack.Catalog.Get(key)
				if !ok {
					return fmt.Errorf("log partition %s is not in Catalog", key)
				}
				if rec.Owner == catalog.OwnerCold {
					return nil
				}
				if rec.MigrationState != "" && rec.MigrationState != catalog.StateCleaned && rec.TargetOwner.Valid() {
					return dayManager.Resume(day)
				}
				parsed, err := time.Parse("2006-01-02", day)
				if err != nil {
					return err
				}
				return dayManager.Start(day, catalog.OwnerCold, parsed.Format("20060102"))
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
	// FR-478：归档 Registry/Rehydrate 进入同一 Worker 查询面。凭据只来自
	// 环境覆盖的配置对象，不写入日志或诊断响应。
	var archiveRegistry *archive.Registry
	var rehydrateManager *archive.RehydrateManager
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
			// 归档 manifest 记录受管 VL 的真实构建标识（FR-476 资产审批），而非占位。
			if vlsup.AssetBuildID != "" {
				registry.SetEngineVersion("victorialogs/" + vlsup.AssetBuildID)
			}
			archiveRegistry = registry
			rehydrate := archive.NewRehydrateManager(archive.RehydrateOptions{
				DefaultTimeout: 5 * time.Minute, DefaultLeaseTTL: 5 * time.Minute,
			})
			rehydrateManager = rehydrate
			backend := archive.NewQueryBackend(registry, rehydrate)
			if rehydrateVLClient != nil {
				backend.SetPublisher(&archive.VLRehydratePublisher{Catalog: logStack.Catalog, VL: rehydrateVLClient})
			}
			logStack.SetArchiveBackend(backend)
			slog.Info("日志归档运行时已装配", "provider", provider.Kind())
		}
	}
	// FR-474：将配置化 FileTailer/STDIO/ArchiveImporter 接入常驻 Worker。
	// 未配置 source 时不猜测实例目录；实例生命周期可通过同一 Manager 动态登记。
	var logIngest *ingest.Manager
	registeredLogInstanceIDs := manager.ListInstances
	if len(cfg.LogSources) > 0 || vlHTTPClient != nil {
		sources := make([]ingest.SourceConfig, 0, len(cfg.LogSources))
		for _, source := range cfg.LogSources {
			sources = append(sources, ingest.SourceConfig{
				LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration,
				Path: source.Path, Mode: pipeline.AcquireMode(source.Mode), RotateTo: source.RotateTo, ArchiveGlob: source.ArchiveGlob,
				Stream: source.Stream, SourceCategory: logtypes.Source(source.SourceCategory),
				StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay,
			})
		}
		// 启动增量对账（FR-497）：配置键 log_reconcile.*，非法/越界值经 ReconcileConfig()
		// 收敛为 ingest 的归一化默认（默认启用）。
		reconcileCfg := cfg.LogReconcile.ReconcileConfig()
		// 采集索引（FR-496）：历史投递批次裁剪（log_index.batch_prune.*）与持久化提交单元
		// 预算（log_index.persist.*）分别经 LogIndexConfig 的映射收敛为归一化默认（裁剪默认
		// 开启；切分默认每提交单元 512 行、目标 40ms——真源是 stateindex.DefaultCommitBudget）。
		persistYieldCfg, persistHotBudgetCfg, persistPriorityStreak := cfg.PersistGateTuning()
		verifyBudgetCfg, verifyMaxQueriesPerChunk := cfg.VerifyQueryTuning()
		indexPruneCfg := cfg.LogIndex.IndexPrune()
		indexCommitCfg := cfg.LogIndex.CommitBudget()
		newIngest := func() (*ingest.Manager, error) {
			return ingest.New(ingest.Options{
				Root: root.Base(), VL: vlHTTPClient, Catalog: logStack.Catalog,
				Journal: logStack.Catalog.Journal(), Archive: archiveRegistry, Sources: sources,
				// 采集归一化的节点级默认时区（缺陷 C，键 log_ingest.time_zone）：源未显式配置时
				// 生效，空串 = UTC（零配置零行为变化）。中文 locale 的 JVM 与 Worker 同机部署时
				// 配 local 即可对齐本地时间；非法值已在 config.Load 阶段启动即拒。
				DefaultTimeZone: cfg.IngestDefaultTimeZone(),
				// 采集归一化的节点级默认字符集（复审 P2-3，键 log_ingest.charset）：源未显式配置时
				// 生效，空串 = auto（零配置零行为变化）。GBK 日志源配 gbk 可免去自动判定的启发式
				// 不确定性；非法值已在 config.Load 阶段启动即拒。
				DefaultCharset:   cfg.IngestDefaultCharset(),
				CapacityProvider: logCapacityProvider,
				// 启动增量对账（FR-497）：按「源 × UTC 天」只补缺失天。
				Reconcile: &reconcileCfg,
				// 索引有界化（FR-496 §2.4）：历史投递批次按 reclaim 水位裁剪，默认开启。
				IndexPrune: &indexPruneCfg,
				// 索引持久化切分（FR-498 P0）：按行数 + 耗时双上界切成多个提交单元。
				IndexCommit: &indexCommitCfg,
				// 持久化门公平性（键 log_index.persist.cycle_yield / priority_streak）：
				// 让路窗口决定「恢复链 ↔ 采集轮」的权衡，老化阈值决定优先权的反饿保护。
				PersistYield:          persistYieldCfg,
				PersistHotBudget:      persistHotBudgetCfg,
				PersistPriorityStreak: persistPriorityStreak,
				// 校验/对账查询的全局天花板（键 log_index.verify.*）：天花板语义，正常路径零等待。
				VerifyBudget:             verifyBudgetCfg,
				VerifyMaxQueriesPerChunk: verifyMaxQueriesPerChunk,
				// 单源 WAL **真实积压**上限（键 log_capacity.max_wal_entries / max_wal_bytes）：
				// 直接下发到 WAL.SetLimits。此前 SetLimits 无任何调用点（现场只剩硬编码
				// 16MiB/5000），而配置里的 max_wal_bytes 被接到了只增不减的累计量上。
				// 恢复期独立配额（方案②）：恢复中/排空宽限内按 factor 倍判（仍有界 ✓），
				// 宽限过期回归常规闸 ✓。旋钮键 log_capacity.recovery_quota_factor /
				// recovery_drain_grace（默认 4 / 30m ✓）。
				RecoveryQuotaFactor: func() float64 { f, _ := cfg.RecoveryQuotaTuning(); return f }(),
				RecoveryDrainGrace:  func() time.Duration { _, g := cfg.RecoveryQuotaTuning(); return g }(),
				WALLimits: &acquire.WALLimits{
					MaxEntries: cfg.LogCapacity.MaxWALEntries,
					// 配置 0 = 字节维度**不限**（见 Config.WALBudgetNotice）⇒ 传负值哨兵，
					// 绝不能让它退化成 acquire 包的硬编码 16 MiB（2026-10-03 现场 16 MiB 之谜 ✗）。
					MaxBytes: cfg.EffectiveMaxWALBytes(),
				},
				// 回放限速（键 log_capacity.max_replay_events_per_drain）：恢复期单轮外发上限，
				// 把「恢复瞬间」从一次流量尖峰摊成若干轮，不丢数据。
				MaxReplayEventsPerDrain: cfg.LogCapacity.MaxReplayEvents,
				// 静默源的未闭合缓冲超时（键 log_ingest.multiline_unclosed_timeout，默认 5s）：
				// 到期未闭合即以显式标记强制闭合并推进 durable，否则悬挂区间会挡住轮转恢复。
				MultilineUnclosedTimeout: unclosedTimeout,
				// 整节点解算单次调用的预算（键 log_ingest.resolve_gaps_*）：源数 + 墙钟双上界，
				// 超出即分片返回（不伪装成功），见 ingest.ResolveGapsBudget。
				ResolveGapsBudget: resolveGapsBudget,
				// 对账/重发的外部条件与切片（键 log_reconcile.vl_ready_* / replay_*）：
				// 就绪探针把「VL 未就绪时的必然失败重发」挡在门外；切片把整窗重发变成有界可续。
				ReplayTuning: func() *ingest.ReplayTuning {
					tuning := cfg.ReconcileReplayTuning()
					return &tuning
				}(),
				// 段读聚合的让路与轮内预算（键 log_index.scan.*）：现场点名 readSegment 是每轮重活
				// 的 I/O 大头且"既不让路也无上界" ✗ ⇒ 由配置面驱动（默认 8192 行 / 1ms / 2s ✓）。
				Scan: func() *ingest.ScanTuning {
					tuning := cfg.ScanTuning()
					return &tuning
				}(),
				// 启动恢复放到后台：New 立刻返回 ⇒ 反向隧道/WS/HTTP 立即可达（2026-10-02 事故：
				// 恢复链与规模成正比且阻塞 New，worker 20–30 分钟不监听）。恢复进度经就绪面
				// （startup_recovery_in_progress / _failed）如实上报，不放宽任何就绪判据。
				StartupRecoveryBackground: true,
				RecoveryHold: func(source ingest.SourceConfig, _ string) (bool, string) {
					if rehydrateManager == nil {
						return false, ""
					}
					generation := uint64(1)
					if rec, ok := logStack.Catalog.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay}); ok && rec.Generation > 0 {
						generation = rec.Generation
					}
					key := archive.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay, Generation: generation}
					if !rehydrateManager.CanCleanup(key) {
						return true, "active archive rehydrate or query-view lease"
					}
					return false, ""
				},
				VLRoute: func(source ingest.SourceConfig) (*vlsup.Client, bool, error) {
					target, ok := logStack.Catalog.RouteWrite(catalog.PartitionKey{
						StorageNamespace: source.StorageNamespace, UTCDay: source.UTCDay,
					}, source.UTCDay)
					if !ok {
						return vlHTTPClient, false, nil
					}
					switch target.Owner {
					case catalog.OwnerHot:
						return vlHTTPClient, target.Frozen, nil
					case catalog.OwnerCold:
						return coldVLClient, target.Frozen, nil
					default:
						return nil, target.Frozen, fmt.Errorf("ingest: Catalog write owner %s requires an explicit recovery path", target.Owner)
					}
				},
			})
		}
		// 采集运行时创建失败不再一次性放弃（2026-09-28 生产教训：一次瞬时失败即让整个采集
		// 静默停摆——无重试、无告警，实例在跑而日志不再入库）。带退避重试，使 VL 就绪延迟或
		// 瞬时故障恢复后能自愈；仍失败则打出显式 ERROR（供日志告警规则捕获）。
		const (
			ingestCreateAttempts   = 6
			ingestCreateRetryDelay = 20 * time.Second
		)
		var manager *ingest.Manager
		var ingestErr error
		for attempt := 1; attempt <= ingestCreateAttempts; attempt++ {
			manager, ingestErr = newIngest()
			if ingestErr == nil {
				break
			}
			slog.Error("日志采集运行时创建失败（将重试）",
				"attempt", attempt, "maxAttempts", ingestCreateAttempts, "error", ingestErr)
			if attempt < ingestCreateAttempts {
				time.Sleep(time.Duration(attempt) * ingestCreateRetryDelay)
			}
		}
		if ingestErr != nil {
			slog.Error("日志采集运行时创建失败：已达重试上限，本次启动不再采集（需告警介入）", "error", ingestErr)
			register.SetLogIngestHealth(false, ingestErr.Error())
		} else {
			logIngest = manager
			register.SetLogIngestHealth(true, "")
			workerServer.SetInstanceLogCollector(manager)
			logStack.LogRPC.SetCutoverReadiness(grpcsvc.CutoverReadinessFunc(func() (bool, time.Time, []string) {
				readiness := manager.PrepareCutoverReadiness()
				for _, id := range manager.MissingInstanceBindings(registeredLogInstanceIDs()) {
					readiness.LedgerReady = false
					readiness.Reasons = append(readiness.Reasons, "instance:"+id+":acquisition_not_bound")
				}
				return readiness.LedgerReady, readiness.CutoffTime, readiness.Reasons
			}))
			logStack.LogRPC.SetIngestGapResolver(manager)
			// 手动归档导入入口（④）：把「扫描并导入待导入 gz」接到 gRPC 上，
			// 供采集轮停摆（源被暂停/停止）时由运维显式触发；操作人经 metadata 携带。
			logStack.LogRPC.SetIngestArchiveImporter(manager)
			// 「已放弃位置」查询面标记（缺口 B 批2）：把 ingest 的放弃凭据接到搜索响应上，
			// 使下游能区分「本来就没有」与「已被确认永久丢失」——不再静默跳过。
			// 跨包适配放在这里（apps 同时依赖 ingest 与 grpcsvc），服务层不反向依赖 ingest。
			logStack.LogRPC.SetIngestAbandonmentProvider(grpcsvc.IngestAbandonmentFunc(
				func(targetIDs []string) []grpcsvc.IngestPositionGap {
					ranges := manager.AbandonedRanges(targetIDs)
					if len(ranges) == 0 {
						return nil
					}
					out := make([]grpcsvc.IngestPositionGap, 0, len(ranges))
					for _, r := range ranges {
						out = append(out, grpcsvc.IngestPositionGap{
							StorageNamespace: r.StorageNamespace, From: r.From, To: r.To,
							ReasonCode: r.ReasonCode, Operator: r.Operator, AtUTC: r.AtUTC,
						})
					}
					return out
				}))
			ingestCtx, cancelIngest := context.WithCancel(context.Background())
			defer cancelIngest()
			defer func() {
				if stopErr := manager.Stop(); stopErr != nil {
					slog.Error("日志采集运行时停止持久化失败", "error", stopErr)
				}
			}()
			go manager.Start(ingestCtx)
			// 定时归档导入扫描（④，键 log_ingest.archive_scan_interval）：**默认关**。
			// 打开后按周期把「躺在源目录里但还没被导入」的 gz 走一遍与自动路径完全相同的
			// 导入管道；关闭时 RunArchiveScan 返回 nil（不启动任何 goroutine）。
			if scanInterval, scanErr := cfg.IngestArchiveScanInterval(); scanErr != nil {
				slog.Warn("log_ingest.archive_scan_interval 无法解析，定时扫描保持关闭", "error", scanErr)
			} else if stopScan := manager.RunArchiveScan(scanInterval); stopScan != nil {
				slog.Info("定时归档导入扫描已启用", "interval", scanInterval)
				defer stopScan()
			}
			// 采集索引持久化耗时采样（FR-496 spec §3.3）：每分钟以 Debug 打印 P50/P95/Max 与
			// 本窗口写入行数/字节数，供真机「60 源单次持久化 ≤50ms」验收直接取证
			// （整本重写会让写入行数逼近索引总行数，读数一眼可辨）。
			go sampleLogIndexPersistLatency(ingestCtx, manager)
			// 保留策略驱动器：首轮立即执行（启动时正是「停机期间攒下的过期分区」最需要处理的时刻），
			// 之后按 log_retention.sweep.interval 周期跑。与采集运行时共用同一 ctx 生命周期。
			if logRetentionDriver != nil {
				go logRetentionDriver.Run(ingestCtx)
				slog.Info("日志冷热分层驱动器已启动", "interval", logRetentionDriver.Interval().String())
			}
			slog.Info("日志采集运行时已启动", "sources", len(sources), "vlReady", vlHTTPClient != nil)
		}
	}
	if rangeClient == nil {
		slog.Info("Worker 日志查询面已装配", "buildId", version.Version, "rangeClient", "unimplemented-stub")
	}
	// Worker 升级二进制下载与服务端 jar 下载经进程级出站持有者（FR-174/FR-185）：
	// CP 下发代理改动运行时即时生效。
	workerServer.SetHTTPClientProvider(outboundProvider.Client)
	// 非 JDK 运行时安装管理器（FR-299，首批 Node.js）：托管根 <dataRoot>/opt/runtimes，
	// 下载经进程级出站持有者（代理改动运行时即时生效，同 JDK）。
	runtimeMgr := wruntime.NewManager(root.RuntimesDir())
	runtimeMgr.SetHTTPClientProvider(outboundProvider.Client)
	workerServer.SetRuntimeManager(runtimeMgr)
	// 运行时扫描器（FR-298 节点运行时库）：ScanRuntimes 按常见安装路径发现 jdk/nodejs 候选；
	// JDK 托管根与运行时托管根传入，其下候选标 already_registered。
	var runtimeManagedRoots []string
	if jdkMgr != nil {
		runtimeManagedRoots = append(runtimeManagedRoots, jdkMgr.RootDir())
	}
	runtimeManagedRoots = append(runtimeManagedRoots, runtimeMgr.RootDir())
	runtimeScanner := runtimescan.New(runtimeManagedRoots)
	// FR-299×FR-300 胶合：托管安装根不在默认 node 路径表内，动态追加两种布局
	// （linux tar.gz 解出 bin/node；windows zip 顶层 node.exe），使一键安装的托管 Node
	// 能被扫描发现并被 Bot spawn 解析器选中。
	runtimeScanner.AddNodeGlobs(
		filepath.Join(runtimeMgr.RootDir(), "nodejs-*", "bin", "node"),
		filepath.Join(runtimeMgr.RootDir(), "nodejs-*", "*", "bin", "node"),
		filepath.Join(runtimeMgr.RootDir(), "nodejs-*", "node.exe"),
		filepath.Join(runtimeMgr.RootDir(), "nodejs-*", "*", "node.exe"),
	)
	workerServer.SetRuntimeScanner(runtimeScanner)
	// 包管理器（FR-306）：托管 .npmrc 与 corepack 激活落 opt/runtimes（与 nodejs-* 平级）。
	workerServer.SetPkgManager(pkgmgr.NewManager(runtimeMgr.RootDir()))
	// 全文搜索追加忽略规则（worker.yml search.ignore，叠加内置默认集，FR-074）。
	workerServer.SetSearchIgnore(cfg.Search.Ignore)
	// 节点制品缓存（FR-178）：按 sha256 缓存下载过的核心 jar（var/artifact-cache），建实例命中即秒拷免重下。
	// 容量上限来自 worker.yml artifact_cache.max_bytes（0=不限），可经 CP 端点运行时下发覆盖。
	artifactCache := artifactcache.New(root.ArtifactCacheDir())
	artifactCache.SetCap(cfg.ArtifactCache.MaxBytes)
	workerServer.SetArtifactCache(artifactCache)
	slog.Info("节点制品缓存已启用", "dir", root.ArtifactCacheDir(), "maxBytes", cfg.ArtifactCache.MaxBytes)

	// Bot 管理器：按需 spawn bot-worker(Node) 子进程，经 stdin/stdout IPC 管理 Mineflayer Bot。
	// 入口脚本解析顺序（FR-308 见 ADR-072；FR-286 迁移见 ADR-064）：
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
	// FR-300/308：扫描只提供候选路径，NodeResolver 会逐个真实探测并强制 >=22.13.0。
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

	// 反编译器（FR-075，见 ADR-018）：解析 CFR jar（配置路径>内嵌>数据根缓存>按需下载 sha256 pin），
	// 缓存落数据根 cache/tools；反编译经实例/系统 JDK 受控调起 CFR，只读+超时+体积上限+失败降级。
	decompProvider := decompiler.NewProvider(decompiler.Config{
		ConfigPath:    cfg.Decompiler.CFRPath,
		CacheDir:      filepath.Join(root.CacheDir(), "tools"),
		Embedded:      wembed.CFRJar,
		AllowDownload: cfg.Decompiler.AllowDownload,
		// CFR 按需下载经进程级出站持有者（FR-174/FR-185）：CP 下发代理改动运行时即时生效。
		HTTPClientProvider: outboundProvider.Client,
	})
	workerServer.SetDecompiler(decompProvider)

	// 终端会话隧道桥（FR-281 M2，见 ADR-066）：TerminalSession 回环拨本机 WS 终端服务，
	// 令牌校验/会话层单一真源仍是 ws.TerminalServer。
	workerServer.SetTerminalWSAddr(fmt.Sprintf("ws://127.0.0.1:%d/ws/terminal", wsPort))

	// WS 服务在节点完成注册并取得非空专用密钥后才监听，避免以空密钥暴露入口。
	terminalServer := ws.NewTerminalServer(wsTokenSecret)

	// 桥接进程输出：一份给 WebSocket 终端（交互），一份给 StreamInstanceEvents 事件流（CP 采集落库，FR-049）。
	// 两条路径相互独立，从同一份进程输出分流，互不阻塞。
	manager.SetOutputHandler(func(instanceID string, stream string, data []byte) {
		text := string(data)
		terminalServer.Broadcast(instanceID, stream, text)
		workerServer.EmitOutput(instanceID, stream, text)
		if logIngest != nil {
			if err := logIngest.AppendInstanceOutput(instanceID, stream, data); err != nil {
				if errors.Is(err, ingest.ErrInstanceBindingPending) {
					slog.Debug("实例日志绑定尚未完成，输出已写入 pending spool", "instanceId", instanceID, "stream", stream)
				} else {
					slog.Error("实例日志受管 Raw 持久化失败", "instanceId", instanceID, "stream", stream, "error", err)
				}
			}
		}
	})

	// FR-471 缺陷修复：实例每次开始新一轮运行即重置其终端环形缓冲，使崩溃快照（FR-313）截取的
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

	// 插件桥服务端（ServerProbe 反向 WS，FR-065，见 ADR-016）：与终端 WS 并列、同一监听端口。
	// 探针主动连入 /ws/plugin-bridge，事件经 gRPC StreamPluginEvents 冒泡到 CP；token 校验使用 CP 下发的专用 WS 密钥。
	pluginBridge := ws.NewPluginBridgeServer(wsTokenSecret)
	workerServer.SetPluginBridge(pluginBridge)

	wsMux := http.NewServeMux()
	wsMux.HandleFunc("/ws/terminal", terminalServer.Handler())
	wsMux.HandleFunc("/ws/plugin-bridge", pluginBridge.Handler())

	wsAddr := localWSAddr(wsPort)
	wsServer := &http.Server{Addr: wsAddr, Handler: wsMux}

	// 注册到 Control Plane（FR-080，见 ADR-020）。
	// Control Plane 未启动时 Worker 不退出，按指数退避重试直到注册成功。
	// 崩溃快照上报（FR-313）：进程非正常退出时，把策略捕获的退出码/信号/时长 +
	// 终端环形缓冲的尾部输出（最后 200 行 / 64KB）组装为快照，异步经 gRPC 上报 CP
	// 持久化。上报失败（网络 / 老 CP Unimplemented）记日志丢弃，不阻塞状态机。
	crashReporter := crashreport.New(cpAddr)
	manager.SetCrashHandler(func(instanceID string, info process.CrashInfo) {
		crashReporter.Report(crashreport.Snapshot{
			InstanceUUID: instanceID,
			OccurredAt:   info.OccurredAt,
			ExitCode:     info.ExitCode,
			// FR-467：docker 容器被 cgroup OOM killer 杀掉时，宿主侧拿不到信号，
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
		// 本次启动由 setup 完成首注册并已持久化身份（FR-222，见 ADR-051）：
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

		// 首次注册成功后持久化身份（含 node_secret，0600），重启复用、不重复消费 token（FR-080）。
		// CP 下发的 WS 令牌密钥一并持久化（FR-275，见 ADR-061）。
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
				// 重注册带来新 WS 令牌密钥（存量节点升级 / CP 轮换后的主路径，FR-275）：补写身份文件。
				identity.WSTokenSecret = regResult.WSTokenSecret
				if err := register.SaveIdentity(etcDir, identity); err != nil {
					slog.Warn("持久化 WS 令牌密钥失败（内存已生效，重启后经注册自愈）", "error", err)
				}
			}
			identityForPersist = identity
		}
	}

	// 节点身份就绪后崩溃快照上报可用（FR-313）：两条注册路径（setup 首注册 / 常规注册）在此汇合。
	crashReporter.SetIdentity(nodeUUID, regResult.NodeSecret)
	// 孤儿处置审计上报（FR-455/456）同样就绪：此后运行期扫描/接管兜底的处置动作可落 CP 审计库。
	orphanAuditReporter.SetIdentity(nodeUUID, regResult.NodeSecret)

	// bot-worker dist 自愈下发（FR-308，见 ADR-072）：注册成功即持身份从 CP 拉取内嵌归档，
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

	// CP 下发的 WS 令牌密钥热应用（FR-275，见 ADR-061）：注册响应非空即应用到终端/插件桥。
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
		if err := wsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("WS 服务器退出", "error", err)
		}
	}()

	// 启动心跳上报（携带注册获得的 node_secret 供 Control Plane 鉴权）。
	// 节拍取共享契约值 directprobe.HeartbeatInterval（ADR-013 的 30s）：单拍采集预算的上界正是
	// 由它反推（见 internal/platform/directprobe），两处必须同源，否则「预算 < 节拍」的护栏失效。
	hb := heartbeat.New(cpAddr, nodeUUID, regResult.NodeSecret, directprobe.HeartbeatInterval, manager)
	// 受管运行时快照随反向隧道 Heartbeat 上报；只读取既有 Worker/Bot Worker，不会拉起 Bot 子进程。
	hb.SetManagedRuntimeProvider(workerServer)
	// 运行中长任务进度随心跳上报（FR-183，见 ADR-040）：心跳读 Worker gRPC Server 的内存任务表。
	hb.SetTaskProvider(workerServer)
	// CP 经心跳响应下发节点期望出站代理（FR-185，见 ADR-043）：generation 变化时重建出站持有者，
	// 注入到各下载点（JDK/CFR/自更新/服务端 jar）即时生效；下发为空回退本地 worker.yml/env。
	hb.SetProxyRebuilder(func(c httpclient.Config) error {
		if err := outboundProvider.Rebuild(c); err != nil {
			slog.Warn("据心跳下发重建出站代理失败", "proxy", httpclient.Sanitize(c.URL), "error", err)
			return err
		}
		slog.Info("出站代理已据 CP 下发运行时更新", "proxy", httpclient.Sanitize(c.URL), "noProxy", c.NoProxy)
		return nil
	})
	// CP 经心跳下发 WS 令牌密钥（FR-275，见 ADR-061）：值变化即热更新终端/插件桥校验并补写
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
	// CP 经心跳响应下发实例健康巡检策略（FR-459）：阈值/动作/探针类型/熔断阈值写入巡检器生效值，
	// 无需重启即生效；本地开关（worker.health_scan_disabled）为硬关，CP 无法远程开启。
	hb.SetHealthPolicyApplier(func(p process.HealthPolicy) {
		healthScanner.SetPolicy(p)
	})
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
	// 受管 VL 必须随 Worker 一起回收：否则它们会成为孤儿并继续占用 hot/cold/rehydrate
	// 端口，导致下次启动的新 VL 因端口被占而立即退出（真机事故），采集面长期降级。
	if vlSupervisor != nil {
		vlSupervisor.StopAll(shutdownCtx)
	}
	slog.Info("Worker Node 已停止")
}

// localWSAddr 返回仅供本机终端回环桥与本机探针使用的 WebSocket 监听地址。
func localWSAddr(port int) string {
	return fmt.Sprintf("127.0.0.1:%d", port)
}

// sampleLogIndexPersistLatency 周期性打印采集索引（FR-496，本地 SQLite）的单次持久化耗时
// P50/P95/Max 与本窗口的写入行数/删除行数/字节数。
//
// 为什么以 Info 打印：真机验收口径是「60 源规模下单次持久化 ≤50ms」，而现场默认日志级别是
// Info——Debug 打点在真机上默认不落盘，验收人只能临时改级别才能读数，读数口径随人而变。
// 升为 Info 后现场按默认配置即可取值，且节拍（1 分钟）与字段原样不变，仍可与旧格式
// （整本重写 JSON）直接对比：写入行数是最直接的判据——回到整本重写时该值会逼近索引总行数，
// 而增量实现只随本批次变更增长。采样环由 stateindex 维护（最近 4096 次，见 stateindex.sampleRing）。
//
// 三个 Delta 的窗口口径见 logIndexPersistWindow 的注释：它们与 P50/P95/Max 取自同一个窗口，
// 都是「本窗口新增」。
func sampleLogIndexPersistLatency(ctx context.Context, manager *ingest.Manager) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	// 首个窗口的基准取采样器启动时刻（它与采集循环同时启动，见 main 里的 `go manager.Start`）：
	// 早于这一刻的持久化不属于任何一个窗口，不会被计入首条日志。
	window := &logIndexPersistWindow{cursor: time.Now()}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			latency := manager.PersistLatency()
			if latency.Count == 0 {
				// 本窗口一次都没持久化（空闲），环里没有新增样本可结账——保持基准不动，
				// 下一个窗口把这段时间的持久化一并结清。
				continue
			}
			logIndexPersistSample(window, latency, manager.PersistSamples())
		}
	}
}

// logIndexPersistWindow 把 stateindex 采样环换算成「本窗口的增量」。
//
// 为什么不能拿「环和」相减当增量：采样环只保留最近 4096 次（提交单元）持久化，而采集循环每 250 ms 就可能触发
// 一次 persist，真机 60 源实测约 2.5 次/s（环覆盖 ≈205 s），环在启动几分钟后就会写满。写满后
// 「环和」是滑动窗口和，相邻两次读数相减得到的是「本窗口新增 − 同期被挤出的旧样本」——稳态下
// 两者近似相等，差值趋近 0，会把整整一分钟的写入量抹掉；环未满时「环和」又等于自启动累计值，
// 直接当成窗口量上报，就是把「开服至今的写入」当成「一分钟的写入」（FR-498 实测：删除因此被读成
// 写入的 9 倍，而同口径真值约为删除 ≈ 0.5 × 写入）。两种形态都不是窗口增量。
//
// 所以这里按「样本身份」做差：窗口基准是一个时间游标，即上一次读数时环内最新样本的 StartedAt，
// 每次只累计 StartedAt 严格晚于游标的样本。不管环满没满、有没有挤出旧样本，累计到的都恰好是
// 「两次读数之间新增的那几次持久化」——这才叫窗口增量。
//
// 前提：单个窗口内新增的样本数少于环容量（4096，见 stateindex.sampleRing）。否则最早的那几次
// 新增样本已被挤出环外，读数只能是下界。生产上采集循环节拍 250 ms、采样节拍 1 分钟，单窗口最多
// 约 240 次持久化周期；切分（FR-498 P0）后一次周期会产生多个**提交单元**采样，60 源 × 60 行/s
// 实测约 2 单元/周期 ⇒ 单窗口约 400 个样本，仍远小于 4096（环容量正是为此从 512 提到 4096）。
type logIndexPersistWindow struct {
	// cursor 是上一次读数时环内最新样本的 StartedAt。采样时间戳由 recordSample 在每次 Apply
	// 开始时用 time.Now() 记录（带单调读数），同一进程内严格递增，可当环内样本的身份游标用。
	cursor time.Time
}

// logIndexPersistDelta 是一个窗口内的写入增量，与同一条日志里的 P50/P95/Max 同窗口。
type logIndexPersistDelta struct {
	RowsWritten  int64
	RowsDeleted  int64
	BytesWritten int64
}

// advance 推进一个窗口并返回本窗口增量：只累计游标之后的样本，因此环未满（环和 = 自启动累计）
// 与环已满（环和 = 滑动和）两种形态下都成立。
func (w *logIndexPersistWindow) advance(samples []stateindex.Sample) logIndexPersistDelta {
	var delta logIndexPersistDelta
	var newest time.Time
	for _, sample := range samples {
		if sample.StartedAt.After(newest) {
			newest = sample.StartedAt
		}
		if !sample.StartedAt.After(w.cursor) {
			continue
		}
		delta.RowsWritten += int64(sample.RowsWritten)
		delta.RowsDeleted += int64(sample.RowsDeleted)
		delta.BytesWritten += sample.BytesWritten
	}
	// 游标只前进不后退：时钟回拨时若直接覆写，已结账的样本会被重复计入下一个窗口。
	if newest.After(w.cursor) {
		w.cursor = newest
	}
	return delta
}

// logIndexPersistSample 把一个窗口的读数落成一条采样日志：分位取整环（口径不变），三个 Delta
// 取本窗口增量（见 logIndexPersistWindow）。
//
// 口径（FR-498 切分改造）：P50/P95/Max 是**单个提交单元**（一次 IMMEDIATE 事务）的耗时——
// 验收线「单次持久化 ≤50ms」约束的就是它，一个周期按行数 + 耗时双上界切成若干个单元
// （见 stateindex.CommitBudget）。`cycles` 是环内周期数、`samples` 是单元数，两者之比即
// 「一周期切了几个单元」；`maxRows` 是环内单个单元的最大写+删行数（每事务行数有界的直读值）。
func logIndexPersistSample(window *logIndexPersistWindow, latency stateindex.Latency, samples []stateindex.Sample) {
	delta := window.advance(samples)
	slog.Info("采集索引持久化耗时采样",
		"samples", latency.Count,
		"cycles", latency.Cycles,
		"p50ms", latency.P50.Milliseconds(),
		"p95ms", latency.P95.Milliseconds(),
		"maxMs", latency.Max.Milliseconds(),
		"maxRows", latency.MaxRows,
		"rowsWrittenDelta", delta.RowsWritten,
		"rowsDeletedDelta", delta.RowsDeleted,
		"bytesWrittenDelta", delta.BytesWritten,
	)
}

// sampleLogBudget 周期性采样受管 VL 的 RSS 与数据盘预算并暴露降级（FR-476 / 契约 §6.6）。
// 每次采样以 Debug 记录实际数值（供 Runbook C 取证）；状态变化时升为 Info/Warn。
func sampleLogBudget(sup *vlsup.Supervisor) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	last := vlsup.BudgetOK
	for range ticker.C {
		verdict := sup.SampleBudget()
		slog.Debug("日志资源预算采样",
			"state", string(verdict.State),
			"vlRssBytes", verdict.Sample.ProcessRSSBytes,
			"diskUsagePercent", verdict.Sample.DiskUsagePercent,
		)
		if verdict.State == last {
			continue
		}
		if verdict.State == vlsup.BudgetOK {
			slog.Info("日志资源预算恢复正常", "state", string(verdict.State))
		} else {
			slog.Warn("日志资源预算降级", "state", string(verdict.State), "reasons", verdict.Reasons)
		}
		last = verdict.State
	}
}
