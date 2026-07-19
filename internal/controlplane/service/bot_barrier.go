package service

import (
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

const barrierReleaseLead = 250 * time.Millisecond

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
	Release  *BarrierRelease
}

type barrierState struct {
	definition BarrierDefinition
	arrivals   map[string]BarrierParticipant
	pending    map[string]BarrierParticipant
	releasedAt int64
	timedOut   bool
}

// BarrierCoordinator 在 CP 进程内维护显式运行生命周期的屏障状态。
type BarrierCoordinator struct {
	mu     sync.Mutex
	clock  BotLoadClock
	states map[BarrierScope]*barrierState
}

// NewBarrierCoordinator 创建可注入时钟的内存屏障协调器。
func NewBarrierCoordinator(clock BotLoadClock) *BarrierCoordinator {
	return &BarrierCoordinator{clock: normalizeBotLoadClock(clock), states: make(map[BarrierScope]*barrierState)}
}

// Ensure 仅在首次进入时冻结 expectedBotSet；后续同作用域调用不改变分母。
func (c *BarrierCoordinator) Ensure(definition BarrierDefinition) error {
	if err := validateBarrierDefinition(definition); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.states[definition.Scope]; exists {
		return nil
	}
	definition.ExpectedBots = cloneExpectedBots(definition.ExpectedBots)
	if definition.TimeoutPolicy == "" {
		definition.TimeoutPolicy = "fail"
	}
	c.states[definition.Scope] = &barrierState{
		definition: definition, arrivals: make(map[string]BarrierParticipant), pending: make(map[string]BarrierParticipant),
	}
	return nil
}

// Arrive 按 Bot+generation 幂等计数；释放后重连仍返回同一 releaseAt。
func (c *BarrierCoordinator) Arrive(arrival BarrierArrival) BarrierResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.states[arrival.Scope]
	if state == nil {
		return BarrierResult{Decision: BarrierUnknown}
	}
	if state.timedOut {
		return BarrierResult{Decision: BarrierTimedOut}
	}
	if state.releasedAt > 0 {
		return c.arriveAfterRelease(state, arrival)
	}
	expectedGeneration, expected := state.definition.ExpectedBots[arrival.BotUUID]
	if !expected {
		return BarrierResult{Decision: BarrierUnexpectedBot}
	}
	if arrival.Generation != expectedGeneration {
		return BarrierResult{Decision: BarrierStaleGeneration}
	}
	if existing, ok := state.arrivals[arrival.BotUUID]; ok && existing.Generation == arrival.Generation {
		return BarrierResult{Decision: BarrierDuplicate}
	}
	state.arrivals[arrival.BotUUID] = participantFromArrival(arrival)
	if len(state.arrivals) < barrierThreshold(state.definition) {
		return BarrierResult{Decision: BarrierWaiting}
	}
	c.release(state, state.arrivals)
	return BarrierResult{Decision: BarrierReleased, Release: releaseSnapshot(state)}
}

func (c *BarrierCoordinator) arriveAfterRelease(state *barrierState, arrival BarrierArrival) BarrierResult {
	expectedGeneration, expected := state.definition.ExpectedBots[arrival.BotUUID]
	if !expected {
		return BarrierResult{Decision: BarrierUnexpectedBot}
	}
	if arrival.Generation != expectedGeneration {
		return BarrierResult{Decision: BarrierStaleGeneration}
	}
	participant := participantFromArrival(arrival)
	state.pending[arrival.BotUUID] = participant
	return BarrierResult{Decision: BarrierAlreadyReleased, Release: releaseSnapshot(state)}
}

