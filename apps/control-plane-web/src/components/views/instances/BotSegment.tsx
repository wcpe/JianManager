import { useMemo, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Bot, ChevronRight, Gauge } from 'lucide-react'

import { Button } from '@jianmanager/ui/components/button'
import { EmptyState } from '@jianmanager/ui/components/empty-state'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import { cn } from '@jianmanager/ui'
import DangerConfirm from '@/components/views/DangerConfirm'
import BotStatusDot from '@/components/views/instances/BotStatusDot'
import type {
  BotBatchAction,
  BotBatchFilter,
  BotBatchRequest,
  BotBatchResult,
  BotInfo,
  BotSummary,
} from '@jianmanager/ui/lib/bot'
import {
  groupBots,
  parseBotConfig,
  summaryCounts,
  type BotGroupBy,
  type BotStatusKind,
} from '@jianmanager/ui/lib/bot-list'

/** 提示通道：视图算好文案交外壳展示（本包不弹 toast）。 */
export type BotNotice = (kind: 'success' | 'error', message: string) => void

const BEHAVIOR_VALUES = ['idle', 'guard', 'follow', 'patrol'] as const
const STATUS_VALUES = ['connected', 'connecting', 'disconnected', 'error'] as const

/**
 * 控制台工作区的 Bot 段（FR-039）：聚合优先（概览卡片 + 筛选/分组 + 分页 + 批量），
 * 永不一次性铺开全部 Bot。概览计数来自 `GET /bots/summary`（全量聚合），
 * 列表分页拉 `GET /bots`，分组仅作用于当前页数据。
 *
 * 布局为分栏（FR-423，spec §3.1 Bot 62:38）：左 62% 是 Bot 表（工具栏 + 批量条 + 分组列表 + 分页），
 * 右 38% 是运行概览（四档聚合计数）。原先四张概览卡横铺整宽压在列表上方，
 * 每张卡里一个数字撑着 68px、四张一起吃掉一整条横带，把真正要看的列表往下推。
 *
 * 注：spec 该行右栏还写了「压测」，但压测（bot-load）是 `/bots` 独立页的会话/模板能力，
 * 不在本页签内，故右栏只有运行概览。
 *
 * 受控视图（ADR-097 a 范式）：筛选、分页与三份数据都提到外壳——取数依赖它们，视图拿不到；
 * 创建对话框也由外壳注入（它自带接线层）。分组、勾选、二次确认留在视图内。
 */
export interface BotSegmentProps {
  /** 当前工作区打开的实例 id。 */
  instanceId: number
  /** 关键字搜索词。 */
  q: string
  onQChange: (q: string) => void
  /** 状态筛选（空串=不限）。 */
  statusFilter: string
  onStatusFilterChange: (s: string) => void
  /** 行为筛选（空串=不限）。 */
  behaviorFilter: string
  onBehaviorFilterChange: (b: string) => void
  /** 当前页（从 1 起）。 */
  page: number
  onPageChange: (page: number) => void
  /** 聚合计数（全量，不受分页影响）。 */
  summary?: BotSummary
  /** 当前页 Bot 列表。 */
  bots: BotInfo[]
  /** 筛选后的总数（分页用）。 */
  total: number
  /** 列表加载中。 */
  loading?: boolean
  /** 创建 Bot 对话框插槽（外壳用已迁包的接线层渲染）。 */
  createDialog: (args: { open: boolean; onOpenChange: (open: boolean) => void }) => ReactNode
  /** 批量操作。 */
  onBatch: (payload: BotBatchRequest) => Promise<BotBatchResult>
  /** 单条改行为。 */
  onSetBehavior: (payload: { id: number; behavior: string }) => Promise<void>
  /** 删除单条。 */
  onDeleteBot: (id: number) => Promise<void>
  /** 提示通道。 */
  notify: BotNotice
}

const PAGE_SIZE = 50

