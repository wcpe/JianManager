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
	// DefaultRetentionPeriod 是**唯一**受管 VL 实例的默认原生 TTL（180 天）。
	//
	// 历史沿革：原为 120d（热层），另有 DefaultColdRetentionPeriod=730d（冷层）。
	// 2026-10-29 用户裁定废除冷热双层、只保留一个 VL，并选定 180d：
	// 30d 会让超过一月的历史日志在 VL 里永久不可查；730d 按当前写入速率约 120GB，
	// 而 /home 只剩 394G。180d 约 30GB，是「可查范围」与「磁盘」的折中。
	//
	// 注意：VL 原生 TTL 到期是**直接删**，它绕过平台侧「删前归档」。
	DefaultRetentionPeriod = "180d"
	// DefaultHotRetention 是产品热窗口的唯一默认值，和原生TTL分开。
	DefaultHotRetention = 90 * 24 * time.Hour
	// DefaultColdRetention 是产品冷窗口的唯一默认值。
	//
	// 2026-10-29：冷热双层废除后，「冷窗口」不再是一个独立的物理层，而是
	// **同一个 VL 能查多久**。原值 730d 已无法兼具：VL 只保留 180d，
	// 继续声明 730d 会让启动校验（retention 必须 ≥ 声明窗口）直接拒绝启动。
	// 因此对齐到 180d。若运维真需要 730d，应该同时把
	// `log_vl.retention_period` 调到 ≥ 730d，而不是关掉这道校验。
	DefaultColdRetention = 180 * 24 * time.Hour
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
