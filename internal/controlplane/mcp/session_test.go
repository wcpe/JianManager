package mcp

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

func testPrincipal(id uint, name string) *service.AgentPrincipal {
	return &service.AgentPrincipal{
		TokenID:     id,
		Name:        name,
		TokenPrefix: "jmat_ab12",
	}
}

func TestSessionManager_CreateAndList(t *testing.T) {
	m := NewSessionManager(Config{
		IdleTimeout:         time.Hour,
		AbsoluteTimeout:     24 * time.Hour,
		MaxGlobalSessions:   10,
		MaxSessionsPerToken: 4,
	})
	s, err := m.Create(CreateParams{
		Principal: testPrincipal(1, "ci"),
		ClientIP:  "1.2.3.4",
		Transport: TransportStreamableHTTP,
	})
	require.NoError(t, err)
	require.NotEmpty(t, s.ID)
	assert.Equal(t, TransportStreamableHTTP, s.Transport)
	assert.Equal(t, "ci", s.TokenName)
	assert.Equal(t, "jmat_ab12", s.TokenPrefix)

	list := m.List()
	require.Len(t, list, 1)
	assert.Equal(t, s.ID, list[0].ID)
	assert.Equal(t, "1.2.3.4", list[0].ClientIP)
}

func TestSessionManager_GlobalLimit(t *testing.T) {
	m := NewSessionManager(Config{
		IdleTimeout:         time.Hour,
		AbsoluteTimeout:     24 * time.Hour,
		MaxGlobalSessions:   2,
		MaxSessionsPerToken: 10,
	})
	_, err := m.Create(CreateParams{Principal: testPrincipal(1, "a"), Transport: TransportStreamableHTTP})
	require.NoError(t, err)
	_, err = m.Create(CreateParams{Principal: testPrincipal(2, "b"), Transport: TransportStreamableHTTP})
	require.NoError(t, err)
	_, err = m.Create(CreateParams{Principal: testPrincipal(3, "c"), Transport: TransportStreamableHTTP})
	require.ErrorIs(t, err, ErrSessionLimitGlobal)
	assert.Contains(t, err.Error(), "全局")
}

func TestSessionManager_PerTokenLimit(t *testing.T) {
	m := NewSessionManager(Config{
		IdleTimeout:         time.Hour,
		AbsoluteTimeout:     24 * time.Hour,
		MaxGlobalSessions:   32,
		MaxSessionsPerToken: 2,
	})
	p := testPrincipal(7, "tok")
	_, err := m.Create(CreateParams{Principal: p, Transport: TransportStreamableHTTP})
	require.NoError(t, err)
	_, err = m.Create(CreateParams{Principal: p, Transport: TransportSSE})
	require.NoError(t, err)
	_, err = m.Create(CreateParams{Principal: p, Transport: TransportStreamableHTTP})
	require.ErrorIs(t, err, ErrSessionLimitToken)
	assert.Contains(t, err.Error(), "Token")
}

func TestSessionManager_Kick(t *testing.T) {
	m := NewSessionManager(DefaultConfig())
	s, err := m.Create(CreateParams{Principal: testPrincipal(1, "a"), Transport: TransportStreamableHTTP})
	require.NoError(t, err)

	require.NoError(t, m.Kick(s.ID, "管理员踢线"))
	_, err = m.Get(s.ID)
	require.ErrorIs(t, err, ErrSessionNotFound)
	assert.True(t, s.IsClosed())
	// 踢线后 context 取消
	select {
	case <-s.Context().Done():
	default:
		t.Fatal("踢线后 context 应已取消")
	}
	assert.Empty(t, m.List())
}

func TestSessionManager_IdleTimeout(t *testing.T) {
	m := NewSessionManager(Config{
		IdleTimeout:         50 * time.Millisecond,
		AbsoluteTimeout:     time.Hour,
		MaxGlobalSessions:   10,
		MaxSessionsPerToken: 4,
	})
	base := time.Now()
	m.now = func() time.Time { return base }

	s, err := m.Create(CreateParams{Principal: testPrincipal(1, "a"), Transport: TransportStreamableHTTP})
	require.NoError(t, err)

	// 推进超过空闲
	m.now = func() time.Time { return base.Add(100 * time.Millisecond) }
	m.reapOnce()

	_, err = m.Get(s.ID)
	require.ErrorIs(t, err, ErrSessionNotFound)
}

func TestSessionManager_AbsoluteTimeout(t *testing.T) {
	m := NewSessionManager(Config{
		IdleTimeout:         time.Hour,
		AbsoluteTimeout:     80 * time.Millisecond,
		MaxGlobalSessions:   10,
		MaxSessionsPerToken: 4,
	})
	base := time.Now()
	m.now = func() time.Time { return base }

	s, err := m.Create(CreateParams{Principal: testPrincipal(1, "a"), Transport: TransportStreamableHTTP})
	require.NoError(t, err)
	// 刷新活动也不能逃过绝对超时
	_ = m.Touch(s.ID, "agent_whoami")

	m.now = func() time.Time { return base.Add(100 * time.Millisecond) }
	m.reapOnce()

	_, err = m.Get(s.ID)
	require.ErrorIs(t, err, ErrSessionNotFound)
}

func TestSessionManager_ConcurrentCreate(t *testing.T) {
	m := NewSessionManager(Config{
		IdleTimeout:         time.Hour,
		AbsoluteTimeout:     24 * time.Hour,
		MaxGlobalSessions:   8,
		MaxSessionsPerToken: 8,
	})
	var wg sync.WaitGroup
	var okCount int
	var mu sync.Mutex
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := m.Create(CreateParams{
				Principal: testPrincipal(uint(i%3+1), "c"),
				Transport: TransportStreamableHTTP,
			})
			if err == nil {
				mu.Lock()
				okCount++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	assert.Equal(t, 8, okCount)
	assert.Equal(t, 8, m.Count())
}

func TestSessionManager_TouchLastTool(t *testing.T) {
	m := NewSessionManager(DefaultConfig())
	s, err := m.Create(CreateParams{Principal: testPrincipal(1, "a"), Transport: TransportStreamableHTTP})
	require.NoError(t, err)
	require.NoError(t, m.Touch(s.ID, "instance_start"))
	snap := m.List()[0]
	assert.Equal(t, "instance_start", snap.LastTool)
}
