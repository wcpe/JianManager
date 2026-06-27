package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wcpe/JianManager/internal/platform/dataroot"
	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// instanceInfo 是 jmctl 从一份 <uuid>.pid 记录还原出的本机受管实例视图。
// 仅含展示与寻址所需字段，不引入 Worker 的任何重量级状态。
type instanceInfo struct {
	UUID       string // 实例 UUID
	WrapperPID int    // daemon wrapper 进程 PID
	JavaPID    int    // 被托管的 Java 进程 PID（诊断/展示）
	SocketAddr string // Unix Socket 路径 / Named Pipe 名称
	WorkDir    string // 实例工作目录
	Alive      bool   // wrapper 进程是否存活（IsPIDAlive 探测）
}

// resolvePIDDir 解析 daemon PID 文件目录，与 Worker 实际写入路径对齐（ADR-010 / ADR-041 §1）。
//
// Worker 把 <uuid>.pid 写到「服务器工作目录根」= 数据根下 var/servers（process.Manager 的
// pidDir 即 serversDir，见 cmd/worker/main.go）。jmctl 据此发现实例，故按以下优先级解析：
//  1. flag（--pid-dir）显式指定；
//  2. 环境变量 JIANMANAGER_DATA_DIR 指向的数据根下 var/servers；
//  3. 默认数据根 ./data 下 var/servers（开发环境）。
//
// 2/3 两档要求目录实际存在才采用，否则继续回退；全部落空报错并提示用 --pid-dir。
func resolvePIDDir(flag string) (string, error) {
	if strings.TrimSpace(flag) != "" {
		return flag, nil
	}

	// JIANMANAGER_DATA_DIR 优先；为空时 dataroot 回退到默认 ./data。
	// Resolve 不创建目录，仅求绝对路径，由下方存在性判断决定是否采用。
	if root, err := dataroot.Resolve(""); err == nil {
		serversDir := root.ServersDir()
		if dirExists(serversDir) {
			return serversDir, nil
		}
	}

	return "", fmt.Errorf("未找到 daemon PID 目录（数据根下 var/servers 不存在）；请用 --pid-dir 显式指定")
}

// dirExists 判断路径是否为已存在目录。
func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// scanInstances 扫描 pidDir 下所有 *.pid，还原为实例视图（含存活探测）。
// 损坏/无法解析的 PID 文件被跳过（不让单个坏文件阻断整次列举）。
// 结果按 UUID 升序，保证输出与前缀匹配的确定性。
func scanInstances(pidDir string) ([]instanceInfo, error) {
	entries, err := os.ReadDir(pidDir)
	if err != nil {
		return nil, fmt.Errorf("读取 PID 目录 %s 失败: %w", pidDir, err)
	}

	var insts []instanceInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pid") {
			continue
		}
		pf := daemon.NewPIDFile(filepath.Join(pidDir, e.Name()))
		rec, err := pf.ReadRecord()
		if err != nil {
			continue // 跳过损坏记录
		}
		uuid := rec.InstanceUUID
		if uuid == "" {
			// 旧版裸 PID 文件无 uuid 字段，用文件名（去 .pid）兜底。
			uuid = strings.TrimSuffix(e.Name(), ".pid")
		}
		insts = append(insts, instanceInfo{
			UUID:       uuid,
			WrapperPID: rec.WrapperPID,
			JavaPID:    rec.JavaPID,
			SocketAddr: rec.SocketAddr,
			WorkDir:    rec.WorkDir,
			Alive:      daemon.IsPIDAlive(rec.WrapperPID),
		})
	}
	sort.Slice(insts, func(i, j int) bool { return insts[i].UUID < insts[j].UUID })
	return insts, nil
}

// resolvePrefix 在实例集合中按 UUID 唯一前缀解析目标（类 docker/git 短 ID）。
// 唯一匹配返回该实例；多个匹配报错并列出候选；无匹配 / 空前缀报错。
func resolvePrefix(insts []instanceInfo, prefix string) (instanceInfo, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return instanceInfo{}, fmt.Errorf("实例前缀为空；请提供 UUID 或其唯一前缀")
	}
	var matches []instanceInfo
	for _, in := range insts {
		if strings.HasPrefix(in.UUID, prefix) {
			matches = append(matches, in)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return instanceInfo{}, fmt.Errorf("没有实例的 UUID 以 %q 开头", prefix)
	default:
		var uuids []string
		for _, m := range matches {
			uuids = append(uuids, m.UUID)
		}
		return instanceInfo{}, fmt.Errorf("前缀 %q 匹配到多个实例，请补足以唯一确定：\n  %s",
			prefix, strings.Join(uuids, "\n  "))
	}
}

// runList 实现 `jmctl list`：扫描并打印本机全部 daemon 实例（非交互）。
func runList(args []string) error {
	fs := newFlagSet("list")
	pidDirFlag := fs.String("pid-dir", "", "daemon PID 目录（默认数据根下 var/servers）")
	if err := fs.Parse(args); err != nil {
		return err
	}

	pidDir, err := resolvePIDDir(*pidDirFlag)
	if err != nil {
		return err
	}
	insts, err := scanInstances(pidDir)
	if err != nil {
		return err
	}
	if len(insts) == 0 {
		fmt.Printf("PID 目录 %s 下无受管 daemon 实例\n", pidDir)
		return nil
	}

	fmt.Printf("PID 目录: %s\n", pidDir)
	fmt.Printf("%-36s  %-6s  %-10s  %-10s  %s\n", "UUID", "存活", "WrapperPID", "JavaPID", "工作目录")
	for _, in := range insts {
		alive := "否"
		if in.Alive {
			alive = "是"
		}
		fmt.Printf("%-36s  %-6s  %-10d  %-10d  %s\n", in.UUID, alive, in.WrapperPID, in.JavaPID, in.WorkDir)
	}
	return nil
}
