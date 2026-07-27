package grpc

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	psproc "github.com/shirou/gopsutil/v4/process"

	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

const instanceResourceSnapshotTimeout = 3 * time.Second

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
	snapshotCtx, cancel := context.WithTimeout(ctx, instanceResourceSnapshotTimeout)
	defer cancel()
	return snapshotInstanceProcessTree(snapshotCtx, int32(pid)), nil
}

func unavailableInstanceResourceSnapshot(reason string) *workerpb.GetInstanceResourceSnapshotResponse {
	return &workerpb.GetInstanceResourceSnapshotResponse{UnavailableReason: reason}
}

func snapshotInstanceProcessTree(ctx context.Context, rootPID int32) *workerpb.GetInstanceResourceSnapshotResponse {
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
	response.UnavailableReason = unavailableInstanceResourceReason(response)
	return response
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
