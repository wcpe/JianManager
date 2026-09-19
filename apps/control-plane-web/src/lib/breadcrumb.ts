/**
 * 路由 → 面包屑轨迹（FR-134 统一页头/面包屑）。
 *
 * 据 pathname 首段映射出「域 → 页面」两级轨迹（i18n key + 可点目标），供全局顶栏统一渲染。
 * 与 `ConsoleSidebar` 六域 IA（FR-431）、`Workspace` 路由表对齐。纯函数，便于单测。
 */

/** 单个面包屑节点：label 为 i18n key；to 存在则可点跳转。 */
export interface Crumb {
  labelKey: string
  to?: string
}

/** 路由首段 → 所属域（域不可直接导航，仅作上下文，故无 to）。 */
const SEGMENT_DOMAIN: Record<string, string> = {
  // 服务器域。
  nodes: 'nav.servers',
  instances: 'nav.servers',
  players: 'nav.servers',
  bots: 'nav.servers',
  // 群组网络。
  networks: 'nav.groupNetwork',
  // 工作台（FR-431 独立域）。
  super: 'nav.workbench',
  director: 'nav.workbench',
  // 观测。
  monitor: 'nav.observability',
  'client-dist-monitor': 'nav.observability',
  logs: 'nav.observability',
  statistics: 'nav.observability',
  notifications: 'nav.observability',
  alerts: 'nav.observability',
  // 客户端分发。
  'client-channels': 'nav.clientDistribution',
  'client-dist-ops': 'nav.clientDistribution',
  'client-dist-security': 'nav.clientDistribution',
  // 平台设置 · 分节父级。
  users: 'nav.identityAccess',
  groups: 'nav.identityAccess',
  permissions: 'nav.identityAccess',
  tasks: 'nav.taskSchedule',
  schedules: 'nav.taskSchedule',
  'runtime-assets': 'nav.storageRuntime',
  storage: 'nav.storageRuntime',
  'artifact-storages': 'nav.storageRuntime',
  'backup-storages': 'nav.storageRuntime',
  backups: 'nav.storageRuntime',
  templates: 'nav.platformSettings',
  audit: 'nav.auditSettings',
  settings: 'nav.auditSettings',
  licenses: 'nav.auditSettings',
  'agent-tokens': 'nav.agentAccess',
  'mcp-sessions': 'nav.agentAccess',
  'agent-call-logs': 'nav.agentAccess',
  'artifact-versions': 'nav.systemMaintenance',
  database: 'nav.systemMaintenance',
  'system-update': 'nav.systemMaintenance',
}

/** 路由首段 → 页面标题 i18n key（叶子，可点回到该列表页）。 */
const SEGMENT_PAGE: Record<string, string> = {
  nodes: 'nav.nodes',
  instances: 'nav.allInstances',
  networks: 'nav.networkTopology',
  super: 'nav.superWorkbench',
  director: 'nav.director',
  monitor: 'nav.monitoring',
  'client-dist-monitor': 'nav.clientDistMonitor',
  'client-dist-ops': 'nav.clientDistOps',
  'client-dist-security': 'nav.clientDistOps',
  alerts: 'nav.alerts',
  logs: 'nav.logs',
  statistics: 'nav.statistics',
  tasks: 'nav.tasks',
  notifications: 'nav.notifications',
  players: 'nav.players',
  bots: 'nav.bots',
  templates: 'nav.templates',
  backups: 'nav.backups',
  'backup-storages': 'nav.backupStorages',
  'artifact-storages': 'nav.artifactStorages',
  schedules: 'nav.schedules',
  'runtime-assets': 'nav.runtimeAssets',
  'client-channels': 'nav.clientChannels',
  storage: 'nav.storage',
  database: 'nav.database',
  'system-update': 'nav.systemUpdate',
  'artifact-versions': 'nav.artifactVersions',
  users: 'nav.users',
  groups: 'nav.groups',
  permissions: 'nav.permissions',
  settings: 'nav.systemSettings',
  audit: 'nav.audit',
  licenses: 'licenses.title',
  'agent-tokens': 'nav.agentTokens',
  'mcp-sessions': 'nav.mcpSessions',
  'agent-call-logs': 'nav.agentCallLogs',
}

/**
 * 据 pathname 计算面包屑轨迹：
 * - 根路径 `/` → 「平台首页」（无 to，已在当前页）。
 * - 已知首段 → [域(无 to), 页面(有 to 回列表)]；若有更深子段（如 /instances/:id）则页面节点可点、末节点由调用方补具体名称。
 * - 未知首段 → 空数组（调用方回退通用标题）。
 */
export function breadcrumbTrail(pathname: string): Crumb[] {
  const segs = pathname.split('/').filter(Boolean)
  if (segs.length === 0) return [{ labelKey: 'nav.platformHome' }]

  const first = segs[0]
  if (first === 'networks') {
    const pageKey = segs[1] === 'topology' ? 'nav.networkTopology' : 'nav.groupManagement'
    return [{ labelKey: 'nav.groupNetwork' }, { labelKey: pageKey }]
  }

  const domainKey = SEGMENT_DOMAIN[first]
  const pageKey = SEGMENT_PAGE[first]
  if (!pageKey) return []

  const hasDeeper = segs.length > 1
  const pageTo = '/' + first
  const trail: Crumb[] = []
  if (domainKey) trail.push({ labelKey: domainKey })
  // 有更深子段时，页面节点可点回列表；否则为当前页（无 to）。
  // /client-dist-security 无独立列表页，crumb 仅保留页面节点（与旧行为一致）。
  if (first === 'client-dist-security') return [{ labelKey: pageKey }]
  trail.push(hasDeeper ? { labelKey: pageKey, to: pageTo } : { labelKey: pageKey })
  return trail
}
