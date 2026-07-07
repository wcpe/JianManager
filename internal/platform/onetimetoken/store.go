package onetimetoken

import (
	"sync"
	"time"
)

// Store 记录已消费的一次性 token，并按过期时间清理。
type Store struct {
	mu   sync.Mutex
	used map[string]time.Time
	now  func() time.Time
}

// NewStore 创建一次性 token 存储。
func NewStore() *Store {
	return &Store{
		used: make(map[string]time.Time),
		now:  time.Now,
	}
}

// Consume 尝试消费 token；已消费、空值或已过期均返回 false。
func (s *Store) Consume(token string, expiresAt time.Time) bool {
	if token == "" || s == nil {
		return false
	}
	now := s.now()
	if !expiresAt.After(now) {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked(now)
	if _, ok := s.used[token]; ok {
		return false
	}
	s.used[token] = expiresAt
	return true
}

// Release 释放尚未真正建立会话的 token 消费记录。
func (s *Store) Release(token string) {
	if token == "" || s == nil {
		return
	}
	s.mu.Lock()
	delete(s.used, token)
	s.mu.Unlock()
}

func (s *Store) purgeExpiredLocked(now time.Time) {
	for token, expiresAt := range s.used {
		if !expiresAt.After(now) {
			delete(s.used, token)
		}
	}
}
