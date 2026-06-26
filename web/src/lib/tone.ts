/**
 * 语义色调 → 图标块样式（FR-163）。
 * 统一卡片原语（Panel/StatCard）的图标块取色：主色走淡染 accent，状态色走 12% 底 + 状态前景。
 */
import type { StatusLevel } from '@/lib/threshold'

/** 色调：主色或四档状态色。 */
export type Tone = 'primary' | StatusLevel

/** 色调 → 图标块底色 + 前景 Tailwind 类。 */
export function toneChipClass(tone: Tone): string {
  switch (tone) {
    case 'primary':
      return 'bg-accent text-primary'
    case 'success':
      return 'bg-status-success/12 text-status-success'
    case 'warning':
      return 'bg-status-warning/12 text-status-warning'
    case 'danger':
      return 'bg-status-danger/12 text-status-danger'
    case 'info':
      return 'bg-status-info/12 text-status-info'
    default:
      return 'bg-muted text-muted-foreground'
  }
}
