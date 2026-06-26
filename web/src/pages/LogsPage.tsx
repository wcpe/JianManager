import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Download, Radio } from 'lucide-react'
import { useLogs, exportLogs, type LogQueryParams } from '@/api/logs'
import { useNodes } from '@/api/nodes'
import { useInstances } from '@/api/instances'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Panel } from '@/components/ui/panel'
import { StatusBadge } from '@/components/ui/status-badge'
import { cn } from '@/lib/utils'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  logLevelStatus,
  timeRangeToParams,
  buildExportParams,
  computeVirtualWindow,
  TIME_RANGE_PRESETS,
  type TimeRangePreset,
  type LogExportScope,
} from './logs-filters'

// Radix Select 不允许空字符串值，用哨兵代表「全部」。
const SENTINEL_ALL = '__all__'
const PAGE_SIZE = 100
const SOURCES = ['instance', 'control_plane', 'worker']
const LEVELS = ['error', 'warn', 'info', 'debug']
const EXPORT_SCOPES: LogExportScope[] = ['currentPage', 'allMatched', 'range']

// 虚拟滚动：固定行高 + 视口上下各预渲染 8 行。
const ROW_HEIGHT = 30
const OVERSCAN = 8
const VIEWPORT_HEIGHT = 460
// 实时跟随轮询间隔（ms）。
const FOLLOW_INTERVAL = 3000

/**
 * 日志中心（FR-049 查看 / FR-050 检索 / FR-150 增强）。
 * 套「流水检索」范式：强筛选（级别 pill + 来源/节点/实例/时间范围 + 关键字）→ 时间线行（虚拟滚动）；
 * 「实时跟随」开关锁定首页并按间隔轮询（tail）；导出可选范围（当前页/全部匹配/时间段）。
 * 级别用 StatusBadge 着色，token 驱动，与告警页语义统一。
 */
