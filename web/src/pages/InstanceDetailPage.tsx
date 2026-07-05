import { useParams } from 'react-router'
import { useTranslation } from 'react-i18next'

import InstanceConsolePage from '@/components/console/InstanceConsolePage'

/**
 * 服务器详情路由（`/instances/:id`，FR-269）：单服默认入口为固定分区的服务器统一控制台。
 * FR-166 可组合画布仍保留在高级工作区/导播台路径，不再作为该深链的唯一渲染结果。
 */
export default function InstanceDetailPage() {
  const { id } = useParams<{ id: string }>()
  const { t } = useTranslation()
  const instanceId = Number(id)

  if (!Number.isFinite(instanceId) || instanceId <= 0) {
    return <p className="text-muted-foreground">{t('serverConsole.noInstance')}</p>
  }

  return <InstanceConsolePage instanceId={instanceId} />
}
