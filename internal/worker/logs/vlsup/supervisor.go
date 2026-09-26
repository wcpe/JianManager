package vlsup

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync"
	"time"
)

// InstanceState supervisor 账面进程状态。
// 注意：进程健康、分区恢复完成、查询完整可用是三个独立状态（契约 §5.3 / FR-476 §3.1）。
type InstanceState string

const (
	StateStopped  InstanceState = "STOPPED"
	StateStarting InstanceState = "STARTING"
	StateRunning  InstanceState = "RUNNING"
	StateStopping InstanceState = "STOPPING"
	StateFailed   InstanceState = "FAILED"
)

// InstanceStatus 对外可见的实例状态快照。
type InstanceStatus struct {
	Namespace       Namespace
	State           InstanceState
	Port            int
	ListenAddr      string
	StorageDataPath string
	PID             int
	AssetTag        string
	AssetBuildID    string
	AssetSHA256     string
	LastError       string
	// HealthOK 表示最近一次 HTTP /health 成功；不蕴含分区恢复或查询可用。
	HealthOK bool
	// PartitionRecoveryComplete 分区恢复完成；foundation 不自动推断，保持 false 直至上层登记。
	PartitionRecoveryComplete bool
	// QueryReady 查询范围完整可用；同上，与 HealthOK 分离。
	QueryReady bool
}

// Options 构造 Supervisor。
type Options struct {
	// BinaryPath victoria-logs 可执行文件路径。
	BinaryPath string
	// AssetSHA256 期望的解包可执行文件 SHA-256；每次 Start 前 VerifyAsset。
	AssetSHA256 string
	// DataRoot 受管数据根；各 namespace 使用 DataRoot/<ns>/data。
	DataRoot string
	// RetentionPeriod VL runtime retention；空则 DefaultRetentionPeriod。
	RetentionPeriod string
	// AuthUsername / AuthPassword 本地 Basic-auth；生产必须非空。
	AuthUsername string
	AuthPassword string
	// Ports 可选端口覆盖；缺省 DefaultPort(ns)。
	Ports map[Namespace]int
	// MemoryAllowedBytes 可选 per-namespace cache 覆盖；HOT 缺省 DefaultHotCacheBytes。
	MemoryAllowedBytes map[Namespace]int64
	// MemoryLimitBytes 受管 VL 进程的 Go 软内存上限（GOMEMLIMIT，字节）。
	// 0 使用 DefaultVLMemoryLimitBytes；负值表示不注入（保留 VL 默认行为）。
	// 用于约束单 Worker 日志数据面 RSS（契约 §6.6）。
	MemoryLimitBytes int64
	// Factory 进程工厂；nil 使用 RealFactory。测试注入 fake。
	Factory CmdFactory
	// Sink 独立失败 sink；nil 使用 slog 默认 IndependentLoggerSink。
	Sink Sink
	// AllowVLRecursiveSink 守卫标志。生产必须为 false：
	// supervisor 进程错误不得递归写回 VL 日志管道。
	AllowVLRecursiveSink bool
	// AssetTag / AssetBuildID 写入 status 的资产标识；空则使用 AssetTag/AssetBuildID 常量。
	AssetTag     string
	AssetBuildID string
	// HealthClient 可选：按实例 base URL 构造健康客户端；nil 时用 NewClient。
	HealthClient func(baseURL, user, pass string) (*Client, error)
}

type instance struct {
	cfg    InstanceConfig
	state  InstanceState
	proc   Process
	status InstanceStatus
}

// Supervisor 管理 named VL 实例（hot|cold|rehydrate）。
type Supervisor struct {
	binPath     string
	assetSHA    string
	dataRoot    string
	retention   string
	authUser    string
	authPass    string
	ports       map[Namespace]int
	memOverride map[Namespace]int64
	factory     CmdFactory
	sink        Sink
	allowVLRec  bool
	assetTag    string
	assetBuild  string
	newClient   func(baseURL, user, pass string) (*Client, error)

	mu          sync.Mutex
	instances   map[Namespace]*instance
	budget      CacheBudget
	guardBlocks int
}

