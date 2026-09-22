package grpc

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	psproc "github.com/shirou/gopsutil/v4/process"

	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

const instanceResourceSnapshotTimeout = 3 * time.Second

// instanceWorkDirSizeTimeout 工作目录大小统计的独立预算（FR-467）。
// 数十 GB 目录遍历可达数秒，故不与 3s 的进程快照预算共用——磁盘统计慢不该拖垮进程指标。
//
// N-5 修正：本预算必须在**独立 ctx** 上派生才真正可达。原先 instanceWorkDirBytes 收的是
// 进程快照的 3s 子 ctx（snapshotCtx），10s 的 WithTimeout 只能被父预算截断到 3s——
// 「独立预算」名存实亡，大目录遍历在 3s 被砍掉 → WorkDirAvailable=false →
// CP 磁盘维度整维无值（quota_metric_source.go 只在 WorkDirAvailable 时填 DiskBytes），
// 磁盘配额静默不生效。现在磁盘统计从入站 RPC ctx 派生，只受调用方 deadline 约束。
const instanceWorkDirSizeTimeout = 10 * time.Second

// GetInstanceResourceSnapshot 返回受管根进程及完整子进程树的资源快照（FR-399）。
// 每个资源指标单独标记可用性，避免以零值掩盖进程退出或系统权限限制。
func (s *Server) GetInstanceResourceSnapshot(ctx context.Context, req *workerpb.GetInstanceResourceSnapshotRequest) (*workerpb.GetInstanceResourceSnapshotResponse, error) {
	state, err := s.manager.GetState(req.InstanceUuid)
	if err != nil {
		return nil, fmt.Errorf("实例不存在: %w", err)
	}
	if state != process.StateRunning {
		return unavailableInstanceResourceSnapshot("实例未运行"), nil
	}
	pid := s.manager.GetInstancePID(req.InstanceUuid)
	if pid <= 0 {
		return unavailableInstanceResourceSnapshot("实例根进程不存在"), nil
	}
	snapshotCtx, workDirCtx, cancel := resourceSnapshotContexts(ctx)
	defer cancel()
	// N-5：磁盘统计刻意**不**继承 snapshotCtx 的 3s 预算，而是用 workDirCtx——
	// 父预算截断会让 10s 的磁盘预算实际只剩 3s，与「独立预算」的注释和设计意图不符。
	// workDirCtx 仍派生自入站 ctx，故仍受调用方（CP 侧 quotaDiskTimeout=15s）deadline 约束。
	return snapshotInstanceProcessTree(snapshotCtx, int32(pid), func(_ context.Context) (int64, bool) {
		return s.instanceWorkDirBytes(workDirCtx, req.InstanceUuid)
	}), nil
}

// resourceSnapshotContexts 派生进程快照与磁盘统计两个**互不截断**的 ctx（N-5）。
//
// 存在的意义是把「磁盘统计有独立预算」这条容易写错的性质变成可断言的对象：若磁盘 ctx
// 从进程快照的 3s 子 ctx 派生（原实现经 snapshotInstanceProcessTree 的 wctx 参数如此），
// 10s 的 WithTimeout 会被父预算截断到 ≤3s——大目录遍历被砍 → WorkDirAvailable=false →
// CP 磁盘维度整维无值（quota_metric_source.go 只在 WorkDirAvailable 时填 DiskBytes）。
// 两者都从入站 rpcCtx 派生，因此仍受调用方 deadline 约束，不会无限挂起。
func resourceSnapshotContexts(rpcCtx context.Context) (context.Context, context.Context, context.CancelFunc) {
	// 注意：不要在闭包里引用被 return 赋值的具名返回值（闭包会被写回该名字，形成自调用死循环）。
	processCtx, processCancel := context.WithTimeout(rpcCtx, instanceResourceSnapshotTimeout)
	workDirCtx, workDirCancel := context.WithTimeout(rpcCtx, instanceWorkDirSizeTimeout)
	return processCtx, workDirCtx, func() {
		workDirCancel()
		processCancel()
	}
}

func unavailableInstanceResourceSnapshot(reason string) *workerpb.GetInstanceResourceSnapshotResponse {
	return &workerpb.GetInstanceResourceSnapshotResponse{UnavailableReason: reason}
}

