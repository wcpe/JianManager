package heartbeat

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	gopsprocess "github.com/shirou/gopsutil/v4/process"

	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

const (
	processMetricLimit        = 10
	processMetricTimeout      = 3 * time.Second
	processCommandSummarySize = 120
	processTreeLimit          = 128
)

type processIOState struct {
	readBytes  uint64
	writeBytes uint64
	sampledAt  int64
}

var (
	processIOMu    sync.Mutex
	processIOCache = map[string]processIOState{}
)

func collectProcessMetrics(snaps []process.InstanceSnapshot) []*workerpb.ProcessMetricSample {
	ctx, cancel := context.WithTimeout(context.Background(), processMetricTimeout)
	defer cancel()

	var out []*workerpb.ProcessMetricSample
	activeKeys := map[string]struct{}{}
	for _, snap := range snaps {
		if snap.State != string(process.StateRunning) || snap.PID <= 0 {
			continue
		}
		sampledAt := time.Now().UnixMilli()
		samples := sampleInstanceProcesses(ctx, snap, sampledAt)
		for _, sm := range samples {
			activeKeys[processMetricKey(sm.InstanceUuid, sm.Pid)] = struct{}{}
		}
		out = append(out, topProcessSamples(samples, processMetricLimit)...)
	}
	pruneProcessIOCache(activeKeys)
	return out
}

func sampleInstanceProcesses(ctx context.Context, snap process.InstanceSnapshot, sampledAt int64) []*workerpb.ProcessMetricSample {
	root, err := gopsprocess.NewProcessWithContext(ctx, int32(snap.PID))
	if err != nil {
		return nil
	}
	procs := processTree(ctx, root, processTreeLimit)
	out := make([]*workerpb.ProcessMetricSample, 0, len(procs))
	for _, p := range procs {
		if sm := sampleProcess(ctx, snap.UUID, p, sampledAt); sm != nil {
			out = append(out, sm)
		}
	}
	return out
}

func processTree(ctx context.Context, root *gopsprocess.Process, limit int) []*gopsprocess.Process {
	if root == nil || limit <= 0 {
		return nil
	}
	seen := map[int32]struct{}{}
	var out []*gopsprocess.Process
	var walk func(*gopsprocess.Process)
	walk = func(p *gopsprocess.Process) {
		if p == nil || len(out) >= limit {
			return
		}
		if _, ok := seen[p.Pid]; ok {
			return
		}
		seen[p.Pid] = struct{}{}
		out = append(out, p)
		children, err := p.ChildrenWithContext(ctx)
		if err != nil {
			return
		}
		for _, child := range children {
			walk(child)
		}
	}
	walk(root)
	return out
}

func sampleProcess(ctx context.Context, instanceUUID string, p *gopsprocess.Process, sampledAt int64) *workerpb.ProcessMetricSample {
	name := optionalMetricValue(p.NameWithContext(ctx))
	cpuPercent := optionalMetricValue(p.CPUPercentWithContext(ctx))
	mem := optionalMetricValue(p.MemoryInfoWithContext(ctx))
	user := optionalMetricValue(p.UsernameWithContext(ctx))
	args := optionalMetricValue(p.CmdlineSliceWithContext(ctx))
	rss := uint64(0)
	if mem != nil {
		rss = mem.RSS
	}
	if name == "" && len(args) == 0 && rss == 0 && cpuPercent == 0 {
		return nil
	}
	readBps, writeBps := sampleProcessIO(ctx, instanceUUID, p, sampledAt)
	return &workerpb.ProcessMetricSample{
		InstanceUuid:     instanceUUID,
		Pid:              p.Pid,
		Name:             name,
		CpuPercent:       cpuPercent,
		RssBytes:         rss,
		User:             user,
		CommandSummary:   sanitizeCommandSummary(args),
		SampledAtUnixMs:  sampledAt,
		ReadBytesPerSec:  readBps,
		WriteBytesPerSec: writeBps,
	}
}

