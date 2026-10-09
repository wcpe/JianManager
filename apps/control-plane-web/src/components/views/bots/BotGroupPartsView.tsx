/**
 * @file BotGroupPartsView：Bot 舰队页分组总览的展示件——分组操作区（在控制台打开 + 单组批量菜单）、
 *       分组行（勾选 + 可展开标签 + 健康条 + 总数，行下挂成员窥视）与成员窥视（分页列表 + 翻页器）。
 *       展示件不取数、不发请求、不弹 toast；分组批量与窥视成员数据分别经回调与 props 注入。
 * @input lib/bot（BotInfo/BotSummaryGroup/BotBatchAction）、lib/bots-overview（GroupByDim）、
 *        views/bots/BotListParts（PeekRow）、views/console/BotHealthBar、
 *        Button/Checkbox/Select/Table 原语、翻译上下文
 * @output BotGroupActions、BotGroupActionsProps、BotGroupRow、BotGroupRowProps、BotGroupPeek、
 *         BotGroupPeekProps、BOT_PEEK_PAGE_SIZE
 * @sync apps/control-plane-web/src/pages/BotsPage.tsx（容器保留 useBotBatch/useBots 取数与路由装配）
 * @since FR-502（组件受控化迁包；原页 FR-040 全局 Bot 页、FR-147 分组总览与单组批量、FR-371 会话与模板页签）
 */
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@jianmanager/ui/components/button'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import { TableCell, TableRow } from '@jianmanager/ui/components/table'
import { BotHealthBar } from '@/components/views/console/BotHealthBar'
import { PeekRow } from '@/components/views/bots/BotListParts'
import type { BotBatchAction, BotInfo, BotSummaryGroup } from '@/lib/bots/bot'
import type { GroupByDim } from '@/lib/bots/bots-overview'

/**
 * 窥视列表的固定页大小（FR-147）。
 *
 * 一处导出供两处共用：容器按它请求（`useBots({ page, pageSize })`），本文件按它算总页数。
 * 若各写一份，改页大小时会静默错位——翻页器仍按旧值分页，请求却已换档。
 */
export const BOT_PEEK_PAGE_SIZE = 10

/** 单组批量菜单可下发的行为集合（与后端 behavior 枚举对齐）。 */
const BEHAVIOR_OPTIONS = ['idle', 'guard', 'follow', 'patrol'] as const

/**
 * 分组操作区（卡片/行复用）：在控制台打开（仅实例维度）+ 单组批量菜单。
 *
 * 受控边界（ADR-097 b 范式）：不取数、不弹 toast。批量下发经 `onRunBatch` 上报（容器接
 * `useBotBatch` 并弹提示），在途态经 `pending` 注入；「在控制台打开」只上报意图，
 * 路由跳转由容器注入（包内不依赖 react-router）。
 */
export interface BotGroupActionsProps {
  /** 分组维度：仅 instance 维度给「在控制台打开」入口（其余维度没有对应的实例深链）。 */
  groupBy: GroupByDim
  /** 在控制台打开该组实例页；仅 instance 维度会渲染该按钮。 */
  onOpenInConsole?: () => void
  /** 单组批量在途：禁用下拉，避免连点重复下发。 */
  pending?: boolean
  /** 单组批量下发（容器接 mutation 与结果提示）；`behavior` 仅在设行为动作时传入。 */
  onRunBatch: (action: BotBatchAction, behavior?: string) => void
}

export function BotGroupActions({ groupBy, onOpenInConsole, pending = false, onRunBatch }: BotGroupActionsProps) {
  const { t } = useTranslation()
  return (
    <>
      {groupBy === 'instance' && (
        <Button variant="ghost" size="xs" onClick={onOpenInConsole}>
          {t('bots.openInConsole')}
        </Button>
      )}
      <BotGroupBatchMenu pending={pending} onRunBatch={onRunBatch} />
    </>
  )
}

/** 每组批量操作菜单：设行为 / 停止 / 删除（目标 = 该组筛选，由容器按下发的分组装配）。 */
function BotGroupBatchMenu({
  pending,
  onRunBatch,
}: {
  /** 批量在途：禁用下拉。 */
  pending: boolean
  /** 上报所选动作；选项值 `behavior:<name>` 表示「设为该行为」，其余为直接动作。 */
  onRunBatch: (action: BotBatchAction, behavior?: string) => void
}) {
  const { t } = useTranslation()
  return (
    <Select
      value=""
      onValueChange={(v: string) => {
        if (v.startsWith('behavior:')) onRunBatch('set-behavior', v.slice('behavior:'.length))
        else onRunBatch(v as BotBatchAction)
      }}
    >
      <SelectTrigger size="sm" className="w-28" disabled={pending}>
        <SelectValue placeholder={t('bots.batch')} />
      </SelectTrigger>
      <SelectContent>
        {BEHAVIOR_OPTIONS.map((b) => (
          <SelectItem key={b} value={`behavior:${b}`}>
            {t('bots.setBehaviorTo', { behavior: t(`bots.${b}`) })}
          </SelectItem>
        ))}
        <SelectItem value="stop">{t('bots.batchStop')}</SelectItem>
        <SelectItem value="delete">{t('bots.batchDelete')}</SelectItem>
      </SelectContent>
    </Select>
  )
}

