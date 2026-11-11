import type { LogEntry } from '@/lib/console/console-log-types'

/**
 * 把选中的日志行格式化成可粘贴的纯文本。
 *
 * # 为何不是「按行拼接 message」
 *
 * 复制日志的目的几乎都是拿去贴给别人或提工单，接收方需要能对上「哪一行、什么时候、哪条流」。
 * 只给正文会让上下文彻底丢失。但也**不能**把 JSON 全字段倒出来——那反而没法读。
 * 折中是「时间 + 级别 + 来源 + 正文」，正文里的换行保留。
 *
 * # 为何时间用本地时间字符串而非原始 RFC3339
 *
 * 排查的人是在浏览器上看这些行的，复制出去后与屏幕上看到的必须能对上；
 * 时区换算会让人以为复制错了行。
 */
export interface CopyFormatOptions {
  /** 每条日志的「具体来源」显示名（实例名/节点名），由调用方反查后传入。 */
  sourceLabelOf: (log: LogEntry) => string
  /** 时间格式化，默认按本地时区。 */
  formatTime?: (raw: string) => string
}

function defaultFormatTime(raw: string): string {
  const d = new Date(raw)
  if (Number.isNaN(d.getTime())) return raw
  return d.toLocaleString()
}

/** 单行紧凑文本（不含换行）。 */
export function formatLogLine(log: LogEntry, options: CopyFormatOptions): string {
  const time = (options.formatTime ?? defaultFormatTime)(log.time)
  const level = (log.level || 'info').toUpperCase()
  const source = options.sourceLabelOf(log)
  return `${time}\t${level}\t${source}\t${log.message}`
}

/** 多行文本：一行一条，末尾无多余空行。 */
export function formatLogsForCopy(items: LogEntry[], options: CopyFormatOptions): string {
  return items.map((log) => formatLogLine(log, options)).join('\n')
}
