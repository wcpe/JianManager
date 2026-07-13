package process

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestStart_MemGuardRejects 可用内存塞不下待启实例时 Start 被拒、状态保持 STOPPED（FR-317）。
func TestStart_MemGuardRejects(t *testing.T) {
	m := NewManager(t.TempDir())
	// 8G 主机、可用 2G；实例 -Xmx2G 估算 2611MB > 可用-保留 → 拒
	m.SetMemReader(func() (int64, int64, error) { return 2048, 8192, nil })
	assert.NoError(t, createDirect(m, "inst-1", "big", "java -Xmx2G -jar server.jar nogui", "."))

	err := m.Start("inst-1")
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "内存不足")
		assert.Contains(t, err.Error(), "拒绝启动")
	}
	state, _ := m.GetState("inst-1")
	assert.Equal(t, StateStopped, state, "被闸拒绝应保持原状态")
}

// TestStart_MemGuardConfig 自定义保留水位与显式关闭（FR-317）。
func TestStart_MemGuardConfig(t *testing.T) {
	m := NewManager(t.TempDir())
	m.SetMemReader(func() (int64, int64, error) { return 3000, 8192, nil })
	assert.NoError(t, createDirect(m, "inst-1", "srv", "java -Xmx2G -jar s.jar", "."))
	// 默认保留 max(512, 819)=819：3000-2611=389 < 819 → 拒
	err := m.Start("inst-1")
	assert.Error(t, err)

	// 显式压低保留水位到 256 → 3000-2611=389 ≥ 256 → 过闸（后续真启动会因 java 缺失等报错，
	// 只断言错误不再是内存闸文案）
	m.SetMemGuard(MemGuardConfig{ReserveMB: 256})
	if err := m.Start("inst-1"); err != nil {
		assert.NotContains(t, err.Error(), "内存不足")
	}
}

// TestStart_MemGuardFailOpen 读数失败不拦启动（fail-open），禁用同理（FR-317）。
func TestStart_MemGuardFailOpen(t *testing.T) {
	m := NewManager(t.TempDir())
	m.SetMemReader(func() (int64, int64, error) { return 0, 0, errors.New("psutil broken") })
	assert.NoError(t, createDirect(m, "inst-1", "srv", "java -Xmx64G -jar s.jar", "."))
	if err := m.Start("inst-1"); err != nil {
		assert.NotContains(t, err.Error(), "内存不足", "读数失败应放行内存闸")
	}

	m2 := NewManager(t.TempDir())
	m2.SetMemReader(func() (int64, int64, error) { return 100, 8192, nil })
	m2.SetMemGuard(MemGuardConfig{Disabled: true})
	assert.NoError(t, createDirect(m2, "inst-2", "srv", "java -Xmx64G -jar s.jar", "."))
	if err := m2.Start("inst-2"); err != nil {
		assert.False(t, strings.Contains(err.Error(), "内存不足"), "禁用后不应有内存闸错误")
	}
}
