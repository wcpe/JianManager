import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { CircleHelp } from 'lucide-react'
import { Archive, CheckCheck, Download, LoaderCircle, Play, RotateCw, Square, Upload } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Badge } from '@jianmanager/ui/components/badge'

/** 日志运行时的三个分区命名空间（与 proto 对齐）。 */
export type LogNamespace = 'hot' | 'cold' | 'rehydrate'

/** 单分区运行时实例（本组件所需的最小结构；外壳传 API 返回项会结构兼容）。 */
export interface LogRuntimeInstanceView {
  namespace: LogNamespace
  state?: string
  health_ok?: boolean
  query_ready?: boolean
  listen_addr?: string
  asset_tag?: string
  last_error?: string
}

/** 运行时视图（同上）。 */
export interface LogRuntimeView {
  instances?: LogRuntimeInstanceView[]
  /** 结构化错误：按 code 本地化，未知码回退 message。 */
  error?: { code?: number; message?: string } | null
}

/** 各动作的在途标记：分别控制对应按钮的图标转圈（统一禁用看 `busy`）。 */
export interface LogRuntimePending {
  upload?: boolean
  install?: boolean
  control?: boolean
  migrate?: boolean
  gaps?: boolean
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
  }
  return known[err.code ?? -1] ?? err.message ?? null
}

/** 分区显示名。导出供外壳拼装「HOT 已启动」这类成功提示时复用，避免两处各写一份。 */
export const namespaceNames: Record<LogNamespace, string> = {
  hot: 'HOT',
  cold: 'COLD',
  rehydrate: 'Rehydrate',
}

/**
 * VictoriaLogs 运行时面板（FR-473~484）：三分区起停、审批包上传/下发、缺口核销与分区迁移。
 *
 * 受控视图（ADR-097 b 范式）：不取数、不发请求、不弹 toast——七个动作全部以回调上报，
 * 数据与错误文案经 props 注入。两处需要动作结果的地方仍由视图消费：
 * - 起停按钮的禁用取决于「实例是否存在/是否运行」，那来自注入的数据而非动作结果；
 * - 迁移日期与分区名由外壳拼装请求体，视图只管把用户选的日期交上去。
 */
export interface NodeLogRuntimePanelProps {
  /** 节点系统与架构（用于展示与审批包匹配；外壳已归一化架构别名）。 */
  os: string
  arch: string
  /** 节点是否在线：离线时禁用全部写操作。 */
  online: boolean
  /** 运行时状态；外壳取数后注入。 */
  runtime?: LogRuntimeView
  /** 该平台的审批包是否已缓存（外壳从审批资产列表匹配后注入）。 */
  approvedCached?: boolean
  /** 运行时查询失败消息；外壳注入（有值即视为查询出错）。 */
  runtimeError?: string
  /** 审批资产查询失败消息；外壳注入（有值即禁用上传）。 */
  assetsError?: string
  /** 任一写操作在途：统一禁用。 */
  busy?: boolean
  /** 各动作在途标记。 */
  pending?: LogRuntimePending
  /** 刷新在途（运行时状态）。 */
  fetching?: boolean
  /** 刷新运行时状态与审批资产。 */
  onRefresh: () => void
  /** 上传官方压缩包作为审批包。返回是否成功。 */
  onUpload: (file: File) => Promise<boolean>
  /** 下发受管资产到节点。返回是否成功。 */
  onInstall: () => Promise<boolean>
  /** 起停/重启指定分区。返回是否成功。 */
  onControl: (namespace: LogNamespace, action: 'start' | 'stop' | 'restart') => Promise<boolean>
  /** 把指定 UTC 日的分区迁移到 COLD。返回是否成功。 */
  onMigrate: (utcDay: string) => Promise<boolean>
  /** 核销已被 projection 覆盖的缺口。返回是否成功。 */
  onResolveGaps: () => Promise<boolean>
}

