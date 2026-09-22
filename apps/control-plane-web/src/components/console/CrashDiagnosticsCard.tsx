import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { ChevronDown, Copy, FileWarning } from 'lucide-react'

import { useCrashSnapshots, useCrashTrend, type CrashSnapshot, type CrashTrend } from '@/api/crashSnapshots'
import { copyToClipboard } from '@/lib/clipboard'
import { cn } from '@jianmanager/ui'

interface CrashDiagnosticsProps {
  /** 实例 ID。 */
  instanceId: number
}

/** 根因 → 徽标配色（与状态色体系同源，避免另起一套颜色）。 */
const ROOT_CAUSE_TONE: Record<string, string> = {
  oom: 'bg-status-danger/10 text-status-danger',
  segfault: 'bg-status-danger/10 text-status-danger',
  port_in_use: 'bg-status-warning/10 text-status-warning',
  class_not_found: 'bg-status-warning/10 text-status-warning',
  jvm_args: 'bg-status-warning/10 text-status-warning',
  permission: 'bg-status-warning/10 text-status-warning',
  corrupt_data: 'bg-status-warning/10 text-status-warning',
  unknown: 'bg-muted text-muted-foreground',
}

/**
 * 崩溃诊断（FR-313，增强 FR-470）：实例最近的崩溃快照（后端滚动保留 5 条），
 * 每条含发生时间/退出码/信号/运行时长的同时，**直接给出根因归类 + 置信度 + 命中证据**——
 * 原先运维看到的是 200 行日志，得自己从中找 OutOfMemoryError；机器能做的归类不该让人做。
 *
 * FR-470 另加**趋势迷你图与同类聚合**：快照每实例只留 5 条（连崩会抹掉历史），
 * 趋势来自独立汇总表，回答「最近一周崩了几次、同一种原因崩了多少次」。
 * 关联资源证据（崩前 RSS / 堆峰值）为 OOM 这类根因提供证据链。
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
  // 趋势只在确有崩溃时查询：无崩溃的实例不必多打一个接口。
  const { data: trend } = useCrashTrend(instanceId, 30, snapshots.length > 0)
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
          {trend && <CrashTrendPanel trend={trend} />}
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

/**
 * 趋势面板（FR-470）：近 30 天按天计数迷你图 + Top 根因 + 同类聚合。
 * 数据源是独立汇总表，连崩超过 5 次（快照被滚动裁剪）时这里仍显示完整次数。
 *
 * **时区口径（W-12）**：`points[].day` 是后端 `bucket_day`，语义是 **UTC 日**
 * （写入侧 `t.UTC()`、窗口下界同样 UTC），而同一卡片的快照行时间是本地时区。
 * 这里**不做 UTC→本地换算**，而是把「这是 UTC 日」显式写在 tooltip 与图例上，原因有二：
 * ① 一个 UTC 日横跨本地两个日历日，换算成单个本地日期必然丢信息（UTC−7 时 07-13 会显示成 07-12，
 *    反而与「07-13 那天崩的」对不上）；
 * ② 运维核对崩溃时刻时，UTC 日是**不会随浏览器时区变化**的稳定口径。
 * 两种读法并列会误解，所以标注不可省——这才是原缺陷（UTC 日与本地时间并排却无任何标注）的根因。
 */
