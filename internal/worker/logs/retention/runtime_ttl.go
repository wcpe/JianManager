package retention

import (
	"fmt"
	"math"
	"time"

	"github.com/wcpe/JianManager/internal/platform/vlruntime"
)

// ParseRuntimeTTL 复用批准版运行时解析，不与产品策略TTL的单位混用。
func ParseRuntimeTTL(raw string) (time.Duration, error) {
	return vlruntime.ParseRetention(raw)
}

// RuntimeHeadroom 是日分区搬运需要的有界等待预算，不构成无限故障保护。
// 零复制/恢复预算表示未声明对应SLA；启层配置层必须另外要求正值。
type RuntimeHeadroom struct {
	DriverInterval   time.Duration
	CopyVerifyBudget time.Duration
	RecoveryBudget   time.Duration
}

// ValidateRuntimeHeadroom 返回原生TTL相对热窗口的余量，拒绝日界、扫描和声明SLA不足。
// VL日分区淘汰与产品次日结束起算不同，保守预留一天UTC边界；所有计算保持精度。
func ValidateRuntimeHeadroom(native string, hot time.Duration, requirements RuntimeHeadroom) (time.Duration, error) {
	nativeTTL, err := ParseRuntimeTTL(native)
	if err != nil {
		return 0, err
	}
	if hot <= 0 {
		return 0, fmt.Errorf("retention: 启层热窗口必须大于0")
	}
	if requirements.DriverInterval < 0 || requirements.CopyVerifyBudget < 0 || requirements.RecoveryBudget < 0 {
		return 0, fmt.Errorf("retention: 搬运余量预算不能为负")
	}
	if requirements.DriverInterval == 0 {
		requirements.DriverInterval = DefaultDriverInterval
	}
	required := 24 * time.Hour
	for _, d := range []time.Duration{requirements.DriverInterval, requirements.CopyVerifyBudget, requirements.RecoveryBudget} {
		if d > time.Duration(math.MaxInt64)-required {
			return 0, fmt.Errorf("retention: 搬运余量预算溢出")
		}
		required += d
	}
	margin := nativeTTL - hot
	if margin < required {
		return margin, fmt.Errorf("retention: 原生TTL相对热窗口余量%s不足%s（日界+扫描+复制校验+恢复预算）；有限TTL不能保证无限故障保留", margin, required)
	}
	return margin, nil
}