/**
 * 单个分组行（列表视图）：勾选 + 标签（可展开）+ 健康条 + 总数 + 操作区；展开时在行下追加成员窥视。
 *
 * 受控边界：勾选与展开态由 `BotGroupOverview` 持有后传入（与原页一致，二者都是纯 UI 状态）；
 * 操作区与窥视都是**插槽**——前者接批量 mutation 与路由跳转，后者接 `useBots` 取数容器，
 * 两者均属应用侧装配。窥视插槽按函数传入且仅在展开时调用，故折叠行不会建窥视元素、也不会取数。
 */
export interface BotGroupRowProps {
  /** 本行对应的分组（键 / 展示名 / 在线数 / 总数）。 */
  group: BotSummaryGroup
  /** 本行是否被勾选（批量选择的成员）。 */
  checked: boolean
  /** 切换本行勾选。 */
  onCheck: () => void
  /** 本行是否展开窥视。 */
  expanded: boolean
  /** 展开/收起本行窥视。 */
  onToggleExpand: () => void
  /** 行尾操作区插槽（在控制台打开 + 单组批量菜单，由容器渲染）。 */
  actions: ReactNode
  /** 展开窥视插槽（分页成员列表，由容器渲染含取数的容器）；仅在展开时调用。 */
  renderPeek?: () => ReactNode
}

export function BotGroupRow({
  group,
  checked,
  onCheck,
  expanded,
  onToggleExpand,
  actions,
  renderPeek,
}: BotGroupRowProps) {
  const { t } = useTranslation()

  return (
    <>
      <TableRow>
        <TableCell>
          <Checkbox checked={checked} onCheckedChange={onCheck} aria-label={t('bots.select')} />
        </TableCell>
        <TableCell>
          <button
            type="button"
            onClick={onToggleExpand}
            className="flex items-center gap-1.5 text-left font-medium hover:underline"
          >
            <span className="text-muted-foreground">{expanded ? '▾' : '▸'}</span>
            <span className="truncate">{group.label || group.key}</span>
          </button>
        </TableCell>
        <TableCell>
          <BotHealthBar total={group.total} online={group.online} />
        </TableCell>
        <TableCell className="text-right tabular-nums">{group.total}</TableCell>
        <TableCell>
          <div className="flex items-center justify-end gap-1">{actions}</div>
        </TableCell>
      </TableRow>
      {expanded && renderPeek && (
        <TableRow className="bg-muted/30 hover:bg-muted/30">
          <TableCell colSpan={5} className="p-0">
            {renderPeek()}
          </TableCell>
        </TableRow>
      )}
    </>
  )
}

/**
 * 展开窥视：仅展示该组当前页 Bot（分页，绝不全量铺开），用于核对组内成员。
 *
 * 受控边界：成员数据、加载态与页码全部经 props 注入——页码会触发重新取数，故由容器持有
 * （规则同页码与筛选）；翻页只上报意图（`onPage`），钳制与请求由容器负责。
 */
export interface BotGroupPeekProps {
  /** 本页成员；缺省按空列表渲染。 */
  items?: BotInfo[]
  /** 命中总数（分页信封口径，供总页数与「共 N 个」文案）。 */
  total?: number
  /** 取数中：显示加载文案（优先于空态）。 */
  isLoading?: boolean
  /** 当前页（1 起）。 */
  page: number
  /** 翻页上报（容器改页码并重新取数）；可用范围由容器按同一页大小钳制。 */
  onPage: (page: number) => void
  /** 打开某个 Bot 详情（容器注入弹窗目标）。 */
  onOpenBot: (id: number) => void
}

export function BotGroupPeek({ items, total, isLoading = false, page, onPage, onOpenBot }: BotGroupPeekProps) {
  const { t } = useTranslation()

  if (isLoading) {
    return <p className="px-4 py-3 text-sm text-muted-foreground">{t('common.loading')}</p>
  }
  const list = items ?? []
  const count = total ?? 0
  const totalPages = Math.max(1, Math.ceil(count / BOT_PEEK_PAGE_SIZE))

  if (list.length === 0) {
    return <p className="px-4 py-3 text-sm text-muted-foreground">{t('bots.empty')}</p>
  }

  return (
    <div className="px-4 py-3">
      <ul className="divide-y text-sm">
        {list.map((bot) => (
          <PeekRow key={bot.id} bot={bot} onOpen={() => onOpenBot(bot.id)} />
        ))}
      </ul>
      <div className="mt-2 flex items-center justify-between text-xs text-muted-foreground">
        <span>{t('bots.peekTotal', { total: count })}</span>
        <div className="flex items-center gap-2">
          <Button size="xs" variant="ghost" disabled={page <= 1} onClick={() => onPage(Math.max(1, page - 1))}>
            {t('bots.prevPage')}
          </Button>
          <span>{t('bots.pageOf', { page, totalPages })}</span>
          <Button
            size="xs"
            variant="ghost"
            disabled={page >= totalPages}
            onClick={() => onPage(Math.min(totalPages, page + 1))}
          >
            {t('bots.nextPage')}
          </Button>
        </div>
      </div>
    </div>
  )
}
