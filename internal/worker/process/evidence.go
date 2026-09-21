package process

import (
	"context"
	"path/filepath"
	"time"

	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// FR-455③ 状态真源收敛：CP 在「心跳清单缺失」时向 Worker 拉取进程侧证据，
// 由「是否真的还在跑」二次确认，避免「面板 STOPPED 而进程在跑」的通道抖动误判。

// evidenceSocketProbeTimeout 单次 daemon socket 探活超时。
const evidenceSocketProbeTimeout = 500 * time.Millisecond

// InstanceEvidence 单个实例的进程侧证据（FR-455③）。
type InstanceEvidence struct {
	UUID string
	// Running 是主判据：进程/容器侧证据是否显示该实例仍在运行。
	Running bool
	// RootPID 证据来源根进程 PID（0=未知）。
	RootPID int
	// ProcessAlive 受管进程（wrapper 或 Java 或 direct 根进程）存活。
	ProcessAlive bool
	// SocketReachable daemon socket 可达（daemon 模式）。
	SocketReachable bool
	// ContainerRunning docker 容器在跑（docker 模式）。
	ContainerRunning bool
	// Detail 证据摘要（供 CP statusReason）。
	Detail string
}

// ProbeInstanceEvidence 返回一批实例的进程侧证据（FR-455③）。
// 对每个实例：优先用内存表策略的实时 PID；内存表无该实例（Worker 曾硬崩）时回退 PID 文件 / 容器名探测。
func (m *Manager) ProbeInstanceEvidence(uuids []string) []InstanceEvidence {
	out := make([]InstanceEvidence, 0, len(uuids))
	for _, uuid := range uuids {
		if uuid == "" {
			continue
		}
		out = append(out, m.probeOneEvidence(uuid))
	}
	return out
}

func (m *Manager) probeOneEvidence(uuid string) InstanceEvidence {
	ev := InstanceEvidence{UUID: uuid}

	m.mu.RLock()
	inst, inTable := m.instances[uuid]
	var processType ProcessType
	var strategy IProcessCommand
	if inTable {
		processType = inst.processType
		strategy = inst.strategy
	}
	pidDir := m.pidDir
	m.mu.RUnlock()

	// 内存表中无该实例时按证据来源推断模式：优先 daemon PID 文件，其次 docker 容器名。
	if !inTable {
		if ev2, ok := m.probeDaemonEvidence(uuid, pidDir); ok {
			return ev2
		}
		running, _ := dockerContainerRunning(context.Background(), containerNamePrefix+uuid)
		ev.ContainerRunning = running
		ev.Running = running
		if running {
			ev.Detail = "容器在跑（内存表无该实例）"
		}
		return ev
	}

	switch processType {
	case ProcessTypeDaemon:
		base := InstanceEvidence{UUID: uuid}
		if strategy != nil {
			if pid := strategy.GetPID(); pid > 0 {
				base.RootPID = pid
				base.ProcessAlive = daemon.IsPIDAlive(pid)
			}
		}
		if base.RootPID <= 0 {
			if daemonEv, ok := m.probeDaemonEvidence(uuid, pidDir); ok {
				base = daemonEv
			}
		}
		base.SocketReachable = probeSocketReachable(daemon.SocketAddr(pidDir, uuid), evidenceSocketProbeTimeout)
		base.Running = base.ProcessAlive || base.SocketReachable
		base.Detail = evidenceDetail(base)
		return base
	case ProcessTypeDocker:
		running, _ := dockerContainerRunning(context.Background(), containerNamePrefix+uuid)
		ev.ContainerRunning = running
		ev.Running = running
		if running {
			ev.Detail = "容器在跑"
		}
		return ev
	default: // direct（或未知）
		if strategy != nil {
			if pid := strategy.GetPID(); pid > 0 {
				ev.RootPID = pid
				ev.ProcessAlive = daemon.IsPIDAlive(pid)
			}
		}
		ev.Running = ev.ProcessAlive
		if ev.ProcessAlive {
			ev.Detail = "direct 根进程存活"
		}
		return ev
	}
}

// probeDaemonEvidence 从 PID 文件推断 daemon 实例进程侧证据。PID 文件不存在返回 ok=false。
func (m *Manager) probeDaemonEvidence(uuid, pidDir string) (InstanceEvidence, bool) {
	pidPath := filepath.Join(pidDir, uuid+".pid")
	rec, err := daemon.NewPIDFile(pidPath).ReadRecord()
	if err != nil {
		return InstanceEvidence{}, false
	}
	ev := InstanceEvidence{UUID: uuid}
	if rec.WrapperPID > 0 && daemon.IsPIDAlive(rec.WrapperPID) {
		ev.ProcessAlive = true
		ev.RootPID = rec.WrapperPID
	} else if rec.JavaPID > 0 && rec.JavaPID != rec.WrapperPID && daemon.IsPIDAlive(rec.JavaPID) {
		// wrapper 死、Java 活：证据仍显示在跑（周期扫描负责处置）。
		ev.ProcessAlive = true
		ev.RootPID = rec.JavaPID
	}
	ev.SocketReachable = probeSocketReachable(daemon.SocketAddr(pidDir, uuid), evidenceSocketProbeTimeout)
	ev.Running = ev.ProcessAlive || ev.SocketReachable
	ev.Detail = evidenceDetail(ev)
	return ev, true
}

func evidenceDetail(ev InstanceEvidence) string {
	switch {
	case ev.ProcessAlive && ev.SocketReachable:
		return "进程存活且 socket 可达"
	case ev.ProcessAlive:
		return "进程存活"
	case ev.SocketReachable:
		return "socket 可达"
	default:
		return "无存活证据"
	}
}

// probeSocketReachable 对 daemon socket 做有界探活（拨通即视为可达）。空地址恒 false。
func probeSocketReachable(addr string, timeout time.Duration) bool {
	if addr == "" {
		return false
	}
	done := make(chan error, 1)
	go func() {
		conn, err := daemon.Dial(addr)
		if err == nil {
			_ = conn.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err == nil
	case <-time.After(timeout):
		return false
	}
}
