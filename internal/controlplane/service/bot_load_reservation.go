package service

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

var ErrBotLoadReservationCapacity = errors.New("Bot 负载软预留容量不足")

// BotLoadReservationLease 是单次运行的内存软预留快照。
type BotLoadReservationLease struct {
	RunID        uint
	Reservations map[uint]int
	ExpiresAt    time.Time
	Revision     uint64
}

// BotLoadReservationStore 维护 run→node/count 的 60 秒进程内软预留。
type BotLoadReservationStore struct {
	mu       sync.Mutex
	clock    BotLoadClock
	ttl      time.Duration
	revision uint64
	leases   map[uint]BotLoadReservationLease
}

// NewBotLoadReservationStore 创建软预留存储；CP 重启丢失是 ADR-074 接受的边界。
func NewBotLoadReservationStore(clock BotLoadClock, ttl time.Duration) *BotLoadReservationStore {
	if ttl <= 0 {
		ttl = time.Minute
	}
	return &BotLoadReservationStore{
		clock: normalizeBotLoadClock(clock), ttl: ttl, leases: make(map[uint]BotLoadReservationLease),
	}
}

// Replace 原子替换同一运行的旧预留，并在锁内防止并发预检超卖。
func (s *BotLoadReservationStore) Replace(runID uint, desired, limits map[uint]int) (BotLoadReservationLease, error) {
	return s.ReplaceUntil(runID, desired, limits, s.clock.Now().UTC().Add(s.ttl))
}

// ReplaceUntil 使用计划令牌的同一过期时间原子替换预留。
func (s *BotLoadReservationStore) ReplaceUntil(runID uint, desired, limits map[uint]int, expiresAt time.Time) (BotLoadReservationLease, error) {
	if runID == 0 || !expiresAt.After(s.clock.Now()) {
		return BotLoadReservationLease{}, ErrBotLoadPreflightInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked()
	if err := s.validateReplacementLocked(runID, desired, limits); err != nil {
		return BotLoadReservationLease{}, err
	}
	s.revision++
	lease := BotLoadReservationLease{
		RunID: runID, Reservations: positiveBotLoadCounts(desired),
		ExpiresAt: expiresAt.UTC(), Revision: s.revision,
	}
	if len(lease.Reservations) == 0 {
		delete(s.leases, runID)
		return lease, nil
	}
	s.leases[runID] = lease
	return cloneBotLoadLease(lease), nil
}

func (s *BotLoadReservationStore) validateReplacementLocked(runID uint, desired, limits map[uint]int) error {
	usedByOthers := s.snapshotLocked(runID)
	for nodeID, count := range desired {
		if count < 0 {
			return ErrBotLoadPreflightInvalid
		}
		if usedByOthers[nodeID]+count > limits[nodeID] {
			return fmt.Errorf("%w: 节点 %d 需要 %d，剩余 %d", ErrBotLoadReservationCapacity, nodeID, count, max(0, limits[nodeID]-usedByOthers[nodeID]))
		}
	}
	return nil
}

// Snapshot 汇总未过期预留；excludeRunID 用于避免同一运行重预检重复扣减。
func (s *BotLoadReservationStore) Snapshot(excludeRunID uint) map[uint]int {
	if s == nil {
		return map[uint]int{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked()
	return s.snapshotLocked(excludeRunID)
}

// Lease 返回单运行当前租约副本。
func (s *BotLoadReservationStore) Lease(runID uint) (BotLoadReservationLease, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked()
	lease, ok := s.leases[runID]
	return cloneBotLoadLease(lease), ok
}

// RestoreIfCurrent 仅在 revision 仍是调用方刚写入的租约时回滚，避免覆盖更新的同 run 预检。
func (s *BotLoadReservationStore) RestoreIfCurrent(runID uint, currentRevision uint64, previous BotLoadReservationLease) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.leases[runID]
	if !ok || current.Revision != currentRevision {
		return false
	}
	if previous.RunID == 0 || len(previous.Reservations) == 0 || !previous.ExpiresAt.After(s.clock.Now()) {
		delete(s.leases, runID)
		return true
	}
	s.leases[runID] = cloneBotLoadLease(previous)
	return true
}

// Cleanup 清除所有已过期租约。
func (s *BotLoadReservationStore) Cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked()
}

func (s *BotLoadReservationStore) cleanupLocked() {
	now := s.clock.Now()
	for runID, lease := range s.leases {
		if !now.Before(lease.ExpiresAt) {
			delete(s.leases, runID)
		}
	}
}

func (s *BotLoadReservationStore) snapshotLocked(excludeRunID uint) map[uint]int {
	out := make(map[uint]int)
	for runID, lease := range s.leases {
		if runID == excludeRunID {
			continue
		}
		for nodeID, count := range lease.Reservations {
			out[nodeID] += count
		}
	}
	return out
}

func positiveBotLoadCounts(source map[uint]int) map[uint]int {
	out := make(map[uint]int, len(source))
	for nodeID, count := range source {
		if nodeID != 0 && count > 0 {
			out[nodeID] = count
		}
	}
	return out
}

func cloneBotLoadLease(lease BotLoadReservationLease) BotLoadReservationLease {
	lease.Reservations = cloneBotLoadCounts(lease.Reservations)
	return lease
}
