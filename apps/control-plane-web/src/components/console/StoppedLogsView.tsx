import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Copy, History } from 'lucide-react'
import { useLogs } from '@/api/logs'
import { copyToClipboard } from '@/lib/clipboard'
import { cn } from '@jianmanager/ui'

/**
 * 停机/未运行实例的历史日志回放（FR-345）：从 DB 拉该实例最近日志只读展示，替代空白「实例未运行」占位。
 * 令关服过程/崩溃现场在停机态仍可见（DB 持久，重连/切页签/刷新不丢），并链到完整历史（搜索/时间筛选/导出）。
 *
 * FR-417 补复制按钮：这里正是「服务器起不来，把关服/崩溃日志发给别人看」的现场，
 * 而原先只能鼠标拖选——300 行日志靠拖选是最难受的场景之一。
 */
export default function StoppedLogsView({ instanceId, status }: { instanceId: number; status: string }) {
  const { t } = useTranslation()
  const { data, isLoading } = useLogs({ instanceId, source: 'instance', pageSize: 300 })
  // 后端按 time DESC 返回（最新在前）；正序展示（旧→新）贴近终端，最新在底部。
  const entries = data?.items ? data.items.slice().reverse() : []
  const bodyRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    // 加载后滚到底部（最新日志），贴近终端体验。
    if (bodyRef.current) bodyRef.current.scrollTop = bodyRef.current.scrollHeight
  }, [entries.length])

  // 复制走统一入口（含 HTTP 非安全上下文的 execCommand 兜底），成功/失败都给回执。
  const copyAll = async () => {
    if (entries.length === 0) {
      toast.error(t('instanceDetail.consoleCopyNothing'))
      return
    }
    const ok = await copyToClipboard(entries.map((entry) => entry.message).join('\n'))
    if (ok) toast.success(t('common.copied'))
    else toast.error(t('common.copyFailed'))
  }

  return (
    // h-full：FR-415 起本视图与命令栏同处一个 flex 列中，不撑满就会在自己下方留一大片空白。
    <div className="flex h-full min-h-[400px] flex-col rounded-lg bg-[#1a1b26]">
      <div className="flex items-center justify-between gap-2 border-b border-white/10 px-3 py-1.5 text-xs">
        <span className="truncate text-gray-300">{t('instanceDetail.stoppedShowingHistory', { status })}</span>
        <div className="flex shrink-0 items-center gap-3">
          <button
            type="button"
            onClick={() => void copyAll()}
            className="flex items-center gap-1 text-gray-400 hover:text-gray-100"
          >
            <Copy className="size-3" />
            {t('instanceDetail.consoleCopyLogs')}
          </button>
          <a href={`/logs?instanceId=${instanceId}`} className="text-blue-400 hover:underline">
            {t('instanceDetail.viewFullHistory')}
          </a>
        </div>
      </div>
      <div ref={bodyRef} className="min-h-0 flex-1 overflow-auto p-2 font-mono text-xs leading-relaxed text-gray-300">
        {isLoading ? (
          <p className="text-gray-500">{t('common.loading')}</p>
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
