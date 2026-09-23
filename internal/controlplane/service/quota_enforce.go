package service

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 运行期配额强制循环（FR-467 §2.3）。
//
// 结构参照 alert_evaluator.go 的 Start/Stop/evaluate 巡检形态：周期取样 → 连续 K 次超限
// 才处置（抗抖动）→ 按档位分发 → 回落正常连续 K 次解除。
//
// 「连续 K 次」而非「一次就处置」：内存/CPU 都有瞬时峰值（GC 前 RSS 尖峰、世界加载 CPU 尖峰），
// 一次判定会把正常工作负载误杀。K=3（默认，可配）在「反应够快」与「不误杀」之间取平衡。

const (
	// quotaDefaultInterval 默认巡检周期（spec §5）。
	//
	// M-2 修正：原 30s 与 K=3 组合只给出 90s 判定窗口，短于 MC 的正常长尾——
	// 世界加载/区块生成的 RSS 会在限额附近持续数分钟，GC 前的堆峰值同样如此。
	// 90s 窗口下「抗抖动」叙事并不成立，stop 档（不可逆的停机处置）误停风险偏高。
	// 改为 60s，与 K=5 组合给出约 5 分钟窗口。
	quotaDefaultInterval = 60 * time.Second
	// quotaDefaultStreak 默认连续超限次数（K=5）。
	//
	// 5 × 60s ≈ 5 分钟持续超限才处置：足以跨过加载/GC 长尾，又不会让真正失控的实例
	// 逍遥过久。注意 alert 档不受此窗口的破坏性影响（只告警），故该默认值主要保护
	// stop 档不误停——需要更快响应的部署可下调 streak（设置项即时生效）。
	quotaDefaultStreak = 5
	// quotaMemHeadroomRatio 内存阈值余量：限额 × 1.1 才判超限，给 GC 前峰值留空间（spec §5）。
	quotaMemHeadroomRatio = 1.1
	// quotaCPUHeadroomRatio CPU 阈值余量（同上）。
	quotaCPUHeadroomRatio = 1.1
	// quotaDiskHeadroomRatio 磁盘阈值余量（同口径）。
	quotaDiskHeadroomRatio = 1.0
)

// quotaKind 超限的指标维度。
type quotaKind string

const (
	quotaKindCPU  quotaKind = "cpu"
	quotaKindMem  quotaKind = "memory"
	quotaKindDisk quotaKind = "disk"
)

// quotaSample 一拍采样的实例用量。
type quotaSample struct {
	CPUPercent float64
	// RSSBytes 进程树 RSS（字节）。
	RSSBytes int64
	// DiskBytes 工作目录占用（字节）；0 = 未采集（不参与磁盘判定）。
	DiskBytes int64
}

// quotaMetricSource 提供实例运行期用量（*MetricService 实现）。
type quotaMetricSource interface {
	// LatestProcessSample 取实例最近一拍进程样本（心跳 TOPN，仅作 CPU 兜底）。
	LatestProcessSample(instanceUUID string, since time.Time) (*quotaSample, error)
	// InstanceWorkDirBytes 取实例工作目录占用（FR-467 新增采集）；0 表示不可用。
	//
	// ctx 用于向下传递取消/超时（m-1）；传 context.Background() 即等同改造前行为。
	InstanceWorkDirBytes(ctx context.Context, instanceID uint, instanceUUID string) (int64, error)
	// InstanceResourceUsage 取全进程树 RSS/CPU + 工作目录占用（M-2：不受 TOPN 截断的权威来源）。
	// 返回的 error 表示「采样失败」（DB/RPC 异常），须与 available=false 的「无可采数据」区分（R12）。
	InstanceResourceUsage(ctx context.Context, instanceID uint, instanceUUID string) (*quotaSample, bool, error)
}

// quotaDecision 单次巡检对某实例的判定与处置。
type quotaDecision struct {
	InstanceID uint
	Kind       quotaKind
	Usage      float64
	Limit      float64
	Mode       EnforceMode
	// Exceeded 本拍是否超限（用于连续计数）。
	Exceeded bool
	// Fired 本拍是否触发处置（连续 K 次达成后的那一刻为 true）。
	Fired bool
	// Recovered 本拍是否解除（从限流/超限状态回落到正常且连续 K 次）。
	Recovered bool
	// Detail 人可读说明（告警正文 / 审计详情）。
	Detail string
}

// quotaInstanceState 实例的连续计数状态（内存态，进程重启后重新累积）。
type quotaInstanceState struct {
	// Streak 各维度连续超限次数。
	Streak map[quotaKind]int
	// NormalStreak 各维度连续正常次数（用于解除）。
	NormalStreak map[quotaKind]int
	// Fired 各维度是否已触发处置（避免每拍重复触发）。
	Fired map[quotaKind]bool
}