// DefaultVLMemoryLimitBytes 受管 VL 进程的默认 Go 软内存上限。
//
// 取值依据：契约 §6.6「单 Worker 日志数据面 RSS ≤ 1GiB（不含 page cache）」。Worker 进程自身约
// 0.3GiB，故给单 VL 进程 512MiB 软上限，使默认配置（HOT-only）整体留在 1GiB 以内。
// GOMEMLIMIT 是 Go 软上限（引导 GC 更早回收），不是硬 cgroup 限制。
const DefaultVLMemoryLimitBytes int64 = 512 << 20

// vlProcessEnv 依据配置构造受管 VL 进程的额外环境变量。
// MemoryLimitBytes<=0 且非负默认时注入 DefaultVLMemoryLimitBytes；显式负值表示不注入。
func vlProcessEnv(memoryLimitBytes int64) []string {
	limit := memoryLimitBytes
	if limit == 0 {
		limit = DefaultVLMemoryLimitBytes
	}
	if limit < 0 {
		return nil
	}
	return []string{fmt.Sprintf("GOMEMLIMIT=%dB", limit)}
}

// New 创建 supervisor。缺失 BinaryPath / AssetSHA256 / DataRoot 或空鉴权时返回错误。
func New(opts Options) (*Supervisor, error) {
	if opts.BinaryPath == "" {
		return nil, fmt.Errorf("vlsup: BinaryPath is required")
	}
	if opts.AssetSHA256 == "" {
		return nil, fmt.Errorf("vlsup: AssetSHA256 is required")
	}
	if opts.DataRoot == "" {
		return nil, fmt.Errorf("vlsup: DataRoot is required")
	}
	if opts.AuthUsername == "" || opts.AuthPassword == "" {
		return nil, fmt.Errorf("vlsup: local basic-auth username/password are required")
	}
	factory := opts.Factory
	if factory == nil {
		// 默认真实工厂：注入 VL 进程级 Go 软内存上限（GOMEMLIMIT），使受管 VL RSS 受预算约束
		// （契约 §6.6 单 Worker 日志数据面 RSS ≤ 1GiB；-memory.allowedBytes 只约束 cache 不约束 Go heap）。
		factory = RealFactory{Env: vlProcessEnv(opts.MemoryLimitBytes)}
	}
	sink := opts.Sink
	if sink == nil {
		sink = IndependentLoggerSink{}
	}
	retention := opts.RetentionPeriod
	if retention == "" {
		retention = DefaultRetentionPeriod
	}
	tag := opts.AssetTag
	if tag == "" {
		tag = AssetTag
	}
	build := opts.AssetBuildID
	if build == "" {
		build = AssetBuildID
	}
	newClient := opts.HealthClient
	if newClient == nil {
		newClient = func(baseURL, user, pass string) (*Client, error) {
			return NewClient(ClientOptions{BaseURL: baseURL, Username: user, Password: pass})
		}
	}
	s := &Supervisor{
		binPath:     opts.BinaryPath,
		assetSHA:    opts.AssetSHA256,
		dataRoot:    opts.DataRoot,
		retention:   retention,
		authUser:    opts.AuthUsername,
		authPass:    opts.AuthPassword,
		ports:       map[Namespace]int{},
		memOverride: map[Namespace]int64{},
		factory:     factory,
		sink:        sink,
		allowVLRec:  opts.AllowVLRecursiveSink,
		assetTag:    tag,
		assetBuild:  build,
		newClient:   newClient,
		instances:   map[Namespace]*instance{},
		budget:      DefaultCacheBudget(),
	}
	for _, ns := range []Namespace{NamespaceHot, NamespaceCold, NamespaceRehydrate} {
		port := DefaultPort(ns)
		if p, ok := opts.Ports[ns]; ok && p > 0 {
			port = p
		}
		s.ports[ns] = port
		if m, ok := opts.MemoryAllowedBytes[ns]; ok {
			s.memOverride[ns] = m
		}
		s.instances[ns] = &instance{
			cfg:   s.configFor(ns),
			state: StateStopped,
			status: InstanceStatus{
				Namespace:       ns,
				State:           StateStopped,
				Port:            port,
				ListenAddr:      ListenAddr(port),
				StorageDataPath: StoragePathUnder(s.dataRoot, ns),
				AssetTag:        tag,
				AssetBuildID:    build,
				AssetSHA256:     opts.AssetSHA256,
			},
		}
	}
	return s, nil
}

