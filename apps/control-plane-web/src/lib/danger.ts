import { useAuthStore } from '@/stores/auth'

// 纯逻辑（Role 常量、canRunDanger、DangerPermission 类型）已迁至 `@jianmanager/ui`；
// 此处转出，调用点零改动。
export * from '@jianmanager/ui/lib/danger'

import { canRunDanger, type DangerPermission } from '@jianmanager/ui/lib/danger'
import type { DangerScope } from '@jianmanager/ui/components/views/DangerConfirm'

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
