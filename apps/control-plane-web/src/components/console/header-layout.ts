/**
 * 全局页眉布局逻辑（FR-179，增强 FR-162；通知合并 FR-216）。
 * 顶栏的「槽位顺序 / 右对齐 / 响应式可见性」是纯结构决策，
 * 下沉为纯函数便于单测，组件只按结果渲染（与 lib/breadcrumb、lib/stat-card 同范式）。
 *
 * 设计要点：
 * - 搜索框由 FR-162 的「居中铺中部」改为**靠右对齐**，紧贴右侧操作区（集群徽标/铃铛/账户），窄屏隐藏。
 * - FR-216 把原「站内信收件箱(inbox)」+「告警铃铛(alertBell)」**两个入口合并为单一通知铃铛**（notifications）：
 *   消费统一通知流（站内信 + 告警），下拉预览未读、点进通知中心页。
 */

/** 顶栏右侧操作区槽位。`notifications` 为 FR-216 合并后的单一通知铃铛（站内信 + 告警）。 */
export type HeaderSlot = 'search' | 'clusterBadges' | 'notifications' | 'account'

/**
 * 右侧操作区槽位顺序（从左到右）。
 * 搜索靠右后并入操作区最左；通知铃铛紧邻账户之前；账户菜单沉最右。
 */
export const HEADER_RIGHT_SLOTS: readonly HeaderSlot[] = [
  'search',
  'clusterBadges',
  'notifications',
  'account',
] as const

/** FR-216 合并后的单一通知铃铛槽位（站内信 + 告警统一入口，紧邻账户之前）。 */
export const NOTIFICATIONS_SLOT: HeaderSlot = 'notifications'

/**
 * 槽位的响应式可见性（Tailwind 断点语义）：
 * - `always`：始终显示（通知铃铛 / 账户 = 核心常驻能力，窄屏不可隐）。
 * - `md`：≥md 显示（搜索 = 辅助能力，窄屏隐藏免挤垮工作区，防翻屏）。
 * - `lg`：≥lg 显示（集群徽标 = 信息密度型，窄屏优先让位）。
 */
export type SlotVisibility = 'always' | 'md' | 'lg'

/** 槽位 → 响应式可见性档位。窄屏（<md）仅保留通知铃铛 + 账户，确保不翻屏。 */
export function slotVisibility(slot: HeaderSlot): SlotVisibility {
  switch (slot) {
    case 'search':
      return 'md'
    case 'clusterBadges':
      return 'lg'
    case 'notifications':
    case 'account':
      return 'always'
  }
}

/** 可见性档位 → 容器 Tailwind 类（`always` 不加显隐类）。 */
export function visibilityClass(v: SlotVisibility): string {
  switch (v) {
    case 'md':
      return 'hidden md:block'
    case 'lg':
      return 'hidden lg:flex'
    case 'always':
      return ''
  }
}

/**
 * 搜索框容器类（靠右对齐版）。
 * 不再用 `flex-1 max-w-md` 铺满中部，改为固定上限宽度并交由 `ml-auto`（容器侧）推到右侧操作区左缘，
 * 紧贴集群徽标/铃铛/账户；窄屏（<md）隐藏。
 */
export function searchBoxClass(): string {
  return 'relative hidden w-44 md:block lg:w-56 xl:w-64'
}