export default function NodeLogRuntimePanel({
  os,
  arch,
  online,
  runtime,
  approvedCached,
  runtimeError,
  assetsError,
  busy = false,
  pending,
  fetching = false,
  onRefresh,
  onUpload,
  onInstall,
  onControl,
  onMigrate,
  onResolveGaps,
}: NodeLogRuntimePanelProps) {
  const { t } = useTranslation()
  const fileInput = useRef<HTMLInputElement>(null)
  const [migrationDay, setMigrationDay] = useState('')
  const normalizedArch = arch.toLowerCase() === 'x64' ? 'amd64' : arch.toLowerCase()

  return (
    <section aria-label="VictoriaLogs 运行时" className="border-t pt-4">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <div className="min-w-0">
          <h3 className="text-sm font-semibold">VictoriaLogs</h3>
          <p className="text-xs text-muted-foreground">
            v1.52.0 · {os} / {normalizedArch} · {approvedCached ? '审批包已缓存' : '审批包未缓存'}
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
              // 先清空 input.value，使用户重选同一文件也能触发 change。
              event.currentTarget.value = ''
              if (!file) return
              void onUpload(file)
            }}
          />
          <Button type="button" size="sm" variant="outline" title="上传审批包" aria-label="上传审批包"
            onClick={() => fileInput.current?.click()} disabled={busy || !!assetsError}>
            {pending?.upload ? <LoaderCircle className="size-4 animate-spin" /> : <Upload className="size-4" />}
          </Button>
          <Button type="button" size="sm" variant="outline" title="刷新运行时状态" aria-label="刷新运行时状态"
            onClick={onRefresh} disabled={fetching}>
            <RotateCw className={fetching ? 'size-4 animate-spin' : 'size-4'} />
          </Button>
          <Button type="button" size="sm" onClick={() => void onInstall()} disabled={busy || !online || !approvedCached}>
            {pending?.install ? <LoaderCircle className="mr-1 size-4 animate-spin" /> : <Download className="mr-1 size-4" />}
            {t('logsRuntime.asset.dispatch')}
          </Button>
          {/* 问号放在按钮外侧：放进按钮会并入其可访问名（读屏与测试都读不到「下发资产」）。 */}
          <span title={t('logsRuntime.asset.dispatchHelp')} className="inline-flex items-center">
            <CircleHelp className="size-3.5 text-muted-foreground" aria-label={t('logsRuntime.asset.dispatchHelp')} />
          </span>
        </div>
      </div>

      {runtimeError && <p role="alert" className="mb-2 text-xs text-destructive">{runtimeError}</p>}
      {assetsError && <p role="alert" className="mb-2 text-xs text-destructive">{assetsError}</p>}
      {runtime?.error && (
        <p role="alert" className="mb-2 text-xs text-destructive">
          {localizedLogError(runtime.error, t)}
        </p>
      )}
      <div className="divide-y border-y">
        {(['hot', 'cold', 'rehydrate'] as const).map((namespace) => {
          const instance = runtime?.instances?.find((item) => item.namespace === namespace)
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
                  disabled={busy || !online || !instance || running} onClick={() => void onControl(namespace, 'start')}><Play className="size-4" /></Button>
                <Button type="button" size="sm" variant="outline" title={`停止 ${namespaceNames[namespace]}`} aria-label={`停止 ${namespaceNames[namespace]}`}
                  disabled={busy || !online || !running} onClick={() => void onControl(namespace, 'stop')}><Square className="size-4" /></Button>
                <Button type="button" size="sm" variant="outline" title={`重启 ${namespaceNames[namespace]}`} aria-label={`重启 ${namespaceNames[namespace]}`}
                  disabled={busy || !online || !running} onClick={() => void onControl(namespace, 'restart')}><RotateCw className="size-4" /></Button>
              </div>
            </div>
          )
        })}
      </div>
      <div className="mt-3 flex flex-wrap items-center justify-end gap-2">
        <Button type="button" size="sm" variant="outline" disabled={busy || !online}
          onClick={() => void onResolveGaps()}>
          {pending?.gaps ? <LoaderCircle className="mr-1 size-4 animate-spin" /> : <CheckCheck className="mr-1 size-4" />}
          {t('logsRuntime.gaps.resolve')}
        </Button>
        {/* 问号放在按钮外侧：放进按钮会并入其可访问名（读屏与测试都读不到「核销已覆盖缺口」）。 */}
        <span title={t('logsRuntime.gaps.help')} className="inline-flex items-center">
          <CircleHelp className="size-3.5 text-muted-foreground" aria-label={t('logsRuntime.gaps.help')} />
        </span>
        <span className="inline-flex items-center gap-1">
          <input type="date" aria-label={t('logsRuntime.date.label')} title={t('logsRuntime.date.help')} value={migrationDay} onChange={(event) => setMigrationDay(event.target.value)}
            className="h-8 rounded-md border bg-background px-2 text-sm" />
          <span title={t('logsRuntime.date.help')}><CircleHelp className="size-3.5 text-muted-foreground" aria-label={t('logsRuntime.date.help')} /></span>
        </span>
        <Button type="button" size="sm" variant="outline" disabled={busy || !online || !migrationDay}
          onClick={() => void onMigrate(migrationDay)}>
          {pending?.migrate ? <LoaderCircle className="mr-1 size-4 animate-spin" /> : <Archive className="mr-1 size-4" />}
          {t('logsRuntime.namespace.migrate')}
        </Button>
      </div>
    </section>
  )
}
