import { useAuthStore } from '@/stores/auth'

/**
 * 危险操作的「权限范围」分级（FR-059 角色门禁）。
 *
 * 前端门禁仅用于在 UI 上提前禁用/提示越权操作，最终拒绝由 Control Plane
 * 的 RBAC 中间件强制（architecture-invariants）。这里按最低角色等级判定，
 * 与后端 model.UserRole（0 组成员 / 1 组管理员 / 10 平台管理员）对齐。
 */
export type DangerScope = 'group' | 'platform'

/** 与后端 model.UserRole 对齐的角色等级常量。 */
export const Role = {
  Member: 0,
  GroupAdmin: 1,
  PlatformAdmin: 10,
} as const

/** 不同范围危险操作要求的最低角色等级。 */
const SCOPE_MIN_ROLE: Record<DangerScope, number> = {
  // 组范围：删实例 / 删备份 / 删 Bot 等，组管理员及以上可执行。
  group: Role.GroupAdmin,
  // 平台范围：删用户 / 删节点 / 删群组等，仅平台管理员可执行。
  platform: Role.PlatformAdmin,
}

/**
 * 纯函数：给定角色等级与操作范围，判定是否允许执行危险操作。
 * 抽出便于单测；UI 侧用 useDangerPermission 包装。
 */
export function canRunDanger(role: number | null, scope: DangerScope): boolean {
  return role !== null && role >= SCOPE_MIN_ROLE[scope]
}

/** 危险操作权限判定结果。 */
export interface DangerPermission {
  /** 当前用户是否被允许执行该范围的危险操作。 */
  allowed: boolean
  /** 当前用户角色等级（未登录为 null）。 */
  role: number | null
}

/**
 * 判定当前登录用户能否执行指定范围的危险操作。
 *
 * @param scope 危险操作范围；省略时按组范围判定（最宽松）。
 */
export function useDangerPermission(scope: DangerScope = 'group'): DangerPermission {
  const role = useAuthStore((s) => s.role)
  return {
    allowed: canRunDanger(role, scope),
    role,
  }
}
