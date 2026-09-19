/**
 * 角色枚举与权限种子（FR-432）。与后端 model.Role* / systemRoleNodes 对齐。
 * 新代码禁止硬编码 `role === 10`，一律走这里的 ROLE_* / isPlatformAdmin / hasPerm。
 */

export const ROLE_MEMBER = 0
export const ROLE_GROUP_ADMIN = 1
export const ROLE_GROUP_OPERATOR = 2
export const ROLE_GROUP_VIEWER = 3
export const ROLE_PLATFORM_ADMIN = 10

/** 数值角色 → 后端 roleKey。 */
export const ROLE_KEY_BY_VALUE: Record<number, string> = {
  [ROLE_MEMBER]: 'member',
  [ROLE_GROUP_ADMIN]: 'group_admin',
  [ROLE_GROUP_OPERATOR]: 'group_operator',
  [ROLE_GROUP_VIEWER]: 'group_viewer',
  [ROLE_PLATFORM_ADMIN]: 'platform_admin',
}

/** 数值角色 → i18n key（users.*）。 */
export const ROLE_LABEL_KEY: Record<number, string> = {
  [ROLE_MEMBER]: 'users.member',
  [ROLE_GROUP_ADMIN]: 'users.groupAdmin',
  [ROLE_GROUP_OPERATOR]: 'users.groupOperator',
  [ROLE_GROUP_VIEWER]: 'users.groupViewer',
  [ROLE_PLATFORM_ADMIN]: 'users.platformAdmin',
}

export function isPlatformAdmin(role: number | null | undefined): boolean {
  return role === ROLE_PLATFORM_ADMIN
}

/** 后端 systemRoleNodes 种子节点（platform_admin 由调用方按全开处理，此处不展开）。 */
const GROUP_ADMIN_NODES = [
  'instance.read',
  'instance.create',
  'instance.write',
  'instance.operate',
  'instance.delete',
  'instance.launchspec.write',
  'instance.business.write',
  'file.read',
  'file.write',
  'terminal.access',
  'bot.read',
  'bot.manage',
  'backup.read',
  'backup.write',
  'schedule.read',
  'schedule.write',
  'task.read',
  'task.manage',
  'monitor.read',
  'log.read',
  'stats.read',
  'alert.read',
  'alert.manage',
  'notification.read',
  'player.read',
  'player.manage',
  'network.read',
  'network.manage',
  'group.read',
  'group.member.write',
  'super.read',
  'director.read',
]

const GROUP_OPERATOR_NODES = [
  'instance.read',
  'instance.write',
  'instance.operate',
  'instance.create',
  'instance.delete',
  'file.read',
  'file.write',
  'terminal.access',
  'bot.read',
  'bot.manage',
  'backup.read',
  'backup.write',
  'schedule.read',
  'schedule.write',
  'task.read',
  'task.manage',
  'monitor.read',
  'log.read',
  'stats.read',
  'alert.read',
  'notification.read',
  'player.read',
  'network.read',
  'super.read',
  'director.read',
  'group.read',
]

const GROUP_VIEWER_NODES = [
  'instance.read',
  'file.read',
  'bot.read',
  'backup.read',
  'schedule.read',
  'task.read',
  'monitor.read',
  'log.read',
  'stats.read',
  'alert.read',
  'notification.read',
  'player.read',
  'network.read',
  'group.read',
]

const MEMBER_NODES = [
  'instance.read',
  'instance.write',
  'instance.operate',
  'file.read',
  'file.write',
  'terminal.access',
  'bot.read',
  'bot.manage',
  'backup.read',
  'schedule.read',
  'task.read',
  'monitor.read',
  'log.read',
  'notification.read',
  'player.read',
  'group.read',
]

/**
 * 旧数值角色 → 预置模板默认节点（auth/me 未返回 nodes 时的前端回退）。
 * platform_admin 不在此表：isPlatformAdmin 短路全开。
 */
export const DEFAULT_ROLE_NODES: Record<number, string[]> = {
  [ROLE_MEMBER]: MEMBER_NODES,
  [ROLE_GROUP_ADMIN]: GROUP_ADMIN_NODES,
  [ROLE_GROUP_OPERATOR]: GROUP_OPERATOR_NODES,
  [ROLE_GROUP_VIEWER]: GROUP_VIEWER_NODES,
}

/** 目录中全部常见节点 id（权限 store 测试 / mock 用；不含后端动态扩展）。 */
export const ALL_PERMISSION_NODE_IDS: string[] = [
  'user.read',
  'user.manage',
  'group.read',
  'group.manage',
  'group.member.write',
  'group.quota.write',
  'node.read',
  'node.manage',
  'settings.read',
  'settings.write',
  'system.update',
  'system.db.browse',
  'license.read',
  'audit.read',
  'rbac.read',
  'rbac.manage',
  'instance.read',
  'instance.create',
  'instance.write',
  'instance.operate',
  'instance.delete',
  'instance.launchspec.write',
  'instance.business.write',
  'file.read',
  'file.write',
  'terminal.access',
  'bot.read',
  'bot.manage',
  'backup.read',
  'backup.write',
  'schedule.read',
  'schedule.write',
  'task.read',
  'task.manage',
  'monitor.read',
  'log.read',
  'stats.read',
  'alert.read',
  'alert.manage',
  'notification.read',
  'template.read',
  'template.manage',
  'channel.read',
  'channel.write',
  'dist.publish',
  'dist.ops.read',
  'dist.ops.write',
  'agent.token.read',
  'agent.token.manage',
  'agent.mcp.read',
  'agent.calllog.read',
  'network.read',
  'network.manage',
  'player.read',
  'player.manage',
  'super.read',
  'director.read',
]
