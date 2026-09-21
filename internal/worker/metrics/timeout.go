package metrics

import (
	"errors"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/wcpe/JianManager/internal/platform/directprobe"
)

// 直探超时的 Worker 侧生效值（FR-446，spec §2.6「超时可配」）。
//
// CP 经心跳响应下发 direct_probe.slp_timeout / direct_probe.query_timeout（毫秒），Worker 收到后
// 经 SetDirectProbeTimeouts 写入本进程的生效值；心跳时序链路与 GetInstanceMetrics 实时链路都读
// 同一份值填入 CollectConfig，保证两条链路的超时语义一致。
//
// 为 0（未配置 / 老 CP 不下发）时回退 defaultDirectProbeTimeout。进程内全局单值：Worker 只有一个
// 采样进程，跨包共享（heartbeat 写入、heartbeat/grpc 读取），避免为取值再穿一层依赖注入。
var (
	directProbeTimeoutsMu sync.RWMutex
	directProbeSLPTimeout time.Duration
	directProbeQueryTimeo time.Duration
)

// SetDirectProbeTimeouts 更新 SLP / Query 直探超时的生效值（FR-446）。
// 非正值按「未配置」处理（清空覆盖，回退默认），使非法下发不会把超时归零成立即失败。
func SetDirectProbeTimeouts(slp, query time.Duration) {
	directProbeTimeoutsMu.Lock()
	defer directProbeTimeoutsMu.Unlock()
	directProbeSLPTimeout = normalizeProbeTimeout(slp)
	directProbeQueryTimeo = normalizeProbeTimeout(query)
}

// DirectProbeTimeouts 返回 SLP / Query 直探的当前生效超时（均已归一为非正值→默认）。
func DirectProbeTimeouts() (slp, query time.Duration) {
	directProbeTimeoutsMu.RLock()
	defer directProbeTimeoutsMu.RUnlock()
	return normalizeProbeTimeout(directProbeSLPTimeout), normalizeProbeTimeout(directProbeQueryTimeo)
}

// normalizeProbeTimeout 把超时归一到「(0, maxDirectProbeTimeout]」区间：非正值回退内置默认，
// 超上界则钳制到上界（FR-446 复审 N3）。这样非法/异常的下发值既不会把超时归零成立即失败，
// 也不会突破心跳采集护栏拖垮采集链。
//
// 实现委托给共享契约包（FR-446 复审 NEW-ISSUE A）：CP 写校验、CP 读/下发钳制与本处归一
// **同源**，不再各写一份字面量上界，避免「CP 收下而 Worker 按另一上界静默钳制」的漂移。
func normalizeProbeTimeout(d time.Duration) time.Duration {
	return directprobe.NormalizeTimeout(d)
}

// 直探失败类别（FR-446 审计项 4）：仅用于告警/日志文案，不参与控制流。
const (
	// ProbeErrTimeout 对端无响应 / 读超时（端口被防火墙丢弃、服务未监听但主机可达）。
	ProbeErrTimeout = "timeout"
	// ProbeErrRefused 对端主动拒绝（端口关闭，TCP RST / UDP ICMP port unreachable）。
	ProbeErrRefused = "connection refused"
	// ProbeErrUnreachable 主机/路由不可达或其它网络错误。
	ProbeErrUnreachable = "unreachable"
)

// ProbeErrorCategory 把直探错误归类为短标签，供来源化告警文案使用（如 "SLP: timeout"）。
// 入参为 nil 时返回空串。纯函数、无 IO，便于单测。
func ProbeErrorCategory(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return ProbeErrTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ProbeErrTimeout
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return ProbeErrRefused
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		// 底层 syscall 错误被 net 包包装；统一按不可达处理，避免把平台特定的 errno 文案透出。
		return ProbeErrUnreachable
	}
	return ProbeErrUnreachable
}
