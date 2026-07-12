package pkgmgr

import "runtime"

// corepackName 返回 corepack 可执行名（Windows 为 .cmd shim）。
func corepackName() string {
	if runtime.GOOS == "windows" {
		return "corepack.cmd"
	}
	return "corepack"
}

// pmExeName 返回包管理器可执行名（Windows 为 .cmd shim）。npm 随 Node 自带。
func pmExeName(pm string) string {
	if runtime.GOOS == "windows" {
		return pm + ".cmd"
	}
	return pm
}
