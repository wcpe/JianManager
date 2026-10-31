import type { NodeInfo } from './node-types'
import type { StatusLevel } from '@jianmanager/ui/lib/threshold'

/** 
 * 节点管理页（主从双栏）左栏列表的纯逻辑：状态等级映射、搜索筛选、选中态解析、
 * 收缩态持久化。抽成无 React 依赖的纯函数便于单测（列表筛选/选中/收缩态）。
 */

/** 左栏收缩态持久键（：窄图标轨收缩态跨会话保留，localStorage）。 */
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

/** 节点状态筛选键（用户提出的「节点多了要能把出问题的机器一把捞出来」）。 */
export type NodeStatusFilter = 'all' | 'online' | 'offline' | 'maintenance'

/** 
 * 按搜索词 + 状态筛选节点。
 *
 * 【为什么补状态维度】此前只按名称/host 匹配。运维最高频的动作是「把所有离线节点找出来」——
 * 30 台时要翻页、100 台时基本找不过来。KPI 卡片上的「离线 1」只能看数字，点不动，
 * 与服务器页「点 StatCard 即筛」的手感也不一致。
 *
 * 语义约定：
 * - 状态与搜索词是 **AND**（先按状态过一遍，再按词匹配）；
 * - `maintenance` 是正交标记而非 status 值（维护中的节点 status 仍是 1/0），故单独判断；
 * - 空/空白搜索词不参与过滤，保持原顺序。
 */
export function filterNodes(
  nodes: NodeInfo[],
  query: string,
  status: NodeStatusFilter = 'all',
): NodeInfo[] {
  const q = query.trim().toLowerCase()
  const byStatus = status === 'all' ? nodes : nodes.filter((n) => matchesStatus(n, status))
  if (q === '') return byStatus
  return byStatus.filter(
    (n) => n.name.toLowerCase().includes(q) || n.host.toLowerCase().includes(q),
  )
}

/** 单节点是否命中某个状态筛选。维护中优先于在线/离线（与状态徽章的判据一致）。 */
export function matchesStatus(node: NodeInfo, status: NodeStatusFilter): boolean {
  switch (status) {
    case 'all':
      return true
    // 维护中的节点即使在线，也不该出现在「在线」筛选里——它的可用性语义已经变了。
    case 'maintenance':
      return node.maintenance
    case 'online':
      return node.status === 1 && !node.maintenance
    case 'offline':
      return node.status === 0
  }
}

/** 
 * 把「当前选中节点 id」解析为实时列表中的最新节点对象（右栏即时更新）。
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

/** 持久化左栏收缩态。 */
export function persistNodeListCollapsed(
  collapsed: boolean,
  storage: KVStorage | null = defaultStorage(),
): void {
  if (!storage) return
  storage.setItem(NODE_LIST_COLLAPSED_KEY, collapsed ? '1' : '0')
}