// QuotaEnforcer 运行期配额强制巡检（FR-467 §2.3）。
type QuotaEnforcer struct {
	db      *gorm.DB
	quotas  *QuotaService
	metrics quotaMetricSource
	// instances 停止实例（stop 档处置）；nil 时 stop 档降级为告警。
	instances *InstanceService
	// dispatcher 告警分发（alert 档 + throttle 降级标注）。
	dispatcher *AlertDispatcher
	// audit 审计（instance.quota_* 动作）。
	audit *AuditService
	// settings 读 quota.enforce_interval / quota.enforce_streak。
	settings SettingsReader
	// notifier 站内信（stop 档）。
	notifier *NotificationService

	mu        sync.Mutex
	running   bool
	stopCh    chan struct{}
	states    map[uint]*quotaInstanceState
	lastRunAt time.Time
}

// NewQuotaEnforcer 创建配额强制巡检器。
func NewQuotaEnforcer(db *gorm.DB, quotas *QuotaService) *QuotaEnforcer {
	return &QuotaEnforcer{
		db:     db,
		quotas: quotas,
		stopCh: make(chan struct{}),
		states: make(map[uint]*quotaInstanceState),
	}
}

// SetMetrics 注入运行期用量来源（main 接线）。
func (e *QuotaEnforcer) SetMetrics(m quotaMetricSource) { e.metrics = m }

// SetInstanceService 注入实例服务（stop 档处置）。
func (e *QuotaEnforcer) SetInstanceService(s *InstanceService) { e.instances = s }

// SetDispatcher 注入告警分发器。
func (e *QuotaEnforcer) SetDispatcher(d *AlertDispatcher) { e.dispatcher = d }

// SetAudit 注入审计服务。
func (e *QuotaEnforcer) SetAudit(a *AuditService) { e.audit = a }

// SetSettingsReader 注入设置读取器（quota.enforce_interval / quota.enforce_streak）。
func (e *QuotaEnforcer) SetSettingsReader(r SettingsReader) { e.settings = r }

// SetNotificationService 注入站内信服务（stop 档通知）。
func (e *QuotaEnforcer) SetNotificationService(n *NotificationService) { e.notifier = n }

// interval 取巡检周期（平台设置 quota.enforce_interval，Go duration 文本）。
//
// 用 parseDurationDefault 而**不是** parseDurationOr：后者专为 MC 直探超时设计，末尾会
// `directprobe.NormalizeTimeout` 把值钳到 MaxTimeout(10s)——那是直探单轮预算的上界，
// 与巡检周期无关。先前误用导致任何 >10s 的周期被静默钳成 10s（默认 60s 也不例外，
// 启动日志因此恒打印 "interval":"10s"），使 spec §2.3「64 服应为 120s」根本无法配置、
// 且 K×interval 判定窗口被压缩。同 package 的 health_policy.go 用同一范式处理
// 巡检周期/熔断窗口，其注释已点明该区别（真机验收 2026-09-23 发现并修复）。
func (e *QuotaEnforcer) interval() time.Duration {
	if e.settings == nil {
		return quotaDefaultInterval
	}
	return parseDurationDefault(e.settings.EffectiveValue(SettingKeyQuotaEnforceInterval), quotaDefaultInterval)
}

// streakThreshold 取连续超限次数阈值（平台设置 quota.enforce_streak）。
func (e *QuotaEnforcer) streakThreshold() int {
	if e.settings == nil {
		return quotaDefaultStreak
	}
	n, err := strconv.Atoi(strings.TrimSpace(e.settings.EffectiveValue(SettingKeyQuotaEnforceStreak)))
	if err != nil || n < 1 {
		return quotaDefaultStreak
	}
	return n
}

// Start 启动巡检循环（幂等）。
//
// stopCh 每次 Start 重建（见 restartableStop）：Stop 会 close 该 channel，
// 复用同一个会让 Stop→Start panic 在 close of closed channel（R22/R30）。
func (e *QuotaEnforcer) Start() {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return
	}
	e.running = true
	stop := restartableStop(&e.stopCh)
	interval := e.interval()
	e.mu.Unlock()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		// 启动后先跑一轮（否则要等一个周期才开始强制）。
		e.evaluate()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				e.evaluate()
			}
		}
	}()
	slog.Info("运行期配额强制巡检已启动", "interval", interval.String(), "streak", e.streakThreshold())
}

// Stop 停止巡检循环。
func (e *QuotaEnforcer) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return
	}
	close(e.stopCh)
	e.running = false
	slog.Info("运行期配额强制巡检已停止")
}

