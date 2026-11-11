import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { CircleHelp } from 'lucide-react'
import { Archive, Download, LoaderCircle, Play, RotateCw, Square, Upload } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Badge } from '@jianmanager/ui/components/badge'
import DangerConfirm from '@/components/views/common/DangerConfirm'
import { useAuthStore } from '@/stores/auth'
import { usePermissionsStore } from '@/stores/permissions'
import {
  useApprovedLogAssets,
  useControlLogRuntime,
  useInstallLogAsset,
  useLogRuntime,
  useMigrateLogPartition,
  useUploadLogAsset,
  type LogRuntimeInstance,
} from '@/api/logRuntime'

function errorMessage(error: unknown): string {
  const response = (error as { response?: { data?: { message?: string; error?: string } } })?.response
  return response?.data?.message || response?.data?.error || '操作失败'
}

/**
 * 后端错误的本地化呈现。
 *
 * 后端 `LogError.message` 是面向日志的英文诊断文本（如
 * "managed VictoriaLogs supervisor is not configured"），不适合直接展示给用户。
 * 这里按结构化 `code` 映射为可读中文；未知码才回退到原始 message。
 */
function localizedLogError(
  err: { code?: number; message?: string } | null | undefined,
  t: (k: string) => string,
): string | null {
  if (!err) return null
  // 与 proto LogErrorCode 数值对齐。
  const known: Record<number, string> = {
    3: t('logsRuntime.error.unauthorized'), // LOG_UNAUTHORIZED
    4: t('logsRuntime.error.budget'), // LOG_BUDGET_EXCEEDED
    5: t('logsRuntime.error.notReady'), // LOG_NOT_READY
    6: t('logsRuntime.error.archiveMissing'), // LOG_ARCHIVE_MISSING
    10: t('logsRuntime.error.unsupported'), // LOG_UNSUPPORTED
    // 11 = LOG_ARCHIVE_PURGED（FR-501 归档对象回收）。**必须**与 6 分开：
    // 6 的文案是「历史日志尚未取回」，引导用户去点恢复；而 11 的真相是
    // 「这段日志已按保留期删掉、恢复不了」。复用 6 的文案会让用户反复点恢复、
    // 反复得到第二条错误——那是平台对自己做的动作撒谎。
    11: t('logsRuntime.error.archivePurged'), // LOG_ARCHIVE_PURGED
  }
  return known[err.code ?? -1] ?? err.message ?? null
}

const namespaceNames: Record<LogRuntimeInstance['namespace'], string> = {
  hot: 'HOT',
  cold: 'COLD',
  rehydrate: 'Rehydrate',
}

