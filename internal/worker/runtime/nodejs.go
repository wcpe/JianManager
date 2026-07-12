package runtime

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	goruntime "runtime"
	"strconv"
	"strings"
)

// nodeMirrorBase 返回 Node.js dist 下载基址：CP 下发的 override 优先（来自平台设置
// runtime.mirror.nodejs，语义同 jdk.mirror.*），其次环境变量，最后回退官方源（FR-299）。
func nodeMirrorBase(override string) string {
	if v := strings.TrimSpace(override); v != "" {
		return strings.TrimRight(v, "/")
	}
	if v := strings.TrimSpace(os.Getenv("JIANMANAGER_NODEJS_DIST_BASE")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://nodejs.org/dist"
}

// resolveNodeVersion 经 <base>/index.json 解析指定 major 的最新版本（返回不带 v 前缀，
// 如 "22.17.0"）。index.json 含 lts 标记（string|false），按 major 取最高 X.Y.Z。
// 该 major 无任何条目时返回明确错误（喂给任务失败原因）。
func resolveNodeVersion(client *http.Client, base string, major int) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Get(base + "/index.json")
	if err != nil {
		return "", fmt.Errorf("获取 Node.js 版本索引失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("Node.js 版本索引返回 HTTP %d", resp.StatusCode)
	}
	var entries []struct {
		Version string `json:"version"` // "v22.17.0"
	}
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return "", fmt.Errorf("解析 Node.js 版本索引失败: %w", err)
	}

	var best string
	var bestParts [3]int
	for _, e := range entries {
		parts, ok := parseSemver(strings.TrimPrefix(e.Version, "v"))
		if !ok || parts[0] != major {
			continue
		}
		if best == "" || semverLess(bestParts, parts) {
			best = strings.TrimPrefix(e.Version, "v")
			bestParts = parts
		}
	}
	if best == "" {
		return "", fmt.Errorf("Node.js 版本索引中无 major=%d 的可用版本", major)
	}
	return best, nil
}

// parseSemver 解析 "X.Y.Z" 为整数三元组（缺段按 0）。
func parseSemver(v string) ([3]int, bool) {
	var out [3]int
	segs := strings.SplitN(v, ".", 3)
	if len(segs) == 0 || segs[0] == "" {
		return out, false
	}
	for i, s := range segs {
		if i >= 3 {
			break
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// semverLess 报告 a < b（逐段比较）。
func semverLess(a, b [3]int) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// nodeOSName 返回 nodejs dist 归档命名的平台段（linux / win / darwin）。
func nodeOSName() string {
	switch goruntime.GOOS {
	case "windows":
		return "win"
	case "darwin":
		return "darwin"
	}
	return "linux"
}

// nodeArchiveExt 返回按平台的归档后缀（windows zip，其它 tar.gz）。
func nodeArchiveExt() string {
	if goruntime.GOOS == "windows" {
		return "zip"
	}
	return "tar.gz"
}

// nodeArchiveURL 构造归档下载 URL：<base>/v<ver>/node-v<ver>-<os>-<arch>.<ext>。
func nodeArchiveURL(base, version, arch string) string {
	return fmt.Sprintf("%s/v%s/node-v%s-%s-%s.%s", base, version, version, nodeOSName(), arch, nodeArchiveExt())
}