func snapshotInstanceProcessTree(ctx context.Context, rootPID int32, workDirBytes func(context.Context) (int64, bool)) *workerpb.GetInstanceResourceSnapshotResponse {
	root, err := psproc.NewProcessWithContext(ctx, rootPID)
	if err != nil {
		return unavailableInstanceResourceSnapshot("读取实例根进程失败: " + err.Error())
	}
	processes, err := completeProcessTree(ctx, root)
	if err != nil {
		return unavailableInstanceResourceSnapshot("读取实例子进程树失败: " + err.Error())
	}
	response := &workerpb.GetInstanceResourceSnapshotResponse{
		RootPid:      int64(rootPID),
		ProcessCount: int32(len(processes)),
	}
	response.ProcessRssBytes, response.RssAvailable = processTreeRSS(ctx, processes)
	response.CpuPercent, response.CpuAvailable = processTreeCPU(ctx, processes)
	response.UptimeSeconds, response.UptimeAvailable = processUptime(ctx, root)
	if workDirBytes != nil {
		response.WorkDirBytes, response.WorkDirAvailable = workDirBytes(ctx)
	}
	response.UnavailableReason = unavailableInstanceResourceReason(response)
	return response
}

// instanceWorkDirBytes 统计实例工作目录占用（FR-467 运行期配额强制的磁盘维度）。
//
// 与进程指标分开采集：磁盘占用看目录树大小而非进程 RSS。统计失败（实例不存在/目录不可读）
// 时返回 available=false，让 CP 侧明确「磁盘维度不可用」而不是把 0 当成「没占空间」
// ——0 在配额判定里等于未超限，会静默放过超配额的实例。
//
// 超时独立于进程指标（N-5）：调用方必须传入**已由 resourceSnapshotContexts 派生**的磁盘
// 专用 ctx（10s 预算）。此处不再自己叠一层 WithTimeout——父 ctx 若被 3s 进程预算截断，
// 再叠一层也只是把 10s 缩到 3s，同时让「预算到底多少」变得不可断言。
func (s *Server) instanceWorkDirBytes(dirCtx context.Context, instanceUUID string) (int64, bool) {
	inst, ok := s.manager.GetInstance(instanceUUID)
	if !ok || strings.TrimSpace(inst.WorkDir) == "" {
		return 0, false
	}
	if err := dirCtx.Err(); err != nil {
		return 0, false
	}

	var total int64
	// s-5：用 WalkDir（不调用 os.Lstat 之外的额外 stat）并在每项前检查 ctx——
	// 原先的 filepath.Walk 只能靠回调返回错误来中断，而它也只在**进入每个文件时**才有机会
	// 检查，遇到超大目录树时会长时间占住 goroutine，且无法被调用方的超时立即打断。
	// WalkDir 的 DirEntry 已携带类型与大小所需信息，顺带减少一次系统调用。
	err := filepath.WalkDir(inst.WorkDir, func(_ string, entry os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		// 定期检查取消：目录遍历可能持续数十秒，必须在入口处响应 ctx 而不是走完才返回。
		if err := dirCtx.Err(); err != nil {
			return err
		}
		if entry == nil || entry.IsDir() {
			return nil
		}
		// 只累加常规文件大小；符号链接/设备文件不占实际数据块，计入会虚高。
		info, ierr := entry.Info()
		if ierr != nil || info == nil {
			return nil
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, false
	}
	return total, true
}

func completeProcessTree(ctx context.Context, root *psproc.Process) ([]*psproc.Process, error) {
	seen := make(map[int32]struct{})
	processes := make([]*psproc.Process, 0, 4)
	var walk func(*psproc.Process) error
	walk = func(current *psproc.Process) error {
		if current == nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, ok := seen[current.Pid]; ok {
			return nil
		}
		seen[current.Pid] = struct{}{}
		processes = append(processes, current)
		children, err := current.ChildrenWithContext(ctx)
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

func processTreeRSS(ctx context.Context, processes []*psproc.Process) (int64, bool) {
	var total uint64
	for _, current := range processes {
		memory, err := current.MemoryInfoWithContext(ctx)
		if err != nil || memory == nil || math.MaxUint64-total < memory.RSS {
			return 0, false
		}
		total += memory.RSS
	}
	if total > math.MaxInt64 {
		return 0, false
	}
	return int64(total), true
}

func processTreeCPU(ctx context.Context, processes []*psproc.Process) (float64, bool) {
	var total float64
	for _, current := range processes {
		cpuPercent, err := current.CPUPercentWithContext(ctx)
		if err != nil {
			return 0, false
		}
		total += cpuPercent
	}
	return total, true
}

func processUptime(ctx context.Context, root *psproc.Process) (float64, bool) {
	createdAt, err := root.CreateTimeWithContext(ctx)
	if err != nil || createdAt <= 0 {
		return 0, false
	}
	return float64(time.Now().UnixMilli()-createdAt) / 1000, true
}

func unavailableInstanceResourceReason(response *workerpb.GetInstanceResourceSnapshotResponse) string {
	var unavailable []string
	if !response.RssAvailable {
		unavailable = append(unavailable, "进程树 RSS 不可用")
	}
	if !response.CpuAvailable {
		unavailable = append(unavailable, "进程树 CPU 不可用")
	}
	if !response.UptimeAvailable {
		unavailable = append(unavailable, "根进程运行时长不可用")
	}
	return strings.Join(unavailable, "；")
}
