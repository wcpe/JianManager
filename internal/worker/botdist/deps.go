package botdist

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// requiredDeps bot-worker 运行必需、但不随归档分发的依赖（经 FR-307 全局包安装）。
var requiredDeps = []string{"mineflayer", "mineflayer-pathfinder"}

// GlobalNodeModulesDir 返回 FR-307 托管全局包的 node_modules 布局目录（node_modules 链接目标）。
// npm --global --prefix 的落盘布局按平台不同：Windows 在 <prefix>/node_modules，
// 其余在 <prefix>/lib/node_modules。
func GlobalNodeModulesDir(runtimesRoot string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(runtimesRoot, "global", "node_modules")
	}
	return filepath.Join(runtimesRoot, "global", "lib", "node_modules")
}

// GlobalNodeModulesCandidates 返回两种平台布局的候选（NODE_PATH/依赖预检用，宁多勿漏）。
func GlobalNodeModulesCandidates(runtimesRoot string) []string {
	return []string{
		filepath.Join(runtimesRoot, "global", "lib", "node_modules"),
		filepath.Join(runtimesRoot, "global", "node_modules"),
	}
}

// NodePathEnv 组装 NODE_PATH 环境变量（CJS 依赖解析兜底；ESM 靠 node_modules 链接）。
func NodePathEnv(candidates []string) string {
	return "NODE_PATH=" + strings.Join(candidates, string(os.PathListSeparator))
}

// CheckDeps spawn 前预检 bot 运行时依赖是否可解析：dist 同级 node_modules（链接/实体）、
// 旧布局的上级 node_modules（仓库式部署 bot-worker/node_modules）、托管全局候选，任一命中即可。
// 缺失时返回可操作指引而非让子进程以 ERR_MODULE_NOT_FOUND 裸崩。
func CheckDeps(distDir string, globalCandidates []string) error {
	searchDirs := append([]string{
		filepath.Join(distDir, "node_modules"),
		filepath.Join(filepath.Dir(distDir), "node_modules"),
	}, globalCandidates...)
	var missing []string
	for _, dep := range requiredDeps {
		found := false
		for _, dir := range searchDirs {
			if fi, err := os.Stat(filepath.Join(dir, dep)); err == nil && fi.IsDir() {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, dep)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("bot 依赖未安装：请到节点『全局包管理』安装 mineflayer 与 mineflayer-pathfinder（缺少 %s）",
			strings.Join(missing, "、"))
	}
	return nil
}
