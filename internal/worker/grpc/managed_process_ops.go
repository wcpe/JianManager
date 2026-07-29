package grpc

import (
	"context"
	"errors"
	"math"
	"os"
	"runtime"
	"strings"
	"time"

	psproc "github.com/shirou/gopsutil/v4/process"

	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

const managedProcessTimeout = 3 * time.Second

// InspectManagedProcess 只读探查某实例受管进程树内的目标 PID。
func (s *Server) InspectManagedProcess(ctx context.Context, req *workerpb.ManagedProcessInspectRequest) (*workerpb.ManagedProcessInspectResponse, error) {
	view, resp := s.managedProcessView(ctx, req.InstanceUuid, req.Pid)
	if resp != nil {
		return resp, nil
	}
	defer view.cancel()
	sampledAt := time.Now().UnixMilli()
	return &workerpb.ManagedProcessInspectResponse{
		Success:         true,
		RootPid:         int64(view.rootPID),
		Target:          managedProcessInfo(view.ctx, view.target, view.rootPID, sampledAt),
		Children:        managedProcessInfos(view.ctx, view.targetChildren, view.rootPID, sampledAt),
		Ancestors:       managedProcessInfos(view.ctx, view.ancestors, view.rootPID, sampledAt),
		SampledAtUnixMs: sampledAt,
	}, nil
}

// TerminateManagedProcess 处置某实例受管进程树内的非根目标 PID。
func (s *Server) TerminateManagedProcess(ctx context.Context, req *workerpb.ManagedProcessActionRequest) (*workerpb.ManagedProcessActionResponse, error) {
	view, resp := s.managedProcessView(ctx, req.InstanceUuid, req.Pid)
	if resp != nil {
		return processActionFromInspect(resp), nil
	}
	defer view.cancel()
	mode := strings.TrimSpace(req.Mode)
	if req.Pid == view.rootPID {
		return processActionFailure("ROOT_PROCESS_ACTION_DENIED", "实例根进程须通过实例停止/强杀处置"), nil
	}
	affected, err := applyManagedProcessAction(view.target, view.targetChildren, mode)
	if err != nil {
		return processActionErrorResponse(err), nil
	}
	return &workerpb.ManagedProcessActionResponse{
		Success:       true,
		Message:       managedProcessActionMessage(mode, affected),
		AffectedCount: int32(len(affected)),
		Pid:           req.Pid,
		Mode:          mode,
		AffectedPids:  affected,
	}, nil
}

type managedProcessView struct {
	ctx            context.Context
	cancel         context.CancelFunc
	rootPID        int32
	target         *psproc.Process
	ancestors      []*psproc.Process
	targetChildren []*psproc.Process
}

func (s *Server) managedProcessView(ctx context.Context, instanceUUID string, pid int32) (*managedProcessView, *workerpb.ManagedProcessInspectResponse) {
	if pid <= 0 || strings.TrimSpace(instanceUUID) == "" {
		return nil, managedProcessInspectFailure("INVALID_REQUEST", "instanceUuid 与 pid 必填")
	}
	state, err := s.manager.GetState(instanceUUID)
	if err != nil {
		return nil, managedProcessInspectFailure("INSTANCE_NOT_FOUND", err.Error())
	}
	if state != process.StateRunning {
		return nil, managedProcessInspectFailure("INSTANCE_NOT_RUNNING", "实例未运行")
	}
	return s.managedRunningProcessView(ctx, instanceUUID, pid)
}

func (s *Server) managedRunningProcessView(ctx context.Context, instanceUUID string, pid int32) (*managedProcessView, *workerpb.ManagedProcessInspectResponse) {
	rootPID := int32(s.manager.GetInstancePID(instanceUUID))
	if rootPID <= 0 {
		return nil, managedProcessInspectFailure("INSTANCE_NOT_RUNNING", "实例根进程不存在")
	}
	treeCtx, cancel := context.WithTimeout(ctx, managedProcessTimeout)
	processes, resp := loadManagedProcessTree(treeCtx, rootPID)
	if resp != nil {
		cancel()
		return nil, resp
	}
	target := processByPID(processes, pid)
	if target == nil {
		cancel()
		return nil, managedProcessInspectFailure("PID_NOT_MANAGED", "PID 不属于该实例受管进程树")
	}
	targetTree, err := completeProcessTree(treeCtx, target)
	if err != nil {
		cancel()
		return nil, managedProcessInspectFailure("PROCESS_TREE_UNAVAILABLE", "读取目标进程树失败: "+err.Error())
	}
	return &managedProcessView{ctx: treeCtx, cancel: cancel, rootPID: rootPID, target: target, ancestors: ancestorProcesses(treeCtx, target, rootPID, processes), targetChildren: targetTree[1:]}, nil
}

func loadManagedProcessTree(ctx context.Context, rootPID int32) ([]*psproc.Process, *workerpb.ManagedProcessInspectResponse) {
	root, err := psproc.NewProcessWithContext(ctx, rootPID)
	if err != nil {
		return nil, managedProcessInspectFailure("PROCESS_TREE_UNAVAILABLE", "读取实例根进程失败: "+err.Error())
	}
	processes, err := completeProcessTree(ctx, root)
	if err != nil {
		return nil, managedProcessInspectFailure("PROCESS_TREE_UNAVAILABLE", "读取实例进程树失败: "+err.Error())
	}
	return processes, nil
}

func ancestorProcesses(ctx context.Context, target *psproc.Process, rootPID int32, processes []*psproc.Process) []*psproc.Process {
	byPID := processMap(processes)
	out := make([]*psproc.Process, 0)
	for current := target; current != nil && current.Pid != rootPID; {
		ppid, err := current.PpidWithContext(ctx)
		if err != nil {
			break
		}
		parent := byPID[ppid]
		if parent == nil {
			break
		}
		out = append(out, parent)
		current = parent
	}
	reverseProcesses(out)
	return out
}

func processMap(processes []*psproc.Process) map[int32]*psproc.Process {
	out := make(map[int32]*psproc.Process, len(processes))
	for _, current := range processes {
		if current != nil {
			out[current.Pid] = current
		}
	}
	return out
}

func reverseProcesses(processes []*psproc.Process) {
	for i, j := 0, len(processes)-1; i < j; i, j = i+1, j-1 {
		processes[i], processes[j] = processes[j], processes[i]
	}
}

func managedProcessInspectFailure(code, message string) *workerpb.ManagedProcessInspectResponse {
	return &workerpb.ManagedProcessInspectResponse{Success: false, Code: code, Message: message}
}

func processActionFromInspect(resp *workerpb.ManagedProcessInspectResponse) *workerpb.ManagedProcessActionResponse {
	return processActionFailure(resp.Code, resp.Message)
}

func processActionFailure(code, message string) *workerpb.ManagedProcessActionResponse {
	return &workerpb.ManagedProcessActionResponse{Success: false, Code: code, Message: message}
}

func processByPID(processes []*psproc.Process, pid int32) *psproc.Process {
	for _, current := range processes {
		if current != nil && current.Pid == pid {
			return current
		}
	}
	return nil
}

func managedProcessInfos(ctx context.Context, processes []*psproc.Process, rootPID int32, sampledAt int64) []*workerpb.ManagedProcessInfo {
	out := make([]*workerpb.ManagedProcessInfo, 0, len(processes))
	for _, current := range processes {
		out = append(out, managedProcessInfo(ctx, current, rootPID, sampledAt))
	}
	return out
}

func managedProcessInfo(ctx context.Context, current *psproc.Process, rootPID int32, sampledAt int64) *workerpb.ManagedProcessInfo {
	info := &workerpb.ManagedProcessInfo{Pid: current.Pid, IsRoot: current.Pid == rootPID, SampledAtUnixMs: sampledAt}
	fillManagedProcessIdentity(ctx, current, info)
	fillManagedProcessMetrics(ctx, current, info)
	return info
}

func fillManagedProcessIdentity(ctx context.Context, current *psproc.Process, info *workerpb.ManagedProcessInfo) {
	if ppid, err := current.PpidWithContext(ctx); err == nil {
		info.Ppid = ppid
	}
	if name, err := current.NameWithContext(ctx); err == nil {
		info.Name = name
	}
	if user, err := current.UsernameWithContext(ctx); err == nil {
		info.User = user
	}
	if cmdline, err := current.CmdlineWithContext(ctx); err == nil {
		info.CommandSummary = truncateCommandSummary(cmdline)
	}
}

func fillManagedProcessMetrics(ctx context.Context, current *psproc.Process, info *workerpb.ManagedProcessInfo) {
	if memory, err := current.MemoryInfoWithContext(ctx); err == nil && memory != nil && memory.RSS <= math.MaxInt64 {
		info.RssBytes = int64(memory.RSS)
	}
	if cpu, err := current.CPUPercentWithContext(ctx); err == nil {
		info.CpuPercent = cpu
	}
	if createdAt, err := current.CreateTimeWithContext(ctx); err == nil {
		info.StartedAtUnixMs = createdAt
		info.UptimeSeconds = time.Since(time.UnixMilli(createdAt)).Seconds()
	}
	if threads, err := current.NumThreadsWithContext(ctx); err == nil {
		info.ThreadCount = threads
	}
}

func truncateCommandSummary(command string) string {
	command = strings.TrimSpace(maskSensitiveCommandArgs(command))
	if len(command) <= 240 {
		return command
	}
	return command[:240]
}

func maskSensitiveCommandArgs(command string) string {
	parts := strings.Fields(command)
	for i := 0; i < len(parts); i++ {
		key, inline := commandArgKey(parts[i])
		if !isSensitiveCommandArg(key) {
			continue
		}
		if inline {
			parts[i] = maskInlineCommandArg(parts[i])
		} else if i+1 < len(parts) {
			parts[i+1] = "******"
		}
	}
	return strings.Join(parts, " ")
}

func commandArgKey(part string) (string, bool) {
	trimmed := strings.TrimLeft(part, "-/")
	if idx := strings.IndexAny(trimmed, "=:"); idx >= 0 {
		return trimmed[:idx], true
	}
	return trimmed, false
}

func maskInlineCommandArg(part string) string {
	if idx := strings.IndexAny(part, "=:"); idx >= 0 {
		return part[:idx+1] + "******"
	}
	return "******"
}

func isSensitiveCommandArg(key string) bool {
	key = strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(key))
	for _, fragment := range []string{"password", "passwd", "pwd", "token", "secret", "apikey", "accesskey", "secretkey", "authorization"} {
		if strings.Contains(key, fragment) {
			return true
		}
	}
	return false
}

