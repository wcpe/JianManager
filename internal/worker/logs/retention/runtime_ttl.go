package retention

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// VL v1.52.0 使用 VM c82127b6d4d1 的 flagutil.RetentionDuration：
// 裸数字和大写 M 是固定 31 天月；y 是 365 天；最终小写 m 被拒绝。
// 这里仅解析受管 retention 参数，不复用产品 ParseTTL，避免把月份猜成分钟。
var vlDurationPart = regexp.MustCompile(`^-?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(ms|[smhdwyi])`)

// ParseRuntimeTTL 按已审批 VL 参数语义解析有效保留期，不用于产品策略 TTL。
func ParseRuntimeTTL(raw string) (time.Duration, error) {
	const monthMS = float64(31 * 24 * 60 * 60 * 1000)
	var ms float64
	month := raw
	if strings.HasSuffix(raw, "M") {
		month = strings.TrimSuffix(raw, "M")
	}
	months, err := strconv.ParseFloat(month, 64)
	if err == nil || strings.HasSuffix(raw, "M") {
		if err != nil || math.IsNaN(months) || math.IsInf(months, 0) || months < 0 || months > 1200 {
			return 0, fmt.Errorf("retention: 非法 VL 月份 %q", raw)
		}
		ms = months * monthMS
	} else {
		s := strings.ToLower(raw)
		if strings.HasSuffix(s, "m") {
			return 0, fmt.Errorf("retention: VL 月份必须用大写 M，小写 m 不允许：%q", raw)
		}
		negative := false
		for s != "" {
			part := vlDurationPart.FindStringSubmatch(s)
			if part == nil {
				return 0, fmt.Errorf("retention: 无法解析 VL retention %q", raw)
			}
			unit := part[1]
			value, err := strconv.ParseFloat(strings.TrimSuffix(part[0], unit), 64)
			if err != nil {
				return 0, fmt.Errorf("retention: 无法解析 VL retention %q", raw)
			}
			factor := map[string]float64{"ms": 1, "s": 1000, "m": 60000, "h": 3600000, "d": 86400000, "w": 604800000, "y": 31536000000, "i": 0}[unit]
			local := value * factor
			// MetricsQL 的组合负号作用于其后所有正分量，不按 Go Duration 解释。
			if negative && local > 0 {
				local = -local
			}
			ms += local
			if local < 0 {
				negative = true
			}
			s = s[len(part[0]):]
		}
	}
	// 官方 VL 启动拒绝小于一天；在毫秒转 Duration 前限制范围，防止溢出后反向比较。
	if math.IsNaN(ms) || math.IsInf(ms, 0) || ms < 86400000 || ms/monthMS > 1200 {
		return 0, fmt.Errorf("retention: VL retention %q 必须为 1 天到 1200 个月", raw)
	}
	return time.Duration(int64(ms)) * time.Millisecond, nil
}
