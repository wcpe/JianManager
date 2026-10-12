package vlsup

// ReapOrphans 回收"上一条 worker 命留下的 vlsup 孤儿"（P0 复盘 2026-10-03 ✓）。
//
// 现场形态：worker 被强杀（SIGKILL）后，其托管的 vlsup 子进程成孤儿、继续占用
// 19461/19462 ✗ ⇒ 新 worker 报 `hot exited immediately after start`，只能靠 RangeClient
// 接旧实例（降级 ✓）。故：**启前回收**必须有界、且**只碰自己的孤儿** ✓
// （**调用点只有一个**：apps/worker/main.go 的启动路径传 who="startup"；`who` 参数保留
// 是为了将来若加关停侧调用能区分来源，但**当前不存在关停侧调用**，不要据此以为已有
// 关停级联的回收骨架——关停路径本身不回收孤儿，孤儿留给下一次启动清理 ✓）。
//
// 判据（**两道都过才回收** ✗✓，防误杀他进程）：
//  ① 进程 cmdline 含**本 data 根**（vlsup 的 cmdline 由本节点的 data 根派生 ⇒ 见 cmdline.go ✓）；
//  ② 进程可执行路径（/proc/<pid>/exe）**落在本 data 根之下**（vlsup 二进制来自本节点资产目录 ✓）。
// 语义自检（不静默 ✓）：回收动作逐个记日志（pid + 匹配到的两道证据 ✓）；不肯定的进程**不动** ✗。
//
// 返回被回收的 pid 列表；err 仅用于"扫描面不可用"（非 Linux / 无 /proc）这类整体性失败 ✓。

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// reapGrace 是单个孤儿的"终止 → 强杀"之间的有界宽限（与关停路径同一骨架 ✓）。
const reapGrace = 5 * time.Second

// ErrReapUnsupported 表示当前平台没有可用的 /proc 扫描面（回收整体不可做 ✓，调用方降级）。
var ErrReapUnsupported = errors.New("vlsup: 孤儿回收需要 /proc 扫描面（当前平台不可用）")

// OrphanMatch 汇总"本节点孤儿"的两道证据（供日志与测试断言 ✓）。
type OrphanMatch struct {
	PID     int
	Cmdline string
	Exe     string
}

// matchesOwnOrphan 是判据本体（纯函数 ⇒ 可被测试直接喂样例 ✓）。
func matchesOwnOrphan(cmdline, exe, dataRoot string) bool {
	if dataRoot == "" || cmdline == "" || exe == "" {
		return false
	}
	root := filepath.Clean(dataRoot)
	// 路径边界（防 "/data/A" 误配 "/data/A-evil" ✗✓）：exe 必须**等于根**或**在根之下** ✓。
	exeClean := filepath.Clean(exe)
	underRoot := exeClean == root || strings.HasPrefix(exeClean, root+string(os.PathSeparator))
	// cmdline 同样按**边界**判（root 后须是分隔符/空白/行尾 ✓，避免同款伪装 ✗）。
	boundary := false
	for i := 0; ; {
		j := strings.Index(cmdline[i:], root)
		if j < 0 {
			break
		}
		end := i + j + len(root)
		if end == len(cmdline) || cmdline[end] == '/' || cmdline[end] == ' ' || cmdline[end] == '=' || cmdline[end] == '\t' {
			boundary = true
			break
		}
		i = end
	}
	return boundary && underRoot
}

// scanOwnOrphans 扫 /proc，返回**两道都过**的进程（顺序稳定 ⇒ 可断言 ✓）。
func scanOwnOrphans(dataRoot string, selfPID int) ([]OrphanMatch, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrReapUnsupported, err)
	}
	var out []OrphanMatch
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == selfPID {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil || len(raw) == 0 {
			continue
		}
		cmdline := strings.Join(strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00"), " ")
		exe, err := os.Readlink(filepath.Join("/proc", e.Name(), "exe"))
		if err != nil {
			continue
		}
		if matchesOwnOrphan(cmdline, exe, dataRoot) {
			out = append(out, OrphanMatch{PID: pid, Cmdline: cmdline, Exe: exe})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out, nil
}

// ReapOrphans 回收本节点的 vlsup 孤儿（启前调用 ✓）；每个孤儿：TERM → 有界等 → 未退则 KILL ✓。
// who 为调用点标识（"startup"/"shutdown" ✓）：本函数据此自报一次**汇总**留痕
// （ACQ-5：旧实现注释声称 who "仅用于日志"，函数体却从未引用它 ⇒ 调用点标识被静默丢弃；
// 逐条留痕仍由调用方的 onReap 负责，这里只补一条不随孤儿数增长、且没有孤儿也打的汇总行）。
func ReapOrphans(dataRoot, who string, onReap func(OrphanMatch)) ([]int, error) {
	if strings.TrimSpace(dataRoot) == "" {
		return nil, fmt.Errorf("vlsup: 孤儿回收需要 data 根（空 ⇒ 拒绝执行，防误杀 ✗）")
	}
	found, err := scanOwnOrphans(dataRoot, os.Getpid())
	if err != nil {
		return nil, err
	}
	var reaped []int
	for _, o := range found {
		proc, err := os.FindProcess(o.PID)
		if err != nil {
			continue
		}
		_ = proc.Signal(syscall.SIGTERM)
		deadline := time.Now().Add(reapGrace)
		for time.Now().Before(deadline) {
			if proc.Signal(syscall.Signal(0)) != nil {
				break // 已退出 ✓
			}
			time.Sleep(20 * time.Millisecond)
		}
		if proc.Signal(syscall.Signal(0)) == nil {
			_ = proc.Signal(syscall.SIGKILL)
		}
		if onReap != nil {
			onReap(o) // 逐条留痕（不静默 ✓）：pid + 两道证据 ✓
		}
		reaped = append(reaped, o.PID)
	}
	// 汇总留痕（ACQ-5）：who 是调用点标识，没有孤儿时也要打 ⇒ 现场能区分"没人可回收"与
	// "没跑过回收"。每条进程的逐条留痕由 onReap 负责（pid + 两道证据 ✓），这里只报计数。
	slog.Info("vlsup 孤儿回收完成",
		"who", who, "dataRoot", dataRoot, "found", len(found), "reaped", len(reaped), "pids", reaped)
	return reaped, nil
}
