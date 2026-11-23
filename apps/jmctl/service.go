package main

import (
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// 组件运行状态探测。
//
// 两条信息来源，按可靠性优先：
//
//  1. **systemd**：能拿到 PID 与启动时间，且区分「配置了但没启动」与「根本没配置」。
//     只在有 systemctl 且能查到 unit 时采用。
//  2. **扫进程**：读进程列表比对可执行文件名。覆盖「裸跑」「容器内」「无 systemd」
//     等情形，但拿不到启动时间（跨平台取进程起始时间代价高且不可靠）。
//
// 都不命中则判为未运行，并说明查过什么——比单纯显示「未运行」有用得多，
// 用户能据此判断是「真没跑」还是「跑在别的地方」。

// componentSpec 描述一个待探测的组件。
type componentSpec struct {
	// Name 展示名。
	Name string
	// Binary 可执行文件名（不含路径与 .exe）。
	Binary string
	// UnitCandidates 可能的 systemd unit 名，按优先级排列。
	//
	// Worker 的 unit 名随安装目录派生（见 install-worker.sh），同机多实例时
	// 带不同后缀，因此用前缀匹配而非等值匹配。
	UnitPrefix string
}

// components 是支持的组件清单。
//
// 顺序固定：CP 在前，它是面板入口，运维最先关心。
var components = []componentSpec{
	{Name: "control-plane", Binary: "control-plane", UnitPrefix: "jianmanager-cp"},
	{Name: "worker", Binary: "worker", UnitPrefix: "jianmanager-worker"},
}

// detectServices 依次探测各组件状态。
//
// 进程表只扫一次、供所有组件复用：在 Windows 上每次 tasklist 要 0.8 秒左右，
// 按组件各扫一次会让 status 无谓地慢一倍。
func detectServices() []serviceStatus {
	hasSystemd := hasSystemctl()

	// 仍需退到进程扫描的组件，批量收集后再统一扫一次进程表。
	needScan := make([]componentSpec, 0, len(components))
	results := make([]serviceStatus, len(components))
	for i, c := range components {
		if hasSystemd {
			if s, ok := querySystemd(c); ok {
				results[i] = s
				continue
			}
		}
		results[i] = serviceStatus{Name: c.Name, Source: "none"}
		needScan = append(needScan, c)
	}

	if len(needScan) > 0 {
		found := scanProcessesByName()

		for i, c := range components {
			if results[i].Source != "none" {
				continue
			}
			if pid, ok := found[c.Binary]; ok {
				results[i] = serviceStatus{
					Name:    c.Name,
					Source:  "process",
					Running: true,
					PID:     pid,
					Note:    "按进程名匹配（非 systemd 托管）",
				}
				continue
			}
			// 都没找到：说清查过什么，便于区分「没跑」与「跑在别处」。
			if hasSystemd {
				results[i].Note = "未找到 systemd unit，也未在进程列表中发现"
			} else {
				results[i].Note = "本机无 systemd，仅按进程名查找"
			}
		}
	}
	return results
}

// hasSystemctl 判断本机是否有可用的 systemctl。
//
// Windows 与容器内通常没有；此时直接走扫进程分支，不去执行注定失败的命令。
func hasSystemctl() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	_, err := exec.LookPath("systemctl")
	return err == nil
}

// querySystemd 通过 systemctl 查询 unit 状态。
//
// 用 `systemctl show` 而非 `is-active`：前者一次就能拿到 LoadState（是否配置过）、
// ActiveState、MainPID 与 ActiveEnterTimestamp，省去多次调用。
func querySystemd(c componentSpec) (serviceStatus, bool) {
	unit := findUnit(c.UnitPrefix)
	if unit == "" {
		return serviceStatus{}, false
	}

	out, err := exec.Command("systemctl", "show", unit,
		"--property=ActiveState,MainPID,ActiveEnterTimestamp,LoadState",
		"--no-pager").Output()
	if err != nil {
		return serviceStatus{}, false
	}

	props := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			props[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	// LoadState != loaded 说明 unit 文件不存在或加载失败，交给进程扫描兜底。
	if props["LoadState"] != "loaded" {
		return serviceStatus{}, false
	}

	st := serviceStatus{
		Name:    c.Name,
		Unit:    unit,
		Source:  "systemd",
		Running: props["ActiveState"] == "active",
	}
	if pid, err := strconv.Atoi(props["MainPID"]); err == nil && pid > 0 {
		st.PID = pid
	}
	if st.Running {
		st.Uptime = humanizeSince(props["ActiveEnterTimestamp"])
	} else {
		st.Note = "unit 已安装，当前状态 " + props["ActiveState"]
	}
	return st, true
}

// findUnit 在已安装的 unit 中按前缀匹配。
//
// 用列表匹配而非直接猜名字：Worker 的 unit 名随安装目录派生，
// 硬编码只在默认目录下才对。
func findUnit(prefix string) string {
	out, err := exec.Command("systemctl", "list-unit-files", "--type=service",
		"--no-pager", "--no-legend").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := strings.TrimSuffix(fields[0], ".service")
		if name == prefix || strings.HasPrefix(name, prefix+"-") {
			return name
		}
	}
	return ""
}

// humanizeSince 把 systemd 的时间戳转成「已运行多久」。
//
// systemd 输出形如 `Thu 2026-11-19 10:00:00 CST`，格式随 locale 变化，
// 因此解析失败时原样返回时间戳，不猜。
func humanizeSince(ts string) string {
	ts = strings.TrimSpace(ts)
	if ts == "" || ts == "n/a" {
		return ""
	}
	// 依次尝试常见格式；systemd 的时间戳带星期前缀，先剥掉。
	trimmed := ts
	if i := strings.Index(ts, " "); i > 0 && len(ts[:i]) == 3 {
		trimmed = ts[i+1:]
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05 MST",
		"2006-01-02 15:04:05",
		"Mon 2006-01-02 15:04:05 MST",
	} {
		if t, err := time.Parse(layout, trimmed); err == nil {
			return humanizeDuration(time.Since(t))
		}
	}
	return ts
}

// humanizeDuration 粗粒度渲染时长；运维只需量级，不必精确到秒。
func humanizeDuration(d time.Duration) string {
	if d < 0 {
		return ""
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return itoa(days) + "d" + itoa(hours) + "h"
	case hours > 0:
		return itoa(hours) + "h" + itoa(mins) + "m"
	default:
		return itoa(mins) + "m"
	}
}

// itoa 是 strconv.Itoa 的短别名，仅本文件内使用。
func itoa(n int) string { return strconv.Itoa(n) }
