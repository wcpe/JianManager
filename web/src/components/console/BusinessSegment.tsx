import { useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { Boxes, Loader2, Play, RefreshCw, ShieldAlert } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Panel } from '@/components/ui/panel'
import { cn } from '@/lib/utils'
import DangerConfirm from '@/components/DangerConfirm'
import {
  dispatchBusiness,
  fetchBusinessManifest,
  type BusinessAction,
  type BusinessResult,
} from '@/api/business'
import { isWriteAction } from './business-actions'

/**
 * 业务掌控台（JBIS，FR-119，见 ADR-026/027）。
 *
 * manifest 驱动：取探针汇总的业务能力清单动态渲染域 / 动作（不硬编码具体插件），按动作 args 渲染表单，
 * 经 `POST /business` 下发 `domain.action` + payload，透传展示结果。探针未连 / 无业务 Provider 优雅降级。
 * 视觉为靛蓝圆角范式（FR-163）：统一 {@link Panel} 原语、语义图标块、写动作走语义危险色。
 */
interface BusinessSegmentProps {
  /** 实例 ID。 */
  instanceId: number
}

export default function BusinessSegment({ instanceId }: BusinessSegmentProps) {
  const { t } = useTranslation()
  const manifestQuery = useQuery({
    queryKey: ['business-manifest', instanceId],
    queryFn: () => fetchBusinessManifest(instanceId),
    enabled: !!instanceId,
  })
  const [selected, setSelected] = useState<{ domain: string; action: BusinessAction } | null>(null)
  const [args, setArgs] = useState<Record<string, string>>({})
  const [reason, setReason] = useState('')
  const [result, setResult] = useState<BusinessResult | null>(null)
  const [dispatching, setDispatching] = useState(false)
  const [dispatchError, setDispatchError] = useState('')
  // 写动作二次确认弹窗（FR-121）；非 null 时展示，确认后才真正下发。
  const [confirmOpen, setConfirmOpen] = useState(false)

  const manifest = manifestQuery.data
  const domains = manifest?.available ? (manifest.output?.domains ?? {}) : {}
  const domainEntries = Object.entries(domains)

  const pick = useCallback((domain: string, action: BusinessAction) => {
    setSelected({ domain, action })
    setArgs(Object.fromEntries((action.args ?? []).map((a) => [a, ''])))
    setReason('')
    setResult(null)
    setDispatchError('')
  }, [])

  // 真正执行下发：写动作注入幂等键（稳定 operationId）+ 操作原因（FR-121）。
  const doDispatch = useCallback(async () => {
    if (!selected) return
    setDispatching(true)
    setDispatchError('')
    const write = isWriteAction(selected.action)
    try {
      const res = await dispatchBusiness(
        instanceId,
        selected.domain,
        selected.action.action,
        JSON.stringify(args),
        write
          ? { write: true, operationId: crypto.randomUUID(), reason: reason.trim() || undefined }
          : undefined,
      )
      setResult(res)
    } catch (err: unknown) {
      const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      setDispatchError(msg || t('business.dispatchFailed'))
    } finally {
      setDispatching(false)
    }
  }, [selected, args, reason, instanceId, t])

  // 「下发」入口：写动作先弹二次确认，读动作直接下发（FR-121）。
  const run = useCallback(() => {
    if (!selected) return
    if (isWriteAction(selected.action)) {
      setConfirmOpen(true)
      return
    }
    void doDispatch()
  }, [selected, doDispatch])

  return (
    <div className="flex h-full min-w-0 flex-col gap-3 p-4">
      {/* 头部：图标块 + 标题 + 刷新能力清单 */}
      <div className="flex items-center gap-2.5">
        <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-accent text-primary">
          <Boxes className="size-4" />
        </span>
        <div className="min-w-0">
          <h3 className="text-sm font-semibold">{t('business.title')}</h3>
          <p className="truncate text-xs text-muted-foreground">{t('business.subtitle')}</p>
        </div>
        <Button
          size="sm"
          variant="outline"
          className="ml-auto h-7 rounded-full px-3 text-xs"
          onClick={() => void manifestQuery.refetch()}
          disabled={manifestQuery.isFetching}
        >
          <RefreshCw className={cn('mr-1 size-3.5', manifestQuery.isFetching && 'animate-spin')} />
          {t('business.refresh')}
        </Button>
      </div>

      {manifestQuery.isLoading && (
        <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <Loader2 className="size-3.5 animate-spin" />
          {t('common.loading')}
        </div>
      )}

      {/* 能力不可用：探针未连 / 无业务 Provider */}
      {!manifestQuery.isLoading && (!manifest?.available || domainEntries.length === 0) && (
        <div className="rounded-xl border bg-muted/30 p-4 text-xs text-muted-foreground shadow-soft">
          {manifest?.error || t('business.unavailable')}
        </div>
      )}

      {/* manifest 驱动的能力清单 + 下发面板 */}
      {!manifestQuery.isLoading && manifest?.available && domainEntries.length > 0 && (
        <div className="grid min-h-0 flex-1 grid-cols-1 gap-3 md:grid-cols-[minmax(13rem,17rem)_1fr]">
          {/* 域 / 动作清单 */}
          <Panel title={t('business.capabilities')} icon={<Boxes className="size-3.5" />} bodyClassName="min-h-0 overflow-auto p-2">
            <div className="flex flex-col gap-3">
              {domainEntries.map(([domain, def]) => (
                <div key={domain}>
                  <div className="mb-1 px-1 text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">
                    {domain}
                  </div>
                  <ul className="flex flex-col gap-0.5">
                    {(def.actions ?? []).map((action) => {
                      const active = selected?.domain === domain && selected.action.action === action.action
                      return (
                        <li key={action.action}>
                          <button
                            className={cn(
                              'flex w-full items-center gap-1.5 rounded-lg px-2.5 py-1.5 text-left text-xs transition-colors duration-200',
                              active ? 'bg-accent font-medium text-primary' : 'hover:bg-accent/60',
                            )}
                            onClick={() => pick(domain, action)}
                          >
                            <span className="truncate font-mono">{action.action}</span>
                            {action.readOnly ? (
                              <span className="ml-auto shrink-0 rounded-full bg-muted px-1.5 py-px text-[10px] text-muted-foreground">
                                {t('business.readOnly')}
                              </span>
                            ) : (
                              <ShieldAlert className="ml-auto size-3 shrink-0 text-status-danger" aria-hidden />
                            )}
                          </button>
                        </li>
                      )
                    })}
                  </ul>
                </div>
              ))}
            </div>
          </Panel>

          {/* 下发面板 */}
          <Panel
            title={selected ? `${selected.domain}.${selected.action.action}` : t('business.dispatchPanel')}
            icon={<Play className="size-3.5" />}
            tone={selected && isWriteAction(selected.action) ? 'danger' : 'primary'}
            bodyClassName="min-h-0 overflow-auto p-4"
          >
            {!selected ? (
              <div className="text-xs text-muted-foreground">{t('business.pickAction')}</div>
            ) : (
              <div className="flex flex-col gap-2.5">
                {isWriteAction(selected.action) && (
                  <span className="inline-flex w-fit items-center gap-1 rounded-full bg-status-danger/12 px-2 py-0.5 text-[10px] font-medium text-status-danger">
                    <ShieldAlert className="size-3" aria-hidden />
                    {t('business.writeAction')}
                  </span>
                )}
                {(selected.action.args ?? []).map((arg) => (
                  <label key={arg} className="flex flex-col gap-0.5 text-xs">
                    <span className="text-muted-foreground">{arg}</span>
                    <Input
                      value={args[arg] ?? ''}
                      onChange={(e) => setArgs((prev) => ({ ...prev, [arg]: e.target.value }))}
                      className="h-8 text-xs"
                    />
                  </label>
                ))}
                {/* 写动作可选「原因」，透传进插件流水 + JM 审计（FR-121）。 */}
                {isWriteAction(selected.action) && (
                  <label className="flex flex-col gap-0.5 text-xs">
                    <span className="text-muted-foreground">{t('business.reason')}</span>
                    <Input
                      value={reason}
                      onChange={(e) => setReason(e.target.value)}
                      placeholder={t('business.reasonPlaceholder')}
                      className="h-8 text-xs"
                    />
                  </label>
                )}
                <Button
                  size="sm"
                  variant={isWriteAction(selected.action) ? 'destructive' : 'default'}
                  className="h-8 self-start rounded-full px-4 text-xs"
                  onClick={run}
                  disabled={dispatching}
                >
                  {dispatching ? (
                    <Loader2 className="mr-1 size-3.5 animate-spin" />
                  ) : (
                    <Play className="mr-1 size-3.5" />
                  )}
                  {t('business.dispatch')}
                </Button>

                {dispatchError && <div className="text-xs text-status-danger">{dispatchError}</div>}
                {result && (
                  <div className="mt-1 flex flex-col gap-1">
                    {!result.available && (
                      <div className="text-xs text-status-danger">
                        {result.error || t('business.dispatchFailed')}
                      </div>
                    )}
                    {result.available && (
                      <pre className="overflow-auto rounded-lg bg-muted/50 p-2.5 text-[11px]">
                        {JSON.stringify(result.output, null, 2)}
                      </pre>
                    )}
                  </div>
                )}
              </div>
            )}
          </Panel>
        </div>
      )}

      {/* 写动作二次确认（FR-121）：高危业务写须确认后下发，避免误操作刷钱/吞物品。 */}
      <DangerConfirm
        open={confirmOpen}
        title={t('business.confirmWriteTitle')}
        description={t('business.confirmWriteDesc', {
          domain: selected?.domain ?? '',
          action: selected?.action.action ?? '',
        })}
        confirmLabel={t('business.confirmWrite')}
        scope="group"
        onConfirm={() => {
          setConfirmOpen(false)
          void doDispatch()
        }}
        onCancel={() => setConfirmOpen(false)}
      />
    </div>
  )
}
