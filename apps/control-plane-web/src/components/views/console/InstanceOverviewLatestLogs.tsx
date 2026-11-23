import { useTranslation } from 'react-i18next'
import type { ReactNode } from 'react'

import { FileText } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Card } from '@jianmanager/ui/components/card'
import type { LogEntry } from '@/lib/console/console-log-types'

export interface InstanceOverviewLatestLogsLinkArgs {
  to: string
  className?: string
  children: ReactNode
}

export interface InstanceOverviewLatestLogsProps {
  logs: LogEntry[]
  isLoading?: boolean
  isError?: boolean
  isIncomplete?: boolean
  renderLink?: (args: InstanceOverviewLatestLogsLinkArgs) => ReactNode
  onOpenLogs?: () => void
}

/** Bounded snapshot of the existing overview query; no subscription or follow channel. */
export function InstanceOverviewLatestLogs({ logs, isLoading, isError, isIncomplete, renderLink, onOpenLogs }: InstanceOverviewLatestLogsProps) {
  const { t } = useTranslation()
  const title = t('serverConsole.overviewLatestLogs', { defaultValue: '最近日志' })
  const rows = [...logs].sort((a, b) => timestamp(b.time) - timestamp(a.time)).slice(0, 30)
  const openLogs = renderLink
    ? renderLink({ to: '/logs', className: 'text-[11px] text-muted-foreground hover:text-primary', children: `${t('serverConsole.overviewOpenLogs', { defaultValue: '查看日志' })} →` })
    : <Button type="button" size="sm" variant="invisible" onClick={() => onOpenLogs?.()}>{t('serverConsole.overviewOpenLogs', { defaultValue: '查看日志' })} →</Button>

  return (
    <Card data-testid="overview-latest-logs" className="min-w-0 gap-0 py-0">
      <div className="flex items-center justify-between gap-2 border-b px-3 py-2">
        <h2 className="flex items-center gap-1.5 text-xs font-semibold"><FileText className="size-3.5" />{title}</h2>
        {openLogs}
      </div>
      <div role="region" aria-label={title} tabIndex={0} className="h-[200px] min-w-0 overflow-auto px-3 py-2 text-[11px]">
        {isIncomplete && !isError && <p role="note" className="mb-2 text-muted-foreground">{t('serverConsole.overviewLogsIncomplete', { defaultValue: '最近一小时快照，覆盖尚未确认；更多信息请查看日志页。' })}</p>}
        {isError && <p role="alert" className="mb-2 text-status-warning">{t('serverConsole.overviewLogsFailed', { defaultValue: '暂时无法读取最近日志，可前往日志页查看。' })}</p>}
        {isLoading && <p role="status" className="mb-2 text-muted-foreground">{t('common.loading')}</p>}
        {rows.length === 0 && !isLoading && !isError && <p className="text-muted-foreground">{t('serverConsole.noLogs')}</p>}
        <ol className="space-y-1.5 font-mono">
          {rows.map((log, index) => (
            <li key={`${log.id}-${index}`} data-testid="overview-log-line" className="grid grid-cols-[auto_auto_minmax(0,1fr)] items-start gap-x-2">
              <time dateTime={Number.isFinite(Date.parse(log.time)) ? log.time : undefined} className="text-muted-foreground">{timestamp(log.time) === 0 ? '—' : new Date(log.time).toLocaleTimeString()}</time>
              <span className={log.level === 'error' ? 'text-status-danger' : log.level === 'warn' ? 'text-status-warning' : 'text-muted-foreground'}>{log.level.toUpperCase()}</span>
              <span className="min-w-0 whitespace-pre-wrap break-words">{log.message}</span>
            </li>
          ))}
        </ol>
      </div>
    </Card>
  )
}

function timestamp(time: string): number {
  const parsed = Date.parse(time)
  return Number.isFinite(parsed) ? parsed : 0
}
