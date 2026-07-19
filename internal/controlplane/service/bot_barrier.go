package service

import (
	"context"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	barrierReleaseLead = time.Second
	barrierRetryMin    = 100 * time.Millisecond
	barrierRetryMax    = 2 * time.Second
)

// BarrierScope 是屏障唯一作用域。
type BarrierScope struct {
	RunID      string
	StageIndex int
	CohortKey  string
	BarrierKey string
	Round      int64
}

// BarrierDefinition 在首次进入时冻结期望 Bot 集合与阈值。
type BarrierDefinition struct {
	Scope         BarrierScope
	ExpectedBots  map[string]int64
	Release       ScenarioBarrierRelease
	TimeoutPolicy string
	Deadline      time.Time
	TimeoutBudget time.Duration
}

// BarrierArrival 是单个 Bot 当前 generation 的屏障到达事件。
type BarrierArrival struct {
	Scope            BarrierScope
	BotUUID          string
	Generation       int64
	ActionRunID      string
	CorrelationToken string
}

// BarrierParticipant 保存释放信号所需的完整动作关联。
type BarrierParticipant struct {
	BotUUID          string
	Generation       int64
	ActionRunID      string
	CorrelationToken string
}

// BarrierRelease 是可重复投递且时间一致的屏障释放快照。
type BarrierRelease struct {
	Round           int64
	SignalType      string
	ReleaseAtUnixMS int64
	Pending         []BarrierParticipant
}

// BarrierDecision 表示到达或超时处理结果。
type BarrierDecision string

const (
	BarrierWaiting           BarrierDecision = "waiting"
	BarrierDuplicate         BarrierDecision = "duplicate"
	BarrierStaleGeneration   BarrierDecision = "stale_generation"
	BarrierUnexpectedBot     BarrierDecision = "unexpected_bot"
	BarrierUnknown           BarrierDecision = "unknown"
	BarrierReleased          BarrierDecision = "released"
	BarrierAlreadyReleased   BarrierDecision = "already_released"
	BarrierTimedOut          BarrierDecision = "timed_out"
	BarrierReleasedOnTimeout BarrierDecision = "released_on_timeout"
)

// BarrierResult 返回协调决策和可重试释放快照。
type BarrierResult struct {
	Decision BarrierDecision
	Expected int
	Arrived  int
	Release  *BarrierRelease
}

type barrierState struct {
	definition      BarrierDefinition
	arrivals        map[string]BarrierParticipant
	pending         map[string]BarrierParticipant
	releasedAt      int64
	failureAt       int64
	signalLead      time.Duration
	timeoutPrepared bool
	timedOut        bool
	dispatching     bool
	nextAttempt     time.Time
	backoff         time.Duration
}

type barrierLoadCall struct {
	done       chan struct{}
	err        error
	runVersion uint64
}

// BarrierCoordinator 在 CP 进程内维护显式运行生命周期的屏障状态。
type BarrierCoordinator struct {
	mu         sync.Mutex
	clock      BotLoadClock
	states     map[BarrierScope]*barrierState
	loading    map[BarrierScope]*barrierLoadCall
	runVersion map[string]uint64
}

// NewBarrierCoordinator 创建可注入时钟的内存屏障协调器。
func NewBarrierCoordinator(clock BotLoadClock) *BarrierCoordinator {
	return &BarrierCoordinator{
		clock: normalizeBotLoadClock(clock), states: make(map[BarrierScope]*barrierState),
		loading: make(map[BarrierScope]*barrierLoadCall), runVersion: make(map[string]uint64),
	}
}

// Ensure 兼容直接传入 expected set 的调用。
func (c *BarrierCoordinator) Ensure(definition BarrierDefinition) error {
	return c.EnsureLazy(context.Background(), definition, func(context.Context) (map[string]int64, error) {
		return definition.ExpectedBots, nil
	})
}

