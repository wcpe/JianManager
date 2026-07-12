import { useParams } from 'react-router'
import { useTranslation } from 'react-i18next'

import InstanceConsoleCache from '@/components/console/InstanceConsoleCache'

/**
 * 服务器详情路由（`/instances/:id`，FR-269）：单服默认入口为固定分区的服务器统一控制台。
 * FR-296（ADR-067）后由跨服热缓存宿主渲染——最近 ≤3 个服控制台整体保活，切换瞬时呈现；
 * 配合 Workspace 路由 key 归并，实例间切换不触发路由级 remount。
 * FR-166 可组合画布仍保留在高级工作区/导播台路径，不再作为该深链的唯一渲染结果。
 */
export default function InstanceDetailPage() {
  const { id } = useParams<{ id: string }>()
  const { t } = useTranslation()
  const instanceId = Number(id)

  if (!Number.isFinite(instanceId) || instanceId <= 0) {
    return <p className="text-muted-foreground">{t('serverConsole.noInstance')}</p>
  }

  return <InstanceConsoleCache instanceId={instanceId} />
}
