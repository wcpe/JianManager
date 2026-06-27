import type { NodeInfo } from '@/api/nodes'
import type { StatusLevel } from '@/lib/threshold'

/**
 * 节点管理页（FR-177 主从双栏）左栏列表的纯逻辑：状态等级映射、搜索筛选、选中态解析、
 * 收缩态持久化。抽成无 React 依赖的纯函数便于单测（列表筛选/选中/收缩态）。
 */

/** 左栏收缩态持久键（FR-177：窄图标轨收缩态跨会话保留，localStorage）。 */
export const NODE_LIST_COLLAPSED_KEY = 'nodesPage.listCollapsed'

/** 最小持久存储接口（localStorage 的子集），便于单测注入内存实现（同 config-explorer/favorites）。 */
export interface KVStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
}

/** 取默认存储：浏览器为 localStorage，非 DOM 环境（SSR/单测）为 null。 */
function defaultStorage(): KVStorage | null {
  return typeof localStorage === 'undefined' ? null : localStorage
}

/** 节点状态码 → 状态等级（1 在线=正常 / 2 启动中=警告 / 0 离线=危险）。 */
export function nodeStatusLevel(status: number): StatusLevel {
  if (status === 1) return 'success'
  if (status === 2) return 'warning'
  return 'danger'
}

/**
 * 按搜索词筛选节点（名称/host 大小写不敏感子串）。
 * 空/空白查询返回原列表（保留全部节点、保持原顺序）。
 */
export function filterNodes(nodes: NodeInfo[], query: string): NodeInfo[] {
  const q = query.trim().toLowerCase()
  if (q === '') return nodes
  return nodes.filter(
    (n) => n.name.toLowerCase().includes(q) || n.host.toLowerCase().includes(q),
  )
}

/**
 * 把「当前选中节点 id」解析为实时列表中的最新节点对象（FR-177 右栏即时更新）。
 * id 为 null（未选）或命中不到（节点已被下线）时返回 null，右栏回落空态。
 * 始终返回实时列表里的对象，保证右栏仪表/状态随轮询刷新而非用陈旧选中快照。
 */
export function resolveSelectedNode(nodes: NodeInfo[], selectedId: number | null): NodeInfo | null {
  if (selectedId === null) return null
  return nodes.find((n) => n.id === selectedId) ?? null
}

/** 安全读取左栏收缩态（非 DOM/无值回退 false=展开）。 */
export function loadNodeListCollapsed(storage: KVStorage | null = defaultStorage()): boolean {
  if (!storage) return false
  return storage.getItem(NODE_LIST_COLLAPSED_KEY) === '1'
}

/** 持久化左栏收缩态（FR-177）。 */
export function persistNodeListCollapsed(
  collapsed: boolean,
  storage: KVStorage | null = defaultStorage(),
): void {
  if (!storage) return
  storage.setItem(NODE_LIST_COLLAPSED_KEY, collapsed ? '1' : '0')
}