// EnsureLazy 锁外查询 expected set，并通过每作用域门闩保证仅首次查询。
func (c *BarrierCoordinator) EnsureLazy(ctx context.Context, definition BarrierDefinition, loader func(context.Context) (map[string]int64, error)) error {
	if definition.TimeoutPolicy == "" {
		definition.TimeoutPolicy = "fail"
	}
	if err := validateBarrierDefinition(definition); err != nil {
		return err
	}
	call, leader := c.beginBarrierLoad(definition.Scope)
	if !leader {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-call.done:
			return call.err
		}
	}
	expected, err := loadExpectedBots(ctx, definition.ExpectedBots, loader)
	if err == nil {
		err = validateExpectedBots(definition.Release, expected)
	}
	c.finishBarrierLoad(definition, expected, call, err)
	return call.err
}

func (c *BarrierCoordinator) beginBarrierLoad(scope BarrierScope) (*barrierLoadCall, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.states[scope]; exists {
		call := &barrierLoadCall{done: make(chan struct{})}
		close(call.done)
		return call, false
	}
	if call := c.loading[scope]; call != nil {
		return call, false
	}
	call := &barrierLoadCall{done: make(chan struct{}), runVersion: c.runVersion[scope.RunID]}
	c.loading[scope] = call
	return call, true
}

func (c *BarrierCoordinator) finishBarrierLoad(definition BarrierDefinition, expected map[string]int64, call *barrierLoadCall, loadErr error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if loadErr == nil && call.runVersion != c.runVersion[definition.Scope.RunID] {
		loadErr = context.Canceled
	}
	if loadErr == nil {
		definition.ExpectedBots = cloneExpectedBots(expected)
		if _, exists := c.states[definition.Scope]; !exists {
			c.states[definition.Scope] = &barrierState{
				definition: definition, arrivals: make(map[string]BarrierParticipant), pending: make(map[string]BarrierParticipant),
				signalLead: barrierSignalLead(definition, c.clock.Now().UTC()),
			}
		}
	}
	call.err = loadErr
	delete(c.loading, definition.Scope)
	close(call.done)
}

func loadExpectedBots(ctx context.Context, fallback map[string]int64, loader func(context.Context) (map[string]int64, error)) (map[string]int64, error) {
	if loader == nil {
		return fallback, nil
	}
	return loader(ctx)
}

func barrierSignalLead(definition BarrierDefinition, now time.Time) time.Duration {
	budget := definition.TimeoutBudget
	if budget <= 0 {
		budget = definition.Deadline.Sub(now)
	}
	if budget <= 0 {
		return 0
	}
	return min(barrierReleaseLead, budget/4)
}

func validateBarrierDefinition(definition BarrierDefinition) error {
	scope := definition.Scope
	if strings.TrimSpace(scope.RunID) == "" || strings.TrimSpace(scope.CohortKey) == "" || strings.TrimSpace(scope.BarrierKey) == "" || scope.StageIndex < 0 || scope.Round <= 0 {
		return scenarioValidationError("barrier.scope", "屏障作用域不完整")
	}
	if definition.Deadline.IsZero() {
		return scenarioValidationError("barrier.deadline", "截止时间不能为空")
	}
	if definition.TimeoutPolicy != "fail" && definition.TimeoutPolicy != "release-arrived" {
		return scenarioValidationError("barrier.timeoutPolicy", "必须为 fail 或 release-arrived")
	}
	return validateBarrierRelease(definition.Release, 0)
}

func validateExpectedBots(release ScenarioBarrierRelease, expected map[string]int64) error {
	if len(expected) == 0 {
		return scenarioValidationError("barrier.expectedBotSet", "期望 Bot 集合不能为空")
	}
	for botUUID, generation := range expected {
		if strings.TrimSpace(botUUID) == "" || generation <= 0 {
			return scenarioValidationError("barrier.expectedBotSet", "Bot 或 generation 非法")
		}
	}
	return validateBarrierRelease(release, len(expected))
}

