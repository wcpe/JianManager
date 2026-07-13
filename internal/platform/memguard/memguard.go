// Package memguard 内存水位启动守卫的共享估算与判定逻辑（FR-317）。
// CP 预警闸（按心跳预判）与 Worker 实时闸（启动前读系统内存）复用同一套
// 估算口径，避免两侧判定标准漂移。真机事故背景：验收开 Paper -Xmx2048M 把
// 4G 主机 OOM 至用户态僵死（SSH/面板全失联），启动前无任何水位防线。
package memguard

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// xmxRe 匹配启动命令中的 -Xmx<n><unit>（单位 k/m/g，大小写不敏感；无单位按字节向上取 MB）。
var xmxRe = regexp.MustCompile(`(?i)-Xmx(\d+)([kmg]?)`)

// ParseXmxMB 从启动命令解析 -Xmx 堆上限（MB）。解析不到返回 0；多个 -Xmx 取第一个。
func ParseXmxMB(startCommand string) int64 {
	m := xmxRe.FindStringSubmatch(startCommand)
	if m == nil {
		return 0
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0
	}
	switch strings.ToLower(m[2]) {
	case "g":
		return n * 1024
	case "m":
		return n
	case "k":
		return (n + 1023) / 1024
	default: // 裸字节
		return (n + 1024*1024 - 1) / (1024 * 1024)
	}
}

// jvmOverheadMB JVM 堆外保守开销（元空间/直接内存/线程栈/GC 结构）。
const jvmOverheadMB = 256

// defaultEstimateMB 解析不出内存声明时的保守默认（512 堆 + 开销）。
const defaultEstimateMB = 512 + jvmOverheadMB

// EstimateStartMB 估算一次实例启动的内存需求（MB）：
// docker 模式用 cgroup 上限 memLimitMB（>0 时优先）；宿主模式解析 -Xmx（堆 × 1.15 + 固定开销，
// 有余量地覆盖 RSS 高于堆的常态）；两者皆无用保守默认。
func EstimateStartMB(startCommand string, memLimitMB int64) int64 {
	if memLimitMB > 0 {
		return memLimitMB
	}
	if xmx := ParseXmxMB(startCommand); xmx > 0 {
		return xmx*115/100 + jvmOverheadMB
	}
	return defaultEstimateMB
}

// DefaultReserveMB 默认保留水位：max(512MB, 总内存 10%)——留给内核/系统服务/Worker 自身，
// 防止「刚好塞满」后系统抖一下就 OOM。
func DefaultReserveMB(totalMB int64) int64 {
	tenth := totalMB / 10
	if tenth > 512 {
		return tenth
	}
	return 512
}

// Check 判定水位：可用 − 需求 < 保留 即拒绝，错误文案含三个数字可直接面向用户。
func Check(availMB, requiredMB, reserveMB int64) error {
	if availMB-requiredMB < reserveMB {
		return fmt.Errorf("节点可用内存不足：可用 %dMB，实例预估需 %dMB，需保留 %dMB 安全水位——已拒绝启动以防节点失去响应（FR-317）。请先停止其它实例、调低 -Xmx，或上调节点内存",
			availMB, requiredMB, reserveMB)
	}
	return nil
}
