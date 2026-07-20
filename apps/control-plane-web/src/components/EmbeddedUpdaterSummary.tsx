import { useTranslation } from 'react-i18next'
import { useUpdaterJarsInfo } from '@/api/clientChannels'

/** 管理面旁路展示 Control Plane 当前内嵌更新器版本。 */
export default function EmbeddedUpdaterSummary({ className = '' }: { className?: string }) {
  const { t } = useTranslation()
  const { data } = useUpdaterJarsInfo()

  if (!data) return null

  return (
    <p className={`text-xs text-muted-foreground ${className}`} data-testid="embedded-updater-summary">
      {t('clientVersions.embeddedUpdaterSummary', '内嵌更新器 v{{version}} · core {{coreVersion}}', {
        version: data.version,
        coreVersion: data.coreVersion,
      })}
    </p>
  )
}
