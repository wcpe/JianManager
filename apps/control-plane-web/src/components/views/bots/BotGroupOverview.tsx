import { Fragment, useMemo, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Table,
  TableBody,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import type { ViewMode } from '@jianmanager/ui/components/view-toggle'
import type { BotSummaryGroup } from '@jianmanager/ui/lib/bot'
import type { GroupByDim } from '@jianmanager/ui/lib/bots-overview'

/** 组卡片/行的公共参数：选中与展开状态由本组件持有后下发。 */
export interface BotGroupItemArgs {
  group: BotSummaryGroup
  checked: boolean
  onCheck: () => void
  expanded: boolean
  onToggleExpand: () => void
}

export interface BotGroupOverviewProps {
  groupBy: GroupByDim
  groups: BotSummaryGroup[]
  loading: boolean
  view: ViewMode
  /** 批量条渲染插槽（应用侧注入 mutation 与权限；未选中任何组时不渲染）。 */
  renderBatchBar?: (args: { selectedGroups: BotSummaryGroup[]; onClear: () => void }) => ReactNode
  /** 卡片视图的组卡插槽（应用侧 BotWorktableCard，含操作区与展开窥视）。 */
  renderCard: (args: BotGroupItemArgs) => ReactNode
  /** 列表视图的组行插槽（应用侧 GroupRow）。 */
  renderRow: (args: BotGroupItemArgs) => ReactNode
}
/** 分组总览：每组一卡/行（含多段健康条 + 总数 + 批量 + 展开窥视 + 在控制台打开）。 */
export function BotGroupOverview({
  groupBy,
  groups,
  loading,
  view,
  renderBatchBar,
  renderCard,
  renderRow,
}: BotGroupOverviewProps) {
  const { t } = useTranslation()
  const [expanded, setExpanded] = useState<string | null>(null)
  const [selected, setSelected] = useState<Set<string>>(new Set())

  const selectedGroups = useMemo(
    () => groups.filter((g) => selected.has(g.key)),
    [groups, selected],
  )

  const toggle = (key: string) =>
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  const toggleExpand = (key: string) => setExpanded((cur) => (cur === key ? null : key))

  if (loading) {
    return <p className="text-muted-foreground">{t('common.loading')}</p>
  }

  return (
    <div className="space-y-3">
      {selectedGroups.length > 0 &&
        renderBatchBar?.({ selectedGroups, onClear: () => setSelected(new Set()) })}

      {groups.length === 0 ? (
        <p className="rounded-lg border py-10 text-center text-muted-foreground">{t('bots.empty')}</p>
      ) : view === 'card' ? (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
          {groups.map((group) => (
            <Fragment key={group.key}>
              {renderCard({
                group,
                checked: selected.has(group.key),
                onCheck: () => toggle(group.key),
                expanded: expanded === group.key,
                onToggleExpand: () => toggleExpand(group.key),
              })}
            </Fragment>
          ))}
        </div>
      ) : (
        <div className="rounded-lg border">
          <Table>
            <TableHeader className="bg-muted/50">
              <TableRow>
                <TableHead className="w-10" />
                <TableHead>{t(`bots.groupDim_${groupBy}`)}</TableHead>
                <TableHead className="w-[34%]">{t('bots.health')}</TableHead>
                <TableHead className="w-20 text-right">{t('bots.count')}</TableHead>
                <TableHead className="w-44 text-right">{t('bots.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {groups.map((group) => (
                <Fragment key={group.key}>
                  {renderRow({
                    group,
                    checked: selected.has(group.key),
                    onCheck: () => toggle(group.key),
                    expanded: expanded === group.key,
                    onToggleExpand: () => toggleExpand(group.key),
                  })}
                </Fragment>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  )
}
