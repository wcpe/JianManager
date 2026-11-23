/**
 * 日志洞察卡片的非组件导出（类型与颜色映射）。
 *
 * 为何单独成文件：`LogsInsightCards.tsx` 里既有 React 组件、又有工具函数与类型。
 * React Fast Refresh 要求一个文件只导出组件，否则热更新会整页刷新甚至丢状态。
 * 所以把非组件部分挪到本文件，让那个 `.tsx` 只剩组件导出。
 */

/** 一个维度值及其计数。 */
export interface FacetValue {
  value: string
  count: number
}

export interface LevelCount {
  level: string
  count: number
}

/**
 * 日志级别 → 颜色 token。
 *
 * 与日志行左侧色条、告警页、标签页同一套 `status-*` token，不另造颜色体系。
 */
export function levelColorClass(level: string): string {
  switch (level.toLowerCase()) {
    case 'error':
    case 'fatal':
      return 'bg-status-danger'
    case 'warn':
    case 'warning':
      return 'bg-status-warning'
    case 'info':
      return 'bg-status-info'
    default:
      return 'bg-muted-foreground/50'
  }
}
