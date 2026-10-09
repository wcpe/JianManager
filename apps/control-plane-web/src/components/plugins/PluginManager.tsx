import { useState } from 'react'
import { toast } from 'sonner'
import { PluginManager as PluginManagerImpl } from '@/components/views/plugins/PluginManager'
import type { PluginManagerProps as PluginManagerPropsFull } from '@jianmanager/ui'
import {
  usePlugins,
  useUploadPlugin,
  useDeletePlugin,
  useTogglePlugin,
  usePluginBatchDeploy,
} from '@/api/plugins'
import { useRuntimeAssetsOverview } from '@/api/runtimeAssets'
import { useInstanceSearch } from '@/api/instances'

/**
 * 对外 props（沿用原导出名）：接线层自己注入的字段从调用点口径里排除，
 * 调用点（WorkspaceCardBody 等）只传原来那套 props，零改动。
 */
export type PluginManagerProps = Omit<
  PluginManagerPropsFull,
  | 'plugins'
  | 'isLoading'
  | 'errorText'
  | 'isUploading'
  | 'onUpload'
  | 'isToggling'
  | 'onToggle'
  | 'isDeleting'
  | 'onDelete'
  | 'assets'
  | 'isAssetsLoading'
  | 'instances'
  | 'isInstancesLoading'
  | 'isDeploying'
  | 'onBatchDeploy'
  | 'deployResult'
  | 'onNotify'
>

/**
 * 插件/模组管理面板的应用接线层（ADR-097）。
 *
 * 视图本体已迁入组件库并受控；本层注入：插件列表与加载/失败态、四种操作（上传含进度回传 /
 * 启停 / 删除 / 批量部署）、制品库与实例候选，以及把提示回执接回 toast。其余 props 原样透传。
 */
export default function PluginManager({ instanceId }: PluginManagerProps) {
  const pluginsQ = usePlugins(instanceId)
  const upload = useUploadPlugin(instanceId)
  const toggle = useTogglePlugin(instanceId)
  const remove = useDeletePlugin(instanceId)
  const assetsQ = useRuntimeAssetsOverview()
  const batchDeploy = usePluginBatchDeploy()
  // 批量部署对话框的实例搜索词（视图持有输入态，此处据此外发查询）。
  const [instanceQuery, setInstanceQuery] = useState('')
  const instancesQ = useInstanceSearch(
    { q: instanceQuery || undefined, pageSize: 20, sort: 'name', order: 'asc' },
    true,
  )

  return (
    <PluginManagerImpl
      instanceId={instanceId}
      plugins={pluginsQ.data}
      isLoading={pluginsQ.isLoading}
      errorText={
        pluginsQ.error
          ? (pluginsQ.error as Error & { response?: { data?: { message?: string } } }).response?.data?.message ||
            (pluginsQ.error as Error).message
          : undefined
      }
      isUploading={upload.isPending}
      onUpload={({ file, dir, overwrite, onProgress, onSettled }) =>
        upload.mutate({ file, dir, overwrite, onProgress }, { onSettled })
      }
      isToggling={toggle.isPending}
      onToggle={({ name, dir }) => toggle.mutate({ name, dir })}
      isDeleting={remove.isPending}
      onDelete={({ name, dir }) => remove.mutate({ name, dir })}
      assets={assetsQ.data?.assets.find((g) => g.type === 'plugin')?.items ?? []}
      isAssetsLoading={assetsQ.isLoading}
      instances={instancesQ.data?.items ?? []}
      isInstancesLoading={instancesQ.isLoading}
      isDeploying={batchDeploy.isPending}
      onBatchDeploy={(args) => batchDeploy.mutate(args)}
      deployResult={batchDeploy.data ?? null}
      onInstanceQueryChange={setInstanceQuery}
      onNotify={(kind, message) =>
        kind === 'success' ? toast.success(message) : kind === 'error' ? toast.error(message) : toast(message)
      }
    />
  )
}
