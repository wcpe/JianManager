import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import {
  useServerState,
  type Bounded,
  type ProbeServerState,
  type ServerSection,
  type JvmSection,
  type ClassloaderSection,
  type SchedulerSection,
  type ListenersSection,
  type WorldEntry,
} from '@/api/serverState'
import { Panel } from '@/components/ui/panel'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

/** 默认折叠阈值：有界列表超过此数量时默认仅展示前 N 行，余下点「展开全部」纯前端切片（不二次请求）。 */
const COLLAPSE_THRESHOLD = 20

/** 字节 → 人类可读（GiB/MiB/KiB）；非正数显示 0。 */
function fmtBytes(b: number | undefined): string {
  if (b == null || !Number.isFinite(b) || b <= 0) return '0'
  if (b >= 1024 ** 3) return `${(b / 1024 ** 3).toFixed(2)} GiB`
  if (b >= 1024 ** 2) return `${(b / 1024 ** 2).toFixed(1)} MiB`
  if (b >= 1024) return `${(b / 1024).toFixed(0)} KiB`
  return `${b} B`
}

/** 毫秒时长 → d/h/m/s 紧凑展示。 */
function fmtDuration(ms: number | undefined): string {
  if (ms == null || !Number.isFinite(ms) || ms < 0) return '—'
  const s = Math.floor(ms / 1000)
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (d > 0) return `${d}d ${h}h ${m}m`
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m ${s % 60}s`
  return `${s}s`
}

/** 紧凑键值行（FR-061 风格）：标签 + 值，缺省值降级为破折号。 */
function KV({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-3 border-b border-border/40 py-1 last:border-0">
      <span className="shrink-0 text-muted-foreground">{label}</span>
      <span className="text-right font-medium tabular-nums break-all">{value ?? '—'}</span>
    </div>
  )
}

/** 布尔值徽标：true 绿 / false 灰。 */
function Bool({ v }: { v: boolean | undefined }) {
  if (v == null) return <span className="text-muted-foreground">—</span>
  return (
    <span className={cn(v ? 'text-green-600 dark:text-green-400' : 'text-muted-foreground')}>
      {String(v)}
    </span>
  )
}

/** 分区采集失败时的占位（探针侧某子项 runCatching 降级为 `{ error }`）。 */
function SectionError({ msg }: { msg?: string }) {
  const { t } = useTranslation()
  return <div className="p-2 text-xs text-amber-600 dark:text-amber-400">{msg || t('serverState.sectionError')}</div>
}

/**
 * 有界列表的密集表（FR-061 风格 + FR-077 大数据折叠不卡）。
 *
 * 探针侧 bounded() 已把超大集合裁剪为 items + total + truncated；前端再对 items 做 UI 折叠：
 * 超 [COLLAPSE_THRESHOLD] 行默认仅渲染前 N 行，点「展开全部」纯前端切片展开（不发二次请求）。
 * truncated 时额外提示「已截断，共 total 项」（真实规模超探针上限，前端无法补全）。
 */
function BoundedTable<T>({
  data,
  columns,
  rowKey,
}: {
  data: Bounded<T> | undefined
  columns: { header: string; cell: (row: T) => ReactNode; className?: string }[]
  rowKey: (row: T, i: number) => string
}) {
  const { t } = useTranslation()
  const [expanded, setExpanded] = useState(false)
  const items = data?.items ?? []
  if (items.length === 0) return <div className="p-2 text-xs text-muted-foreground">—</div>

  const collapsible = items.length > COLLAPSE_THRESHOLD
  const shown = collapsible && !expanded ? items.slice(0, COLLAPSE_THRESHOLD) : items

  return (
    <div className="overflow-x-auto">
      <table className="w-full border-collapse text-xs">
        <thead>
          <tr className="border-b text-left text-muted-foreground">
            {columns.map((c) => (
              <th key={c.header} className={cn('px-2 py-1 font-medium', c.className)}>
                {c.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {shown.map((row, i) => (
            <tr key={rowKey(row, i)} className="border-b border-border/40 last:border-0">
              {columns.map((c) => (
                <td key={c.header} className={cn('px-2 py-1 tabular-nums', c.className)}>
                  {c.cell(row)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      <div className="flex items-center justify-between gap-2 px-2 py-1 text-[11px] text-muted-foreground">
        {data?.truncated && <span>{t('serverState.truncatedNote', { total: data.total })}</span>}
        {collapsible && (
          <button
            type="button"
            className="ml-auto text-primary hover:underline"
            onClick={() => setExpanded((e) => !e)}
          >
            {expanded ? t('serverState.collapse') : t('serverState.showAll', { total: items.length })}
          </button>
        )}
      </div>
    </div>
  )
}

/** server 分区：标量键值 + 插件清单密集表。 */
function ServerPanel({ data }: { data: ServerSection | undefined }) {
  const { t } = useTranslation()
  if (!data) return null
  if (data.error) {
    return (
      <Panel title={t('serverState.server.title')}>
        <SectionError msg={data.error} />
      </Panel>
    )
  }
  return (
    <Panel title={t('serverState.server.title')}>
      <div className="grid grid-cols-1 gap-x-6 text-xs sm:grid-cols-2">
        <KV label={t('serverState.server.version')} value={data.version} />
        <KV label={t('serverState.server.bukkitVersion')} value={data.bukkitVersion} />
        <KV label={t('serverState.server.players')} value={`${data.onlinePlayers ?? '—'} / ${data.maxPlayers ?? '—'}`} />
        <KV label={t('serverState.server.viewDistance')} value={data.viewDistance} />
        <KV label={t('serverState.server.onlineMode')} value={<Bool v={data.onlineMode} />} />
        <KV label={t('serverState.server.whitelist')} value={<Bool v={data.whitelistEnabled} />} />
        <KV label={t('serverState.server.allowNether')} value={<Bool v={data.allowNether} />} />
        <KV label={t('serverState.server.allowEnd')} value={<Bool v={data.allowEnd} />} />
        <KV label={t('serverState.server.motd')} value={data.motd} />
      </div>
      <div className="mt-2">
        <div className="mb-1 text-xs font-medium text-muted-foreground">{t('serverState.server.plugins')}</div>
        <BoundedTable
          data={data.plugins}
          rowKey={(p) => p.name}
          columns={[
            { header: t('serverState.server.pluginName'), cell: (p) => p.name },
            { header: t('serverState.server.pluginVersion'), cell: (p) => p.version },
            {
              header: t('serverState.server.pluginEnabled'),
              cell: (p) => (
                <span className={p.enabled ? 'text-green-600 dark:text-green-400' : 'text-muted-foreground'}>
                  {p.enabled ? t('serverState.server.enabled') : t('serverState.server.disabled')}
                </span>
              ),
            },
          ]}
        />
      </div>
    </Panel>
  )
}

/** worlds 分区：每世界一行的密集表。 */
function WorldsPanel({ data }: { data: Bounded<WorldEntry> | undefined }) {
  const { t } = useTranslation()
  if (!data) return null
  if (!('items' in data)) return <Panel title={t('serverState.worlds.title')}><SectionError /></Panel>
  return (
    <Panel title={t('serverState.worlds.title')}>
      <BoundedTable
        data={data}
        rowKey={(w) => w.name}
        columns={[
          { header: t('serverState.worlds.name'), cell: (w) => w.name },
          { header: t('serverState.worlds.environment'), cell: (w) => w.environment },
          { header: t('serverState.worlds.difficulty'), cell: (w) => w.difficulty },
          { header: t('serverState.worlds.loadedChunks'), cell: (w) => w.loadedChunks },
          { header: t('serverState.worlds.entities'), cell: (w) => w.entities },
          { header: t('serverState.worlds.tileEntities'), cell: (w) => w.tileEntities },
          { header: t('serverState.worlds.players'), cell: (w) => w.players },
        ]}
      />
    </Panel>
  )
}

/** jvm 分区：堆/非堆/线程/运行时长等标量键值。 */
function JvmPanel({ data }: { data: JvmSection | undefined }) {
  const { t } = useTranslation()
  if (!data) return null
  if (data.error) {
    return <Panel title={t('serverState.jvm.title')}><SectionError msg={data.error} /></Panel>
  }
  return (
    <Panel title={t('serverState.jvm.title')}>
      <div className="grid grid-cols-1 gap-x-6 text-xs sm:grid-cols-2">
        <KV label={t('serverState.jvm.name')} value={data.jvmName} />
        <KV label={t('serverState.jvm.vendor')} value={data.jvmVendor} />
        <KV label={t('serverState.jvm.version')} value={data.jvmVersion} />
        <KV label={t('serverState.jvm.processors')} value={data.availableProcessors} />
        <KV label={t('serverState.jvm.uptime')} value={fmtDuration(data.uptimeMs)} />
        <KV label={t('serverState.jvm.heapUsed')} value={fmtBytes(data.heapUsedBytes)} />
        <KV label={t('serverState.jvm.heapCommitted')} value={fmtBytes(data.heapCommittedBytes)} />
        <KV label={t('serverState.jvm.heapMax')} value={data.heapMaxBytes != null && data.heapMaxBytes > 0 ? fmtBytes(data.heapMaxBytes) : '—'} />
        <KV label={t('serverState.jvm.nonHeapUsed')} value={fmtBytes(data.nonHeapUsedBytes)} />
        <KV label={t('serverState.jvm.threads')} value={data.threadCount} />
        <KV label={t('serverState.jvm.daemonThreads')} value={data.daemonThreadCount} />
        <KV label={t('serverState.jvm.peakThreads')} value={data.peakThreadCount} />
      </div>
    </Panel>
  )
}

/**
 * classloader 专区（FR-076 重点）：类加载计数 + 各插件类加载器层级链。
 *
 * 计数来自 ClassLoadingMXBean；每插件展示其加载器类名 + parent 链（自顶向下到 bootstrap）。
 */
function ClassloaderPanel({ data }: { data: ClassloaderSection | undefined }) {
  const { t } = useTranslation()
  if (!data) return null
  if (data.error) {
    return <Panel title={t('serverState.classloader.title')}><SectionError msg={data.error} /></Panel>
  }
  return (
    <Panel title={t('serverState.classloader.title')}>
      <div className="grid grid-cols-1 gap-x-6 text-xs sm:grid-cols-3">
        <KV label={t('serverState.classloader.loadedClassCount')} value={data.counts?.loadedClassCount} />
        <KV label={t('serverState.classloader.totalLoadedClassCount')} value={data.counts?.totalLoadedClassCount} />
        <KV label={t('serverState.classloader.unloadedClassCount')} value={data.counts?.unloadedClassCount} />
      </div>
      <div className="mt-2">
        <div className="mb-1 text-xs font-medium text-muted-foreground">{t('serverState.classloader.pluginLoaders')}</div>
        <BoundedTable
          data={data.pluginLoaders}
          rowKey={(p) => p.plugin}
          columns={[
            { header: t('serverState.classloader.plugin'), cell: (p) => p.plugin },
            { header: t('serverState.classloader.loaderClass'), cell: (p) => <span className="break-all">{p.loaderClass}</span> },
            {
              header: t('serverState.classloader.chain'),
              cell: (p) => <span className="break-all text-muted-foreground">{(p.chain ?? []).join(' → ')}</span>,
            },
          ]}
        />
      </div>
    </Panel>
  )
}

/** scheduler 分区：待执行任务 / 活跃 worker。 */
function SchedulerPanel({ data }: { data: SchedulerSection | undefined }) {
  const { t } = useTranslation()
  if (!data) return null
  if (data.error) {
    return <Panel title={t('serverState.scheduler.title')}><SectionError msg={data.error} /></Panel>
  }
  return (
    <Panel title={t('serverState.scheduler.title')}>
      <div className="grid grid-cols-1 gap-x-6 text-xs sm:grid-cols-2">
        <KV label={t('serverState.scheduler.pendingTasks')} value={data.pendingTasks} />
        <KV label={t('serverState.scheduler.activeWorkers')} value={data.activeWorkers} />
      </div>
    </Panel>
  )
}

/** listeners 分区：总注册条目 + 按插件分组密集表。 */
function ListenersPanel({ data }: { data: ListenersSection | undefined }) {
  const { t } = useTranslation()
  if (!data) return null
  if (data.error) {
    return <Panel title={t('serverState.listeners.title')}><SectionError msg={data.error} /></Panel>
  }
  return (
    <Panel title={t('serverState.listeners.title')}>
      <div className="grid grid-cols-1 text-xs sm:grid-cols-2">
        <KV label={t('serverState.listeners.totalRegistered')} value={data.totalRegistered} />
      </div>
      <div className="mt-2">
        <div className="mb-1 text-xs font-medium text-muted-foreground">{t('serverState.listeners.byPlugin')}</div>
        <BoundedTable
          data={data.byPlugin}
          rowKey={(p) => p.plugin}
          columns={[
            { header: t('serverState.listeners.plugin'), cell: (p) => p.plugin },
            { header: t('serverState.listeners.count'), cell: (p) => p.count, className: 'text-right' },
          ]}
        />
      </div>
    </Panel>
  )
}

/** 降级提示卡（探针未连入 / 本次采集不可用）。 */
function DegradedCard({ title, hint, detail }: { title: string; hint: string; detail?: string }) {
  return (
    <Panel>
      <div className="space-y-1 p-2 text-xs">
        <p className="font-medium text-amber-600 dark:text-amber-400">{title}</p>
        <p className="text-muted-foreground">{hint}</p>
        {detail && <p className="text-muted-foreground/80">{detail}</p>}
      </div>
    </Panel>
  )
}

/** 全状态分区网格（仅在 available 时渲染）。 */
function StateSections({ state }: { state: ProbeServerState }) {
  return (
    <div className="grid grid-cols-1 gap-3 xl:grid-cols-2">
      <ServerPanel data={state.server} />
      <JvmPanel data={state.jvm} />
      <ClassloaderPanel data={state.classloader} />
      <WorldsPanel data={state.worlds} />
      <SchedulerPanel data={state.scheduler} />
      <ListenersPanel data={state.listeners} />
    </div>
  )
}

/**
 * 「服务器状态」段（FR-077）：按需展示探针采集的全量 Bukkit 内部状态。
 *
 * 开 tab 即首拉一次（{@link useServerState} enabled），之后**仅手动「刷新」**（按需，不持续轮询）。
 * 状态分区以 FR-061 风格密集表呈现（server/worlds/jvm/classloader 专区/scheduler/listeners）；
 * 探针未连入 / 本次采集超时一律清晰降级提示；插件/世界/加载器等大数据折叠展开纯前端切片不卡。
 */
export default function ServerStateSegment({ instanceId }: { instanceId: number }) {
  const { t } = useTranslation()
  const { data, isFetching, isError, refetch } = useServerState(instanceId, true)

  return (
    <div className="space-y-3 p-4">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-baseline gap-3">
          <h3 className="text-sm font-semibold">{t('serverState.title')}</h3>
          {data?.available && data.state?.collectedAt && (
            <span className="text-[11px] text-muted-foreground">
              {t('serverState.collectedAt')} {new Date(data.state.collectedAt).toLocaleTimeString()}
            </span>
          )}
        </div>
        <Button size="sm" variant="outline" disabled={isFetching} onClick={() => void refetch()}>
          {isFetching ? t('serverState.loading') : t('serverState.refresh')}
        </Button>
      </div>

      {isError && <DegradedCard title={t('serverState.unavailable')} hint={t('serverState.unavailableHint')} />}

      {!data && isFetching && <div className="p-2 text-xs text-muted-foreground">{t('serverState.loading')}</div>}

      {data && !data.connected && (
        <DegradedCard
          title={t('serverState.notConnected')}
          hint={t('serverState.notConnectedHint')}
          detail={data.error}
        />
      )}

      {data && data.connected && !data.available && (
        <DegradedCard title={t('serverState.unavailable')} hint={t('serverState.unavailableHint')} detail={data.error} />
      )}

      {data && data.available && data.state && <StateSections state={data.state} />}
    </div>
  )
}
