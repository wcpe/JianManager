import { useAuthStore } from '@/stores/auth'

/**
 * 危险操作的「权限范围」分级（角色门禁）。
 *
 * 前端门禁仅用于在 UI 上提前禁用/提示越权操作，最终拒绝由 Control Plane
 * 的 RBAC 中间件强制（architecture-invariants）。
 *
 * 位置说明：本模块的纯逻辑曾随业务契约并入组件库，后随业务视图回迁应用侧。
 * `DangerScope` 定义在此处（契约侧）而非视图里——视图要 import 本模块的 hook，
 * 类型若留在视图会形成循环。
 */

/** 危险操作的范围：'group' 组管理员+ / 'platform' 仅平台管理员。 */
export type DangerScope = 'group' | 'platform'

/**
 * 与后端 model.UserRole 对齐的**全部**角色值。
 *
 * 后端的枚举是 5 档（`internal/controlplane/model/user.go`）：
 * 0 组成员 / 1 组管理员 / 2 组运维 / 3 组只读 / 10 平台管理员。
 * 早前这里只列了 3 个，而判定又写成 `role >= 1` —— 于是**组运维（2）与组只读（3）
 * 一并被当成“组管理员及以上”**。而只读角色的权限种子里根本没有
 * `instance.write` / `instance.delete`：结果是只读账号点「删除实例」时
 * 前端门禁判通过、确认按钮可点，只有后端 403 兼底。
 */
export const Role = {
  Member: 0,
  GroupAdmin: 1,
  GroupOperator: 2,
  GroupViewer: 3,
  PlatformAdmin: 10,
} as const

/**
 * 不同范围危险操作要求的**角色白名单**（而不是“最低等级”）。
 *
 * 【为什么不用 `role >= min`】角色值不是能力强度上的单调标尺：
 * 2（组运维）与 3（组只读）数值上大于 1，但它们的权限集**不是**组管理员的超集——
 * 组只读反而少了写/删权限。用大小比较必然把它们误判为“有权限”。
 * 白名单把意图写死：新增角色时必须显式决定它能不能做危险操作。
 */
const SCOPE_ALLOWED_ROLES: Record<DangerScope, readonly number[]> = {
  // 组范围：删实例 / 删备份 / 删 Bot 等——组管理员及以上。
  // 组运维（2）不在内：它的种子权限不含 instance.delete。
  // 组只读（3）更不在内。
  group: [Role.GroupAdmin, Role.PlatformAdmin],
  // 平台范围：删用户 / 删节点 / 删群组等——仅平台管理员。
  platform: [Role.PlatformAdmin],
}

/**
 * 纯函数：给定角色等级与操作范围，判定是否允许执行危险操作。
 * 抽出便于单测；UI 侧由外壳用 auth store 的角色值包装成 hook。
 *
 * 未登录（role=null）一律拒。未知角色（不在任何白名单里）也拒——fail-closed。
 */
export function canRunDanger(role: number | null, scope: DangerScope): boolean {
  if (role === null) return false
  return SCOPE_ALLOWED_ROLES[scope].includes(role)
}

/** 危险操作权限判定结果。 */
export interface DangerPermission {
  /** 当前用户是否被允许执行该范围的危险操作。 */
  allowed: boolean
  /** 当前用户角色等级（未登录为 null）。 */
  role: number | null
}

/** 
 * 判定当前登录用户能否执行指定范围的危险操作（hook 留应用侧——它要读 auth store）。
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
