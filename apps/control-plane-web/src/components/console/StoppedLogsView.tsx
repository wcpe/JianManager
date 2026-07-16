import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { useLogs } from '@/api/logs'
import { cn } from '@jianmanager/ui'

/**
 * 停机/未运行实例的历史日志回放（FR-345）：从 DB 拉该实例最近日志只读展示，替代空白「实例未运行」占位。
 * 令关服过程/崩溃现场在停机态仍可见（DB 持久，重连/切页签/刷新不丢），并链到完整历史（搜索/时间筛选/导出）。
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

  return (
    <div className="flex min-h-[400px] flex-col rounded-lg bg-[#1a1b26]">
      <div className="flex items-center justify-between gap-2 border-b border-white/10 px-3 py-1.5 text-xs">
        <span className="truncate text-gray-300">{t('instanceDetail.stoppedShowingHistory', { status })}</span>
        <a href={`/logs?instanceId=${instanceId}`} className="shrink-0 text-blue-400 hover:underline">
          {t('instanceDetail.viewFullHistory')}
        </a>
      </div>
      <div ref={bodyRef} className="min-h-0 flex-1 overflow-auto p-2 font-mono text-xs leading-relaxed text-gray-300">
        {isLoading ? (
          <p className="text-gray-500">{t('common.loading')}</p>
        ) : entries.length === 0 ? (
          <p className="text-gray-500">{t('instanceDetail.noHistoryLogs')}</p>
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
