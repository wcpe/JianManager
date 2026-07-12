/**
 * Node 可执行解析器（FR-300 bot-worker 接托管 Node 运行时）。
 * spawn bot-worker 前解析用哪个 node：显式配置 > 节点本地扫描最高 major Node > 回退 PATH "node"。
 * 解析一次并缓存（免每次 spawn 重扫）；Refresh 作失效重扫钩子（spawn 失败时重试一次）。
 * 参见 docs/specs/node-runtime-library/spec.md §3.4。
 */

package bot

import (
	"sync"

	"github.com/wcpe/JianManager/internal/worker/runtimescan"
)

// Node 可执行解析来源（进 bot 启动日志，供真机核对选用了哪条链路）。
const (
	// NodeSourceExplicit 显式配置指定（bot 级可选 node 路径字段，V1 无 UI 只留结构）。
	NodeSourceExplicit = "explicit-config"
	// NodeSourceManagedScan 节点本地扫描发现（复用 runtimescan 的 node 探测路径表，取 major 最高探测成功者）。
	NodeSourceManagedScan = "managed-scan"
	// NodeSourcePathFallback 回退 PATH 里的 "node"（FR-300 之前的现行为，保兼容）。
	NodeSourcePathFallback = "path-fallback"
)

// NodeResolution 一次 node 可执行解析结果。
type NodeResolution struct {
	Path   string // node 可执行绝对路径；回退时为 "node"（交 PATH 解析）
	Source string // NodeSource* 之一
}

// NodeResolver 解析 spawn bot-worker 用的 node 可执行。
// 扫描函数可注入（测试用伪扫描结果替换真探测）。
type NodeResolver struct {
	mu       sync.Mutex
	explicit string
	scan     func() []runtimescan.Candidate
	cached   *NodeResolution
}

// NewNodeResolver 创建解析器。explicit 非空则恒优先（操作员意图不被扫描覆盖）；
// scan 为 nil 时用真扫描器（runtimescan 内置 node 路径表 + `node --version` 探测）。
func NewNodeResolver(explicit string, scan func() []runtimescan.Candidate) *NodeResolver {
	if scan == nil {
		scan = func() []runtimescan.Candidate {
			return runtimescan.New(nil).Scan([]string{runtimescan.TypeNodeJS})
		}
	}
	return &NodeResolver{explicit: explicit, scan: scan}
}

// Resolve 返回解析结果：首次真解析，之后走缓存（避免每次 spawn 都跑一轮探测子进程）。
func (r *NodeResolver) Resolve() NodeResolution {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cached == nil {
		res := r.resolveLocked()
		r.cached = &res
	}
	return *r.cached
}

// Refresh 失效缓存并立即重解析（spawn 失败前的重试钩子：托管 node 可能已被删/移动）。
func (r *NodeResolver) Refresh() NodeResolution {
	r.mu.Lock()
	defer r.mu.Unlock()
	res := r.resolveLocked()
	r.cached = &res
	return res
}

func (r *NodeResolver) resolveLocked() NodeResolution {
	if r.explicit != "" {
		return NodeResolution{Path: r.explicit, Source: NodeSourceExplicit}
	}
	// Scan 只返回探测成功的候选；这里不依赖其排序，自行取 nodejs 中 major 最高者。
	var best *runtimescan.Candidate
	cands := r.scan()
	for i := range cands {
		c := &cands[i]
		if c.Type != runtimescan.TypeNodeJS || c.Path == "" {
			continue
		}
		if best == nil || c.Major > best.Major {
			best = c
		}
	}
	if best != nil {
		// 候选路径来自绝对路径 glob 表（runtimescan），本身即绝对路径，不再二次归一。
		return NodeResolution{Path: best.Path, Source: NodeSourceManagedScan}
	}
	return NodeResolution{Path: "node", Source: NodeSourcePathFallback}
}
