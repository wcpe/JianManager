/**
 * 侧栏折叠/展开触发器的无障碍标签（FR-181，增强 FR-131）。
 *
 * 语义规则：标签描述「将发生的动作」而非当前态——展开时取 collapseSidebar、折叠时取 expandSidebar。
 *
 * 消费者变迁：本函数最初服务顶栏 logo（把图标+文字整体当折叠开关）。顶栏那处已在
 * 「折叠后无法展开」的修复中删除（它在折叠态被条件卸载），现在唯一消费者是
 * `WorkspaceSidebar` 底部的常驻开关——但规则本身没变，故保留在此并继续被测试覆盖。
 */

/** 依据当前折叠态，返回 logo 点击触发器应使用的 i18n 标签 key。 */
export function logoToggleLabelKey(collapsed: boolean): string {
  return collapsed ? 'nav.expandSidebar' : 'nav.collapseSidebar'
}