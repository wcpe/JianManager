package grpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wcpe/JianManager/internal/worker/bot"
	"github.com/wcpe/JianManager/proto/workerpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	maxBotBatchSize            = 50
	maxBotActionSignals        = 100
	botBatchCacheLimit         = 1000
	botBatchCacheTTL           = time.Hour
	botFleetReliableQueueLimit = 1024
	// 覆盖单节点 50 Bot × 100 step × 10 attempt 的合法 identity，并保留 20% 安全余量。
	botActionJournalIdentityLimit = maxBotBatchSize * 100 * 10
	botActionJournalLimit         = botActionJournalIdentityLimit * 6 / 5
	botBatchStatusAccepted        = "accepted"
	botBatchStatusConflict        = "conflict"
	botBatchStatusCapacity        = "capacity_insufficient"
	botBatchStatusUnavailable     = "ephemeral_unavailable"
)

type botFleetEventSubscriber struct {
	mu         sync.Mutex
	generation int64
	out        chan *bot.BotWorkerEvent
	wake       chan struct{}
	done       chan struct{}
	stopped    chan struct{}
	stopOnce   sync.Once
	replay     []*bot.BotWorkerEvent
	queue      []*bot.BotWorkerEvent
	limit      int
	err        error
}

func newBotFleetEventSubscriberWithReplay(out chan *bot.BotWorkerEvent, generation int64, replay []*bot.BotWorkerEvent) *botFleetEventSubscriber {
	subscriber := &botFleetEventSubscriber{
		generation: generation, out: out, wake: make(chan struct{}, 1), replay: replay,
		done: make(chan struct{}), stopped: make(chan struct{}), limit: botFleetReliableQueueLimit,
	}
	go subscriber.run()
	return subscriber
}

func (s *botFleetEventSubscriber) run() {
	defer close(s.stopped)
	defer close(s.out)
	for {
		event := s.next()
		if event == nil {
			select {
			case <-s.wake:
				continue
			case <-s.done:
				return
			}
		}
		select {
		case s.out <- event:
		case <-s.done:
			return
		}
	}
}

func (s *botFleetEventSubscriber) next() *bot.BotWorkerEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.replay) > 0 {
		event := s.replay[0]
		s.replay[0] = nil
		s.replay = s.replay[1:]
		return event
	}
	if len(s.queue) == 0 {
		return nil
	}
	event := s.queue[0]
	s.queue[0] = nil
	s.queue = s.queue[1:]
	return event
}

func (s *botFleetEventSubscriber) enqueue(event *bot.BotWorkerEvent) bool {
	select {
	case <-s.done:
		return false
	default:
	}
	s.mu.Lock()
	accepted := s.enqueueLocked(event)
	s.mu.Unlock()
	if accepted {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	return accepted
}

func (s *botFleetEventSubscriber) enqueueLocked(event *bot.BotWorkerEvent) bool {
	reliable := event != nil && event.Evt == "action-event"
	if !reliable && s.mergeRuntimeLocked(event) {
		return true
	}
	if len(s.queue) < s.limit {
		s.queue = append(s.queue, event)
		return true
	}
	if !reliable {
		return true
	}
	for i, queued := range s.queue {
		if queued == nil || queued.Evt != "action-event" {
			copy(s.queue[i:], s.queue[i+1:])
			s.queue[len(s.queue)-1] = event
			return true
		}
	}
	return false
}

func (s *botFleetEventSubscriber) mergeRuntimeLocked(event *bot.BotWorkerEvent) bool {
	if event == nil || (event.Evt != "bot-state" && event.Evt != "heartbeat") {
		return false
	}
	for i := len(s.queue) - 1; i >= 0; i-- {
		if s.queue[i] != nil && s.queue[i].Evt == event.Evt {
			s.queue[i] = event
			return true
		}
	}
	return false
}

func (s *botFleetEventSubscriber) stop(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
	s.stopOnce.Do(func() { close(s.done) })
	<-s.stopped
}

func (s *botFleetEventSubscriber) terminalError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

type botActionJournalPhase uint8

const (
	botActionJournalRunning botActionJournalPhase = iota
	botActionJournalTerminal
	botActionJournalWaiting
)

type botActionJournalEntry struct {
	sequence uint64
	phase    botActionJournalPhase
	event    *bot.BotWorkerEvent
}

func (s *Server) appendBotActionJournalLocked(event *bot.BotWorkerEvent, currentGeneration int64) {
	if event == nil || event.Evt != "action-event" || event.Action == nil {
		return
	}
	eventGeneration := event.WorkerEpochGeneration
	if eventGeneration == 0 {
		eventGeneration = currentGeneration
	}
	if currentGeneration > 0 && eventGeneration > 0 && eventGeneration != currentGeneration {
		return
	}
	s.alignBotActionJournalGenerationLocked(eventGeneration)
	identity := botActionJournalIdentity(event.Action)
	phase := classifyBotActionJournalPhase(event.Action)
	if !s.prepareBotActionJournalIdentityLocked(identity, phase) {
		return
	}
	if s.botActionJournal == nil {
		s.botActionJournal = make(map[string]botActionJournalEntry)
	}
	if _, exists := s.botActionJournal[identity]; !exists && len(s.botActionJournal) >= botActionJournalLimit {
		s.evictBotActionJournalEntryLocked()
	}
	s.botActionJournalSequence++
	s.botActionJournal[identity] = botActionJournalEntry{
		sequence: s.botActionJournalSequence, phase: phase, event: cloneBotActionWorkerEvent(event),
	}
}

func (s *Server) prepareBotActionJournalIdentityLocked(identity string, phase botActionJournalPhase) bool {
	if s.botActionJournal == nil {
		return true
	}
	current, exists := s.botActionJournal[identity]
	if !exists {
		return true
	}
	switch phase {
	case botActionJournalRunning:
		return current.phase == botActionJournalRunning
	case botActionJournalWaiting:
		return current.phase != botActionJournalTerminal
	case botActionJournalTerminal:
		return current.phase != botActionJournalTerminal
	default:
		return false
	}
}

func (s *Server) alignBotActionJournalGenerationLocked(generation int64) {
	if generation == 0 {
		return
	}
	if s.botActionJournalGeneration != 0 && s.botActionJournalGeneration != generation {
		s.clearBotActionJournalLocked()
	}
	s.botActionJournalGeneration = generation
}

func (s *Server) clearBotActionJournalLocked() {
	s.botActionJournal = nil
	s.botActionJournalSequence = 0
	s.botActionJournalGeneration = 0
}

func (s *Server) evictBotActionJournalEntryLocked() {
	victim := ""
	victimPriority := int(^uint(0) >> 1)
	var victimSequence uint64
	for key, entry := range s.botActionJournal {
		priority := botActionJournalEvictionPriority(entry.phase)
		if victim == "" || priority < victimPriority || (priority == victimPriority && entry.sequence < victimSequence) {
			victim, victimPriority, victimSequence = key, priority, entry.sequence
		}
	}
	delete(s.botActionJournal, victim)
}

func botActionJournalEvictionPriority(phase botActionJournalPhase) int {
	switch phase {
	case botActionJournalRunning:
		return 0
	case botActionJournalTerminal:
		return 1
	default:
		return 2
	}
}

func (s *Server) botActionJournalReplayLocked(sessionID string) []*bot.BotWorkerEvent {
	entries := make([]botActionJournalEntry, 0, len(s.botActionJournal))
	for _, entry := range s.botActionJournal {
		if sessionID != "" && entry.event.Action.SessionID != sessionID {
			continue
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].sequence < entries[j].sequence })
	replay := make([]*bot.BotWorkerEvent, 0, len(entries))
	for _, entry := range entries {
		replay = append(replay, cloneBotActionWorkerEvent(entry.event))
	}
	return replay
}

