package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 业务对账协调接口。
//
// 核心原则（规格 §2）：
//
//	**JM 只做摆渡，不做差异计算。**
//
// 差异计算需要理解业务语义，那是业务台的职责。因此本服务刻意只做四件事：
// 解析目标 → 并发下发调用方指定的读动作 → 逐实例原样透传插件 output → 返回。
//
// 明确不做的事（每一条都是刻意的设计选择，不是实现偷懒）：
//   - **不预设动作名**：`action` 由业务台指定，代码里不存在 `if action == "snapshot"` 这类分支。
//   - **不解析 output**：output 以 json.RawMessage 原样搬运，JM 不 unmarshal、不校验结构、不做差异。
//   - **不缓存、不落库**：快照内容仅在请求生命周期内存在于响应体；幂等重放除外（见下）。
// **不算风险**：对账是只读动作，不适用的风险分级，也不需要 confirmToken。
//
// 唯一保留的写侧约束是「拒绝 WRITE/DESTRUCTIVE 动作」：对账通道是只读入口，
// 若允许它跑写动作，就等于绕开了批量命令的权限节点与二次确认。

const (
	// BusinessReconcileConcurrency 单次对账并发默认值（规格 §5：默认 20，避免打爆 Worker）。
	BusinessReconcileConcurrency = 20
	// BusinessReconcileInstanceTimeout 单实例对账超时，**比批量命令的 30s 宽松**（规格 §5：
	// 快照可能慢）。超时只影响该台实例，不牵连同批其他实例。
	BusinessReconcileInstanceTimeout = 60 * time.Second
	// BusinessReconcileIdempotencyWindow 幂等窗口（与批量命令一致：24 小时）。
	BusinessReconcileIdempotencyWindow = 24 * time.Hour
	// businessReconcileMaxTargets 单次对账目标数上限。
	// 与批量命令同量级：对账的响应体更大，但仍需一个硬上限防止单请求打爆 CP。
	businessReconcileMaxTargets = businessBatchMaxTargets
	// businessReconcileIdempotencyMaxRecords 内存中保留的幂等记录条数上限。
	//
	// 【为什么必须设上限】规格 §5 要求「不缓存、不在内存里堆积快照」，而幂等重放
	// （§4「建议保留」）**天然要求**把上次的快照留在内存里——两条要求直接冲突。
	// 折中是给幂等记录一个硬的条数上限：既保留「超时重试能拿回同一批快照」的能力，
	// 又保证最坏情况下的内存占用是常数而非随调用量线性增长。
	//
	// 超限时淘汰最旧的记录：被淘汰的 requestId 再来会当成新请求重新拉一次快照，
	// 代价是「多拉一次」而不是「拿到错的快照」，方向是安全的。
	businessReconcileIdempotencyMaxRecords = 128
)

// 对账相关错误（对应契约错误码）。
var (
	// ErrBusinessReconcileWriteAction 对账通道收到写动作：只读入口不许跑写（422 ACTION_UNSUPPORTED）。
	//
	// 复用 ACTION_UNSUPPORTED 而不是新造错误码：对调用方而言处置动作一致——
	// 「这个动作不能走这条通道」。契约里 409 段声明的是 ErrorBase，code 可自由取值；
	// 这里选 ACTION_UNSUPPORTED 是为了让调用方按一个已知 code 分支即可。
	ErrBusinessReconcileWriteAction = errors.New("对账通道只接受只读动作，该动作在 manifest 中声明为写操作")
)

// BusinessReconcileRequest 已校验的对账请求（服务层入参）。
//
// 与 BusinessBatchRequest 刻意分开：对账没有 confirmToken / reason / actionRisk，
// 少一层「字段在语义上不适用」的解读成本。
type BusinessReconcileRequest struct {
	// Target 目标选择（复用的目标解析）。
	Target BusinessTarget
	// Domain / Action 业务信封。Action 由调用方指定，JM 不预设。
	Domain string
	Action string
	// Payload 不透明载荷（原样下发，不做写上下文注入）。
	Payload json.RawMessage
	// RequestID 必填幂等键。
	RequestID string
	// IssuerKey 调用者身份键（幂等键作用域，防跨调用方读到彼此的快照）。
	IssuerKey string
}

