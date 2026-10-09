import { useTranslation } from 'react-i18next'
import { Loader2, CheckCircle2, XCircle, Wifi } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'

/** 节点存活探测结果（本组件所需的最小结构；外壳传 API 返回项会结构兼容）。 */
export interface PingResultView {
  alive: boolean
  version?: string
  latencyMs?: number
  error?: string
}

/**
 * 节点存活测试按钮（FR-229）：经 gRPC 调用 Worker GetVersion 主动探活，
 * 行内展示在线 + 版本 + 往返耗时，或离线 + 原因。供 JDK 一键下载前「先测再下」避免卡死。
 *
 * 受控视图（ADR-097）：不取数、不发请求——探测结果与在途态经 props 注入，
 * 点击经 `onPing` 上报。注意区分两种失败：`result.alive === false` 是「探测成功但节点离线」，
 * `isError` 是「探测请求本身失败」，两者文案不同，不可合并。
 */
export interface PingNodeButtonProps {
  /** 最近一次探测结果；未探测过为 undefined。 */
  result?: PingResultView
  /** 探测在途。 */
  pending?: boolean
  /** 探测请求本身失败。 */
  isError?: boolean
  /** 触发探测。 */
  onPing: () => void
}

export function PingNodeButton({ result, pending = false, isError = false, onPing }: PingNodeButtonProps) {
  const { t } = useTranslation()
  const res = result
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Button size="sm" variant="outline" onClick={onPing} disabled={pending}>
        {pending ? <Loader2 className="size-4 animate-spin" /> : <Wifi className="size-4" />}
        {t('diagnostics.testNode', '测试节点存活')}
      </Button>
      {!pending && res && (
        res.alive ? (
          <span className="flex items-center gap-1 text-xs text-status-success">
            <CheckCircle2 className="size-3.5" />
            {t('diagnostics.nodeAlive', '在线')}{res.version ? ` · v${res.version}` : ''} · {res.latencyMs}ms
          </span>
        ) : (
          <span className="flex min-w-0 items-center gap-1 text-xs text-destructive" title={res.error}>
            <XCircle className="size-3.5 shrink-0" />
            <span className="truncate">{t('diagnostics.nodeOffline', '离线')}{res.error ? `: ${res.error}` : ''}</span>
          </span>
        )
      )}
      {!pending && isError && (
        <span className="text-xs text-destructive">{t('diagnostics.testFailed', '测试失败')}</span>
      )}
    </div>
  )
}