// evaluate 单次巡检：取运行中实例 → 解析配额 → 连续 K 次判定 → 按档位处置。
//
// 采样并发化（s-5）：每实例一次按需 RPC（全树 RSS/CPU + 磁盘），原先串行执行在
// 64 服规模下会把一次巡检拖到数秒甚至超过巡检周期；此处用有界并发池，
// 上限 quotaSampleConcurrency，兼顾节点压力与巡检时延。
//
// R23：筛选阶段算出的 `*EffectiveQuota` 与目标实例平行保存并复用，避免同一实例在
// 一次 evaluate 内算两遍（每次 EffectiveQuota 是 3~4 次 DB 查询，64 服规模下每轮多出
// ~200 次查询）。
func (e *QuotaEnforcer) evaluate() {
	if e.metrics == nil {
		return
	}
	e.mu.Lock()
	e.lastRunAt = time.Now()
	e.mu.Unlock()

	var instances []model.Instance
	// 必须带上资源限额与待收紧限额列：throttleForQuota 要据此计算「收紧后仍不放宽配置」的目标值，
	// 漏选会让它以零值判断，从而错误地放宽或以过大的值覆盖运维配置。
	if err := e.db.Select("id", "uuid", "name", "node_id", "status", "process_type",
		"cpu_limit", "mem_limit_mb", "disk_limit_mb", "throttle_cpu_limit", "throttle_mem_limit_mb").
		Where("status IN ?", []model.InstanceStatus{model.InstanceStatusRunning, model.InstanceStatusStarting}).
		Find(&instances).Error; err != nil {
		slog.Error("配额巡检：查询运行中实例失败", "error", err)
		return
	}
	// 先筛掉无任何限额的实例，避免为它们白做采样（这是并发池前最划算的一次裁剪）。
	threshold := e.streakThreshold()
	targets := make([]model.Instance, 0, len(instances))
	quotas := make([]*EffectiveQuota, 0, len(instances))
	for i := range instances {
		quota, err := e.quotas.EffectiveQuota(instances[i].ID)
		if err != nil {
			continue
		}
		if quota.CPUCores <= 0 && quota.MemLimitMB <= 0 && quota.DiskLimitMB <= 0 {
			e.resetState(instances[i].ID)
			continue
		}
		targets = append(targets, instances[i])
		quotas = append(quotas, quota)
	}
	if len(targets) == 0 {
		return
	}

	// 有界并发采样：结果按下标回填，保证与 instance 一一对应且顺序无关。
	samples := make([]*quotaSample, len(targets))
	available := make([]bool, len(targets))
	errs := make([]error, len(targets))
	sem := make(chan struct{}, quotaSampleConcurrency)
	var wg sync.WaitGroup
	for i := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			inst := &targets[idx]
			sample, ok, err := e.sampleInstance(inst)
			samples[idx], available[idx], errs[idx] = sample, ok, err
		}(i)
	}
	wg.Wait()

	// R12：区分「不可采样」（预期内：未运行/节点离线）与「采样失败」（DB/RPC 异常）。
	// 后者必须可见——原先所有错误都被吞成 (nil,false,nil)，节点离线期间巡检静默漏检，
	// 运维无法回答「这台实例的配额到底有没有在管」。
	unavailable, failed := 0, 0
	for i := range targets {
		if errs[i] != nil {
			failed++
			slog.Warn("配额巡检：实例采样失败（本轮不参与连续计数）",
				"instanceId", targets[i].ID, "instanceUuid", targets[i].UUID, "error", errs[i])
			continue
		}
		if !available[i] || samples[i] == nil {
			unavailable++
		}
	}
	if unavailable > 0 || failed > 0 {
		slog.Warn("配额巡检：存在未能采样的实例（这些实例本轮不参与连续计数）",
			"unavailable", unavailable, "failed", failed, "total", len(targets))
	}

	for i := range targets {
		inst := &targets[i]
		if !available[i] || samples[i] == nil {
			continue
		}
		// R23：复用筛选阶段算出的配额，不再重复查一遍。
		for _, decision := range e.classify(inst, quotas[i], samples[i], threshold) {
			e.handle(inst, quotas[i], decision)
		}
	}
}

// quotaSampleConcurrency 同时进行的按需采样 RPC 上限（s-5）。
// 取 8：足以把 64 服的巡检时延压到原来的约 1/8，又不至于让单个 Worker 同时承受过多并发请求。
const quotaSampleConcurrency = 8

// sampleInstance 取单实例本轮用量（M-2：全树 RSS/CPU 优先，心跳 TOPN 仅兜底 CPU）。
//
// 优先级：按需全树快照（不受 10 进程截断影响）→ 心跳 TOPN 样本（节点离线时至少给 CPU 判据）。
// 两者都取不到才返回 false，让本轮不参与连续计数（不用「无数据」冒充「未超限」）。
//
// R12：返回值第三项区分「采样失败」（DB/RPC 异常，需留痕告警）与「不可用」
// （节点离线 / 实例未运行，属预期状态）。错误传到 evaluate 后统一计数 + Warn。
func (e *QuotaEnforcer) sampleInstance(inst *model.Instance) (*quotaSample, bool, error) {
	sample, ok, err := e.metrics.InstanceResourceUsage(context.Background(), inst.ID, inst.UUID)
	if err == nil && ok && sample != nil {
		// CPU 维度在按需快照不可用时用心跳样本兜底（磁盘维度只信按需采集）。
		if sample.CPUPercent <= 0 {
			if hb, herr := e.metrics.LatestProcessSample(inst.UUID, time.Now().Add(-3*e.interval())); herr == nil && hb != nil {
				sample.CPUPercent = hb.CPUPercent
			}
		}
		return sample, true, nil
	}
	if err != nil {
		// 采样失败：不再退化成「无数据」，把错误交给调用方计数告警。
		return nil, false, err
	}
	// 兜底：心跳 TOPN 样本（RSS 会被低估，但 CPU 与磁盘仍有参考值）。
	hbSample, herr := e.metrics.LatestProcessSample(inst.UUID, time.Now().Add(-3*e.interval()))
	if herr != nil {
		return nil, false, herr
	}
	if hbSample == nil {
		return nil, false, nil
	}
	if diskBytes, derr := e.metrics.InstanceWorkDirBytes(context.Background(), inst.ID, inst.UUID); derr == nil && diskBytes > 0 {
		hbSample.DiskBytes = diskBytes
	}
	return hbSample, hbSample.CPUPercent > 0 || hbSample.RSSBytes > 0 || hbSample.DiskBytes > 0, nil
}