func (s *Server) botActionJournalSnapshot(sessionID string) []*bot.BotWorkerEvent {
	s.botEventMu.Lock()
	defer s.botEventMu.Unlock()
	return s.botActionJournalReplayLocked(sessionID)
}

func (s *Server) botActionJournalSize() int {
	s.botEventMu.Lock()
	defer s.botEventMu.Unlock()
	return len(s.botActionJournal)
}

func botActionJournalIdentity(action *bot.ActionEvent) string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%s", action.SessionID, action.BotID, action.Generation, action.ActionRunID)
}

func classifyBotActionJournalPhase(action *bot.ActionEvent) botActionJournalPhase {
	if isTerminalBotActionStatus(action.Status) {
		return botActionJournalTerminal
	}
	if action.Status == "waiting" || isBarrierArrivedAction(action.Result) {
		return botActionJournalWaiting
	}
	return botActionJournalRunning
}

func isTerminalBotActionStatus(status string) bool {
	switch status {
	case "succeeded", "failed", "timed_out", "cancelled":
		return true
	default:
		return false
	}
}

func isBarrierArrivedAction(result json.RawMessage) bool {
	var value struct {
		Type string `json:"type"`
	}
	return len(result) > 0 && json.Unmarshal(result, &value) == nil && value.Type == "barrier-arrived"
}

func cloneBotActionWorkerEvent(event *bot.BotWorkerEvent) *bot.BotWorkerEvent {
	if event == nil {
		return nil
	}
	cloned := *event
	if event.Action != nil {
		action := *event.Action
		action.Result = append(json.RawMessage(nil), event.Action.Result...)
		cloned.Action = &action
	}
	return &cloned
}

type botFleetManager interface {
	CapacitySnapshot() bot.BotCapacitySnapshot
	FleetSnapshot(sessionID string) []bot.BotState
	ApplyBotBatch(context.Context, string, string, string, []bot.BotConfig) (*bot.BotWorkerEvent, error)
	StopBotBatch(context.Context, string, []string, int64, string) (*bot.BotWorkerEvent, error)
	SignalActions(context.Context, string, []bot.ActionSignal) (*bot.BotWorkerEvent, error)
	RequestFleetSnapshot(context.Context, string) (*bot.BotWorkerEvent, error)
}

type botBatchCacheEntry struct {
	payloadHash           string
	workerEpoch           string
	workerEpochGeneration int64
	response              *workerpb.ApplyBotBatchResponse
	err                   error
	createdAt             time.Time
	done                  chan struct{}
}

type botFleetOwnership struct {
	workerEpoch           string
	workerEpochGeneration int64
}

type botBatchDispatchPlan struct {
	results       []*workerpb.ApplyBotBatchItemResult
	createConfigs []bot.BotConfig
	createIndexes []int
	stopIDs       []string
	stopIndexes   []int
}

var botRequestSequence atomic.Uint64

// GetBotCapacity 返回本节点 bot-worker 的准入容量与运行时快照。
func (s *Server) GetBotCapacity(ctx context.Context, _ *workerpb.GetBotCapacityRequest) (*workerpb.GetBotCapacityResponse, error) {
	if err := s.prepareBotFleet(ctx); err != nil {
		return unavailableCapacityResponse(err), nil
	}
	return capacityToProto(s.botFleet.CapacitySnapshot()), nil
}

