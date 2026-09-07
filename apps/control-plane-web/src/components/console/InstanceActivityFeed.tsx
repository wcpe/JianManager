import { useTranslation } from 'react-i18next'
import { AlertTriangle, Copy } from 'lucide-react'
import { toast } from 'sonner'

import { copyToClipboard } from '@/lib/clipboard'
import { cn } from '@jianmanager/ui'
import CrashDiagnostics from './CrashDiagnosticsCard'

interface ActivityLog {
  id: number
  level: string
  message: string
  time: string
}

interface InstanceActivityFeedProps {
  instanceId: number
  /** 近期实例日志（新的在前）。 */
  logs: ActivityLog[]
  /** 当前关注事项（buildWatchItems 产出，空数组即一切正常）。 */
  watchItems: string[]
  /** 运行时长（秒），用于「运行状态正常」行的持续时间。 */
  uptimeSeconds?: number
}

/**
 * 动态与告警（FR-423）：把原概览区三张低信息量卡——「最近事件」「关注事项」「崩溃诊断」
 * ——合流成一条时间线。
 *
 * 合流动因是量测出来的：三栏 grid 等高，高度由旁边的图表卡（229px）决定，
 * 而「关注事项」通常只有一行字 → 死区 159px（69%）、「最近事件」两行 → 死区 97px（42%）。
 * 三者本就是同一类信息（这台服务器刚发生了什么 / 现在要不要管），合成一条流后
 * 内容足以填满整栏，且阅读顺序符合排障动线：先看当前状态，再往下翻历史。
 *
 * 排序：当前态（告警或「正常」）恒在最前，其后是崩溃现场，再往下是日志倒序。
 */
export default function InstanceActivityFeed({ instanceId, logs, watchItems, uptimeSeconds }: InstanceActivityFeedProps) {
  const { t } = useTranslation()

  const copyLine = async (text: string) => {
    const ok = await copyToClipboard(text)
    // 复制成功/失败都给回执：静默 void 掉返回值会让用户以为「点了没反应」。
    if (ok) toast.success(t('common.copied'))
    else toast.error(t('common.copyFailed'))
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-auto">
      {/* 当前态：有告警逐条列出，无告警一行绿字带持续时长。 */}
      {watchItems.length > 0 ? (
        watchItems.map((item) => (
          <FeedRow key={item} tone="warning" text={item} icon={<AlertTriangle className="size-3.5 shrink-0 text-status-warning" />} />
        ))
      ) : (
        <FeedRow
          tone="success"
          text={t('serverConsole.healthyNow')}
          meta={uptimeSeconds ? t('serverConsole.healthyFor', { duration: formatDuration(uptimeSeconds) }) : undefined}
        />
      )}

      {/* 崩溃现场（FR-313）：有快照则成段可展开，无则一行灰字。 */}
      <CrashDiagnostics instanceId={instanceId} />

      {/* 日志倒序。 */}
      {logs.map((log) => (
        <FeedRow
          key={log.id}
          tone={log.level === 'error' ? 'danger' : log.level === 'warn' ? 'warning' : 'info'}
          text={log.message}
          meta={new Date(log.time).toLocaleTimeString()}
          onCopy={() => copyLine(`${new Date(log.time).toLocaleString()} [${log.level.toUpperCase()}] ${log.message}`)}
        />
      ))}
      {logs.length === 0 && (
        <p className="px-2.5 py-2 text-[11px] text-muted-foreground">{t('serverConsole.noLogs')}</p>
      )}
    </div>
  )
}

function FeedRow({
  tone,
  text,
  meta,
  icon,
  onCopy,
}: {
  tone: 'success' | 'info' | 'warning' | 'danger'
  text: string
  meta?: string
  icon?: React.ReactNode
  onCopy?: () => void
}) {
  const { t } = useTranslation()
  return (
    <div className="group flex items-start gap-2 border-b px-2.5 py-1.5 last:border-b-0 hover:bg-muted/60">
      {icon ?? (
        <span
          aria-hidden
          className={cn(
            'mt-1.5 size-1.5 shrink-0 rounded-full',
            tone === 'success' && 'bg-status-success',
            tone === 'info' && 'bg-status-info',
            tone === 'warning' && 'bg-status-warning',
            tone === 'danger' && 'bg-status-danger',
          )}
        />
      )}
      <div className="min-w-0 flex-1">
        <p className="break-words text-xs leading-relaxed">{text}</p>
        {meta && <p className="mt-0.5 font-mono text-[10px] text-muted-foreground">{meta}</p>}
      </div>
      {onCopy && (
        <button
          type="button"
          onClick={onCopy}
          title={t('common.copy')}
          className="shrink-0 rounded-sm p-1 text-muted-foreground opacity-0 transition-opacity hover:bg-muted hover:text-foreground focus-visible:opacity-100 group-hover:opacity-100"
        >
          <Copy className="size-3" />
        </button>
      )}
    </div>
  )
}

/** 持续时长人性化：Xd Yh / Xh Ym / Xm。 */
function formatDuration(sec: number): string {
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}