// classify 对一拍采样做各维度的连续计数判定，返回需要处理的判定（含未达阈值时的状态更新）。
func (e *QuotaEnforcer) classify(inst *model.Instance, quota *EffectiveQuota, sample *quotaSample, threshold int) []quotaDecision {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.states[inst.ID]
	if st == nil {
		st = &quotaInstanceState{
			Streak:       map[quotaKind]int{},
			NormalStreak: map[quotaKind]int{},
			Fired:        map[quotaKind]bool{},
		}
		e.states[inst.ID] = st
	}

	checks := []struct {
		kind  quotaKind
		usage float64
		limit float64
		ratio float64
		unit  string
	}{
		{quotaKindMem, float64(sample.RSSBytes) / (1 << 20), float64(quota.MemLimitMB), quotaMemHeadroomRatio, "MiB"},
		{quotaKindCPU, sample.CPUPercent, quota.CPUCores * 100, quotaCPUHeadroomRatio, "%"},
		{quotaKindDisk, float64(sample.DiskBytes) / (1 << 20), float64(quota.DiskLimitMB), quotaDiskHeadroomRatio, "MiB"},
	}

	var out []quotaDecision
	for _, c := range checks {
		if c.limit <= 0 || c.usage <= 0 {
			// 该维度未设限 / 未采到用量：清零计数，避免「限额被删后仍带着旧计数触发」。
			st.Streak[c.kind] = 0
			st.NormalStreak[c.kind] = 0
			continue
		}
		exceeded := c.usage >= c.limit*c.ratio
		d := quotaDecision{
			InstanceID: inst.ID, Kind: c.kind, Usage: c.usage, Limit: c.limit, Mode: quota.EnforceMode,
			Exceeded: exceeded,
		}
		if exceeded {
			st.NormalStreak[c.kind] = 0
			st.Streak[c.kind]++
			d.Detail = fmt.Sprintf("%s 用量 %.1f%s / 限额 %.1f%s（连续 %d 拍）",
				quotaKindLabel(c.kind), c.usage, c.unit, c.limit, c.unit, st.Streak[c.kind])
			if st.Streak[c.kind] >= threshold && !st.Fired[c.kind] {
				st.Fired[c.kind] = true
				d.Fired = true
			}
		} else {
			st.Streak[c.kind] = 0
			if st.Fired[c.kind] {
				st.NormalStreak[c.kind]++
				if st.NormalStreak[c.kind] >= threshold {
					st.Fired[c.kind] = false
					st.NormalStreak[c.kind] = 0
					d.Recovered = true
					d.Detail = fmt.Sprintf("%s 用量回落至 %.1f%s / 限额 %.1f%s，解除强制",
						quotaKindLabel(c.kind), c.usage, c.unit, c.limit, c.unit)
				}
			}
			if !d.Recovered {
				continue
			}
		}
		out = append(out, d)
	}
	return out
}

// handle 按档位执行处置并写审计 / 告警。
func (e *QuotaEnforcer) handle(inst *model.Instance, quota *EffectiveQuota, d quotaDecision) {
	switch {
	case d.Recovered:
		// s-3：恢复必须**真正 Resolve** 活跃告警事件——Fire 时标了 Resolvable: true，
		// 若恢复只写审计不 Resolve，告警会永久停留在「活跃」，且解除后再超限
		// 也不会重新告警（活跃事件仍在，分发器认为已告警过）。
		e.resolveQuotaAlert(inst, d)
		// R7：解除必须**同时清零**该维度已登记的待收紧限额。旧实现只有收紧写入、
		// 没有清零路径，实例一旦超限过一次就被永久钉在 90% 硬顶（见 clearThrottleOnRecover）。
		if err := e.clearThrottleOnRecover(inst, d); err != nil {
			// 清零失败必须留痕：恢复本身成立，但「不再被限流」这半句没兑现，
			// 记 failed=true 以免运维据成功审计误判实例已回到配置限额。
			e.recordAudit(0, "instance.quota_recovered", inst, d, false, "清零待收紧限额失败: "+err.Error())
			return
		}
		e.recordAudit(0, "instance.quota_recovered", inst, d, true, "")
		return
	case !d.Fired:
		return
	}

	switch d.Mode {
	case EnforceModeStop:
		e.stopForQuota(inst, d)
	case EnforceModeThrottle:
		e.throttleForQuota(inst, quota, d)
	default:
		e.alertForQuota(inst, d)
	}
}

