package retention

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// VLDeleter 是保留执行器需要的最小 VL 表面。
//
// 刻意只留一个方法：执行器不该知道删除是怎么实现的（HTTP 形状、鉴权、批处理），
// 那样它才能用假实现做出「删了什么、没删什么、失败怎么处理」的可转红回归。
type VLDeleter interface {
	// RunDeleteTask 提交一次按过滤器 + 截止时间的删除，返回任务 ID。
	RunDeleteTask(ctx context.Context, filter string, before time.Time) (string, error)
}

// ClientDeleter 把 vlsup.Client 适配为 VLDeleter。
//
// 批准版v1.52.0仅消费filter，不消费独立start/end；这里主动使用POST，
// 不能声称该版服务端只接受POST。截止时间必须进入白名单生成的LogsQL过滤器。
// 意图闸、执行闸和受管VL的delete.enable三闸继续默认关闭，生产Sweeper未接线。
type ClientDeleter struct {
	Client *vlsup.Client
}

// RunDeleteTask 实现 VLDeleter。
func (d ClientDeleter) RunDeleteTask(ctx context.Context, filter string, before time.Time) (string, error) {
	if d.Client == nil {
		return "", fmt.Errorf("retention: VictoriaLogs 客户端未就绪")
	}
	safeFilter, err := boundedDeleteFilter(filter, before)
	if err != nil {
		return "", err
	}
	q := url.Values{}
	q.Set("filter", safeFilter)
	body, err := d.Client.Post(ctx, "/delete/run_task", q)
	if err != nil {
		return "", err
	}
	var resp struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("retention: 解析删除任务响应失败：%w（响应 %q）", err, truncateForLog(body, 200))
	}
	if resp.TaskID == "" {
		return "", fmt.Errorf("retention: 删除任务响应缺少 task_id（响应 %q）", truncateForLog(body, 200))
	}
	return resp.TaskID, nil
}

// boundedDeleteFilter 只接收buildFilter能生成的规范字符串，拒绝任何自由LogsQL。
// 批准引擎时间区间闭合，用cutoff-1ns实现严格_time<cutoff的半开语义。
func boundedDeleteFilter(filter string, before time.Time) (string, error) {
	levelLiteral, source, found := strings.Cut(strings.TrimPrefix(filter, "level:="), " AND log_source_id:=")
	if !strings.HasPrefix(filter, "level:=") {
		return "", fmt.Errorf("retention: 删除filter不在精确值白名单内")
	}
	level, err := strconv.Unquote(levelLiteral)
	if err != nil {
		return "", fmt.Errorf("retention: 删除级别必须为精确值")
	}
	if found {
		value, err := strconv.Unquote(source)
		if err != nil {
			return "", fmt.Errorf("retention: 非法删除来源")
		}
		source = value
	}
	canonical, err := buildFilter(source, level)
	if err != nil || canonical != filter {
		return "", fmt.Errorf("retention: 删除filter不是规范白名单表达式")
	}
	ns := before.UnixNano()
	if !time.Unix(0, ns).Equal(before) || ns == math.MinInt64 {
		return "", fmt.Errorf("retention: 截止时间超出纳秒范围或cutoff-1ns下溢")
	}
	lower := time.Unix(0, math.MinInt64).UTC().Format(time.RFC3339Nano)
	upper := time.Unix(0, ns-1).UTC().Format(time.RFC3339Nano)
	return canonical + " AND _time:[" + lower + ", " + upper + "]", nil
}

