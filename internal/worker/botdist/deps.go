package botdist

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// requiredDeps bot-worker 运行必需、但不随归档分发的依赖（经 FR-307 节点受控包根安装）。
var requiredDeps = []string{"mineflayer", "mineflayer-pathfinder"}

// GlobalNodeModulesDir 返回节点受控依赖根的 node_modules 链接目标（见 ADR-072）。
// 只有新根包含完整 Bot 依赖时才切换；否则继续兼容完整的旧 npm 真全局根。
func GlobalNodeModulesDir(runtimesRoot string) string {
	candidates := GlobalNodeModulesCandidates(runtimesRoot)
	if len(missingRequiredDeps(candidates[0])) == 0 {
		return candidates[0]
	}
	if len(missingRequiredDeps(candidates[1])) == 0 {
		return candidates[1]
	}
	return candidates[0]
}

// GlobalNodeModulesCandidates 返回链接选择与 NODE_PATH 候选，新受控根在前、旧真全局目录在后。
func GlobalNodeModulesCandidates(runtimesRoot string) []string {
	return []string{
		filepath.Join(runtimesRoot, "global", "node_modules"),
		filepath.Join(runtimesRoot, "global", "lib", "node_modules"),
	}
}

func missingRequiredDeps(dir string) []string {
	missing := make([]string, 0, len(requiredDeps))
	for _, dep := range requiredDeps {
		if fi, err := os.Stat(filepath.Join(dir, dep)); err != nil || !fi.IsDir() {
			missing = append(missing, dep)
		}
	}
	return missing
}

// NodePathEnv 组装 NODE_PATH 环境变量（CJS 依赖解析兜底；ESM 靠 node_modules 链接）。
func NodePathEnv(candidates []string) string {
	return "NODE_PATH=" + strings.Join(candidates, string(os.PathListSeparator))
}

// RefreshNodeModulesLink 按当前新旧根完整性刷新受控 dist 的 ESM 依赖链接。
// 新根未完整时继续沿用完整旧根，避免安装中间态切断既有 Bot。
func RefreshNodeModulesLink(distDir, runtimesRoot string) error {
	return ensureNodeModulesLink(distDir, GlobalNodeModulesDir(runtimesRoot))
}

func esmNodeModulesDirs(start string) []string {
	dir := filepath.Clean(start)
	var dirs []string
	for {
		dirs = append(dirs, filepath.Join(dir, "node_modules"))
		parent := filepath.Dir(dir)
		if parent == dir {
			return dirs
		}
		dir = parent
	}
}

// CheckDeps spawn 前只预检 ESM 从入口脚本目录向上的 node_modules 祖先链。
// 裸受控根必须先经链接进入这条可见路径，不能作为额外候选直接放行。
func CheckDeps(distDir string) error {
	searchDirs := esmNodeModulesDirs(distDir)
	bestMissing := append([]string(nil), requiredDeps...)
	for _, dir := range searchDirs {
		missing := missingRequiredDeps(dir)
		if len(missing) == 0 {
			return nil
		}
		if len(missing) < len(bestMissing) {
			bestMissing = missing
		}
	}
	return fmt.Errorf("bot 依赖未在同一目录完整安装：请到节点『全局包管理』重新安装 mineflayer 与 mineflayer-pathfinder（缺少 %s）",
		strings.Join(bestMissing, "、"))
}
