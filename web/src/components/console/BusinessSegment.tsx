import { useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { Loader2, Play, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  dispatchBusiness,
  fetchBusinessManifest,
  type BusinessAction,
  type BusinessResult,
} from '@/api/business'

/**
 * 业务掌控台（JBIS，FR-119，见 ADR-026/027）。
 *
 * manifest 驱动：取探针汇总的业务能力清单动态渲染域 / 动作（不硬编码具体插件），按动作 args 渲染表单，
 * 经 `POST /business` 下发 `domain.action` + payload，透传展示结果。探针未连 / 无业务 Provider 优雅降级。
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
  const [result, setResult] = useState<BusinessResult | null>(null)
  const [dispatching, setDispatching] = useState(false)
  const [dispatchError, setDispatchError] = useState('')

  const manifest = manifestQuery.data
  const domains = manifest?.available ? (manifest.output?.domains ?? {}) : {}
  const domainEntries = Object.entries(domains)

  const pick = useCallback((domain: string, action: BusinessAction) => {
    setSelected({ domain, action })
    setArgs(Object.fromEntries((action.args ?? []).map((a) => [a, ''])))
    setResult(null)
    setDispatchError('')
  }, [])

  const run = useCallback(async () => {
    if (!selected) return
    setDispatching(true)
    setDispatchError('')
    try {
      const res = await dispatchBusiness(
        instanceId,
        selected.domain,
        selected.action.action,
        JSON.stringify(args),
      )
      setResult(res)
    } catch (err: unknown) {
      const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      setDispatchError(msg || t('business.dispatchFailed'))
    } finally {
      setDispatching(false)
    }
  }, [selected, args, instanceId, t])

  return (
    <div className="flex h-full min-w-0 flex-col gap-3 p-4">
      {/* 头部：标题 + 刷新能力清单 */}
      <div className="flex items-center gap-2">
        <h3 className="text-sm font-semibold">{t('business.title')}</h3>
        <span className="text-xs text-muted-foreground">{t('business.subtitle')}</span>
        <Button
          size="sm"
          variant="outline"
          className="ml-auto h-7 px-2 text-xs"
          onClick={() => void manifestQuery.refetch()}
          disabled={manifestQuery.isFetching}
        >
          <RefreshCw className={`mr-1 size-3.5 ${manifestQuery.isFetching ? 'animate-spin' : ''}`} />
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
        <div className="rounded border bg-muted/30 p-3 text-xs text-muted-foreground">
          {manifest?.error || t('business.unavailable')}
        </div>
      )}

      {/* manifest 驱动的能力清单 + 下发面板 */}
      {!manifestQuery.isLoading && manifest?.available && domainEntries.length > 0 && (
        <div className="grid min-h-0 flex-1 grid-cols-1 gap-3 md:grid-cols-[minmax(12rem,16rem)_1fr]">
          {/* 域 / 动作清单 */}
          <div className="min-h-0 overflow-auto rounded border">
            {domainEntries.map(([domain, def]) => (
              <div key={domain} className="border-b last:border-b-0">
                <div className="bg-muted/40 px-2 py-1 text-xs font-medium">{domain}</div>
                <ul>
                  {(def.actions ?? []).map((action) => {
                    const active = selected?.domain === domain && selected.action.action === action.action
                    return (
                      <li key={action.action}>
                        <button
                          className={`flex w-full items-center gap-1.5 px-2 py-1.5 text-left text-xs hover:bg-accent ${active ? 'bg-accent font-medium' : ''}`}
                          onClick={() => pick(domain, action)}
                        >
                          <span className="font-mono">{action.action}</span>
                          {action.readOnly && (
                            <span className="rounded bg-muted px-1 text-[10px] text-muted-foreground">
                              {t('business.readOnly')}
                            </span>
                          )}
                        </button>
                      </li>
                    )
                  })}
                </ul>
              </div>
            ))}
          </div>

          {/* 下发面板 */}
          <div className="min-h-0 overflow-auto rounded border p-3">
            {!selected ? (
              <div className="text-xs text-muted-foreground">{t('business.pickAction')}</div>
            ) : (
              <div className="flex flex-col gap-2">
                <div className="text-xs font-medium">
                  <span className="text-muted-foreground">{selected.domain}.</span>
                  <span className="font-mono">{selected.action.action}</span>
                </div>
                {(selected.action.args ?? []).map((arg) => (
                  <label key={arg} className="flex flex-col gap-0.5 text-xs">
                    <span className="text-muted-foreground">{arg}</span>
                    <Input
                      value={args[arg] ?? ''}
                      onChange={(e) => setArgs((prev) => ({ ...prev, [arg]: e.target.value }))}
                      className="h-7 text-xs"
                    />
                  </label>
                ))}
                <Button size="sm" className="h-7 self-start px-3 text-xs" onClick={run} disabled={dispatching}>
                  {dispatching ? (
                    <Loader2 className="mr-1 size-3.5 animate-spin" />
                  ) : (
                    <Play className="mr-1 size-3.5" />
                  )}
                  {t('business.dispatch')}
                </Button>

                {dispatchError && <div className="text-xs text-destructive">{dispatchError}</div>}
                {result && (
                  <div className="mt-1 flex flex-col gap-1">
                    {!result.available && (
                      <div className="text-xs text-destructive">
                        {result.error || t('business.dispatchFailed')}
                      </div>
                    )}
                    {result.available && (
                      <pre className="overflow-auto rounded bg-muted/40 p-2 text-[11px]">
                        {JSON.stringify(result.output, null, 2)}
                      </pre>
                    )}
                  </div>
                )}
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