// clearThrottleOnRecover 把该维度已登记的「待收紧限额」清零（R7）。
//
// 缺陷现场：throttleForQuota 是 ThrottleCPULimit / ThrottleMemLimitMB 的**唯一**写入点，
// 且只写收紧值——全仓没有清零路径。于是实例被限流一次后，`effectiveCPULimit` /
// `effectiveMemLimitMB`（instance.go:1679/1684 取 minPositive*）在**每次启动**都取较小值，
// 实例永久停在 90% 硬顶：运维扩容、清理工作目录、迁组等任何使超限解除的动作都不再有用。
// 配额视图还会继续回显这个旧值（见 Status），运维看到「无超限」+「有限额」自相矛盾。
//
// 语义决策：
//   - **按维度独立清零**：只清 d.Kind 对应的那一维。CPU 已恢复但内存仍超限时必须保留内存
//     的待收紧值，否则「修复」会把仍在生效的另一维防护一起撤掉。
//   - **清零即写库**：只改内存态等于下次启动又读到旧值（与 M-1「只写审计不落库」同类失真）。
//   - 清零不触碰 CPULimit / MemLimitMB（运维显式配置）：生效限额自然回落到配置值，
//     无需改动 effective* 逻辑。
//   - 磁盘维度无 cgroup 收紧手段（throttleForQuota 从不写它），故无对应列可清。
func (e *QuotaEnforcer) clearThrottleOnRecover(inst *model.Instance, d quotaDecision) error {
	var updates map[string]any
	var parts []string
	switch d.Kind {
	case quotaKindCPU:
		if inst.ThrottleCPULimit > 0 {
			updates = map[string]any{"throttle_cpu_limit": 0}
			parts = append(parts, fmt.Sprintf("CPU 待收紧限额 %.2f 核已清零", inst.ThrottleCPULimit))
		}
	case quotaKindMem:
		if inst.ThrottleMemLimitMB > 0 {
			updates = map[string]any{"throttle_mem_limit_mb": 0}
			parts = append(parts, fmt.Sprintf("内存待收紧限额 %d MiB 已清零", inst.ThrottleMemLimitMB))
		}
	}
	if len(updates) == 0 {
		// 该维度本就无待收紧项（alert 档恢复、或从未触发限流）：无需写库。
		return nil
	}
	if err := e.db.Model(&model.Instance{}).Where("id = ?", inst.ID).Updates(updates).Error; err != nil {
		return err
	}
	// 同步内存副本，供同一轮内后续读取（如配额视图）看到清零结果。
	if d.Kind == quotaKindCPU {
		inst.ThrottleCPULimit = 0
	} else {
		inst.ThrottleMemLimitMB = 0
	}
	slog.Info("配额恢复：已清零待收紧限额", "instanceId", inst.ID, "kind", d.Kind,
		"detail", strings.Join(parts, "、"))
	return nil
}

// resolveQuotaAlert 解除该实例该维度的活跃配额告警（dedup key 与 fireQuotaAlert 一致）。
//
// key 复用 `quota:<instanceId>:<kind>`：与 Fire 完全同键才能解到同一条活跃事件。
// 维度必须参与键——内存恢复不应把 CPU 的活跃告警一起解掉。
func (e *QuotaEnforcer) resolveQuotaAlert(inst *model.Instance, d quotaDecision) {
	if e.dispatcher == nil {
		return
	}
	var rules []model.AlertRule
	if err := e.db.Where("enabled = ? AND trigger_type = ?", true, model.AlertTriggerQuotaExceeded).
		Find(&rules).Error; err != nil {
		slog.Warn("配额巡检：查询告警规则失败（无法解除告警）", "error", err)
		return
	}
	msg := fmt.Sprintf("实例 %s 配额已回落：%s", inst.Name, d.Detail)
	for i := range rules {
		rule := &rules[i]
		if rule.TargetID != nil && *rule.TargetID != inst.ID {
			continue
		}
		e.dispatcher.Resolve(rule, fmt.Sprintf("quota:%d:%s", inst.ID, d.Kind), msg)
	}
}

// alertForQuota alert 档：触发告警，不干预进程。
func (e *QuotaEnforcer) alertForQuota(inst *model.Instance, d quotaDecision) {
	msg := fmt.Sprintf("实例 %s 配额超限：%s", inst.Name, d.Detail)
	e.fireQuotaAlert(inst, d, msg)
	e.recordAudit(0, "instance.quota_exceeded", inst, d, true, "")
}

