import { useTranslation } from 'react-i18next'
import { Loader2, CheckCircle2, XCircle, Wifi } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { usePingNode } from '@/api/diagnostics'

/**
 * 节点存活测试按钮（FR-229）：经 gRPC 调用 Worker GetVersion 主动探活，
 * 行内展示在线 + 版本 + 往返耗时，或离线 + 原因。供 JDK 一键下载前「先测再下」避免卡死。
 */
export function PingNodeButton({ nodeId }: { nodeId: number }) {
  const { t } = useTranslation()
  const ping = usePingNode(nodeId)
  const res = ping.data
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Button size="sm" variant="outline" onClick={() => ping.mutate()} disabled={ping.isPending}>
        {ping.isPending ? <Loader2 className="size-4 animate-spin" /> : <Wifi className="size-4" />}
        {t('diagnostics.testNode', '测试节点存活')}
      </Button>
      {!ping.isPending && res && (
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
      {!ping.isPending && ping.isError && (
        <span className="text-xs text-destructive">{t('diagnostics.testFailed', '测试失败')}</span>
      )}
    </div>
  )
}
