import { useTranslation } from 'react-i18next'
import { Input } from '@jianmanager/ui/components/input'
import { Segments, Segment } from '@jianmanager/ui/components/layout'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import { ViewToggle, type ViewMode } from '@jianmanager/ui/components/view-toggle'
import { BOT_STATUSES, GROUP_BY_DIMS } from '@/lib/bots/bots-overview'
import type { GroupByDim } from '@/lib/bots/bots-overview'

/** 节点筛选项（容器取数：个位/十位量级，无服务端搜索需求）。 */
export interface BotToolbarNode {
  id: number
  name: string
}

export interface BotToolbarProps {
  search: string
  onSearch: (v: string) => void
  nodeId: number | null
  onNode: (v: number | null) => void
  status: string
  onStatus: (v: string) => void
  groupBy: GroupByDim
  onGroupBy: (v: GroupByDim) => void
  view: ViewMode
  onView: (v: ViewMode) => void
  /** 可选节点列表（容器经 useNodes 取数）。 */
  nodes?: BotToolbarNode[]
}

/** Radix Select 不接受空字符串 value：用哨兵值表示「全部 / 不过滤」。 */
const SENTINEL_ALL = 'all'
/** 工具栏：搜索 + 节点筛选 + 状态筛选 + 分组维度切换 + 卡/列表视图切换。 */
export function BotToolbar({
  search,
  onSearch,
  nodeId,
  onNode,
  status,
  onStatus,
  groupBy,
  onGroupBy,
  view,
  onView,
  nodes,
}: BotToolbarProps) {
  const { t } = useTranslation()

  return (
    <div className="mb-3 flex flex-wrap items-center gap-2">
      {/* 三个筛选控件原先都没有可访问名（只有占位符）；名字落在可聚焦的控件本体上。 */}
      <Input
        value={search}
        onChange={(e) => onSearch(e.target.value)}
        placeholder={t('bots.searchPlaceholder')}
        aria-label={t('bots.searchPlaceholder')}
        className="h-9 w-56"
      />
      <Select
        value={nodeId === null ? SENTINEL_ALL : String(nodeId)}
        onValueChange={(v: string) => onNode(v === SENTINEL_ALL ? null : Number(v))}
      >
        <SelectTrigger size="sm" className="w-40" aria-label={t('bots.allNodes')}>
          <SelectValue placeholder={t('bots.allNodes')} />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={SENTINEL_ALL}>{t('bots.allNodes')}</SelectItem>
          {nodes?.map((node) => (
            <SelectItem key={node.id} value={String(node.id)}>
              {node.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Select
        value={status === '' ? SENTINEL_ALL : status}
        onValueChange={(v: string) => onStatus(v === SENTINEL_ALL ? '' : v)}
      >
        <SelectTrigger size="sm" className="w-36" aria-label={t('bots.allStatus')}>
          <SelectValue placeholder={t('bots.allStatus')} />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={SENTINEL_ALL}>{t('bots.allStatus')}</SelectItem>
          {BOT_STATUSES.map((s) => (
            <SelectItem key={s} value={s}>
              {t(`bots.status_${s}`)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>

      <div className="ml-auto flex items-center gap-2">
        {/* 分组维度收敛到布局层分段原语：凹槽轨道 + 选中块由 `Segments` 一处决定。
            维度标题移到轨道**外部**——`Segments` 是 `role="tablist"`，把纯文本留在轨道里会
            成为 tablist 的非 tab 子节点（无效 ARIA 结构）。 */}
        <span className="px-2 text-xs text-muted-foreground">{t('bots.groupBy')}</span>
        <Segments aria-label={t('bots.groupBy')}>
          {GROUP_BY_DIMS.map((dim) => (
            <Segment key={dim} active={groupBy === dim} onClick={() => onGroupBy(dim)}>
              {t(`bots.groupDim_${dim}`)}
            </Segment>
          ))}
        </Segments>
        <ViewToggle
          value={view}
          onChange={onView}
          cardLabel={t('grouping.viewCard')}
          listLabel={t('grouping.viewList')}
        />
      </div>
    </div>
  )
}
