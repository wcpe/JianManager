import { toast } from 'sonner'
import { useTranslation } from 'react-i18next'
import { useAuthStore } from '@/stores/auth'
import { usePermissionsStore } from '@/stores/permissions'
import { useApprovedLogAssets, useControlLogRuntime, useInstallLogAsset, useLogRuntime, useMigrateLogPartition, useResolveLogIngestGaps, useUploadLogAsset } from '@/api/logRuntime'
import NodeLogRuntimePanel, {
  namespaceNames,
  type LogNamespace,
} from '@/components/views/nodes/NodeLogRuntimePanel'

/**
 * 后端错误消息：优先 `message`，回退 `error` 字段（沿用迁移前的取值顺序）。
 * 两者都没有时用调用方给的兜底文案（已本地化），不在这里写死中文。
 */
function errorMessage(error: unknown, fallback: string): string {
  const response = (error as { response?: { data?: { message?: string; error?: string } } })?.response
  return response?.data?.message || response?.data?.error || fallback
}

/**
 * VictoriaLogs 运行时分段外壳。
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
  const { t } = useTranslation()
  /** 分区动作的成功提示文案（随语言切换，故在组件内建表）。 */
  const actionLabel: Record<'start' | 'stop' | 'restart', string> = {
    start: t('logsRuntime.action.start'),
    stop: t('logsRuntime.action.stop'),
    restart: t('logsRuntime.action.restart'),
  }
  const failed = t('logsRuntime.opFailed')
  /**
   * 是否可以执行**整日分区迁移**（跨命名空间搬 HOT→COLD，不可逆）。
   *
   * 只允许平台管理员：门禁放在外壳（本组件）而非受控视图，因为判定依赖
   * auth/me 与 JWT 两个 store，属于应用侧知识；视图只接一个已算好的布尔。
   *
   * `loaded` 为假时（auth/me 未回）**回退到 JWT 的 role=10**：把未加载的
   * `isPlatformAdmin=false` 当成「服务端显式拒绝」会让首次进页面的管理员
   * 短暂看到一个禁用按钮，那是误判而非安全。反过来，auth/me 已加载且明确
   * 不是管理员时，旧 JWT 里的 role=10 不得再把它洗回来。
   */
  const jwtRole = useAuthStore((state) => state.role)
  const permLoaded = usePermissionsStore((state) => state.loaded)
  const permIsAdmin = usePermissionsStore((state) => state.isPlatformAdmin)
  const canMigrate = permLoaded ? permIsAdmin : jwtRole === 10
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
      key={nodeId}
      nodeId={nodeId}
      os={os}
      arch={arch}
      online={online}
      runtime={runtime.data}
      approvedCached={approved?.cached}
      runtimeError={runtime.isError ? errorMessage(runtime.error, failed) : undefined}
      assetsError={assets.isError ? errorMessage(assets.error, failed) : undefined}
      busy={busy}
      fetching={runtime.isFetching}
      canMigrate={canMigrate}
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
          toast.success(t('logsRuntime.assetCached'))
          return true
        } catch (error) {
          toast.error(errorMessage(error, failed))
          return false
        }
      }}
      onInstall={async () => {
        try {
          await install.mutateAsync(undefined)
          toast.success(t('logsRuntime.assetInstalled'))
          return true
        } catch (error) {
          toast.error(errorMessage(error, failed))
          return false
        }
      }}
      onControl={async (namespace: LogNamespace, action: 'start' | 'stop' | 'restart') => {
        try {
          await control.mutateAsync({ namespace, action })
          toast.success(`${namespaceNames[namespace]} ${actionLabel[action]}`)
          return true
        } catch (error) {
          toast.error(errorMessage(error, failed))
          return false
        }
      }}
      onMigrate={async (utcDay) => {
        // 门禁在发请求前：越权即使界面被绕过也不得送到后端。
        if (!canMigrate) return t('logsRuntime.migration.denied')
        try {
          // 存储命名空间由节点 id 拼装——这是应用侧的知识，故留在外壳。
          await migrate.mutateAsync({ storageNamespace: `node:${nodeId}`, utcDay })
          toast.success(t('logsRuntime.migrateDone'))
          return null
        } catch (error) {
          // 错误文案不只给 toast：确认框要留着它让用户重试（409/502 处置不同）。
          const message = errorMessage(error, failed)
          toast.error(message)
          return message
        }
      }}
      onResolveGaps={async () => {
        try {
          await resolveGaps.mutateAsync(undefined)
          toast.success(t('logsRuntime.gapsResolved'))
          return true
        } catch (error) {
          toast.error(errorMessage(error, failed))
          return false
        }
      }}
    />
  )
}
