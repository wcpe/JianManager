// Command jmctl 是 JianManager 的紧急控制台 CLI（FR-184，见 ADR-041）。
//
// 当 Control Plane 与 Worker Node 同时不可用时，daemon wrapper（ADR-003）仍托管着运行中的
// 游戏服进程，并在本机暴露一个 Unix Socket / Windows 命名管道（二进制帧协议）。jmctl 绕过整个
// 栈、纯本机、依赖极少地直连该 socket，做「最后一公里」运维：观察控制台输出、发指令、优雅停服 /
// 强杀。它只链 internal/worker/daemon 帧协议包，不引入 gRPC / 数据库 / Worker 服务 / CP。
//
// 安全模型（ADR-041 §3）：纯本机、无网络面、不额外鉴权——能在本机读写守护进程 socket 即等同
// 宿主级运维权限。浏览器/网络永不直触该 socket 的架构不变量不变。
package main

import (
	"flag"
	"fmt"
	"os"
)

// usage 打印命令总览。
func usage() {
	fmt.Fprint(os.Stderr, `jmctl — JianManager 紧急控制台（本机直连守护进程，CP/Worker 不可用时应急用）

用法:
  jmctl list      [--pid-dir DIR]                         列出本机全部 daemon 实例
  jmctl emergency [--instance <uuid 前缀>] [--pid-dir DIR] 交互式紧急终端（Ctrl+C 优雅停服，连按两次强杀）
  jmctl stop      <uuid 前缀> [--pid-dir DIR]              单发优雅停服后退出
  jmctl kill      <uuid 前缀> [--pid-dir DIR]              单发强制终止后退出

所有 <uuid 前缀> 支持唯一前缀补全（类 docker/git 短 ID）。
PID 目录默认取数据根下 var/servers（与 Worker 实际写入路径对齐）：
  --pid-dir > $JIANMANAGER_DATA_DIR/var/servers > ./data/var/servers
`)
}

// newFlagSet 构造子命令的 FlagSet：ContinueOnError + 统一 usage，便于把解析错误返回给 main 统一处理。
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = usage
	return fs
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	sub := os.Args[1]
	args := os.Args[2:]

	var err error
	switch sub {
	case "list":
		err = runList(args)
	case "emergency":
		err = runEmergency(args)
	case "stop":
		err = runStop(args)
	case "kill":
		err = runKill(args)
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s\n\n", sub)
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		os.Exit(1)
	}
}