function CrashTrendPanel({ trend }: { trend: CrashTrend }) {
  const { t } = useTranslation()
  // 按天汇总（可能有多根因，取当天总数）。
  const byDay = new Map<string, number>()
  for (const p of trend.points) {
    byDay.set(p.day, (byDay.get(p.day) ?? 0) + p.count)
  }
  const days = [...byDay.entries()].sort((a, b) => (a[0] < b[0] ? -1 : 1))
  const max = days.reduce((m, [, c]) => Math.max(m, c), 1)

  return (
    <div data-testid="crash-trend" className="space-y-1.5 px-2.5 pt-2">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
        <span>{t('serverConsole.crashTrendTitle', { days: trend.days })}</span>
        <span className="font-mono text-foreground">{t('serverConsole.crashTrendTotal', { count: trend.total })}</span>
        {/* 时区口径必须显式声明：与同卡片快照行的本地时间并列时，不标注等于让人对错柱子。 */}
        <span data-testid="crash-trend-basis" className="text-muted-foreground">
          {t('serverConsole.crashTrendBasis')}
        </span>
      </div>
      {days.length > 0 && (
        /* data-day 供测试按「天」精确断言（同卡片内同类聚合的指纹文本也带 title，按 title 全域取会串）。 */
        <div data-testid="crash-trend-bars" className="flex h-8 items-end gap-0.5" aria-hidden>
          {days.map(([day, count]) => (
            <span
              key={day}
              data-day={day}
              data-count={count}
              title={t('serverConsole.crashTrendDayTip', { day, count })}
              className="min-w-[3px] flex-1 rounded-sm bg-status-danger/50"
              style={{ height: `${Math.max(10, Math.round((count / max) * 100))}%` }}
            />
          ))}
        </div>
      )}
      {trend.byRootCause.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {trend.byRootCause.map((c) => (
            <RootCauseBadge key={c.rootCause} cause={c.rootCause} count={c.count} />
          ))}
        </div>
      )}
      {trend.topSignatures.length > 0 && (
        <div data-testid="crash-signature-aggregate" className="space-y-0.5">
          <div className="text-[11px] text-muted-foreground">{t('serverConsole.crashSameSignature')}</div>
          <ul className="space-y-0.5">
            {trend.topSignatures.map((s) => (
              <li key={`${s.rootCause}:${s.signature}`} className="flex items-center gap-2 text-[11px]">
                <span className="truncate font-mono text-muted-foreground" title={s.signature}>
                  {s.signature || t('serverConsole.crashUnknownSignature')}
                </span>
                <span className="ml-auto shrink-0 font-mono text-foreground">×{s.count}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}

/** 根因徽标（根因标签 + 计数），供趋势面板与快照行复用。 */
function RootCauseBadge({ cause, count }: { cause: string; count?: number }) {
  const { t } = useTranslation()
  return (
    <span
      data-testid="crash-root-cause"
      data-cause={cause}
      className={cn('rounded-full px-2 py-0.5 text-[11px] font-medium', ROOT_CAUSE_TONE[cause] ?? ROOT_CAUSE_TONE.unknown)}
    >
      {t(`serverConsole.crashCause.${cause}`, { defaultValue: cause })}
      {typeof count === 'number' && <span className="ml-1 font-mono">×{count}</span>}
    </span>
  )
}

function CrashSnapshotRow({ snapshot, expanded, onToggle }: { snapshot: CrashSnapshot; expanded: boolean; onToggle: () => void }) {
  const { t } = useTranslation()
  const evidence = snapshot.evidenceLines ?? []

  /**
   * 复制崩溃现场（FR-417 + FR-470）：带上时间/退出码/信号/根因的抬头再接尾部输出。
   * 只复制 `tailOutput` 会丢掉「哪次崩溃、怎么死的」，贴给别人看时对方要反问一轮；
   * FR-470 起抬头补上机器判定的根因，接手的人一眼知道该查什么。
   */
  const copySnapshot = async () => {
    const header = [
      `${new Date(snapshot.occurredAt).toLocaleString()}`,
      `${t('serverConsole.crashExitCode')} ${snapshot.exitCode}`,
      snapshot.signal ? `${t('serverConsole.crashSignal')} ${snapshot.signal}` : '',
      `${t('serverConsole.crashDuration')} ${formatCrashDuration(snapshot.durationMs)}`,
      snapshot.rootCause ? `${t('serverConsole.crashRootCause')} ${snapshot.rootCause}` : '',
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
        {snapshot.rootCause && <RootCauseBadge cause={snapshot.rootCause} />}
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
        <div className="space-y-2 border-t px-2.5 py-2">
          {snapshot.confidence !== undefined && snapshot.confidence > 0 && (
            <p className="text-[11px] text-muted-foreground">
              {t('serverConsole.crashConfidence', { percent: Math.round(snapshot.confidence * 100) })}
            </p>
          )}
          {evidence.length > 0 && (
            <div data-testid="crash-evidence" className="space-y-0.5">
              <div className="text-[11px] text-muted-foreground">{t('serverConsole.crashEvidence')}</div>
              <ul className="space-y-0.5 rounded-sm bg-muted p-2">
                {evidence.map((line, idx) => (
                  <li key={`${idx}-${line}`} className="truncate font-mono text-[11px] text-foreground" title={line}>
                    {line}
                  </li>
                ))}
              </ul>
            </div>
          )}
          {snapshot.correlation && <CrashCorrelationBlock correlation={snapshot.correlation} />}
          {snapshot.tailOutput ? (
            <>
              <div className="flex justify-end">
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

/**
 * 资源关联证据（FR-470 §2.3）：崩前 RSS / 内存上限 / 堆峰值。
 * nearOom 为真时高亮——这是 OOM 崩溃最直接的证据链（「不是猜的，崩前 RSS 已经顶到上限」）。
 */
function CrashCorrelationBlock({ correlation }: { correlation: NonNullable<CrashSnapshot['correlation']> }) {
  const { t } = useTranslation()
  return (
    <div data-testid="crash-correlation" className="space-y-0.5 text-[11px]">
      <div className="text-muted-foreground">{t('serverConsole.crashCorrelation')}</div>
      <ul className="space-y-0.5">
        {correlation.nearOom && (
          <li className="font-medium text-status-danger">{t('serverConsole.crashNearOom')}</li>
        )}
        {correlation.rssAtCrash > 0 && (
          <li className="font-mono text-muted-foreground">
            {t('serverConsole.crashRssAtCrash', {
              rss: formatBytes(correlation.rssAtCrash),
              limit: correlation.memLimitMb > 0 ? `${correlation.memLimitMb} MiB` : t('serverConsole.crashNoLimit'),
            })}
          </li>
        )}
        {correlation.heapUsedMax > 0 && (
          <li className="font-mono text-muted-foreground">
            {t('serverConsole.crashHeapPeak', { heap: formatBytes(correlation.heapUsedMax) })}
          </li>
        )}
        {correlation.note && <li className="text-muted-foreground">{correlation.note}</li>}
      </ul>
    </div>
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

/** 字节人性化（<1KiB 显示 B，<1MiB 显示 KiB，否则 MiB/GiB）。 */
function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KiB`
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)} GiB`
}
