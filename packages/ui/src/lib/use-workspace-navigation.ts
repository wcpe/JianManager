import type { WorkspaceDef } from './workspace-navigation'

// 工作区导航 hook（useWorkspaceNavigation）要读路由与三个应用侧 store，留在应用侧接线；
// 包内只保留与它们无关的纯函数。
/** 深链（`/instances/:id` 这类带参数的子路由）不是可停留的页面，不进导航行与落地页。 */
export function isDeepLink(to: string): boolean {
  return to.includes(':')
}

/**
 * 工作区的落地页：第一个分组的第一个导航目的地（原型「切工作区进该区第一页」）。
 * 服务器运维 → 平台首页、观测与自动化 → 监控总览、运营与分发 → 玩家、平台管理 → 用户。
 */
export function landingPathOf(workspace: WorkspaceDef): string | null {
  for (const group of workspace.groups) {
    if (group.to && !isDeepLink(group.to)) return group.to
    for (const entry of group.children ?? []) {
      if (!isDeepLink(entry.to)) return entry.to
    }
    for (const section of group.sections ?? []) {
      for (const entry of section.children) if (!isDeepLink(entry.to)) return entry.to
    }
  }
  return null
}