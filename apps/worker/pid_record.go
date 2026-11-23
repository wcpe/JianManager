package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// writeWorkerPIDRecord 写 Worker 自身的 PID 记录，路径为 <安装目录>/logs/worker.pid
// （即 data 目录的同级 logs/，与生产既有布局一致）。
//
// 记录字段与 CP 的 cp-lifecycle.sh 同口径：pid + starttime + exe + cwd 可直接与 /proc 核对，
// 从而区分「该 PID 仍是这个 Worker」与「PID 已被系统复用」——这是纯 PID 数字做不到的。
//
// 为什么需要（2026-09-28 生产排查）：worker 目录里遗留过一份停在多日前的 worker.pid，
// 内容指向早已退出的 PID；任何按它识别进程的生命周期脚本都会认错进程（甚至误判「在运行」）。
// 由 Worker 启动时原子覆盖写，可保证该文件始终指向当前进程；写失败仅告警，不影响启动。
//
// 非 Linux 平台没有 /proc：starttime/exe/cwd 留空（读者按可得的字段校验），文件仍会维护。
func writeWorkerPIDRecord(dataDir string, wsPort int, configPath string) error {
	trimmed := strings.TrimRight(dataDir, string(os.PathSeparator))
	if trimmed == "" {
		return fmt.Errorf("worker pid record: empty data dir")
	}
	logDir := filepath.Join(filepath.Dir(trimmed), "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}

	exe, _ := os.Executable()
	cwd, _ := os.Getwd()
	record := strings.Join([]string{
		fmt.Sprintf("pid=%d", os.Getpid()),
		"starttime=" + procStartTime(os.Getpid()),
		"exe=" + procLink("/proc/self/exe"),
		"cwd=" + firstNonEmpty(procLink("/proc/self/cwd"), cwd),
		"binary=" + exe,
		"config=" + configPath,
		"data_dir=" + trimmed,
		fmt.Sprintf("ws_port=%d", wsPort),
		"",
	}, "\n")

	// 原子写：同目录临时文件 + rename，权限 0600（记录含进程路径信息，与 CP 的 umask 077 一致）。
	tmp, err := os.CreateTemp(logDir, "worker.pid.tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // 成功 rename 后此处为无操作
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(record); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(logDir, "worker.pid"))
}

// procStartTime 返回 /proc/<pid>/stat 的第 22 个字段（进程启动时刻，单位 clock tick）。
// 与 cp-lifecycle.sh 的 proc_starttime 同口径：先去掉含空格的 comm 字段（到最后一个 ')'），
// 再数第 20 个字段（即全字段的第 22 个）。非 Linux 或读取失败返回空串。
func procStartTime(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	s := string(b)
	idx := strings.LastIndex(s, ")")
	if idx < 0 {
		return ""
	}
	fields := strings.Fields(s[idx+1:])
	const startTimeIndexAfterComm = 19 // 第 3 个字段起算，第 22 个即索引 19
	if len(fields) <= startTimeIndexAfterComm {
		return ""
	}
	return fields[startTimeIndexAfterComm]
}

// procLink 返回符号链接目标（/proc 下的 exe/cwd），失败返回空串。
func procLink(path string) string {
	target, err := os.Readlink(path)
	if err != nil {
		return ""
	}
	return target
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
