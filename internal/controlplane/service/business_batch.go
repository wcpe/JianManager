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

// 业务批量命令执行引擎（执行语义流式进度）。
//
// 设计要点：
//   - **复用 InstanceBatchService 的分片有界并发模型**（信号量 + WaitGroup），但业务命令
//     必须经 BusinessService.DispatchWrite 走插件通道——业务语义只有插件知道，JM 只转发信封。
//   - **部分失败不整体失败**：HTTP 200，逐实例结果在 results 里表达。
//   - **同步与流式共用同一份执行逻辑**：执行协程按批推 `progress`，最终结果既是 done 帧也是
//     同步响应的 body，两条路径不可能给出不一致的数字。

const (
	// BusinessBatchConcurrency 单批并发默认值（§6.1）。
	BusinessBatchConcurrency = 20
	// BusinessBatchInstanceTimeout 单实例超时默认值，**独立于**单实例命令的 5s（§6.1）。
	BusinessBatchInstanceTimeout = 30 * time.Second
	// BusinessBatchIdempotencyWindow 幂等窗口（§6.3：24 小时）。
	BusinessBatchIdempotencyWindow = 24 * time.Hour
	// BusinessBatchProgressBatch 每完成多少个实例推送一次 progress 帧（§3）。
	BusinessBatchProgressBatch = 20
	// businessBatchMaxTargets 单次批量目标数上限，防止单请求打爆 CP。
	businessBatchMaxTargets = 5000
)

// 逐实例结果状态（InstanceResult.status 枚举）。
//
// 五个状态必须严格区分——它们对操作者的含义完全不同：
// 离线是「没下发」，超时是「下发了但不知道结果」，skipped 是「前置校验就没过」。
const (
	// BusinessInstanceSuccess 探针回执成功。
	BusinessInstanceSuccess = "success"
	// BusinessInstanceFailed 探针回执失败（含错误码）。
	BusinessInstanceFailed = "failed"
	// BusinessInstanceOffline 目标离线/探针未连（**未下发**）。
	BusinessInstanceOffline = "offline"
	// BusinessInstanceTimeout 下发后超时未回执。
	BusinessInstanceTimeout = "timeout"
	// BusinessInstanceSkipped 前置校验失败或目标已不可达。
	BusinessInstanceSkipped = "skipped"
)

// BusinessInstanceResult 单实例执行结果（对应 InstanceResult schema）。
type BusinessInstanceResult struct {
	InstanceID uint            `json:"instanceId"`
	Status     string          `json:"status"`
	Output     json.RawMessage `json:"output,omitempty"`
	Error      string          `json:"error,omitempty"`
	DurationMs int64           `json:"durationMs"`
}

// BusinessBatchRequest 已校验的批量执行请求（服务层入参）。
type BusinessBatchRequest struct {
	// Target 目标选择。同步/流式执行时若带 confirmToken，则由令牌正文提供并覆盖此字段。
	Target BusinessTarget
	// ConfirmToken 预览返回的确认令牌。
	ConfirmToken string
	// Domain / Action 业务信封。
	Domain string
	Action string
	// Payload 不透明载荷（CP 不解析业务内容）。
	Payload json.RawMessage
	// ActionRisk 动作自身风险（READ / WRITE / DESTRUCTIVE），由业务台按 manifest 透传。
	ActionRisk string
	// RequestID 必填幂等键。
	RequestID string
	// Reason critical 动作必填。
	Reason string
	// IssuerKey 调用者身份键（令牌归属校验 + 幂等键作用域）。
	IssuerKey string
	// Operator / OperatorID 操作者身份（注入插件 payload 供下游追责）。
	Operator   string
	OperatorID uint
}

// BusinessBatchResponse 批量执行响应（对应 BatchExecuteResponse schema）。
type BusinessBatchResponse struct {
	RequestID string `json:"requestId"`
	Total     int    `json:"total"`
	Succeeded int    `json:"succeeded"`
	Failed    int    `json:"failed"`
	Offline   int    `json:"offline"`
	// Timeout / Skipped 也在计数里体现（契约未列，但逐实例状态已含这两种，缺了会让
	// total ≠ succeeded + failed + offline 而无法对账）。
	Timeout int                      `json:"timeout"`
	Skipped int                      `json:"skipped"`
	Results []BusinessInstanceResult `json:"results"`
	// IdempotentReplay 标记本次响应是幂等重放（§8 IDEMPOTENT_REPLAY），便于调用方区分。
	IdempotentReplay bool `json:"idempotentReplay,omitempty"`
}

