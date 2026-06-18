package service

import (
	"encoding/json"
	"fmt"
	"strings"
)

// LaunchSpec 是 MC 实例的结构化启动规格（ADR-008）。
// 由「绑定 JDK + JVM 参数（内存/GC）+ 核心 jar + 额外 args」派生 java 启动命令，
// 取代用户手填的自由文本 start_command（根治 BUG-005 的引号/路径歧义）。
type LaunchSpec struct {
	// MemoryMb 堆内存（同时作为 -Xms 与 -Xmx），<=0 时不注入内存参数。
	MemoryMb int `json:"memoryMb"`
	// JvmArgs 额外 JVM 参数（如 -XX:+UseG1GC），位于内存参数之后、-jar 之前。
	JvmArgs []string `json:"jvmArgs"`
	// CoreJar 工作目录内的核心 jar 文件名（如 paper.jar），相对工作目录。
	CoreJar string `json:"coreJar"`
	// ExtraArgs 传给服务端的额外参数（`nogui` 已默认附加，无需重复）。
	ExtraArgs []string `json:"extraArgs"`
}

// parseLaunchSpec 解析实例存储的 LaunchSpec JSON。空字符串表示「无结构化启动」，
// 返回 (nil, nil)，调用方应回退到自由文本 start_command。
func parseLaunchSpec(raw string) (*LaunchSpec, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var spec LaunchSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return nil, fmt.Errorf("解析 launchSpec 失败: %w", err)
	}
	return &spec, nil
}

// deriveStartCommand 由 LaunchSpec 派生 java 启动命令：
//
//	java -Xms<m>M -Xmx<m>M <jvmArgs...> -jar <coreJar> nogui <extraArgs...>
//
// 命令使用 PATH 中的 `java`——Worker 启动时会按实例绑定的 jdk_path 把
// `<jdk>/bin` 接入 PATH（见 process/command.go ComposeEnv），从而选中正确的 JDK，
// 避免在命令里写死跨平台不一致的绝对路径。
func deriveStartCommand(spec *LaunchSpec) (string, error) {
	if spec == nil {
		return "", fmt.Errorf("launchSpec 为空")
	}
	if strings.TrimSpace(spec.CoreJar) == "" {
		return "", fmt.Errorf("launchSpec 缺少 coreJar")
	}

	parts := []string{"java"}
	if spec.MemoryMb > 0 {
		parts = append(parts, fmt.Sprintf("-Xms%dM", spec.MemoryMb), fmt.Sprintf("-Xmx%dM", spec.MemoryMb))
	}
	for _, a := range spec.JvmArgs {
		if a = strings.TrimSpace(a); a != "" {
			parts = append(parts, a)
		}
	}
	parts = append(parts, "-jar", quoteIfSpace(spec.CoreJar), "nogui")
	for _, a := range spec.ExtraArgs {
		if a = strings.TrimSpace(a); a != "" {
			parts = append(parts, a)
		}
	}
	return strings.Join(parts, " "), nil
}

// quoteIfSpace 当字符串含空白时用双引号包裹，保证 shell 把它当作单个参数。
func quoteIfSpace(s string) string {
	if strings.ContainsAny(s, " \t") {
		return `"` + s + `"`
	}
	return s
}
