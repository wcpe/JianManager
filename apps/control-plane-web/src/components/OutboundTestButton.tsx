import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Loader2, CheckCircle2, XCircle, Plug } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { useTestHTTP } from '@/api/diagnostics'

/**
 * 出站连通性测试按钮（FR-229，可自定义目标 FR-280）：经 CP 出站客户端（含已配置代理）GET 目标 URL，
 * 行内展示可达 + 状态码 + 往返耗时，或失败原因。供代理设置 / JDK 下载源测试复用。
 *
 * `editable` 为真时（FR-280）额外渲染 URL 输入框，运维可测任意地址（默认 `defaultUrl`，代理测试默认
 * `https://www.google.com`——不再写死 GitHub）；为假时保持固定 `defaultUrl` 的一键测试。
 */
export function OutboundTestButton({
  defaultUrl,
  label,
  editable = false,
}: {
  defaultUrl: string
  label: string
  editable?: boolean
}) {
  const { t } = useTranslation()
  const test = useTestHTTP()
  const res = test.data
  const [url, setUrl] = useState(defaultUrl)
  const target = editable ? url.trim() : defaultUrl
  const canTest = target.length > 0 && !test.isPending
  return (
    <div className="space-y-1.5">
      <div className="flex flex-wrap items-center gap-2">
        {editable && (
          <Input
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="https://www.google.com"
            className="h-8 w-full max-w-xs text-xs"
            aria-label={t('diagnostics.testUrlLabel', '测试目标地址')}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && canTest) test.mutate(target)
            }}
          />
        )}
        <Button size="sm" variant="outline" onClick={() => test.mutate(target)} disabled={!canTest}>
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
    </div>
  )
}
