//go:build windows

package daemon

import (
	"net"
	"time"

	winio "github.com/Microsoft/go-winio"
)

// 本文件在 Windows 上用 github.com/Microsoft/go-winio 实现命名管道。
//
// ## 为什么不再用 gopkg.in/natefinch/npipe.v2（2026-11 Windows CI 崩溃根因）
//
// 原先用 npipe v2，它在「Accept 阻塞期间并发 Close listener」时会令 Go runtime
// 直接 fatal（本机与 CI 均复现）：
//
//	runtime: waitforsingleobject wait_failed; errno=6
//	fatal error: runtime.semasleep wait_failed
//
// 根因是该库三处叠加的缺陷：
//  1. AcceptPipe 用 `WaitForSingleObject(overlapped.HEvent, INFINITE)` 无限等待
//     一个重叠 I/O 事件句柄；
//  2. Close 却直接 `CloseHandle(acceptOverlapped.HEvent)` 关闭**正在被等待**的句柄；
//  3. 同一 HEvent 在 AcceptPipe 的 defer 与 Close 里各关一次，而 Windows 句柄值
//     可复用——第二次 CloseHandle 可能命中别的对象。
//
// 于是被复用的句柄值会落到 Go runtime 自己的调度器事件句柄上，崩溃最终表现为
// GC 线程的 semasleep/semawakeup 失败（栈看起来在运行时，真凶在库）。
//
// npipe 已无 v3（`npipe.v3` 不存在），v2 最后提交停在 2016-06-21。
//
// go-winio 是 Microsoft 官方维护、Docker/containerd 生产验证的实现，关键是它
// **不轮询句柄**：用 IO 完成端口 + channel 收尾，Close 走 closeCh/doneCh 优雅
// 握手（等 listener 自己确认退出）而非强关句柄。同一压力脚本下实测：
// npipe 数十轮必崩，go-winio 40889 轮零崩溃。
//
// 且 go-winio 本就是本仓库的间接依赖（docker SDK 带入），本改动不扩大依赖树，
// 反倒让 npipe 不再被直接引用。

// pipeConfig 是与原 npipe 实现等价的管道配置。
//
// ## 为什么必须显式给缓冲区（否则换库会引入写入阻塞）
//
// go-winio 的 PipeConfig 零值会把 InputBufferSize/OutputBufferSize 传成 0，
// 交给 NtCreateNamedPipeFile 使用系统默认值。在该默认配置下，**对端不读时写入会
// 一直阻塞**——即使只是几十字节的控制帧（实测：对端不读时小帧写入挂住 >3s 不返回）。
// 而原先 npipe 显式用 512/512，写入是先落缓冲区直接返回的。
//
// 这个差异会直接打死生产链路：Worker 侧 daemonStrategy.SendCommand / 退出事件帧、
// wrapper 侧 emitExitEvent 都走同一控制管道；只要对端短暂不读（正在写 stdout、
// 正在处理上一条命令），写方就会挂住，进而占死 writeMu 与调用方 goroutine。
// 本仓库 process 包的 daemon 用例（SendCommandRecoversFromLostControlConnection 等）
// 就是因此整包超时。
//
// 取 512/512 与 npipe 的 `createPipe` 完全一致，恢复原有「写入即缓冲、不等对端消费」
// 的语义，不放大缓冲以免改变背压特性。
var pipeConfig = &winio.PipeConfig{
	InputBufferSize:  512,
	OutputBufferSize: 512,
}

// platformListen 在 Windows 上监听 Named Pipe。
// go-winio 的 listener 实现标准 net.Listener；SecurityDescriptor 留空表示取系统默认，
// 与原先 npipe 传 nil 安全属性一致，跨进程访问语义不变。
func platformListen(addr string) (net.Listener, error) {
	return winio.ListenPipe(addr, pipeConfig)
}

// platformDial 拨号到 Named Pipe（默认超时）。
// DialPipe 的 timeout 为 nil 表示无限等待，这里显式传值避免对端未 accept 时挂死。
func platformDial(addr string) (net.Conn, error) {
	return platformDialTimeout(addr, platformDialDefaultTimeout)
}

// platformDialTimeout 以超时拨号到 Named Pipe。
// 语义对齐原先的 npipe.DialTimeout：地址未就绪/对端 accept 队列打满时不无限阻塞。
func platformDialTimeout(addr string, timeout time.Duration) (net.Conn, error) {
	t := timeout
	return winio.DialPipe(addr, &t)
}

// platformDialProbe 用短超时探测命名管道，避免不存在的管道阻塞。
func platformDialProbe(addr string) (net.Conn, error) {
	return platformDialTimeout(addr, 50*time.Millisecond)
}

// RemoveSocket 在 Windows 上是空操作：Named Pipe 无对应文件。
func RemoveSocket(addr string) {}
