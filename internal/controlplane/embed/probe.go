package embed

import "embed"

// probeFS 内嵌 ServerProbe 监控探针 jar（FR-010 建服自动部署）。
// jar 为构建产物，经 `make embed-probe` 注入到 embed/probe/；目录内的 .gitignore
// 保证未注入 jar 时该目录依然存在、go:embed 不致编译失败。
//
//go:embed all:probe
var probeFS embed.FS

// ServerProbeJar 返回内嵌的 ServerProbe 探针 jar 字节。
// 未经 `make embed-probe` 注入（jar 缺失）时返回 nil，调用方据此优雅跳过自动部署
// （探针端口仍已分配，运维手动放入探针 jar 后 Worker 抓取即生效）。
func ServerProbeJar() []byte {
	b, err := probeFS.ReadFile("probe/ServerProbe.jar")
	if err != nil {
		return nil
	}
	return b
}
