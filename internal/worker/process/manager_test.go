package process

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestManager_Create(t *testing.T) {
	m := NewManager("/tmp/test")

	err := m.Create("inst-1", "Test Server", "echo hello", "/tmp", nil, false)
	assert.NoError(t, err)

	// 重复创建应失败
	err = m.Create("inst-1", "Test Server 2", "echo hello", "/tmp", nil, false)
	assert.Error(t, err)
}

func TestManager_GetState(t *testing.T) {
	m := NewManager("/tmp/test")
	m.Create("inst-1", "Test", "echo hello", "/tmp", nil, false)

	state, err := m.GetState("inst-1")
	assert.NoError(t, err)
	assert.Equal(t, StateStopped, state)

	_, err = m.GetState("nonexistent")
	assert.Error(t, err)
}

func TestManager_ListInstances(t *testing.T) {
	m := NewManager("/tmp/test")
	m.Create("inst-1", "Test 1", "echo 1", "/tmp", nil, false)
	m.Create("inst-2", "Test 2", "echo 2", "/tmp", nil, false)

	list := m.ListInstances()
	assert.Len(t, list, 2)
	assert.Contains(t, list, "inst-1")
	assert.Contains(t, list, "inst-2")
}

func TestManager_Remove(t *testing.T) {
	m := NewManager("/tmp/test")
	m.Create("inst-1", "Test", "echo hello", "/tmp", nil, false)

	err := m.Remove("inst-1")
	assert.NoError(t, err)

	_, err = m.GetState("inst-1")
	assert.Error(t, err)

	// 移除不存在的实例不报错
	err = m.Remove("nonexistent")
	assert.NoError(t, err)
}

func TestBackoffDelay(t *testing.T) {
	tests := []struct {
		crashCount int
		minSeconds int
		maxSeconds int
	}{
		{1, 1, 1},
		{2, 2, 2},
		{3, 4, 4},
		{4, 8, 8},
		{5, 16, 16},
		{6, 30, 30}, // 上限 30s
		{10, 30, 30},
	}

	for _, tt := range tests {
		delay := backoffDelay(tt.crashCount)
		assert.Equal(t, tt.minSeconds, int(delay.Seconds()),
			"crashCount=%d should give %ds delay", tt.crashCount, tt.minSeconds)
	}
}
