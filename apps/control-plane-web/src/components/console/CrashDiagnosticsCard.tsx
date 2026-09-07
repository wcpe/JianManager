import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { ChevronDown, Copy, FileWarning } from 'lucide-react'

import { useCrashSnapshots, type CrashSnapshot } from '@/api/crashSnapshots'
import { copyToClipboard } from '@/lib/clipboard'
import { cn } from '@jianmanager/ui'

interface CrashDiagnosticsProps {
  /** 实例 ID。 */
  instanceId: number
}

/**
 * 崩溃诊断（FR-313）：实例最近的崩溃快照（后端滚动保留 5 条），
 * 每条含发生时间/退出码/信号/运行时长，展开可见崩溃前终端尾部输出（等宽字体）。
 * 与 FR-312 失败横幅互补：横幅一句话说「为什么没起来」，这里留「死前现场」。
 *
 * FR-423 起不再自带卡壳，而是作为一段嵌进概览的「动态与告警」流——
 * 原先它单独占一张卡，无崩溃时仍撑 88px 说一句「暂无」，纯属浪费；
 * 现在无快照就是流末尾一行灰字。
 *
 * FR-417 给崩溃尾部输出补复制按钮：那段 `<pre>` 装的正是 Java 异常堆栈，
 * 而堆栈靠鼠标拖选是最难受的复制场景。
 */
export default function CrashDiagnostics({ instanceId }: CrashDiagnosticsProps) {
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

  // 容器恒存在（testid 挂在此层）：快照是异步数据，若空态与列表各自是根节点，
  // 拿到的节点引用会在数据到达时被换掉，调用方（含测试）无法在同一容器内等待。
  return (
    <div data-testid="crash-diagnostics" className={cn(snapshots.length > 0 && 'border-b last:border-b-0')}>
      {snapshots.length === 0 ? (
        <div className="flex items-center gap-2 px-2.5 py-1.5 text-[11px] text-muted-foreground">
          <span aria-hidden className="size-1.5 shrink-0 rounded-full bg-muted-foreground/40" />
          <span>{t('serverConsole.crashEmpty')}</span>
        </div>
      ) : (
        <>
          <div className="flex items-center gap-1.5 px-2.5 pt-2 text-[11px] font-medium text-status-danger">
            <FileWarning className="size-3.5" />
            {/* 标题独占一个元素：与右侧提示同处一个 div 会让 textContent 连在一起，破坏按名查询。 */}
            <span>{t('serverConsole.crashDiagnostics')}</span>
            <span className="ml-auto font-normal text-muted-foreground">{t('serverConsole.crashKeepHint')}</span>
          </div>
          <ul className="space-y-1.5 p-2">
            {snapshots.map((snap) => (
              <CrashSnapshotRow key={snap.id} snapshot={snap} expanded={expanded.has(snap.id)} onToggle={() => toggle(snap.id)} />
            ))}
          </ul>
        </>
      )}
    </div>
  )
}

function CrashSnapshotRow({ snapshot, expanded, onToggle }: { snapshot: CrashSnapshot; expanded: boolean; onToggle: () => void }) {
  const { t } = useTranslation()

  /**
   * 复制崩溃现场（FR-417）：带上时间/退出码/信号的抬头再接尾部输出。
   * 只复制 `tailOutput` 会丢掉「哪次崩溃、怎么死的」，贴给别人看时对方要反问一轮。
   */
  const copySnapshot = async () => {
    const header = [
      `${new Date(snapshot.occurredAt).toLocaleString()}`,
      `${t('serverConsole.crashExitCode')} ${snapshot.exitCode}`,
      snapshot.signal ? `${t('serverConsole.crashSignal')} ${snapshot.signal}` : '',
      `${t('serverConsole.crashDuration')} ${formatCrashDuration(snapshot.durationMs)}`,
    ]
      .filter(Boolean)
      .join(' · ')
    const ok = await copyToClipboard(`${header}\n${snapshot.tailOutput ?? ''}`.trimEnd())
    if (ok) toast.success(t('common.copied'))
    else toast.error(t('common.copyFailed'))
  }

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
            <>
              <div className="mb-1 flex justify-end">
                <button
                  type="button"
                  onClick={() => void copySnapshot()}
                  className="flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] text-muted-foreground hover:bg-muted hover:text-foreground"
                >
                  <Copy className="size-3" />
                  {t('serverConsole.crashCopy')}
                </button>
              </div>
              <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-sm bg-muted p-2 font-mono text-[11px] leading-relaxed text-foreground">
                {snapshot.tailOutput}
              </pre>
            </>
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
