package metrics

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/platform/directprobe"
)

// TestDirectProbeTimeoutsDefault 未配置（0）时回退内置默认，且设置后生效（FR-446 超时可配）。
func TestDirectProbeTimeoutsDefault(t *testing.T) {
	// 用例间共享全局生效值，逐例显式还原，避免相互污染。
	t.Cleanup(func() { SetDirectProbeTimeouts(0, 0) })

	SetDirectProbeTimeouts(0, 0)
	slp, query := DirectProbeTimeouts()
	assert.Equal(t, defaultDirectProbeTimeout, slp)
	assert.Equal(t, defaultDirectProbeTimeout, query)

	SetDirectProbeTimeouts(1500*time.Millisecond, 2*time.Second)
	slp, query = DirectProbeTimeouts()
	assert.Equal(t, 1500*time.Millisecond, slp)
	assert.Equal(t, 2*time.Second, query)
}

// TestDirectProbeTimeoutsNonPositiveFallsBack 非正下发值按「未配置」处理，不会把超时归零。
func TestDirectProbeTimeoutsNonPositiveFallsBack(t *testing.T) {
	t.Cleanup(func() { SetDirectProbeTimeouts(0, 0) })

	SetDirectProbeTimeouts(-1, 0)
	slp, query := DirectProbeTimeouts()
	assert.Equal(t, defaultDirectProbeTimeout, slp)
	assert.Equal(t, defaultDirectProbeTimeout, query)
}

// TestDirectProbeTimeoutsClampUpperBound 下发/本地值超上界时钳制到共享上界（FR-446 复审 N3）：
// Worker 独立钳制，令异常 CP 下发或 env 覆盖也无法把每实例每拍最坏阻塞量推过心跳采集预算。
//
// 上界值不再是字面量 30s，而取 directprobe.MaxTimeout（与 CP 写校验/读钳制同源，见 NEW-ISSUE A）；
// 期望值直接引用常量，故上界调整时本用例自动跟随，不会与实现漂移。
func TestDirectProbeTimeoutsClampUpperBound(t *testing.T) {
	t.Cleanup(func() { SetDirectProbeTimeouts(0, 0) })

	SetDirectProbeTimeouts(90*time.Second, 2*time.Minute)
	slp, query := DirectProbeTimeouts()
	assert.Equal(t, maxDirectProbeTimeout, slp)
	assert.Equal(t, maxDirectProbeTimeout, query)

	// 上界内的值不受影响；恰好等于上界仍被接受。
	SetDirectProbeTimeouts(maxDirectProbeTimeout, 2*time.Second)
	slp, query = DirectProbeTimeouts()
	assert.Equal(t, maxDirectProbeTimeout, slp)
	assert.Equal(t, 2*time.Second, query)

	// 略超上界即被钳制（边界另一侧）。
	SetDirectProbeTimeouts(maxDirectProbeTimeout+time.Second, maxDirectProbeTimeout+time.Second)
	slp, query = DirectProbeTimeouts()
	assert.Equal(t, maxDirectProbeTimeout, slp)
	assert.Equal(t, maxDirectProbeTimeout, query)
}

// TestDirectProbeUpperBoundMatchesSharedContract 锁定 Worker 归一的默认/上界与共享契约包一致
// （FR-446 复审 NEW-ISSUE A）：任一处改走独立字面量即失败。
func TestDirectProbeUpperBoundMatchesSharedContract(t *testing.T) {
	assert.Equal(t, directprobe.DefaultTimeout, defaultDirectProbeTimeout)
	assert.Equal(t, directprobe.MaxTimeout, maxDirectProbeTimeout)
	assert.Equal(t, directprobe.ProbeScrapeTimeoutCap, probeScrapeTimeoutCap)
}

// TestCollectConfigTimeoutOverridesEffective 显式传入的超时优先于进程生效值（采集两条链路都具名传入）。
func TestCollectConfigTimeoutOverridesEffective(t *testing.T) {
	t.Cleanup(func() { SetDirectProbeTimeouts(0, 0) })
	SetDirectProbeTimeouts(5*time.Second, 5*time.Second)

	// 端口 0 → 不发起任何真实探测；只验证超时取值路径不 panic 且返回「全不可用」。
	tel := CollectInstanceTelemetry(CollectConfig{
		Host:         "127.0.0.1",
		SLPTimeout:   250 * time.Millisecond,
		QueryTimeout: 300 * time.Millisecond,
	})
	require.NotNil(t, tel)
	assert.False(t, tel.SLPAvailable)
	assert.False(t, tel.QueryAvailable)
	assert.Nil(t, tel.SLPErr, "未配置端口不算失败")
	assert.Nil(t, tel.QueryErr)
}

// TestProbeErrorCategory 三类直探错误归类（FR-446 审计项 4 的告警文案依赖它）。
func TestProbeErrorCategory(t *testing.T) {
	assert.Equal(t, "", ProbeErrorCategory(nil))
	assert.Equal(t, ProbeErrTimeout, ProbeErrorCategory(os.ErrDeadlineExceeded))
	assert.Equal(t, ProbeErrTimeout, ProbeErrorCategory(fmt.Errorf("读取超时: %w", os.ErrDeadlineExceeded)))

	timeoutErr := &net.OpError{Op: "read", Net: "udp", Err: &timeoutError{}}
	assert.Equal(t, ProbeErrTimeout, ProbeErrorCategory(timeoutErr))

	refused := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	assert.Equal(t, ProbeErrRefused, ProbeErrorCategory(refused))

	other := &net.OpError{Op: "write", Net: "udp", Err: errors.New("network is unreachable")}
	assert.Equal(t, ProbeErrUnreachable, ProbeErrorCategory(other))
	assert.Equal(t, ProbeErrUnreachable, ProbeErrorCategory(errors.New("boom")))
}

// timeoutError 是只实现 net.Error 的 Timeout()==true 的假实现。
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
