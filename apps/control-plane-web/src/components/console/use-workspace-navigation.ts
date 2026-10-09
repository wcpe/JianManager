import { useMemo } from 'react'
import { useLocation, useNavigate } from 'react-router'

import { useAuthStore } from '@/stores/auth'
import { useConsoleStore } from '@/stores/console'
import { usePermissionsStore } from '@/stores/permissions'
import { workspaceOfPath, workspacesForPermissions, workspacesForRole } from '@/lib/workspace-navigation'
import type { WorkspaceKey } from '@/lib/workspace-navigation'

// 纯函数（isDeepLink / landingPathOf）已回迁应用侧；此处转出，调用点零改动。
export { isDeepLink, landingPathOf } from '@/lib/use-workspace-navigation'
import { landingPathOf } from '@/lib/use-workspace-navigation'

/**
 * 工作区导航的唯一数据源（FR-496 阶段 6 补丁：从 `WorkspaceSidebar` 抽成独立模块）。
 *
 * 为什么抽出来：顶栏（`ConsoleHeader`）与侧栏（`WorkspaceSidebar`）都要消费同一份
 * 「按权限裁剪的工作区列表 + 当前工作区」。此前顶栏只借用了一个独立成行的
 * `ConsoleWorkspaceBar`，两者同文件、不构成复用；补丁把工作区切换并回顶栏后，
 * 两处各自写一遍「按权限裁剪 + 判断当前工作区」必然漂移，出现「顶栏给了切过去、
 * 侧栏却是空的」这种自相矛盾。
 *
 * 裁剪语义与旧侧栏 `navGroupsForPermissions` / `navGroupsForRole` 完全一致
 * （权限未加载先用角色种子，避免首帧闪出无权入口）。
 */
export function useWorkspaceNavigation() {
  const { pathname } = useLocation()
  const navigate = useNavigate()
  const role = useAuthStore((s) => s.role)
  const permNodes = usePermissionsStore((s) => s.nodes)
  const permAdmin = usePermissionsStore((s) => s.isPlatformAdmin)
  const permLoaded = usePermissionsStore((s) => s.loaded)
  const lastWorkspaceKey = useConsoleStore((s) => s.lastWorkspaceKey)
  const setLastWorkspaceKey = useConsoleStore((s) => s.setLastWorkspaceKey)

  const workspaces = useMemo(
    () => (permLoaded ? workspacesForPermissions(permNodes, permAdmin) : workspacesForRole(role)),
    [permLoaded, permNodes, permAdmin, role],
  )

  // 当前工作区由路由决定（`workspaceOfPath`）。路由判不出时才回落到上次选择——
  // 顶栏高亮不该因为一次未知路径（或尚未登记的页面）就跳回第一个工作区。
  // 回落到列表首项是最后兜底，保证任何权限组合下顶栏都有且只有一个高亮项。
  const visibleKey = (key: string | null): WorkspaceKey | null =>
    key && workspaces.some((workspace) => workspace.key === key) ? (key as WorkspaceKey) : null
  const routedKey = visibleKey(workspaceOfPath(pathname))
  const activeKey = routedKey ?? visibleKey(lastWorkspaceKey) ?? workspaces[0]?.key ?? null
  const activeWorkspace = workspaces.find((workspace) => workspace.key === activeKey) ?? null

  /** 切工作区：记住选择并跳到该工作区的落地页（已在落地页则不重复导航）。 */
  const goToWorkspace = (key: string) => {
    const target = workspaces.find((workspace) => workspace.key === key)
    if (!target) return
    setLastWorkspaceKey(key)
    const landing = landingPathOf(target)
    if (landing && landing !== pathname) navigate(landing)
  }

  return { workspaces, activeKey, activeWorkspace, goToWorkspace }
}