func (s *Supervisor) configFor(ns Namespace) InstanceConfig {
	port, ok := s.ports[ns]
	if !ok || port <= 0 {
		port = DefaultPort(ns)
	}
	return InstanceConfig{
		Namespace:          ns,
		Port:               port,
		StorageDataPath:    StoragePathUnder(s.dataRoot, ns),
		RetentionPeriod:    s.retention,
		MemoryAllowedBytes: s.memOverride[ns],
		AuthUsername:       s.authUser,
		AuthPassword:       s.authPass,
	}
}

// Config 返回 namespace 的启动配置副本。
func (s *Supervisor) Config(ns Namespace) (InstanceConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inst, ok := s.instances[ns]
	if !ok {
		return InstanceConfig{}, fmt.Errorf("vlsup: unknown namespace %q", ns)
	}
	return inst.cfg, nil
}

// Start 校验资产并启动指定 namespace 实例。
// 流程：VerifyAsset → BuildArgs → Factory.Start → 记录 tag/build id 到 status。
func (s *Supervisor) Start(ctx context.Context, ns Namespace) error {
	s.mu.Lock()
	inst, ok := s.instances[ns]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("vlsup: unknown namespace %q", ns)
	}
	if inst.state == StateRunning {
		s.mu.Unlock()
		return fmt.Errorf("vlsup: instance %s already running", ns)
	}
	inst.state = StateStarting
	inst.status.HealthOK = false
	inst.status.QueryReady = false
	cfg := inst.cfg
	s.mu.Unlock()

	fail := func(err error) error {
		s.mu.Lock()
		inst.state = StateFailed
		inst.status.State = StateFailed
		inst.status.LastError = err.Error()
		s.mu.Unlock()
		if blocked := reportViaSink(s.sink, s.allowVLRec, string(ns), err); blocked {
			s.mu.Lock()
			s.guardBlocks++
			s.mu.Unlock()
		}
		return err
	}

	if err := VerifyAsset(s.binPath, s.assetSHA); err != nil {
		return fail(err)
	}
	// 启动前确保 namespace 数据目录存在：VL 不会自建数据根，缺失时进程立即退出
	// （真机实测：控制面 start rehydrate 报成功但进程变僵尸、端口未监听）。
	if cfg.StorageDataPath != "" {
		if err := os.MkdirAll(cfg.StorageDataPath, 0o700); err != nil {
			return fail(fmt.Errorf("vlsup: prepare %s data dir: %w", ns, err))
		}
	}
	args, err := BuildArgs(cfg)
	if err != nil {
		return fail(err)
	}
	proc, err := s.factory.Start(ctx, s.binPath, args)
	if err != nil {
		return fail(fmt.Errorf("vlsup: start %s: %w", ns, err))
	}
	// 启动后必须确认进程真存活：Start 成功只代表 fork/exec 成功，二进制仍可能立即退出
	// （真机实测：数据目录缺失时 VL 秒退，控制面却报「启动成功」）。此处给一个极短窗口
	// 让失败进程暴露，并把已退出的进程句柄立即回收，避免僵尸堆积。
	if exited, waitErr := waitStartupLiveness(ctx, proc, startLivenessWindow); exited {
		_ = proc.Stop(DefaultStopGrace)
		return fail(fmt.Errorf("vlsup: %s exited immediately after start%s", ns, detailSuffix(waitErr)))
	}

	s.mu.Lock()
	inst.proc = proc
	inst.state = StateRunning
	inst.status.State = StateRunning
	inst.status.PID = proc.PID()
	inst.status.AssetTag = s.assetTag
	inst.status.AssetBuildID = s.assetBuild
	inst.status.AssetSHA256 = s.assetSHA
	inst.status.Port = cfg.Port
	inst.status.ListenAddr = ListenAddr(cfg.Port)
	inst.status.StorageDataPath = cfg.StorageDataPath
	inst.status.LastError = ""
	s.mu.Unlock()
	return nil
}

// startLivenessWindow 是启动后判定「进程是否立即退出」的观察窗口。
//
// 取值权衡：真实 VL 存活进程不会在数百毫秒内退出，而缺目录、端口被占、二进制不兼容等
// 立即可见的失败都会在该窗口内暴露；窗口过长会拖慢控制面的启动响应，故取 750ms。
const startLivenessWindow = 750 * time.Millisecond

