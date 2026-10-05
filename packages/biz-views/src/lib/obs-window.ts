/**
 * 分发观测时间窗（FR-425）：预设档 或 任意起止（RFC3339）。
 * 后端 parseObsRange 原生支持 from/to，本类型只做前端状态与 API 参数的桥。
 */
import type { TFunction } from 'i18next'

export type ObsWindow = { range: string } | { from: string; to: string }

export const OBS_PRESETS = ['24h', '7d', '30d', '90d', '180d'] as const

export type TFunc = TFunction

export function presetLabel(range: string, t: TFunc): string {
  const map: Record<string, string> = {
    '24h': t('clientDistObs.range24h', '近 24 小时'),
    '7d': t('clientDistObs.range7d', '近 7 天'),
    '30d': t('clientDistObs.range30d', '近 30 天'),
    '90d': t('clientDistObs.range90d', '近 90 天'),
    '180d': t('clientDistObs.range180d', '近 180 天'),
  }
  return map[range] ?? range
}

export function toLocalInput(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function fmtRange(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(d.getMonth() + 1)}/${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** 窗口展示文案（页面头部/标题复用）。 */
export function obsWindowLabel(w: ObsWindow, t: TFunc): string {
  if ('range' in w) return presetLabel(w.range, t)
  return `${fmtRange(w.from)} ~ ${fmtRange(w.to)}`
}

/** 把窗口转成 API 参数（from/to 优先，回退 range）。 */
export function obsWindowParams(w: ObsWindow): { range?: string; from?: string; to?: string } {
  return 'range' in w ? { range: w.range } : { from: w.from, to: w.to }
}
