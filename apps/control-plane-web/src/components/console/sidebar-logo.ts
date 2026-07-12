/**
 * 顶部 logo 折叠/展开触发器辅助（FR-181，增强 FR-131）。
 *
 * logo（Boxes 图标 + JianManager 文字）整体作为一个按钮复用 console store 的 `toggleSidebar`：
 * 展开态点击=收起、折叠态点击=展开。其无障碍标签描述「将发生的动作」而非当前态，
 * 故展开时取 collapseSidebar、折叠时取 expandSidebar（i18n key）。
 */

/** 依据当前折叠态，返回 logo 点击触发器应使用的 i18n 标签 key。 */
export function logoToggleLabelKey(collapsed: boolean): string {
  return collapsed ? 'nav.expandSidebar' : 'nav.collapseSidebar'
}