// BusinessBatchProgress 流式进度帧（§3）。
type BusinessBatchProgress struct {
	Done      int `json:"done"`
	Total     int `json:"total"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Offline   int `json:"offline"`
	Timeout   int `json:"timeout,omitempty"`
	Skipped   int `json:"skipped,omitempty"`
}

// BusinessBatchPreview 预览结果（服务层内部表示，路由层负责组装响应体）。
type BusinessBatchPreview struct {
	Resolved     *BusinessResolvedTarget
	Risk         BusinessRiskAssessment
	ConfirmToken string
	ExpiresAt    time.Time
}

// idempotencyRecord 是一次批量执行的幂等快照。
//
// 只存在内存（§6.3「内存 + 可选持久化」）：进程重启后在途记录丢失，调用方凭 requestId 重试
// 会被当成新请求重新执行——这是规格接受的取舍（结果本身不落库，ADR-106 不持有业务数据）。
type idempotencyRecord struct {
	digest    string
	response  *BusinessBatchResponse
	createdAt time.Time
}

// BusinessBatchService 业务批量命令服务。
type BusinessBatchService struct {
	db       *gorm.DB
	biz      *BusinessService
	resolver *BusinessTargetResolver
	signer   *BusinessConfirmTokenSigner

	concurrency     int
	instanceTimeout time.Duration
	progressEvery   int
	now             func() time.Time

	idemMu sync.Mutex
	idem   map[string]*idempotencyRecord
}

// NewBusinessBatchService 创建业务批量命令服务。
//
// signer 为 nil 时，high/critical 风险无法签发令牌——服务仍可用（低风险路径），
// 但高风险预览会返回明确的错误而不是签出一个不受保护的令牌。
func NewBusinessBatchService(db *gorm.DB, biz *BusinessService, signer *BusinessConfirmTokenSigner) *BusinessBatchService {
	return &BusinessBatchService{
		db:              db,
		biz:             biz,
		resolver:        NewBusinessTargetResolver(db),
		signer:          signer,
		concurrency:     BusinessBatchConcurrency,
		instanceTimeout: BusinessBatchInstanceTimeout,
		progressEvery:   BusinessBatchProgressBatch,
		now:             time.Now,
		idem:            make(map[string]*idempotencyRecord),
	}
}

// SetConcurrency 覆盖单批并发度（测试/运维调参用）。
func (s *BusinessBatchService) SetConcurrency(n int) {
	if s == nil || n <= 0 {
		return
	}
	s.concurrency = n
}

// SetInstanceTimeout 覆盖单实例超时。
func (s *BusinessBatchService) SetInstanceTimeout(d time.Duration) {
	if s == nil || d <= 0 {
		return
	}
	s.instanceTimeout = d
}

// SetProgressBatch 覆盖 progress 帧的批次粒度。
func (s *BusinessBatchService) SetProgressBatch(n int) {
	if s == nil || n <= 0 {
		return
	}
	s.progressEvery = n
}

// SetClock 覆盖时钟（测试用）。
func (s *BusinessBatchService) SetClock(now func() time.Time) {
	if s == nil || now == nil {
		return
	}
	s.now = now
}

// Resolver 暴露目标解析器（预览与执行共用同一实例，保证口径一致）。
func (s *BusinessBatchService) Resolver() *BusinessTargetResolver {
	if s == nil {
		return nil
	}
	return s.resolver
}

// Signer 暴露令牌签名器（路由层构造处理器时取用；nil 表示未配置密钥）。
func (s *BusinessBatchService) Signer() *BusinessConfirmTokenSigner {
	if s == nil {
		return nil
	}
	return s.signer
}

// Preview 解析目标并评估风险，签发 confirmToken（§3.1 / §7.1）。
//
// 无副作用、可重复调用。目标解析为空返回 ErrBusinessTargetEmpty。
func (s *BusinessBatchService) Preview(target BusinessTarget, actionRisk, issuerKey string, scope BusinessTargetScope) (*BusinessBatchPreview, error) {
	if s == nil {
		return nil, fmt.Errorf("业务批量服务未初始化")
	}
	resolved, err := s.resolver.ResolveScoped(target, scope)
	if err != nil {
		return nil, err
	}
	if resolved.Count() == 0 {
		return nil, ErrBusinessTargetEmpty
	}
	msgScope, err := s.resolver.ClassifyScope(resolved, DefaultBusinessScopeLevels)
	if err != nil {
		return nil, err
	}
	risk := AssessBusinessRisk(BusinessRiskInput{
		Target:     resolved,
		Scope:      msgScope,
		ActionRisk: actionRisk,
	})
	preview := &BusinessBatchPreview{Resolved: resolved, Risk: risk}

	// 低风险不签发令牌（前端不展示确认步骤，凭空给一个令牌只会诱导误用）。
	if !risk.RequiresConfirmation {
		return preview, nil
	}
	if s.signer == nil {
		return nil, fmt.Errorf("高风险批量预览需要确认令牌签名器，但未配置签名密钥")
	}
	token, expiresAt, err := s.signer.Issue(issuerKey, target, resolved.Hash(), risk.Level)
	if err != nil {
		return nil, err
	}
	preview.ConfirmToken = token
	preview.ExpiresAt = expiresAt
	return preview, nil
}

// Execute 同步执行批量命令，返回完整结果集（§7.2）。
//
// progress 为 nil 时不同步推送进度（同步模式）。
func (s *BusinessBatchService) Execute(ctx context.Context, req BusinessBatchRequest, scope BusinessTargetScope) (*BusinessBatchResponse, error) {
	return s.ExecuteWithProgress(ctx, req, scope, nil)
}

// ExecuteWithProgress 执行批量命令，可选在每批完成时回调进度。
//
// 回调在**执行协程内同步调用**，因此实现必须是非阻塞的（流式处理器用有界 channel 投递）；
// 回调返回值被忽略，避免让「客户端写失败」反向影响执行（§5：断开 ≠ 取消）。
func (s *BusinessBatchService) ExecuteWithProgress(
	ctx context.Context,
	req BusinessBatchRequest,
	scope BusinessTargetScope,
	progress func(BusinessBatchProgress),
) (*BusinessBatchResponse, error) {
	if s == nil {
		return nil, fmt.Errorf("业务批量服务未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if req.RequestID == "" {
		return nil, fmt.Errorf("requestId 必填")
	}
	if req.Domain == "" || req.Action == "" {
		return nil, ErrInvalidBusinessCommand
	}
	// 批量写动作要把操作者/幂等上下文注入 payload，因此 payload 必须是 JSON 对象（或空）。
	// 提前拒绝，避免把「载荷形态不对」拖到逐实例阶段变成 100 条 failed。
	if err := validateBatchPayload(req.Payload); err != nil {
		return nil, err
	}

	// ── ① 令牌验签 + 归属 + 时效（此步不比对目标 hash）───────────────────
	target, claims, err := s.authenticateConfirmation(req, req.Target)
	if err != nil {
		return nil, err
	}

	// ── ② 幂等检查：同一 requestId + 同一参数直接回放首次结果 ─────────────
	//
	// 幂等闸刻意放在「目标漂移比对」之前：客户端超时重试时，分组可能已经又变了，
	// 此时期望的是拿回首次结果（那才是它当初确认并执行的那一次），而不是 TARGET_CHANGED。
	digest := businessBatchRequestDigest(target, req)
	if cached, idemErr := s.beginIdempotent(req.IssuerKey, req.RequestID, digest); idemErr != nil {
		return nil, idemErr
	} else if cached != nil {
		replay := *cached
		replay.IdempotentReplay = true
		// 重放同样要给出进度终态，否则流式调用方永远等不到 done 帧的 progress 对齐。
		if progress != nil {
			progress(progressOf(&replay))
		}
		return &replay, nil
	}

	resolved, risk, effectiveActionRisk, err := s.prepareExecution(req, target, claims, scope)
	if err != nil {
		// 前置校验未通过：撤销本次幂等占位。
		// 为什么必须撤销：调用方修正参数（或补上 token）后用同一个 requestId 重试时，
		// 若占位还在，会被判为「同请求正在执行中」而走进一条不会再产生结果的路径，
		// 表现为「重试静默什么都没做」。失败不构成一次成功的幂等记录。
		s.abortIdempotent(req.IssuerKey, req.RequestID, digest)
		return nil, err
	}

	// ── ⑥ 执行 ───────────────────────────────────────────────────────
	resp := s.run(ctx, resolved, req, effectiveActionRisk, progress)
	s.finishIdempotent(req.IssuerKey, req.RequestID, digest, resp)
	logBusinessBatchSummary(resp, risk.Level)
	return resp, nil
}

// prepareExecution 完成执行前的全部校验与解析：目标漂移比对 → 规模上限 → 风险复核 → reason。
//
// 抽成独立函数的目的不只是可读性：所有失败路径都必须让调用方撤销幂等占位，
// 把校验集中在一处才不会漏掉某条 return。
// 返回值里带 `effectiveActionRisk`（请求体与令牌 RiskLevel 取高）：
// 执行期必须按它判定 readOnly，否则客户端改一个字段就能让写动作降级成只读透传。
func (s *BusinessBatchService) prepareExecution(
	req BusinessBatchRequest, target BusinessTarget, claims *BusinessConfirmClaims, scope BusinessTargetScope,
) (*BusinessResolvedTarget, BusinessRiskAssessment, string, error) {
	// ── 重新解析目标并比对 token 绑定的 hash（§4.2）────────────────────
	resolved, err := s.resolver.ResolveScoped(target, scope)
	if err != nil {
		return nil, BusinessRiskAssessment{}, "", err
	}
	if claims != nil && (resolved.Count() == 0 || resolved.Hash() != claims.InstancesHash) {
		// 目标漂移：必须重新预览——这正是「确认时看到的就是将要执行的」的保证。
		return nil, BusinessRiskAssessment{}, "", ErrBusinessTargetChanged
	}
	if resolved.Count() == 0 {
		return nil, BusinessRiskAssessment{}, "", ErrBusinessTargetEmpty
	}
	if resolved.Count() > businessBatchMaxTargets {
		return nil, BusinessRiskAssessment{}, "", fmt.Errorf("批量目标数 %d 超过上限 %d", resolved.Count(), businessBatchMaxTargets)
	}

	// ── 风险复核：无令牌也要判断是否该拒绝 ──────────────────────────────
	msgScope, err := s.resolver.ClassifyScope(resolved, DefaultBusinessScopeLevels)
	if err != nil {
		return nil, BusinessRiskAssessment{}, "", err
	}
	// **动作风险取「请求体」与「令牌内」的较高者**（本文件此前最严重的一处缺陷）。
	//
	// 原先只信 `req.ActionRisk`（客户端字段），而票据里的 `claims.RiskLevel` 虽然签发了、
	// 却从未被比对。于是有两条绕过路径：
	//   ① 预览时用 DESTRUCTIVE 拿到令牌 → 执行时改传 READ → `RequiresReason` 变 false
	//      → 绕过「critical 必须填 reason」；
	//   ② 同一份客户端输入还决定 `readOnly`（见 executeOne）→ 从 DispatchWrite 降级为
	//      Dispatch → 写上下文（operator/operatorId/taskId）不再注入，下游审计与对账断链。
	//
	// 取 max 而不是「一律用令牌」：令牌只覆盖签发那一刻，无法表达执行期**新增**的风险
	// （例如目标集合里多出了离线实例、或调用方把动作换成了更高危的同类动作）。
	// 两者取高，两边都不会被对方的低估掩盖。
	effectiveActionRisk := resolveEffectiveActionRisk(req.ActionRisk, claims)
	risk := AssessBusinessRisk(BusinessRiskInput{Target: resolved, Scope: msgScope, ActionRisk: effectiveActionRisk})
	if req.ConfirmToken == "" && risk.RequiresConfirmation {
		return nil, BusinessRiskAssessment{}, "", ErrBusinessConfirmationRequired
	}

	// ── critical 必须填 reason ──────────────────────────────────────
	if risk.RequiresReason && req.Reason == "" {
		return nil, BusinessRiskAssessment{}, "", ErrBusinessReasonRequired
	}
	return resolved, risk, effectiveActionRisk, nil
}

// abortIdempotent 撤销一次未产生结果的幂等占位（仅当尚未写入 response）。
func (s *BusinessBatchService) abortIdempotent(issuerKey, requestID, digest string) {
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

// authenticateConfirmation 校验 confirmToken（若有），返回应当执行的目标选择与令牌正文。
//
// 只做「验签 / 归属 / 时效」，**不比对目标 hash**——hash 比对需要重新解析目标，
// 而幂等回放应当优先于漂移检查（见 ExecuteWithProgress 的步骤②）。
//
// 有令牌时**以令牌正文里的 target 为准**：令牌是签名的，它承载的是「用户确认时看到的选择」；
// 请求体里的 target 只在无令牌的低风险路径使用。
func (s *BusinessBatchService) authenticateConfirmation(req BusinessBatchRequest, bodyTarget BusinessTarget) (BusinessTarget, *BusinessConfirmClaims, error) {
	if req.ConfirmToken == "" {
		return bodyTarget, nil, nil
	}
	if s.signer == nil {
		return BusinessTarget{}, nil, ErrBusinessTokenInvalid
	}
	claims, err := s.signer.Verify(req.ConfirmToken, BusinessConfirmExpectation{IssuerKey: req.IssuerKey})
	if err != nil {
		return BusinessTarget{}, nil, err
	}
	return claims.Target, claims, nil
}

// validateBatchPayload 校验批量载荷形态：空载荷或 JSON 对象均可，其余拒绝。
func validateBatchPayload(payload json.RawMessage) error {
	trimmed := strings.TrimSpace(string(payload))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if !json.Valid([]byte(trimmed)) {
		return fmt.Errorf("%w: payload 不是合法 JSON", ErrInvalidBusinessCommand)
	}
	if !strings.HasPrefix(trimmed, "{") {
		return fmt.Errorf("%w: 批量业务命令的 payload 必须是 JSON 对象", ErrInvalidBusinessCommand)
	}
	return nil
}

// run 是执行主体：分片有界并发下发，逐实例收敛结果。
func (s *BusinessBatchService) run(
	ctx context.Context,
	resolved *BusinessResolvedTarget,
	req BusinessBatchRequest,
	effectiveActionRisk string,
	progress func(BusinessBatchProgress),
) *BusinessBatchResponse {
	total := resolved.Count()
	resp := &BusinessBatchResponse{
		RequestID: req.RequestID,
		Total:     total,
		Results:   make([]BusinessInstanceResult, total),
	}

	// payload 已在此前统一注入操作者上下文与幂等键（见 buildPayload），这里只做下发。
	payload := req.payloadString()

	conc := s.concurrency
	if conc <= 0 {
		conc = BusinessBatchConcurrency
	}
	sem := make(chan struct{}, conc)
	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	done := 0
	for i := range resolved.Instances {
		inst := resolved.Instances[i]
		// 拿 token 之前检查取消：客户端「取消」不该继续抢占新实例（已下发的照旧完成）。
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			// 未派发的实例统一记 skipped，保证 total == 各状态之和。
			for j := i; j < total; j++ {
				resp.Results[j] = BusinessInstanceResult{
					InstanceID: resolved.Instances[j].ID,
					Status:     BusinessInstanceSkipped,
					Error:      "调用已取消，未下发",
				}
			}
			wg.Wait()
			s.aggregate(resp)
			if progress != nil {
				progress(progressOf(resp))
			}
			return resp
		}

		wg.Add(1)
		go func(idx int, target model.Instance) {
			defer wg.Done()
			defer func() { <-sem }()

			result := s.runOne(ctx, target, req, effectiveActionRisk, payload)
			mu.Lock()
			resp.Results[idx] = result
			done++
			batch := done
			mu.Unlock()

			// 每完成一批推一次进度；最后一批由调用方在聚合后补终态帧，避免重复。
			if progress != nil && batch%s.progressEveryOr() == 0 && batch < total {
				mu.Lock()
				snapshot := progressOf(resp)
				mu.Unlock()
				progress(snapshot)
			}
		}(i, inst)
	}
	wg.Wait()

	s.aggregate(resp)
	if progress != nil {
		progress(progressOf(resp))
	}
	return resp
}

// progressEveryOr 返回进度批次粒度（零值兜底，避免除零）。
func (s *BusinessBatchService) progressEveryOr() int {
	if s.progressEvery <= 0 {
		return BusinessBatchProgressBatch
	}
	return s.progressEvery
}

// runOne 对单实例下发业务命令并归类结果。
//
// 分类规则（§6.2）：
//   - 实例非 RUNNING        → offline（未下发）
//   - 节点未连入连接池      → offline（未下发）
//   - 下发超时              → timeout
//   - 探针回执失败          → failed
//   - 回执成功              → success
func (s *BusinessBatchService) runOne(ctx context.Context, inst model.Instance, req BusinessBatchRequest, effectiveActionRisk, payload string) BusinessInstanceResult {
	started := s.now()
	out := BusinessInstanceResult{InstanceID: inst.ID}

	// 离线判定优先于一切：未下发的实例不该算「失败」，否则运营会误以为服务端拒绝。
	if inst.Status != model.InstanceStatusRunning {
		out.Status = BusinessInstanceOffline
		out.Error = fmt.Sprintf("实例未运行（当前状态 %s）", inst.Status)
		out.DurationMs = s.now().Sub(started).Milliseconds()
		return out
	}
	if s.biz == nil {
		out.Status = BusinessInstanceSkipped
		out.Error = "业务通道未初始化"
		out.DurationMs = s.now().Sub(started).Milliseconds()
		return out
	}

	// 单实例超时用独立 context：超时只影响这一台，不牵连同批其他实例。
	cctx, cancel := context.WithTimeout(ctx, s.instanceTimeout)
	defer cancel()

	type outcome struct {
		res *BusinessResult
		err error
	}
	// READ 动作走纯透传 Dispatch（不注入写上下文）；其余按写动作处理。
	// 理由与单实例路径一致：给只读动作注入 operator/taskId 会污染插件侧流水，
	// 且写上下文是「高危写」的审计约定，不该套在只读动作上。
	// 判据取**生效风险**而不是 `req.ActionRisk`：后者是客户端字段，
	// 用它决定「走 Dispatch 还是 DispatchWrite」等于让调用方自己选要不要注入写上下文
	// （operator / operatorId / taskId），进而绕开下游审计与对账。
	readOnly := NormalizeActionRisk(effectiveActionRisk) == BusinessActionRiskRead
	ch := make(chan outcome, 1)
	go func() {
		var (
			res *BusinessResult
			err error
		)
		if readOnly {
			res, err = s.biz.Dispatch(inst.ID, req.Domain, req.Action, payload)
		} else {
			res, err = s.biz.DispatchWrite(inst.ID, req.Domain, req.Action, payload, WriteContext{
				TaskID:     req.RequestID,
				Operator:   req.Operator,
				OperatorID: req.OperatorID,
				Reason:     req.Reason,
			})
		}
		ch <- outcome{res: res, err: err}
	}()

	select {
	case <-cctx.Done():
		out.Status = BusinessInstanceTimeout
		out.Error = fmt.Sprintf("下发后 %s 内未收到回执", s.instanceTimeout)
		out.DurationMs = s.now().Sub(started).Milliseconds()
		return out
	case got := <-ch:
		out.DurationMs = s.now().Sub(started).Milliseconds()
		if got.err != nil {
			// 硬错误（实例不存在 / payload 非法）按 failed 计：确实是本实例执行不了。
			out.Status = BusinessInstanceFailed
			out.Error = got.err.Error()
			return out
		}
		switch {
		case got.res == nil:
			out.Status = BusinessInstanceFailed
			out.Error = "无业务响应"
		case got.res.Available:
			out.Status = BusinessInstanceSuccess
			out.Output = got.res.Output
		case isBusinessOfflineReason(got.res.Error):
			// 节点未连/探针未连：语义上是「没下发」，归 offline 而不是 failed。
			out.Status = BusinessInstanceOffline
			out.Error = got.res.Error
		default:
			out.Status = BusinessInstanceFailed
			out.Error = got.res.Error
		}
		return out
	}
}

// businessOfflineReasons 是 BusinessService 降级文案中代表「未下发」的几种。
//
// 用文案匹配而非错误码是本项目既有现实（BusinessResult 只有 Error 字符串，ADR-026 的
// 「降级即默认」就是这样表达的）。集中在一处便于将来引入结构化原因后统一替换。
var businessOfflineReasons = map[string]struct{}{
	"节点未连接": {},
	"节点不存在": {},
}

// isBusinessOfflineReason 判断降级原因是否属于「目标不可达（未下发）」。
func isBusinessOfflineReason(reason string) bool {
	_, ok := businessOfflineReasons[reason]
	return ok
}

// aggregate 汇总各状态计数（results 已按 target 顺序填好）。
func (s *BusinessBatchService) aggregate(resp *BusinessBatchResponse) {
	resp.Succeeded, resp.Failed, resp.Offline, resp.Timeout, resp.Skipped = 0, 0, 0, 0, 0
	for _, r := range resp.Results {
		switch r.Status {
		case BusinessInstanceSuccess:
			resp.Succeeded++
		case BusinessInstanceFailed:
			resp.Failed++
		case BusinessInstanceOffline:
			resp.Offline++
		case BusinessInstanceTimeout:
			resp.Timeout++
		case BusinessInstanceSkipped:
			resp.Skipped++
		}
	}
}

// progressOf 由当前结果集构造进度帧（调用方须保证 results 在锁内读取或已全部写完）。
func progressOf(resp *BusinessBatchResponse) BusinessBatchProgress {
	p := BusinessBatchProgress{Total: resp.Total}
	for _, r := range resp.Results {
		// 空状态表示该槽位尚未完成（并发填充中），不计入 done。
		if r.Status == "" {
			continue
		}
		p.Done++
		switch r.Status {
		case BusinessInstanceSuccess:
			p.Succeeded++
		case BusinessInstanceFailed:
			p.Failed++
		case BusinessInstanceOffline:
			p.Offline++
		case BusinessInstanceTimeout:
			p.Timeout++
		case BusinessInstanceSkipped:
			p.Skipped++
		}
	}
	return p
}

// payloadString 序列化已注入上下文的不透明载荷。
func (req BusinessBatchRequest) payloadString() string {
	if len(req.Payload) == 0 {
		return ""
	}
	return string(req.Payload)
}

// businessBatchRequestDigest 计算「这组请求参数」的指纹，用于幂等冲突检测。
//
// 覆盖目标集合 + 业务信封 + payload + reason：任一变化都说明「同一个 requestId 被用在了
// 不同的事情上」，必须拒绝而不是静默返回旧结果——那样调用方会以为新请求已生效。
func businessBatchRequestDigest(target BusinessTarget, req BusinessBatchRequest) string {
	canonical := struct {
		GroupIDs           []uint               `json:"groupIds"`
		IncludeDescendants bool                 `json:"includeDescendants"`
		InstanceIDs        []uint               `json:"instanceIds"`
		Filters            *BusinessBatchFilter `json:"filters"`
		Domain             string               `json:"domain"`
		Action             string               `json:"action"`
		Payload            json.RawMessage      `json:"payload"`
		Reason             string               `json:"reason"`
	}{
		GroupIDs:           canonicalInstanceIDs(target.GroupIDs),
		IncludeDescendants: target.IncludeDescendants,
		InstanceIDs:        canonicalInstanceIDs(target.InstanceIDs),
		Filters:            target.Filters,
		Domain:             req.Domain,
		Action:             req.Action,
		Payload:            req.Payload,
		Reason:             req.Reason,
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		// 序列化失败不该让请求通过幂等闸：用随机值使每次都被视为新请求（宁可重复执行也不误回放）。
		return fmt.Sprintf("marshal-error:%d", time.Now().UnixNano())
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// idempotencyKey 构造幂等键：调用者身份 + requestId。
//
// 加上身份前缀是刻意的：不同调用方偶然生成同一个 requestId 时，
// 后者不该拿到前者的结果（那是跨租户信息泄漏）。
func idempotencyKey(issuerKey, requestID string) string {
	return issuerKey + "\x00" + requestID
}

// beginIdempotent 检查幂等窗口。
//
// 返回 (非 nil, nil) 表示命中并回放；(nil, nil) 表示是新请求，已占位；
// (nil, err) 表示同 key 但 payload 不同。
func (s *BusinessBatchService) beginIdempotent(issuerKey, requestID, digest string) (*BusinessBatchResponse, error) {
	s.idemMu.Lock()
	defer s.idemMu.Unlock()
	s.evictExpiredLocked()

	key := idempotencyKey(issuerKey, requestID)
	rec, ok := s.idem[key]
	if !ok {
		// 先占位：并发同 requestId 的第二个请求会看到 digest 相同但 response 为 nil，
		// 此时阻塞等待是不必要的复杂度——直接当作「已在执行」不返回结果会让调用方困惑，
		// 因此占位记录同时登记 digest，只有拿到 response 才回放（见 finishIdempotent）。
		s.idem[key] = &idempotencyRecord{digest: digest, createdAt: s.now()}
		return nil, nil
	}
	if rec.digest != digest {
		return nil, ErrBusinessIdempotencyKeyReused
	}
	if rec.response == nil {
		// 同请求正在执行中：按「首次结果尚未产生」处理，允许本次继续（并发重复提交极罕见，
		// 且两次都会写回结果，最终一致）。这里不返回错误，避免把并发重试变成业务失败。
		return nil, nil
	}
	return rec.response, nil
}

// finishIdempotent 写回首次结果。
func (s *BusinessBatchService) finishIdempotent(issuerKey, requestID, digest string, resp *BusinessBatchResponse) {
	if resp == nil {
		return
	}
	s.idemMu.Lock()
	defer s.idemMu.Unlock()
	key := idempotencyKey(issuerKey, requestID)
	rec, ok := s.idem[key]
	if !ok || rec.digest != digest {
		s.idem[key] = &idempotencyRecord{digest: digest, response: resp, createdAt: s.now()}
		return
	}
	rec.response = resp
}

// evictExpiredLocked 清理超出幂等窗口的记录（调用方须持锁）。
func (s *BusinessBatchService) evictExpiredLocked() {
	cutoff := s.now().Add(-BusinessBatchIdempotencyWindow)
	for key, rec := range s.idem {
		if rec.createdAt.Before(cutoff) {
			delete(s.idem, key)
		}
	}
}

// IdempotencyWindowSize 返回当前内存中的幂等记录数（诊断/测试）。
func (s *BusinessBatchService) IdempotencyWindowSize() int {
	if s == nil {
		return 0
	}
	s.idemMu.Lock()
	defer s.idemMu.Unlock()
	return len(s.idem)
}

// BusinessBatchErrorCode 把服务层错误映射为契约错误码与 HTTP 状态码（§8）。
//
// 集中一处映射，避免路由与流式分支各自维护一套（那是最容易漂移的地方）。
func BusinessBatchErrorCode(err error) (code string, status int) {
	switch {
	case err == nil:
		return "", 200
	case errors.Is(err, ErrBusinessTargetEmpty):
		return "TARGET_EMPTY", 400
	case errors.Is(err, ErrBusinessTargetUnspecified):
		return "TARGET_EMPTY", 400
	case errors.Is(err, ErrBusinessTargetChanged):
		return "TARGET_CHANGED", 409
	case errors.Is(err, ErrBusinessTokenExpired):
		return "TOKEN_EXPIRED", 409
	case errors.Is(err, ErrBusinessTokenInvalid):
		return "TOKEN_EXPIRED", 409
	case errors.Is(err, ErrBusinessTokenMismatch):
		return "TOKEN_MISMATCH", 403
	case errors.Is(err, ErrBusinessConfirmationRequired):
		return "CONFIRMATION_REQUIRED", 409
	case errors.Is(err, ErrBusinessReasonRequired):
		return "REASON_REQUIRED", 400
	case errors.Is(err, ErrBusinessIdempotencyKeyReused):
		return "IDEMPOTENCY_KEY_REUSED", 409
	case errors.Is(err, ErrInvalidBusinessCommand):
		return "INVALID_REQUEST", 400
	}
	return "INTERNAL_ERROR", 500
}

// logBusinessBatchSummary 记录批量执行摘要（通道维度，不含业务 payload/output）。
func logBusinessBatchSummary(resp *BusinessBatchResponse, risk string) {
	if resp == nil {
		return
	}
	slog.Info("业务批量执行完成",
		"requestId", resp.RequestID,
		"risk", risk,
		"total", resp.Total,
		"succeeded", resp.Succeeded,
		"failed", resp.Failed,
		"offline", resp.Offline,
		"timeout", resp.Timeout,
		"skipped", resp.Skipped,
	)
}