export default function NodeLogRuntimePanel({ nodeId, os, arch, online }: {
  nodeId: number
  os: string
  arch: string
  online: boolean
}) {
  const { t } = useTranslation()
  const role = useAuthStore((state) => state.role)
  // 有效平台身份包含角色模板绑定；加载后的显式否定不能被旧JWT数字角色覆盖。
  const isPlatformAdmin = usePermissionsStore((state) => state.loaded ? state.isPlatformAdmin : role === 10)
  const fileInput = useRef<HTMLInputElement>(null)
  const runtime = useLogRuntime(nodeId, true)
  const assets = useApprovedLogAssets(true)
  const upload = useUploadLogAsset()
  const install = useInstallLogAsset(nodeId)
  const control = useControlLogRuntime(nodeId)
	const migrate = useMigrateLogPartition(nodeId)
	const [migrationDay, setMigrationDay] = useState('')
  const [migrationTarget, setMigrationTarget] = useState<{ nodeId: number; storageNamespace: string; utcDay: string } | null>(null)
  const [migrationError, setMigrationError] = useState('')
  const migrationInFlight = useRef(false)
  const confirmMigration = () => {
    if (!migrationTarget || migrationInFlight.current || !isPlatformAdmin) return
    migrationInFlight.current = true
    setMigrationError('')
    migrate.mutate({ storageNamespace: migrationTarget.storageNamespace, utcDay: migrationTarget.utcDay, targetNodeId: migrationTarget.nodeId }, {
      onSuccess: () => { toast.success(t('logsRuntime.migration.success')); setMigrationTarget(null) },
      onError: (error) => {
        const response = (error as { response?: unknown }).response
        const message = !response ? t('logsRuntime.migration.unknown') : errorMessage(error)
        setMigrationError(message)
        toast.error(message)
      },
      onSettled: () => { migrationInFlight.current = false },
    })
  }
  const normalizedArch = arch.toLowerCase() === 'x64' ? 'amd64' : arch.toLowerCase()
  const approved = assets.data?.find((asset) => asset.os === os.toLowerCase() && asset.arch === normalizedArch)
  const busy = upload.isPending || install.isPending || control.isPending || migrate.isPending

  const act = (namespace: LogRuntimeInstance['namespace'], action: 'start' | 'stop' | 'restart') => {
    control.mutate({ namespace, action }, {
      onSuccess: () => toast.success(`${namespaceNames[namespace]} ${action === 'start' ? '已启动' : action === 'stop' ? '已停止' : '已重启'}`),
      onError: (error) => toast.error(errorMessage(error)),
    })
  }

  return (
    <section aria-label="VictoriaLogs 运行时" className="border-t pt-4">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <div className="min-w-0">
          <h3 className="text-sm font-semibold">VictoriaLogs</h3>
          <p className="text-xs text-muted-foreground">
            v1.52.0 · {os} / {normalizedArch} · {approved?.cached ? '审批包已缓存' : '审批包未缓存'}
          </p>
        </div>
        <div className="flex items-center gap-1">
          <input
            ref={fileInput}
            type="file"
            accept=".gz,.zip"
            className="sr-only"
            aria-label="选择 VictoriaLogs 官方压缩包"
            onChange={(event) => {
              const file = event.currentTarget.files?.[0]
              if (!file) return
              upload.mutate({ os: os.toLowerCase(), arch: normalizedArch, file }, {
                onSuccess: () => toast.success('审批包已缓存'),
                onError: (error) => toast.error(errorMessage(error)),
              })
              event.currentTarget.value = ''
            }}
          />
          <Button type="button" size="sm" variant="outline" title="上传审批包" aria-label="上传审批包"
            onClick={() => fileInput.current?.click()} disabled={busy || assets.isError}>
            {upload.isPending ? <LoaderCircle className="size-4 animate-spin" /> : <Upload className="size-4" />}
          </Button>
          <Button type="button" size="sm" variant="outline" title="刷新运行时状态" aria-label="刷新运行时状态"
            onClick={() => { void runtime.refetch(); void assets.refetch() }} disabled={runtime.isFetching}>
            <RotateCw className={runtime.isFetching ? 'size-4 animate-spin' : 'size-4'} />
          </Button>
          <Button type="button" size="sm" onClick={() => install.mutate(undefined, {
            onSuccess: () => toast.success('受管资产已安装'),
            onError: (error) => toast.error(errorMessage(error)),
          })} disabled={busy || !online || !approved?.cached}>
            {install.isPending ? <LoaderCircle className="mr-1 size-4 animate-spin" /> : <Download className="mr-1 size-4" />}
            {t('logsRuntime.asset.dispatch')}
          </Button>
          {/* 问号放在按钮外侧：放进按钮会并入其可访问名（读屏与测试都读不到「下发资产」）。 */}
          <span title={t('logsRuntime.asset.dispatchHelp')} className="inline-flex items-center">
            <CircleHelp className="size-3.5 text-muted-foreground" aria-label={t('logsRuntime.asset.dispatchHelp')} />
          </span>
        </div>
      </div>

      {runtime.isError && <p role="alert" className="mb-2 text-xs text-destructive">{errorMessage(runtime.error)}</p>}
      {assets.isError && <p role="alert" className="mb-2 text-xs text-destructive">{errorMessage(assets.error)}</p>}
      {runtime.data?.error && (
        <p role="alert" className="mb-2 text-xs text-destructive">
          {localizedLogError(runtime.data.error, t)}
        </p>
      )}
      <div className="divide-y border-y">
        {(['hot', 'cold', 'rehydrate'] as const).map((namespace) => {
          const instance = runtime.data?.instances?.find((item) => item.namespace === namespace)
          const running = instance?.state === 'RUNNING'
          return (
            <div key={namespace} className="flex min-h-14 flex-wrap items-center justify-between gap-2 py-2 text-sm">
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="inline-flex items-center gap-1">
                    <span className="font-medium">{namespaceNames[namespace]}</span>
                    <span title={t('logsRuntime.namespace.help')}>
                      <CircleHelp className="size-3.5 text-muted-foreground" aria-label={t('logsRuntime.namespace.help')} />
                    </span>
                  </span>
                  <Badge variant="outline">{instance?.state ?? (online ? t('logsRuntime.namespace.notReady') : t('logsRuntime.namespace.offline'))}</Badge>
                  {instance?.health_ok && <span className="text-xs text-status-success">进程健康</span>}
                  {instance?.query_ready && <span className="text-xs text-status-success">可查询</span>}
                </div>
                <div className="mt-0.5 break-all text-xs text-muted-foreground">
                  {instance?.listen_addr ?? '—'} · {instance?.asset_tag ?? '—'}
                  {instance?.last_error && <span className="ml-2 text-destructive">{instance.last_error}</span>}
                </div>
              </div>
              <div className="flex shrink-0 items-center gap-1">
                <Button type="button" size="sm" variant="outline" title={`启动 ${namespaceNames[namespace]}`} aria-label={`启动 ${namespaceNames[namespace]}`}
                  disabled={busy || !online || !instance || running} onClick={() => act(namespace, 'start')}><Play className="size-4" /></Button>
                <Button type="button" size="sm" variant="outline" title={`停止 ${namespaceNames[namespace]}`} aria-label={`停止 ${namespaceNames[namespace]}`}
                  disabled={busy || !online || !running} onClick={() => act(namespace, 'stop')}><Square className="size-4" /></Button>
                <Button type="button" size="sm" variant="outline" title={`重启 ${namespaceNames[namespace]}`} aria-label={`重启 ${namespaceNames[namespace]}`}
                  disabled={busy || !online || !running} onClick={() => act(namespace, 'restart')}><RotateCw className="size-4" /></Button>
              </div>
            </div>
          )
        })}
      </div>
	  <div className="mt-3 flex flex-wrap items-center justify-end gap-2">
		<span className="inline-flex items-center gap-1">
		  <input type="date" aria-label={t('logsRuntime.date.label')} title={t('logsRuntime.date.help')} value={migrationDay} onChange={(event) => setMigrationDay(event.target.value)}
			className="h-8 rounded-md border bg-background px-2 text-sm" />
		  <span title={t('logsRuntime.date.help')}><CircleHelp className="size-3.5 text-muted-foreground" aria-label={t('logsRuntime.date.help')} /></span>
		</span>
		<Button type="button" size="sm" variant="outline" disabled={busy || !online || !migrationDay || !isPlatformAdmin}
      title={!isPlatformAdmin ? t('logsRuntime.migration.denied') : undefined}
      onClick={() => {
        setMigrationError('')
        setMigrationTarget({ nodeId, storageNamespace: `node:${nodeId}`, utcDay: migrationDay })
      }}>
		  {migrate.isPending ? <LoaderCircle className="mr-1 size-4 animate-spin" /> : <Archive className="mr-1 size-4" />}
		  {t('logsRuntime.namespace.migrate')}
		</Button>
      {!isPlatformAdmin && <span className="text-xs text-muted-foreground">{t('logsRuntime.migration.denied')}</span>}
	  </div>
      {/* 本端点统一用有效平台身份开窗并守卫提交，不改变全局危险操作的数字角色契约。 */}
      <DangerConfirm
        open={migrationTarget !== null && isPlatformAdmin}
        title={t('logsRuntime.migration.title')}
        description={migrationTarget ? `${t('logsRuntime.migration.description', {
          node: migrationTarget.nodeId, namespace: migrationTarget.storageNamespace, day: migrationTarget.utcDay,
        })}${migrationError ? `\n${migrationError}` : ''}` : ''}
        confirmLabel={t('logsRuntime.migration.confirm')}
        pending={migrate.isPending}
        onConfirm={confirmMigration}
        onCancel={() => { if (!migrationInFlight.current) setMigrationTarget(null) }}
      />
    </section>
  )
}