export default function BotSegment({
  instanceId,
  q,
  onQChange,
  statusFilter,
  onStatusFilterChange,
  behaviorFilter,
  onBehaviorFilterChange,
  page,
  onPageChange,
  summary,
  bots,
  total,
  loading = false,
  createDialog,
  onBatch,
  onSetBehavior,
  onDeleteBot,
  notify,
}: BotSegmentProps) {
  const { t } = useTranslation()

  const [groupBy, setGroupBy] = useState<BotGroupBy>('behavior')
  // 行内选择 + 新建对话框
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [showCreate, setShowCreate] = useState(false)

  // 列表筛选维度（与摘要/批量共用），空串表示不限
  const filter: BotBatchFilter = {
    instanceId,
    ...(statusFilter ? { status: statusFilter } : {}),
    ...(behaviorFilter ? { behavior: behaviorFilter } : {}),
    ...(q.trim() ? { q: q.trim() } : {}),
  }

  const counts = summaryCounts(summary)
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const groups = useMemo(() => groupBots(bots, groupBy), [bots, groupBy])

  const toggleSelect = (id: number) =>
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2 p-4 xl:flex-row">
      {/* 左栏 62%：Bot 表。新建按钮上提到卡头 actions——列表滚动时它仍常驻可点。 */}
      <div className="flex flex-none flex-col xl:min-h-0 xl:flex-[1.6]">
        {/* 内容驱动 + 栏高封顶（spec §3.2）：Bot 多时撑到栏高上限后列表内部滚，少时贴合内容。
            不写死 flex-1——否则 3 个 Bot 也把卡撑满整栏，正是本批要消灭的「矮内容被拉平成死区」。 */}
        <Panel
          className="max-h-full min-h-0 flex-none"
          bodyClassName="flex min-h-0 flex-col gap-3 overflow-hidden p-3"
          icon={<Bot className="size-3.5" />}
          title={t('bots.title')}
          actions={
            <Button size="sm" onClick={() => setShowCreate(true)}>
              + {t('bots.createBot')}
            </Button>
          }
        >
          {/* 工具栏：搜索 + 状态筛选 + 行为筛选 + 分组切换 */}
          <div className="flex flex-none flex-wrap items-center gap-2">
            <Input
              value={q}
              onChange={(e) => onQChange(e.target.value)}
              placeholder={t('bots.searchPlaceholder')}
              className="h-9 w-44"
            />
            <Select
              value={statusFilter || 'all'}
              onValueChange={(v: string) => onStatusFilterChange(v === 'all' ? '' : v)}
            >
              <SelectTrigger className="h-9 w-32">
                <SelectValue placeholder={t('bots.status')} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">{t('bots.allStatus')}</SelectItem>
                {STATUS_VALUES.map((s) => (
                  <SelectItem key={s} value={s}>
                    {t(`bots.${s}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select
              value={behaviorFilter || 'all'}
              onValueChange={(v: string) => onBehaviorFilterChange(v === 'all' ? '' : v)}
            >
              <SelectTrigger className="h-9 w-32">
                <SelectValue placeholder={t('bots.behavior')} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">{t('bots.allBehavior')}</SelectItem>
                {BEHAVIOR_VALUES.map((b) => (
                  <SelectItem key={b} value={b}>
                    {t(`bots.${b}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select value={groupBy} onValueChange={(v: string) => setGroupBy(v as BotGroupBy)}>
              <SelectTrigger className="h-9 w-36">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="behavior">{t('bots.groupByBehavior')}</SelectItem>
                <SelectItem value="status">{t('bots.groupByStatus')}</SelectItem>
              </SelectContent>
            </Select>
          </div>

          {/* 批量条：作用于当前筛选集或选中集（flex-none：与工具栏同属常驻控制区，不随列表滚走） */}
          <div className="flex-none">
            <BotBatchBar
              filter={filter}
              selectedIds={[...selected]}
              filteredTotal={total}
              onDone={() => setSelected(new Set())}
              onBatch={onBatch}
              notify={notify}
            />
          </div>

          {/* 分组 + 分页列表：本卡唯一的滚动容器 */}
          {loading ? (
            <p className="text-sm text-muted-foreground">{t('common.loading')}</p>
          ) : total === 0 ? (
            <EmptyState icon={<Bot />} title={t('bots.empty')} />
          ) : (
            <div className="min-h-0 space-y-2 overflow-auto">
              {groups.map((group) => (
                <BotGroupBlock
                  key={group.key}
                  groupBy={groupBy}
                  groupKey={group.key}
                  bots={group.bots}
                  selected={selected}
                  onToggleSelect={toggleSelect}
                  onBatch={onBatch}
                  onSetBehavior={onSetBehavior}
                  onDeleteBot={onDeleteBot}
                  notify={notify}
                />
              ))}
              <Pagination page={page} totalPages={totalPages} total={total} onChange={onPageChange} />
            </div>
          )}
        </Panel>
      </div>

      {/* 右栏 38%：运行概览（聚合计数覆盖全量，不受分页/分组影响）。
          xl 以下退成整页纵向堆叠，权重只在 xl 生效——窄屏若仍按权重分高度，
          两栏会互相挤压，页面该滚的时候滚不动。 */}
      <div className="flex flex-none flex-col gap-2 xl:min-h-0 xl:flex-1">
        {/* 内容驱动高度：四个数字就是全部内容，撑高只会在卡内留死区（spec §3.2）。 */}
        <Panel
          className="flex-none"
          icon={<Gauge className="size-3.5" />}
          title={t('bots.runtimeOverview')}
        >
          <div className="grid grid-cols-2 gap-2">
            <SummaryCard label={t('bots.summaryTotal')} value={counts.total} tone="neutral" />
            <SummaryCard label={t('bots.summaryOnline')} value={counts.online} tone="online" />
            <SummaryCard label={t('bots.summaryConnecting')} value={counts.connecting} tone="connecting" />
            <SummaryCard label={t('bots.summaryError')} value={counts.error} tone="error" />
          </div>
        </Panel>
      </div>

      {createDialog({ open: showCreate, onOpenChange: setShowCreate })}
    </div>
  )
}

const TONE_CLASS: Record<string, string> = {
  neutral: 'text-foreground',
  online: 'text-green-500',
  connecting: 'text-amber-500',
  error: 'text-red-500',
}

/** 单张概览卡片。 */
function SummaryCard({
  label,
  value,
  tone,
}: {
  label: string
  value: number
  tone: 'neutral' | 'online' | 'connecting' | 'error'
}) {
  return (
    <div className="rounded-lg border bg-card px-4 py-3">
      <p className="text-xs text-muted-foreground">{label}</p>
      <p className={cn('mt-1 text-2xl font-semibold tabular-nums', TONE_CLASS[tone])}>{value}</p>
    </div>
  )
}

/** 分组块：可折叠表头（组名 + 计数 + 整组批量设行为）+ 展开后的成员行。 */
function BotGroupBlock({
  groupBy,
  groupKey,
  bots,
  selected,
  onToggleSelect,
  onBatch,
  onSetBehavior,
  onDeleteBot,
  notify,
}: {
  groupBy: BotGroupBy
  groupKey: string
  bots: BotInfo[]
  selected: Set<number>
  onToggleSelect: (id: number) => void
  onBatch: BotSegmentProps['onBatch']
  onSetBehavior: BotSegmentProps['onSetBehavior']
  onDeleteBot: BotSegmentProps['onDeleteBot']
  notify: BotNotice
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(true)
  const [pending, setPending] = useState(false)

  const label =
    groupBy === 'status'
      ? t(`bots.statusKind.${groupKey as BotStatusKind}`)
      : t(`bots.${groupKey}`, { defaultValue: groupKey })

  const runGroupBehavior = async (behavior: string) => {
    setPending(true)
    try {
      const r = await onBatch({ action: 'set-behavior', ids: bots.map((b) => b.id), behavior })
      notify('success', t('bots.batchDone', { succeeded: r.succeeded, failed: r.failed }))
    } catch {
      notify('error', t('bots.batchFailed'))
    } finally {
      setPending(false)
    }
  }

  return (
    <div className="rounded-lg border">
      <div className="flex items-center gap-2 px-3 py-2">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          // 标题基色已继承卡片前景色（WorkspaceCard 的 text-card-foreground 与 foreground 同值），
          // hover:text-foreground 不会产生可见变化，故按同卡族可点文本的既有写法取 primary。
          className="flex items-center gap-1.5 text-sm font-medium hover:text-primary"
        >
          <ChevronRight className={cn('size-4 transition-transform', open && 'rotate-90')} />
          <span>{label}</span>
          <span className="text-xs text-muted-foreground">({bots.length})</span>
        </button>
        <div className="ml-auto">
          <Select onValueChange={(v: string) => void runGroupBehavior(v)} disabled={pending}>
            <SelectTrigger className="h-7 w-32 text-xs">
              <SelectValue placeholder={t('bots.setGroupBehavior')} />
            </SelectTrigger>
            <SelectContent>
              {BEHAVIOR_VALUES.map((b) => (
                <SelectItem key={b} value={b}>
                  {t(`bots.${b}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>
      {open && (
        <ul className="divide-y border-t">
          {bots.map((bot) => (
            <BotRow
              key={bot.id}
              bot={bot}
              checked={selected.has(bot.id)}
              onToggleSelect={() => onToggleSelect(bot.id)}
              onBatch={onBatch}
              onSetBehavior={onSetBehavior}
              onDeleteBot={onDeleteBot}
              notify={notify}
            />
          ))}
        </ul>
      )}
    </div>
  )
}

/** 单 Bot 行：选择框 + 状态点 + 名称 + 地址 + 行内改行为 + 停止 + 删除。 */
function BotRow({
  bot,
  checked,
  onToggleSelect,
  onBatch,
  onSetBehavior,
  onDeleteBot,
  notify,
}: {
  bot: BotInfo
  checked: boolean
  onToggleSelect: () => void
  onBatch: BotSegmentProps['onBatch']
  onSetBehavior: BotSegmentProps['onSetBehavior']
  onDeleteBot: BotSegmentProps['onDeleteBot']
  notify: BotNotice
}) {
  const { t } = useTranslation()
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [pending, setPending] = useState(false)
  const config = parseBotConfig(bot.config)

  // 停止/重连复用批量端点的单条形式（FR-038 批量动作 stop/start）；「重连」语义即重新上线（start）
  const runAction = async (action: 'stop' | 'start') => {
    setPending(true)
    try {
      await onBatch({ action, ids: [bot.id] })
      notify('success', t(action === 'stop' ? 'bots.stopDone' : 'bots.reconnectDone'))
    } catch {
      notify('error', t(action === 'stop' ? 'bots.stopFailed' : 'bots.reconnectFailed'))
    } finally {
      setPending(false)
    }
  }

  const changeBehavior = async (value: string) => {
    setPending(true)
    try {
      await onSetBehavior({ id: bot.id, behavior: value })
    } catch {
      notify('error', t('common.error'))
    } finally {
      setPending(false)
    }
  }

  return (
    <li className="flex items-center gap-3 px-3 py-2 text-sm">
      <Checkbox checked={checked} onCheckedChange={onToggleSelect} aria-label={bot.name} />
      <BotStatusDot status={bot.status} />
      <span className="min-w-0 flex-1 truncate font-medium" title={bot.lastError || undefined}>
        {bot.name}
        {bot.status === 'error' && bot.lastError && (
          <span className="ml-2 truncate text-xs font-normal text-destructive">{bot.lastError}</span>
        )}
      </span>
      <span className="hidden truncate text-xs text-muted-foreground sm:inline">
        {config.server}:{config.port}
      </span>
      <Select value={bot.behavior} onValueChange={(v: string) => void changeBehavior(v)} disabled={pending}>
        <SelectTrigger className="h-7 w-24 text-xs">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {BEHAVIOR_VALUES.map((b) => (
            <SelectItem key={b} value={b}>
              {t(`bots.${b}`)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Button variant="ghost" size="xs" onClick={() => void runAction('start')} disabled={pending}>
        {t('bots.reconnect')}
      </Button>
      <Button variant="ghost" size="xs" onClick={() => void runAction('stop')} disabled={pending}>
        {t('bots.stop')}
      </Button>
      <Button
        variant="ghost"
        size="xs"
        onClick={() => setConfirmDelete(true)}
        className="text-red-600 hover:text-red-700"
      >
        {t('common.delete')}
      </Button>
      <DangerConfirm
        open={confirmDelete}
        title={t('bots.deleteConfirm')}
        description={t('common.irreversible')}
        confirmLabel={t('common.delete')}
        scope="group"
        onConfirm={() => {
          void onDeleteBot(bot.id)
          setConfirmDelete(false)
        }}
        onCancel={() => setConfirmDelete(false)}
      />
    </li>
  )
}

/**
 * 批量条：对「当前筛选集」（按 filter，覆盖所有分页）或「选中集」（按 ids）执行批量操作。
 * 有勾选时优先作用于选中集，否则作用于当前筛选集，并提示影响范围。
 */
function BotBatchBar({
  filter,
  selectedIds,
  filteredTotal,
  onDone,
  onBatch,
  notify,
}: {
  filter: BotBatchFilter
  selectedIds: number[]
  filteredTotal: number
  onDone: () => void
  onBatch: BotSegmentProps['onBatch']
  notify: BotNotice
}) {
  const { t } = useTranslation()
  const [behavior, setBehavior] = useState('')
  const [confirm, setConfirm] = useState<BotBatchAction | null>(null)
  const [pending, setPending] = useState(false)

  const useSelection = selectedIds.length > 0
  const scopeCount = useSelection ? selectedIds.length : filteredTotal

  const run = async (action: BotBatchAction) => {
    const payload: BotBatchRequest = useSelection
      ? { action, ids: selectedIds, ...(action === 'set-behavior' ? { behavior } : {}) }
      : { action, filter, ...(action === 'set-behavior' ? { behavior } : {}) }
    setPending(true)
    try {
      const r = await onBatch(payload)
      notify('success', t('bots.batchDone', { succeeded: r.succeeded, failed: r.failed }))
      onDone()
    } catch {
      notify('error', t('bots.batchFailed'))
    } finally {
      setPending(false)
    }
  }

  return (
    <div className="flex flex-wrap items-center gap-2 rounded-lg border bg-muted/40 px-3 py-2">
      <span className="text-xs text-muted-foreground">
        {useSelection
          ? t('bots.batchScopeSelected', { count: scopeCount })
          : t('bots.batchScopeFiltered', { count: scopeCount })}
      </span>
      <div className="ml-auto flex flex-wrap items-center gap-2">
        <Select value={behavior} onValueChange={setBehavior}>
          <SelectTrigger className="h-8 w-28 text-xs">
            <SelectValue placeholder={t('bots.behavior')} />
          </SelectTrigger>
          <SelectContent>
            {BEHAVIOR_VALUES.map((b) => (
              <SelectItem key={b} value={b}>
                {t(`bots.${b}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button
          size="sm"
          variant="outline"
          disabled={!behavior || scopeCount === 0 || pending}
          onClick={() => void run('set-behavior')}
        >
          {t('bots.batchSetBehavior')}
        </Button>
        <Button
          size="sm"
          variant="outline"
          disabled={scopeCount === 0 || pending}
          onClick={() => setConfirm('stop')}
        >
          {t('bots.batchStop')}
        </Button>
        <Button
          size="sm"
          variant="destructive"
          disabled={scopeCount === 0 || pending}
          onClick={() => setConfirm('delete')}
        >
          {t('bots.batchDelete')}
        </Button>
      </div>

      <DangerConfirm
        open={confirm !== null}
        title={
          confirm === 'delete'
            ? t('bots.batchDeleteConfirm', { count: scopeCount })
            : t('bots.batchStopConfirm', { count: scopeCount })
        }
        description={confirm === 'delete' ? t('common.irreversible') : ''}
        confirmLabel={confirm === 'delete' ? t('common.delete') : t('bots.stop')}
        scope="group"
        onConfirm={() => {
          if (confirm) void run(confirm)
          setConfirm(null)
        }}
        onCancel={() => setConfirm(null)}
      />
    </div>
  )
}

/** 分页器：上一页/下一页 + 当前页/总页数 + 总数。 */
function Pagination({
  page,
  totalPages,
  total,
  onChange,
}: {
  page: number
  totalPages: number
  total: number
  onChange: (page: number) => void
}) {
  const { t } = useTranslation()
  if (totalPages <= 1) {
    return <p className="px-1 pt-1 text-xs text-muted-foreground">{t('bots.totalCount', { count: total })}</p>
  }
  return (
    <div className="flex items-center justify-between px-1 pt-1">
      <span className="text-xs text-muted-foreground">{t('bots.totalCount', { count: total })}</span>
      <div className="flex items-center gap-2">
        <Button size="xs" variant="outline" disabled={page <= 1} onClick={() => onChange(page - 1)}>
          {t('bots.prevPage')}
        </Button>
        <span className="text-xs text-muted-foreground">
          {t('bots.pageOf', { page, totalPages })}
        </span>
        <Button
          size="xs"
          variant="outline"
          disabled={page >= totalPages}
          onClick={() => onChange(page + 1)}
        >
          {t('bots.nextPage')}
        </Button>
      </div>
    </div>
  )
}
