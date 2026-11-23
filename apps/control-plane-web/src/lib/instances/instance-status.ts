/**
 * 实例状态 → 状态点类型的归并（纯逻辑）。
 *
 * 归包理由：状态点是多棵实例树/列表共用的展示元素，把「哪些状态算运行中/过渡中/崩溃」
 * 这一判断与渲染放在一起，避免每处各写一份 switch。
 */

/** 状态点类型：绿（运行中）/ 琥珀（过渡中）/ 红（崩溃）/ 空心灰（已停止）。 */
export type StatusDotKind = 'running' | 'transitioning' | 'crashed' | 'stopped'

/** 把实例状态字符串归到状态点类型；未知状态按「已停止」处理。 */
export function statusDotKind(status: string): StatusDotKind {
  switch (status) {
    case 'RUNNING':
      return 'running'
    case 'STARTING':
    case 'STOPPING':
      return 'transitioning'
    case 'CRASHED':
    case 'DAMAGED':
      return 'crashed'
    default:
      return 'stopped'
  }
}