export default function LogsPage() {
  const { t } = useTranslation()
  const { data: nodes } = useNodes()
  const { data: instances } = useInstances()

  const [source, setSource] = useState('')
  const [level, setLevel] = useState('')
  const [nodeId, setNodeId] = useState<number | null>(null)
  const [instanceId, setInstanceId] = useState<number | null>(null)
  const [keyword, setKeyword] = useState('')
  const [range, setRange] = useState<TimeRangePreset>('all')
  const [page, setPage] = useState(1)
  const [follow, setFollow] = useState(false)
  const [exporting, setExporting] = useState(false)

  // 跟随态钉在第 1 页（最新）；锚点 now 在每次构建参数时取，配合轮询滚动时间窗。
  const timeParams = timeRangeToParams(range, new Date())
  const params: LogQueryParams = {
    page: follow ? 1 : page,
    pageSize: PAGE_SIZE,
    ...(source ? { source } : {}),
    ...(level ? { level } : {}),
    ...(nodeId !== null ? { nodeId } : {}),
    ...(instanceId !== null ? { instanceId } : {}),
    ...(keyword.trim() ? { keyword: keyword.trim() } : {}),
    ...timeParams,
  }

  const { data, isLoading, isError } = useLogs(params, {
    refetchInterval: follow ? FOLLOW_INTERVAL : false,
  })

  const items = data?.items ?? []
  const total = data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))

  // 改任一筛选都回到第 1 页，避免停留在越界页。
  const resetTo = useCallback(
    <T,>(setter: (v: T) => void) =>
      (v: T) => {
        setter(v)
        setPage(1)
      },
    [],
  )

  const handleExport = async (scope: LogExportScope) => {
    setExporting(true)
    try {
      await exportLogs(buildExportParams(params, scope))
      toast.success(t('logs.exportStarted'))
    } catch {
      toast.error(t('logs.exportFailed'))
    } finally {
      setExporting(false)
    }
  }

  return (
    <div className="flex h-full min-h-0 flex-col gap-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">{t('logs.title')}</h1>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="outline" size="sm" disabled={exporting || total === 0}>
              <Download className="size-3.5" />
              {exporting ? t('logs.exporting') : t('logs.export')}
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            {EXPORT_SCOPES.map((scope) => (
              <DropdownMenuItem
                key={scope}
                disabled={scope === 'range' && range === 'all'}
                onSelect={() => handleExport(scope)}
              >
                {t(`logs.exportScope_${scope}`)}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      {/* 强筛选工具栏 */}
      <div className="flex flex-wrap items-center gap-2">
        {/* 级别快速 pill（全部 + 四级） */}
        <div className="flex items-center gap-1">
          <LevelPill active={level === ''} onClick={() => resetTo(setLevel)('')}>
            {t('logs.allLevels')}
          </LevelPill>
          {LEVELS.map((l) => (
            <LevelPill
              key={l}
              level={l}
              active={level === l}
              onClick={() => resetTo(setLevel)(level === l ? '' : l)}
            >
              {t(`logs.level_${l}`)}
            </LevelPill>
          ))}
        </div>

        <Input
          value={keyword}
          onChange={(e) => resetTo(setKeyword)(e.target.value)}
          placeholder={t('logs.searchPlaceholder')}
          className="h-9 w-52"
        />
        <Select
          value={range}
          onValueChange={(v: string) => resetTo(setRange)(v as TimeRangePreset)}
        >
          <SelectTrigger size="sm" className="w-36">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {TIME_RANGE_PRESETS.map((r) => (
              <SelectItem key={r} value={r}>
                {t(`logs.range_${r}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={source === '' ? SENTINEL_ALL : source}
          onValueChange={(v: string) => resetTo(setSource)(v === SENTINEL_ALL ? '' : v)}
        >
          <SelectTrigger size="sm" className="w-32">
            <SelectValue placeholder={t('logs.allSources')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={SENTINEL_ALL}>{t('logs.allSources')}</SelectItem>
            {SOURCES.map((s) => (
              <SelectItem key={s} value={s}>
                {t(`logs.source_${s}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={nodeId === null ? SENTINEL_ALL : String(nodeId)}
          onValueChange={(v: string) => resetTo(setNodeId)(v === SENTINEL_ALL ? null : Number(v))}
        >
          <SelectTrigger size="sm" className="w-36">
            <SelectValue placeholder={t('logs.allNodes')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={SENTINEL_ALL}>{t('logs.allNodes')}</SelectItem>
            {nodes?.map((node) => (
              <SelectItem key={node.id} value={String(node.id)}>
                {node.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={instanceId === null ? SENTINEL_ALL : String(instanceId)}
          onValueChange={(v: string) =>
            resetTo(setInstanceId)(v === SENTINEL_ALL ? null : Number(v))
          }
        >
          <SelectTrigger size="sm" className="w-44">
            <SelectValue placeholder={t('logs.allInstances')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={SENTINEL_ALL}>{t('logs.allInstances')}</SelectItem>
            {instances?.map((inst) => (
              <SelectItem key={inst.id} value={String(inst.id)}>
                {inst.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        {/* 实时跟随开关 pill，靠右 */}
        <button
          type="button"
          aria-pressed={follow}
          onClick={() => setFollow((f) => !f)}
          className={cn(
            'ml-auto inline-flex items-center gap-1.5 rounded-full px-3 py-1.5 text-xs font-medium transition-colors duration-200 ease-ios',
            follow
              ? 'bg-status-success/15 text-status-success'
              : 'bg-muted text-muted-foreground hover:bg-accent',
          )}
        >
          <Radio className={cn('size-3.5', follow && 'animate-pulse')} />
          {t('logs.follow')}
        </button>
      </div>

      {isLoading && !data ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : isError ? (
        <p className="text-destructive">{t('logs.loadError')}</p>
      ) : (
        <Panel
          className="min-h-0 flex-1"
          bodyClassName="flex min-h-0 flex-col p-0"
        >
          <LogTimeline items={items} follow={follow} />
          <LogFooter
            follow={follow}
            total={total}
            page={follow ? 1 : page}
            totalPages={totalPages}
            onPrev={() => setPage((p) => Math.max(1, p - 1))}
            onNext={() => setPage((p) => Math.min(totalPages, p + 1))}
          />
        </Panel>
      )}
    </div>
  )
}

/** 级别快速筛选 pill：选中态主色淡染，非选中态弱色；带级别时前导状态色点。 */
function LevelPill({
  level,
  active,
  onClick,
  children,
}: {
  level?: string
  active: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  const status = level ? logLevelStatus(level) : null
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={cn(
        'inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium transition-colors duration-200 ease-ios',
        active
          ? 'bg-primary/10 text-primary'
          : 'text-muted-foreground hover:bg-accent hover:text-foreground',
      )}
    >
      {status && (
        <span
          className={cn('size-1.5 rounded-full', {
            'bg-status-danger': status === 'danger',
            'bg-status-warning': status === 'warning',
            'bg-status-info': status === 'info',
            'bg-muted-foreground': status === 'neutral',
          })}
        />
      )}
      {children}
    </button>
  )
}

/**
 * 日志时间线（虚拟滚动）：固定行高，仅渲染视口附近的窗口，千行级日志不一次性入 DOM。
 * 跟随态下每次数据更新自动滚到顶部（最新在前）。
 */
function LogTimeline({
  items,
  follow,
}: {
  items: import('@/api/logs').LogEntry[]
  follow: boolean
}) {
  const { t } = useTranslation()
  const containerRef = useRef<HTMLDivElement>(null)
  const [scrollTop, setScrollTop] = useState(0)

  // 跟随态：新数据到来时滚回顶部，保证最新行可见。
  useEffect(() => {
    if (follow && containerRef.current) {
      containerRef.current.scrollTop = 0
      setScrollTop(0)
    }
  }, [follow, items])

  const win = computeVirtualWindow({
    scrollTop,
    viewportHeight: VIEWPORT_HEIGHT,
    rowHeight: ROW_HEIGHT,
    total: items.length,
    overscan: OVERSCAN,
  })
  const visible = items.slice(win.startIndex, win.endIndex)

  if (items.length === 0) {
    return (
      <div
        className="flex items-center justify-center text-sm text-muted-foreground"
        style={{ height: VIEWPORT_HEIGHT }}
      >
        {t('logs.empty')}
      </div>
    )
  }

  return (
    <div
      ref={containerRef}
      onScroll={(e) => setScrollTop(e.currentTarget.scrollTop)}
      className="min-h-0 flex-1 overflow-y-auto"
      style={{ maxHeight: VIEWPORT_HEIGHT }}
    >
      <div style={{ height: win.padTop }} />
      {visible.map((log) => (
        <LogRow key={log.id} log={log} />
      ))}
      <div style={{ height: win.padBottom }} />
    </div>
  )
}

/** 单条日志行（固定高度，等宽内容）：时间 + 级别徽标 + 来源 + 消息，hover 行高亮。 */
function LogRow({ log }: { log: import('@/api/logs').LogEntry }) {
  const { t } = useTranslation()
  return (
    <div
      className="flex items-center gap-3 border-b border-border/60 px-3 text-xs transition-colors hover:bg-accent/50"
      style={{ height: ROW_HEIGHT }}
    >
      <span className="w-40 shrink-0 font-mono text-[11px] text-muted-foreground">
        {new Date(log.time).toLocaleString()}
      </span>
      <StatusBadge
        level={logLevelStatus(log.level)}
        label={t(`logs.level_${log.level}`, log.level)}
        className="w-16 shrink-0 justify-center"
      />
      <span className="w-14 shrink-0 text-[11px] text-muted-foreground">
        {t(`logs.source_${log.source}`, log.source)}
      </span>
      <span className="min-w-0 flex-1 truncate font-mono text-[11px]" title={log.message}>
        {log.message}
      </span>
    </div>
  )
}

/** 时间线底部条：跟随态显实时指示；否则显分页。 */
function LogFooter({
  follow,
  total,
  page,
  totalPages,
  onPrev,
  onNext,
}: {
  follow: boolean
  total: number
  page: number
  totalPages: number
  onPrev: () => void
  onNext: () => void
}) {
  const { t } = useTranslation()
  if (follow) {
    return (
      <div className="flex shrink-0 items-center gap-2 border-t px-3 py-2 text-[11px] text-status-success">
        <span className="size-1.5 animate-pulse rounded-full bg-status-success" />
        {t('logs.followingHint')}
        <span className="ml-auto text-muted-foreground">{t('logs.totalCount', { count: total })}</span>
      </div>
    )
  }
  return (
    <div className="flex shrink-0 items-center justify-between border-t px-3 py-2 text-sm text-muted-foreground">
      <span>{t('logs.totalCount', { count: total })}</span>
      <div className="flex items-center gap-2">
        <Button variant="outline" size="sm" disabled={page <= 1} onClick={onPrev}>
          {t('logs.prevPage')}
        </Button>
        <span>{t('logs.pageInfo', { page, totalPages })}</span>
        <Button variant="outline" size="sm" disabled={page >= totalPages} onClick={onNext}>
          {t('logs.nextPage')}
        </Button>
      </div>
    </div>
  )
}
