import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Copy, AlertTriangle, Info } from 'lucide-react'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import { Button } from '@jianmanager/ui/components/button'
import { cn } from '@jianmanager/ui'
import {
  useNodeDiagnosticsLog,
  usePlatformDiagnosticsLog,
  type ProcessLogTail,
} from '@/api/nodeDiagnostics'
import { copyToClipboard } from '@/lib/clipboard'

/**
 * 进程日志诊断抽屉（不是日志检索页）。
 *
 * 设计边界（改动前请先读）：
 * - 只展示「最近 N 行」，**不做**关键字检索 / 时间窗 / 分页 / 多节点聚合；
 * - 拉取即展示，不落任何存储；
 * - 截断时必须显式告知，否则运维会误以为这就是全部。
 *
 * 为什么不做检索：需要按条件查日志请去日志中心（实例日志走 VL 联邦）。
 * 进程自身日志的用途是「出事时人看几百行」，量级和目的都不同。
 */
export interface DiagnosticsLogDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 传节点 ID 拉节点日志；不传则拉 CP 自身日志。 */
  nodeId?: number | null
  /** 抽屉标题（例：“节点 node-01 诊断日志”）。 */
  title: string
}

/** 把字节数格式化成便于人读的形式。 */
function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`
}

/** 等宽日志正文区（保留换行，不做 JSON 美化——这是诊断，不是日志检索）。 */
function LogBody({ data, loading, error }: { data?: ProcessLogTail; loading: boolean; error: unknown }) {
  const { t } = useTranslation()
  if (loading) return <div className="p-4 text-sm text-muted-foreground">{t('common.loading')}</div>
  if (error) return <div className="p-4 text-sm text-status-danger">{t('diagnostics.loadFailed')}</div>
  if (!data) return null
  if (data.note) {
    return (
      <div className="flex items-start gap-2 p-4 text-sm text-muted-foreground">
        <Info className="mt-0.5 size-4 shrink-0" />
        <span>{data.note}</span>
      </div>
    )
  }
  if (!data.content) {
    return <div className="p-4 text-sm text-muted-foreground">{t('diagnostics.empty')}</div>
  }
  return (
    <pre className="min-h-0 flex-1 overflow-auto whitespace-pre-wrap break-all bg-muted/40 p-3 font-mono text-[11px] leading-relaxed">
      {data.content}
    </pre>
  )
}

/** 底部：截断提示 + 可复制的 shell 命令。 */
function LogFooter({ data }: { data?: ProcessLogTail }) {
  const { t } = useTranslation()
  if (!data?.path) return null
  // 只提示当前文件的路径，不拼接复杂命令：运维自己知道要 grep 什么。
  const hint = `tail -n 500 ${data.path}`
  return (
    <div className="space-y-2 border-t px-3 py-2 text-[11px] text-muted-foreground">
      {data.truncated && (
        <div className="flex items-center gap-1.5 text-status-warning">
          <AlertTriangle className="size-3.5 shrink-0" />
          <span>
            {t('diagnostics.truncated', { size: formatBytes(data.fileBytes) })}
          </span>
        </div>
      )}
      <div className="flex items-center gap-2">
        <span className="shrink-0">{t('diagnostics.moreHint')}</span>
        <code className="min-w-0 flex-1 truncate rounded bg-muted px-1.5 py-0.5 font-mono">{hint}</code>
        <Button
          variant="ghost"
          size="sm"
          className="h-6 shrink-0 px-1.5"
          onClick={async () => {
            const ok = await copyToClipboard(hint)
            if (ok) {
              toast.success(t('common.copied'))
            } else {
              toast.error(t('common.copyFailed'))
            }
          }}
        >
          <Copy className="size-3.5" />
        </Button>
      </div>
    </div>
  )
}

export default function DiagnosticsLogDialog({ open, onOpenChange, nodeId, title }: DiagnosticsLogDialogProps) {
  const { t } = useTranslation()
  // 一次性拉取的行数（允许运维手动切档，但不开放到任意值）。
  const [lines, setLines] = useState(200)

  const platformQuery = usePlatformDiagnosticsLog(open && !nodeId, lines)
  const nodeQuery = useNodeDiagnosticsLog(nodeId ?? null, open && !!nodeId, lines)
  const query = nodeId ? nodeQuery : platformQuery

  const copyAll = async () => {
    const text = query.data?.content
    if (!text) {
      toast.error(t('diagnostics.empty'))
      return
    }
    const ok = await copyToClipboard(text)
    if (ok) {
      toast.success(t('common.copied'))
    } else {
      toast.error(t('common.copyFailed'))
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[calc(100dvh-6rem)] flex-col sm:max-w-4xl">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>
        <div className="flex items-center gap-2 text-xs">
          <span className="text-muted-foreground">{t('diagnostics.linesLabel')}</span>
          {[100, 200, 500].map((n) => (
            <Button
              key={n}
              variant={lines === n ? 'default' : 'outline'}
              size="sm"
              className="h-6 px-2 text-[11px]"
              onClick={() => setLines(n)}
            >
              {n}
            </Button>
          ))}
          <Button variant="outline" size="sm" className="ml-auto h-6 px-2 text-[11px]" onClick={() => void query.refetch()}>
            {t('diagnostics.refresh')}
          </Button>
          <Button variant="outline" size="sm" className="h-6 px-2 text-[11px]" onClick={() => void copyAll()}>
            <Copy className="mr-1 size-3" />
            {t('common.copy')}
          </Button>
        </div>
        <div className={cn('flex min-h-0 flex-1 flex-col overflow-hidden rounded-md border')}>
          <LogBody data={query.data} loading={query.isLoading} error={query.error} />
        </div>
        <LogFooter data={query.data} />
      </DialogContent>
    </Dialog>
  )
}
