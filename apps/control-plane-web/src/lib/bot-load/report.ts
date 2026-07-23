import type { BotLoadReport } from './types'
import { BOT_CHAT_SUCCESS_DISCLAIMER_ZH } from './types'

/** 报告下载文件名。 */
export function reportFilename(runUuid: string, format: 'json' | 'csv'): string {
  return `bot-load-${runUuid}.${format}`
}

/** 触发浏览器下载 blob。 */
export function downloadBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.rel = 'noopener'
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

/** 确保报告含免责声明（后端缺失时前端兜底展示用，不改写下载内容）。 */
export function reportDisclaimer(report?: BotLoadReport | null): string {
  return report?.disclaimer?.trim() || BOT_CHAT_SUCCESS_DISCLAIMER_ZH
}