func truncateForLog(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

// TargetOutcome 是单个目标的执行结果。
type TargetOutcome struct {
	Target SweepTarget
	// TaskID 删除任务 ID（未执行时为空）。
	TaskID string
	// Skipped 为真表示本次未下发（降级/节流/未启用执行器）。
	Skipped bool
	// SkipReason 说明为什么跳过。
	SkipReason string
	// Err 非空表示下发失败。
	Err error
}

// SweepResult 是一次扫描的结果。
type SweepResult struct {
	// Planned 本次计算出的目标数。
	Planned int
	// Submitted 实际下发的删除任务数。
	Submitted int
	// Skipped 因节流或未启用执行器而未下发的目标数。
	Skipped int
	// Failed 下发失败的目标数。
	Failed int
	// Outcomes 逐目标结果（观测与回归用）。
	Outcomes []TargetOutcome
	// DryRun 报告本次是否只计算未执行。
	DryRun bool
}

// Sweeper 按策略周期性地把保留期翻译成 VL 删除任务。
//
// 并发口径：由单个后台循环调用（与采集轮无关），内部不加锁；节流表只在本实例内生效，
// 进程重启后立刻补扫一次——删除是幂等的，多扫一次只是多一次索引遍历，不会删错。
//
// **生产接线状态（RET-2）**：本版本没有NewSweeper的生产调用点；
// 即使存在受管VL的delete.enable配置位，也不能把打开开关描述成执行器已接线。
// 这是 fail-closed 方向，但启动日志不得声称删除已生效（措辞见 Policy.Describe 与
// DescribeDeleteExecutor）。补接线时必须一并补上 `-delete.enable` 的显式配置位
// （默认关、开启需二次确认）与 SweepStats 的消费点。
type Sweeper struct {
	policy  Policy
	deleter VLDeleter
	// lastRun 按过滤器节流：同一个过滤器在 Interval 内只下发一次。
	lastRun map[string]time.Time
	// stats 是累计观测读数。
	stats SweepStats
}

// dryRunReason 说明为什么这一轮没有下发（观测面据此区分「没开」与「没到点」）。
//
// **不是** `Sweeper` 能力的分支表：调用方 `Sweep` 只在 `res.DryRun==true` 时调它，
// 而该条件已排除「VLSweep 关」「Discard 关」「deleter 为 nil」三种 ⇒ 正常情况下
// **一律落到 `default`**（三闸全开且客户端就绪，却仍被标 DryRun 只可能是判据分叉）。
//
// 前三个 `case` 因此是**防御性**的（判据若被改动，文案仍能与原因对上），
// 而不是「当前会走到」的路径——写清楚这一点，避免读者把它读成三分支的真实分派。
func dryRunReason(p Policy) string {
	n := p.Normalize()
	switch {
	case !n.Sweep.VLSweep:
		return "执行闸未打开（sweep.vl_sweep=false）：仅计算，不删除"
	case !p.Discard:
		return "意图闸未打开（discard=false）：按硬规则「删前归档」，到期数据应走搬运而非删除"
	case !DeleteExecutorWired:
		// 防御分支（当前不可达，如实标注）：`DeleteExecutorWired` 表达的是「**生产装配处**
		// 是否构造了本 Sweeper」，而本类型在测试中是可构造且可用的 ⇒ 它**不参与**
		// `Sweep` 的 DryRun 判据（见该处注释）。生产不可达由启动日志的
		// DescribeDeleteExecutor 单独声明，两者职责不同。
		// 真出现本分支说明有人把该常量塞进了判据——那会让类型失去能力，须回退。
		return "删除执行器未接线（本版本无生产构造点）：只算计划，不删除"
	default:
		return "删除客户端未就绪"
	}
}

// SweepStats 是执行器的累计读数（观测面）。
//
// 接线状态（OBS-1）：本版本的 `Sweeper` 没有生产构造点，故这些读数在生产**没有消费点**——
// 它们当前只服务回归与将来的诊断面（`PlanOnly` 的干跑规模就是靠 SkippedDisabled 读出来的）。
// 接上执行器时必须同时把它们接到既有的分钟打点，否则「省了多少 / 删了什么」在现场不可读。
type SweepStats struct {
	// Sweeps 扫描轮数。
	Sweeps int64
	// Planned 累计目标数。
	Planned int64
	// Submitted 累计下发数。
	Submitted int64
	// Failed 累计失败数。
	Failed int64
	// SkippedDisabled 因执行器未启用而跳过的目标数（= 干跑规模，运维据此决定是否开启）。
	SkippedDisabled int64
	// LastSweepAt 最近一次扫描时刻。
	LastSweepAt time.Time
	// LastError 最近一次错误文本。
	LastError string
}

// NewSweeper 创建执行器。deleter 为 nil 时只做干跑（与 Sweep.VLSweep=false 同效）。
func NewSweeper(policy Policy, deleter VLDeleter) *Sweeper {
	return &Sweeper{policy: policy, deleter: deleter, lastRun: map[string]time.Time{}}
}

// SetPolicy 替换策略（保留节流表：策略微调不该让已下发的删除重来一遍）。
func (s *Sweeper) SetPolicy(policy Policy) {
	if s == nil {
		return
	}
	s.policy = policy
}

// Stats 返回累计读数副本。
func (s *Sweeper) Stats() SweepStats {
	if s == nil {
		return SweepStats{}
	}
	return s.stats
}

// PlanOnly 在不执行的前提下算出本次计划（观测面：先看得见，再决定动不动手）。
func (s *Sweeper) PlanOnly(now time.Time, sourceIDs, levels []string) ([]SweepTarget, error) {
	if s == nil {
		return nil, nil
	}
	return s.policy.Plan(now, sourceIDs, levels)
}

// Sweep 执行一轮：算计划 → 逐个下发（带节流与错误隔离）。
//
// # 与硬规则「删前归档」的关系（两道独立的闸）
//
// 本方法直接调用**删除**接口，因此它在硬规则下只允许服务于「显式知情放弃」这一种情形。
// 判定写成两道**相互独立**的闸，两层都满足才会真的删：
//   - Sweep.VLSweep：执行闸（默认关，需运维显式打开，且受管 VL 需带 `-delete.enable`）；
//   - Policy.Discard：意图闸（默认关，需运维显式声明「我知道这会永久删掉日志」）。
//
// 为什么不合并成一个开关：一个开关被误开只有一种防护，两个独立开关被同时误开的
// 概率低得多，而这条路径的后果是**不可逆**的。同理，默认路径永远是 PlanArchive 的
// 搬运（数据仍在、仍可查），本方法在默认配置下不产生任何调用。
//
// 错误隔离是本方法的核心性质：**单个目标下发失败绝不影响其余目标**。
// 保留清理是后台的省空间动作，一次 VL 抖动不该让整轮作废（那会让过期数据一直留着）。
// 因此失败只记录并按目标返回，最终错误只在「计划本身算不出来」时返回。
func (s *Sweeper) Sweep(ctx context.Context, now time.Time, sourceIDs, levels []string) (SweepResult, error) {
	var res SweepResult
	if s == nil || !s.policy.Enabled {
		return res, nil
	}
	targets, err := s.policy.Plan(now, sourceIDs, levels)
	if err != nil {
		// 计划算不出来（例如源标识非法）仍然返回已算出的部分，让调用方决定怎么处置：
		// 把已经能确定的过期档一起丢掉，只会让过期数据更久地占着空间。
		s.stats.LastError = err.Error()
		if len(targets) == 0 {
			return res, err
		}
	}
	res.Planned = len(targets)
	// 只有两闸都开、且客户端就绪，才进入下发分支；否则一律落到「仅计算」。
	//
	// **刻意不纳入 `DeleteExecutorWired`**（N-5 复核，2026-10-12）：该常量的语义是
	// 「**生产装配处**是否构造了 Sweeper」，而 `Sweeper` 类型本身是可用的（测试即构造它）。
	// 把常量塞进本判据会让类型自身失去能力、并让端到端用例（策略→计划→真实 HTTP 请求）
	// 失去意义——那是把「接线状态」与「类型能力」两件事混为一谈。
	// 「生产不可达」由 DescribeDeleteExecutor 在启动日志显式声明，正是为此设的观测面。
	res.DryRun = !s.policy.Normalize().Sweep.VLSweep || s.deleter == nil || !s.policy.Discard
	s.stats.Sweeps++
	s.stats.Planned += int64(len(targets))
	s.stats.LastSweepAt = now

	interval := s.policy.Normalize().Sweep.Interval
	timeout := s.policy.Normalize().Sweep.Timeout

	for _, t := range targets {
		outcome := TargetOutcome{Target: t}
		switch {
		case res.DryRun:
			outcome.Skipped = true
			outcome.SkipReason = dryRunReason(s.policy)
			s.stats.SkippedDisabled++
		case s.throttled(t, now, interval):
			outcome.Skipped = true
			outcome.SkipReason = fmt.Sprintf("节流：同一过滤器在 %s 内只下发一次", interval)
		default:
			taskID, derr := s.runOne(ctx, t, timeout)
			if derr != nil {
				outcome.Err = derr
				s.stats.Failed++
				s.stats.LastError = derr.Error()
			} else {
				outcome.TaskID = taskID
				s.stats.Submitted++
				s.markRun(t, now)
			}
		}
		if outcome.Skipped {
			res.Skipped++
		}
		if outcome.Err != nil {
			res.Failed++
		}
		if outcome.TaskID != "" {
			res.Submitted++
		}
		res.Outcomes = append(res.Outcomes, outcome)
	}
	return res, err
}

func (s *Sweeper) runOne(ctx context.Context, t SweepTarget, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = DefaultSweepTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return s.deleter.RunDeleteTask(runCtx, t.Filter, t.Before)
}

func (s *Sweeper) throttled(t SweepTarget, now time.Time, interval time.Duration) bool {
	if interval <= 0 {
		return false
	}
	last, ok := s.lastRun[t.Filter]
	return ok && now.Sub(last) < interval
}

func (s *Sweeper) markRun(t SweepTarget, now time.Time) {
	if s.lastRun == nil {
		s.lastRun = map[string]time.Time{}
	}
	s.lastRun[t.Filter] = now
}

// DescribeOutcomes 把逐目标结果折成一行可读摘要（日志用；条数多时只列前几条）。
func DescribeOutcomes(outcomes []TargetOutcome) string {
	if len(outcomes) == 0 {
		return "无目标"
	}
	sorted := append([]TargetOutcome(nil), outcomes...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Target.Level < sorted[j].Target.Level
	})
	var parts []string
	for i, o := range sorted {
		if i >= 8 {
			parts = append(parts, fmt.Sprintf("…共 %d 条", len(sorted)))
			break
		}
		switch {
		case o.Err != nil:
			parts = append(parts, fmt.Sprintf("FAIL(%s):%v", o.Target.Level, o.Err))
		case o.Skipped:
			parts = append(parts, fmt.Sprintf("skip(%s)", o.Target.Level))
		default:
			parts = append(parts, fmt.Sprintf("ok(%s task=%s)", o.Target.Level, o.TaskID))
		}
	}
	return strings.Join(parts, " ")
}
