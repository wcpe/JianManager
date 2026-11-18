package grpc

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	psproc "github.com/shirou/gopsutil/v4/process"
)

// 进程树的「读取」与「判断」分层。
//
// 为什么分开：选游戏进程、收集整棵树、聚合 RSS/CPU 这些是纯算法，与操作系统无关；
// 而能否枚举到子进程、能否读出 java.exe 这个名字，取决于平台的系统调用。
// 两者混在一个函数里时，任何一次进程启动抖动都会让算法断言一起变红，
// 失败信息也分不清是「判断错了」还是「系统没读出来」。
//
// 因此：procNode 只声明读取能力，纯算法（*In 系列）只接收 procNode；
// 面向 gopsutil 的入口函数保留原签名，内部包一层 gopsutilNode 后交给纯算法。
// 生产路径的系统调用顺序与拆分前完全一致。

// procNode 是进程树遍历与聚合所需的最小只读能力。
// 生产实现是 gopsutilNode；测试可注入内存树，不启动任何真实进程。
type procNode interface {
	// PID 返回进程号。
	PID() int32
	// Name 返回进程名（Windows 上含 .exe）。
	Name(ctx context.Context) (string, error)
	// Children 返回直接子进程。
	Children(ctx context.Context) ([]procNode, error)
	// RSS 返回常驻内存字节数。
	RSS(ctx context.Context) (uint64, error)
	// CPUPercent 返回累计 CPU 百分比。
	CPUPercent(ctx context.Context) (float64, error)
	// CreatedAtUnixMs 返回进程创建时刻（毫秒）。
	CreatedAtUnixMs(ctx context.Context) (int64, error)
}

// gopsutilNode 把 *psproc.Process 适配为 procNode，不做任何额外缓存或改序。
type gopsutilNode struct {
	proc *psproc.Process
}

func (n *gopsutilNode) PID() int32 { return n.proc.Pid }

func (n *gopsutilNode) Name(ctx context.Context) (string, error) {
	return n.proc.NameWithContext(ctx)
}

func (n *gopsutilNode) Children(ctx context.Context) ([]procNode, error) {
	children, err := n.proc.ChildrenWithContext(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]procNode, 0, len(children))
	for _, child := range children {
		out = append(out, &gopsutilNode{proc: child})
	}
	return out, nil
}

func (n *gopsutilNode) RSS(ctx context.Context) (uint64, error) {
	memory, err := n.proc.MemoryInfoWithContext(ctx)
	if err != nil || memory == nil {
		return 0, fmt.Errorf("读取进程 %d 内存信息失败: %w", n.proc.Pid, err)
	}
	return memory.RSS, nil
}

func (n *gopsutilNode) CPUPercent(ctx context.Context) (float64, error) {
	return n.proc.CPUPercentWithContext(ctx)
}

func (n *gopsutilNode) CreatedAtUnixMs(ctx context.Context) (int64, error) {
	return n.proc.CreateTimeWithContext(ctx)
}

// asProcess 把 procNode 还原为 gopsutil 句柄；非本包实现返回 nil。
func asProcess(node procNode) *psproc.Process {
	if adapter, ok := node.(*gopsutilNode); ok {
		return adapter.proc
	}
	return nil
}

// procTreeMaxNodes 是受管实例进程树的规模上限。
// 受管实例树很小（wrapper/sh/java + 少量子工具），64 足够，同时防环。
const procTreeMaxNodes = 64

// ├─ 纯算法：只依赖 procNode，可用内存树验证 ├─

// resolveGameProcIn 在给定进程树中选择游戏进程。
// 规则与拆分前一致：优先最深层的 java；无 java 命名者回退最深后代；无后代回退根。
func resolveGameProcIn(ctx context.Context, root procNode) procNode {
	if root == nil {
		return nil
	}
	deepest, deepestDepth := root, -1
	var javaDeepest procNode
	javaDepth := -1
	seen := map[int32]struct{}{}
	var walk func(node procNode, depth int)
	walk = func(node procNode, depth int) {
		if node == nil || len(seen) >= procTreeMaxNodes {
			return
		}
		if _, ok := seen[node.PID()]; ok {
			return
		}
		seen[node.PID()] = struct{}{}
		if depth > deepestDepth {
			deepestDepth, deepest = depth, node
		}
		if name, err := node.Name(ctx); err == nil {
			if n := strings.ToLower(name); n == "java" || n == "java.exe" {
				if depth > javaDepth {
					javaDepth, javaDeepest = depth, node
				}
			}
		}
		children, err := node.Children(ctx)
		if err != nil {
			return
		}
		for _, child := range children {
			walk(child, depth+1)
		}
	}
	walk(root, 0)
	if javaDeepest != nil {
		return javaDeepest
	}
	return deepest
}

// collectProcessTreeIn 深度优先收集整棵进程树（含根），按 PID 去重并受规模上限保护。
func collectProcessTreeIn(ctx context.Context, root procNode) ([]procNode, error) {
	seen := make(map[int32]struct{})
	processes := make([]procNode, 0, 4)
	var walk func(procNode) error
	walk = func(current procNode) error {
		if current == nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, ok := seen[current.PID()]; ok {
			return nil
		}
		seen[current.PID()] = struct{}{}
		processes = append(processes, current)
		children, err := current.Children(ctx)
		if err != nil {
			return err
		}
		for _, child := range children {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root); err != nil {
		return nil, err
	}
	return processes, nil
}

// sumTreeRSSIn 汇总整树 RSS；任一项读取失败或有溢出风险即整体不可用。
func sumTreeRSSIn(ctx context.Context, processes []procNode) (int64, bool) {
	var total uint64
	for _, current := range processes {
		rss, err := current.RSS(ctx)
		if err != nil || math.MaxUint64-total < rss {
			return 0, false
		}
		total += rss
	}
	if total > math.MaxInt64 {
		return 0, false
	}
	return int64(total), true
}

// sumTreeCPUIn 汇总整树 CPU 百分比；任一项读取失败即整体不可用。
func sumTreeCPUIn(ctx context.Context, processes []procNode) (float64, bool) {
	var total float64
	for _, current := range processes {
		cpuPercent, err := current.CPUPercent(ctx)
		if err != nil {
			return 0, false
		}
		total += cpuPercent
	}
	return total, true
}

// uptimeIn 由根进程创建时刻推算运行时长（秒）。
func uptimeIn(ctx context.Context, root procNode) (float64, bool) {
	createdAt, err := root.CreatedAtUnixMs(ctx)
	if err != nil || createdAt <= 0 {
		return 0, false
	}
	return float64(time.Now().UnixMilli()-createdAt) / 1000, true
}