// ApplyBotBatch 幂等应用最多 50 个 assignment，并仅把 Node 明确回执计为 accepted。
func (s *Server) ApplyBotBatch(ctx context.Context, req *workerpb.ApplyBotBatchRequest) (*workerpb.ApplyBotBatchResponse, error) {
	if err := validateBotBatchRequest(req); err != nil {
		return nil, err
	}
	if err := s.prepareBotFleet(ctx); err != nil {
		return unavailableBotBatchResponse(req, err), nil
	}
	payloadHash, err := botBatchPayloadHash(req)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "计算 Bot 批次摘要失败: %v", err)
	}
	capacity := s.botFleet.CapacitySnapshot()
	entry, owner, err := s.beginBotBatch(ctx, req.IdempotencyKey, payloadHash, capacity)
	if err != nil {
		return nil, err
	}
	if !owner {
		if entry.err != nil {
			return nil, entry.err
		}
		return cloneBotBatchResponse(entry.response), nil
	}

	response, applyErr := s.applyBotBatchOnce(ctx, req)
	if applyErr != nil {
		s.abortBotBatch(req.IdempotencyKey, entry, applyErr)
		return nil, applyErr
	}
	currentCapacity := s.botFleet.CapacitySnapshot()
	if stableBotBatchResponse(response) && botBatchEntryMatchesCapacity(entry, currentCapacity) {
		s.completeBotBatch(entry, response)
	} else {
		s.releaseBotBatch(req.IdempotencyKey, entry, response)
	}
	return cloneBotBatchResponse(response), nil
}

func (s *Server) applyBotBatchOnce(ctx context.Context, req *workerpb.ApplyBotBatchRequest) (*workerpb.ApplyBotBatchResponse, error) {
	s.botOwnershipMu.Lock()
	defer s.botOwnershipMu.Unlock()

	capacity := s.botFleet.CapacitySnapshot()
	s.pruneBotOwnershipLocked(capacity)
	if req.ExpectedCapacityGeneration != 0 && req.ExpectedCapacityGeneration != capacity.CapacityGeneration {
		return nil, status.Errorf(codes.FailedPrecondition, "Bot 容量世代已变化: expected=%d actual=%d", req.ExpectedCapacityGeneration, capacity.CapacityGeneration)
	}

	plan := planBotBatch(req.Assignments, capacity, s.botFleet.FleetSnapshot(""))
	s.dispatchBotCreates(ctx, req, &plan)
	s.dispatchBotStops(ctx, req, &plan)
	currentCapacity := s.botFleet.CapacitySnapshot()
	s.updateBotOwnershipLocked(req.Assignments, plan.results, capacity, currentCapacity)
	return &workerpb.ApplyBotBatchResponse{
		BatchId:            req.BatchId,
		IdempotencyKey:     req.IdempotencyKey,
		Results:            plan.results,
		CapacityGeneration: currentCapacity.CapacityGeneration,
	}, nil
}

// GetBotFleetSnapshot 返回 Node 当前完整快照，供 CP 建立流基线。
func (s *Server) GetBotFleetSnapshot(ctx context.Context, req *workerpb.GetBotFleetSnapshotRequest) (*workerpb.GetBotFleetSnapshotResponse, error) {
	if err := s.prepareBotFleet(ctx); err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	event, err := s.botFleet.RequestFleetSnapshot(ctx, nextBotRequestID("snapshot"))
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "获取 Bot fleet 快照失败: %v", err)
	}
	states := s.botFleet.FleetSnapshot(req.SessionUuid)
	if event != nil {
		states = filterBotStates(event.Bots, req.SessionUuid)
	}
	return fleetSnapshotResponse(states, s.botFleet.CapacitySnapshot()), nil
}

// StreamBotFleetEvents 持续发送类型化 runtime/action 事件，旧 BotEvent 流保持不变。
func (s *Server) StreamBotFleetEvents(req *workerpb.StreamBotFleetEventsRequest, stream workerpb.WorkerService_StreamBotFleetEventsServer) error {
	if err := s.prepareBotFleet(stream.Context()); err != nil {
		return status.Error(codes.Unavailable, err.Error())
	}
	ch := make(chan *bot.BotWorkerEvent, 256)
	subscriber := s.addBotFleetEventSubscriber(ch, req.SessionUuid)
	defer s.removeBotEventSubscriber(ch)

	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case event, ok := <-ch:
			if !ok {
				if err := subscriber.terminalError(); err != nil {
					return err
				}
				return status.Error(codes.Unavailable, "bot-worker 进程已退出，请重新订阅")
			}
			for _, fleetEvent := range botWorkerEventToFleetProto(event, req.SessionUuid) {
				if err := stream.Send(fleetEvent); err != nil {
					return err
				}
			}
		}
	}
}

// SignalBotActions 批量投递通用动作信号，并返回逐信号回执。
func (s *Server) SignalBotActions(ctx context.Context, req *workerpb.SignalBotActionsRequest) (*workerpb.SignalBotActionsResponse, error) {
	if err := validateBotActionSignals(req); err != nil {
		return nil, err
	}
	if err := s.prepareBotFleet(ctx); err != nil {
		return unavailableSignalResponse(req.Signals, err), nil
	}

	signals := make([]bot.ActionSignal, 0, len(req.Signals))
	for _, signal := range req.Signals {
		signals = append(signals, botActionSignalFromProto(signal))
	}
	event, err := s.botFleet.SignalActions(ctx, nextBotRequestID("signal"), signals)
	if err != nil {
		return unavailableSignalResponse(req.Signals, err), nil
	}
	return signalResponseFromEvent(req.Signals, event), nil
}

func (s *Server) prepareBotFleet(ctx context.Context) error {
	if s.botFleet == nil {
		return fmt.Errorf("本节点未启用 Bot 能力")
	}
	if s.botMgr == nil {
		return nil
	}
	if err := s.ensureBotManager(); err != nil {
		return err
	}
	if err := s.botMgr.WaitReady(ctx); err != nil {
		return fmt.Errorf("等待 bot-worker 就绪失败: %w", err)
	}
	return nil
}

