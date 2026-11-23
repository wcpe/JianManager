//go:build darwin

package daemon

// unixSocketPathMax 是 macOS 上 Unix domain socket 路径的字节上限。
//
// 为什么与 Linux 取值不同：Linux 的 sockaddr_un.sun_path 为 108 字节，
// 而 BSD 系（含 macOS）为 104 字节。本常量是**预检阈值**，取值口径与 Linux 一致：
// 只在「确定不可能成功」时提前拦截，宁可放行让内核判定，也不误拦可用目录。
//
// 为什么取 103 而不是 104：保留结尾 NUL。与 Linux 侧取 107（而非 108）同一口径。
//
// 注意：该值未在真机 macOS 上实测过（本仓库无 darwin 运行环境）。若将来在 macOS
// 上发现 103 误拦了本来可用的路径，按「宁放行」原则下调即可 —— 预检的目的只是
// 把内核那句不指向根因的 "bind: invalid argument" 换成可读诊断，不是追求精确边界。
const unixSocketPathMax = 103
