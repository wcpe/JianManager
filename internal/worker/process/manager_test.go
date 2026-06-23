package process

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// createDirect 是测试辅助：用 direct 方式创建实例（旧 Create 默认行为）。
func createDirect(m *Manager, uuid, name, cmd, dir string) error {
	return m.Create(uuid, name, cmd, "", dir, nil, false, ProcessTypeDirect, "", "", 0, 0)
}

func TestManager_Create(t *testing.T) {
	m := NewManager(t.TempDir())

	err := createDirect(m, "inst-1", "Test Server", "echo hello", ".")
	assert.NoError(t, err)

	// 重复创建应失败
	err = createDirect(m, "inst-1", "Test Server 2", "echo hello", ".")
	assert.Error(t, err)
}

func TestManager_GetState(t *testing.T) {
	m := NewManager(t.TempDir())
	createDirect(m, "inst-1", "Test", "echo hello", ".")

	state, err := m.GetState("inst-1")
	assert.NoError(t, err)
	assert.Equal(t, StateStopped, state)

	_, err = m.GetState("nonexistent")
	assert.Error(t, err)
}

func TestManager_ListInstances(t *testing.T) {
	m := NewManager(t.TempDir())
	createDirect(m, "inst-1", "Test 1", "echo 1", ".")
	createDirect(m, "inst-2", "Test 2", "echo 2", ".")

	list := m.ListInstances()
	assert.Len(t, list, 2)
	assert.Contains(t, list, "inst-1")
	assert.Contains(t, list, "inst-2")
}

func TestManager_Remove(t *testing.T) {
	m := NewManager(t.TempDir())
	createDirect(m, "inst-1", "Test", "echo hello", ".")

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
		wantSeconds int
	}{
		{1, 1},
		{2, 2},
		{3, 4},
		{4, 8},
		{5, 16},
		{6, 30}, // 上限 30s
		{10, 30},
	}

	for _, tt := range tests {
		delay := backoffDelay(tt.crashCount)
		assert.Equal(t, tt.wantSeconds, int(delay.Seconds()),
			"crashCount=%d should give %ds delay", tt.crashCount, tt.wantSeconds)
	}
}

// TestNewStrategy_Routing 验证 Manager 按 ProcessType 路由到正确策略。
// direct → *directStrategy；daemon → *daemonStrategy；docker → *dockerStrategy（ADR-019）；rcon/未知 → ErrNotImplemented。
func TestNewStrategy_Routing(t *testing.T) {
	m := NewManager(t.TempDir())

	tests := []struct {
		name       string
		pt         ProcessType
		wantErr    bool
		wantDaemon bool
		wantDirect bool
		wantDocker bool
	}{
		{"direct", ProcessTypeDirect, false, false, true, false},
		{"empty defaults direct", "", false, false, true, false},
		{"daemon", ProcessTypeDaemon, false, true, false, false},
		{"docker", ProcessTypeDocker, false, false, false, true},
		{"rcon not implemented", ProcessTypeRCON, true, false, false, false},
		{"unknown not implemented", ProcessType("bogus"), true, false, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := m.newStrategy(CommandSpec{UUID: "x", ProcessType: tt.pt})
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, s)
				return
			}
			assert.NoError(t, err)
			if tt.wantDaemon {
				_, ok := s.(*daemonStrategy)
				assert.True(t, ok, "期望 daemonStrategy")
			}
			if tt.wantDirect {
				_, ok := s.(*directStrategy)
				assert.True(t, ok, "期望 directStrategy")
			}
			if tt.wantDocker {
				_, ok := s.(*dockerStrategy)
				assert.True(t, ok, "期望 dockerStrategy")
			}
			if s != nil {
				_ = s.Close()
			}
		})
	}
}

// TestManager_DockerStartWithoutImageFails docker 策略缺镜像名时启动失败并置 CRASHED。
// docker 模式已落地（ADR-019），但未提供 image 时无法创建容器，按启动失败处理。
// 注：完整的 docker 生命周期（拉镜像/创建/attach/停止）经注入 fake 客户端在 docker_test.go 覆盖。
func TestManager_DockerStartWithoutImageFails(t *testing.T) {
	m := NewManager(t.TempDir())
	err := m.Create("inst-d", "Docker", "echo hi", "", ".", nil, false, ProcessTypeDocker, "", "", 0, 0)
	assert.NoError(t, err)

	err = m.Start("inst-d")
	assert.Error(t, err)

	// 状态应为 CRASHED（缺镜像，启动失败）
	st, _ := m.GetState("inst-d")
	assert.Equal(t, StateCrashed, st)
}