// waitStartupLiveness 在 startLivenessWindow 内观察进程是否退出。
//
// 返回 (true, nil) 表示进程已退出（调用方须按启动失败处理）；返回 (false, nil) 表示
// 窗口内仍存活。ctx 取消时提前返回，避免阻塞控制面请求。
func waitStartupLiveness(ctx context.Context, proc Process, window time.Duration) (bool, error) {
	if proc == nil {
		return true, fmt.Errorf("no process handle")
	}
	timer := time.NewTimer(window)
	defer timer.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if proc.Exited() {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-timer.C:
			return proc.Exited(), nil
		case <-tick.C:
		}
	}
}

func detailSuffix(err error) string {
	if err == nil {
		return ""
	}
	return ": " + err.Error()
}

// Stop 停止指定 namespace 实例。
func (s *Supervisor) Stop(ctx context.Context, ns Namespace) error {
	_ = ctx
	s.mu.Lock()
	inst, ok := s.instances[ns]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("vlsup: unknown namespace %q", ns)
	}
	if inst.state != StateRunning && inst.proc == nil {
		inst.state = StateStopped
		inst.status.State = StateStopped
		inst.status.PID = 0
		inst.status.HealthOK = false
		inst.status.QueryReady = false
		s.mu.Unlock()
		return nil
	}
	inst.state = StateStopping
	inst.status.State = StateStopping
	proc := inst.proc
	s.mu.Unlock()

	var err error
	if proc != nil {
		err = proc.Stop(DefaultStopGrace)
	}

	s.mu.Lock()
	inst.proc = nil
	inst.state = StateStopped
	inst.status.State = StateStopped
	inst.status.PID = 0
	inst.status.HealthOK = false
	inst.status.QueryReady = false
	if err != nil {
		inst.state = StateFailed
		inst.status.State = StateFailed
		inst.status.LastError = err.Error()
	}
	s.mu.Unlock()
	if err != nil {
		wrapped := fmt.Errorf("vlsup: stop %s: %w", ns, err)
		if blocked := reportViaSink(s.sink, s.allowVLRec, string(ns), wrapped); blocked {
			s.mu.Lock()
			s.guardBlocks++
			s.mu.Unlock()
		}
		return wrapped
	}
	return nil
}

// StopAll 停止全部受管 namespace，供 Worker 退出时回收受管子进程。
//
// 必要性（真机事故）：Worker 退出若不停受管 VL，VL 会成为 PPID=1 的孤儿并继续占用
// hot/cold/rehydrate 端口；下次启动时新 VL 因端口被占而立即退出，采集面长期降级。
// 各 namespace 独立停止，单个失败不阻断其余回收。
func (s *Supervisor) StopAll(ctx context.Context) {
	for _, ns := range []Namespace{NamespaceHot, NamespaceCold, NamespaceRehydrate} {
		s.mu.Lock()
		inst, ok := s.instances[ns]
		running := ok && (inst.state == StateRunning || inst.proc != nil)
		s.mu.Unlock()
		if !running {
			continue
		}
		if err := s.Stop(ctx, ns); err != nil {
			// 单个 namespace 回收失败不应阻断其余，交由上层日志记录。
			continue
		}
	}
}

// ReplaceBinary switches managed namespaces to an already verified versioned
// executable. Failure to start the new asset restores the previous binary and
// attempts to restart every namespace that was running before the change.
func (s *Supervisor) ReplaceBinary(ctx context.Context, path, sha string) error {
	if err := VerifyAsset(path, sha); err != nil {
		return err
	}
	s.mu.Lock()
	oldPath, oldSHA := s.binPath, s.assetSHA
	if oldPath == path && oldSHA == sha {
		s.mu.Unlock()
		return nil
	}
	var running []Namespace
	for _, ns := range []Namespace{NamespaceHot, NamespaceCold, NamespaceRehydrate} {
		if s.instances[ns].state == StateRunning {
			running = append(running, ns)
		}
	}
	s.mu.Unlock()
	for _, ns := range running {
		if err := s.Stop(ctx, ns); err != nil {
			return fmt.Errorf("vlsup: stop before asset switch: %w", err)
		}
	}
	s.mu.Lock()
	s.binPath, s.assetSHA = path, sha
	for _, inst := range s.instances {
		inst.status.AssetSHA256 = sha
	}
	s.mu.Unlock()
	var started []Namespace
	for _, ns := range running {
		if err := s.Start(ctx, ns); err != nil {
			for _, active := range started {
				_ = s.Stop(ctx, active)
			}
			s.mu.Lock()
			s.binPath, s.assetSHA = oldPath, oldSHA
			for _, inst := range s.instances {
				inst.status.AssetSHA256 = oldSHA
			}
			s.mu.Unlock()
			for _, restore := range running {
				_ = s.Start(ctx, restore)
			}
			return fmt.Errorf("vlsup: new asset startup failed and previous asset was restored: %w", err)
		}
		started = append(started, ns)
	}
	return nil
}

