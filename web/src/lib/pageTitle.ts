/**
 * 路由 → 页面标题 i18n key 映射（FR-162 全局顶栏轻量标题）。
 * 仅做「当前在哪个区」的轻量标题，不展开 FR-134 的统一页头/面包屑全量。
 * 纯函数，便于单测；子路由（如 /instances/:id）按首段归并到所属区。
 */

/** 路由首段 → 标题 i18n key。与 `Workspace.tsx` 的路由表、`ConsoleSidebar` 的 nav.* 对齐。 */
const SEGMENT_TITLE_KEYS: Record<string, string> = {
  monitor: 'nav.monitoring',
  nodes: 'nav.nodes',
  instances: 'nav.allInstances',
  networks: 'nav.networks',
  players: 'nav.players',
  bots: 'nav.bots',
  alerts: 'nav.alerts',
  logs: 'nav.logs',
  users: 'nav.users',
  groups: 'nav.groups',
  templates: 'nav.templates',
  'runtime-assets': 'nav.runtimeAssets',
  schedules: 'nav.schedules',
  backups: 'nav.backups',
  'backup-storages': 'nav.backupStorages',
  audit: 'nav.audit',
  'client-channels': 'nav.clientChannels',
  storage: 'nav.storage',
  settings: 'nav.systemSettings',
  database: 'nav.database',
  'system-update': 'nav.systemUpdate',
  licenses: 'licenses.title',
}

/**
 * 据 pathname 取页面标题 i18n key：
 * 根路径 → 仪表盘；已知首段 → 对应区标题；未知 → 空串（调用方回退到通用「控制台」）。
 */
export function consoleTitleKey(pathname: string): string {
  const seg = pathname.split('/').filter(Boolean)[0]
  if (!seg) return 'nav.dashboard'
  return SEGMENT_TITLE_KEYS[seg] ?? ''
}
