import { useRef, useState } from 'react'
import { toast } from 'sonner'
import { Archive, CheckCheck, Download, LoaderCircle, Play, RotateCw, Square, Upload } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Badge } from '@jianmanager/ui/components/badge'
import {
  useApprovedLogAssets,
  useControlLogRuntime,
  useInstallLogAsset,
  useLogRuntime,
  useMigrateLogPartition,
  useResolveLogIngestGaps,
  useUploadLogAsset,
  type LogRuntimeInstance,
} from '@/api/logRuntime'

function errorMessage(error: unknown): string {
  const response = (error as { response?: { data?: { message?: string; error?: string } } })?.response
  return response?.data?.message || response?.data?.error || '操作失败'
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
  const fileInput = useRef<HTMLInputElement>(null)
  const runtime = useLogRuntime(nodeId, true)
  const assets = useApprovedLogAssets(true)
  const upload = useUploadLogAsset()
  const install = useInstallLogAsset(nodeId)
  const control = useControlLogRuntime(nodeId)
	const migrate = useMigrateLogPartition(nodeId)
	const resolveGaps = useResolveLogIngestGaps(nodeId)
	const [migrationDay, setMigrationDay] = useState('')
  const normalizedArch = arch.toLowerCase() === 'x64' ? 'amd64' : arch.toLowerCase()
  const approved = assets.data?.find((asset) => asset.os === os.toLowerCase() && asset.arch === normalizedArch)
  const busy = upload.isPending || install.isPending || control.isPending || migrate.isPending || resolveGaps.isPending

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
            下发资产
          </Button>
        </div>
      </div>

      {runtime.isError && <p role="alert" className="mb-2 text-xs text-destructive">{errorMessage(runtime.error)}</p>}
      {assets.isError && <p role="alert" className="mb-2 text-xs text-destructive">{errorMessage(assets.error)}</p>}
      {runtime.data?.error && <p role="alert" className="mb-2 text-xs text-destructive">{runtime.data.error.message}</p>}
      <div className="divide-y border-y">
        {(['hot', 'cold', 'rehydrate'] as const).map((namespace) => {
          const instance = runtime.data?.instances?.find((item) => item.namespace === namespace)
          const running = instance?.state === 'RUNNING'
          return (
            <div key={namespace} className="flex min-h-14 flex-wrap items-center justify-between gap-2 py-2 text-sm">
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="font-medium">{namespaceNames[namespace]}</span>
                  <Badge variant="outline">{instance?.state ?? (online ? '未就绪' : '离线')}</Badge>
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
		<Button type="button" size="sm" variant="outline" disabled={busy || !online}
		  onClick={() => resolveGaps.mutate(undefined, { onSuccess: () => toast.success('已核销 projection 覆盖的缺口'), onError: (error) => toast.error(errorMessage(error)) })}>
		  {resolveGaps.isPending ? <LoaderCircle className="mr-1 size-4 animate-spin" /> : <CheckCheck className="mr-1 size-4" />}
		  核销已覆盖缺口
		</Button>
		<input type="date" aria-label="迁移 UTC 日期" value={migrationDay} onChange={(event) => setMigrationDay(event.target.value)}
		  className="h-8 rounded-md border bg-background px-2 text-sm" />
		<Button type="button" size="sm" variant="outline" disabled={busy || !online || !migrationDay}
		  onClick={() => migrate.mutate({ storageNamespace: `node:${nodeId}`, utcDay: migrationDay }, {
			onSuccess: () => toast.success('分区已迁移到 COLD'),
			onError: (error) => toast.error(errorMessage(error)),
		  })}>
		  {migrate.isPending ? <LoaderCircle className="mr-1 size-4 animate-spin" /> : <Archive className="mr-1 size-4" />}
		  迁移到 COLD
		</Button>
	  </div>
    </section>
  )
}
