/**
 * StatCard「按指标混搭」逻辑（FR-163）。
 * KPI 卡右侧视觉随指标性质而变（设计 design.md §1.3）：占比→条、走势→趋势线、计数→双值。
 * 纯函数下沉便于单测，组件只做渲染。
 */
import type { StatusLevel } from '@/lib/threshold'

/** 指标性质：占比 / 走势 / 计数 / 普通数值。 */
export type StatKind = 'ratio' | 'trend' | 'count' | 'plain'

/** 右侧视觉形态：迷你条 / 趋势线 / 双值（主/总）/ 无。 */
export type StatVisual = 'bar' | 'trend' | 'dual' | 'none'

/** 指标性质 → 右侧视觉（「按指标混搭」）。 */
export function pickStatVisual(kind: StatKind): StatVisual {
  switch (kind) {
    case 'ratio':
      return 'bar'
    case 'trend':
      return 'trend'
    case 'count':
      return 'dual'
    default:
      return 'none'
  }
}

/**
 * 增量着色：方向 → 箭头 + 状态等级。
 * `goodWhen` 指明「哪个方向是好的」：默认 `up`（如在线玩家上升=好，绿）；
 * `down` 用于「越低越好」的指标（如内存占用、延迟，上升=坏，红）。
 * 零 / 非数值不出增量（返回 null）。
 */
export function deltaTone(
  delta: number,
  goodWhen: 'up' | 'down' = 'up',
): { arrow: '↑' | '↓'; level: StatusLevel } | null {
  if (!Number.isFinite(delta) || delta === 0) return null
  const up = delta > 0
  const good = up === (goodWhen === 'up')
  return { arrow: up ? '↑' : '↓', level: good ? 'success' : 'danger' }
}