// BusinessReconcileResult 单实例对账结果（对应 ReconcileResponse.results 项）。
//
// 与 BusinessInstanceResult 的差异：这里用 success 布尔而非五态 status。
// 为什么不在契约上加第二个枚举：对账的结果只有「拿到快照」和「没拿到」两种，
// 调用方据此决定要不要重试；细分「离线/超时/失败」的动机是运营处置，
// 而那部分信息已经在 error 文本里给足了。
type BusinessReconcileResult struct {
	InstanceID uint `json:"instanceId"`
	Success    bool `json:"success"`
	// Output 插件返回的原始快照，**JM 不解析**（json.RawMessage 原样搬运）。
	Output json.RawMessage `json:"output,omitempty"`
	// Error 失败原因（INSTANCE_OFFLINE / INSTANCE_TIMEOUT / 探针降级文案等）。
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"durationMs"`
}

// BusinessReconcileResponse 对账响应（对应 ReconcileResponse schema）。
//
// 只有 requestId + results 两个字段，与契约严格一致——规格不要求汇总计数，
// 加上去会让调用方误以为 JM 对结果有「语义」，而它只是搬运工。
type BusinessReconcileResponse struct {
	RequestID string `json:"requestId"`
	// Results 逐实例结果，顺序与解析后的目标实例顺序（ID 升序）一致。
	Results []BusinessReconcileResult `json:"results"`
	// IdempotentReplay 标记本次响应是幂等重放（非契约字段，omitempty 不污染正常响应）。
	IdempotentReplay bool `json:"idempotentReplay,omitempty"`
}

// SuccessfulCount 返回成功实例数（路由层流式 done 帧用；不作为契约字段）。
func (r *BusinessReconcileResponse) SuccessfulCount() int {
	if r == nil {
		return 0
	}
	n := 0
	for _, item := range r.Results {
		if item.Success {
			n++
		}
	}
	return n
}

// reconcileIdempotencyRecord 是一次对账的幂等快照。
//
// 【为什么对账也要幂等】规格 §4 表里对账的幂等一栏是「建议保留」。保留的理由是
// 调用方超时重试时能拿到同一批快照，避免「第一次成功第二次部分失败」让业务台
// 以为数据不一致。记录存在内存，进程重启即失（与批量命令同一取舍）。
type reconcileIdempotencyRecord struct {
	digest    string
	response  *BusinessReconcileResponse
	createdAt time.Time
}

// BusinessReconcileService 业务对账服务。
//
// 【为什么不复用一个 *BusinessBatchService 实例】它对写语义有硬编码假设：
// 执行前必须过 confirmToken 与风险分级、payload 必须是对象、非 READ 动作要注入写上下文。
// 这些在对账场景下全是错的。因此本服务**复用它的组件**（目标解析器、BusinessService
// 通道、错误映射的口径），而不是复用它的流程。
type BusinessReconcileService struct {
	db       *gorm.DB
	biz      *BusinessService
	resolver *BusinessTargetResolver

	concurrency     int
	instanceTimeout time.Duration
	now             func() time.Time

	// manifestLookup 拉取某实例的业务能力清单（默认为 biz.Manifest）。抽成字段便于测试注入。
	manifestLookup func(instanceID uint) (*BusinessResult, error)
	// dispatch 下发一次只读业务命令（默认为 biz.DispatchWithTimeout）。
	//
	// 抽成字段不只是为了测试：对账的单实例超时（60s）必须真正传到 gRPC 层，
	// 否则会被 BusinessService 内部的 8s 单命令超时先掐断，规格 §5 的 60s 形同虚设。
	dispatch func(instanceID uint, domain, action, payloadJSON string, timeout time.Duration) (*BusinessResult, error)

	// idemMax 幂等记录条数上限（见 businessReconcileIdempotencyMaxRecords）。
	idemMax int
	idemMu  sync.Mutex
	idem    map[string]*reconcileIdempotencyRecord
}

// NewBusinessReconcileService 创建业务对账服务。
func NewBusinessReconcileService(db *gorm.DB, biz *BusinessService) *BusinessReconcileService {
	s := &BusinessReconcileService{
		db:              db,
		biz:             biz,
		resolver:        NewBusinessTargetResolver(db),
		concurrency:     BusinessReconcileConcurrency,
		instanceTimeout: BusinessReconcileInstanceTimeout,
		now:             time.Now,
		idemMax:         businessReconcileIdempotencyMaxRecords,
		idem:            make(map[string]*reconcileIdempotencyRecord),
	}
	if biz != nil {
		s.manifestLookup = biz.Manifest
		s.dispatch = biz.DispatchWithTimeout
	}
	return s
}

// SetConcurrency 覆盖并发度（测试/运维调参用）。
func (s *BusinessReconcileService) SetConcurrency(n int) {
	if s == nil || n <= 0 {
		return
	}
	s.concurrency = n
}

// SetInstanceTimeout 覆盖单实例超时（默认 60s，独立于批量命令的 30s）。
func (s *BusinessReconcileService) SetInstanceTimeout(d time.Duration) {
	if s == nil || d <= 0 {
		return
	}
	s.instanceTimeout = d
}

// SetClock 覆盖时钟（测试用）。
func (s *BusinessReconcileService) SetClock(now func() time.Time) {
	if s == nil || now == nil {
		return
	}
	s.now = now
}

// SetManifestLookup 覆盖 manifest 拉取实现（测试注入用；nil 时恢复默认）。
func (s *BusinessReconcileService) SetManifestLookup(fn func(instanceID uint) (*BusinessResult, error)) {
	if s == nil {
		return
	}
	if fn == nil {
		if s.biz != nil {
			s.manifestLookup = s.biz.Manifest
		} else {
			s.manifestLookup = nil
		}
		return
	}
	s.manifestLookup = fn
}

// SetIdempotencyMaxRecords 覆盖幂等记录条数上限（测试/运维调参用）。
func (s *BusinessReconcileService) SetIdempotencyMaxRecords(n int) {
	if s == nil || n <= 0 {
		return
	}
	s.idemMax = n
}

// Resolver 暴露目标解析器（与批量命令同一实例口径）。
func (s *BusinessReconcileService) Resolver() *BusinessTargetResolver {
	if s == nil {
		return nil
	}
	return s.resolver
}

// Reconcile 同步执行对账，返回逐实例原始快照。
//
// progress 为 nil 时不同步推送（同步模式）；非 nil 时每完成一个实例回调一次，
// 供流式处理器逐实例下发（规格 §5「流式返回」）。
//
// 【回调的并发约定】回调在**各实例的工作协程内**调用（并发度 = 服务并发度），
// 因此实现必须同时满足两条：
//   - 线程安全（不同实例的回调可能同时发生，顺序也不保证）；
//   - 非阻塞（每次回调占住一个并发槽位，慢回调会直接拖慢对账本身）。
//
// 流式处理器用「带缓冲 channel + 非阻塞投递」满足这两条（见 router 层）。
func (s *BusinessReconcileService) Reconcile(
	ctx context.Context,
	req BusinessReconcileRequest,
	scope BusinessTargetScope,
	progress func(BusinessReconcileResult),
) (*BusinessReconcileResponse, error) {
	if s == nil {
		return nil, fmt.Errorf("业务对账服务未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// ① 参数校验（最便宜的拒法放最前）。
	if strings.TrimSpace(req.RequestID) == "" {
		return nil, fmt.Errorf("%w: requestId 必填", ErrInvalidBusinessCommand)
	}
	if strings.TrimSpace(req.Domain) == "" || strings.TrimSpace(req.Action) == "" {
		return nil, ErrInvalidBusinessCommand
	}

	// ② 幂等检查：同 requestId + 同参数直接回放首次结果，不重复下发。
	digest := businessReconcileRequestDigest(req)
	if cached, idemErr := s.beginIdempotent(req.IssuerKey, req.RequestID, digest); idemErr != nil {
		return nil, idemErr
	} else if cached != nil {
		replay := *cached
		replay.IdempotentReplay = true
		if progress != nil {
			for _, item := range replay.Results {
				progress(item)
			}
		}
		return &replay, nil
	}

	// ③ 目标解析（复用的解析器与范围收敛）。
	resolved, err := s.prepareTarget(req.Target, scope)
	if err != nil {
		// 前置失败不构成一次成功的幂等记录，必须撤销占位，否则调用方修正参数后
		// 用同一 requestId 重试会拿到一个不存在的「首次结果」。
		s.abortIdempotent(req.IssuerKey, req.RequestID, digest)
		return nil, err
	}

	// ④ 动作只读校验（需要 manifest 的那一步）。
	if err := s.guardReadOnlyAction(resolved, req); err != nil {
		s.abortIdempotent(req.IssuerKey, req.RequestID, digest)
		return nil, err
	}

	// ⑤ 执行。
	resp := s.run(ctx, resolved, req, progress)
	s.finishIdempotent(req.IssuerKey, req.RequestID, digest, resp)
	logBusinessReconcileSummary(resp)
	return resp, nil
}

// prepareTarget 解析目标并做规模上限判定。
func (s *BusinessReconcileService) prepareTarget(target BusinessTarget, scope BusinessTargetScope) (*BusinessResolvedTarget, error) {
	resolved, err := s.resolver.ResolveScoped(target, scope)
	if err != nil {
		return nil, err
	}
	if resolved.Count() == 0 {
		return nil, ErrBusinessTargetEmpty
	}
	if resolved.Count() > businessReconcileMaxTargets {
		return nil, fmt.Errorf("对账目标数 %d 超过上限 %d", resolved.Count(), businessReconcileMaxTargets)
	}
	return resolved, nil
}

// guardReadOnlyAction 确认动作是只读的，否则拒绝整个请求。
//
// 【为什么要调 manifest】对账是只读入口。若允许它跑写动作，就等于给了调用方一条
// 绕过批量命令权限节点（instance:business:write）与二次确认的旁路——同一个写动作
// 换条通道就能执行，整个535 的写侧硬化就失效了。
//
// 【为什么只查一台实例】manifest 是插件侧的声明，同一业务的多个实例通常跑同一插件；
// 逐台拉 100 份 manifest 只为确认一个动作名，成本（100 次跨网络往返）远超收益。
// 取解析后的**第一台运行中实例**；一台都不可达时无从校验。
//
// 【降级策略】拉不到 manifest（探针未连／节点未连／插件无业务 Provider）时**放行**：
//   - 放行的代价有限：真正的下发仍会逐实例失败并给出明确错误；
//   - 拒绝的代价很大：探针刚重连（恰好是规格 §6 列的对账触发时机）时 manifest 可能
//     还取不到，此时拒绝会让「重连后立即对账」这个最常见的用法完全不可用。
//
// 【为什么声明缺失也放行】动作未在 manifest 中声明时，插件自己会拒绝并回执错误，
// 逐实例错误比整体 422 的信息量更大（可能只是那台实例的插件版本旧）。
// 但声明为 WRITE / DESTRUCTIVE 必须拒绝——这是「这条通道不许跑写」的硬边界。
func (s *BusinessReconcileService) guardReadOnlyAction(resolved *BusinessResolvedTarget, req BusinessReconcileRequest) error {
	if resolved == nil || s.manifestLookup == nil {
		return nil
	}
	probeID, ok := firstRunningInstanceID(resolved.Instances)
	if !ok {
		// 没有任何运行中实例：无从校验，且每台都会在 run 阶段被判 offline。
		return nil
	}
	manifest, err := s.manifestLookup(probeID)
	if err != nil {
		slog.Warn("对账动作只读校验：manifest 拉取失败，放行（由插件侧最终裁决）",
			"instanceId", probeID, "domain", req.Domain, "action", req.Action, "error", err)
		return nil
	}
	if manifest == nil || !manifest.Available {
		slog.Warn("对账动作只读校验：manifest 不可用，放行（由插件侧最终裁决）",
			"instanceId", probeID, "domain", req.Domain, "action", req.Action)
		return nil
	}
	risk, declared, parseErr := lookupActionRisk(manifest.Output, req.Domain, req.Action)
	if parseErr != nil {
		// 清单结构不符合预期：按「不可判定」处理，不因为 JM 解析失败而拒绝只读请求。
		slog.Warn("对账动作只读校验：manifest 结构无法解析，放行",
			"instanceId", probeID, "domain", req.Domain, "action", req.Action, "error", parseErr)
		return nil
	}
	if !declared {
		// 未声明：交给插件裁决（逐实例错误比整体 422 更有信息量）。
		return nil
	}
	if NormalizeActionRisk(risk) != BusinessActionRiskRead {
		return fmt.Errorf("%w（%s.%s 声明为 %s）", ErrBusinessReconcileWriteAction, req.Domain, req.Action, risk)
	}
	return nil
}

// firstRunningInstanceID 返回第一台运行中实例的 ID。
func firstRunningInstanceID(instances []model.Instance) (uint, bool) {
	for _, inst := range instances {
		if inst.Status == model.InstanceStatusRunning {
			return inst.ID, true
		}
	}
	return 0, false
}

// manifestEnvelope 只抽取 manifest 输出里跟「动作是否只读」有关的最小结构。
//
// 这是 JM 对 manifest 内容的**唯一**一处读取，且刻意做得极窄：只找
// domains.<domain>.actions[] 里与请求同名的动作，读它的 risk 字段。
// 其余内容（inputSchema / outputSchema / attributes…）一律不碰——ADR-106 的边界是
// 「不把 manifest 内容当业务语义来源」，而区分 READ 与 WRITE 是通道安全约束，不是业务语义。
type manifestEnvelope struct {
	Domains map[string]struct {
		Actions []struct {
			Name string `json:"name"`
			Risk string `json:"risk"`
		} `json:"actions"`
	} `json:"domains"`
}

// lookupActionRisk 在 manifest 输出里查某域某动作的风险等级。
//
// 返回 (risk, declared, err)：
//   - declared=false 表示清单里没有这个域或这个动作；
//   - err 非 nil 表示清单整体结构无法解析（调用方按「不可判定」放行）。
func lookupActionRisk(output json.RawMessage, domain, action string) (string, bool, error) {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" || trimmed == "null" {
		return "", false, nil
	}
	var env manifestEnvelope
	if err := json.Unmarshal([]byte(trimmed), &env); err != nil {
		return "", false, err
	}
	d, ok := env.Domains[domain]
	if !ok {
		return "", false, nil
	}
	for _, a := range d.Actions {
		if a.Name == action {
			return a.Risk, true, nil
		}
	}
	return "", false, nil
}

// run 是执行主体：有界并发下发，逐实例收敛结果。
//
// 与批量命令 run 的差异：
//   - 结果只有 success 布尔（不是五态），因为调用方只需要知道「有没有拿到快照」；
//   - 每完成一个实例就回调一次 progress（对账要逐实例流式，不是按批）；
//   - 不做汇总计数（契约不含）。
func (s *BusinessReconcileService) run(
	ctx context.Context,
	resolved *BusinessResolvedTarget,
	req BusinessReconcileRequest,
	progress func(BusinessReconcileResult),
) *BusinessReconcileResponse {
	total := resolved.Count()
	resp := &BusinessReconcileResponse{
		RequestID: req.RequestID,
		Results:   make([]BusinessReconcileResult, total),
	}
	payload := strings.TrimSpace(string(req.Payload))

	conc := s.concurrency
	if conc <= 0 {
		conc = BusinessReconcileConcurrency
	}
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup

	for i := range resolved.Instances {
		inst := resolved.Instances[i]
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			// 已取消：未派发的实例填明确错误，保证 results 长度与目标一致。
			for j := i; j < total; j++ {
				resp.Results[j] = BusinessReconcileResult{
					InstanceID: resolved.Instances[j].ID,
					Error:      "调用已取消，未下发",
				}
				if progress != nil {
					progress(resp.Results[j])
				}
			}
			wg.Wait()
			return resp
		}

		wg.Add(1)
		go func(idx int, target model.Instance) {
			defer wg.Done()
			defer func() { <-sem }()
			result := s.runOne(ctx, target, req, payload)
			resp.Results[idx] = result
			// 逐实例回调：对账的流式语义是「拿到一个推一个」，不是按批。
			if progress != nil {
				progress(result)
			}
		}(i, inst)
	}
	wg.Wait()
	return resp
}

// runOne 对单实例下发读动作并归类结果。
//
// 分类规则（与批量命令的 offline 口径一致，保证运营看到的是同一套词）：
//   - 实例非 RUNNING              → 明确错误（未下发）；
//   - 节点未连 / 探针未连         → 明确错误（未下发）；
//   - 超过单实例超时              → INSTANCE_TIMEOUT；
//   - 探针回执失败                → 回执里的原始原因；
//   - 回执成功                    → output 原样透传（json.RawMessage，不解析）。
func (s *BusinessReconcileService) runOne(
	ctx context.Context, inst model.Instance, req BusinessReconcileRequest, payload string,
) BusinessReconcileResult {
	started := s.now()
	out := BusinessReconcileResult{InstanceID: inst.ID}
	elapsed := func() int64 { return s.now().Sub(started).Milliseconds() }

	if inst.Status != model.InstanceStatusRunning {
		out.Error = fmt.Sprintf("INSTANCE_OFFLINE: 实例未运行（当前状态 %s）", inst.Status)
		out.DurationMs = elapsed()
		return out
	}
	if s.biz == nil || s.dispatch == nil {
		out.Error = "业务通道未初始化"
		out.DurationMs = elapsed()
		return out
	}

	// 单实例超时用独立 context：超时只影响这一台，不牵连同批其他实例（规格 §7 验收 4）。
	cctx, cancel := context.WithTimeout(ctx, s.instanceTimeout)
	defer cancel()

	type outcome struct {
		res *BusinessResult
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		// 只读动作走纯透传（不注入写上下文）——对账是读，注入 operator/taskId
		// 会污染插件侧流水，且写上下文是「高危写」的审计约定。
		//
		// 超时同样传给业务层：否则它内部的 8s 单命令超时会先掐断，
		// 使这里配的 60s 成为不可达的上限。
		res, err := s.dispatch(inst.ID, req.Domain, req.Action, payload, s.instanceTimeout)
		ch <- outcome{res: res, err: err}
	}()

	select {
	case <-cctx.Done():
		out.Error = fmt.Sprintf("INSTANCE_TIMEOUT: 下发后 %s 内未收到回执", s.instanceTimeout)
		out.DurationMs = elapsed()
		return out
	case got := <-ch:
		out.DurationMs = elapsed()
		if got.err != nil {
			out.Error = got.err.Error()
			return out
		}
		switch {
		case got.res == nil:
			out.Error = "无业务响应"
		case got.res.Available:
			out.Success = true
			// 原样搬运：不 unmarshal、不校验结构、不做任何差异计算。
			out.Output = got.res.Output
		case isBusinessOfflineReason(got.res.Error):
			// 节点未连 / 探针未连：语义上是「没下发」，与 offline 同一口径。
			out.Error = "INSTANCE_OFFLINE: " + got.res.Error
		default:
			out.Error = got.res.Error
		}
		return out
	}
}

// businessReconcileRequestDigest 计算对账请求参数指纹（幂等冲突检测）。
//
// 覆盖目标集合 + 业务信封 + payload：任一变化都说明「同一个 requestId 被用在了
// 不同的事情上」，必须拒绝而不是静默返回旧快照——那样业务台会拿着上一轮的
// 数据去做差异计算，得出完全错误的结论。
func businessReconcileRequestDigest(req BusinessReconcileRequest) string {
	canonical := struct {
		GroupIDs           []uint               `json:"groupIds"`
		IncludeDescendants bool                 `json:"includeDescendants"`
		InstanceIDs        []uint               `json:"instanceIds"`
		Filters            *BusinessBatchFilter `json:"filters"`
		Domain             string               `json:"domain"`
		Action             string               `json:"action"`
		Payload            json.RawMessage      `json:"payload"`
	}{
		GroupIDs:           canonicalInstanceIDs(req.Target.GroupIDs),
		IncludeDescendants: req.Target.IncludeDescendants,
		InstanceIDs:        canonicalInstanceIDs(req.Target.InstanceIDs),
		Filters:            req.Target.Filters,
		Domain:             req.Domain,
		Action:             req.Action,
		Payload:            req.Payload,
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		// 序列化失败不该让请求通过幂等闸：用随机值使每次都被视为新请求
		// （宁可重复拉一次快照，也不误回放上一次的快照）。
		return fmt.Sprintf("marshal-error:%d", time.Now().UnixNano())
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// beginIdempotent 检查幂等窗口（返回语义与批量命令同名方法一致）。
func (s *BusinessReconcileService) beginIdempotent(issuerKey, requestID, digest string) (*BusinessReconcileResponse, error) {
	s.idemMu.Lock()
	defer s.idemMu.Unlock()
	s.evictExpiredLocked()

	key := idempotencyKey(issuerKey, requestID)
	rec, ok := s.idem[key]
	if !ok {
		s.idem[key] = &reconcileIdempotencyRecord{digest: digest, createdAt: s.now()}
		// 插入后再压一次上限：只在检查前裁剪会让实际条数恒为「上限 + 1」，
		// 长期高频调用下内存仍会超出一格。
		s.trimLocked()
		return nil, nil
	}
	if rec.digest != digest {
		return nil, ErrBusinessIdempotencyKeyReused
	}
	if rec.response == nil {
		// 同请求正在执行中：允许本次继续（并发重复提交极罕见，且两次都会写回结果）。
		return nil, nil
	}
	return rec.response, nil
}

// finishIdempotent 写回首次结果。
func (s *BusinessReconcileService) finishIdempotent(issuerKey, requestID, digest string, resp *BusinessReconcileResponse) {
	if resp == nil {
		return
	}
	s.idemMu.Lock()
	defer s.idemMu.Unlock()
	key := idempotencyKey(issuerKey, requestID)
	rec, ok := s.idem[key]
	if !ok || rec.digest != digest {
		s.idem[key] = &reconcileIdempotencyRecord{digest: digest, response: resp, createdAt: s.now()}
		return
	}
	rec.response = resp
}

// abortIdempotent 撤销一次未产生结果的幂等占位（仅当尚未写入 response）。
func (s *BusinessReconcileService) abortIdempotent(issuerKey, requestID, digest string) {
	if requestID == "" {
		return
	}
	s.idemMu.Lock()
	defer s.idemMu.Unlock()
	key := idempotencyKey(issuerKey, requestID)
	if rec, ok := s.idem[key]; ok && rec.digest == digest && rec.response == nil {
		delete(s.idem, key)
	}
}

// evictExpiredLocked 清理超窗记录（调用方须持锁）。
func (s *BusinessReconcileService) evictExpiredLocked() {
	cutoff := s.now().Add(-BusinessReconcileIdempotencyWindow)
	for key, rec := range s.idem {
		if rec.createdAt.Before(cutoff) {
			delete(s.idem, key)
		}
	}
	s.trimLocked()
}

// trimLocked 把记录数压到上限以内（调用方须持锁）。
//
// 时间窗与条数上限缺一不可：前者管「多久以前的不再回放」，后者管「最多占多少内存」。
// 只有时间窗时，24 小时内的高频调用仍能把内存堆满（每份记录都含完整快照）。
func (s *BusinessReconcileService) trimLocked() {
	limit := s.idemMax
	if limit <= 0 {
		limit = businessReconcileIdempotencyMaxRecords
	}
	for len(s.idem) > limit {
		// 淘汰最旧的一条。map 遍历无序，因此每轮找最小 createdAt——
		// 上限通常是几十~上百条，线性扫描的成本远低于维护一个额外索引的复杂度。
		var oldestKey string
		var oldestAt time.Time
		for key, rec := range s.idem {
			if oldestKey == "" || rec.createdAt.Before(oldestAt) {
				oldestKey, oldestAt = key, rec.createdAt
			}
		}
		if oldestKey == "" {
			return
		}
		delete(s.idem, oldestKey)
	}
}

// IdempotencyWindowSize 返回当前内存中的对账幂等记录数（诊断/测试）。
func (s *BusinessReconcileService) IdempotencyWindowSize() int {
	if s == nil {
		return 0
	}
	s.idemMu.Lock()
	defer s.idemMu.Unlock()
	return len(s.idem)
}

// BusinessReconcileErrorCode 把对账错误映射为契约错误码与 HTTP 状态码。
//
// 复用批量命令的错误映射（同一套 code ↔ status 口径），只额外处理对账特有的两条。
func BusinessReconcileErrorCode(err error) (code string, status int) {
	switch {
	case err == nil:
		return "", 200
	case errors.Is(err, ErrBusinessReconcileWriteAction):
		return "ACTION_UNSUPPORTED", 422
	}
	return BusinessBatchErrorCode(err)
}

// logBusinessReconcileSummary 记录对账摘要（通道维度，**不含 payload/output**）。
func logBusinessReconcileSummary(resp *BusinessReconcileResponse) {
	if resp == nil {
		return
	}
	slog.Info("业务对账完成",
		"requestId", resp.RequestID,
		"total", len(resp.Results),
		"succeeded", resp.SuccessfulCount(),
	)
}
