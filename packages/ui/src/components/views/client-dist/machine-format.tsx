import type { TFunction } from 'i18next'
import { Badge } from '@jianmanager/ui/components/badge'

/**
 * 机器更新清单/时间线共用的格式化与徽标（FR-426）。
 * 从应用侧 `MachineListPanel` 原样提取，供面板与时间线两个展示组件复用。
 */

/** 字节数转人类可读（1024 进制）。 */
export function fmtBytes(b: number): string {
  if (!Number.isFinite(b) || b <= 0) return '0'
  if (b >= 1e9) return `${(b / 1024 / 1024 / 1024).toFixed(1)}G`
  if (b >= 1e6) return `${(b / 1024 / 1024).toFixed(0)}M`
  if (b >= 1e3) return `${(b / 1024).toFixed(0)}K`
  return String(b)
}

/** ISO 时间转本地字符串（非法值原样返回）。 */
export function fmtTime(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}

/** 更新结果徽标。 */
export function resultBadge(t: TFunction, result: string) {
  switch (result) {
    case 'success': return <Badge className="bg-emerald-600">{t('clientDistObs.resultSuccess', '成功')}</Badge>
    case 'fail-static': return <Badge variant="outline" className="border-amber-500/50 text-amber-600">{t('clientDistObs.resultFailStatic', '失败静态')}</Badge>
    case 'rollback': return <Badge variant="outline">{t('clientDistObs.resultRollback', '回滚')}</Badge>
    default: return <Badge variant="destructive">{t('clientDistObs.resultError', '错误')}</Badge>
  }
}

/** 版本滞后徽标。 */
export function lagBadge(t: TFunction, lag: number) {
  if (lag <= 0) return <Badge variant="outline" className="text-emerald-600">{t('clientDistObs.lagLatest', '已最新')}</Badge>
  if (lag >= 3) return <Badge variant="outline" className="border-amber-500/50 text-amber-600">{t('clientDistObs.lagBehind', '落后 {{n}} 版', { n: lag })}</Badge>
  return <Badge variant="outline" className="text-muted-foreground">{t('clientDistObs.lagBehind', '落后 {{n}} 版', { n: lag })}</Badge>
}
