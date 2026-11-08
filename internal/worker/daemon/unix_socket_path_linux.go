//go:build linux

package daemon

// unixSocketPathMax 是 Linux 上 Unix domain socket 路径的字节上限。
//
// sockaddr_un.sun_path 为 108 字节（含结尾 NUL）。实测 111 字节仍能 bind 成功，
// 说明内核实际容忍度高于结构体字面值，故此处取 107：
// 只在「确定不可能成功」时提前拦截，不做理论上更严格但会误伤可用目录的收紧。
const unixSocketPathMax = 107
