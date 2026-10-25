// Package vlruntime 提供批准版 VictoriaLogs 的纯运行时契约，不依赖日志业务包。
package vlruntime

import (
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultRetentionPeriod 比产品热窗口多30天；有限原生TTL不是无限失败保护。
	DefaultRetentionPeriod = "120d"
	// DefaultColdRetentionPeriod 是冷层原生有限TTL的唯一默认字面量。
	DefaultColdRetentionPeriod = "730d"
	// DefaultHotRetention 是产品热窗口的唯一默认值，和原生TTL分开。
	DefaultHotRetention = 90 * 24 * time.Hour
	// DefaultColdRetention 是产品冷窗口的唯一默认值，不按迁入时间重新起算。
	DefaultColdRetention = 730 * 24 * time.Hour
)

// 批准版将裸数和大写M解释为31天月；组合分量的负号延续到后续正分量。
var durationPart = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]*)?(ms|[smhdwyi])`)

// ParseRetention 按批准版v1.52.0的有效retention语义解析，精确累加后截到毫秒。
// 最终小写m被拒绝，最少1天、最多1200个31天月；不接受NaN、Inf或溢出。
func ParseRetention(raw string) (time.Duration, error) {
	const monthMS int64 = 31 * 24 * 60 * 60 * 1000
	ms := new(big.Rat)
	month := strings.TrimSuffix(raw, "M")
	f, err := strconv.ParseFloat(month, 64)
	if err == nil || strings.HasSuffix(raw, "M") {
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 1200 {
			return 0, fmt.Errorf("vlruntime: 非法VL月份 %q", raw)
		}
		r, ok := new(big.Rat).SetString(month)
		if !ok {
			return 0, fmt.Errorf("vlruntime: 无法解析VL月份 %q", raw)
		}
		ms.Mul(r, new(big.Rat).SetInt64(monthMS))
	} else {
		s := strings.ToLower(raw)
		if s == "" || strings.HasSuffix(s, "m") {
			return 0, fmt.Errorf("vlruntime: VL月份必须用大写M且不能留空：%q", raw)
		}
		negative := false
		factors := map[string]int64{"ms": 1, "s": 1000, "m": 60000, "h": 3600000, "d": 86400000, "w": 604800000, "y": 31536000000, "i": 0}
		for s != "" {
			part := durationPart.FindStringSubmatch(s)
			if part == nil {
				return 0, fmt.Errorf("vlruntime: 无法解析VL保留期 %q", raw)
			}
			r, ok := new(big.Rat).SetString(strings.TrimSuffix(part[0], part[1]))
			if !ok {
				return 0, fmt.Errorf("vlruntime: 无法解析VL保留期 %q", raw)
			}
			r.Mul(r, new(big.Rat).SetInt64(factors[part[1]]))
			if negative && r.Sign() > 0 {
				r.Neg(r)
			}
			ms.Add(ms, r)
			if r.Sign() < 0 {
				negative = true
			}
			s = s[len(part[0]):]
		}
	}
	if ms.Cmp(new(big.Rat).SetInt64(86400000)) < 0 || ms.Cmp(new(big.Rat).SetInt64(1200*monthMS)) > 0 {
		return 0, fmt.Errorf("vlruntime: VL保留期 %q 必须为1天到1200个月", raw)
	}
	whole := new(big.Int).Quo(ms.Num(), ms.Denom())
	if !whole.IsInt64() || whole.Int64() > math.MaxInt64/int64(time.Millisecond) {
		return 0, fmt.Errorf("vlruntime: VL保留期溢出")
	}
	return time.Duration(whole.Int64()) * time.Millisecond, nil
}
