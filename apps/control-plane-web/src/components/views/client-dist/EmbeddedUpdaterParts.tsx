import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Download } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'

// ── 内嵌更新器版本摘要 ───────────────────────────────────────────────

export interface EmbeddedUpdaterSummaryProps {
  /** 更新器信息（容器经 useUpdaterJarsInfo 取数）；缺省不渲染。 */
  info?: { version: string; coreVersion: string }
  className?: string
}

/** 管理面旁路展示 Control Plane 当前内嵌更新器版本。 */
export function EmbeddedUpdaterSummary({ info, className = '' }: EmbeddedUpdaterSummaryProps) {
  const { t } = useTranslation()
  if (!info) return null

  return (
    <p className={`text-xs text-muted-foreground ${className}`} data-testid="embedded-updater-summary">
      {t('clientVersions.embeddedUpdaterSummary', '内嵌更新器 v{{version}} · core {{coreVersion}}', {
        version: info.version,
        coreVersion: info.coreVersion,
      })}
    </p>
  )
}

// ── 分发数据导出按钮 ─────────────────────────────────────────────────

export interface ClientDistExportButtonProps {
  /** 导出类型（透传给容器注入的导出实现）。 */
  kind: string
  /** 导出筛选条件（透传）。 */
  filters: unknown
  size?: 'xs' | 'sm' | 'default' | 'lg' | 'icon' | 'icon-sm' | 'icon-lg'
  /**
   * 执行导出（容器注入 `exportClientDistCSV` + `saveClientDistCSV`）。
   * 抛错时由本组件按状态码给出限流/失败文案。
   */
  onExport: (kind: string, filters: unknown) => Promise<void>
  /** 结果回执（容器注入 toast；组件库不依赖 toast 实现）。 */
  onNotify?: (level: 'success' | 'error', message: string) => void
}

/**
 * 客户端分发数据导出按钮：执行导出并给出结果回执。
 * 429 视为限流（给出可操作文案），其余为失败。
 */
export function ClientDistExportButton({
  kind,
  filters,
  size = 'sm',
  onExport,
  onNotify,
}: ClientDistExportButtonProps) {
  const { t } = useTranslation()
  const [exporting, setExporting] = useState(false)

  const runExport = async () => {
    setExporting(true)
    try {
      await onExport(kind, filters)
      onNotify?.('success', t('clientDistExport.success'))
    } catch (error) {
      const status = (error as { response?: { status?: number } }).response?.status
      onNotify?.('error', status === 429 ? t('clientDistExport.rateLimited') : t('clientDistExport.failed'))
    } finally {
      setExporting(false)
    }
  }

  return (
    <Button type="button" variant="outline" size={size} disabled={exporting} onClick={runExport}>
      <Download className="size-3.5" />
      {exporting ? t('clientDistExport.exporting') : t('clientDistExport.button')}
    </Button>
  )
}