func validateBarrierRelease(release ScenarioBarrierRelease, expected int) error {
	switch release.Type {
	case "all":
		if release.Value != 0 {
			return scenarioValidationError("barrier.release.value", "all 不接受 value")
		}
	case "count":
		if release.Value < 1 || (expected > 0 && release.Value > expected) {
			return scenarioValidationError("barrier.release.value", "必须在期望 Bot 数量范围内")
		}
	case "percent":
		if release.Value < 1 || release.Value > 100 {
			return scenarioValidationError("barrier.release.value", "必须在 1..100 之间")
		}
	default:
		return scenarioValidationError("barrier.release.type", "必须为 all、count 或 percent")
	}
	return nil
}

// Arrive 按 Bot+generation 幂等计数；释放或失败后重连仍复用同一绝对执行时间。
func (c *BarrierCoordinator) Arrive(arrival BarrierArrival) BarrierResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.states[arrival.Scope]
	if state == nil {
		return BarrierResult{Decision: BarrierUnknown}
	}
	if decision := validateBarrierArrival(state, arrival); decision != "" {
		return c.result(state, decision)
	}
	return c.arriveLocked(state, arrival, c.clock.Now().UTC())
}

func validateBarrierArrival(state *barrierState, arrival BarrierArrival) BarrierDecision {
	expectedGeneration, expected := state.definition.ExpectedBots[arrival.BotUUID]
	if !expected {
		return BarrierUnexpectedBot
	}
	if arrival.Generation != expectedGeneration {
		return BarrierStaleGeneration
	}
	return ""
}

func (c *BarrierCoordinator) arriveLocked(state *barrierState, arrival BarrierArrival, now time.Time) BarrierResult {
	c.prepareTimeoutSignalLocked(state, now)
	c.finalizeTimeoutLocked(state, now)
	participant := participantFromArrival(arrival)
	if state.timedOut {
		c.rememberPostDecisionArrival(state, participant, now)
		return c.result(state, BarrierTimedOut)
	}
	if state.releasedAt > 0 && !state.timeoutPrepared {
		c.rememberPostDecisionArrival(state, participant, now)
		return c.result(state, BarrierAlreadyReleased)
	}
	if _, duplicate := state.arrivals[arrival.BotUUID]; duplicate {
		state.arrivals[arrival.BotUUID] = participant
		if state.signalType() != "" {
			state.pending[arrival.BotUUID] = participant
			state.nextAttempt = now
		}
		return c.result(state, BarrierDuplicate)
	}
	state.arrivals[arrival.BotUUID] = participant
	if state.failureAt > 0 || state.timeoutPrepared {
		state.pending[arrival.BotUUID] = participant
		state.nextAttempt = now
	}
	if len(state.arrivals) < barrierThreshold(state.definition) {
		return c.result(state, BarrierWaiting)
	}
	c.release(state, now)
	return c.result(state, BarrierReleased)
}

func (c *BarrierCoordinator) rememberPostDecisionArrival(state *barrierState, participant BarrierParticipant, now time.Time) {
	state.arrivals[participant.BotUUID] = participant
	state.pending[participant.BotUUID] = participant
	state.nextAttempt = now
	if state.backoff <= 0 {
		state.backoff = barrierRetryMin
	}
}

func barrierThreshold(definition BarrierDefinition) int {
	switch definition.Release.Type {
	case "count":
		return definition.Release.Value
	case "percent":
		return int(math.Ceil(float64(len(definition.ExpectedBots)*definition.Release.Value) / 100))
	default:
		return len(definition.ExpectedBots)
	}
}

func (c *BarrierCoordinator) release(state *barrierState, now time.Time) {
	if state.releasedAt == 0 || state.timeoutPrepared {
		remaining := state.definition.Deadline.Sub(now)
		if remaining < state.signalLead {
			state.releasedAt = now.UnixMilli()
		} else {
			state.releasedAt = now.Add(min(state.signalLead, remaining)).UnixMilli()
		}
	}
	state.failureAt = 0
	state.timeoutPrepared = false
	state.timedOut = false
	c.resetPending(state)
	state.nextAttempt = now
	state.backoff = barrierRetryMin
}

