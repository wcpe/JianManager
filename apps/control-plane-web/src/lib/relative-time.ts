/**
 * 把一个过去时刻格式化为「相对当前的中文近似时间」（FR-186 系统更新页「上次检查：<相对时间>」）。
 * 纯函数、可注入 now 便于测试；非法/空入参返回空串（调用方据此不展示）。
 */

/** formatRelativeTime 的可选项。 */
export interface RelativeTimeOptions {
  /** 参照「现在」的毫秒时间戳；省略取 Date.now()（注入便于测试）。 */
  now?: number
}

/**
 * 将 ISO 时间字符串（或毫秒时间戳）格式化为「刚刚 / N 分钟前 / N 小时前 / N 天前 / 具体日期」。
 * - < 1 分钟 → 「刚刚」
 * - < 1 小时 → 「N 分钟前」
 * - < 24 小时 → 「N 小时前」
 * - < 7 天 → 「N 天前」
 * - 其余 → 本地化日期（toLocaleDateString）
 * 未来时间按「刚刚」处理（时钟偏差容错）。非法入参返回 ''。
 */
export function formatRelativeTime(input: string | number | null | undefined, opts: RelativeTimeOptions = {}): string {
  if (input === null || input === undefined || input === '') return ''
  const ts = typeof input === 'number' ? input : Date.parse(input)
  if (Number.isNaN(ts)) return ''

  const now = opts.now ?? Date.now()
  const diffMs = now - ts
  // 未来时间（时钟偏差）按刚刚。
  if (diffMs < 0) return '刚刚'

  const sec = Math.floor(diffMs / 1000)
  if (sec < 60) return '刚刚'
  const min = Math.floor(sec / 60)
  if (min < 60) return `${min} 分钟前`
  const hour = Math.floor(min / 60)
  if (hour < 24) return `${hour} 小时前`
  const day = Math.floor(hour / 24)
  if (day < 7) return `${day} 天前`
  return new Date(ts).toLocaleDateString()
}
