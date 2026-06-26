import { useEffect } from 'react'
import { useParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { useConsoleStore } from '@/stores/console'

/**
 * 实例详情路由（`/instances/:id`）——深链入口（FR-166）。
 *
 * 工作区已从固定六 Tab 升级为可组合卡片画布（{@link WorkspaceCanvas}，ADR「可组合卡片工作区」
 * 取代 ADR-030）。控制台内打开实例统一走 `openInstance`（console store）渲染画布；
 * 本路由仅作直链/书签回退，挂载时把实例打开进画布，行为与控制台内一致。
 */
export default function InstanceDetailPage() {
  const { id } = useParams<{ id: string }>()
  const { t } = useTranslation()
  const openInstance = useConsoleStore((s) => s.openInstance)

  useEffect(() => {
    const instanceId = Number(id)
    if (Number.isFinite(instanceId) && instanceId > 0) {
      openInstance(instanceId)
    }
  }, [id, openInstance])

  // 画布由 Workspace 在 openInstanceId 置位后接管渲染；此处仅在切换间隙显示占位。
  return <p className="text-muted-foreground">{t('common.loading')}</p>
}