func validateBotActionSignals(req *workerpb.SignalBotActionsRequest) error {
	if req == nil {
		return status.Error(codes.InvalidArgument, "动作信号请求不能为空")
	}
	if len(req.Signals) > maxBotActionSignals {
		return status.Errorf(codes.InvalidArgument, "单次动作信号不能超过 %d 个", maxBotActionSignals)
	}
	seen := make(map[string]struct{}, len(req.Signals))
	for _, signal := range req.Signals {
		if signal == nil || signal.SignalId == "" {
			return status.Error(codes.InvalidArgument, "signalId 不能为空")
		}
		if _, exists := seen[signal.SignalId]; exists {
			return status.Errorf(codes.InvalidArgument, "signalId 不能重复: %s", signal.SignalId)
		}
		seen[signal.SignalId] = struct{}{}
	}
	return nil
}

func validateBotBatchRequest(req *workerpb.ApplyBotBatchRequest) error {
	if req == nil || len(req.Assignments) == 0 {
		return status.Error(codes.InvalidArgument, "Bot 批次不能为空")
	}
	if len(req.Assignments) > maxBotBatchSize {
		return status.Errorf(codes.InvalidArgument, "单批 Bot assignment 不能超过 %d 个", maxBotBatchSize)
	}
	if req.BatchId == "" || req.IdempotencyKey == "" {
		return status.Error(codes.InvalidArgument, "batchId 和 idempotencyKey 不能为空")
	}
	return nil
}