// throttleForQuota throttle 档：docker 落「待收紧限额」并告警；非 docker **诚实降级**为告警。
//
// 明确决策（spec §2.4 / M-1）：非 docker 模式**不做内核级 CPU 节流**——本版不引入 nice/cpulimit
// 注入（依赖宿主工具，且行为不可预期）。因此这里只告警并在文案里说明「无法限流」，
// 而不是假装做了限制。
//
// docker 分支（M-1）：cgroup 限额只能在创建容器时注入，运行期无法收紧，故**必须把
// 「待收紧限额」持久化到实例行**（throttle_cpu_limit / throttle_mem_limit_mb），
// 由启动/重建容器时的规格翻译消费（见 effectiveCPULimit / effectiveMemLimitMB）。
// 只写审计不落库就等于限流永不发生——审计写成功而实际什么都没做，是最坏的一种失真。
func (e *QuotaEnforcer) throttleForQuota(inst *model.Instance, quota *EffectiveQuota, d quotaDecision) {
	if inst.ProcessType != model.ProcessTypeDocker {
		msg := fmt.Sprintf("实例 %s 配额超限（%s）：非 docker 模式无法内核级限流，已降级为告警；"+
			"如需硬限请改用 docker 模式", inst.Name, d.Detail)
		e.fireQuotaAlert(inst, d, msg)
		e.recordAudit(0, "instance.quota_throttle_unsupported", inst, d, false, "非 docker 模式不支持内核级限流，已降级为告警")
		return
	}
	// 收紧目标：把硬上限压到**生效限额之下**留一档余量。
	//
	// 为什么不是「按当前用量留余量」：用量必然 ≥ 限额（否则不会走到这里），
	// 以用量为基准算出的目标恒大于配额限额，于是永远没有可采纳的收紧值——
	// 那等于换了个写法继续「限流永不发生」。真正能收敛的做法是把 cgroup 硬上限
	// 压到限额的 90%：重启后容器在此硬顶下运行，实例无法再贴/破配额线。
	updates := map[string]any{}
	var parts []string
	if d.Kind == quotaKindCPU {
		// N-2 修正：原先用 roundUpToHalfCore（**向上**取半核整倍数），在 0.5 核整数倍的
		// 常用限额下恒等于 configured（0.50×0.9=0.45 → 向上取 0.50），而 tightenLimit 要求
		// candidate < configured 才采纳 → 0.5/1.0/1.5/…/4.5 全部落到「无可进一步收紧」分支，
		// 即**运维最常用的限额值下 CPU 限流永不发生**。改为直接用 90% 原值（Worker 侧
		// NanoCPUs = cpuLimit×1e9 支持任意小数，Docker 只要求 >0 且 ≤ 宿主核数，见
		// worker/process/docker.go applyResourceLimits 与 daemon_unix.go 的范围校验），
		// 不再做整倍数取整——「向下取整」同样会退化（0.50×0.9=0.45 向下取 0.0 会产出非法零值）。
		target := quota.CPUCores * quotaThrottleTightenRatio
		if tightened := tightenLimit(inst.ThrottleCPULimit, inst.CPULimit, target); tightened != inst.ThrottleCPULimit {
			updates["throttle_cpu_limit"] = tightened
			parts = append(parts, fmt.Sprintf("CPU 限额 → %.2f 核", tightened))
		}
	}
	if d.Kind == quotaKindMem {
		target := int64(float64(quota.MemLimitMB) * quotaThrottleTightenRatio)
		if tightened := tightenLimitInt64(inst.ThrottleMemLimitMB, inst.MemLimitMB, target); tightened != inst.ThrottleMemLimitMB {
			updates["throttle_mem_limit_mb"] = tightened
			parts = append(parts, fmt.Sprintf("内存限额 → %d MiB", tightened))
		}
	}
	if len(updates) == 0 {
		// 该维度已有更严的待收紧值（或磁盘维度无 cgroup 收紧手段）：只告警，不谎称已收紧。
		msg := fmt.Sprintf("实例 %s 配额超限（%s）：该维度无可进一步收紧的 cgroup 限额，已告警", inst.Name, d.Detail)
		e.fireQuotaAlert(inst, d, msg)
		e.recordAudit(0, "instance.quota_throttled", inst, d, true, "该维度限额已是最严，无变更")
		return
	}
	if err := e.db.Model(&model.Instance{}).Where("id = ?", inst.ID).Updates(updates).Error; err != nil {
		msg := fmt.Sprintf("实例 %s 配额超限（%s）：登记待收紧限额失败（%v），已降级为告警", inst.Name, d.Detail, err)
		e.fireQuotaAlert(inst, d, msg)
		e.recordAudit(0, "instance.quota_throttled", inst, d, false, "登记待收紧限额失败: "+err.Error())
		return
	}
	detail := fmt.Sprintf("已登记待收紧限额（%s），下次启动容器时生效", strings.Join(parts, "、"))
	msg := fmt.Sprintf("实例 %s 配额超限（%s）：%s（运行期无法收紧 cgroup，需重启容器生效）", inst.Name, d.Detail, detail)
	e.fireQuotaAlert(inst, d, msg)
	e.recordAudit(0, "instance.quota_throttled", inst, d, true, detail)
}

