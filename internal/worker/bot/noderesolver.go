/**
 * Node 可执行解析器（FR-300/FR-308）。
 * spawn bot-worker 前按显式配置、托管扫描、PATH 依次解析，并对每个候选执行真实版本探测。
 * 仅接受 Node.js >=22.13.0；托管候选按完整版本排序，失败结果不缓存。
 */

package bot

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/worker/runtimescan"
)

// Node 可执行解析来源（进 bot 启动日志，供真机核对选用了哪条链路）。
const (
	// NodeSourceExplicit 显式配置指定（bot 级可选 node 路径字段，V1 无 UI 只留结构）。
	NodeSourceExplicit = "explicit-config"
	// NodeSourceManagedScan 节点本地扫描发现。
	NodeSourceManagedScan = "managed-scan"
	// NodeSourcePathFallback 回退 PATH 里的 "node"。
	NodeSourcePathFallback = "path-fallback"
)

const nodeProbeTimeout = 5 * time.Second

var minimumBotNodeVersion = nodeVersion{major: 22, minor: 13, patch: 0}

type nodeVersion struct {
	major int
	minor int
	patch int
}

func (v nodeVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
}

func (v nodeVersion) compare(other nodeVersion) int {
	for _, pair := range [][2]int{{v.major, other.major}, {v.minor, other.minor}, {v.patch, other.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	return 0
}

// NodeResolution 一次 node 可执行解析结果。
type NodeResolution struct {
	Path    string // node 可执行绝对路径；PATH 回退时为 "node"
	Source  string // NodeSource* 之一
	Version string // 真实探测得到的完整版本
}

// NodeResolver 解析 spawn bot-worker 用的 node 可执行。
type NodeResolver struct {
	mu       sync.Mutex
	explicit string
	scan     func() []runtimescan.Candidate
	probe    func(path string) (string, error)
	cached   *NodeResolution
}

// NewNodeResolver 创建解析器。explicit 非空时保持操作员意图，探测失败直接返回错误；
// scan 为 nil 时使用 runtimescan 的真实扫描器，所有候选仍会再次探测以避免陈旧版本信息。
func NewNodeResolver(explicit string, scan func() []runtimescan.Candidate) *NodeResolver {
	if scan == nil {
		scan = func() []runtimescan.Candidate {
			return runtimescan.New(nil).Scan([]string{runtimescan.TypeNodeJS})
		}
	}
	return &NodeResolver{explicit: explicit, scan: scan, probe: probeNodeVersion}
}

// Resolve 返回满足最低版本的解析结果；只缓存成功结果。
func (r *NodeResolver) Resolve() (NodeResolution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cached != nil {
		return *r.cached, nil
	}
	res, err := r.resolveLocked()
	if err != nil {
		return NodeResolution{}, err
	}
	r.cached = &res
	return res, nil
}

// Refresh 清除缓存并立即重解析；失败结果不缓存，后续 Resolve 会再次真实探测。
func (r *NodeResolver) Refresh() (NodeResolution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cached = nil
	res, err := r.resolveLocked()
	if err != nil {
		return NodeResolution{}, err
	}
	r.cached = &res
	return res, nil
}

func (r *NodeResolver) resolveLocked() (NodeResolution, error) {
	if r.explicit != "" {
		res, _, err := r.probeCandidate(r.explicit, NodeSourceExplicit)
		if err != nil {
			return NodeResolution{}, fmt.Errorf("显式 Node.js 不可用: %w", err)
		}
		return res, nil
	}

	if res, ok := r.bestManagedCandidate(); ok {
		return res, nil
	}
	res, _, err := r.probeCandidate("node", NodeSourcePathFallback)
	if err != nil {
		return NodeResolution{}, fmt.Errorf("未找到可用的 Node.js >=%s：托管候选与 PATH node 均未通过真实探测: %w",
			minimumBotNodeVersion.String(), err)
	}
	return res, nil
}

func (r *NodeResolver) bestManagedCandidate() (NodeResolution, bool) {
	var best NodeResolution
	var bestVersion nodeVersion
	found := false
	seen := make(map[string]struct{})
	for _, candidate := range r.scan() {
		if candidate.Type != runtimescan.TypeNodeJS || candidate.Path == "" {
			continue
		}
		if _, ok := seen[candidate.Path]; ok {
			continue
		}
		seen[candidate.Path] = struct{}{}
		res, version, err := r.probeCandidate(candidate.Path, NodeSourceManagedScan)
		if err != nil {
			continue
		}
		if !found || version.compare(bestVersion) > 0 ||
			(version.compare(bestVersion) == 0 && res.Path < best.Path) {
			best, bestVersion, found = res, version, true
		}
	}
	return best, found
}

func (r *NodeResolver) probeCandidate(path, source string) (NodeResolution, nodeVersion, error) {
	raw, err := r.probe(path)
	if err != nil {
		return NodeResolution{}, nodeVersion{}, err
	}
	version, ok := parseNodeVersion(raw)
	if !ok {
		return NodeResolution{}, nodeVersion{}, fmt.Errorf("Node.js 版本格式无效: %q", raw)
	}
	if version.compare(minimumBotNodeVersion) < 0 {
		return NodeResolution{}, nodeVersion{}, fmt.Errorf("Node.js %s 低于最低要求 %s", version.String(), minimumBotNodeVersion.String())
	}
	return NodeResolution{Path: path, Source: source, Version: version.String()}, version, nil
}

func probeNodeVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), nodeProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func parseNodeVersion(raw string) (nodeVersion, bool) {
	value := strings.TrimPrefix(strings.TrimSpace(raw), "v")
	if cut := strings.IndexAny(value, "-+"); cut >= 0 {
		value = value[:cut]
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return nodeVersion{}, false
	}
	numbers := [3]int{}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return nodeVersion{}, false
		}
		numbers[i] = n
	}
	return nodeVersion{major: numbers[0], minor: numbers[1], patch: numbers[2]}, true
}
