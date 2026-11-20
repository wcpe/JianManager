package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/wcpe/JianManager/internal/platform/dataroot"
	"github.com/wcpe/JianManager/internal/version"
)

// `jmctl server status`：打印本机这套 JianManager 部署的关键信息。
//
// 定位：运维在服务器上敲一条命令，就能看到「装在哪、跑没跑、数据多大、日志在哪」，
// 不必回忆部署时的目录或去翻 systemd。
//
// 全程只读本地文件，**不连 Control Plane**：CP 挂掉正是最需要这条命令的时候。
// Worker 注册信息存在 CP 的数据库里，本机拿不到，因此不在此展示——想知道有哪些节点，
// 打开面板或（CP 正常时）用 `jm api GET api/v1/nodes`。

// serverStatus 是汇总结果，便于 JSON 输出与测试断言。
type serverStatus struct {
	Version     string         `json:"version"`
	OS          string         `json:"os"`
	Arch        string         `json:"arch"`
	DataRoot    string         `json:"dataRoot"`
	DataRootOK  bool           `json:"dataRootExists"`
	Layout      layoutStatus   `json:"layout"`
	Configs     []configStatus `json:"configs"`
	Instances   instanceCount  `json:"instances"`
	DiskUsageMB int64          `json:"diskUsageMb,omitempty"`
	Binary      string         `json:"binary"`
	Warnings    []string       `json:"warnings,omitempty"`
}

// layoutStatus 记录标准目录是否存在——排查「数据跑哪去了」的第一手信息。
type layoutStatus struct {
	Bin     bool `json:"bin"`
	Etc     bool `json:"etc"`
	Servers bool `json:"servers"`
	Log     bool `json:"log"`
}

// configStatus 是一份配置文件的探测结果。
type configStatus struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
}

// instanceCount 是本机受管实例的统计。
type instanceCount struct {
	Total int `json:"total"`
	Alive int `json:"alive"`
}

// runServer 分发 `jmctl server <sub>`。
func runServer(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(os.Stderr, `用法: jmctl server <子命令>

  status [--data-dir DIR] [--json]   打印本机部署概况（版本 / 目录 / 配置 / 实例 / 占用）

说明:
  - 只读本地文件，不连 Control Plane；CP 不可用时依然可用
  - Worker 节点列表不在本机，请看面板或用 jm api GET api/v1/nodes
  - --data-dir 覆盖数据根，优先级高于 JIANMANAGER_DATA_DIR
`)
		return nil
	}
	switch args[0] {
	case "status":
		return runServerStatus(args[1:])
	default:
		return fmt.Errorf("未知 server 子命令 %q；支持 status", args[0])
	}
}

// runServerStatus 采集并打印部署概况。
func runServerStatus(args []string) error {
	dataDir := ""
	asJSON := false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--data-dir":
			if i+1 >= len(args) {
				return fmt.Errorf("--data-dir 需要参数")
			}
			i++
			dataDir = args[i]
		case strings.HasPrefix(args[i], "--data-dir="):
			dataDir = strings.TrimPrefix(args[i], "--data-dir=")
		case args[i] == "--json":
			asJSON = true
		default:
			return fmt.Errorf("未知参数 %q；用法: jmctl server status [--data-dir DIR] [--json]", args[i])
		}
	}

	st := collectServerStatus(dataDir)

	if asJSON {
		return printJSONValue(st)
	}
	printServerStatus(st)
	return nil
}