// quotaThrottleTightenRatio 收紧目标相对**生效限额**的比例（0.9 = 压到限额的 90%）。
//
// 压到限额之下才叫「收紧」：重启后容器在更低一档的 cgroup 硬顶下运行，
// 从而不可能再贴/破配额线。留 10% 余量是为了不把限额压得比实例的稳态需求还低
// （那会让容器反复 OOM/被限流，从「超限告警」变成「必然不可用」）。
const quotaThrottleTightenRatio = 0.9

// tightenLimit 计算新的待收紧 CPU 限额：取「不大于目标值」且「比现值更严」的正数。
// 现值 <=0（尚未登记）时直接取目标值；目标不小于现值（无法再收紧或现值已更严）时返回原值。
func tightenLimit(current, configured, target float64) float64 {
	if target <= 0 {
		return current
	}
	// 不得放宽运维显式配置的限额：收紧结果严格小于 configured（若 configured>0）。
	candidate := target
	if configured > 0 && candidate >= configured {
		return current
	}
	if current > 0 && candidate >= current {
		return current
	}
	return candidate
}

// tightenLimitInt64 同 tightenLimit 的整数版本（内存 MiB）。
func tightenLimitInt64(current, configured, target int64) int64 {
	if target <= 0 {
		return current
	}
	candidate := target
	if configured > 0 && candidate >= configured {
		return current
	}
	if current > 0 && candidate >= current {
		return current
	}
	return candidate
}

// stopForQuota stop 档：优雅停止实例、置 statusReason、站内信、审计。
func (e *QuotaEnforcer) stopForQuota(inst *model.Instance, d quotaDecision) {
	reason := fmt.Sprintf("配额超限已停止（%s）", quotaKindLabel(d.Kind))
	if e.instances == nil {
		// 未装配实例服务：不能停服，降级为告警（诚实降级，不假装已停）。
		e.fireQuotaAlert(inst, d, fmt.Sprintf("实例 %s 配额超限（%s）：停止能力未装配，已降级为告警", inst.Name, d.Detail))
		e.recordAudit(0, "instance.quota_stop_unsupported", inst, d, false, "实例服务未装配，无法停止")
		return
	}
	stopErr := e.instances.Stop(inst.ID)
	if stopErr != nil {
		slog.Warn("配额超限停止实例失败", "instanceId", inst.ID, "error", stopErr)
		e.fireQuotaAlert(inst, d, fmt.Sprintf("实例 %s 配额超限（%s），但停止失败：%v", inst.Name, d.Detail, stopErr))
		e.recordAudit(0, "instance.quota_stopped", inst, d, false, stopErr.Error())
		return
	}
	_ = e.db.Model(&model.Instance{}).Where("id = ?", inst.ID).
		Update("status_reason", reason).Error
	if e.notifier != nil {
		_ = e.notifier.Create(0, model.NotificationLevelWarning,
			"实例因配额超限被停止", fmt.Sprintf("%s：%s", inst.Name, d.Detail), "")
	}
	e.fireQuotaAlert(inst, d, fmt.Sprintf("实例 %s 已因配额超限停止：%s", inst.Name, d.Detail))
	e.recordAudit(0, "instance.quota_stopped", inst, d, true, "")
}

// fireQuotaAlert 触发 quota_exceeded 告警（按已配置的规则路由到通知渠道）。
func (e *QuotaEnforcer) fireQuotaAlert(inst *model.Instance, d quotaDecision, msg string) {
	if e.dispatcher == nil {
		return
	}
	var rules []model.AlertRule
	if err := e.db.Where("enabled = ? AND trigger_type = ?", true, model.AlertTriggerQuotaExceeded).
		Find(&rules).Error; err != nil {
		slog.Warn("配额巡检：查询告警规则失败", "error", err)
		return
	}
	for i := range rules {
		rule := &rules[i]
		if rule.TargetID != nil && *rule.TargetID != inst.ID {
			continue
		}
		e.dispatcher.Fire(AlertTrigger{
			Rule:       rule,
			TargetID:   inst.ID,
			DedupKey:   fmt.Sprintf("quota:%d:%s", inst.ID, d.Kind),
			Value:      d.Usage,
			Message:    msg,
			Resolvable: true,
		})
	}
}

// recordAudit 写配额审计（动作命名域.动作：instance.quota_*）。
func (e *QuotaEnforcer) recordAudit(userID uint, action string, inst *model.Instance, d quotaDecision, success bool, errMsg string) {
	if e.audit == nil {
		return
	}
	detail := fmt.Sprintf("%s usage=%.1f limit=%.1f mode=%s", d.Kind, d.Usage, d.Limit, d.Mode)
	e.audit.RecordResultSafe(userID, action, "instance", fmt.Sprintf("%d", inst.ID), detail, "", success, errMsg)
}

// resetState 清空某实例的计数状态（无限额 / 已停机时调用，避免状态长期滞留）。
func (e *QuotaEnforcer) resetState(instanceID uint) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.states, instanceID)
}

// quotaKindLabel 维度的中文标签（告警/审计文案）。
func quotaKindLabel(k quotaKind) string {
	switch k {
	case quotaKindCPU:
		return "CPU"
	case quotaKindMem:
		return "内存"
	case quotaKindDisk:
		return "磁盘"
	}
	return string(k)
}

