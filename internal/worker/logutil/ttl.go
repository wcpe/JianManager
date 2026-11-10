package logutil

// 保留期解析。
//
// 2026-11-10：从已删除的 `logs/retention` 包迁来。
//
// 为何不像包一起删：`ParseRuntimeTTL` 仍在生产配置校验链上——它解析
// `log_vl.retention_period`（真机为 180d），该值还用于校验“VL 原生淘汰不会
// 早于产品声明的保留期”。这条校验与冷热分层无关，属单 VL 架构自身的保护。
//
// 两者职责不同，不能合并：
//   - ParseTTL           产品策略时长的解析（允许 3d / 2w 这类“天/周”写法）；
//   - ParseRuntimeTTL    VL 子进程参数的解析，复用批准版 vlruntime，单一口径。

import (
	"time"

	"github.com/wcpe/JianManager/internal/platform/vlruntime"
)

// ParseRuntimeTTL 复用批准版运行时解析，不与产品策略 TTL 的单位混用。
func ParseRuntimeTTL(raw string) (time.Duration, error) {
	return vlruntime.ParseRetention(raw)
}

