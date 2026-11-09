import { useEffect, useRef, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Copy, History } from 'lucide-react'
import { copyToClipboard } from '@/lib/shared/clipboard'
import type { LogEntry } from '@/lib/console/console-log-types'
import { cn } from '@jianmanager/ui'
import { Button } from '@jianmanager/ui/components/button'

export interface StoppedLogsViewProps {
  instanceId: number
  status: string
  /** 日志条目（应用侧取数；后端按时间倒序返回，这里自行正序展示）。 */
  entries?: LogEntry[]
  isLoading?: boolean
  /**
   * 加载失败时的错误文案 key；null/undefined 表示未失败。
   *
   * 为何由容器算：失败要区分 403 无权限 / 超时 / 一般加载失败，而状态码只有取数层看得到。
   * 本视图不触达 api，所以只接一个已算好的 key。
   */
  errorKey?: string | null
  /** 重试回调（失败态展示重试按钮）；缺省时不渲染按钮。 */
  onRetry?: () => void
  /** 提示通道：视图算好文案交外壳展示（本包不弹 toast）。 */
  onNotify?: (kind: 'success' | 'error', message: string) => void
  /** 渲染「完整历史」链接（应用侧接路由；缺省时不渲染该入口）。 */
  renderLink?: (args: { to: string; className?: string; children: ReactNode }) => ReactNode
}

/** 
 * 停机/未运行实例的历史日志回放：从 DB 拉该实例最近日志只读展示，替代空白「实例未运行」占位。
 * 令关服过程/崩溃现场在停机态仍可见（DB 持久，重连/切页签/刷新不丢），并链到完整历史（搜索/时间筛选/导出）。
 *
 *补复制按钮：这里正是「服务器起不来，把关服/崩溃日志发给别人看」的现场，
 * 而原先只能鼠标拖选——300 行日志靠拖选是最难受的场景之一。
 *
 * 取数与路由由 props 注入（ADR-103）：本视图不触达 api / 路由。
 */
export function StoppedLogsView({
  instanceId,
  status,
  entries: rawEntries,
  isLoading,
  errorKey,
  onRetry,
  onNotify,
  renderLink,
}: StoppedLogsViewProps) {
  const { t } = useTranslation()
  const notify = (kind: 'success' | 'error', message: string) => onNotify?.(kind, message)
  // 后端按 time DESC 返回（最新在前）；正序展示（旧→新）贴近终端，最新在底部。
  const entries = rawEntries ? rawEntries.slice().reverse() : []
  const bodyRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    // 加载后滚到底部（最新日志），贴近终端体验。
    if (bodyRef.current) bodyRef.current.scrollTop = bodyRef.current.scrollHeight
  }, [entries.length])

  // 复制走统一入口（含 HTTP 非安全上下文的 execCommand 兜底），成功/失败都给回执。
  const copyAll = async () => {
    if (entries.length === 0) {
      notify('error', t('instanceDetail.consoleCopyNothing'))
      return
    }
    const ok = await copyToClipboard(entries.map((entry) => entry.message).join('\n'))
    notify(ok ? 'success' : 'error', ok ? t('common.copied') : t('common.copyFailed'))
  }

  return (
    // h-full：起本视图与命令栏同处一个 flex 列中，不撑满就会在自己下方留一大片空白。
    <div className="flex h-full min-h-[400px] flex-col rounded-lg bg-[#1a1b26]">
      <div className="flex items-center justify-between gap-2 border-b border-white/10 px-3 py-1.5 text-xs">
        <span className="truncate text-gray-300">{t('instanceDetail.stoppedShowingHistory', { status })}</span>
        <div className="flex shrink-0 items-center gap-3">
          <Button
            type="button"
            variant="invisible"
            size="auto"
            onClick={() => void copyAll()}
            className="flex items-center gap-1 text-xs font-normal text-gray-400 hover:text-gray-100"
          >
            <Copy className="size-3" />
            {t('instanceDetail.consoleCopyLogs')}
          </Button>
          {renderLink?.({
            to: `/logs?instanceId=${instanceId}`,
            className: 'text-blue-400 hover:underline',
            children: t('instanceDetail.viewFullHistory'),
          })}
        </div>
      </div>
      {/* 失败横幅：既包括「无行可展示」也包括「有旧行但本次未刷新」。
          后者不能直接报错（旧行还有用），也不能拿旧行充新——必须明确说“本次未刷新”。 */}
      {errorKey && (
        <div role="alert" className="flex items-center justify-between gap-2 border-b border-white/10 px-3 py-2 text-xs text-status-warning">
          <span>
            {t(errorKey)}
            {entries.length > 0 && ` · ${t('instanceDetail.historyStale')}`}
          </span>
          {onRetry && (
            <button type="button" className="shrink-0 underline" onClick={onRetry}>
              {t('logsFederation.action.retry')}
            </button>
          )}
        </div>
      )}
      <div ref={bodyRef} className="min-h-0 flex-1 overflow-auto p-2 font-mono text-xs leading-relaxed text-gray-300">
        {isLoading ? (
          <p className="text-gray-500">{t('common.loading')}</p>
        ) : errorKey && entries.length === 0 ? (
          <p className="text-gray-500">{t('instanceDetail.historyNotLoaded')}</p>
        ) : entries.length === 0 ? (
          // 空态居中卡片：原先一行灰字吊在左上角， pane 大半空白显得像坏了。
          <div className="flex h-full flex-col items-center justify-center gap-2 text-center">
            <div className="grid size-10 place-items-center rounded-full bg-white/[0.04] text-gray-500">
              <History className="size-4.5" />
            </div>
            <p className="max-w-72 text-xs leading-relaxed text-gray-500">{t('instanceDetail.noHistoryLogs')}</p>
          </div>
        ) : (
          entries.map((e) => (
            <div
              key={e.id}
              className={cn(
                'whitespace-pre-wrap break-all',
                e.level === 'error' && 'text-red-400',
                e.level === 'warn' && 'text-amber-400',
              )}
            >
              {e.message}
            </div>
          ))
        )}
      </div>
    </div>
  )
}