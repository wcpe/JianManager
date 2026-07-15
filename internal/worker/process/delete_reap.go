package process

import (
	"log/slog"
	"os"

	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// ReapDaemonForDelete 在删除实例前强杀该实例的 daemon 进程树并清理遗留的 PID 文件 / socket（FR-310）。
//
// 背景（真机缺陷）：删除运行中 daemon 实例时，删除流程只杀 wrapper 组随即 RemoveAll 工作目录。
// Unix 上被托管的 Java 经 wrapper 的 applyProcAttr 自成进程组（见 daemon.applyProcAttr），杀 wrapper
// 组够不到它；未死透的 Java 在 RemoveAll 之后继续写 world region 文件，把工作目录重建出来——于是
// worker 日志打印「已清理工作目录」（RemoveAll 返回 nil），盘上却残留 world/plugins。此外 wrapper 被
// 强杀时其 defer cleanupPIDFile 不执行，遗留 <uuid>.pid 与 <uuid>.sock（本机 var/servers 下）。
//
// 处置：按 PID 记录强杀 wrapper 与 Java 两棵进程树——Unix 各杀其组，故两棵都要杀；Windows taskkill /T
// 已覆盖子树，补杀已死 PID 无害——轮询确认死透后再由调用方 RemoveAll，杜绝存活进程回写；确认死透即
// 清理遗留的 PID 文件与 socket。与 FR-325 接管兜底 reapOrphanWrapper 同源（复用 killTree /
// waitPIDsGone / pidAlive 桩，便于确定性单测）。
//
// 无 PID 文件（非 daemon 实例，或 wrapper 已优雅退出自清理）时为空操作。强杀后仍有存活（权限不足等）
// 则保留 PID 文件，让后续 Worker 重启的接管扫描 RecoverDaemonInstances 仍能发现并再次兜底，杜绝孤儿
// 永久失联——此时工作目录可能残留，由调用方 removeInstanceWorkDir 的校验重试如实反映。
func (m *Manager) ReapDaemonForDelete(uuid string) {
	pidPath := daemon.PIDFileName(m.pidDir, uuid)
	rec, err := daemon.NewPIDFile(pidPath).ReadRecord()
	if err != nil {
		return // 无 PID 文件：非 daemon 或已自清理，无需强杀 / 清理
	}

	slog.Info("删除实例：强杀 daemon 进程树以防存活 Java 回写工作目录",
		"instanceId", uuid, "wrapperPid", rec.WrapperPID, "javaPid", rec.JavaPID)

	if rec.WrapperPID > 0 {
		if err := m.killTree(rec.WrapperPID); err != nil {
			slog.Warn("删除实例：强杀 wrapper 进程树报错（可能已死或权限不足，以存活复核为准）",
				"instanceId", uuid, "wrapperPid", rec.WrapperPID, "error", err)
		}
	}
	// Unix 上 Java 自成进程组，杀 wrapper 组够不到它，必须按 Java PID 补杀其树。
	if rec.JavaPID > 0 && rec.JavaPID != rec.WrapperPID {
		if err := m.killTree(rec.JavaPID); err != nil {
			slog.Warn("删除实例：强杀 Java 进程树报错（可能已死或权限不足，以存活复核为准）",
				"instanceId", uuid, "javaPid", rec.JavaPID, "error", err)
		}
	}

	if !m.waitPIDsGone([]int{rec.WrapperPID, rec.JavaPID}) {
		slog.Warn("删除实例：daemon 进程树强杀后仍有存活，保留 PID 文件待接管扫描再兜底",
			"instanceId", uuid, "wrapperPid", rec.WrapperPID, "javaPid", rec.JavaPID)
		return
	}

	_ = os.Remove(pidPath)
	if rec.SocketAddr != "" {
		daemon.RemoveSocket(rec.SocketAddr)
	} else {
		daemon.RemoveSocket(daemon.SocketAddr(m.pidDir, uuid))
	}
	slog.Info("删除实例：daemon 进程树已强杀并清理 PID 文件 / socket", "instanceId", uuid)
}
