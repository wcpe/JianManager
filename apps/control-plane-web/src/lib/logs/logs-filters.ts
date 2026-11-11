/**
 * 日志中心纯函数助手。
 * 级别配色归一、时间范围预设、导出范围裁剪、虚拟滚动窗口计算——抽为纯函数便于 vitest 覆盖，
 * UI 组件只做渲染与状态编排。
 */
import type { StatusLevel } from '@jianmanager/ui'
import type { LogQueryParams } from '@/api/logs'

/**
 * 日志中心固定主视图，顺序与管理员可见的切换控件一致。
 *
 * 2026-10-29：`legacy`（CP `logs` 表的切换前存量）随该表删除而下线，从取值域里移除。
 * 留着它会让 `?view=legacy` 深链落进一个「不存在的数据源」——页面既不报错也不给数据，
 * 是最难察觉的一类静默失效。现在这类深链按非法值回退默认视图。
 */
export const LOG_VIEWS = ['platform', 'node_instance', 'all'] as const

/**
 * 日志级别 → 状态等级。
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

/**
 * tail-first 分页窗口（纯函数）：以锚点为**窗口上界**、按预设窗宽整体前移，
 * 返回该窗应发的 `from/to`；首屏锚点为 now，已枚举完窗口的下一窗锚点为原 from。
 * 语义与首屏的 `timeParamsFor(range, anchor, false)` 完全一致（同一 `timeRangeToParams`）。
 *
 * 放在本模块而非页面组件里：页面文件只导出组件（react-refresh/only-export-components），
 * 纯函数与 `timeRangeToParams` 同域便于 vitest 直接覆盖。
 */
export function windowForAnchor(
  range: TimeRangePreset,
  anchor: string | number | Date,
): Pick<LogQueryParams, 'from' | 'to'> {
  return timeRangeToParams(range, new Date(anchor))
}

/** 所有联邦操作共用的条件；分页、游标和聚合维度不属于固定视图身份。 */
export interface LogQueryScope extends Omit<LogQueryParams, 'page' | 'pageSize'> {
  onlineOnly?: boolean
}

/** 固定字段顺序，同时剔除空条件，保证查询、聚合及导出的指纹一致。 */
export function normalizeLogScope(params: LogQueryScope): LogQueryScope {
  return {
    ...(params.view ? { view: params.view } : {}),
    ...(params.source ? { source: params.source } : {}),
    ...(params.level ? { level: params.level } : {}),
    ...(params.nodeId ? { nodeId: params.nodeId } : {}),
    ...(params.instanceId ? { instanceId: params.instanceId } : {}),
    ...(params.advanced
      ? { advanced: true, logsQL: params.logsQL?.trim() ?? '' }
      : params.keyword?.trim() ? { keyword: params.keyword.trim() } : {}),
    ...(params.onlineOnly ? { onlineOnly: true } : {}),
    ...(params.from ? { from: params.from } : {}),
    ...(params.to ? { to: params.to } : {}),
  }
}

/** 查询身份包含实际移窗边界，不能把占位旧页认作新条件的响应。 */
export function logScopeKey(params: LogQueryScope): string {
  return JSON.stringify(normalizeLogScope(params))
}

/** 导出范围：当前页 / 全部匹配 / 时间段（导出范围可选）。 */
export type LogExportScope = 'currentPage' | 'allMatched' | 'range'

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
 * 计算固定行高列表的可见窗口（虚拟滚动）。
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
