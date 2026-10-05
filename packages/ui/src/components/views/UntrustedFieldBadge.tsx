import { useTranslation } from 'react-i18next'
import { Badge } from '@jianmanager/ui/components/badge'

/** 标识客户端自报、不可作为授权依据的字段（文案外置 FR-430）。 */
export default function UntrustedFieldBadge() {
  const { t } = useTranslation()
  return (
    <Badge variant="outline" className="border-status-warning/40 px-1.5 py-0 text-[10px] text-status-warning">
      {t('clientDistOps.untrusted')}
    </Badge>
  )
}
