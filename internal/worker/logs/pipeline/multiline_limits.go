package pipeline

import (
	"fmt"

	"github.com/wcpe/JianManager/internal/worker/logs/normalize"
)

// DefaultMultilineLimits 是生产管线有限缓存默认值的唯一真源，不改变独立 Normalizer 的零值契约。
// 溢出由既有 TRUNCATED/PARTIAL 事件承接全部原文和精确源区间，不丢续行、不发明事件身份。
func DefaultMultilineLimits() normalize.Limits {
	return normalize.Limits{MaxBytes: 1 << 20, MaxLines: 512}
}

func productionMultilineLimits(limits normalize.Limits) (normalize.Limits, error) {
	if limits.MaxBytes < 0 || limits.MaxLines < 0 || limits.Timeout < 0 {
		return normalize.Limits{}, fmt.Errorf("pipeline: multiline limits must not be negative")
	}
	defaults := DefaultMultilineLimits()
	if limits.MaxBytes == 0 {
		limits.MaxBytes = defaults.MaxBytes
	}
	if limits.MaxLines == 0 {
		limits.MaxLines = defaults.MaxLines
	}
	return limits, nil
}
