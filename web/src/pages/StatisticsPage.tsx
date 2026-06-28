import { useTranslation } from 'react-i18next'
import { BarChart3 } from 'lucide-react'

/**
 * 观测·统计页（FR-215 占位）。
 * 本 FR 仅放占位空状态；平台级聚合统计的实质内容由 FR-220 补齐。
 */
export default function StatisticsPage() {
  const { t } = useTranslation()
  return (
    <div className="space-y-4">
      <h1 className="text-xl font-bold">{t('statistics.title')}</h1>
      <div className="flex min-h-[50vh] flex-col items-center justify-center gap-3 rounded-lg border border-dashed text-center">
        <BarChart3 className="size-10 text-muted-foreground/50" />
        <p className="text-sm font-medium text-muted-foreground">{t('statistics.placeholder')}</p>
        <p className="text-xs text-muted-foreground/70">{t('statistics.placeholderHint')}</p>
      </div>
    </div>
  )
}