// collectServerStatus 采集全部信息。
//
// 数据根解析失败不视为致命错误：那本身就是最需要报告的信息，
// 因此照常输出其余字段，并把原因记进 Warnings。
func collectServerStatus(dataDir string) serverStatus {
	st := serverStatus{
		Version: version.Version,
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		Binary:  executablePath(),
	}

	root, err := dataroot.Resolve(dataDir)
	if err != nil {
		st.Warnings = append(st.Warnings, "解析数据根失败: "+err.Error())
		return st
	}
	st.DataRoot = root.Base()
	st.DataRootOK = dirExists(root.Base())

	st.Layout = layoutStatus{
		Bin:     dirExists(root.BinDir()),
		Etc:     dirExists(root.EtcDir()),
		Servers: dirExists(root.ServersDir()),
		Log:     dirExists(root.LogDir()),
	}

	// 两份配置文件并列探测：CP 与 Worker 可同机也可分离，缺哪份就说明没装哪个组件。
	for _, name := range []string{"control-plane.yml", "worker.yml"} {
		p := filepath.Join(root.EtcDir(), name)
		st.Configs = append(st.Configs, configStatus{Name: name, Path: p, Exists: fileExists(p)})
	}

	// 实例统计复用 list 的扫描逻辑，口径与 `jmctl list` 保持一致。
	if st.Layout.Servers {
		if insts, err := scanInstances(root.ServersDir()); err == nil {
			st.Instances.Total = len(insts)
			for _, it := range insts {
				if it.Alive {
					st.Instances.Alive++
				}
			}
		} else {
			st.Warnings = append(st.Warnings, "扫描实例失败: "+err.Error())
		}
	}

	st.DiskUsageMB = 0
	if st.DataRootOK {
		st.DiskUsageMB = dirSizeMB(root.Base())
	}

	if !st.DataRootOK {
		st.Warnings = append(st.Warnings, "数据根不存在："+st.DataRoot+"（尚未初始化，或 --data-dir 指错了）")
	}
	if !st.Layout.Etc && st.DataRootOK {
		st.Warnings = append(st.Warnings, "缺少 etc/ 目录：组件可能装在别处，或用默认配置运行")
	}
	installed := 0
	for _, c := range st.Configs {
		if c.Exists {
			installed++
		}
	}
	if st.DataRootOK && installed == 0 {
		st.Warnings = append(st.Warnings, "etc/ 下未找到 control-plane.yml 或 worker.yml：若组件装在其他目录，请用 --data-dir 指向其数据根")
	}
	return st
}

// printServerStatus 以易读形式打印。
func printServerStatus(st serverStatus) {
	fmt.Printf("版本:     %s (%s/%s)\n", st.Version, st.OS, st.Arch)
	fmt.Printf("可执行:   %s\n", orDash(st.Binary))
	fmt.Printf("数据根:   %s", orDash(st.DataRoot))
	if st.DataRootOK {
		fmt.Printf(" (%.1f MB)\n", float64(st.DiskUsageMB))
	} else {
		fmt.Printf(" [不存在]\n")
	}

	fmt.Printf("目录:     bin=%s etc=%s var/servers=%s var/log=%s\n",
		mark(st.Layout.Bin), mark(st.Layout.Etc), mark(st.Layout.Servers), mark(st.Layout.Log))

	fmt.Printf("配置:\n")
	for _, c := range st.Configs {
		if c.Exists {
			fmt.Printf("  ✓ %-18s %s\n", c.Name, c.Path)
		} else {
			fmt.Printf("  · %-18s (未安装)\n", c.Name)
		}
	}

	if st.Layout.Servers {
		fmt.Printf("实例:     共 %d 个，存活 %d 个\n", st.Instances.Total, st.Instances.Alive)
	}

	if len(st.Warnings) > 0 {
		fmt.Printf("\n需要注意:\n")
		for _, w := range st.Warnings {
			fmt.Printf("  · %s\n", w)
		}
	}
}

// mark 把布尔渲染成勾/叉，便于扫读。
func mark(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}

// orDash 空值显示为短横线。
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// executablePath 返回当前二进制的绝对路径（解析符号链接后的真实位置）。
//
// 应急场景里「我正在跑的是哪一份 jmctl」很关键：机器上可能同时存在
// 多个版本（旧布局 /opt 与新布局并存），报出真实路径才便于判断。
func executablePath() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return p
}

// printJSONValue 以缩进 JSON 输出任意结构。
func printJSONValue(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化输出失败: %w", err)
	}
	fmt.Println(string(b))
	return nil
}

// fileExists 判断普通文件是否存在。
func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// dirSizeMB 估算目录占用（MB）。
//
// 只读元数据不做全量遍历排序，且容忍权限错误：生产数据根里可能有 root 拥有的
// 子目录，普通用户跑 status 时不该因此失败。
func dirSizeMB(root string) int64 {
	var total int64
	_ = filepath.Walk(root, func(_ string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil // 跳过无权限项，继续统计其余部分
		}
		if !fi.IsDir() {
			total += fi.Size()
		}
		return nil
	})
	return total / (1024 * 1024)
}

// sortedConfigNames 供测试稳定断言顺序。
func sortedConfigNames(cs []configStatus) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name)
	}
	sort.Strings(out)
	return out
}
