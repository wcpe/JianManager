import { toast } from 'sonner'
import {
  useApprovedLogAssets,
  useControlLogRuntime,
  useInstallLogAsset,
  useLogRuntime,
  useMigrateLogPartition,
  useResolveLogIngestGaps,
  useUploadLogAsset,
} from '@/api/logRuntime'
import NodeLogRuntimePanel, {
  namespaceNames,
  type LogNamespace,
} from '@jianmanager/ui/components/views/nodes/NodeLogRuntimePanel'

/** 后端错误消息：优先 `message`，回退 `error` 字段（沿用迁移前的取值顺序）。 */
function errorMessage(error: unknown): string {
  const response = (error as { response?: { data?: { message?: string; error?: string } } })?.response
  return response?.data?.message || response?.data?.error || '操作失败'
}

/** 分区动作的成功提示文案。 */
const actionLabel: Record<'start' | 'stop' | 'restart', string> = {
  start: '已启动',
  stop: '已停止',
  restart: '已重启',
}

/**
 * VictoriaLogs 运行时分段外壳（ADR-097 b 范式）。
 *
 * 七个动作（上传/下发/起停重启/迁移/核销）与两个查询全在这里，连同它们各自的
 * 提示文案与错误转换。受控视图只把「用户点了哪个分区的哪个动作」「选了哪个日期」
 * 上报上来，并按注入的 `busy` 统一禁用。
 */
export default function NodeLogRuntimeTab({
  nodeId,
  os,
  arch,
  online,
}: {
  nodeId: number
  os: string
  arch: string
  online: boolean
}) {
  const runtime = useLogRuntime(nodeId, true)
  const assets = useApprovedLogAssets(true)
  const upload = useUploadLogAsset()
  const install = useInstallLogAsset(nodeId)
  const control = useControlLogRuntime(nodeId)
  const migrate = useMigrateLogPartition(nodeId)
  const resolveGaps = useResolveLogIngestGaps(nodeId)

  const normalizedArch = arch.toLowerCase() === 'x64' ? 'amd64' : arch.toLowerCase()
  const approved = assets.data?.find((asset) => asset.os === os.toLowerCase() && asset.arch === normalizedArch)
  const busy = upload.isPending || install.isPending || control.isPending || migrate.isPending || resolveGaps.isPending

  return (
    <NodeLogRuntimePanel
      os={os}
      arch={arch}
      online={online}
      runtime={runtime.data}
      approvedCached={approved?.cached}
      runtimeError={runtime.isError ? errorMessage(runtime.error) : undefined}
      assetsError={assets.isError ? errorMessage(assets.error) : undefined}
      busy={busy}
      fetching={runtime.isFetching}
      pending={{
        upload: upload.isPending,
        install: install.isPending,
        control: control.isPending,
        migrate: migrate.isPending,
        gaps: resolveGaps.isPending,
      }}
      onRefresh={() => {
        void runtime.refetch()
        void assets.refetch()
      }}
      onUpload={async (file) => {
        try {
          await upload.mutateAsync({ os: os.toLowerCase(), arch: normalizedArch, file })
          toast.success('审批包已缓存')
          return true
        } catch (error) {
          toast.error(errorMessage(error))
          return false
        }
      }}
      onInstall={async () => {
        try {
          await install.mutateAsync(undefined)
          toast.success('受管资产已安装')
          return true
        } catch (error) {
          toast.error(errorMessage(error))
          return false
        }
      }}
      onControl={async (namespace: LogNamespace, action: 'start' | 'stop' | 'restart') => {
        try {
          await control.mutateAsync({ namespace, action })
          toast.success(`${namespaceNames[namespace]} ${actionLabel[action]}`)
          return true
        } catch (error) {
          toast.error(errorMessage(error))
          return false
        }
      }}
      onMigrate={async (utcDay) => {
        try {
          // 存储命名空间由节点 id 拼装——这是应用侧的知识，故留在外壳。
          await migrate.mutateAsync({ storageNamespace: `node:${nodeId}`, utcDay })
          toast.success('分区已迁移到 COLD')
          return true
        } catch (error) {
          toast.error(errorMessage(error))
          return false
        }
      }}
      onResolveGaps={async () => {
        try {
          await resolveGaps.mutateAsync(undefined)
          toast.success('已核销 projection 覆盖的缺口')
          return true
        } catch (error) {
          toast.error(errorMessage(error))
          return false
        }
      }}
    />
  )
}