func botBatchPayloadHash(req *workerpb.ApplyBotBatchRequest) (string, error) {
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(req)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func (s *Server) beginBotBatch(ctx context.Context, key, payloadHash string, capacity bot.BotCapacitySnapshot) (*botBatchCacheEntry, bool, error) {
	s.botBatchMu.Lock()
	if s.botBatchResults == nil {
		s.botBatchResults = make(map[string]*botBatchCacheEntry)
	}
	s.cleanupBotBatchCacheLocked(time.Now())
	if entry, ok := s.botBatchResults[key]; ok {
		if entry.workerEpoch != capacity.WorkerEpoch || entry.workerEpochGeneration != capacity.WorkerEpochGeneration {
			delete(s.botBatchResults, key)
		} else {
			if entry.payloadHash != payloadHash {
				s.botBatchMu.Unlock()
				return nil, false, status.Error(codes.FailedPrecondition, "相同 idempotencyKey 对应的 Bot 批次载荷不一致")
			}
			s.botBatchMu.Unlock()
			select {
			case <-entry.done:
				return entry, false, nil
			case <-ctx.Done():
				return nil, false, ctx.Err()
			}
		}
	}
	if len(s.botBatchResults) >= botBatchCacheLimit {
		s.botBatchMu.Unlock()
		return nil, false, status.Error(codes.ResourceExhausted, "Bot 批次幂等缓存已满")
	}
	entry := &botBatchCacheEntry{
		payloadHash: payloadHash, workerEpoch: capacity.WorkerEpoch,
		workerEpochGeneration: capacity.WorkerEpochGeneration, createdAt: time.Now(), done: make(chan struct{}),
	}
	s.botBatchResults[key] = entry
	s.botBatchMu.Unlock()
	return entry, true, nil
}

func (s *Server) completeBotBatch(entry *botBatchCacheEntry, response *workerpb.ApplyBotBatchResponse) {
	s.botBatchMu.Lock()
	entry.response = cloneBotBatchResponse(response)
	close(entry.done)
	s.botBatchMu.Unlock()
}

func (s *Server) releaseBotBatch(key string, entry *botBatchCacheEntry, response *workerpb.ApplyBotBatchResponse) {
	s.botBatchMu.Lock()
	entry.response = cloneBotBatchResponse(response)
	if s.botBatchResults[key] == entry {
		delete(s.botBatchResults, key)
	}
	close(entry.done)
	s.botBatchMu.Unlock()
}

func stableBotBatchResponse(response *workerpb.ApplyBotBatchResponse) bool {
	if response == nil || len(response.Results) == 0 {
		return false
	}
	for _, result := range response.Results {
		if result == nil || result.Status == botBatchStatusUnavailable || result.Status == botBatchStatusCapacity {
			return false
		}
	}
	return true
}

func botBatchEntryMatchesCapacity(entry *botBatchCacheEntry, capacity bot.BotCapacitySnapshot) bool {
	return entry.workerEpoch == capacity.WorkerEpoch && entry.workerEpochGeneration == capacity.WorkerEpochGeneration
}

func sameBotWorkerEpoch(left, right bot.BotCapacitySnapshot) bool {
	return left.WorkerEpoch == right.WorkerEpoch && left.WorkerEpochGeneration == right.WorkerEpochGeneration
}

func (s *Server) updateBotOwnershipLocked(assignments []*workerpb.BotAssignment, results []*workerpb.ApplyBotBatchItemResult, started, current bot.BotCapacitySnapshot) {
	if !sameBotWorkerEpoch(started, current) {
		s.pruneBotOwnershipLocked(current)
		return
	}
	if s.botOwnership == nil {
		s.botOwnership = make(map[string]botFleetOwnership)
	}
	for index, assignment := range assignments {
		if assignment == nil || index >= len(results) || results[index] == nil {
			continue
		}
		result := results[index]
		if assignment.DesiredState == "running" && result.Accepted {
			s.botOwnership[assignment.BotUuid] = botFleetOwnership{
				workerEpoch: current.WorkerEpoch, workerEpochGeneration: current.WorkerEpochGeneration,
			}
		}
		if assignment.DesiredState == "stopped" && (result.Accepted || result.ErrorCode == "already_stopped") {
			delete(s.botOwnership, assignment.BotUuid)
		}
	}
}

func (s *Server) pruneBotOwnershipLocked(capacity bot.BotCapacitySnapshot) {
	for botID, ownership := range s.botOwnership {
		if ownership.workerEpoch != capacity.WorkerEpoch || ownership.workerEpochGeneration != capacity.WorkerEpochGeneration {
			delete(s.botOwnership, botID)
		}
	}
}

func (s *Server) isFleetOwnedBotLocked(botID string) bool {
	if s.botFleet == nil {
		return false
	}
	capacity := s.botFleet.CapacitySnapshot()
	s.pruneBotOwnershipLocked(capacity)
	_, owned := s.botOwnership[botID]
	return owned
}

func (s *Server) clearBotOwnership(epoch string, generation int64) {
	s.botOwnershipMu.Lock()
	defer s.botOwnershipMu.Unlock()
	if epoch == "" && generation == 0 {
		clear(s.botOwnership)
		return
	}
	for botID, ownership := range s.botOwnership {
		if epoch != "" && ownership.workerEpoch != epoch {
			continue
		}
		if generation != 0 && ownership.workerEpochGeneration != generation {
			continue
		}
		delete(s.botOwnership, botID)
	}
}

func (s *Server) clearBotBatchCache() {
	s.botBatchMu.Lock()
	clear(s.botBatchResults)
	s.botBatchMu.Unlock()
}

func (s *Server) abortBotBatch(key string, entry *botBatchCacheEntry, err error) {
	s.botBatchMu.Lock()
	entry.err = err
	if s.botBatchResults[key] == entry {
		delete(s.botBatchResults, key)
	}
	close(entry.done)
	s.botBatchMu.Unlock()
}

func (s *Server) cleanupBotBatchCacheLocked(now time.Time) {
	for key, entry := range s.botBatchResults {
		select {
		case <-entry.done:
			if now.Sub(entry.createdAt) >= botBatchCacheTTL {
				delete(s.botBatchResults, key)
			}
		default:
		}
	}
	for len(s.botBatchResults) >= botBatchCacheLimit {
		key := oldestCompletedBotBatch(s.botBatchResults)
		if key == "" {
			return
		}
		delete(s.botBatchResults, key)
	}
}

func oldestCompletedBotBatch(entries map[string]*botBatchCacheEntry) string {
	var oldestKey string
	var oldestTime time.Time
	for key, entry := range entries {
		select {
		case <-entry.done:
			if oldestKey == "" || entry.createdAt.Before(oldestTime) {
				oldestKey, oldestTime = key, entry.createdAt
			}
		default:
		}
	}
	return oldestKey
}

func validateBotAssignment(assignment *workerpb.BotAssignment) string {
	if assignment == nil {
		return "Bot assignment 不能为空"
	}
	if strings.TrimSpace(assignment.BotUuid) == "" {
		return "botUuid 不能为空"
	}
	if strings.TrimSpace(assignment.SessionUuid) == "" {
		return "sessionUuid 不能为空"
	}
	if assignment.Generation <= 0 {
		return "generation 必须大于 0"
	}
	if !validBotConfigHash(assignment.ConfigHash) {
		return "configHash 必须是 64 位十六进制 SHA-256 摘要"
	}
	if assignment.DesiredState == "stopped" {
		return ""
	}
	if assignment.DesiredState != "running" {
		return "desiredState 仅支持 running/stopped"
	}
	return validateRunningBotAssignment(assignment)
}

func validBotConfigHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateRunningBotAssignment(assignment *workerpb.BotAssignment) string {
	if strings.TrimSpace(assignment.InstanceUuid) == "" {
		return "running assignment 的 instanceUuid 不能为空"
	}
	if strings.TrimSpace(assignment.Name) == "" {
		return "running assignment 的 name 不能为空"
	}
	if strings.TrimSpace(assignment.Host) == "" {
		return "running assignment 的 host 不能为空"
	}
	if assignment.Port <= 0 || assignment.Port > 65535 {
		return "running assignment 的 port 必须在 1-65535 范围内"
	}
	return ""
}

func assignmentBotID(assignment *workerpb.BotAssignment) string {
	if assignment == nil {
		return ""
	}
	return assignment.BotUuid
}

func planBotBatch(assignments []*workerpb.BotAssignment, capacity bot.BotCapacitySnapshot, states []bot.BotState) botBatchDispatchPlan {
	plan := botBatchDispatchPlan{results: make([]*workerpb.ApplyBotBatchItemResult, len(assignments))}
	existing := make(map[string]bot.BotState, len(states))
	for _, state := range states {
		existing[state.ID] = state
	}
	remaining := capacity.MaxBots - capacity.ActiveBots
	seen := make(map[string]struct{}, len(assignments))
	for index, assignment := range assignments {
		remaining = planBotAssignment(&plan, index, assignment, existing, seen, capacity, remaining)
	}
	return plan
}

func planBotAssignment(plan *botBatchDispatchPlan, index int, assignment *workerpb.BotAssignment, existing map[string]bot.BotState, seen map[string]struct{}, capacity bot.BotCapacitySnapshot, remaining int) int {
	if message := validateBotAssignment(assignment); message != "" {
		plan.results[index] = invalidBotAssignmentResult(assignmentBotID(assignment), message)
		return remaining
	}
	if _, duplicate := seen[assignment.BotUuid]; duplicate {
		plan.results[index] = conflictBotResult(assignment.BotUuid, "duplicate_assignment", "批次内 Bot assignment 重复")
		return remaining
	}
	seen[assignment.BotUuid] = struct{}{}
	state, exists := existing[assignment.BotUuid]
	if reason := assignmentConflictReason(assignment, state, exists); reason != "" {
		plan.results[index] = conflictBotResult(assignment.BotUuid, reason, "Bot assignment 与当前版本冲突")
		return remaining
	}
	if !capacity.Ready || capacity.Legacy {
		plan.results[index] = unavailableBotResult(assignment.BotUuid, "bot-worker 未声明 fleet 准入能力")
		return remaining
	}
	if assignment.DesiredState == "stopped" {
		return planBotStop(plan, index, assignment, exists, remaining)
	}
	if !exists && remaining <= 0 {
		plan.results[index] = capacityBotResult(assignment.BotUuid)
		return remaining
	}
	plan.createIndexes = append(plan.createIndexes, index)
	plan.createConfigs = append(plan.createConfigs, botConfigFromAssignment(assignment))
	if !exists {
		remaining--
	}
	return remaining
}

func planBotStop(plan *botBatchDispatchPlan, index int, assignment *workerpb.BotAssignment, exists bool, remaining int) int {
	if !exists {
		plan.results[index] = &workerpb.ApplyBotBatchItemResult{
			BotUuid: assignment.BotUuid, Accepted: true, Skipped: true,
			Status: botBatchStatusAccepted, ErrorCode: "already_stopped",
		}
		return remaining
	}
	plan.stopIndexes = append(plan.stopIndexes, index)
	plan.stopIDs = append(plan.stopIDs, assignment.BotUuid)
	return remaining + 1
}

func assignmentConflictReason(assignment *workerpb.BotAssignment, state bot.BotState, exists bool) string {
	if !exists {
		return ""
	}
	if assignment.Generation < state.Generation {
		return "stale_generation"
	}
	if assignment.Generation == state.Generation && assignment.ConfigHash != "" && state.ConfigHash != "" && assignment.ConfigHash != state.ConfigHash {
		return "config_hash_conflict"
	}
	return ""
}

func (s *Server) dispatchBotCreates(ctx context.Context, req *workerpb.ApplyBotBatchRequest, plan *botBatchDispatchPlan) {
	if len(plan.createConfigs) == 0 {
		return
	}
	event, err := s.botFleet.ApplyBotBatch(ctx, nextBotRequestID("create"), req.BatchId, req.IdempotencyKey, plan.createConfigs)
	botIDs := make([]string, 0, len(plan.createConfigs))
	for _, config := range plan.createConfigs {
		botIDs = append(botIDs, config.ID)
	}
	mergeBotDispatchResults(plan.results, plan.createIndexes, botIDs, event, err)
}

func (s *Server) dispatchBotStops(ctx context.Context, req *workerpb.ApplyBotBatchRequest, plan *botBatchDispatchPlan) {
	if len(plan.stopIDs) == 0 {
		return
	}
	event, err := s.botFleet.StopBotBatch(ctx, nextBotRequestID("stop"), plan.stopIDs, maxAssignmentGeneration(req.Assignments), "desired_state=stopped")
	mergeBotDispatchResults(plan.results, plan.stopIndexes, plan.stopIDs, event, err)
}

func mergeBotDispatchResults(results []*workerpb.ApplyBotBatchItemResult, indexes []int, botIDs []string, event *bot.BotWorkerEvent, err error) {
	if err != nil {
		for position, index := range indexes {
			results[index] = unavailableBotResult(botIDs[position], err.Error())
		}
		return
	}
	byID := make(map[string]bot.BotItemResult)
	if event != nil {
		for _, result := range event.Results {
			byID[result.BotID] = result
		}
	}
	for position, index := range indexes {
		botID := botIDs[position]
		item, ok := byID[botID]
		if !ok {
			results[index] = unavailableBotResult(botID, "bot-worker 未返回逐项回执")
			continue
		}
		results[index] = botItemResultToProto(item)
	}
}

func botConfigFromAssignment(assignment *workerpb.BotAssignment) bot.BotConfig {
	return bot.BotConfig{
		ID: assignment.BotUuid, Name: assignment.Name, Host: assignment.Host, Port: int(assignment.Port),
		Username: assignment.Username, Version: assignment.Version, Auth: assignment.Auth,
		SessionID: assignment.SessionUuid, Generation: assignment.Generation, ConfigHash: assignment.ConfigHash,
		CohortKey: assignment.CohortKey, Scenario: json.RawMessage(assignment.ScenarioJson), ResumeStepID: assignment.ResumeStepId,
		ConnectNotBefore: assignment.ConnectNotBeforeUnixMs, CorrelationSeed: assignment.CorrelationSeed,
	}
}

func botItemResultToProto(result bot.BotItemResult) *workerpb.ApplyBotBatchItemResult {
	statusValue := result.Status
	if result.Accepted {
		statusValue = botBatchStatusAccepted
	} else if statusValue == "" && result.Skipped {
		statusValue = botBatchStatusConflict
	} else if statusValue == "" {
		statusValue = botBatchStatusUnavailable
	}
	return &workerpb.ApplyBotBatchItemResult{
		BotUuid: result.BotID, Accepted: result.Accepted, Skipped: result.Skipped,
		Status: statusValue, ErrorCode: result.ErrorCode, Error: result.Error,
	}
}

func invalidBotAssignmentResult(botID, message string) *workerpb.ApplyBotBatchItemResult {
	return conflictBotResult(botID, "invalid_assignment", message)
}

func conflictBotResult(botID, code, message string) *workerpb.ApplyBotBatchItemResult {
	return &workerpb.ApplyBotBatchItemResult{BotUuid: botID, Skipped: true, Status: botBatchStatusConflict, ErrorCode: code, Error: message}
}

func capacityBotResult(botID string) *workerpb.ApplyBotBatchItemResult {
	return &workerpb.ApplyBotBatchItemResult{BotUuid: botID, Status: botBatchStatusCapacity, ErrorCode: botBatchStatusCapacity, Error: "Bot 容量不足"}
}

func unavailableBotResult(botID, message string) *workerpb.ApplyBotBatchItemResult {
	return &workerpb.ApplyBotBatchItemResult{BotUuid: botID, Status: botBatchStatusUnavailable, ErrorCode: botBatchStatusUnavailable, Error: message}
}

func unavailableBotBatchResponse(req *workerpb.ApplyBotBatchRequest, err error) *workerpb.ApplyBotBatchResponse {
	results := make([]*workerpb.ApplyBotBatchItemResult, 0, len(req.Assignments))
	for _, assignment := range req.Assignments {
		botID := ""
		if assignment != nil {
			botID = assignment.BotUuid
		}
		results = append(results, unavailableBotResult(botID, err.Error()))
	}
	return &workerpb.ApplyBotBatchResponse{BatchId: req.BatchId, IdempotencyKey: req.IdempotencyKey, Results: results}
}

func maxAssignmentGeneration(assignments []*workerpb.BotAssignment) int64 {
	var generation int64
	for _, assignment := range assignments {
		if assignment != nil && assignment.Generation > generation {
			generation = assignment.Generation
		}
	}
	return generation
}

func capacityToProto(snapshot bot.BotCapacitySnapshot) *workerpb.GetBotCapacityResponse {
	return &workerpb.GetBotCapacityResponse{
		Ready: snapshot.Ready, Legacy: snapshot.Legacy, MaxBots: int32(snapshot.MaxBots),
		ActiveBots: int32(snapshot.ActiveBots), ConnectingBots: int32(snapshot.ConnectingBots),
		CapacityGeneration: snapshot.CapacityGeneration, WorkerEpoch: snapshot.WorkerEpoch,
		WorkerEpochGeneration: snapshot.WorkerEpochGeneration, BotWorkerVersion: snapshot.BotWorkerVersion,
		RssBytes: snapshot.RSSBytes, EventLoopP95Ms: snapshot.EventLoopP95Ms,
		ObservedAtUnixMs: snapshot.ObservedAt.UnixMilli(), UnavailableReason: snapshot.UnavailableReason,
		Features: append([]string(nil), snapshot.Features...),
	}
}

func unavailableCapacityResponse(err error) *workerpb.GetBotCapacityResponse {
	return &workerpb.GetBotCapacityResponse{Legacy: true, MaxBots: 50, ObservedAtUnixMs: time.Now().UnixMilli(), UnavailableReason: err.Error()}
}

func fleetSnapshotResponse(states []bot.BotState, capacity bot.BotCapacitySnapshot) *workerpb.GetBotFleetSnapshotResponse {
	bots := make([]*workerpb.BotRuntimeSnapshot, 0, len(states))
	for i := range states {
		bots = append(bots, botStateToRuntimeProto(&states[i]))
	}
	return &workerpb.GetBotFleetSnapshotResponse{Bots: bots, CapacityGeneration: capacity.CapacityGeneration, ObservedAtUnixMs: time.Now().UnixMilli()}
}

func filterBotStates(states []bot.BotState, sessionID string) []bot.BotState {
	if sessionID == "" {
		return states
	}
	filtered := make([]bot.BotState, 0, len(states))
	for _, state := range states {
		if state.SessionID == sessionID {
			filtered = append(filtered, state)
		}
	}
	return filtered
}

func botStateToRuntimeProto(state *bot.BotState) *workerpb.BotRuntimeSnapshot {
	result := &workerpb.BotRuntimeSnapshot{
		BotUuid: state.ID, SessionUuid: state.SessionID, Generation: state.Generation, ConfigHash: state.ConfigHash,
		WorkerEpoch: state.WorkerEpoch, WorkerEpochGeneration: state.WorkerEpochGeneration, EventSeq: state.EventSeq,
		Status: state.Status, CurrentStepId: state.CurrentStepID, Health: state.Health, Food: int32(state.Food),
		ReconnectCount: int32(state.ReconnectCount), ErrorCode: state.ErrorCode, LastError: state.LastError,
		ObservedAtUnixMs: state.ObservedAt,
	}
	if state.Position != nil {
		result.Pos = &workerpb.BotPosition{X: state.Position.X, Y: state.Position.Y, Z: state.Position.Z}
	}
	return result
}

func botWorkerEventToFleetProto(event *bot.BotWorkerEvent, sessionID string) []*workerpb.BotFleetEvent {
	if event == nil {
		return nil
	}
	if event.Evt == "action-event" && event.Action != nil {
		if sessionID != "" && event.Action.SessionID != sessionID {
			return nil
		}
		return []*workerpb.BotFleetEvent{{Event: &workerpb.BotFleetEvent_ActionEvent{ActionEvent: actionEventToProto(event.Action)}}}
	}
	if event.Evt != "bot-state" {
		return nil
	}
	results := make([]*workerpb.BotFleetEvent, 0, len(event.Bots))
	for i := range event.Bots {
		if sessionID != "" && event.Bots[i].SessionID != sessionID {
			continue
		}
		results = append(results, &workerpb.BotFleetEvent{Event: &workerpb.BotFleetEvent_RuntimeSnapshot{RuntimeSnapshot: botStateToRuntimeProto(&event.Bots[i])}})
	}
	return results
}

func actionEventToProto(event *bot.ActionEvent) *workerpb.BotActionEvent {
	return &workerpb.BotActionEvent{
		BotUuid: event.BotID, SessionUuid: event.SessionID, Generation: event.Generation,
		ActionRunId: event.ActionRunID, StepId: event.StepID, Attempt: int32(event.Attempt), Status: event.Status,
		ErrorCode: event.ErrorCode, Message: event.Message, CorrelationToken: event.CorrelationToken,
		ResultJson: string(event.Result), DurationMs: event.DurationMS, ObservedAtUnixMs: event.ObservedAt,
	}
}

func botActionSignalFromProto(signal *workerpb.BotActionSignal) bot.ActionSignal {
	if signal == nil {
		return bot.ActionSignal{}
	}
	return bot.ActionSignal{
		SignalID: signal.SignalId, BotID: signal.BotUuid, SessionID: signal.SessionUuid, Generation: signal.Generation,
		ActionRunID: signal.ActionRunId, StepID: signal.StepId, Type: signal.Type,
		CorrelationToken: signal.CorrelationToken, Payload: json.RawMessage(signal.PayloadJson), ObservedAt: signal.ObservedAtUnixMs,
	}
}

func signalResponseFromEvent(signals []*workerpb.BotActionSignal, event *bot.BotWorkerEvent) *workerpb.SignalBotActionsResponse {
	byID := make(map[string]bot.SignalItemResult)
	if event != nil {
		for _, result := range event.SignalResults {
			byID[result.SignalID] = result
		}
	}
	results := make([]*workerpb.SignalBotActionItemResult, 0, len(signals))
	for _, signal := range signals {
		signalID := ""
		if signal != nil {
			signalID = signal.SignalId
		}
		result, ok := byID[signalID]
		if !ok {
			results = append(results, &workerpb.SignalBotActionItemResult{SignalId: signalID, ErrorCode: botBatchStatusUnavailable, Error: "bot-worker 未返回逐项回执"})
			continue
		}
		results = append(results, &workerpb.SignalBotActionItemResult{SignalId: result.SignalID, Accepted: result.Accepted, Skipped: result.Skipped, ErrorCode: result.ErrorCode, Error: result.Error})
	}
	return &workerpb.SignalBotActionsResponse{Results: results}
}

func unavailableSignalResponse(signals []*workerpb.BotActionSignal, err error) *workerpb.SignalBotActionsResponse {
	results := make([]*workerpb.SignalBotActionItemResult, 0, len(signals))
	for _, signal := range signals {
		signalID := ""
		if signal != nil {
			signalID = signal.SignalId
		}
		results = append(results, &workerpb.SignalBotActionItemResult{SignalId: signalID, ErrorCode: botBatchStatusUnavailable, Error: err.Error()})
	}
	return &workerpb.SignalBotActionsResponse{Results: results}
}

func (s *Server) addBotEventSubscriber(ch chan *bot.BotWorkerEvent) {
	s.botEventMu.Lock()
	s.botEventSubs = append(s.botEventSubs, ch)
	s.botEventMu.Unlock()
}

func (s *Server) addBotFleetEventSubscriber(ch chan *bot.BotWorkerEvent, sessionIDs ...string) *botFleetEventSubscriber {
	generation := s.currentBotWorkerGeneration()
	sessionID := ""
	if len(sessionIDs) > 0 {
		sessionID = sessionIDs[0]
	}
	s.botEventMu.Lock()
	s.alignBotActionJournalGenerationLocked(generation)
	subscriber := newBotFleetEventSubscriberWithReplay(ch, generation, s.botActionJournalReplayLocked(sessionID))
	s.botEventSubs = append(s.botEventSubs, ch)
	if s.botFleetEventSubs == nil {
		s.botFleetEventSubs = make(map[chan *bot.BotWorkerEvent]*botFleetEventSubscriber)
	}
	s.botFleetEventSubs[ch] = subscriber
	s.botEventMu.Unlock()

	if s.botMgr != nil {
		currentGeneration := s.currentBotWorkerGeneration()
		if !s.botMgr.IsRunning() || currentGeneration != generation {
			s.closeBotFleetEventSubscriber(ch, status.Error(codes.Unavailable, "bot-worker 进程已退出，请重新订阅"))
		}
	}
	return subscriber
}

func (s *Server) closeBotFleetEventSubscriber(ch chan *bot.BotWorkerEvent, err error) {
	s.botEventMu.Lock()
	subscriber, fleet := s.botFleetEventSubs[ch]
	removed := fleet && s.removeBotEventSubscriberLocked(ch)
	s.botEventMu.Unlock()
	if removed {
		subscriber.stop(err)
	}
}

func (s *Server) removeBotEventSubscriber(ch chan *bot.BotWorkerEvent) {
	s.botEventMu.Lock()
	subscriber := s.botFleetEventSubs[ch]
	removed := s.removeBotEventSubscriberLocked(ch)
	s.botEventMu.Unlock()
	if !removed {
		return
	}
	if subscriber != nil {
		subscriber.stop(nil)
		return
	}
	close(ch)
}

func (s *Server) removeBotEventSubscriberLocked(ch chan *bot.BotWorkerEvent) bool {
	delete(s.botFleetEventSubs, ch)
	for i, eventChannel := range s.botEventSubs {
		if eventChannel == ch {
			s.botEventSubs = append(s.botEventSubs[:i], s.botEventSubs[i+1:]...)
			return true
		}
	}
	return false
}

func nextBotRequestID(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), botRequestSequence.Add(1))
}

func cloneBotBatchResponse(response *workerpb.ApplyBotBatchResponse) *workerpb.ApplyBotBatchResponse {
	if response == nil {
		return nil
	}
	return proto.Clone(response).(*workerpb.ApplyBotBatchResponse)
}