// CheckTimeout 使用注入时钟执行 fail 或 release-arrived 策略。
func (c *BarrierCoordinator) CheckTimeout(scope BarrierScope) BarrierResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.states[scope]
	if state == nil {
		return BarrierResult{Decision: BarrierUnknown}
	}
	if state.releasedAt > 0 {
		return BarrierResult{Decision: BarrierAlreadyReleased, Release: releaseSnapshot(state)}
	}
	if state.timedOut || c.clock.Now().Before(state.definition.Deadline) {
		if state.timedOut {
			return BarrierResult{Decision: BarrierTimedOut}
		}
		return BarrierResult{Decision: BarrierWaiting}
	}
	if state.definition.TimeoutPolicy == "release-arrived" {
		c.release(state, state.arrivals)
		return BarrierResult{Decision: BarrierReleasedOnTimeout, Release: releaseSnapshot(state)}
	}
	state.timedOut = true
	return BarrierResult{Decision: BarrierTimedOut}
}

// PendingRelease 返回尚未由 Worker 确认接收的释放信号。
func (c *BarrierCoordinator) PendingRelease(scope BarrierScope) *BarrierRelease {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.states[scope]
	if state == nil || state.releasedAt == 0 {
		return nil
	}
	return releaseSnapshot(state)
}

// MarkDelivered 只移除已确认 accepted/skipped 的 Bot；失败项继续保留供重试。
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
}

// StopRun 清理指定运行的全部屏障状态，不介入 FR-355 运行状态机。
func (c *BarrierCoordinator) StopRun(runID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for scope := range c.states {
		if scope.RunID == runID {
			delete(c.states, scope)
		}
	}
}

// Exists 仅用于生命周期协调和测试诊断。
func (c *BarrierCoordinator) Exists(scope BarrierScope) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, exists := c.states[scope]
	return exists
}

func (c *BarrierCoordinator) release(state *barrierState, participants map[string]BarrierParticipant) {
	if state.releasedAt == 0 {
		state.releasedAt = c.clock.Now().Add(barrierReleaseLead).UnixMilli()
	}
	for botUUID, participant := range participants {
		state.pending[botUUID] = participant
	}
}

func releaseSnapshot(state *barrierState) *BarrierRelease {
	pending := make([]BarrierParticipant, 0, len(state.pending))
	for _, participant := range state.pending {
		pending = append(pending, participant)
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].BotUUID < pending[j].BotUUID })
	return &BarrierRelease{Round: state.definition.Scope.Round, ReleaseAtUnixMS: state.releasedAt, Pending: pending}
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

func validateBarrierDefinition(definition BarrierDefinition) error {
	scope := definition.Scope
	if strings.TrimSpace(scope.RunID) == "" || strings.TrimSpace(scope.CohortKey) == "" || strings.TrimSpace(scope.BarrierKey) == "" || scope.StageIndex < 0 || scope.Round <= 0 {
		return scenarioValidationError("barrier.scope", "屏障作用域不完整")
	}
	if len(definition.ExpectedBots) == 0 {
		return scenarioValidationError("barrier.expectedBotSet", "期望 Bot 集合不能为空")
	}
	for botUUID, generation := range definition.ExpectedBots {
		if strings.TrimSpace(botUUID) == "" || generation <= 0 {
			return scenarioValidationError("barrier.expectedBotSet", "Bot 或 generation 非法")
		}
	}
	if definition.Deadline.IsZero() {
		return scenarioValidationError("barrier.deadline", "截止时间不能为空")
	}
	if definition.TimeoutPolicy != "" && definition.TimeoutPolicy != "fail" && definition.TimeoutPolicy != "release-arrived" {
		return scenarioValidationError("barrier.timeoutPolicy", "必须为 fail 或 release-arrived")
	}
	return validateBarrierRelease(definition.Release, len(definition.ExpectedBots))
}

func validateBarrierRelease(release ScenarioBarrierRelease, expected int) error {
	switch release.Type {
	case "all":
		return nil
	case "count":
		if release.Value < 1 || release.Value > expected {
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

func cloneExpectedBots(source map[string]int64) map[string]int64 {
	cloned := make(map[string]int64, len(source))
	for botUUID, generation := range source {
		cloned[botUUID] = generation
	}
	return cloned
}

func participantFromArrival(arrival BarrierArrival) BarrierParticipant {
	return BarrierParticipant{BotUUID: arrival.BotUUID, Generation: arrival.Generation, ActionRunID: arrival.ActionRunID, CorrelationToken: arrival.CorrelationToken}
}