func (c *BarrierCoordinator) resetPending(state *barrierState) {
	clear(state.pending)
	for botUUID, participant := range state.arrivals {
		state.pending[botUUID] = participant
	}
}

// CheckTimeout 使用注入时钟执行 fail 或 release-arrived 策略。
func (c *BarrierCoordinator) CheckTimeout(scope BarrierScope) BarrierResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.states[scope]
	if state == nil {
		return BarrierResult{Decision: BarrierUnknown}
	}
	decision := c.checkTimeoutLocked(state, c.clock.Now().UTC())
	if decision != "" {
		return c.result(state, decision)
	}
	if state.releasedAt > 0 {
		return c.result(state, BarrierAlreadyReleased)
	}
	return c.result(state, BarrierWaiting)
}

func (c *BarrierCoordinator) checkTimeoutLocked(state *barrierState, now time.Time) BarrierDecision {
	if (state.releasedAt > 0 && !state.timeoutPrepared) || state.timedOut || now.Before(state.definition.Deadline) {
		return ""
	}
	c.finalizeTimeoutLocked(state, now)
	if state.definition.TimeoutPolicy == "release-arrived" {
		return BarrierReleasedOnTimeout
	}
	return BarrierTimedOut
}

func (c *BarrierCoordinator) prepareTimeoutSignalLocked(state *barrierState, now time.Time) {
	if state.releasedAt > 0 || state.failureAt > 0 || state.timedOut {
		return
	}
	dispatchAt := state.definition.Deadline.Add(-state.signalLead)
	if now.Before(dispatchAt) {
		return
	}
	if state.definition.TimeoutPolicy == "release-arrived" {
		state.releasedAt = state.definition.Deadline.UnixMilli()
		state.timeoutPrepared = true
	} else {
		state.failureAt = state.definition.Deadline.UnixMilli()
	}
	c.resetPending(state)
	state.nextAttempt = now
	state.backoff = barrierRetryMin
}

func (c *BarrierCoordinator) finalizeTimeoutLocked(state *barrierState, now time.Time) {
	if (state.releasedAt > 0 && !state.timeoutPrepared) || state.timedOut || now.Before(state.definition.Deadline) {
		return
	}
	if state.timeoutPrepared {
		state.timeoutPrepared = false
		return
	}
	if state.definition.TimeoutPolicy == "release-arrived" {
		state.releasedAt = state.definition.Deadline.UnixMilli()
	} else {
		state.failureAt = state.definition.Deadline.UnixMilli()
		state.timedOut = true
	}
	c.resetPending(state)
	state.nextAttempt = now
	if state.backoff <= 0 {
		state.backoff = barrierRetryMin
	}
}

// TakeReady 集中扫描 deadline 与待投递项，并声明本轮锁外 RPC 所有权。
func (c *BarrierCoordinator) TakeReady(now time.Time) map[BarrierScope]*BarrierRelease {
	c.mu.Lock()
	defer c.mu.Unlock()
	ready := make(map[BarrierScope]*BarrierRelease)
	for scope, state := range c.states {
		c.prepareTimeoutSignalLocked(state, now)
		c.finalizeTimeoutLocked(state, now)
		if state.signalType() == "" || state.dispatching || len(state.pending) == 0 || now.Before(state.nextAttempt) {
			continue
		}
		state.dispatching = true
		ready[scope] = releaseSnapshot(scope, state)
	}
	return ready
}

func (state *barrierState) signalType() string {
	if state.releasedAt > 0 {
		return "barrier-release"
	}
	if state.failureAt > 0 {
		return "barrier-fail"
	}
	return ""
}

func (state *barrierState) signalAt() int64 {
	if state.releasedAt > 0 {
		return state.releasedAt
	}
	return state.failureAt
}

