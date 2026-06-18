import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronRight } from 'lucide-react'
import { toast } from 'sonner'
import {
  useBots,
  useBotSummary,
  useBotBatch,
  useSetBotBehavior,
  useDeleteBot,
  type BotInfo,
  type BotBatchAction,
  type BotBatchFilter,
} from '@/api/bots'
import {
  summaryCounts,
  groupBots,
  parseBotConfig,
  type BotGroupBy,
  type BotStatusKind,
} from './bot-list'
import BotStatusDot from './BotStatusDot'
import CreateBotDialog from './CreateBotDialog'
import ConfirmDialog from '@/components/ConfirmDialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { cn } from '@/lib/utils'

const PAGE_SIZE = 50

const BEHAVIOR_VALUES = ['idle', 'guard', 'follow', 'patrol'] as const
const STATUS_VALUES = ['connected', 'connecting', 'disconnected', 'error'] as const

/**
 * 控制台工作区的 Bot 段（FR-039）：聚合优先（概览卡片 + 筛选/分组 + 分页 + 批量），
 * 永不一次性铺开全部 Bot。概览计数来自 `GET /bots/summary`（全量聚合），
 * 列表分页拉 `GET /bots`，分组仅作用于当前页数据。
 */
interface BotSegmentProps {
  /** 当前工作区打开的实例 id */
  instanceId: number
}

