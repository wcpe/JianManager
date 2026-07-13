package process

import (
	"log/slog"

	"github.com/shirou/gopsutil/v4/mem"

	"github.com/wcpe/JianManager/internal/platform/memguard"
)

// MemGuardConfig Worker 实时内存闸配置（FR-317，worker.yml memory_guard 段）。
type MemGuardConfig struct {
	// ReserveMB 保留水位（MB）；0 = 默认策略 max(512MB, 总内存 10%)。
	ReserveMB int64
	// Disabled 显式关闭守卫（应急逃生口，如误判阻塞关键启动时）。
	Disabled bool
}

// readSysMem 读系统内存（可用/总量，MB）；测试经 SetMemReader 注入假读数。
type readSysMem func() (availMB, totalMB int64, err error)

func realReadSysMem() (int64, int64, error) {
	vm, err := mem.VirtualMemory()
	if err != nil {
		return 0, 0, err
	}
	return int64(vm.Available / 1024 / 1024), int64(vm.Total / 1024 / 1024), nil
}

// SetMemGuard 注入内存闸配置（main 从 worker.yml 接线）。
func (m *Manager) SetMemGuard(cfg MemGuardConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.memGuard = cfg
}

// SetMemReader 注入系统内存读数器（仅测试）。
func (m *Manager) SetMemReader(r readSysMem) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readMem = r
}

// preflightMemory 启动前实时内存闸（FR-317 Worker 侧，最后防线）：
// 可用内存 − 实例预估需求 < 保留水位 即拒绝启动，防止把节点内存跑满至失去响应
// （真机事故：Paper -Xmx2048M 把主机 OOM 至 SSH/面板全失联）。
// 读数失败放行（fail-open）：内存读数故障不应瘫痪全部启动能力，仅告警留痕。
func (m *Manager) preflightMemory(inst *Instance) error {
	if m.memGuard.Disabled {
		return nil
	}
	reader := m.readMem
	if reader == nil {
		reader = realReadSysMem
	}
	availMB, totalMB, err := reader()
	if err != nil {
		slog.Warn("内存水位读数失败，跳过启动内存闸（fail-open）", "error", err)
		return nil
	}
	required := memguard.EstimateStartMB(inst.StartCommand, inst.MemLimitMB)
	reserve := m.memGuard.ReserveMB
	if reserve <= 0 {
		reserve = memguard.DefaultReserveMB(totalMB)
	}
	if err := memguard.Check(availMB, required, reserve); err != nil {
		slog.Warn("启动被内存闸拒绝", "instance", inst.Name, "availMB", availMB, "requiredMB", required, "reserveMB", reserve)
		return err
	}
	return nil
}