// EvaluateOnce 供运维手动触发一次巡检（导出供测试与未来的「立即巡检」入口使用）。
func (e *QuotaEnforcer) EvaluateOnce() {
	e.evaluate()
}

// QuotaStatus 实例配额的实时视图（GET /instances/:id/quota）。
type QuotaStatus struct {
	InstanceID  uint    `json:"instanceId"`
	CPUCores    float64 `json:"cpuCores"`
	MemLimitMB  int64   `json:"memLimitMb"`
	DiskLimitMB int64   `json:"diskLimitMb"`
	EnforceMode string  `json:"enforceMode"`
	MemSource   string  `json:"memSource"`
	DiskSource  string  `json:"diskSource"`
	CPUSource   string  `json:"cpuSource"`
	GroupID     uint    `json:"groupId"`
	// 实时用量（采样失败时为 0 + SampleNote）。
	CPUPercent float64 `json:"cpuPercent"`
	RSSBytes   int64   `json:"rssBytes"`
	DiskBytes  int64   `json:"diskBytes"`
	SampleNote string  `json:"sampleNote,omitempty"`
	// Enforced 当前是否处于已触发的强制状态（按维度）。
	//
	// **本次进程内**语义（s-2）：连续计数是 CP 内存态，进程重启后重新累积，
	// 因此这些标志只反映「本进程观察到并已触发」的状态；已达阈值但重启过的实例会显示 false
	// 直到重新累积到阈值。历史事实请查审计 instance.quota_* 记录。
	EnforcedCPU  bool `json:"enforcedCpu"`
	EnforcedMem  bool `json:"enforcedMem"`
	EnforcedDisk bool `json:"enforcedDisk"`
	// EnforceStateScope 强制状态的作用域说明（固定为 in_process），使前端/接口调用方
	// 不必猜测该字段是否持久。
	EnforceStateScope string `json:"enforceStateScope"`
	// SupportedThrottle 该实例是否支持内核级限流（仅 docker）。
	SupportedThrottle bool `json:"supportedThrottle"`
	// ThrottleCPULimit / ThrottleMemLimitMB 已登记待收紧的限额（M-1）：
	// throttle 档触发时落库，下次启动/重建容器时在此生效。0=无待收紧项。
	ThrottleCPULimit   float64 `json:"throttleCpuLimit"`
	ThrottleMemLimitMB int64   `json:"throttleMemLimitMb"`
}

// Status 汇总实例的配额与实时用量（读侧）。
func (e *QuotaEnforcer) Status(instanceID uint) (*QuotaStatus, error) {
	var inst model.Instance
	if err := e.db.Select("id", "uuid", "name", "process_type", "status",
		"throttle_cpu_limit", "throttle_mem_limit_mb").First(&inst, instanceID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrQuotaInstanceNotFound
		}
		return nil, err
	}
	quota, err := e.quotas.EffectiveQuota(instanceID)
	if err != nil {
		return nil, err
	}
	out := &QuotaStatus{
		InstanceID: instanceID,
		CPUCores:   quota.CPUCores, MemLimitMB: quota.MemLimitMB, DiskLimitMB: quota.DiskLimitMB,
		EnforceMode: string(quota.EnforceMode),
		MemSource:   quota.MemSource, DiskSource: quota.DiskSource, CPUSource: quota.CPUSource,
		GroupID:           quota.GroupID,
		SupportedThrottle: inst.ProcessType == model.ProcessTypeDocker,
		// s-2：连续计数是内存态，显式标注作用域，避免被误读为持久状态。
		EnforceStateScope:  "in_process",
		ThrottleCPULimit:   inst.ThrottleCPULimit,
		ThrottleMemLimitMB: inst.ThrottleMemLimitMB,
	}
	if e.metrics != nil && inst.Status == model.InstanceStatusRunning {
		if sample, serr := e.metrics.LatestProcessSample(inst.UUID, time.Now().Add(-3*e.interval())); serr == nil {
			out.CPUPercent, out.RSSBytes = sample.CPUPercent, sample.RSSBytes
		} else {
			out.SampleNote = "暂无运行期进程样本"
		}
		if disk, derr := e.metrics.InstanceWorkDirBytes(context.Background(), inst.ID, inst.UUID); derr == nil {
			out.DiskBytes = disk
		} else {
			// R12：磁盘读取失败必须让读侧看见，不能与「占用为 0」混同。
			out.SampleNote = "磁盘占用读取失败，本次未取到实时值"
		}
	} else if inst.Status != model.InstanceStatusRunning {
		out.SampleNote = "实例未运行，无实时用量"
	}
	e.mu.Lock()
	if st := e.states[instanceID]; st != nil {
		out.EnforcedCPU = st.Fired[quotaKindCPU]
		out.EnforcedMem = st.Fired[quotaKindMem]
		out.EnforcedDisk = st.Fired[quotaKindDisk]
	}
	e.mu.Unlock()
	return out, nil
}
