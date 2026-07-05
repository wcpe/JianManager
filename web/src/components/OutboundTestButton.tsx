import { useTranslation } from 'react-i18next'
import { Loader2, CheckCircle2, XCircle, Plug } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { useTestHTTP } from '@/api/diagnostics'

/**
 * 出站连通性测试按钮（FR-229）：经 CP 出站代理 GET 指定 URL，
 * 行内展示可达 + 状态码 + 往返耗时，或失败原因。供代理设置 / JDK 下载源测试复用。
 */
export function OutboundTestButton({ url, label }: { url: string; label: string }) {
  const { t } = useTranslation()
  const test = useTestHTTP()
  const res = test.data
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Button size="sm" variant="outline" onClick={() => test.mutate(url)} disabled={test.isPending}>
        {test.isPending ? <Loader2 className="size-4 animate-spin" /> : <Plug className="size-4" />}
        {label}
      </Button>
      {!test.isPending && res && (
        res.ok ? (
          <span className="flex items-center gap-1 text-xs text-status-success">
            <CheckCircle2 className="size-3.5" />
            {t('diagnostics.reachable', '可达')} · {res.status} · {res.latencyMs}ms
          </span>
        ) : (
          <span className="flex min-w-0 items-center gap-1 text-xs text-destructive" title={res.error}>
            <XCircle className="size-3.5 shrink-0" />
            <span className="truncate">{t('diagnostics.unreachable', '不通')}{res.error ? `: ${res.error}` : ''}</span>
          </span>
        )
      )}
      {!test.isPending && test.isError && (
        <span className="text-xs text-destructive">{t('diagnostics.testFailed', '测试失败')}</span>
      )}
    </div>
  )
}
