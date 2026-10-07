import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { CheckCircle2, Loader2, Plug, XCircle } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'

/** 出站测试结果（容器注入）。 */
export interface OutboundTestResult {
  ok: boolean
  status?: number
  latencyMs?: number
  error?: string
}

export interface OutboundTestButtonProps {
  /** 固定测试目标（`editable` 为假时使用）。 */
  defaultUrl: string
  label: string
  /** 为真时额外渲染 URL 输入框，可测任意地址（FR-280）。 */
  editable?: boolean
  /** 发起测试（容器注入 mutation）。 */
  onTest: (url: string) => void
  /** 最近一次结果（容器持有）。 */
  result?: OutboundTestResult
  /** 测试进行中。 */
  pending?: boolean
  /** 测试请求本身失败（非「不通」）。 */
  isError?: boolean
}

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
  onTest,
  result,
  pending = false,
  isError = false,
}: OutboundTestButtonProps) {
  const { t } = useTranslation()
  const [url, setUrl] = useState(defaultUrl)
  const target = editable ? url.trim() : defaultUrl
  const canTest = target.length > 0 && !pending

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
              if (e.key === 'Enter' && canTest) onTest(target)
            }}
          />
        )}
        <Button size="sm" variant="outline" onClick={() => onTest(target)} disabled={!canTest}>
          {pending ? <Loader2 className="size-4 animate-spin" /> : <Plug className="size-4" />}
          {label}
        </Button>
        {!pending && result && (
          result.ok ? (
            <span className="flex items-center gap-1 text-xs text-status-success">
              <CheckCircle2 className="size-3.5" />
              {t('diagnostics.reachable', '可达')} · {result.status} · {result.latencyMs}ms
            </span>
          ) : (
            <span className="flex min-w-0 items-center gap-1 text-xs text-destructive" title={result.error}>
              <XCircle className="size-3.5 shrink-0" />
              <span className="truncate">
                {t('diagnostics.unreachable', '不通')}
                {result.error ? `: ${result.error}` : ''}
              </span>
            </span>
          )
        )}
        {!pending && isError && (
          <span className="text-xs text-destructive">{t('diagnostics.testFailed', '测试失败')}</span>
        )}
      </div>
    </div>
  )
}
