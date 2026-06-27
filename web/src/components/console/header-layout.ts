/**
 * 全局页眉布局逻辑（FR-179，增强 FR-162）。
 * 顶栏的「槽位顺序 / 右对齐 / 响应式可见性 / 站内信挂载点预留」是纯结构决策，
 * 下沉为纯函数便于单测，组件只按结果渲染（与 lib/breadcrumb、lib/stat-card 同范式）。
 *
 * 设计要点：
 * - 搜索框由 FR-162 的「居中铺中部」改为**靠右对齐**，紧贴右侧操作区（集群徽标/铃铛/账户），窄屏隐藏。
 * - 右侧操作区在告警铃铛前预留**站内信收件箱**挂载点（FR-183，本期仅占位不实现逻辑）。
 */

/** 顶栏右侧操作区槽位。`inbox` 为 FR-183 站内信收件箱预留挂载点（本期占位）。 */
export type HeaderSlot = 'search' | 'clusterBadges' | 'inbox' | 'alertBell' | 'account'

/**
 * 右侧操作区槽位顺序（从左到右）。
 * 搜索靠右后并入操作区最左；收件箱紧邻告警铃铛之前（FR-183 挂载点）；账户菜单沉最右。
 */
export const HEADER_RIGHT_SLOTS: readonly HeaderSlot[] = [
  'search',
  'clusterBadges',
  'inbox',
  'alertBell',
  'account',
] as const

/** FR-183 站内信收件箱挂载点在右侧操作区中的固定位置（紧邻告警铃铛之前）。 */
export const INBOX_SLOT: HeaderSlot = 'inbox'

/** 站内信收件箱所追踪的 FR 编号（占位挂载点，本期不实现逻辑）。 */
export const INBOX_SLOT_FR = 'FR-183'

/**
 * 槽位的响应式可见性（Tailwind 断点语义）：
 * - `always`：始终显示（铃铛 / 账户 = 核心常驻能力，窄屏不可隐）。
 * - `md`：≥md 显示（搜索 = 辅助能力，窄屏隐藏免挤垮工作区，防翻屏）。
 * - `lg`：≥lg 显示（集群徽标 = 信息密度型，窄屏优先让位）。
 * - `reserved`：预留挂载点，本期不渲染可见内容（FR-183 站内信）。
 */
export type SlotVisibility = 'always' | 'md' | 'lg' | 'reserved'

/** 槽位 → 响应式可见性档位。窄屏（<md）仅保留铃铛 + 账户，确保不翻屏。 */
export function slotVisibility(slot: HeaderSlot): SlotVisibility {
  switch (slot) {
    case 'search':
      return 'md'
    case 'clusterBadges':
      return 'lg'
    case 'inbox':
      return 'reserved'
    case 'alertBell':
    case 'account':
      return 'always'
  }
}

/** 可见性档位 → 容器 Tailwind 类（`reserved` 与 `always` 不加显隐类）。 */
export function visibilityClass(v: SlotVisibility): string {
  switch (v) {
    case 'md':
      return 'hidden md:block'
    case 'lg':
      return 'hidden lg:flex'
    case 'always':
    case 'reserved':
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
