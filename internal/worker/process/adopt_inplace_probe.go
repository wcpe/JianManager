package process

import (
	"log/slog"
	"os"
	"path/filepath"
)

// 就地收养的“现探”路径：缓存未命中时现场找一次目录下的运行时进程。
//
// ## 为什么要单独一条路径
// 孤儿扫描（OrphanScanner）是**周期**的（默认 60s 一轮），它把观测结果写进
// `m.foreignRuntimes`，收养从那个缓存读。这在稳态下没问题，但两个时刻缓存必空：
//   - Worker 刚重启，第一轮扫描还没跑；
//   - 实例刚导入，扫描还没轮到它。
//
// 真机踩过：重启 Worker 后立即对 5 台服点收养，全回 `adopted:false / 没有正在运行的进程`，
// 而 `ps` 里 java 一个个都在跑——用户看到的就是“平台说没东西可收养”与事实相反。
//
// 所以收养不能只信缓存：没命中就现探一次。只读（/proc + 目录），不发任何信号，
// 与扫描的观测语义完全一致；选代表的规则也复用同一套（优先像运行时的进程）。

// probeForeignRuntimeOnDemand 现场探测某工作目录下的运行时进程（不走周期缓存）。
//
// @param workDir 实例工作目录（绝对路径）。
// @return 观测到的进程与是否找到；找不到时返回 ok=false（调用方据此报“无可收养”）。
func (m *Manager) probeForeignRuntimeOnDemand(workDir string) (ForeignRuntime, bool) {
	if workDir == "" {
		return ForeignRuntime{}, false
	}
	procs, err := preflightListProcesses()
	if err != nil {
		slog.Warn("就地收养：进程枚举失败，无法现探目录占用", "workDir", workDir, "error", err)
		return ForeignRuntime{}, false
	}

	clean := filepath.Clean(workDir)
	self := os.Getpid()
	// 平台受管进程（本实例或其它实例的 wrapper/java）不算外来，与扫描同口径。
	managed, _ := m.managedInstanceRuntime()

	bestPID := 0
	bestCmdline := ""
	fallbackPID := 0
	fallbackCmdline := ""
	for _, proc := range procs {
		if proc.PID <= 0 || proc.PID == self {
			continue
		}
		if _, isManaged := managed[proc.PID]; isManaged {
			continue
		}
		if !procInWorkDir(proc, clean) {
			continue
		}
		if fallbackPID == 0 || proc.PID < fallbackPID {
			fallbackPID, fallbackCmdline = proc.PID, proc.Cmdline
		}
		if !looksLikeRuntimeCmdline(proc.Cmdline) {
			continue
		}
		if bestPID == 0 || proc.PID < bestPID {
			bestPID, bestCmdline = proc.PID, proc.Cmdline
		}
	}

	if bestPID > 0 {
		return ForeignRuntime{PID: bestPID, Cmdline: truncateCmdline(bestCmdline)}, true
	}
	if fallbackPID > 0 {
		// 目录里有东西在跑但都不像运行时（如 tmux shell 空壳）：仍然返回，
		// 由上层做命令行对照并给出“不像游戏服运行时”的明确理由（而不是笼统说没有）。
		return ForeignRuntime{PID: fallbackPID, Cmdline: truncateCmdline(fallbackCmdline)}, true
	}
	return ForeignRuntime{}, false
}
