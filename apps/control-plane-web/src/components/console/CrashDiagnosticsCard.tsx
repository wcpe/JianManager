import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown, FileWarning } from 'lucide-react'

import { useCrashSnapshots, type CrashSnapshot } from '@/api/crashSnapshots'
import { cn } from '@jianmanager/ui'

/** 崩溃诊断卡（FR-313）：实例控制台概览区的快照列表 + 尾部输出展开。 */
interface CrashDiagnosticsCardProps {
  /** 实例 ID。 */
  instanceId: number
}

/**
 * 崩溃诊断卡（FR-313）：展示实例最近的崩溃快照（后端滚动保留 5 条），
 * 每条含发生时间/退出码/信号/运行时长，展开可见崩溃前终端尾部输出（等宽字体）。
 * 与 FR-312 失败横幅互补：横幅一句话说「为什么没起来」，此卡留「死前现场」。
 */
export default function CrashDiagnosticsCard({ instanceId }: CrashDiagnosticsCardProps) {
  const { t } = useTranslation()
  const { data: snapshots = [] } = useCrashSnapshots(instanceId)
  // 记录展开的快照 id 集合：允许同时展开多条对比（如连崩场景）。
  const [expanded, setExpanded] = useState<ReadonlySet<number>>(new Set())

  const toggle = (id: number) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  return (
    <section data-testid="crash-diagnostics" className="rounded-lg border bg-card p-3 shadow-soft">
      <div className="mb-2 flex items-center justify-between gap-2">
        <h2 className="flex items-center gap-1.5 text-sm font-semibold">
          <FileWarning className="size-4 text-status-danger" />
          {t('serverConsole.crashDiagnostics')}
        </h2>
        <span className="text-[11px] text-muted-foreground">{t('serverConsole.crashKeepHint')}</span>
      </div>

      {snapshots.length === 0 ? (
        <p className="rounded-md border border-dashed bg-muted/50 px-3 py-4 text-center text-xs text-muted-foreground">
          {t('serverConsole.crashEmpty')}
        </p>
      ) : (
        <ul className="space-y-1.5">
          {snapshots.map((snap) => (
            <CrashSnapshotRow key={snap.id} snapshot={snap} expanded={expanded.has(snap.id)} onToggle={() => toggle(snap.id)} />
          ))}
        </ul>
      )}
    </section>
  )
}

function CrashSnapshotRow({ snapshot, expanded, onToggle }: { snapshot: CrashSnapshot; expanded: boolean; onToggle: () => void }) {
  const { t } = useTranslation()
  return (
    <li className="rounded-md border bg-muted/50">
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={expanded}
        className="flex w-full flex-wrap items-center gap-x-3 gap-y-1 px-2.5 py-2 text-left text-xs hover:bg-muted/80"
      >
        <span className="font-mono text-muted-foreground">{new Date(snapshot.occurredAt).toLocaleString()}</span>
        <span className="rounded-full bg-status-danger/10 px-2 py-0.5 font-mono font-medium text-status-danger">
          {t('serverConsole.crashExitCode')} {snapshot.exitCode}
        </span>
        {snapshot.signal && (
          <span className="rounded-full bg-status-warning/10 px-2 py-0.5 font-mono text-status-warning">
            {t('serverConsole.crashSignal')} {snapshot.signal}
          </span>
        )}
        <span className="font-mono text-muted-foreground">
          {t('serverConsole.crashDuration')} {formatCrashDuration(snapshot.durationMs)}
        </span>
        <ChevronDown className={cn('ml-auto size-3.5 shrink-0 text-muted-foreground transition-transform', expanded && 'rotate-180')} />
      </button>
      {expanded && (
        <div className="border-t px-2.5 py-2">
          {snapshot.tailOutput ? (
            <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-sm bg-muted p-2 font-mono text-[11px] leading-relaxed text-foreground">
              {snapshot.tailOutput}
            </pre>
          ) : (
            <p className="text-xs text-muted-foreground">{t('serverConsole.crashNoOutput')}</p>
          )}
        </div>
      )}
    </li>
  )
}

/** 运行时长人性化：<1s 显示毫秒，<60s 显示秒，<1h 显示分秒，否则时分。 */
function formatCrashDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  const totalSeconds = Math.floor(ms / 1000)
  if (totalSeconds < 60) return `${(ms / 1000).toFixed(1)}s`
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60
  if (minutes < 60) return `${minutes}m ${seconds}s`
  const hours = Math.floor(minutes / 60)
  return `${hours}h ${minutes % 60}m`
}