export default function BotSegment({ instanceId }: BotSegmentProps) {
  const { t } = useTranslation()

  // 工具栏筛选状态（变更后回到第 1 页）
  const [q, setQ] = useState('')
  const [statusFilter, setStatusFilter] = useState('')
  const [behaviorFilter, setBehaviorFilter] = useState('')
  const [groupBy, setGroupBy] = useState<BotGroupBy>('behavior')
  const [page, setPage] = useState(1)

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

  const { data: summary } = useBotSummary({ instanceId })
  const counts = summaryCounts(summary)

  const { data: botList, isLoading } = useBots({ ...filter, page, pageSize: PAGE_SIZE })
  const bots = useMemo(() => botList?.items ?? [], [botList])
  const total = botList?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))

  const groups = useMemo(() => groupBots(bots, groupBy), [bots, groupBy])

  const resetPage = () => setPage(1)
  const clearSelection = () => setSelected(new Set())

  const toggleSelect = (id: number) =>
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  return (
    <div className="flex h-full flex-col">
      <div className="min-h-0 flex-1 space-y-4 overflow-auto p-4">
        {/* 概览卡片：总计 / 在线 / 连接中 / 异常（聚合，覆盖全量） */}
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <SummaryCard label={t('bots.summaryTotal')} value={counts.total} tone="neutral" />
          <SummaryCard label={t('bots.summaryOnline')} value={counts.online} tone="online" />
          <SummaryCard label={t('bots.summaryConnecting')} value={counts.connecting} tone="connecting" />
          <SummaryCard label={t('bots.summaryError')} value={counts.error} tone="error" />
        </div>

        {/* 工具栏：搜索 + 状态筛选 + 行为筛选 + 分组切换 + 新建 */}
        <div className="flex flex-wrap items-center gap-2">
          <Input
            value={q}
            onChange={(e) => {
              setQ(e.target.value)
              resetPage()
            }}
            placeholder={t('bots.searchPlaceholder')}
            className="h-9 w-44"
          />
          <Select
            value={statusFilter || 'all'}
            onValueChange={(v) => {
              setStatusFilter(v === 'all' ? '' : v)
              resetPage()
            }}
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
            onValueChange={(v) => {
              setBehaviorFilter(v === 'all' ? '' : v)
              resetPage()
            }}
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
          <Select value={groupBy} onValueChange={(v) => setGroupBy(v as BotGroupBy)}>
            <SelectTrigger className="h-9 w-36">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="behavior">{t('bots.groupByBehavior')}</SelectItem>
              <SelectItem value="status">{t('bots.groupByStatus')}</SelectItem>
            </SelectContent>
          </Select>
          <div className="ml-auto">
            <Button size="sm" onClick={() => setShowCreate(true)}>
              + {t('bots.createBot')}
            </Button>
          </div>
        </div>

        {/* 批量条：作用于当前筛选集或选中集 */}
        <BotBatchBar
          filter={filter}
          selectedIds={[...selected]}
          filteredTotal={total}
          onDone={clearSelection}
        />

        {/* 分组 + 分页列表 */}
        {isLoading ? (
          <p className="text-sm text-muted-foreground">{t('common.loading')}</p>
        ) : total === 0 ? (
          <p className="text-sm text-muted-foreground">{t('bots.empty')}</p>
        ) : (
          <div className="space-y-2">
            {groups.map((group) => (
              <BotGroupBlock
                key={group.key}
                groupBy={groupBy}
                groupKey={group.key}
                bots={group.bots}
                selected={selected}
                onToggleSelect={toggleSelect}
              />
            ))}
            <Pagination page={page} totalPages={totalPages} total={total} onChange={setPage} />
          </div>
        )}
      </div>

      <CreateBotDialog open={showCreate} onOpenChange={setShowCreate} instanceId={instanceId} />
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
}: {
  groupBy: BotGroupBy
  groupKey: string
  bots: BotInfo[]
  selected: Set<number>
  onToggleSelect: (id: number) => void
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(true)
  const batch = useBotBatch()

  const label =
    groupBy === 'status'
      ? t(`bots.statusKind.${groupKey as BotStatusKind}`)
      : t(`bots.${groupKey}`, { defaultValue: groupKey })

  const runGroupBehavior = (behavior: string) => {
    batch.mutate(
      { action: 'set-behavior', ids: bots.map((b) => b.id), behavior },
      {
        onSuccess: (r) => toast.success(t('bots.batchDone', { succeeded: r.succeeded, failed: r.failed })),
        onError: () => toast.error(t('bots.batchFailed')),
      },
    )
  }

  return (
    <div className="rounded-lg border">
      <div className="flex items-center gap-2 px-3 py-2">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          className="flex items-center gap-1.5 text-sm font-medium"
        >
          <ChevronRight className={cn('size-4 transition-transform', open && 'rotate-90')} />
          <span>{label}</span>
          <span className="text-xs text-muted-foreground">({bots.length})</span>
        </button>
        <div className="ml-auto">
          <Select onValueChange={runGroupBehavior}>
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
}: {
  bot: BotInfo
  checked: boolean
  onToggleSelect: () => void
}) {
  const { t } = useTranslation()
  const setBehavior = useSetBotBehavior()
  const del = useDeleteBot()
  const batch = useBotBatch()
  const [confirmDelete, setConfirmDelete] = useState(false)
  const config = parseBotConfig(bot.config)

  // 停止/重连复用批量端点的单条形式（FR-038 批量动作 stop/start）；「重连」语义即重新上线（start）
  const runAction = (action: 'stop' | 'start') => {
    batch.mutate(
      { action, ids: [bot.id] },
      {
        onSuccess: () => toast.success(t(action === 'stop' ? 'bots.stopDone' : 'bots.reconnectDone')),
        onError: () => toast.error(t(action === 'stop' ? 'bots.stopFailed' : 'bots.reconnectFailed')),
      },
    )
  }

  return (
    <li className="flex items-center gap-3 px-3 py-2 text-sm">
      <Checkbox checked={checked} onCheckedChange={onToggleSelect} aria-label={bot.name} />
      <BotStatusDot status={bot.status} />
      <span className="min-w-0 flex-1 truncate font-medium">{bot.name}</span>
      <span className="hidden truncate text-xs text-muted-foreground sm:inline">
        {config.server}:{config.port}
      </span>
      <Select
        value={bot.behavior}
        onValueChange={(value) => setBehavior.mutate({ id: bot.id, behavior: value })}
      >
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
      <Button variant="ghost" size="xs" onClick={() => runAction('start')} disabled={batch.isPending}>
        {t('bots.reconnect')}
      </Button>
      <Button variant="ghost" size="xs" onClick={() => runAction('stop')} disabled={batch.isPending}>
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
      <ConfirmDialog
        open={confirmDelete}
        title={t('bots.deleteConfirm')}
        description={t('common.irreversible')}
        confirmLabel={t('common.delete')}
        variant="destructive"
        onConfirm={() => {
          del.mutate(bot.id)
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
}: {
  filter: BotBatchFilter
  selectedIds: number[]
  filteredTotal: number
  onDone: () => void
}) {
  const { t } = useTranslation()
  const batch = useBotBatch()
  const [behavior, setBehavior] = useState('')
  const [confirm, setConfirm] = useState<BotBatchAction | null>(null)

  const useSelection = selectedIds.length > 0
  const scopeCount = useSelection ? selectedIds.length : filteredTotal

  const run = (action: BotBatchAction) => {
    const payload = useSelection
      ? { action, ids: selectedIds, ...(action === 'set-behavior' ? { behavior } : {}) }
      : { action, filter, ...(action === 'set-behavior' ? { behavior } : {}) }
    batch.mutate(payload, {
      onSuccess: (r) => {
        toast.success(t('bots.batchDone', { succeeded: r.succeeded, failed: r.failed }))
        onDone()
      },
      onError: () => toast.error(t('bots.batchFailed')),
    })
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
          disabled={!behavior || scopeCount === 0 || batch.isPending}
          onClick={() => run('set-behavior')}
        >
          {t('bots.batchSetBehavior')}
        </Button>
        <Button
          size="sm"
          variant="outline"
          disabled={scopeCount === 0 || batch.isPending}
          onClick={() => setConfirm('stop')}
        >
          {t('bots.batchStop')}
        </Button>
        <Button
          size="sm"
          variant="destructive"
          disabled={scopeCount === 0 || batch.isPending}
          onClick={() => setConfirm('delete')}
        >
          {t('bots.batchDelete')}
        </Button>
      </div>

      <ConfirmDialog
        open={confirm !== null}
        title={
          confirm === 'delete'
            ? t('bots.batchDeleteConfirm', { count: scopeCount })
            : t('bots.batchStopConfirm', { count: scopeCount })
        }
        description={confirm === 'delete' ? t('common.irreversible') : ''}
        confirmLabel={confirm === 'delete' ? t('common.delete') : t('bots.stop')}
        variant={confirm === 'delete' ? 'destructive' : 'default'}
        onConfirm={() => {
          if (confirm) run(confirm)
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