// optionalMetricValue 把单项采样失败视为缺测，避免权限或进程退出中断其他指标采集。
func optionalMetricValue[T any](value T, err error) T {
	if err == nil {
		return value
	}
	var zero T
	return zero
}

func topProcessSamples(samples []*workerpb.ProcessMetricSample, limit int) []*workerpb.ProcessMetricSample {
	if len(samples) == 0 {
		return nil
	}
	sort.SliceStable(samples, func(i, j int) bool {
		if samples[i].CpuPercent == samples[j].CpuPercent {
			return samples[i].RssBytes > samples[j].RssBytes
		}
		return samples[i].CpuPercent > samples[j].CpuPercent
	})
	if limit > 0 && len(samples) > limit {
		return samples[:limit]
	}
	return samples
}

func sanitizeCommandSummary(args []string) string {
	if len(args) == 0 {
		return ""
	}
	clean := make([]string, 0, len(args))
	redactNext := false
	for _, arg := range args {
		if redactNext {
			clean = append(clean, "***")
			redactNext = false
			continue
		}
		lower := strings.ToLower(arg)
		if sensitiveCommandArg(lower) {
			if strings.Contains(arg, "=") {
				clean = append(clean, arg[:strings.Index(arg, "=")+1]+"***")
			} else {
				clean = append(clean, arg)
				redactNext = true
			}
			continue
		}
		clean = append(clean, arg)
	}
	return truncateCommandSummary(strings.Join(clean, " "))
}

func sensitiveCommandArg(lower string) bool {
	return strings.Contains(lower, "password") ||
		strings.Contains(lower, "passwd") ||
		strings.Contains(lower, "secret") ||
		strings.Contains(lower, "token") ||
		strings.Contains(lower, "access-key") ||
		strings.Contains(lower, "secret-key")
}

func truncateCommandSummary(summary string) string {
	if len(summary) <= processCommandSummarySize {
		return summary
	}
	return summary[:processCommandSummarySize-3] + "..."
}

func sampleProcessIO(ctx context.Context, instanceUUID string, p *gopsprocess.Process, sampledAt int64) (uint64, uint64) {
	ioCounters, err := p.IOCountersWithContext(ctx)
	if err != nil || ioCounters == nil {
		return 0, 0
	}
	return updateProcessIORate(processMetricKey(instanceUUID, p.Pid), processIOState{
		readBytes:  ioCounters.ReadBytes,
		writeBytes: ioCounters.WriteBytes,
		sampledAt:  sampledAt,
	})
}

func processMetricKey(instanceUUID string, pid int32) string {
	return instanceUUID + ":" + strconv.Itoa(int(pid))
}

func updateProcessIORate(key string, current processIOState) (uint64, uint64) {
	processIOMu.Lock()
	defer processIOMu.Unlock()

	previous, ok := processIOCache[key]
	processIOCache[key] = current
	if !ok {
		return 0, 0
	}
	return processIOBytesPerSec(previous, current)
}

func processIOBytesPerSec(previous, current processIOState) (uint64, uint64) {
	elapsedMillis := current.sampledAt - previous.sampledAt
	if elapsedMillis <= 0 {
		return 0, 0
	}
	readDelta := counterDelta(previous.readBytes, current.readBytes)
	writeDelta := counterDelta(previous.writeBytes, current.writeBytes)
	return bytesPerSecond(readDelta, elapsedMillis), bytesPerSecond(writeDelta, elapsedMillis)
}

func counterDelta(previous, current uint64) uint64 {
	if current < previous {
		return 0
	}
	return current - previous
}

func bytesPerSecond(delta uint64, elapsedMillis int64) uint64 {
	if delta == 0 {
		return 0
	}
	return uint64(float64(delta) / (float64(elapsedMillis) / 1000))
}

func pruneProcessIOCache(activeKeys map[string]struct{}) {
	processIOMu.Lock()
	defer processIOMu.Unlock()

	for key := range processIOCache {
		if _, ok := activeKeys[key]; !ok {
			delete(processIOCache, key)
		}
	}
}