// CompleteRelease 合并逐项回执，失败项保留并按有界退避重试。
func (c *BarrierCoordinator) CompleteRelease(scope BarrierScope, signalType string, executeAt int64, report ActionSignalReport, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.states[scope]
	if state == nil {
		return
	}
	state.dispatching = false
	if signalType != state.signalType() || executeAt != state.signalAt() {
		state.nextAttempt = now
		return
	}
	for _, item := range report.Items {
		if item.Accepted || (item.Skipped && item.ErrorCode == "") {
			delete(state.pending, item.Input.BotUUID)
		}
	}
	if len(state.pending) == 0 {
		return
	}
	state.nextAttempt = now.Add(state.backoff)
	state.backoff = min(state.backoff*2, barrierRetryMax)
}

// Accepts 判断 Bot 与 generation 是否属于已冻结期望集合。
func (c *BarrierCoordinator) Accepts(scope BarrierScope, botUUID string, generation int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.states[scope]
	if state == nil {
		return false
	}
	expectedGeneration, exists := state.definition.ExpectedBots[botUUID]
	return exists && expectedGeneration == generation
}

// PendingRelease 返回尚未由 Worker 确认接收的释放信号。
func (c *BarrierCoordinator) PendingRelease(scope BarrierScope) *BarrierRelease {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.states[scope]
	if state == nil || state.signalType() == "" {
		return nil
	}
	return releaseSnapshot(scope, state)
}

// MarkDelivered 兼容直接标记已确认 Bot 的调用。
func (c *BarrierCoordinator) MarkDelivered(scope BarrierScope, botUUIDs []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.states[scope]
	if state == nil {
		return
	}
	for _, botUUID := range botUUIDs {
		delete(state.pending, botUUID)
	}
	state.dispatching = false
}

func releaseSnapshot(scope BarrierScope, state *barrierState) *BarrierRelease {
	botUUIDs := make([]string, 0, len(state.pending))
	for botUUID := range state.pending {
		botUUIDs = append(botUUIDs, botUUID)
	}
	sort.Strings(botUUIDs)
	pending := make([]BarrierParticipant, 0, len(botUUIDs))
	for _, botUUID := range botUUIDs {
		pending = append(pending, state.pending[botUUID])
	}
	return &BarrierRelease{Round: scope.Round, SignalType: state.signalType(), ReleaseAtUnixMS: state.signalAt(), Pending: pending}
}

func (c *BarrierCoordinator) result(state *barrierState, decision BarrierDecision) BarrierResult {
	result := BarrierResult{Decision: decision, Expected: len(state.definition.ExpectedBots), Arrived: len(state.arrivals)}
	if state.releasedAt > 0 {
		result.Release = releaseSnapshot(state.definition.Scope, state)
	}
	return result
}

// StopRun 清理指定运行的全部屏障状态并使在途懒加载失效。
func (c *BarrierCoordinator) StopRun(runID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runVersion[runID]++
	for scope := range c.states {
		if scope.RunID == runID {
			delete(c.states, scope)
		}
	}
}

// Has 原子判断作用域是否已冻结。
func (c *BarrierCoordinator) Has(scope BarrierScope) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, exists := c.states[scope]
	return exists
}

// Exists 保留既有测试和诊断调用。
func (c *BarrierCoordinator) Exists(scope BarrierScope) bool { return c.Has(scope) }

// ScheduledCount 返回仍需 deadline 或投递处理的作用域数量。
func (c *BarrierCoordinator) ScheduledCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, state := range c.states {
		needsDeadline := state.releasedAt == 0 && !state.timedOut
		if needsDeadline || len(state.pending) > 0 || state.dispatching {
			count++
		}
	}
	return count
}

func cloneExpectedBots(source map[string]int64) map[string]int64 {
	cloned := make(map[string]int64, len(source))
	for botUUID, generation := range source {
		cloned[botUUID] = generation
	}
	return cloned
}

func participantFromArrival(arrival BarrierArrival) BarrierParticipant {
	return BarrierParticipant{
		BotUUID: arrival.BotUUID, Generation: arrival.Generation,
		ActionRunID: arrival.ActionRunID, CorrelationToken: arrival.CorrelationToken,
	}
}
