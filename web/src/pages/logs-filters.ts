/**
 * 日志中心纯函数助手（FR-150）。
 * 级别配色归一、时间范围预设、导出范围裁剪、虚拟滚动窗口计算——抽为纯函数便于 vitest 覆盖，
 * UI 组件只做渲染与状态编排。
 */
import type { StatusLevel } from '@/lib/threshold'
import type { LogQueryParams } from '@/api/logs'

/**
 * 日志级别 → 状态等级（FR-150）。
 * 经 StatusBadge 着色，使日志与告警页语义统一：error=danger、warn=warning、info=info、debug=neutral。
 * 着色全部走 `--status-*` token，不硬编码品牌色。
 */
export function logLevelStatus(level: string): StatusLevel {
  switch (level) {
    case 'error':
      return 'danger'
    case 'warn':
      return 'warning'
    case 'info':
      return 'info'
    default:
      // debug 及未知级别归中性，弱化呈现。
      return 'neutral'
  }
}

/** 时间范围预设：all 不限，其余为「最近 N」相对窗口。 */
export type TimeRangePreset = 'all' | '15m' | '1h' | '24h' | '7d'

/** 全部预设（供下拉渲染，顺序即展示顺序）。 */
export const TIME_RANGE_PRESETS: TimeRangePreset[] = ['all', '15m', '1h', '24h', '7d']

/** 各相对预设对应的毫秒跨度。 */
const PRESET_SPAN_MS: Record<Exclude<TimeRangePreset, 'all'>, number> = {
  '15m': 15 * 60 * 1000,
  '1h': 60 * 60 * 1000,
  '24h': 24 * 60 * 60 * 1000,
  '7d': 7 * 24 * 60 * 60 * 1000,
}

/**
 * 把时间范围预设转为 `from`/`to`（RFC3339）。
 * `all` 返回空对象（不约束时间）；相对预设以 `now` 为上界、回溯对应跨度为下界。
 * `now` 显式传入便于测试；UI 调用时传 `new Date()`。
 */
export function timeRangeToParams(
  preset: TimeRangePreset,
  now: Date,
): Pick<LogQueryParams, 'from' | 'to'> {
  if (preset === 'all') return {}
  const span = PRESET_SPAN_MS[preset]
  return {
    from: new Date(now.getTime() - span).toISOString(),
    to: now.toISOString(),
  }
}

/** 导出范围：当前页 / 全部匹配 / 时间段（FR-150 导出范围可选）。 */
export type LogExportScope = 'currentPage' | 'allMatched' | 'range'

/**
 * 按导出范围裁剪查询参数（FR-150）。
 * - currentPage：保留 page/pageSize，仅导出可见页。
 * - allMatched / range：去掉分页，导出整个筛选结果集（range 复用筛选区已设的 from/to）。
 * 始终返回新对象，不修改入参。未知范围按 allMatched 兜底。
 */
export function buildExportParams(base: LogQueryParams, scope: LogExportScope): LogQueryParams {
  const out: LogQueryParams = { ...base }
  if (scope === 'currentPage') return out
  // allMatched 与 range 都导出全量匹配；range 的时间窗已在 base.from/to 内。
  delete out.page
  delete out.pageSize
  return out
}

/** 虚拟滚动窗口计算入参。 */
export interface VirtualWindowInput {
  /** 滚动容器的 scrollTop（px）。 */
  scrollTop: number
  /** 滚动视口高度（px）。 */
  viewportHeight: number
  /** 单行固定高度（px）。 */
  rowHeight: number
  /** 行总数。 */
  total: number
  /** 视口上下额外预渲染的行数，避免快速滚动露白。 */
  overscan: number
}

/** 虚拟滚动窗口：仅渲染 [startIndex, endIndex) 区间，上下用 spacer 占位撑出滚动条。 */
export interface VirtualWindow {
  startIndex: number
  endIndex: number
  /** 顶部 spacer 高度（px）。 */
  padTop: number
  /** 底部 spacer 高度（px）。 */
  padBottom: number
}

/**
 * 计算固定行高列表的可见窗口（FR-150 虚拟滚动）。
 * 只渲染视口附近 overscan 圈内的行，其余以上下 spacer 占位，使千行级日志不一次性入 DOM。
 * 防御 rowHeight ≤ 0（退化为全量）与空列表。
 */
export function computeVirtualWindow(input: VirtualWindowInput): VirtualWindow {
  const { scrollTop, viewportHeight, rowHeight, total, overscan } = input
  if (total <= 0) return { startIndex: 0, endIndex: 0, padTop: 0, padBottom: 0 }
  if (rowHeight <= 0) {
    // 行高未知时不做窗口化，全量渲染（安全兜底）。
    return { startIndex: 0, endIndex: total, padTop: 0, padBottom: 0 }
  }

  const firstVisible = Math.floor(scrollTop / rowHeight)
  const visibleCount = Math.ceil(viewportHeight / rowHeight)

  const startIndex = Math.max(0, firstVisible - overscan)
  const endIndex = Math.min(total, firstVisible + visibleCount + overscan)

  return {
    startIndex,
    endIndex,
    padTop: startIndex * rowHeight,
    padBottom: (total - endIndex) * rowHeight,
  }
}
