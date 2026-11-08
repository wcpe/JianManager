//go:build race

package vlsup

// raceEnabled 在 -race 构建下为 true。
//
// 用途：让**依赖绝对时间预算**的用例在竞态检测下放宽预算。与 stateindex / query 的同名开关口径一致。
//
// 当前唯一使用点：`TestExecHandleNormalStopIsSingleWaitAndExitZero` —— 它让 8 个 goroutine
// 并发调 `Stop(time.Second)`，并断言「正常停止 ⇒ ExitCode==0」。而 `realProcess.Stop` 的语义是
// 「`grace` 内没等到进程退出就 `Kill()` ⇒ ExitCode=-1」。竞态检测器把整条路径放慢后，1 秒
// 往往不够子进程收尾，于是成了 `-1`——除非这个差值就是被测对象，否则它只反映插桩开销。
//
// **只放宽时间预算，不放宽断言**：`Stopped`/`ExitCodeKnown`/`ExitCode==0` 三条在两种构建下都查。
const raceEnabled = true