type managedProcessActionError struct {
	code string
	err  error
}

func (e managedProcessActionError) Error() string { return e.err.Error() }

func applyManagedProcessAction(target *psproc.Process, children []*psproc.Process, mode string) ([]int32, error) {
	switch mode {
	case "terminate":
		return signalOneProcess(target.Pid)
	case "kill_tree":
		return killProcessList(append(append([]*psproc.Process(nil), children...), target))
	default:
		return nil, managedActionError("INVALID_REQUEST", "不支持的进程处置模式")
	}
}

func managedActionError(code, message string) error {
	return managedProcessActionError{code: code, err: errors.New(message)}
}

func processActionErrorResponse(err error) *workerpb.ManagedProcessActionResponse {
	var actionErr managedProcessActionError
	if errors.As(err, &actionErr) {
		return processActionFailure(actionErr.code, actionErr.Error())
	}
	return processActionFailure("PROCESS_ACTION_FAILED", err.Error())
}

func managedProcessActionMessage(mode string, affected []int32) string {
	if mode == "terminate" {
		return "已提交温和终止"
	}
	return "已强制终止受管进程树"
}

func killProcessList(processes []*psproc.Process) ([]int32, error) {
	affected := make([]int32, 0, len(processes))
	for _, current := range processes {
		if current == nil {
			continue
		}
		if err := killOneProcess(current.Pid); err != nil {
			return affected, err
		}
		affected = append(affected, current.Pid)
	}
	return affected, nil
}

func signalOneProcess(pid int32) ([]int32, error) {
	if runtime.GOOS == "windows" {
		return nil, managedActionError("UNSUPPORTED", "当前平台无法提供可靠的温和终止语义")
	}
	proc, err := os.FindProcess(int(pid))
	if err != nil {
		return nil, err
	}
	if err := proc.Signal(os.Interrupt); err != nil {
		return nil, err
	}
	return []int32{pid}, nil
}

func killOneProcess(pid int32) error {
	proc, err := os.FindProcess(int(pid))
	if err != nil {
		return err
	}
	return proc.Kill()
}
