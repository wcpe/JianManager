package ingest

import (
	"fmt"
	"os"
	"strings"
	"time"

	// 为什么在包内内嵌时区库（与 Control Plane 侧 apps/control-plane/main.go 同一取舍）：
	// 官方容器镜像（alpine）与部分裸机不带 /usr/share/zoneinfo，缺了它 `time.LoadLocation`
	// 对**任何合法时区名**都会失败——于是「按源配置时区」会退化成「部署形态决定能不能配」。
	// 内嵌后可保证合法 IANA 名必可解析；真正非法的名字在登记阶段被显式拒绝。
	_ "time/tzdata"
)

// 缺陷 C（时间戳偏移 8 小时）的配置面。
//
// 现场（2026-10-01/02）：日志文本里的 `[23:54:02]`（本地 HKT=UTC+8）被平台存成
// `event_time_utc=2026-10-01T23:54:02Z`（实际应为 15:54:02Z），导致时间窗、排序与实时跟随全部错位。
//
// 归一化侧的换算逻辑早已存在（normalize.applyClock 按 Location 解释 [HH:MM:SS] 并做跨午夜回拨），
// 缺的是**把正确时区接进来**：此前没有任何配置面能把源时区传给 normalize，
// Location 恒为默认 UTC —— 于是本地时间被当成 UTC 直接落库。
//
// 取值语义（不硬编码任何时区）：
//   - 空串：UTC（保持既有行为，未配置的源零变化）；
//   - "utc"/"z"：显式 UTC；
//   - "local"：跟随节点进程时区（容器/裸机的 TZ 设置）；
//   - 其余：IANA 时区名（如 Asia/Hong_Kong、Asia/Shanghai）。
const (
	// TimeZoneUTC 显式 UTC。
	TimeZoneUTC = "utc"
	// TimeZoneLocal 跟随节点本地时区。
	TimeZoneLocal = "local"
)

// ParseTimeZone 解析按源时区配置；返回 nil 位置表示使用 UTC。
//
// 与 CP 侧「非法值回退 UTC」不同，这里**显式返回错误**：源时区配错会让整源的时间轴静默偏移，
// 属于必须暴露的配置错误（登记阶段即失败，而不是让运维在查询结果里猜）。
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
			return nil, fmt.Errorf("ingest: 未知时区 %q（支持 UTC/local 或 IANA 名，如 Asia/Hong_Kong）", raw)
		}
		return loc, nil
	}
}

// IsValidTimeZone 报告配置取值是否可解析（供登记阶段校验）。
func IsValidTimeZone(raw string) bool {
	_, err := ParseTimeZone(raw)
	return err == nil
}

// DefaultTimeZoneHint 返回「节点级默认时区」配置的诊断提示；无异常时返回空串。
//
// 为什么必须有它（2026-10-02 真机复验）：现场把 `log_ingest.time_zone: local` 配上并重启后，
// 新入库条目**仍是 +8h**——而接线、恢复路径、归一化链路逐段核对都是对的，因为 `local` 解析的是
// **Worker 进程**的本地时区：容器（`alpine` 基础镜像无 `/etc/localtime`、无 `TZ`）与部分 systemd
// 单元（`Environment=TZ=` 空串）里 `local` 恰好等于 UTC，于是「配了 local」= 「什么都没配」。
// 这个坑不可能靠读代码或单测发现，只能靠**运行期自证**：把配置值、解析结果与解析依据打出来。
//
// 参数 loc 为已解析出的位置（由调用方用 ParseTimeZone 得到），便于本函数保持纯函数、可直测。
func DefaultTimeZoneHint(raw string, loc *time.Location) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case TimeZoneLocal, "node", "host":
	default:
		return ""
	}
	if loc == nil {
		return ""
	}
	if _, offset := time.Now().In(loc).Zone(); offset == 0 {
		return "log_ingest.time_zone=local 在 Worker 进程内解析为 UTC+00:00（" +
			describeProcessLocalTimeZone() + "）：这与「跟随节点本地时区」的意图不符，" +
			"日志时间轴会按 UTC 解释。生产建议显式写 IANA 名（如 Asia/Hong_Kong）"
	}
	return ""
}

// describeProcessLocalTimeZone 描述进程本地时区的来源（TZ 环境变量与 /etc/localtime），
// 供上面的告警定位「为什么 local 成了 UTC」。
func describeProcessLocalTimeZone() string {
	tz, set := os.LookupEnv("TZ")
	switch {
	case !set:
		tz = "<未设置>"
	case strings.TrimSpace(tz) == "":
		tz = `""（空串：Go 按 UTC 处理）`
	}
	localtime := "缺失"
	if _, err := os.Stat("/etc/localtime"); err == nil {
		localtime = "存在"
	}
	return "TZ=" + tz + ", /etc/localtime=" + localtime
}
