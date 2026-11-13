package logutil

// 原生态 TTL 相对热窗口的余量校验。
//
// 2026-11-10：从已删除的 `logs/retention` 包迁来。
//
// 为何保留：这是单 VL 架构自身的保护——它回答“VL 的 -retentionPeriod
// 会不会早于产品声明的保留期把数据删掉”。冷热分层没了之后这条校验更关键：
// 现在 VL 就是唯一存储，它删了就没有第二份了。

import (
	"fmt"
	"math"
	"time"
)

// RuntimeHeadroom 是日分区搬运需要的有界等待预算，不构成无限故障保护。
// 零复制/恢复预算表示未声明对应 SLA；启层配置层必须另外要求正值。
type RuntimeHeadroom struct {
	DriverInterval   time.Duration
	CopyVerifyBudget time.Duration
	RecoveryBudget   time.Duration
}

// ValidateRuntimeHeadroom 返回原生 TTL 相对热窗口的余量，拒绝日界、扫描和声明 SLA 不足。
// VL 日分区淘汰与产品次日结束起算不同，保守预留一天 UTC 边界；所有计算保持精度。
func ValidateRuntimeHeadroom(native string, hot time.Duration, requirements RuntimeHeadroom) (time.Duration, error) {
	nativeTTL, err := ParseRuntimeTTL(native)
	if err != nil {
		return 0, err
	}
	if hot <= 0 {
		return 0, fmt.Errorf("启层热窗口必须大于0")
	}
	if requirements.DriverInterval < 0 || requirements.CopyVerifyBudget < 0 || requirements.RecoveryBudget < 0 {
		return 0, fmt.Errorf("搬运余量预算不能为负")
	}
	if requirements.DriverInterval == 0 {
		requirements.DriverInterval = DefaultDriverInterval
	}
	required := 24 * time.Hour
	for _, d := range []time.Duration{requirements.DriverInterval, requirements.CopyVerifyBudget, requirements.RecoveryBudget} {
		if d > time.Duration(math.MaxInt64)-required {
			return 0, fmt.Errorf("搬运余量预算溢出")
		}
		required += d
	}
	margin := nativeTTL - hot
	if margin < required {
		return margin, fmt.Errorf(
			"原生TTL相对热窗口余量%s不足%s（日界+扫描+复制校验+恢复预算）；有限TTL不能保证无限故障保留",
			margin, required)
	}
	return margin, nil
}