// Health 探测实例 HTTP /health。
// 成功仅表示进程 HTTP 健康；分区恢复完成与查询可用不由此推断。
func (s *Supervisor) Health(ctx context.Context, ns Namespace) error {
	s.mu.Lock()
	inst, ok := s.instances[ns]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("vlsup: unknown namespace %q", ns)
	}
	cfg := inst.cfg
	running := inst.state == StateRunning
	s.mu.Unlock()

	if !running {
		return fmt.Errorf("vlsup: instance %s not running", ns)
	}
	base := "http://" + ListenAddr(cfg.Port)
	cli, err := s.newClient(base, cfg.AuthUsername, cfg.AuthPassword)
	if err != nil {
		return err
	}
	herr := cli.Health(ctx)

	s.mu.Lock()
	if herr == nil {
		inst.status.HealthOK = true
		inst.status.LastError = ""
	} else {
		inst.status.HealthOK = false
		inst.status.LastError = herr.Error()
	}
	s.mu.Unlock()
	if herr != nil {
		if blocked := reportViaSink(s.sink, s.allowVLRec, string(ns), herr); blocked {
			s.mu.Lock()
			s.guardBlocks++
			s.mu.Unlock()
		}
	}
	return herr
}

// WaitHealthy 轮询 /health 直到实例就绪或超时。
//
// 用途：受管 VL 启动是异步的——Start 返回时进程可能尚未监听端口。若此时接线 ingest/查询并立即向
// VL 写入，会因连接被拒失败，甚至导致采集运行时创建失败（FR-476 启动时序 / Runbook C）。
// 故在接线前等待就绪；超时返回最后一次错误，调用方据此保持降级而不静默假成功。
func (s *Supervisor) WaitHealthy(ctx context.Context, ns Namespace, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var last error
	for {
		if last = s.Health(ctx, ns); last == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("vlsup: %s not healthy within %s: %w", ns, timeout, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// ClientFor 返回该实例的 Basic-auth 客户端（base URL 可注入测试时经 HealthClient/NewClient）。
func (s *Supervisor) ClientFor(ns Namespace) (*Client, error) {
	s.mu.Lock()
	inst, ok := s.instances[ns]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("vlsup: unknown namespace %q", ns)
	}
	cfg := inst.cfg
	s.mu.Unlock()
	return s.newClient("http://"+ListenAddr(cfg.Port), cfg.AuthUsername, cfg.AuthPassword)
}

// Status 返回 namespace 状态快照。
func (s *Supervisor) Status(ns Namespace) (InstanceStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inst, ok := s.instances[ns]
	if !ok {
		return InstanceStatus{}, fmt.Errorf("vlsup: unknown namespace %q", ns)
	}
	s.reconcileExitedLocked(inst)
	return inst.status, nil
}

// reconcileExitedLocked 让账面状态跟随进程实况：进程已退出却仍记 RUNNING 时改为 FAILED。
//
// 必要性（真机实测）：启动后的存活观察窗口无法覆盖「先初始化数百毫秒、随后才退出」的失败
// （rehydrate 缺目录即如此——窗口内进程尚存，稍后才死）。只靠启动时的检查会让控制面长期
// 报 RUNNING 而实际端口无监听；故在任何状态读取点都以进程句柄为准校正，避免误导性成功上报。
// 调用方须持有 s.mu。
func (s *Supervisor) reconcileExitedLocked(inst *instance) {
	if inst == nil || inst.proc == nil || inst.state != StateRunning {
		return
	}
	if !inst.proc.Exited() {
		return
	}
	inst.state = StateFailed
	inst.status.State = StateFailed
	inst.status.HealthOK = false
	inst.status.QueryReady = false
	if inst.status.LastError == "" {
		inst.status.LastError = "managed VictoriaLogs process exited unexpectedly"
	}
}



// StatusAll 返回全部受管 namespace 的状态。
func (s *Supervisor) StatusAll() []InstanceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]InstanceStatus, 0, len(s.instances))
	for _, ns := range []Namespace{NamespaceHot, NamespaceCold, NamespaceRehydrate} {
		if inst, ok := s.instances[ns]; ok {
			// 与 Status 一致：以进程实况校正，避免已退出实例仍报 RUNNING。
			s.reconcileExitedLocked(inst)
			out = append(out, inst.status)
		}
	}
	return out
}

