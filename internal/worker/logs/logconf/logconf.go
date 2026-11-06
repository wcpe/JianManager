// Package logconf 放「采集配置面」需要的中立定义。
//
// # 为何需要这个包
//
// 这几个类型/函数原本住在 `logs/ingest`（旧采集链路，约 23k 行）。删旧链路时发现
// 它们其实是**采集配置的公用词汇**，不属于任何一条具体实现：
//
//   - `InstanceRegistration`：gRPC 层要向采集器登记一个实例，接口签名要用它。
//     放在这里，gRPC 就不必依赖某个具体采集器实现。
//   - `ParseTimeZone` / `IsValidTimeZone`：解释日志行内 `[HH:MM:SS]` 的时区。
//     这是**配置语义**，与“用哪个采集器读文件”无关。
//   - `IsValidCharset`：同属配置语义（真源仍在 normalize）。
//
// # 为何不直接放进 logtail
//
// `gRPC`（internal/worker/grpc）需要 `InstanceRegistration`，而 logtail 依赖 vlsup、
// catalog 等重包。让 grpc 为了一个 6 字段的结构体去背这些依赖是错的。
// 本包只依赖 stdlib + normalize + tzdata。
package logconf

import (
	"fmt"
	"strings"
	"time"

	// 内嵌时区库：官方 alpine 镜像与部分裸机不带 /usr/share/zoneinfo，
	// 缺了它 LoadLocation 对**任何合法时区名**都会失败。
	// （与旧位置同一取舍，直接随迁逸一起搬过来。）
	_ "time/tzdata"

	"github.com/wcpe/JianManager/internal/worker/logs/normalize"
)

// InstanceRegistration 是一条实例日志登记（gRPC → 采集器）。
//
// # 为何带 RuleKey / RelativePath
//
// 两者都是 CP 推导下发的**路径意图**：RuleKey 是核心类型（bukkit/bungee/…），
// RelativePath 是实例级覆盖。新采集器目前只靠 WorkDir 推导路径，但登记层
// 必须忠实携带完整信息——将来要在新采集器里应用规则/覆盖时不用改协议。
type InstanceRegistration struct {
	UUID       string
	TargetID   string
	Generation string
	Mode       string
	WorkDir    string
	RuleKey    string // 日志采集规则名，空 = bukkit
	// RelativePath 是实例级日志相对路径覆盖，空 = 规则默认。
	RelativePath string
}

// 采集模式常量（与 CP 下发的 `log_acquire_mode` 逐字一致）。
//
// # 为什么放在这里而不是留在旧 pipeline 包
//
// 这两个字符串是**协议取值**（CP 下发、Worker 据此分流），不属于任何采集实现。
// 旧 `pipeline` 包已随旧链路删除；把它们搬到这里，新的桥接层就不必依赖一个
// 正在消失的包。取值必须逐字保持，否则存量实例的登记会被判成未知模式。
const (
	// ModeFilePrimary 实例自己写日志文件（Minecraft Java），进程输出不要重复落盘。
	ModeFilePrimary = "FILE_PRIMARY"
	// ModeStdioPrimary 日志只存在于进程管道（代理 / 通用二进制 / Beacon）。
	ModeStdioPrimary = "STDIO_PRIMARY"
)

// 时区取值常量（供配置与登记使用）。
const (
	// TimeZoneUTC 显式 UTC。
	TimeZoneUTC = "utc"
	// TimeZoneLocal 跟随节点本地时区。
	TimeZoneLocal = "local"
)

// ParseTimeZone 解析时区配置；nil 位置语义上等价于 UTC。
//
// # 为何显式返回错误而不是回退 UTC
//
// 时区配错会让整源时间轴**静默偏移**（现场曾出现 +8 小时），
// 只能在查询结果里发现。所以非法值必须在配置加载阶段就拒。
func ParseTimeZone(raw string) (*time.Location, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return time.UTC, nil
	case TimeZoneUTC, "z", "gmt":
		return time.UTC, nil
	case TimeZoneLocal, "node", "host":
		return time.Local, nil
	default:
		loc, err := time.LoadLocation(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("logconf: 未知时区 %q（支持 UTC/local 或 IANA 名，如 Asia/Hong_Kong）", raw)
		}
		return loc, nil
	}
}

// IsValidTimeZone 报告配置取值是否可解析。
func IsValidTimeZone(raw string) bool {
	_, err := ParseTimeZone(raw)
	return err == nil
}

// IsValidCharset 报告日志字符集配置是否合法。
//
// 真源是 `normalize`（字符集判定与解码的唯一实现）。本函数只是一层转发，
// 让配置层不必为了校验一个字符串而 import 整个解码包。
func IsValidCharset(raw string) bool {
	return normalize.IsValidCharset(raw)
}
