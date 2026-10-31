/** 
 * 导航数据结构（阶段 6：从 `nav-config.ts` 抽出）。
 *
 * 【为什么单独成一个文件】
 * 旧实现把「类型」与「六域数据」放在同一模块（`nav-config.ts`）。阶段 6 删掉六域数据后，
 * 类型仍被这些地方使用：
 * - 数据层 `lib/console/workspace-navigation.ts`（四工作区内部复用 `NavGroup` 结构，不另造平行类型）；
 * - 渲染层 `components/views/**`（受控视图只接类型，不接数据）；
 * - `Workspace.tsx` 的路由表承接 RouteRegistry 语义。
 *
 * 因此类型下沉到本文件：删数据不删类型，避免「为了删一份过期数据，把整条链路改成 any」。
 * 数据（四工作区）见 {@link ./workspace-navigation}。
 */

import type { LucideIcon } from 'lucide-react'

/** 
 * 侧栏导航项（多级侧栏共用）。`perm` 为权限节点，数组表示 any-of。
 *
 * 定义在数据层而非链接组件（`components/views/console/SidebarNavLink`）：导航数据与导航渲染
 * 是两件事，数据不该反向依赖渲染组件；链接组件按需 import 本类型。
 */
export interface NavEntry {
  to: string
  labelKey: string
  icon?: LucideIcon
  perm?: string | string[]
}

/** 
 * 一个导航分区（leaf=单链接；children=可展开子项；sections=带小标题的二级分节）。
 * `perm` 为权限节点，数组表示 any-of（432）。
 */
export interface NavGroup {
  key: string
  labelKey: string
  icon: LucideIcon
  to?: string
  perm?: string | string[]
  children?: NavEntry[]
  sections?: NavSection[]
}

/** 带标题的二级分节，用于「平台设置」类分组。 */
export interface NavSection {
  labelKey: string
  children: NavEntry[]
}