// EnforceCacheBudget 登记/校验进程级 cache 预算（占位，见 package budget 文档）。
func (s *Supervisor) EnforceCacheBudget(cacheBytes int64) error {
	if err := EnforceCacheBudget(cacheBytes); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.budget.HotCacheBytes = cacheBytes
	return nil
}

// Budget 返回当前预算登记值。
func (s *Supervisor) Budget() CacheBudget {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.budget
}

// SetSubBudget 登记一项 Worker 日志子预算（wal/staging/export/cold/rehydrate/projection/rss）。
// totalBytes > 0 时同时登记总预算，并在登记后执行契约 §6.6 的 25% 预留门禁。
func (s *Supervisor) SetSubBudget(name string, bytes int64) error {
	if bytes < 0 {
		return fmt.Errorf("vlsup: sub-budget %q must be >= 0, got %d", name, bytes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	budget := s.budget
	subs := make(map[string]int64, len(budget.SubBudgets)+1)
	for k, v := range budget.SubBudgets {
		subs[k] = v
	}
	subs[name] = bytes
	budget.SubBudgets = subs
	if err := EnforceReserveRatio(budget); err != nil {
		return err
	}
	s.budget = budget
	return nil
}

// SetTotalLogBudget 登记 Worker 日志总预算并立即执行 25% 预留门禁。
func (s *Supervisor) SetTotalLogBudget(totalBytes int64) error {
	if totalBytes < 0 {
		return fmt.Errorf("vlsup: total log budget must be >= 0, got %d", totalBytes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	budget := s.budget
	budget.TotalWorkerLogBudgetBytes = totalBytes
	if err := EnforceReserveRatio(budget); err != nil {
		return err
	}
	s.budget = budget
	return nil
}

// SampleBudget 采样受管 VL 进程 RSS 与数据盘使用率并评估降级状态（FR-476 / 契约 §6.6）。
//
// 仅纳入 RUNNING 实例；RSS 为各受管 VL 进程之和（Worker 日志数据面主要占用）。
// 单维度采样失败（进程已退出、磁盘不可读）不阻断：跳过该维度，返回仍可用的 verdict。
func (s *Supervisor) SampleBudget() BudgetVerdict {
	s.mu.Lock()
	budget := s.budget
	dataRoot := s.dataRoot
	pids := make([]int, 0, len(s.instances))
	for _, inst := range s.instances {
		if inst.state == StateRunning && inst.status.PID > 0 {
			pids = append(pids, inst.status.PID)
		}
	}
	s.mu.Unlock()

	var sample BudgetSample
	for _, pid := range pids {
		if rss, err := SampleProcessRSSBytes(pid); err == nil {
			sample.ProcessRSSBytes += rss
		}
	}
	if pct, err := SampleDiskUsagePercent(dataRoot); err == nil {
		sample.DiskUsagePercent = pct
	}
	return EvaluateBudget(budget, sample, DefaultBudgetThresholds())
}

// GuardBlocks 返回被守卫拒绝的递归 sink 尝试次数。
func (s *Supervisor) GuardBlocks() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.guardBlocks
}

// AllowVLRecursiveSink 返回守卫标志当前值（生产应为 false）。
func (s *Supervisor) AllowVLRecursiveSink() bool {
	return s.allowVLRec
}

// Ensure 健康客户端使用的绝对 URL（导出便于上层复用同一 base 规则）。
func InstanceBaseURL(ns Namespace, port int) string {
	if port <= 0 {
		port = DefaultPort(ns)
	}
	return "http://" + ListenAddr(port)
}

// QueryValues 辅助：构造 select 查询参数；localhost 策略由 Client 校验。
func QueryValues(kv map[string]string) url.Values {
	q := url.Values{}
	for k, v := range kv {
		q.Set(k, v)
	}
	return q
}
