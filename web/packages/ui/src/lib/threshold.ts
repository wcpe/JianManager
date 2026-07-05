/**
 * 阈值 → 状态等级映射（FR-061）。
 * 资源/TPS/实例状态按阈值归一为状态等级，供仪表盘/迷你条/徽章统一着色，异常自浮现。
 */

/** 状态等级：与 index.css 的 --status-* 色系一一对应；neutral 用前景弱色。 */
export type StatusLevel = 'success' | 'warning' | 'danger' | 'info' | 'neutral'

/** 资源占用率（0~100）→ 等级：<50 正常 / 50–80 警告 / >80 危险。 */
export function resourceLevel(pct: number): StatusLevel {
  if (!Number.isFinite(pct)) return 'neutral'
  if (pct > 80) return 'danger'
  if (pct >= 50) return 'warning'
  return 'success'
}

/** TPS（0~20）→ 等级：≥18 正常 / 15–18 警告 / <15 危险。 */
export function tpsLevel(tps: number): StatusLevel {
  if (!Number.isFinite(tps) || tps < 0) return 'neutral'
  if (tps >= 18) return 'success'
  if (tps >= 15) return 'warning'
  return 'danger'
}

/** 实例运行状态 → 等级：运行=正常，启停中=警告，崩溃=危险，停止/未知=中性。 */
export function instanceStatusLevel(status: string): StatusLevel {
  switch (status) {
    case 'RUNNING':
      return 'success'
    case 'STARTING':
    case 'STOPPING':
      return 'warning'
    case 'CRASHED':
      return 'danger'
    default:
      return 'neutral'
  }
}

/** 等级 → CSS 颜色变量引用，用于 SVG/内联样式（仪表盘描边、图表）。 */
export function statusColorVar(level: StatusLevel): string {
  switch (level) {
    case 'success':
      return 'var(--status-success)'
    case 'warning':
      return 'var(--status-warning)'
    case 'danger':
      return 'var(--status-danger)'
    case 'info':
      return 'var(--status-info)'
    default:
      return 'var(--muted-foreground)'
  }
}

/** 等级 → 文字色 Tailwind 类，用于行内数值着色。 */
export function statusTextClass(level: StatusLevel): string {
  switch (level) {
    case 'success':
      return 'text-status-success'
    case 'warning':
      return 'text-status-warning'
    case 'danger':
      return 'text-status-danger'
    case 'info':
      return 'text-status-info'
    default:
      return 'text-muted-foreground'
  }
}
