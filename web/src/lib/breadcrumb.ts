/**
 * 路由 → 面包屑轨迹（FR-134 统一页头/面包屑）。
 *
 * 据 pathname 首段映射出「域 → 页面」两级轨迹（i18n key + 可点目标），供全局顶栏统一渲染。
 * 与 `ConsoleSidebar` 五域 IA、`Workspace` 路由表对齐。纯函数，便于单测。
 */

/** 单个面包屑节点：label 为 i18n key；to 存在则可点跳转。 */
export interface Crumb {
  labelKey: string
  to?: string
}

/** 路由首段 → 所属域（域不可直接导航，仅作上下文，故无 to）。 */
const SEGMENT_DOMAIN: Record<string, string> = {
  // 集群
  nodes: 'nav.cluster',
  instances: 'nav.cluster',
  networks: 'nav.cluster',
  // 监控
  monitor: 'nav.monitor',
  alerts: 'nav.monitor',
  logs: 'nav.monitor',
  // 运营
  players: 'nav.operations',
  bots: 'nav.operations',
  templates: 'nav.operations',
  backups: 'nav.operations',
  'backup-storages': 'nav.operations',
  schedules: 'nav.operations',
  'client-channels': 'nav.operations',
  // 系统
  'runtime-assets': 'nav.system',
  storage: 'nav.system',
  database: 'nav.system',
  'system-update': 'nav.system',
  users: 'nav.system',
  groups: 'nav.system',
  settings: 'nav.system',
  audit: 'nav.system',
  licenses: 'nav.system',
}

/** 路由首段 → 页面标题 i18n key（叶子，可点回到该列表页）。 */
const SEGMENT_PAGE: Record<string, string> = {
  nodes: 'nav.nodes',
  instances: 'nav.allInstances',
  networks: 'nav.networks',
  monitor: 'nav.monitoring',
  alerts: 'nav.alerts',
  logs: 'nav.logs',
  players: 'nav.players',
  bots: 'nav.bots',
  templates: 'nav.templates',
  backups: 'nav.backups',
  'backup-storages': 'nav.backupStorages',
  schedules: 'nav.schedules',
  'runtime-assets': 'nav.runtimeAssets',
  'client-channels': 'nav.clientChannels',
  storage: 'nav.storage',
  database: 'nav.database',
  'system-update': 'nav.systemUpdate',
  users: 'nav.users',
  groups: 'nav.groups',
  settings: 'nav.systemSettings',
  audit: 'nav.audit',
  licenses: 'licenses.title',
}

/**
 * 据 pathname 计算面包屑轨迹：
 * - 根路径 `/` → 单节点「总览」（无 to，已在当前页）。
 * - 已知首段 → [域(无 to), 页面(有 to 回列表)]；若有更深子段（如 /instances/:id）则页面节点可点、末节点由调用方补具体名称。
 * - 未知首段 → 空数组（调用方回退通用标题）。
 */
export function breadcrumbTrail(pathname: string): Crumb[] {
  const segs = pathname.split('/').filter(Boolean)
  if (segs.length === 0) return [{ labelKey: 'nav.dashboard' }]

  const first = segs[0]
  const domainKey = SEGMENT_DOMAIN[first]
  const pageKey = SEGMENT_PAGE[first]
  if (!pageKey) return []

  const hasDeeper = segs.length > 1
  const pageTo = '/' + first
  const trail: Crumb[] = []
  if (domainKey) trail.push({ labelKey: domainKey })
  // 有更深子段时，页面节点可点回列表；否则为当前页（无 to）。
  trail.push(hasDeeper ? { labelKey: pageKey, to: pageTo } : { labelKey: pageKey })
  return trail
}
