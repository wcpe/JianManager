// Package embed 内嵌 Worker 侧可选构建产物（CFR 反编译器 jar 等）。
package embed

import "embed"

// cfrFS 内嵌 CFR 反编译器 jar（FR-075 反编译，见 ADR-018）。
// jar 为构建产物，经 `make embed-cfr` 注入到 embed/cfr/；目录内的 .gitignore
// 保证未注入 jar 时该目录依然存在、go:embed 不致编译失败（jar 不入库，~2MB）。
//
//go:embed all:cfr
var cfrFS embed.FS

// CFRJar 返回内嵌的 CFR 反编译器 jar 字节。
// 未经 `make embed-cfr` 注入（jar 缺失）时返回 nil，调用方据此回退到
// 数据根缓存 / 按需下载（见 decompiler.Provider）。
func CFRJar() []byte {
	b, err := cfrFS.ReadFile("cfr/cfr.jar")
	if err != nil {
		return nil
	}
	return b
}
