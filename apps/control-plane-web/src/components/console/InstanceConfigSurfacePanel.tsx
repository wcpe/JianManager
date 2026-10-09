import { Link } from 'react-router'
import { InstanceConfigSurfacePanel as InstanceConfigSurfacePanelView } from '@/components/views/console/InstanceConfigSurfacePanel'
import { useConfigSurface, useUpdateConfigSurface } from '@/api/configSurface'

/**
 * 实例「关键配置」面板的应用接线层（ADR-097）。
 *
 * 视图本体已迁入组件库并受控；本层取清单、把保存接到 mutation，并注入「打开文件」的路由链接，
 * 保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function InstanceConfigSurfacePanel({ instanceId }: { instanceId: number }) {
  const { data: items, isLoading } = useConfigSurface(instanceId)
  const update = useUpdateConfigSurface(instanceId)

  return (
    <InstanceConfigSurfacePanelView
      instanceId={instanceId}
      items={items}
      isLoading={isLoading}
      isSaving={update.isPending}
      onSave={(updates) => update.mutate(updates)}
      renderLink={({ to, className, children }) => (
        <Link to={to} className={className}>
          {children}
        </Link>
      )}
    />
  )
}
